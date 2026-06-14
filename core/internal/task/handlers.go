package task

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"gorm.io/gorm"
)

// HTTP 层：POST /v1/video/generations（提交）+ GET /v1/video/generations/:id（查询）。
// 与 relay 同款 API Key 认证；org 隔离在 Get 的查询谓词强制。

type createReq struct {
	Model   string `json:"model" binding:"required"`
	Content []any  `json:"content" binding:"required"`
}

func (s *Service) HandleCreate(c *gin.Context) {
	key, ok := s.authKey(c)
	if !ok {
		return
	}
	// Entitlements 功能门（P0-2）：超管可按 org 关闭视频生成。
	if ent, eerr := identity.LoadEntitlements(s.DB, key.OrgID); eerr == nil && !ent.FeatureVideo {
		taskErr(c, http.StatusForbidden, "video generation is not enabled for this organization")
		return
	}
	var req createReq
	if err := c.ShouldBindJSON(&req); err != nil {
		taskErr(c, http.StatusBadRequest, "model and content are required")
		return
	}
	t, err := s.Submit(c.Request.Context(), SubmitInput{
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		Model: req.Model, Content: req.Content,
	})
	switch {
	case errors.Is(err, relay.ErrModelNotPriced) || errors.Is(err, relay.ErrNoChannel):
		taskErr(c, http.StatusBadRequest, "model not available")
	case errors.Is(err, ErrConcurrencyLimit):
		c.Header("Retry-After", "10")
		taskErr(c, http.StatusTooManyRequests, "concurrency limit reached, retry later")
	case errors.Is(err, ledger.ErrInsufficientBalance):
		taskErr(c, http.StatusPaymentRequired, "insufficient balance")
	case err != nil:
		s.Log.Error("task submit failed", "err", err)
		taskErr(c, http.StatusBadGateway, "task submit failed")
	default:
		c.JSON(http.StatusAccepted, t)
	}
}

func (s *Service) HandleGet(c *gin.Context) {
	key, ok := s.authKey(c)
	if !ok {
		return
	}
	t, err := s.Get(key.OrgID, c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			taskErr(c, http.StatusNotFound, "task not found")
			return
		}
		taskErr(c, http.StatusInternalServerError, "task query failed")
		return
	}
	c.JSON(http.StatusOK, t)
}

func (s *Service) authKey(c *gin.Context) (*identity.APIKey, bool) {
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		taskErr(c, http.StatusUnauthorized, "missing api key")
		return nil, false
	}
	key, err := identity.VerifyKey(s.DB, strings.TrimPrefix(auth, "Bearer "), s.Now())
	if err != nil {
		taskErr(c, http.StatusUnauthorized, "invalid api key")
		return nil, false
	}
	return key, true
}

func taskErr(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": gin.H{"message": msg, "type": "laiai_task_error"}})
}
