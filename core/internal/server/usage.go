package server

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
)

const (
	usageDefaultPageSize = 20
	usageMaxPageSize     = 100
	usageExportMaxRows   = 5000 // 自助导出上限：够日常对账，又不至于把内存/连接拖垮
)

// getUsage：GET /api/usage?page=&page_size=&model=
// 调用历史（Invocation Store 元数据）。org_id 谓词强制（红线 5）；
// 响应字段由 Invocation 的 json tag 控制——渠道归属/上游 id 等平台内部信息不外露。
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
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
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
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="usage.csv"`)
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"time", "request_id", "model", "status", "status_code", "stream",
		"prompt_tokens", "completion_tokens", "cost_usd", "frt_ms", "duration_ms"})
	for i := range rows {
		r := &rows[i]
		_ = w.Write([]string{
			r.CreatedAt.UTC().Format(time.RFC3339), csvSafe(r.RequestID), csvSafe(r.Model), csvSafe(r.Status),
			strconv.Itoa(r.StatusCode), strconv.FormatBool(r.IsStream),
			strconv.FormatInt(r.PromptTokens, 10), strconv.FormatInt(r.CompletionTokens, 10),
			r.CostUSD.String(), strconv.Itoa(r.FRTMs), strconv.Itoa(r.DurationMs),
		})
	}
	w.Flush()
}

// getUsageByModel：GET /api/usage/by-model?days=N —— 当前 org 近 N 天按模型聚合。
// 调用数/成功数/输入输出 token/费用，按调用量降序。仪表盘"模型分布"用。
func (s *Server) getUsageByModel(c *gin.Context) {
	claims := mustClaims(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if days < 1 || days > 90 {
		days = 14
	}
	since := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	var rows []struct {
		Model            string          `json:"model"`
		Calls            int64           `json:"calls"`
		Succeeded        int64           `json:"succeeded"`
		PromptTokens     int64           `json:"prompt_tokens"`
		CompletionTokens int64           `json:"completion_tokens"`
		CostUSD          decimal.Decimal `json:"cost_usd"`
	}
	err := s.readOnly().WithContext(c.Request.Context()).Raw(`
		SELECT model,
		       COUNT(*) AS calls,
		       SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS succeeded,
		       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		       COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM invocations
		WHERE org_id = ? AND created_at >= ?
		GROUP BY model
		ORDER BY calls DESC`, claims.OrgID, since).Scan(&rows).Error
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
	var rows []struct {
		APIKeyID         uint64          `json:"api_key_id"`
		Name             string          `json:"name"`
		Calls            int64           `json:"calls"`
		PromptTokens     int64           `json:"prompt_tokens"`
		CompletionTokens int64           `json:"completion_tokens"`
		CostUSD          decimal.Decimal `json:"cost_usd"`
	}
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

// getUsageSummary：GET /api/usage/summary?days=N —— 当前 org 近 N 天按日聚合
// （调用数/成功数/token/费用），供 Console 仪表盘图表。DATE() 在 MySQL 与 SQLite 同义。
func (s *Server) getUsageSummary(c *gin.Context) {
	claims := mustClaims(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if days < 1 || days > 90 {
		days = 14
	}
	since := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	var rows []struct {
		Day              string          `json:"day"`
		Calls            int64           `json:"calls"`
		Succeeded        int64           `json:"succeeded"`
		PromptTokens     int64           `json:"prompt_tokens"`
		CompletionTokens int64           `json:"completion_tokens"`
		CostUSD          decimal.Decimal `json:"cost_usd"`
	}
	err := s.readOnly().Raw(`
		SELECT DATE(created_at) AS day,
		       COUNT(*) AS calls,
		       SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS succeeded,
		       COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		       COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM invocations
		WHERE org_id = ? AND created_at >= ?
		GROUP BY DATE(created_at)
		ORDER BY day`, claims.OrgID, since).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "summary query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "items": rows})
}

// getUsageByModelDaily：GET /api/usage/by-model-daily?days=N —— 当前 org 近 N 天
// 按(日, 模型)聚合，供仪表盘「按模型分色」的堆叠柱状图。DATE() 两库同义。
func (s *Server) getUsageByModelDaily(c *gin.Context) {
	claims := mustClaims(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if days < 1 || days > 90 {
		days = 14
	}
	since := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	var rows []struct {
		Day     string          `json:"day"`
		Model   string          `json:"model"`
		Calls   int64           `json:"calls"`
		CostUSD decimal.Decimal `json:"cost_usd"`
	}
	err := s.readOnly().WithContext(c.Request.Context()).Raw(`
		SELECT DATE(created_at) AS day, model,
		       COUNT(*) AS calls, COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM invocations
		WHERE org_id = ? AND created_at >= ?
		GROUP BY DATE(created_at), model
		ORDER BY day`, claims.OrgID, since).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "by-model-daily query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "items": rows})
}

// getUsagePerf：GET /api/usage/perf —— 当前 org 实时性能（近 60 秒 RPM/TPM）。
// 仪表盘性能指标卡用。invocations.created_at 是 DATETIME(3)（autoCreateTime:milli
// 只设精度，列仍是 datetime），必须用时间串比较——不能用 epoch 整数（datetime 对
// int 比较会退化成字符串比较，等于统计全表）。MySQL 与 SQLite 同义。
func (s *Server) getUsagePerf(c *gin.Context) {
	claims := mustClaims(c)
	since := s.now().Add(-60 * time.Second).UTC().Format("2006-01-02 15:04:05") // 近 60 秒
	var row struct {
		Calls  int64 `json:"rpm"`
		Tokens int64 `json:"tpm"`
	}
	// 实时指标（近 60 秒）必须走主库：只读副本的复制延迟会让窗口过滤掉尚未复制到的最近行，
	// 在线却显示 0 RPM/0 TPM 或严重低估。其余可容延迟的用量聚合(getUsage/Export/Summary/ByModel*)才用 readOnly()。
	s.db.WithContext(c.Request.Context()).Raw(`
		SELECT COUNT(*) AS calls,
		       COALESCE(SUM(prompt_tokens + completion_tokens), 0) AS tokens
		FROM invocations WHERE org_id = ? AND created_at >= ?`,
		claims.OrgID, since).Scan(&row)
	c.JSON(http.StatusOK, gin.H{"rpm": row.Calls, "tpm": row.Tokens})
}
