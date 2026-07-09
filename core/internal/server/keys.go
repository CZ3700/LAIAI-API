package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type createKeyReq struct {
	Name          string   `json:"name"`
	Scopes        string   `json:"scopes"`
	ExpiresInDays int      `json:"expires_in_days"` // 0 = 永不过期
	QuotaLimit    string   `json:"quota_limit"`     // 累计消费上限 USD，空/0=无限
	ModelLimits   []string `json:"model_limits"`    // 模型白名单，空=不限
	AllowIPs      string   `json:"allow_ips"`       // IP 白名单（逗号/换行），空=不限
	RPMLimit      int      `json:"rpm_limit"`       // key 级每分钟请求上限，0=不限（0041）
}

func (s *Server) createKey(c *gin.Context) {
	claims := mustClaims(c)
	var req createKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	var expires *time.Time
	if req.ExpiresInDays > 0 {
		t := s.now().AddDate(0, 0, req.ExpiresInDays)
		expires = &t
	}
	quota := decimal.Zero
	if req.QuotaLimit != "" {
		var err error
		if quota, err = decimal.NewFromString(req.QuotaLimit); err != nil || quota.IsNegative() {
			fail(c, http.StatusBadRequest, "quota_limit must be a non-negative decimal string")
			return
		}
	}
	if req.RPMLimit < 0 {
		fail(c, http.StatusBadRequest, "rpm_limit must be non-negative")
		return
	}
	var plaintext string
	var key *identity.APIKey
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ent, err := identity.LoadEntitlements(tx, claims.OrgID)
		if err != nil {
			return err
		}
		uid := claims.UserID
		plaintext, key, err = identity.IssueKey(tx, identity.IssueKeyInput{
			OrgID: claims.OrgID, UserID: &uid,
			Name: req.Name, Scopes: req.Scopes, ExpiresAt: expires,
			QuotaLimit:  quota,
			ModelLimits: joinTrim(req.ModelLimits),
			AllowIPs:    req.AllowIPs,
			RPMLimit:    req.RPMLimit,
			MaxKeys:     ent.MaxAPIKeys, // entitlements 上限（红线：原子在 IssueKey 内强制）
		})
		return err
	})
	if errors.Is(err, identity.ErrKeyLimit) {
		fail(c, http.StatusForbidden, "api key limit reached for this organization")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "key issue failed")
		return
	}
	// 明文仅此一次返回（红线 8）
	c.JSON(http.StatusCreated, gin.H{
		"id": key.ID, "key": plaintext, "prefix": key.Prefix, "name": key.Name,
	})
}

func (s *Server) listKeys(c *gin.Context) {
	claims := mustClaims(c)
	var keys []identity.APIKey
	// org_id 谓词强制（红线 5：org 隔离在 repository 层）
	if err := s.db.Where("org_id = ?", claims.OrgID).Order("id DESC").Find(&keys).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		out = append(out, gin.H{
			"id": k.ID, "prefix": k.Prefix, "name": k.Name,
			"scopes": k.Scopes, "status": k.Status, "expires_at": k.ExpiresAt,
			"quota_limit": k.QuotaLimit, "used_amount": k.UsedAmount,
			"model_limits": k.ModelLimits, "allow_ips": k.AllowIPs, "rpm_limit": k.RPMLimit,
		})
	}
	c.JSON(http.StatusOK, gin.H{"keys": out})
}

// joinTrim：去空白后逗号连接（空切片 → 空串 = 不限）。
func joinTrim(items []string) string {
	cleaned := make([]string, 0, len(items))
	for _, s := range items {
		if t := strings.TrimSpace(s); t != "" {
			cleaned = append(cleaned, t)
		}
	}
	return strings.Join(cleaned, ",")
}

func (s *Server) revokeKey(c *gin.Context) {
	claims := mustClaims(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	// 先校验归属（org 隔离），再吊销
	k := &identity.APIKey{}
	if err := s.db.Where("id = ? AND org_id = ?", id, claims.OrgID).First(k).Error; err != nil {
		fail(c, http.StatusNotFound, "key not found")
		return
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return identity.RevokeKey(tx, k.ID)
	}); err != nil {
		fail(c, http.StatusInternalServerError, "revoke failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": k.ID})
}

// deleteKey：DELETE /api/keys/:id/purge —— 物理删除（仅限已吊销的 key）。
// 活跃 key 必须先吊销再删，避免误删导致在用凭证瞬间失效（吊销是软态可审计）。
func (s *Server) deleteKey(c *gin.Context) {
	claims := mustClaims(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	k := &identity.APIKey{}
	if err := s.db.Where("id = ? AND org_id = ?", id, claims.OrgID).First(k).Error; err != nil {
		fail(c, http.StatusNotFound, "key not found")
		return
	}
	if k.Status == 1 {
		fail(c, http.StatusBadRequest, "revoke the key before deleting it")
		return
	}
	if err := s.db.Where("id = ? AND org_id = ?", id, claims.OrgID).Delete(&identity.APIKey{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "delete failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": k.ID})
}
