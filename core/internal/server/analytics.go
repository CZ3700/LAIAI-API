package server

import (
	"net/http"
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
	now := s.now()
	sinceTime := now.AddDate(0, 0, -days+1)
	sinceDate := sinceTime.Format("2006-01-02")                                             // datetime 列阈值
	startOfDay := time.Date(sinceTime.Year(), sinceTime.Month(), sinceTime.Day(), 0, 0, 0, 0, sinceTime.Location())
	startUnix := startOfDay.Unix()                                                          // bigint 列阈值

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
