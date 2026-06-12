package server

import (
	"net/http"

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
