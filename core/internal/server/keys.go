package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type createKeyReq struct {
	EnableVideoAsync bool            `json:"enable_video_async"`
	Name             string          `json:"name"`
	Scopes           string          `json:"scopes"`
	ExpiresInDays    int             `json:"expires_in_days"` // 0 = 永不过期
	QuotaLimit       string          `json:"quota_limit"`     // 累计消费上限 USD，空/0=无限
	ModelLimits      []string        `json:"model_limits"`    // 模型白名单，空=不限
	AllowIPs         string          `json:"allow_ips"`       // IP 白名单（逗号/换行），空=不限
	RPMLimit         int             `json:"rpm_limit"`       // key 级每分钟请求上限，0=不限（0041）
	RoutePolicy      string          `json:"route_policy"`    // key 级路由策略（0043）：''|order|latency|throughput|price
	ModelAccessMode  json.RawMessage `json:"model_access_mode"`
	RoutingGroupID   json.RawMessage `json:"routing_group_id"`
}

func (r createKeyReq) routing() (*string, *uint64, error) {
	var mode *string
	var group *uint64
	if len(r.ModelAccessMode) != 0 {
		var value string
		if bytes.Equal(bytes.TrimSpace(r.ModelAccessMode), []byte("null")) || json.Unmarshal(r.ModelAccessMode, &value) != nil {
			return nil, nil, identity.ErrModelAccessMode
		}
		mode = &value
	}
	if len(r.RoutingGroupID) != 0 && !bytes.Equal(bytes.TrimSpace(r.RoutingGroupID), []byte("null")) {
		var value uint64
		if json.Unmarshal(r.RoutingGroupID, &value) != nil {
			return nil, nil, identity.ErrRoutingGroup
		}
		group = &value
	}
	_, err := identity.ValidateKeyRouting(mode, group, joinTrim(r.ModelLimits))
	return mode, group, err
}

func (s *Server) createKey(c *gin.Context) {
	claims := mustClaims(c)
	var req createKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	mode, group, err := req.routing()
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, identity.ErrGroupRoutingDisabled) {
			status = http.StatusConflict
		}
		c.AbortWithStatusJSON(status, gin.H{"error": err.Error(), "code": err.Error()})
		return
	}
	requestID := rand.Text()
	c.Header("X-Request-ID", requestID)
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
	if !relay.ValidRoutePolicy(req.RoutePolicy) {
		fail(c, http.StatusBadRequest, "route_policy must be one of: order, latency, throughput, price")
		return
	}
	var plaintext string
	var key *identity.APIKey
	if req.EnableVideoAsync && !videoWriteScope(req.Scopes) {
		fail(c, 400, "enable_video_async requires video write scope")
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Serialize key admission and first-time policy creation with admin policy writes.
		var org identity.Organization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", claims.OrgID).Take(&org).Error; err != nil {
			return err
		}
		if err := identity.VerifyActiveMembership(tx, claims.UserID, claims.OrgID); err != nil {
			return err
		}
		ent, err := identity.LoadEntitlements(tx, claims.OrgID)
		if err != nil {
			return err
		}
		if videoWriteScope(req.Scopes) {
			if err := s.ensureKeyVideoAccess(tx, claims.OrgID, claims.UserID, req.EnableVideoAsync, ent); err != nil {
				return err
			}
		}
		uid := claims.UserID
		plaintext, key, err = identity.IssueKey(tx, identity.IssueKeyInput{
			OrgID: claims.OrgID, UserID: &uid,
			Name: req.Name, Scopes: req.Scopes, ExpiresAt: expires,
			QuotaLimit:      quota,
			ModelLimits:     joinTrim(req.ModelLimits),
			AllowIPs:        req.AllowIPs,
			RPMLimit:        req.RPMLimit,
			RoutePolicy:     req.RoutePolicy,
			MaxKeys:         ent.MaxAPIKeys, // entitlements 上限（红线：原子在 IssueKey 内强制）
			ModelAccessMode: mode, RoutingGroupID: group,
			PolicyAudit: routinggroup.Audit{ActorUserID: claims.UserID, RequestID: requestID},
		})
		return err
	})
	if replyKeyVideoError(c, err) {
		return
	}
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
			"route_policy":     k.RoutePolicy,
			"routing_group_id": k.RoutingGroupID, "model_access_mode": k.ModelAccessMode, "policy_version": k.PolicyVersion,
			"grouped_key_issuance_enabled": false,
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
		return identity.RevokeKeyWithAudit(tx, k.ID, claims.OrgID, routinggroup.Audit{ActorUserID: claims.UserID, RequestID: rand.Text()})
	}); err != nil {
		fail(c, http.StatusInternalServerError, "revoke failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": k.ID})
}

// deleteKey retains revoked Keys referenced by historical video billing.
// Revocation stops authentication; retention allows settlement and corrections.
func (s *Server) deleteKey(c *gin.Context) {
	claims := mustClaims(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	if id == 0 {
		fail(c, http.StatusNotFound, "key not found")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return identity.PurgeRevokedKeyTx(tx, claims.OrgID, id, videobilling.HasKeySnapshotReferenceTx)
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "key not found")
		return
	}
	if errors.Is(err, identity.ErrKeyPurgeActive) {
		fail(c, http.StatusBadRequest, "revoke the key before deleting it")
		return
	}
	if errors.Is(err, identity.ErrKeyPurgeReferenced) {
		fail(c, http.StatusConflict, "key is retained for video billing history; revocation remains effective")
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "key deletion unavailable")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}
