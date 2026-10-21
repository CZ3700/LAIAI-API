package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pricingRebindView struct {
	Model               string             `json:"model"`
	CurrentChannelID    uint64             `json:"current_channel_id"`
	TargetChannelID     uint64             `json:"target_channel_id"`
	TargetChannelName   string             `json:"target_channel_name"`
	ProviderScopeID     uint64             `json:"provider_scope_id"`
	ConfigVersion       string             `json:"config_version"`
	PricingVersion      string             `json:"pricing_version"`
	CapabilityVersion   string             `json:"capability_version"`
	FXUSDPerCNY         string             `json:"fx_usd_per_cny"`
	FXVersion           string             `json:"fx_version"`
	ContextSHA256       string             `json:"context_sha256"`
	CatalogVersion      string             `json:"catalog_version"`
	HasActiveCredential bool               `json:"has_active_credential"`
	Publishable         bool               `json:"publishable"`
	LiveTested          bool               `json:"live_tested"`
	Cells               []pricingCell      `json:"cells"`
	Diff                []pricingDiff      `json:"diff"`
	Sources             []pricingSourceRef `json:"sources"`
}

type pricingRebindSnapshot struct {
	pricingRequest
	Operation        string `json:"operation"`
	CurrentChannelID uint64 `json:"current_channel_id"`
	TargetChannelID  uint64 `json:"target_channel_id"`
}

type rebindChannel struct {
	ID                  uint64
	Name                string
	Provider            string
	BaseURL             string
	Status              int8
	OwnerOrgID          uint64
	Models              string
	ModelMapping        string
	HasLegacyCredential bool
}

type rebindCredential struct {
	ID          uint64
	Status      int8
	Fingerprint string
	HasValue    bool
}

// This repair keeps the existing metering configuration and financial scope.
// Only an orphaned binding (or the already selected channel) can be published.
// Preview and publication use the same local validation; neither calls upstream.
func loadPricingRebind(tx *gorm.DB, model string, channelID uint64, now time.Time, lock bool) (pricingRebindView, video.ModelConfig, pricingRequest, []videobilling.PricingRule, error) {
	var out pricingRebindView
	var request pricingRequest
	cfg, cap, scope, bounds, err := loadPricingContext(tx, model, lock)
	if err != nil {
		return out, cfg, request, nil, err
	}
	fail := func(err error) (pricingRebindView, video.ModelConfig, pricingRequest, []videobilling.PricingRule, error) {
		return out, cfg, request, nil, err
	}
	if cfg.VerifiedAt == nil || !cfg.Enabled || !cap.Verified || !cap.Enabled || channelID == 0 {
		return fail(videobilling.ErrPricingUnavailable)
	}
	query := func() *gorm.DB {
		q := tx.Session(&gorm.Session{})
		if lock {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		return q
	}
	if cfg.ChannelID != channelID {
		var oldChannel relay.Channel
		err := query().Select("id").Where("id = ?", cfg.ChannelID).Take(&oldChannel).Error
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			if err == nil {
				err = videobilling.ErrConflict
			}
			return fail(err)
		}
	}
	var target rebindChannel
	if err := query().Table("channels").Select("id,name,provider,base_url,status,owner_org_id,models,model_mapping,credential_enc <> '' AS has_legacy_credential").Where("id = ?", channelID).Take(&target).Error; err != nil {
		return fail(err)
	}
	if target.OwnerOrgID != 0 || target.Status != 1 || (target.Provider != "volcengine" && target.Provider != "modelark") || target.BaseURL != "https://ark.cn-beijing.volces.com/api/v3" {
		return fail(videobilling.ErrPricingUnavailable)
	}
	mapping := map[string]string{}
	if target.ModelMapping != "" && json.Unmarshal([]byte(target.ModelMapping), &mapping) != nil {
		return fail(videobilling.ErrInvalid)
	}
	matched := false
	for _, listed := range strings.Split(target.Models, ",") {
		listed = strings.TrimSpace(listed)
		upstream := listed
		if mapped, exists := mapping[listed]; exists {
			upstream = mapped
		}
		publicModel, reason := seedanceIdentity(listed)
		if upstream == cap.UpstreamModel && reason == "" && (publicModel == "" || publicModel == model) {
			matched = true
		}
	}
	if !matched {
		return fail(videobilling.ErrConflict)
	}
	// Do not select or return credential ciphertext. Legacy fallback is valid
	// only when the channel has no credential rows, matching relay selection.
	var credentials []rebindCredential
	if err := query().Table("channel_credentials").Select("id,status,fingerprint,credential_enc <> '' AS has_value").Where("channel_id = ?", channelID).Order("id").Find(&credentials).Error; err != nil {
		return fail(err)
	}
	hasCredential := len(credentials) == 0 && target.HasLegacyCredential
	for _, credential := range credentials {
		hasCredential = hasCredential || credential.Status == relay.CredentialActive && credential.HasValue
	}
	if !hasCredential {
		return fail(videobilling.ErrPricingUnavailable)
	}
	// Re-read the existing scope with the publication lock. No FX or budget
	// revision is permitted as a side effect of repairing the channel.
	if err := query().Where("id = ? AND enabled = ?", cfg.ProviderScopeID, true).Take(&scope).Error; err != nil {
		return fail(err)
	}
	fx, err := parsePrice(scope.FXUSDPerCNY)
	if err != nil || !priceVersion.MatchString(scope.FXVersion) || scope.FXObservedAt.IsZero() || scope.FXObservedAt.After(now) {
		return fail(videobilling.ErrInvalid)
	}
	var budgets []videobilling.QuotaPolicy
	if err := query().Where("org_id = 0 AND scope = ? AND owner_id = ? AND enabled = ?", videobilling.ScopeProvider, scope.ID, true).Order("id").Find(&budgets).Error; err != nil {
		return fail(err)
	}
	day, month := false, false
	for _, budget := range budgets {
		if budget.LimitAmount != nil && budget.LimitAmount.IsPositive() && (budget.Metric == videobilling.MetricCNY || budget.Metric == videobilling.MetricUSD) && budget.Model == "" {
			day = day || budget.Period == videobilling.PeriodDay
			month = month || budget.Period == videobilling.PeriodMonth
		}
	}
	if !day || !month {
		return fail(videobilling.ErrPricingUnavailable)
	}
	current, err := currentPricingRules(tx, cfg)
	if err != nil {
		return fail(err)
	}
	reference, err := arkpricecatalog.ReferenceCandidates(model)
	if err != nil {
		return fail(err)
	}
	before := priceMatrix(cap, current)
	cells := append([]pricingCell(nil), before...)
	for i := range cells {
		cell := &cells[i]
		for _, price := range reference.Prices {
			if price.Key != priceCellKey(*cell) {
				continue
			}
			cost, err := parsePrice(price.SupplierCNYPer1M)
			if err != nil || price.Precision != "exact" {
				return fail(videobilling.ErrInvalid)
			}
			retail, err := parsePrice(cost.Mul(fx).Round(12).String())
			if err != nil {
				return fail(err)
			}
			cell.SupplierCNYPer1M, cell.RetailUSDPer1M, cell.RiskMultiplier = cost.String(), retail.String(), "1"
			cell.Enabled = priceCellSupported(*cell, cap) && (!cell.InputVideo || cell.MinimumRuleVersion != "")
		}
	}
	next := cfg
	next.ChannelID = channelID
	sources, evidence, err := resolvePricingSources(tx, next, cells, current)
	if err != nil {
		return fail(err)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fail(err)
	}
	version := hex.EncodeToString(token[:])
	request = pricingRequest{ExpectedConfigVersion: cfg.Version, ConfigVersion: "cfg-" + version, PricingVersion: "price-" + version, EvidenceHash: evidence, Cells: cells, SourceSet: &sources}
	_, rules, _, err := validatePricingRequestWithContext(tx, model, request, now, next, cap, scope, bounds)
	if err != nil {
		return fail(err)
	}
	minimumVersions := []string{}
	for _, cell := range cells {
		if cell.MinimumRuleVersion != "" {
			minimumVersions = append(minimumVersions, cell.MinimumRuleVersion)
		}
	}
	var minimumSets []videobilling.MinimumRuleSet
	var minimumCells []videobilling.MinimumRuleCell
	if len(minimumVersions) > 0 {
		if err := query().Where("model = ? AND version IN ?", model, minimumVersions).Order("id").Find(&minimumSets).Error; err != nil {
			return fail(err)
		}
		ids := make([]uint64, 0, len(minimumSets))
		for _, set := range minimumSets {
			ids = append(ids, set.ID)
		}
		if len(ids) > 0 {
			if err := query().Where("rule_set_id IN ?", ids).Order("id").Find(&minimumCells).Error; err != nil {
				return fail(err)
			}
		}
	}
	out = pricingRebindView{
		Model: model, CurrentChannelID: cfg.ChannelID, TargetChannelID: target.ID, TargetChannelName: target.Name, ProviderScopeID: scope.ID,
		ConfigVersion: cfg.Version, PricingVersion: cfg.PricingVersion, CapabilityVersion: cap.Version,
		FXUSDPerCNY: fx.String(), FXVersion: scope.FXVersion, CatalogVersion: reference.CatalogVersion,
		HasActiveCredential: true, Publishable: true, Cells: cells, Diff: preparePricingDiff(before, cells), Sources: sources.Refs,
	}
	out.ContextSHA256 = verificationDigest(struct {
		Configuration video.ModelConfig
		Scope         video.ProviderScope
		Channel       rebindChannel
		Credentials   []rebindCredential
		Budgets       []videobilling.QuotaPolicy
		Current       []videobilling.PricingRule
		Cells         []pricingCell
		Sources       pricingSourceSet
		MinimumSets   []videobilling.MinimumRuleSet
		MinimumCells  []videobilling.MinimumRuleCell
	}{cfg, scope, target, credentials, budgets, current, cells, sources, minimumSets, minimumCells})
	return out, cfg, request, rules, nil
}

func (s *Server) previewArkPricingRebind(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	channelID, err := strconv.ParseUint(c.Query("channel_id"), 10, 64)
	if err != nil || channelID == 0 {
		fail(c, 422, "target channel required")
		return
	}
	var out pricingRebindView
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		out, _, _, _, err = loadPricingRebind(tx, c.Param("model"), channelID, s.now().UTC(), false)
		return err
	})
	if err != nil {
		var count int64
		if errors.Is(err, gorm.ErrRecordNotFound) && s.db.Model(&video.ModelConfig{}).Where("public_model = ?", c.Param("model")).Count(&count).Error == nil && count == 0 {
			initialPricingFail(c, 409, initialPricingError("initial_setup_required"))
		} else {
			c.AbortWithStatusJSON(409, gin.H{"error": "暂不能继承发布，请核对已有配置、精确模型映射、启用凭据、日/月预算及最低用量覆盖。", "code": "channel_rebind_unavailable"})
		}
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}

func (s *Server) publishArkPricingRebind(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	var in struct {
		ChannelID             uint64 `json:"channel_id"`
		ExpectedContextSHA256 string `json:"expected_context_sha256"`
	}
	key := c.GetHeader("Idempotency-Key")
	if videoAdminDecode(c, &in) != nil || in.ChannelID == 0 || len(key) == 0 || len(key) > 128 || strings.TrimSpace(key) != key {
		fail(c, 422, "target channel, expected context and Idempotency-Key required")
		return
	}
	digest, err := hex.DecodeString(in.ExpectedContextSHA256)
	if err != nil || len(digest) != 32 {
		fail(c, 422, "invalid expected context")
		return
	}
	bodyHash := verificationDigest(struct {
		Operation, Model, Context string
		ChannelID                 uint64
	}{"rebind_official_pricing", c.Param("model"), in.ExpectedContextSHA256, in.ChannelID})
	var receipt pricingPublication
	replayed := false
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// Serialize both repairs and normal pricing publications on this model.
		var lockedConfig video.ModelConfig
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("public_model = ?", c.Param("model")).Take(&lockedConfig).Error; err != nil {
			return err
		}
		prior := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("created_by = ? AND idempotency_key = ?", actor.ID, key).Take(&receipt).Error
		if prior == nil {
			if receipt.Model != c.Param("model") || receipt.BodyHash != bodyHash {
				return videobilling.ErrConflict
			}
			replayed = true
			return nil
		}
		if !errors.Is(prior, gorm.ErrRecordNotFound) {
			return prior
		}
		now := s.now().UTC()
		view, cfg, request, rules, err := loadPricingRebind(tx, c.Param("model"), in.ChannelID, now, true)
		if err != nil {
			return err
		}
		if view.ContextSHA256 != in.ExpectedContextSHA256 {
			return videobilling.ErrConflict
		}
		if err := tx.Model(&videobilling.PricingRule{}).Where("model = ? AND provider_scope_id = ? AND enabled = ?", cfg.PublicModel, cfg.ProviderScopeID, true).Updates(map[string]any{"enabled": false, "effective_until": now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&rules).Error; err != nil {
			return err
		}
		var capability ark.Capability
		if err := json.Unmarshal([]byte(cfg.CapabilityJSON), &capability); err != nil {
			return err
		}
		revision := videoConfigurationRevision{PublicModel: cfg.PublicModel, Version: request.ConfigVersion, CapabilityVersion: view.CapabilityVersion, CapabilityHash: verificationDigest(capability), PricingVersion: request.PricingVersion, ProviderScopeID: cfg.ProviderScopeID, ChannelID: in.ChannelID, CreatedBy: actor.ID, EvidenceHash: request.EvidenceHash, CreatedAt: now}
		if err := tx.Create(&revision).Error; err != nil {
			return err
		}
		if err := tx.Model(&cfg).Where("version = ?", cfg.Version).Updates(map[string]any{"channel_id": in.ChannelID, "version": request.ConfigVersion, "pricing_version": request.PricingVersion, "updated_at": now}).Error; err != nil {
			return err
		}
		snapshot, _ := json.Marshal(pricingRebindSnapshot{pricingRequest: request, Operation: "rebind_official_pricing", CurrentChannelID: view.CurrentChannelID, TargetChannelID: in.ChannelID})
		receipt = pricingPublication{Model: cfg.PublicModel, Version: request.PricingVersion, ConfigVersion: request.ConfigVersion, IdempotencyKey: key, BodyHash: bodyHash, SnapshotJSON: string(snapshot), CreatedBy: actor.ID, CreatedAt: now}
		if err := tx.Create(&receipt).Error; err != nil {
			return err
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.pricing.rebind_publish", Target: cfg.PublicModel, Detail: fmt.Sprintf("from_channel=%d;to_channel=%d;version=%s;catalog=%s;context_sha256=%s", view.CurrentChannelID, in.ChannelID, request.PricingVersion, view.CatalogVersion, view.ContextSHA256), CreatedAt: now.UnixMilli()}).Error
	})
	if err != nil {
		fail(c, 409, "channel repair publication rejected: stale context, invalid target or conflicting idempotency key")
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"model": c.Param("model"), "channel_id": in.ChannelID, "config_version": receipt.ConfigVersion, "pricing_version": receipt.Version, "published": true, "publication": receipt})
}
