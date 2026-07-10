package server

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// adminAnalytics：GET /api/admin/analytics?days=N —— 运营颗粒度总览。
// 一次性回三类经营参数：资金（真金入账/消耗/沉淀负债/支付转化）、
// 用户（总量/新增/活跃组织/ARPU）、质量（时延/错误分布/厂商分布），外加
// 日序列（调用/消耗/入账）供前端 sparkline。
//
// 时间口径要点（两套时间类型，别混）：
//   - invocations.created_at / users.created_at 是 DATETIME(3) → 用日期串比较（DATE() 两库同义）
//   - topup_orders.created_at / paid_at 是 BIGINT(unix 秒) → 用整数比较，按日分桶在 Go 里做
//     （FROM_UNIXTIME / strftime 在 MySQL 与 SQLite 不通用，挪到应用层保证可移植 + 可测）
func (s *Server) adminAnalytics(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if days < 1 || days > 90 {
		days = 14
	}
	now := s.now().UTC() // 与 DATETIME 列的 UTC 存储口径一致（DSN loc=UTC），避免跨时区窗口偏移
	sinceTime := now.AddDate(0, 0, -days+1)
	sinceDate := sinceTime.Format("2006-01-02") // datetime 列阈值
	startOfDay := time.Date(sinceTime.Year(), sinceTime.Month(), sinceTime.Day(), 0, 0, 0, 0, sinceTime.Location())
	startUnix := startOfDay.Unix() // bigint 列阈值

	// —— 资金：真金入账（已支付充值，按 amount_usd 入账口径） ——
	var cashIn decimal.Decimal
	rdb.Model(&TopupOrder{}).Where("status = ? AND paid_at >= ?", topupPaid, startUnix).
		Select("COALESCE(SUM(amount_usd), 0)").Scan(&cashIn)

	// 消耗（确认收入）：窗口内 relay 计费总额
	var consumption decimal.Decimal
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", sinceDate).
		Select("COALESCE(SUM(cost_usd), 0)").Scan(&consumption)

	// 沉淀资金（客户余额负债）：全部 user_credit 账户已入余额之和——资金红线监控点
	var float decimal.Decimal
	rdb.Model(&ledger.Account{}).Where("type = ?", ledger.AccountUserCredit).
		Select("COALESCE(SUM(posted_balance), 0)").Scan(&float)

	// 充值订单状态分布 + 转化率（窗口内创建口径）
	type statusCount struct {
		Status int8
		N      int64
	}
	var topupStatus []statusCount
	rdb.Model(&TopupOrder{}).Where("created_at >= ?", startUnix).
		Select("status, COUNT(*) AS n").Group("status").Scan(&topupStatus)
	var topupPaidN, topupPendingN, topupFailedN, topupTotal int64
	for _, sc := range topupStatus {
		topupTotal += sc.N
		switch sc.Status {
		case topupPaid:
			topupPaidN = sc.N
		case topupFailed:
			topupFailedN = sc.N
		case topupExpired:
			topupFailedN += sc.N // 过期归入未成交
		default:
			topupPendingN += sc.N
		}
	}
	conversion := 0.0
	if topupTotal > 0 {
		conversion = float64(topupPaidN) / float64(topupTotal)
	}

	// 按支付渠道的入账分布
	type provAmount struct {
		Provider  string          `json:"provider"`
		PaidCount int64           `json:"paid_count"`
		Amount    decimal.Decimal `json:"amount"`
	}
	var byProvider []provAmount
	rdb.Model(&TopupOrder{}).Where("status = ? AND paid_at >= ?", topupPaid, startUnix).
		Select("provider, COUNT(*) AS paid_count, COALESCE(SUM(amount_usd), 0) AS amount").
		Group("provider").Order("amount DESC").Scan(&byProvider)
	if byProvider == nil {
		byProvider = []provAmount{}
	}

	// —— 用户/组织 ——
	var totalUsers, newUsers, totalOrgs, activeOrgs int64
	rdb.Model(&identity.User{}).Count(&totalUsers)
	rdb.Model(&identity.User{}).Where("created_at >= ?", sinceDate).Count(&newUsers)
	rdb.Model(&identity.Organization{}).Count(&totalOrgs)
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", sinceDate).
		Distinct("org_id").Count(&activeOrgs)
	arpu := decimal.Zero
	if activeOrgs > 0 {
		arpu = consumption.Div(decimal.NewFromInt(activeOrgs)).Round(4)
	}

	// —— 质量：时延 + 错误分布 + 厂商分布 ——
	var qual struct {
		AvgDuration float64 `json:"avg_duration_ms"`
		AvgFRT      float64 `json:"avg_frt_ms"`
	}
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", sinceDate).
		Select("COALESCE(AVG(duration_ms), 0) AS avg_duration, COALESCE(AVG(frt_ms), 0) AS avg_frt").Scan(&qual)

	type statusBreak struct {
		Status string `json:"status"`
		N      int64  `json:"n"`
	}
	var errors []statusBreak
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", sinceDate).
		Select("status, COUNT(*) AS n").Group("status").Order("n DESC").Scan(&errors)
	if errors == nil {
		errors = []statusBreak{}
	}

	type provStat struct {
		Provider string          `json:"provider"`
		Calls    int64           `json:"calls"`
		Cost     decimal.Decimal `json:"cost"`
	}
	var providers []provStat
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", sinceDate).
		Select("provider, COUNT(*) AS calls, COALESCE(SUM(cost_usd), 0) AS cost").
		Group("provider").Order("calls DESC").Scan(&providers)
	if providers == nil {
		providers = []provStat{}
	}

	// —— Top 消费组织（窗口内，带组织名） ——
	type orgSpend struct {
		OrgID uint64          `json:"org_id"`
		Name  string          `json:"name"`
		Calls int64           `json:"calls"`
		Cost  decimal.Decimal `json:"cost"`
	}
	var topOrgs []orgSpend
	rdb.Raw(`SELECT i.org_id AS org_id, o.name AS name,
	                COUNT(*) AS calls, COALESCE(SUM(i.cost_usd), 0) AS cost
	         FROM invocations i LEFT JOIN organizations o ON o.id = i.org_id
	         WHERE i.created_at >= ?
	         GROUP BY i.org_id, o.name ORDER BY cost DESC LIMIT 10`, sinceDate).Scan(&topOrgs)
	if topOrgs == nil {
		topOrgs = []orgSpend{}
	}

	// —— 日序列：调用/消耗（来自 invocations，DATE() 可移植） ——
	type dayAgg struct {
		Day   string          `json:"day"`
		Calls int64           `json:"calls"`
		Cost  decimal.Decimal `json:"cost"`
	}
	var callRows []dayAgg
	rdb.Raw(`SELECT DATE(created_at) AS day, COUNT(*) AS calls, COALESCE(SUM(cost_usd), 0) AS cost
	         FROM invocations WHERE created_at >= ? GROUP BY DATE(created_at) ORDER BY day`, sinceDate).Scan(&callRows)
	callsByDay := map[string]dayAgg{}
	for _, r := range callRows {
		callsByDay[r.Day] = r
	}

	// 入账日序列：在 Go 里把 unix 秒按本地日分桶（避开 DB 时间函数方言）
	var paidRows []struct {
		PaidAt    int64
		AmountUSD decimal.Decimal
	}
	rdb.Model(&TopupOrder{}).Where("status = ? AND paid_at >= ?", topupPaid, startUnix).
		Select("paid_at, amount_usd").Scan(&paidRows)
	cashByDay := map[string]decimal.Decimal{}
	for _, r := range paidRows {
		day := time.Unix(r.PaidAt, 0).In(now.Location()).Format("2006-01-02")
		cashByDay[day] = cashByDay[day].Add(r.AmountUSD)
	}

	// 连续日序列（补零），保证 sparkline 等距
	type seriesPoint struct {
		Day    string          `json:"day"`
		Calls  int64           `json:"calls"`
		Cost   decimal.Decimal `json:"cost"`
		CashIn decimal.Decimal `json:"cash_in"`
	}
	series := make([]seriesPoint, 0, days)
	for i := 0; i < days; i++ {
		day := startOfDay.AddDate(0, 0, i).Format("2006-01-02")
		p := seriesPoint{Day: day, Cost: decimal.Zero, CashIn: decimal.Zero}
		if r, ok := callsByDay[day]; ok {
			p.Calls, p.Cost = r.Calls, r.Cost
		}
		if amt, ok := cashByDay[day]; ok {
			p.CashIn = amt
		}
		series = append(series, p)
	}

	c.JSON(http.StatusOK, gin.H{
		"days": days,
		"finance": gin.H{
			"cash_in":          cashIn,
			"consumption":      consumption,
			"float":            float,
			"topup_paid":       topupPaidN,
			"topup_pending":    topupPendingN,
			"topup_failed":     topupFailedN,
			"topup_conversion": conversion,
			"by_provider":      byProvider,
		},
		"users": gin.H{
			"total_users": totalUsers,
			"new_users":   newUsers,
			"total_orgs":  totalOrgs,
			"active_orgs": activeOrgs,
			"arpu":        arpu,
		},
		"quality": gin.H{
			"avg_duration_ms": qual.AvgDuration,
			"avg_frt_ms":      qual.AvgFRT,
			"errors":          errors,
			"by_provider":     providers,
		},
		"top_orgs": topOrgs,
		"series":   series,
	})
}

// adminChannelHealth：GET /api/admin/channel-health?days=N —— 渠道健康看板（M2，2026-06-14）。
// 从 Invocation Store 派生每渠道窗口健康：成功率/平均时延/平均首字节(frt)/近1h错误/最近活跃。
// LEFT JOIN 含零流量的渠道（运营要看到闲置渠道）。**只读可观测，不反馈选路**（设计 §5.4）。
// 口径：invocations 每请求一行、channel_id=终态命中渠道，故被重试绕过的中途失败不计入该渠道。
func (s *Server) adminChannelHealth(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days < 1 || days > 90 {
		days = 7
	}
	now := s.now().UTC() // 与 DATETIME 列 UTC 存储口径一致
	since := now.AddDate(0, 0, -days+1).Format("2006-01-02")
	hourAgo := now.Add(-time.Hour).Format("2006-01-02 15:04:05") // 近1h错误用全时间戳串比较（两库通用）

	type chRow struct {
		ID           uint64  `json:"id"`
		Name         string  `json:"name"`
		Provider     string  `json:"provider"`
		Status       int8    `json:"status"`
		Calls        int64   `json:"calls"`
		Succeeded    int64   `json:"succeeded"`
		Errors1h     int64   `json:"errors_1h"`
		AvgLatencyMs float64 `json:"avg_latency_ms"`
		AvgFrtMs     float64 `json:"avg_frt_ms"`
		LastActive   string  `json:"last_active"`
		Failed       int64   `json:"failed"`       // Go 派生
		SuccessRate  float64 `json:"success_rate"` // Go 派生（避免 SQL 浮点除法两库差异）
	}
	var rows []chRow
	rdb.Raw(`
		SELECT c.id, c.name, c.provider, c.status,
		       COUNT(i.id) AS calls,
		       COALESCE(SUM(CASE WHEN i.status = 'success' THEN 1 ELSE 0 END), 0) AS succeeded,
		       COALESCE(SUM(CASE WHEN i.status <> 'success' AND i.created_at >= ? THEN 1 ELSE 0 END), 0) AS errors1h,
		       COALESCE(AVG(i.duration_ms), 0) AS avg_latency_ms,
		       COALESCE(AVG(i.frt_ms), 0) AS avg_frt_ms,
		       COALESCE(MAX(i.created_at), '') AS last_active
		FROM channels c
		LEFT JOIN invocations i ON i.channel_id = c.id AND i.created_at >= ?
		GROUP BY c.id, c.name, c.provider, c.status
		ORDER BY c.status DESC, calls DESC, c.id`, hourAgo, since).Scan(&rows)

	for i := range rows {
		rows[i].Failed = rows[i].Calls - rows[i].Succeeded
		rows[i].SuccessRate = 1.0 // 零流量默认满分（避免新渠道被误判为不健康）
		if rows[i].Calls > 0 {
			rows[i].SuccessRate = float64(rows[i].Succeeded) / float64(rows[i].Calls)
		}
	}
	if rows == nil {
		rows = []chRow{}
	}
	c.JSON(http.StatusOK, gin.H{"window_days": days, "channels": rows})
}

// adminModelChannelStats：GET /api/admin/model-channel-stats?hours=N ——
// (model×channel) 交叉维度看板（指令 §5 health dashboard；设计 §4.4 策略化选路 A3 的运营侧）。
// 每 (model, channel) 的 calls/成功率/p50/p95/avg_frt——admin 低频现算不缓存；
// 采样上限 5 万行（sampled 标记），行数上限 300（按 calls 降序截断）。
// 与 relay 进程内 chanStats（1h 窗口、喂路由策略）同口径不同窗口——这里是运营回看。
func (s *Server) adminModelChannelStats(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if hours < 1 || hours > 168 {
		hours = 24
	}
	const maxRows, maxOut = 50000, 300
	since := s.now().UTC().Add(-time.Duration(hours) * time.Hour).Format("2006-01-02 15:04:05")

	var raw []struct {
		Model      string `gorm:"column:model"`
		ChannelID  uint64 `gorm:"column:channel_id"`
		Status     string `gorm:"column:status"`
		DurationMs int    `gorm:"column:duration_ms"`
		FRTMs      int    `gorm:"column:frt_ms"`
	}
	if err := rdb.Table("invocations").
		Select("model, channel_id, status, duration_ms, frt_ms").
		Where("created_at >= ? AND channel_id > 0", since).
		Order("id DESC").Limit(maxRows).Find(&raw).Error; err != nil {
		fail(c, http.StatusInternalServerError, "stats query failed")
		return
	}

	type key struct {
		model string
		ch    uint64
	}
	type acc struct {
		calls, succeeded int
		durs             []int
		frtSum           int64
	}
	accs := map[key]*acc{}
	for i := range raw {
		k := key{raw[i].Model, raw[i].ChannelID}
		a := accs[k]
		if a == nil {
			a = &acc{}
			accs[k] = a
		}
		a.calls++
		if raw[i].Status == "success" {
			a.succeeded++
			if raw[i].DurationMs > 0 {
				a.durs = append(a.durs, raw[i].DurationMs)
			}
			a.frtSum += int64(raw[i].FRTMs)
		}
	}

	// 渠道名映射（含已删渠道回退 #id）
	var chs []struct {
		ID   uint64 `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	rdb.Table("channels").Select("id, name").Find(&chs)
	names := make(map[uint64]string, len(chs))
	for _, ch := range chs {
		names[ch.ID] = ch.Name
	}

	type outRow struct {
		Model       string  `json:"model"`
		ChannelID   uint64  `json:"channel_id"`
		ChannelName string  `json:"channel_name"`
		Calls       int     `json:"calls"`
		SuccessRate float64 `json:"success_rate"`
		P50         int     `json:"p50"`
		P95         int     `json:"p95"`
		AvgFrtMs    int     `json:"avg_frt_ms"`
	}
	out := make([]outRow, 0, len(accs))
	for k, a := range accs {
		p50, p95, _ := percentiles(a.durs)
		row := outRow{
			Model: k.model, ChannelID: k.ch, ChannelName: names[k.ch],
			Calls: a.calls, P50: p50, P95: p95,
		}
		if row.ChannelName == "" {
			row.ChannelName = fmt.Sprintf("#%d", k.ch)
		}
		if a.calls > 0 {
			row.SuccessRate = float64(a.succeeded) / float64(a.calls)
		}
		if a.succeeded > 0 {
			row.AvgFrtMs = int(a.frtSum / int64(a.succeeded))
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].ChannelID < out[j].ChannelID
	})
	truncated := false
	if len(out) > maxOut {
		out, truncated = out[:maxOut], true
	}
	c.JSON(http.StatusOK, gin.H{
		"window_hours": hours, "rows": out,
		"sampled": len(raw) == maxRows, "truncated": truncated,
	})
}
