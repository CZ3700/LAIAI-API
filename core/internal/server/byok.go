package server

// BYOK 自带上游 Key（0044，设计 §4.4；指令 §5 / roadmap A5）：org 自助管理自有上游渠道。
// 复用 channels 表（owner_org_id>0），adapter/信封加密/failover 机器全套免费获得。
// 安全：provider 显式白名单（relay.ByokProviderProfile）、base_url 强制 https + SSRF 校验
// （绝不 allow-private，严于 advanced_custom）、key 信封加密、列表只回显 SHA-256 指纹前 12、
// 每 org 上限 10 条、owner/admin 才可写、增删启停写审计（绝不记明文/密文）。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
)

const byokMaxChannels = 10

// byokRequireManager：BYOK 写操作需 owner/admin（读=成员即可）。
func (s *Server) byokRequireManager(c *gin.Context) (orgID uint64, ok bool) {
	claims := mustClaims(c)
	var m identity.OrgMember
	if err := s.db.Where("org_id = ? AND user_id = ?", claims.OrgID, claims.UserID).First(&m).Error; err != nil ||
		(m.Role != "owner" && m.Role != "admin") {
		fail(c, http.StatusForbidden, "owner or admin role required")
		return 0, false
	}
	return claims.OrgID, true
}

// listByokChannels：GET /api/org/byok-channels —— 本 org 自有渠道（脱敏：只指纹不密文）。
func (s *Server) listByokChannels(c *gin.Context) {
	claims := mustClaims(c)
	var chs []relay.Channel
	if err := s.db.Where("owner_org_id = ?", claims.OrgID).Order("id").Find(&chs).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]gin.H, 0, len(chs))
	for _, ch := range chs {
		out = append(out, gin.H{
			"id": ch.ID, "name": ch.Name, "provider": ch.Provider, "base_url": ch.BaseURL,
			"models": ch.Models, "status": ch.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"channels": out, "max": byokMaxChannels})
}

// createByokChannel：POST /api/org/byok-channels {provider, api_key, models[], base_url?, name?}。
func (s *Server) createByokChannel(c *gin.Context) {
	orgID, ok := s.byokRequireManager(c)
	if !ok {
		return
	}
	var req struct {
		Provider string   `json:"provider"`
		APIKey   string   `json:"api_key"`
		BaseURL  string   `json:"base_url"`
		Models   []string `json:"models"`
		Name     string   `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Provider == "" || req.APIKey == "" {
		fail(c, http.StatusBadRequest, "provider and api_key are required")
		return
	}
	defaultBase, allowed := relay.ByokProviderProfile(req.Provider)
	if !allowed {
		fail(c, http.StatusBadRequest, "provider not supported for BYOK")
		return
	}
	base := strings.TrimSpace(req.BaseURL)
	if base == "" {
		base = defaultBase
	}
	if base == "" {
		fail(c, http.StatusBadRequest, "base_url is required for this provider")
		return
	}
	// SSRF（设计 §4.4 安全①）：强制 https + 配置期校验（绝不 allow-private）；拨号期由
	// clientFor 对 owner_org_id>0 恒走守卫复检（防 DNS rebinding）。
	if u, err := url.Parse(base); err != nil || u.Scheme != "https" {
		fail(c, http.StatusBadRequest, "base_url must be https")
		return
	}
	if err := relay.ValidateAdvancedUpstreamURL(base, false); err != nil {
		fail(c, http.StatusBadRequest, "base_url rejected: "+err.Error())
		return
	}
	models := joinTrim(req.Models)
	if models == "" {
		fail(c, http.StatusBadRequest, "models list is required")
		return
	}
	var n int64
	if err := s.db.Model(&relay.Channel{}).Where("owner_org_id = ?", orgID).Count(&n).Error; err != nil || n >= byokMaxChannels {
		fail(c, http.StatusForbidden, fmt.Sprintf("byok channel limit reached (%d)", byokMaxChannels))
		return
	}
	enc, err := s.keyring.Encrypt(req.APIKey)
	if err != nil {
		fail(c, http.StatusInternalServerError, "credential encrypt failed")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "byok-" + req.Provider
	}
	ch := &relay.Channel{
		Name: name, Provider: req.Provider, BaseURL: base,
		CredentialEnc: enc, Models: models, Weight: 100, Priority: 0,
		OwnerOrgID: orgID, Status: 1,
	}
	if err := s.db.Create(ch).Error; err != nil {
		fail(c, http.StatusInternalServerError, "create failed")
		return
	}
	s.invalidateCache(relay.KindChannel, "")
	sum := sha256.Sum256([]byte(req.APIKey))
	s.writeAudit(c, "byok_channel.create",
		fmt.Sprintf("channel:%d", ch.ID),
		fmt.Sprintf("org=%d provider=%s models=%d fp=%s", orgID, req.Provider, len(req.Models), hex.EncodeToString(sum[:])[:12]))
	c.JSON(http.StatusCreated, gin.H{"id": ch.ID, "name": ch.Name, "provider": ch.Provider, "base_url": base, "models": models, "status": ch.Status})
}

// byokChannelByID：归属校验（org 隔离红线：只能动自己的）。
func (s *Server) byokChannelByID(c *gin.Context, orgID uint64) (*relay.Channel, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return nil, false
	}
	ch := &relay.Channel{}
	if err := s.db.Where("id = ? AND owner_org_id = ?", id, orgID).First(ch).Error; err != nil {
		fail(c, http.StatusNotFound, "channel not found")
		return nil, false
	}
	return ch, true
}

// patchByokChannel：PATCH /api/org/byok-channels/:id {status} —— 启停。
func (s *Server) patchByokChannel(c *gin.Context) {
	orgID, ok := s.byokRequireManager(c)
	if !ok {
		return
	}
	ch, ok := s.byokChannelByID(c, orgID)
	if !ok {
		return
	}
	var req struct {
		Status *int8 `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Status == nil || (*req.Status != 0 && *req.Status != 1) {
		fail(c, http.StatusBadRequest, "status must be 0 or 1")
		return
	}
	if err := s.db.Model(&relay.Channel{}).Where("id = ? AND owner_org_id = ?", ch.ID, orgID).
		Update("status", *req.Status).Error; err != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	s.invalidateCache(relay.KindChannel, "")
	s.writeAudit(c, "byok_channel.status", fmt.Sprintf("channel:%d", ch.ID), fmt.Sprintf("org=%d status=%d", orgID, *req.Status))
	c.JSON(http.StatusOK, gin.H{"id": ch.ID, "status": *req.Status})
}

// deleteByokChannel：DELETE /api/org/byok-channels/:id —— 物理删除（BYOK 不被 task 引用，v1 无悬挂）。
func (s *Server) deleteByokChannel(c *gin.Context) {
	orgID, ok := s.byokRequireManager(c)
	if !ok {
		return
	}
	ch, ok := s.byokChannelByID(c, orgID)
	if !ok {
		return
	}
	if err := s.db.Where("id = ? AND owner_org_id = ?", ch.ID, orgID).Delete(&relay.Channel{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "delete failed")
		return
	}
	s.invalidateCache(relay.KindChannel, "")
	s.writeAudit(c, "byok_channel.delete", fmt.Sprintf("channel:%d", ch.ID), fmt.Sprintf("org=%d provider=%s", orgID, ch.Provider))
	c.JSON(http.StatusOK, gin.H{"deleted": ch.ID})
}
