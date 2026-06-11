package server

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 平台管理 API（M1 内部验证阶段）：X-Admin-Token 门禁（常数时间比较）。
// M3 Console 落地后升级为正式 root RBAC；资金类操作的审计 = journal 本身（kind=grant 全留痕）。

func (s *Server) adminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(s.adminToken) == 0 {
			fail(c, http.StatusNotFound, "admin api disabled") // 未配置 = 整组端点不存在
			return
		}
		got := c.GetHeader("X-Admin-Token")
		if subtle.ConstantTimeCompare([]byte(got), s.adminToken) != 1 {
			fail(c, http.StatusUnauthorized, "admin token invalid")
			return
		}
		c.Next()
	}
}

type grantReq struct {
	OrgID          uint64 `json:"org_id" binding:"required"`
	AmountUSD      string `json:"amount_usd" binding:"required"`
	Reason         string `json:"reason" binding:"required"`
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
}

// adminGrantCredit：授信入账（红线 2：幂等键由调用方提供，UNIQUE 兜底）。
func (s *Server) adminGrantCredit(c *gin.Context) {
	var req grantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "org_id, amount_usd, reason, idempotency_key required")
		return
	}
	amount, err := decimal.NewFromString(req.AmountUSD)
	if err != nil || !amount.IsPositive() {
		fail(c, http.StatusBadRequest, "amount_usd must be a positive decimal string")
		return
	}
	var entry *ledger.JournalEntry
	var applied bool
	err = s.db.Transaction(func(tx *gorm.DB) error {
		acc, err := ledger.EnsureAccount(tx, req.OrgID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		entry, applied, err = ledger.Post(tx, ledger.PostInput{
			IdempotencyKey: "admin_grant:" + req.IdempotencyKey,
			AccountID:      acc.ID,
			Kind:           ledger.KindGrant,
			Delta:          amount,
			RefType:        "admin_grant",
			Reason:         req.Reason,
		})
		return err
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "grant failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"entry_id": entry.ID, "balance_after": entry.BalanceAfter, "already_applied": applied,
	})
}

type channelReq struct {
	Name     string   `json:"name" binding:"required"`
	Provider string   `json:"provider" binding:"required"`
	BaseURL  string   `json:"base_url" binding:"required"`
	APIKey   string   `json:"api_key" binding:"required"`
	Models   []string `json:"models" binding:"required"`
	Weight   int      `json:"weight"`
	Priority int      `json:"priority"` // 大者先用；重试降层
}

func (s *Server) adminCreateChannel(c *gin.Context) {
	if s.keyring == nil {
		fail(c, http.StatusServiceUnavailable, "LAIAI_DATA_KEYS not configured")
		return
	}
	var req channelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name, provider, base_url, api_key, models required")
		return
	}
	enc, err := s.keyring.Encrypt(req.APIKey)
	if err != nil {
		fail(c, http.StatusInternalServerError, "credential encrypt failed")
		return
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}
	ch := &relay.Channel{
		Name: req.Name, Provider: req.Provider,
		BaseURL: strings.TrimRight(req.BaseURL, "/"),
		CredentialEnc: enc, Models: strings.Join(req.Models, ","),
		Weight: weight, Priority: req.Priority, Status: 1,
	}
	if err := s.db.Create(ch).Error; err != nil {
		fail(c, http.StatusInternalServerError, "channel create failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": ch.ID, "name": ch.Name})
}

func (s *Server) adminListChannels(c *gin.Context) {
	var channels []relay.Channel
	if err := s.db.Order("id").Find(&channels).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]gin.H, 0, len(channels))
	for _, ch := range channels {
		out = append(out, gin.H{ // 凭证永不回显，连密文都不给
			"id": ch.ID, "name": ch.Name, "provider": ch.Provider,
			"base_url": ch.BaseURL, "models": ch.Models,
			"weight": ch.Weight, "status": ch.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"channels": out})
}

// adminDisableChannel：禁用而非删除（调度池即时剔除；审计留痕）。
func (s *Server) adminDisableChannel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	res := s.db.Model(&relay.Channel{}).Where("id = ?", id).Update("status", 0)
	if res.Error != nil || res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"disabled": id})
}

type priceReq struct {
	Model          string `json:"model" binding:"required"`
	InputUSDPer1M  string `json:"input_usd_per_1m" binding:"required"`
	OutputUSDPer1M string `json:"output_usd_per_1m" binding:"required"`
}

// adminUpsertPrice：模型定价 upsert（uk_model_prices_model 兜底）。
func (s *Server) adminUpsertPrice(c *gin.Context) {
	var req priceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "model, input_usd_per_1m, output_usd_per_1m required")
		return
	}
	in, err1 := decimal.NewFromString(req.InputUSDPer1M)
	out, err2 := decimal.NewFromString(req.OutputUSDPer1M)
	if err1 != nil || err2 != nil || in.IsNegative() || out.IsNegative() {
		fail(c, http.StatusBadRequest, "prices must be non-negative decimal strings")
		return
	}
	p := &relay.ModelPrice{Model: req.Model, InputUSDPer1M: in, OutputUSDPer1M: out}
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{"input_usd_per_1m", "output_usd_per_1m"}),
	}).Create(p).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "price upsert failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"model": p.Model})
}

func (s *Server) adminListPrices(c *gin.Context) {
	var prices []relay.ModelPrice
	if err := s.db.Order("model").Find(&prices).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}
