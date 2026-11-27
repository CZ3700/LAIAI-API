package server

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
)

// wireTaskAssetConverter 将 ark 素材转换引擎挂载到 task.Service.Submit 前置阶段。
func (s *Server) wireTaskAssetConverter(ts *task.Service) {
	if ts == nil || ts.AssetConverter != nil {
		return
	}
	ts.AssetConverter = func(ctx context.Context, fwd *forward.ResolvedForward, orgID, userID uint64, body map[string]any) ([]string, error) {
		return ark.ConvertBodyAssets(ctx, s.db, s.arkHTTPClient, s.keyring, ark.ConvertRequestOptions{
			OrgID:                  orgID,
			UserID:                 userID,
			AssetConvert:           fwd.AssetConvert,
			AssetConvertNS:         fwd.AssetConvertNS,
			AssetModeration:        fwd.AssetModeration,
			UpstreamAssetConvert:   fwd.UpstreamAssetConvert,
			UpstreamAssetBindingID: fwd.UpstreamAssetBindingID,
			AllowPrivateFetch:      s.arkHTTPClient != nil,
			PollInterval:           s.arkPollOverride,
			OpenAIOverride:         s.arkOpenAIOverride,
		}, body)
	}
}

// SetArkTestHooks 仅用于 E2E 测试注入本地 mock HTTP 客户端、OpenAI base URL 与快速轮询步长。
func (s *Server) SetArkTestHooks(httpClient *http.Client, openAIOverride string, pollOverride time.Duration) {
	s.arkHTTPClient = httpClient
	s.arkOpenAIOverride = openAIOverride
	s.arkPollOverride = pollOverride
}

// SyncArkMonitor 供 cmd/core 定时任务或管理端手动触发火山方舟接入点监控与分账对账。
func (s *Server) SyncArkMonitor(ctx context.Context) (*ark.SyncSummary, error) {
	cnyPerUSD, _ := s.effectiveCNYPerUSD()
	return ark.SyncAllAccounts(ctx, s.db, s.keyring, cnyPerUSD, s.arkHTTPClient, s.arkOpenAIOverride)
}

// CleanupExpiredArkAssets 供 cmd/core 定时清理过期转换素材（含云端 DeleteAsset）。
func (s *Server) CleanupExpiredArkAssets(ctx context.Context) (int, error) {
	return ark.CleanupExpiredAssets(ctx, s.db, s.keyring, 168, s.arkHTTPClient)
}

func (s *Server) v1ArkAccountsList(c *gin.Context) {
	var rows []ark.Account
	if err := s.readOnly().WithContext(c.Request.Context()).Order("id DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query ark accounts failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

func (s *Server) v1ArkAccountsCreate(c *gin.Context) {
	var req struct {
		Name          string `json:"name" binding:"required"`
		VolcAccountID string `json:"volc_account_id"`
		AccessKey     string `json:"access_key" binding:"required"`
		SecretKey     string `json:"secret_key" binding:"required"`
		Region        string `json:"region"`
		Remark        string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name, access_key and secret_key are required")
		return
	}
	if s.keyring == nil {
		fail(c, http.StatusInternalServerError, "keyring not configured")
		return
	}
	skEnc, err := s.keyring.Encrypt(strings.TrimSpace(req.SecretKey))
	if err != nil {
		fail(c, http.StatusInternalServerError, "encrypt secret_key failed")
		return
	}
	row := ark.Account{
		Name:          strings.TrimSpace(req.Name),
		VolcAccountID: strings.TrimSpace(req.VolcAccountID),
		AccessKey:     strings.TrimSpace(req.AccessKey),
		SecretKeyEnc:  skEnc,
		SecretKeyMask: ark.MaskSecret(req.SecretKey),
		Region:        cmp.Or(strings.TrimSpace(req.Region), ark.DefaultVolcRegion),
		Remark:        strings.TrimSpace(req.Remark),
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		s.Log().Error("create ark account failed", "err", err)
		fail(c, http.StatusBadRequest, "create ark account failed")
		return
	}
	s.writeAudit(c, "ark_account_create", strconv.FormatUint(row.ID, 10), row.Name)
	c.JSON(http.StatusCreated, gin.H{"data": row})
}

func (s *Server) v1ArkAccountsUpdate(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var existing ark.Account
	if err := s.db.WithContext(c.Request.Context()).First(&existing, id).Error; err != nil {
		fail(c, http.StatusNotFound, "ark account not found")
		return
	}
	var req struct {
		Name          *string `json:"name"`
		VolcAccountID *string `json:"volc_account_id"`
		AccessKey     *string `json:"access_key"`
		SecretKey     *string `json:"secret_key"`
		Region        *string `json:"region"`
		Remark        *string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid JSON body")
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.VolcAccountID != nil {
		updates["volc_account_id"] = strings.TrimSpace(*req.VolcAccountID)
	}
	if req.AccessKey != nil && strings.TrimSpace(*req.AccessKey) != "" {
		updates["access_key"] = strings.TrimSpace(*req.AccessKey)
	}
	if req.SecretKey != nil && strings.TrimSpace(*req.SecretKey) != "" && s.keyring != nil {
		if enc, err := s.keyring.Encrypt(strings.TrimSpace(*req.SecretKey)); err == nil {
			updates["secret_key_enc"] = enc
			updates["secret_key_mask"] = ark.MaskSecret(*req.SecretKey)
		}
	}
	if req.Region != nil && strings.TrimSpace(*req.Region) != "" {
		updates["region"] = strings.TrimSpace(*req.Region)
	}
	if req.Remark != nil {
		updates["remark"] = strings.TrimSpace(*req.Remark)
	}
	if err := s.db.WithContext(c.Request.Context()).Model(&ark.Account{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		fail(c, http.StatusBadRequest, "update ark account failed")
		return
	}
	_ = s.db.WithContext(c.Request.Context()).First(&existing, id).Error
	s.writeAudit(c, "ark_account_update", strconv.FormatUint(id, 10), existing.Name)
	c.JSON(http.StatusOK, gin.H{"data": existing})
}

func (s *Server) v1ArkAccountsDelete(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	res := s.db.WithContext(c.Request.Context()).Delete(&ark.Account{}, id)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "delete ark account failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "ark account not found")
		return
	}
	s.writeAudit(c, "ark_account_delete", strconv.FormatUint(id, 10), "deleted")
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1ArkAccountsBalance(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var row ark.Account
	if err := s.db.WithContext(c.Request.Context()).First(&row, id).Error; err != nil {
		fail(c, http.StatusNotFound, "ark account not found")
		return
	}
	sk := row.SecretKeyEnc
	if s.keyring != nil && strings.HasPrefix(sk, "enc:v1:") {
		dec, err := s.keyring.Decrypt(sk)
		if err != nil {
			fail(c, http.StatusInternalServerError, "decrypt secret_key failed")
			return
		}
		sk = dec
	}
	cl := ark.NewClient(ark.ClientConfig{
		AccessKey:       row.AccessKey,
		SecretKey:       sk,
		Region:          row.Region,
		BaseURLOverride: s.arkOpenAIOverride,
	}, s.arkHTTPClient)
	info, err := cl.QueryBalanceDetail(c.Request.Context())
	if err != nil {
		s.Log().Warn("Ark upstream request failed")
		fail(c, http.StatusBadGateway, "Ark upstream request failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"configured": true, "balance": info, "currency": "CNY"})
}

func (s *Server) v1ArkBindingsList(c *gin.Context) {
	var rows []ark.EndpointBinding
	if err := s.readOnly().WithContext(c.Request.Context()).Order("id DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query bindings failed")
		return
	}
	type bindingView struct {
		ark.EndpointBinding
		OrgAvailableUSD decimal.Decimal `json:"org_available_usd"`
	}
	out := make([]bindingView, 0, len(rows))
	for _, r := range rows {
		avail, _ := ledger.OrgAvailable(s.db, r.OrgID, ledger.AccountUserCredit)
		out = append(out, bindingView{EndpointBinding: r, OrgAvailableUSD: avail})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (s *Server) v1ArkBindingsCreate(c *gin.Context) {
	var req struct {
		AccountID  uint64 `json:"account_id" binding:"required"`
		EndpointID string `json:"endpoint_id" binding:"required"`
		OrgID      uint64 `json:"org_id"`
		UserUID    string `json:"user_uid"`
		APIKey     string `json:"api_key"`
		Remark     string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "account_id and endpoint_id are required")
		return
	}
	orgID := req.OrgID
	if orgID == 0 {
		orgID = 1
	}
	var encKey, mask string
	if strings.TrimSpace(req.APIKey) != "" && s.keyring != nil {
		enc, err := s.keyring.Encrypt(strings.TrimSpace(req.APIKey))
		if err != nil {
			fail(c, http.StatusInternalServerError, "encrypt api_key failed")
			return
		}
		encKey = enc
		mask = ark.MaskSecret(req.APIKey)
	}
	row := ark.EndpointBinding{
		AccountID:  req.AccountID,
		EndpointID: strings.TrimSpace(req.EndpointID),
		OrgID:      orgID,
		UserID:     orgID,
		UserUID:    cmp.Or(strings.TrimSpace(req.UserUID), strconv.FormatUint(orgID, 10)),
		APIKeyEnc:  encKey,
		APIKeyMask: mask,
		Status:     1,
		Remark:     strings.TrimSpace(req.Remark),
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		s.Log().Error("create endpoint binding failed", "err", err)
		fail(c, http.StatusBadRequest, "create endpoint binding failed")
		return
	}
	s.writeAudit(c, "ark_binding_create", strconv.FormatUint(row.ID, 10), row.EndpointID)
	c.JSON(http.StatusCreated, gin.H{"data": row})
}

func (s *Server) v1ArkBindingsUpdate(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var existing ark.EndpointBinding
	if err := s.db.WithContext(c.Request.Context()).First(&existing, id).Error; err != nil {
		fail(c, http.StatusNotFound, "binding not found")
		return
	}
	var req struct {
		EndpointID *string `json:"endpoint_id"`
		OrgID      *uint64 `json:"org_id"`
		UserUID    *string `json:"user_uid"`
		APIKey     *string `json:"api_key"`
		Remark     *string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid JSON body")
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	rebound := false
	if req.EndpointID != nil && strings.TrimSpace(*req.EndpointID) != "" && strings.TrimSpace(*req.EndpointID) != existing.EndpointID {
		updates["endpoint_id"] = strings.TrimSpace(*req.EndpointID)
		rebound = true
	}
	if req.OrgID != nil && *req.OrgID > 0 && *req.OrgID != existing.OrgID {
		updates["org_id"] = *req.OrgID
		updates["user_id"] = *req.OrgID
		rebound = true
	}
	if req.UserUID != nil {
		updates["user_uid"] = strings.TrimSpace(*req.UserUID)
	}
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" && s.keyring != nil {
		if enc, err := s.keyring.Encrypt(strings.TrimSpace(*req.APIKey)); err == nil {
			updates["api_key_enc"] = enc
			updates["api_key_mask"] = ark.MaskSecret(*req.APIKey)
		}
	}
	if req.Remark != nil {
		updates["remark"] = strings.TrimSpace(*req.Remark)
	}
	// 对齐 tkeapi 2026-08-08：换绑用户/接入点只推进 wallet_ledger_after 隔离旧流水，绝不清零 used_quota/wallet_charged_quota！
	if rebound {
		now := time.Now().UTC()
		updates["wallet_ledger_after"] = &now
	}
	if err := s.db.WithContext(c.Request.Context()).Model(&ark.EndpointBinding{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		fail(c, http.StatusBadRequest, "update binding failed")
		return
	}
	_ = s.db.WithContext(c.Request.Context()).First(&existing, id).Error
	c.JSON(http.StatusOK, gin.H{"data": existing})
}

func (s *Server) v1ArkBindingsDelete(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	res := s.db.WithContext(c.Request.Context()).Delete(&ark.EndpointBinding{}, id)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "delete binding failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "binding not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1ArkBindingsToggle(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var b ark.EndpointBinding
	if err := s.db.WithContext(c.Request.Context()).First(&b, id).Error; err != nil {
		fail(c, http.StatusNotFound, "binding not found")
		return
	}
	var req struct {
		Status *int8 `json:"status"`
	}
	_ = c.ShouldBindJSON(&req)
	targetStatus := int8(1)
	if req.Status != nil {
		targetStatus = *req.Status
	} else if b.Status == 1 {
		targetStatus = 0
	}

	if targetStatus == 1 {
		avail, err := ledger.OrgAvailable(s.db.WithContext(c.Request.Context()), b.OrgID, ledger.AccountUserCredit)
		if err != nil || !avail.IsPositive() {
			fail(c, http.StatusBadRequest, "组织钱包可用余额必须大于 0 才允许启用接入点")
			return
		}
	}

	var acc ark.Account
	if err := s.db.WithContext(c.Request.Context()).First(&acc, b.AccountID).Error; err == nil {
		sk := acc.SecretKeyEnc
		if s.keyring != nil && strings.HasPrefix(sk, "enc:v1:") {
			if dec, derr := s.keyring.Decrypt(sk); derr == nil {
				sk = dec
			}
		}
		cl := ark.NewClient(ark.ClientConfig{
			AccessKey: acc.AccessKey, SecretKey: sk, Region: acc.Region, BaseURLOverride: s.arkOpenAIOverride,
		}, s.arkHTTPClient)
		if targetStatus == 1 {
			_ = cl.StartEndpoint(c.Request.Context(), b.EndpointID)
		} else {
			_ = cl.StopEndpoint(c.Request.Context(), b.EndpointID)
		}
	}

	fuseReason := ark.FuseReasonManual
	if targetStatus == 1 {
		fuseReason = ""
	}
	if err := s.db.WithContext(c.Request.Context()).Model(&ark.EndpointBinding{}).Where("id = ?", b.ID).Updates(map[string]any{
		"status":      targetStatus,
		"fuse_reason": fuseReason,
		"updated_at":  time.Now().UTC(),
	}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "toggle binding failed")
		return
	}
	_ = s.db.WithContext(c.Request.Context()).First(&b, b.ID).Error
	c.JSON(http.StatusOK, gin.H{"data": b})
}

func (s *Server) v1ArkTasksList(c *gin.Context) {
	q := s.readOnly().WithContext(c.Request.Context()).Model(&ark.VideoTask{})
	if ep := strings.TrimSpace(c.Query("endpoint_id")); ep != "" {
		q = q.Where("endpoint_id = ?", ep)
	}
	if accID := ark.ParseUintOrZero(c.Query("account_id")); accID > 0 {
		q = q.Where("account_id = ?", accID)
	}
	var total int64
	_ = q.Count(&total).Error
	var rows []ark.VideoTask
	if err := q.Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query ark tasks failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": total})
}

func (s *Server) v1ArkMonitorSync(c *gin.Context) {
	sum, err := s.SyncArkMonitor(c.Request.Context())
	if err != nil {
		s.Log().Error("ark monitor sync failed", "err", err)
		fail(c, http.StatusInternalServerError, "ark monitor sync failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "summary": sum})
}

func (s *Server) v1ArkUserOverview(c *gin.Context) {
	claims := mustClaims(c)
	ctx := c.Request.Context()
	avail, _ := ledger.OrgAvailable(s.db.WithContext(ctx), claims.OrgID, ledger.AccountUserCredit)
	var bindings []ark.EndpointBinding
	_ = s.readOnly().WithContext(ctx).Where("org_id = ?", claims.OrgID).Order("id DESC").Find(&bindings).Error
	epIDs := make([]string, 0, len(bindings))
	for _, b := range bindings {
		epIDs = append(epIDs, b.EndpointID)
	}
	tasks := make([]ark.VideoTask, 0)
	if len(epIDs) > 0 {
		_ = s.readOnly().WithContext(ctx).Where("endpoint_id IN ?", epIDs).Order("id DESC").Limit(100).Find(&tasks).Error
	}
	c.JSON(http.StatusOK, gin.H{
		"org_available_usd": avail,
		"bindings":          bindings,
		"tasks":             tasks,
	})
}

func (s *Server) v1UpstreamAssetBindingsList(c *gin.Context) {
	var rows []ark.UpstreamAssetBinding
	if err := s.readOnly().WithContext(c.Request.Context()).Order("id DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query upstream asset bindings failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

func (s *Server) v1UpstreamAssetBindingsCreate(c *gin.Context) {
	var req struct {
		Name            string `json:"name" binding:"required"`
		SourceType      string `json:"source_type"`
		ChannelConfigID uint64 `json:"channel_config_id"`
		AssetBasePath   string `json:"asset_base_path"`
		AssetProtocol   string `json:"asset_protocol"`
		VolcAK          string `json:"volc_ak"`
		VolcSK          string `json:"volc_sk"`
		VolcProjectName string `json:"volc_project_name"`
		VolcRegion      string `json:"volc_region"`
		Remark          string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	skEnc := ""
	skMask := ""
	if strings.TrimSpace(req.VolcSK) != "" && s.keyring != nil {
		if enc, err := s.keyring.Encrypt(strings.TrimSpace(req.VolcSK)); err == nil {
			skEnc = enc
			skMask = ark.MaskSecret(req.VolcSK)
		}
	}
	row := ark.UpstreamAssetBinding{
		RID:             ark.GenerateRID(),
		Name:            strings.TrimSpace(req.Name),
		SourceType:      cmp.Or(strings.TrimSpace(req.SourceType), "channel"),
		ChannelConfigID: req.ChannelConfigID,
		AssetBasePath:   strings.TrimSpace(req.AssetBasePath),
		AssetProtocol:   cmp.Or(strings.TrimSpace(req.AssetProtocol), ark.ProtocolArkAction),
		VolcAK:          strings.TrimSpace(req.VolcAK),
		VolcSKEnc:       skEnc,
		VolcSKMask:      skMask,
		VolcProjectName: cmp.Or(strings.TrimSpace(req.VolcProjectName), "default"),
		VolcRegion:      cmp.Or(strings.TrimSpace(req.VolcRegion), ark.DefaultVolcRegion),
		IsActive:        1,
		Remark:          strings.TrimSpace(req.Remark),
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		s.Log().Error("create upstream asset binding failed", "err", err)
		fail(c, http.StatusBadRequest, "create upstream asset binding failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": row})
}

func (s *Server) v1UpstreamAssetBindingsUpdate(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var existing ark.UpstreamAssetBinding
	if err := s.db.WithContext(c.Request.Context()).First(&existing, id).Error; err != nil {
		fail(c, http.StatusNotFound, "upstream asset binding not found")
		return
	}
	var req struct {
		Name            *string `json:"name"`
		SourceType      *string `json:"source_type"`
		ChannelConfigID *uint64 `json:"channel_config_id"`
		AssetBasePath   *string `json:"asset_base_path"`
		AssetProtocol   *string `json:"asset_protocol"`
		VolcAK          *string `json:"volc_ak"`
		VolcSK          *string `json:"volc_sk"`
		VolcProjectName *string `json:"volc_project_name"`
		VolcRegion      *string `json:"volc_region"`
		IsActive        *int8   `json:"is_active"`
		Remark          *string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid JSON body")
		return
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.SourceType != nil && strings.TrimSpace(*req.SourceType) != "" {
		updates["source_type"] = strings.TrimSpace(*req.SourceType)
	}
	if req.ChannelConfigID != nil {
		updates["channel_config_id"] = *req.ChannelConfigID
	}
	if req.AssetBasePath != nil {
		updates["asset_base_path"] = strings.TrimSpace(*req.AssetBasePath)
	}
	if req.AssetProtocol != nil && strings.TrimSpace(*req.AssetProtocol) != "" {
		updates["asset_protocol"] = strings.TrimSpace(*req.AssetProtocol)
	}
	if req.VolcAK != nil {
		updates["volc_ak"] = strings.TrimSpace(*req.VolcAK)
	}
	if req.VolcSK != nil && strings.TrimSpace(*req.VolcSK) != "" && s.keyring != nil {
		if enc, err := s.keyring.Encrypt(strings.TrimSpace(*req.VolcSK)); err == nil {
			updates["volc_sk_enc"] = enc
			updates["volc_sk_mask"] = ark.MaskSecret(*req.VolcSK)
		}
	}
	if req.VolcProjectName != nil {
		updates["volc_project_name"] = strings.TrimSpace(*req.VolcProjectName)
	}
	if req.VolcRegion != nil {
		updates["volc_region"] = strings.TrimSpace(*req.VolcRegion)
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.Remark != nil {
		updates["remark"] = strings.TrimSpace(*req.Remark)
	}
	if err := s.db.WithContext(c.Request.Context()).Model(&ark.UpstreamAssetBinding{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		fail(c, http.StatusBadRequest, "update binding failed")
		return
	}
	_ = s.db.WithContext(c.Request.Context()).First(&existing, id).Error
	c.JSON(http.StatusOK, gin.H{"data": existing})
}

func (s *Server) v1UpstreamAssetBindingsDelete(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	res := s.db.WithContext(c.Request.Context()).Delete(&ark.UpstreamAssetBinding{}, id)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "delete binding failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "binding not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1UpstreamAssetBindingGenerateRule(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var b ark.UpstreamAssetBinding
	if err := s.db.WithContext(c.Request.Context()).First(&b, id).Error; err != nil {
		fail(c, http.StatusNotFound, "binding not found")
		return
	}
	oldPath := "/v1/video/generations"
	newPath := "/api/v3/contents/generations/tasks"
	if b.AssetProtocol == ark.ProtocolPortraitREST {
		newPath = "/tenant/seedance/infer"
	}
	cfgObj := map[string]any{
		"target_type":               "volcengine",
		"upstream_asset_convert":    true,
		"upstream_asset_binding_id": b.ID,
		"fast_response":             true,
		"auth_type":                 "bearer",
		"path_rewrite": map[string]string{
			"old": oldPath,
			"new": newPath,
		},
		"path_passthrough": []string{newPath},
		"poll_path":        "/api/v3/contents/generations/tasks/${task_id}",
	}
	cfgBytes, _ := json.Marshal(cfgObj)
	ruleName := fmt.Sprintf("火山视频转素材ID - %s", b.Name)
	var rule forward.ForwardRule
	if b.ForwardRuleID > 0 {
		if err := s.db.First(&rule, b.ForwardRuleID).Error; err == nil {
			_ = s.db.Model(&forward.ForwardRule{}).Where("id = ?", rule.ID).Updates(map[string]any{
				"name":        ruleName,
				"config_json": string(cfgBytes),
				"is_active":   1,
			}).Error
			_ = s.db.First(&rule, rule.ID).Error
			s.invalidateCache(relay.KindForwardRule, "")
			c.JSON(http.StatusOK, gin.H{"data": rule, "binding": b})
			return
		}
	}
	rule = forward.ForwardRule{
		EID:         fmt.Sprintf("28%03d", b.ID%1000),
		Name:        ruleName,
		RuleType:    "volcengine",
		Category:    "视频",
		Description: fmt.Sprintf("由上游素材绑定 [%s] (%s) 自动生成，自动将媒体 URL 转为 asset:// 素材 ID。", b.Name, b.RID),
		ConfigJSON:  string(cfgBytes),
		IsActive:    1,
		IsSystem:    0,
	}
	if err := s.db.Create(&rule).Error; err != nil {
		rule.EID = fmt.Sprintf("19%05d", time.Now().UnixNano()%100000)
		if err2 := s.db.Create(&rule).Error; err2 != nil {
			fail(c, http.StatusInternalServerError, "create generated forward rule failed")
			return
		}
	}
	_ = s.db.Model(&ark.UpstreamAssetBinding{}).Where("id = ?", b.ID).Update("forward_rule_id", rule.ID).Error
	b.ForwardRuleID = rule.ID
	s.invalidateCache(relay.KindForwardRule, "")
	c.JSON(http.StatusCreated, gin.H{"data": rule, "binding": b})
}

func (s *Server) v1AssetsAdminConfigGet(c *gin.Context) {
	ns := cmp.Or(strings.TrimSpace(c.Query("ns")), ark.PluginAssetManager)
	cfg := s.loadPluginExtConfigMap(ns)
	c.JSON(http.StatusOK, gin.H{
		"ns":                ns,
		"volc_access_key":   cfg["volc_access_key"],
		"volc_secret_mask":  ark.MaskSecret(cfg["volc_secret_key"]),
		"volc_project_name": cmp.Or(cfg["volc_project_name"], "default"),
		"volc_region":       cmp.Or(cfg["volc_region"], ark.DefaultVolcRegion),
		"volc_group_id":     cfg["volc_group_id"],
	})
}

func (s *Server) v1AssetsAdminConfigPut(c *gin.Context) {
	var req struct {
		NS              string `json:"ns"`
		VolcAccessKey   string `json:"volc_access_key"`
		VolcSecretKey   string `json:"volc_secret_key"`
		VolcProjectName string `json:"volc_project_name"`
		VolcRegion      string `json:"volc_region"`
		VolcGroupID     string `json:"volc_group_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ns := cmp.Or(strings.TrimSpace(req.NS), ark.PluginAssetManager)
	_ = s.savePluginExtSetting(ns, "volc_access_key", strings.TrimSpace(req.VolcAccessKey))
	if strings.TrimSpace(req.VolcSecretKey) != "" {
		val := strings.TrimSpace(req.VolcSecretKey)
		if s.keyring != nil {
			if enc, err := s.keyring.Encrypt(val); err == nil {
				val = enc
			}
		}
		_ = s.savePluginExtSetting(ns, "volc_secret_key", val)
	}
	_ = s.savePluginExtSetting(ns, "volc_project_name", cmp.Or(strings.TrimSpace(req.VolcProjectName), "default"))
	_ = s.savePluginExtSetting(ns, "volc_region", cmp.Or(strings.TrimSpace(req.VolcRegion), ark.DefaultVolcRegion))
	_ = s.savePluginExtSetting(ns, "volc_group_id", strings.TrimSpace(req.VolcGroupID))
	c.JSON(http.StatusOK, gin.H{"ok": true, "ns": ns})
}

func (s *Server) v1AssetsAdminList(c *gin.Context) {
	q := s.readOnly().WithContext(c.Request.Context()).Model(&ark.PluginAsset{})
	if ns := strings.TrimSpace(c.Query("ns")); ns != "" {
		q = q.Where("plugin_ns = ?", ns)
	}
	if src := strings.TrimSpace(c.Query("source")); src != "" {
		q = q.Where("source = ?", src)
	}
	var rows []ark.PluginAsset
	if err := q.Order("id DESC").Limit(200).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query assets failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

func (s *Server) v1AssetsAdminDelete(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	res := s.db.WithContext(c.Request.Context()).Delete(&ark.PluginAsset{}, id)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "delete asset record failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "asset record not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1AssetsAdminModerationResult(c *gin.Context) {
	var req struct {
		ID   string `json:"id" binding:"required"`
		Type string `json:"type"`
		NS   string `json:"ns"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "id is required")
		return
	}
	ns := cmp.Or(strings.TrimSpace(req.NS), ark.PluginAssetManager)
	cl, _, err := s.arkClientForNamespace(ns)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := cl.GetModerationResult(c.Request.Context(), req.ID, cmp.Or(req.Type, "asset_id"))
	if err != nil {
		s.Log().Warn("Ark upstream request failed")
		fail(c, http.StatusBadGateway, "Ark upstream request failed")
		return
	}
	var parsed any
	if json.Unmarshal(raw, &parsed) == nil {
		c.JSON(http.StatusOK, parsed)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

func (s *Server) v1AssetsUserList(c *gin.Context) {
	claims := mustClaims(c)
	ns := cmp.Or(strings.TrimSpace(c.GetHeader("x-plugin-ns")), strings.TrimSpace(c.Query("ns")), ark.PluginAssetManager)
	var rows []ark.PluginAsset
	_ = s.readOnly().WithContext(c.Request.Context()).
		Where("org_id = ? AND plugin_ns = ?", claims.OrgID, ns).
		Order("id DESC").Limit(100).Find(&rows).Error
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

func (s *Server) v1AssetsUserGroups(c *gin.Context) {
	claims := mustClaims(c)
	ns := cmp.Or(strings.TrimSpace(c.GetHeader("x-plugin-ns")), strings.TrimSpace(c.Query("ns")), ark.PluginAssetManager)
	var rows []ark.PluginAssetGroup
	_ = s.readOnly().WithContext(c.Request.Context()).
		Where("org_id = ? AND plugin_ns = ?", claims.OrgID, ns).
		Order("id DESC").Find(&rows).Error
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

func (s *Server) v1AssetsUserStorageInfo(c *gin.Context) {
	ns := cmp.Or(strings.TrimSpace(c.GetHeader("x-plugin-ns")), strings.TrimSpace(c.Query("ns")), ark.PluginAssetManager)
	c.JSON(http.StatusOK, gin.H{"plugin_ns": ns, "retention_hours": 168, "storage_provider": "tos"})
}
