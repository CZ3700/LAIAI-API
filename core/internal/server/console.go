package server

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// getBalance：GET /api/balance —— 当前 org 余额/冻结（Console 仪表盘）。
// 只读，不 EnsureAccount（首笔授信前没有账户行是正常态，返回 0）。
func (s *Server) getBalance(c *gin.Context) {
	claims := mustClaims(c)
	acc := &ledger.Account{}
	err := s.db.WithContext(c.Request.Context()).
		Where("org_id = ? AND type = ?", claims.OrgID, ledger.AccountUserCredit).
		First(acc).Error
	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, gin.H{"balance": decimal.Zero, "held": decimal.Zero})
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "balance query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"balance": acc.PostedBalance, "held": acc.HeldAmount})
}

// getPricing：GET /api/pricing —— 公开模型价目（无需登录；营销站/模型市场页同源数据）。
// 与 /v1/models 同一可见性规则：已定价且有活跃渠道才列出（孤儿定价隐藏）。
func (s *Server) getPricing(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	var prices []relay.ModelPrice
	if err := rdb.Order("model").Find(&prices).Error; err != nil {
		fail(c, http.StatusInternalServerError, "pricing query failed")
		return
	}
	var channels []relay.Channel
	if err := rdb.Where("status = 1").Find(&channels).Error; err != nil {
		fail(c, http.StatusInternalServerError, "pricing query failed")
		return
	}
	servedBy := func(model string) (string, bool) {
		for i := range channels {
			if channels[i].ServesModel(model) {
				return channels[i].Provider, true
			}
		}
		return "", false
	}
	out := make([]gin.H, 0, len(prices))
	for _, p := range prices {
		provider, ok := servedBy(p.Model)
		if !ok {
			continue
		}
		out = append(out, gin.H{
			"model":             p.Model,
			"provider":          provider,
			"input_usd_per_1m":  p.InputUSDPer1M,
			"output_usd_per_1m": p.OutputUSDPer1M,
			"image_usd":         p.ImageUSD,
		})
	}
	c.JSON(http.StatusOK, gin.H{"models": out})
}

// getRankings：GET /api/rankings?days=N —— 全局模型/厂商调用量排行（公开，营销/SEO）。
// 跨所有 org 聚合（不泄露任何单 org 数据，只是总量）。默认 30 天。
func (s *Server) getRankings(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	days := 30
	if n, err := strconv.Atoi(c.Query("days")); err == nil && n >= 1 && n <= 90 {
		days = n
	}
	since := s.now().AddDate(0, 0, -days+1).Format("2006-01-02")

	var models []struct {
		Model  string `json:"model"`
		Calls  int64  `json:"calls"`
		Tokens int64  `json:"tokens"`
	}
	rdb.Raw(`SELECT model,
		COUNT(*) AS calls,
		COALESCE(SUM(prompt_tokens + completion_tokens), 0) AS tokens
		FROM invocations WHERE created_at >= ? AND status = 'success'
		GROUP BY model ORDER BY calls DESC LIMIT 20`, since).Scan(&models)

	var providers []struct {
		Provider string `json:"provider"`
		Calls    int64  `json:"calls"`
	}
	rdb.Raw(`SELECT provider,
		COUNT(*) AS calls
		FROM invocations WHERE created_at >= ? AND status = 'success' AND provider != ''
		GROUP BY provider ORDER BY calls DESC LIMIT 12`, since).Scan(&providers)

	if models == nil {
		models = []struct {
			Model  string `json:"model"`
			Calls  int64  `json:"calls"`
			Tokens int64  `json:"tokens"`
		}{}
	}
	if providers == nil {
		providers = []struct {
			Provider string `json:"provider"`
			Calls    int64  `json:"calls"`
		}{}
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "models": models, "providers": providers})
}
