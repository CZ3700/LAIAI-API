package server

import (
	"encoding/csv"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
)

const (
	usageDefaultPageSize = 20
	usageMaxPageSize     = 100
	usageExportMaxRows   = 5000  // 自助导出上限：够日常对账，又不至于把内存/连接拖垮
	usagePerfMaxRows     = 20000 // 性能分位最多取窗口内最近 N 行；超出则置 sampled=true，避免大 org 把内存/连接拖垮
)

// getUsage：GET /api/usage?page=&page_size=&model=
// 调用历史（Invocation Store 元数据）。org_id 谓词强制（红线 5）；
// 响应字段由 Invocation 的 json tag 控制——渠道归属（ChannelID/Provider）不外露；
// upstream_request_id 对请求所有者可见（排障闭环 0045：开发者找厂商开工单的唯一凭证，
// id 本身是不透明串，不泄露渠道归属）。
func (s *Server) getUsage(c *gin.Context) {
	claims := mustClaims(c)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(usageDefaultPageSize)))
	if pageSize < 1 || pageSize > usageMaxPageSize {
		pageSize = usageDefaultPageSize
	}

	q := s.readOnly().WithContext(c.Request.Context()).Model(&invocation.Invocation{}).Where("org_id = ?", claims.OrgID)
	if model := c.Query("model"); model != "" {
		q = q.Where("model = ?", model)
	}
	// modality：按调用模态筛（0040，图像日志页等）。值即列值精确匹配；历史行 modality='' 不会命中任何
	// 非空筛选——已知且可接受（迁移注记：不回填）。
	if modality := c.Query("modality"); modality != "" {
		q = q.Where("modality = ?", modality)
	}
	// request_id：精确匹配（uk_inv_request 唯一索引，工单锚点直查）。
	if reqID := c.Query("request_id"); reqID != "" {
		q = q.Where("request_id = ?", reqID)
	}
	// api_key_id：按 Key 维度筛调用（指令 0-14 天「Request Logs 补 API key filter」）。
	// org_id 谓词已在链上，跨 org 的 key id 只会得到空集，无越权面。
	if keyID := c.Query("api_key_id"); keyID != "" {
		id, err := strconv.ParseUint(keyID, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "api_key_id must be an integer")
			return
		}
		q = q.Where("api_key_id = ?", id)
	}
	// status：精确匹配真实枚举；特殊值 "failed" = 非 success（覆盖 upstream_error/no_channel/
	// insufficient_balance），让"失败"筛选在服务端完成——分页与 total 才能正确反映过滤结果，
	// 而不是前端只对当前页本地隐藏 success（会少计失败、total 也对不上）。
	if status := c.Query("status"); status != "" {
		if status == "failed" {
			q = q.Where("status <> ?", invocation.StatusSuccess)
		} else {
			q = q.Where("status = ?", status)
		}
	}
	// 时间窗（UTC）服务端过滤：since/until 为 YYYY-MM-DD 或 datetime 串（与 summary/by-model 同口径，
	// created_at 按 UTC 存储）。请求日志页"本月/上月/近 7 日/自定义"走此路径，避免对分页页本地按浏览器时区筛
	// （会漏算窗口内分页外的行、且边界日按本地月误分桶）。区间为左闭右开 [since, until)。
	if since := c.Query("since"); since != "" {
		q = q.Where("created_at >= ?", since)
	}
	if until := c.Query("until"); until != "" {
		q = q.Where("created_at < ?", until)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "usage query failed")
		return
	}
	var rows []invocation.Invocation
	if err := q.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "usage query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total": total, "page": page, "page_size": pageSize, "items": rows,
	})
}

// getUsageExport：GET /api/usage/export?days=N&model=&status= —— 当前 org 调用明细 CSV 自助下载。
// 与 getUsage 同 org 隔离口径（红线 5）；按 days 截最近 N 天（默认 30，1..365），上限 usageExportMaxRows。
// CSV 注入防护走 csvSafe（与 admin 导出一致）；只导出对外字段，不含渠道/上游 id 等平台内部信息。
func (s *Server) getUsageExport(c *gin.Context) {
	money, err := s.newExportMoney(c.Query("display_currency"))
	if err != nil {
		fail(c, 422, err.Error())
		return
	}
	claims := mustClaims(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 365 {
		days = 30
	}
	since := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	q := s.readOnly().WithContext(c.Request.Context()).Model(&invocation.Invocation{}).
		Where("org_id = ? AND created_at >= ?", claims.OrgID, since)
	if model := c.Query("model"); model != "" {
		q = q.Where("model = ?", model)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []invocation.Invocation
	if err := q.Order("id DESC").Limit(usageExportMaxRows).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "usage export query failed")
		return
	}
	money.headers(c, "usage")
	w := csv.NewWriter(c.Writer)
	_ = w.Write(money.costHeader([]string{"time", "request_id", "model", "status", "status_code", "stream",
		"prompt_tokens", "completion_tokens", "cost_usd", "frt_ms", "duration_ms"}))
	for i := range rows {
		r := &rows[i]
		_ = w.Write(money.costRow([]string{
			r.CreatedAt.UTC().Format(time.RFC3339), csvSafe(r.RequestID), csvSafe(r.Model), csvSafe(r.Status),
			strconv.Itoa(r.StatusCode), strconv.FormatBool(r.IsStream),
			strconv.FormatInt(r.PromptTokens, 10), strconv.FormatInt(r.CompletionTokens, 10),
			r.CostUSD.String(), strconv.Itoa(r.FRTMs), strconv.Itoa(r.DurationMs),
		}, r.CostUSD))
	}
	w.Flush()
}

// getUsageByModel：GET /api/usage/by-model?days=N&api_key_id= —— 当前 org 近 N 天按模型聚合。
// 调用数/成功数/输入输出 token/费用，按调用量降序。仪表盘"模型分布"用。
// api_key_id：可选按 Key 过滤（Usage 页 Key 维度；org 谓词已强制，跨 org key id 只得空集）。
func (s *Server) getUsageByModel(c *gin.Context) {
	claims := mustClaims(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if days < 1 || days > 90 {
		days = 14
	}
	since := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	args := []any{claims.OrgID, since}
	keyPred := ""
	if keyID := c.Query("api_key_id"); keyID != "" {
		id, err := strconv.ParseUint(keyID, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "api_key_id must be an integer")
			return
		}
		keyPred = " AND api_key_id = ?"
		args = append(args, id)
	}
	rows := []struct {
		Model            string          `json:"model"`
		Calls            int64           `json:"calls"`
		Succeeded        int64           `json:"succeeded"`
		PromptTokens     int64           `json:"prompt_tokens"`
		CompletionTokens int64           `json:"completion_tokens"`
		CostUSD          decimal.Decimal `json:"cost_usd"`
	}{}
	err := s.readOnly().WithContext(c.Request.Context()).Raw(`
		SELECT model,
		       COUNT(*) AS calls,
		       SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS succeeded,
		       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		       COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM invocations
		WHERE org_id = ? AND created_at >= ?`+keyPred+`
		GROUP BY model
		ORDER BY calls DESC`, args...).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "by-model query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "items": rows})
}

// getUsageByKey：GET /api/usage/by-key?days=N —— 当前 org 近 N 天按 API Key 聚合
// （调用数/输入输出 token/费用），仪表盘"密钥排行榜"用。LEFT JOIN api_keys 取名字（已删除的 key
// 名为空，前端回退 #id 展示）。org_id 过滤保证只见本组织 key。
func (s *Server) getUsageByKey(c *gin.Context) {
	claims := mustClaims(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 90 {
		days = 30
	}
	since := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	rows := []struct {
		APIKeyID         uint64          `json:"api_key_id"`
		Name             string          `json:"name"`
		Calls            int64           `json:"calls"`
		PromptTokens     int64           `json:"prompt_tokens"`
		CompletionTokens int64           `json:"completion_tokens"`
		CostUSD          decimal.Decimal `json:"cost_usd"`
	}{}
	err := s.readOnly().WithContext(c.Request.Context()).Raw(`
		SELECT i.api_key_id AS api_key_id,
		       COALESCE(MAX(k.name), '') AS name,
		       COUNT(*) AS calls,
		       COALESCE(SUM(i.prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(i.completion_tokens), 0) AS completion_tokens,
		       COALESCE(SUM(i.cost_usd), 0) AS cost_usd
		FROM invocations i
		LEFT JOIN api_keys k ON k.id = i.api_key_id
		WHERE i.org_id = ? AND i.created_at >= ?
		GROUP BY i.api_key_id
		ORDER BY calls DESC`, claims.OrgID, since).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "by-key query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "items": rows})
}

// getUsageSummary：GET /api/usage/summary?days=N&granularity=day|hour&api_key_id= —— 当前 org 聚合序列。
// day（默认）：近 N 天按日分桶，DATE() 在 MySQL 与 SQLite 同义，与既有行为逐字一致。
// hour：近 24 小时按小时分桶（days 参数忽略）——小时格式化两库无共同函数，按方言分支（服务端常量拼接，无注入面）。
// api_key_id：可选按 Key 过滤（org 谓词已强制，跨 org key id 只得空集）。
func (s *Server) getUsageSummary(c *gin.Context) {
	claims := mustClaims(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if days < 1 || days > 90 {
		days = 14
	}
	gran := c.DefaultQuery("granularity", "day")
	bucket := "DATE(created_at)"
	var since string
	if gran == "hour" {
		if s.readOnly().Dialector.Name() == "mysql" {
			bucket = "DATE_FORMAT(created_at, '%Y-%m-%d %H:00')"
		} else { // sqlite（测试内存库）
			bucket = "strftime('%Y-%m-%d %H:00', created_at)"
		}
		since = s.now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	} else {
		gran = "day" // 未知值静默回落（与 days 越界钳制同风格）
		since = s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	}
	args := []any{claims.OrgID, since}
	keyPred := ""
	if keyID := c.Query("api_key_id"); keyID != "" {
		id, err := strconv.ParseUint(keyID, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "api_key_id must be an integer")
			return
		}
		keyPred = " AND api_key_id = ?"
		args = append(args, id)
	}
	rows := []struct {
		Day              string          `json:"day"`
		Calls            int64           `json:"calls"`
		Succeeded        int64           `json:"succeeded"`
		PromptTokens     int64           `json:"prompt_tokens"`
		CompletionTokens int64           `json:"completion_tokens"`
		CostUSD          decimal.Decimal `json:"cost_usd"`
	}{}
	err := s.readOnly().Raw(`
		SELECT `+bucket+` AS day,
		       COUNT(*) AS calls,
		       SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS succeeded,
		       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		       COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM invocations
		WHERE org_id = ? AND created_at >= ?`+keyPred+`
		GROUP BY `+bucket+`
		ORDER BY day`, args...).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "summary query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "granularity": gran, "items": rows})
}

// percentiles：与前端原口径一致的分位（过滤 0/负值后升序，idx = ceil(p/100*n)-1，封顶 [0, n-1]）。
// 返回 p50/p95/p99（毫秒整数）。空样本返回全 0。
func percentiles(values []int) (p50, p95, p99 int) {
	f := make([]int, 0, len(values))
	for _, v := range values {
		if v > 0 {
			f = append(f, v)
		}
	}
	if len(f) == 0 {
		return 0, 0, 0
	}
	sort.Ints(f)
	at := func(p float64) int {
		idx := int(math.Ceil(p/100*float64(len(f)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx > len(f)-1 {
			idx = len(f) - 1
		}
		return f[idx]
	}
	return at(50), at(95), at(99)
}

// getUsagePerfStats：GET /api/usage/perf-stats?mins=N —— 当前 org 滚动时间窗的性能分位/成功率/时长分布，
// **服务端聚合**。取代前端对最近 100 行(page_size=100)本地算分位——那既被分页截断、时间窗选择器又形同虚设
// （7d/30d 与 24h 拿的是同一批 ≤100 行）。本端口在窗口全量上计算，与仪表盘成功率(SUM(success)/COUNT)同定义，
// 跨页一致。mins 默认 1440(近 24h)，上限 43200(30 天)。frt 取 frt_ms，缺则回退 duration_ms；时长桶取 duration_ms，
// 缺则回退 frt_ms——与前端原口径一致，保证迁移后数值含义不变。窗口内行数超 usagePerfMaxRows 时取最近样本并置
// sampled=true（前端据此提示"仅统计最近 N 条"，不谎称全量）。
func (s *Server) getUsagePerfStats(c *gin.Context) {
	claims := mustClaims(c)
	mins, _ := strconv.Atoi(c.DefaultQuery("mins", "1440"))
	if mins < 1 || mins > 43200 {
		mins = 1440
	}
	since := s.now().Add(-time.Duration(mins) * time.Minute).UTC().Format("2006-01-02 15:04:05")

	type prow struct {
		Model      string
		Status     string
		FRTMs      int `gorm:"column:frt_ms"`
		DurationMs int `gorm:"column:duration_ms"`
	}
	var rows []prow
	// 可容延迟，用只读副本；多取一行以判定是否触顶（sampled）。
	s.readOnly().WithContext(c.Request.Context()).Model(&invocation.Invocation{}).
		Select("model, status, frt_ms, duration_ms").
		Where("org_id = ? AND created_at >= ?", claims.OrgID, since).
		Order("id DESC").Limit(usagePerfMaxRows + 1).Scan(&rows)
	sampled := len(rows) > usagePerfMaxRows
	if sampled {
		rows = rows[:usagePerfMaxRows]
	}

	frtSample := func(r prow) int {
		if r.FRTMs > 0 {
			return r.FRTMs
		}
		return r.DurationMs
	}
	durSample := func(r prow) int {
		if r.DurationMs > 0 {
			return r.DurationMs
		}
		return r.FRTMs
	}

	bucketDefs := []struct {
		label    string
		min, max int
	}{
		{"<1s", 0, 1000},
		{"1-3s", 1000, 3000},
		{"3-10s", 3000, 10000},
		{">10s", 10000, math.MaxInt},
	}
	buckets := make([]int64, len(bucketDefs))

	type magg struct {
		calls, succeeded int64
		frts             []int
	}
	models := map[string]*magg{}
	var modelOrder []string

	frts := make([]int, 0, len(rows))
	durs := make([]int, 0, len(rows))
	var succeeded int64
	for i := range rows {
		r := rows[i]
		frts = append(frts, frtSample(r))
		d := durSample(r)
		durs = append(durs, d)
		ok := r.Status == invocation.StatusSuccess
		if ok {
			succeeded++
		}
		for bi := range bucketDefs {
			if d > bucketDefs[bi].min && d <= bucketDefs[bi].max {
				buckets[bi]++
				break
			}
		}
		m := models[r.Model]
		if m == nil {
			m = &magg{}
			models[r.Model] = m
			modelOrder = append(modelOrder, r.Model)
		}
		m.calls++
		if ok {
			m.succeeded++
		}
		m.frts = append(m.frts, frtSample(r))
	}

	calls := int64(len(rows))
	successRate := 1.0
	if calls > 0 {
		successRate = float64(succeeded) / float64(calls)
	}
	frtP50, frtP95, frtP99 := percentiles(frts)
	durP50, durP95, durP99 := percentiles(durs)

	bucketOut := make([]gin.H, len(bucketDefs))
	for i := range bucketDefs {
		bucketOut[i] = gin.H{"label": bucketDefs[i].label, "count": buckets[i]}
	}

	type modelStat struct {
		Model       string  `json:"model"`
		Calls       int64   `json:"calls"`
		Succeeded   int64   `json:"succeeded"`
		SuccessRate float64 `json:"success_rate"`
		P50         int     `json:"p50"`
		P95         int     `json:"p95"`
		P99         int     `json:"p99"`
	}
	modelOut := make([]modelStat, 0, len(modelOrder))
	for _, name := range modelOrder {
		m := models[name]
		p50, p95, p99 := percentiles(m.frts)
		sr := 1.0
		if m.calls > 0 {
			sr = float64(m.succeeded) / float64(m.calls)
		}
		modelOut = append(modelOut, modelStat{name, m.calls, m.succeeded, sr, p50, p95, p99})
	}
	sort.SliceStable(modelOut, func(i, j int) bool { return modelOut[i].Calls > modelOut[j].Calls })

	c.JSON(http.StatusOK, gin.H{
		"window_mins":  mins,
		"calls":        calls,
		"succeeded":    succeeded,
		"success_rate": successRate,
		"frt":          gin.H{"p50": frtP50, "p95": frtP95, "p99": frtP99},
		"dur":          gin.H{"p50": durP50, "p95": durP95, "p99": durP99},
		"buckets":      bucketOut,
		"models":       modelOut,
		"sampled":      sampled,
		"sample_size":  calls,
	})
}
