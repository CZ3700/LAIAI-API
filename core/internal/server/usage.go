package server

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
)

const (
	usageDefaultPageSize = 20
	usageMaxPageSize     = 100
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

	q := s.db.Model(&invocation.Invocation{}).Where("org_id = ?", claims.OrgID)
	if model := c.Query("model"); model != "" {
		q = q.Where("model = ?", model)
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

// getUsageSummary：GET /api/usage/summary?days=N —— 当前 org 近 N 天按日聚合
// （调用数/成功数/token/费用），供 Console 仪表盘图表。DATE() 在 MySQL 与 SQLite 同义。
func (s *Server) getUsageSummary(c *gin.Context) {
	claims := mustClaims(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "14"))
	if days < 1 || days > 90 {
		days = 14
	}
	since := s.now().AddDate(0, 0, -days+1).Format("2006-01-02")
	var rows []struct {
		Day              string          `json:"day"`
		Calls            int64           `json:"calls"`
		Succeeded        int64           `json:"succeeded"`
		PromptTokens     int64           `json:"prompt_tokens"`
		CompletionTokens int64           `json:"completion_tokens"`
		CostUSD          decimal.Decimal `json:"cost_usd"`
	}
	err := s.db.Raw(`
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
