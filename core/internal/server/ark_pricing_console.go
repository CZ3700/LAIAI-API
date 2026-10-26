package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricetemplate"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pricingPublication struct {
	ID             uint64    `gorm:"primaryKey" json:"id"`
	Model          string    `json:"model"`
	Version        string    `json:"version"`
	ConfigVersion  string    `json:"config_version"`
	IdempotencyKey string    `json:"-"`
	BodyHash       string    `json:"body_hash"`
	SnapshotJSON   string    `gorm:"column:snapshot_json" json:"snapshot_json"`
	CreatedBy      uint64    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

func (pricingPublication) TableName() string { return "video_pricing_publications" }

type pricingCell struct {
	Mode               string `json:"mode"`
	Resolution         string `json:"resolution"`
	InputVideo         bool   `json:"input_video"`
	ServiceTier        string `json:"service_tier"`
	Draft              bool   `json:"draft"`
	Enabled            bool   `json:"enabled"`
	RetailUSDPer1M     string `json:"retail_usd_per_1m"`
	SupplierCNYPer1M   string `json:"supplier_cny_per_1m"`
	RiskMultiplier     string `json:"risk_multiplier"`
	MinimumRuleVersion string `json:"minimum_rule_version"`
	Available          bool   `json:"available"`
	UnavailableReason  string `json:"unavailable_reason,omitempty"`
}
type pricingRequest struct {
	ExpectedConfigVersion string                      `json:"expected_config_version"`
	ConfigVersion         string                      `json:"config_version"`
	PricingVersion        string                      `json:"pricing_version"`
	EvidenceHash          string                      `json:"evidence_hash"`
	Cells                 []pricingCell               `json:"cells"`
	SourceSet             *pricingSourceSet           `json:"source_set,omitempty"`
	TemplateRef           *arkpricetemplate.Reference `json:"template_ref,omitempty"`
}

var priceDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
var priceVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

func parsePrice(s string) (decimal.Decimal, error) {
	if !priceDecimal.MatchString(s) {
		return decimal.Zero, videobilling.ErrInvalid
	}
	d, e := decimal.NewFromString(s)
	if e != nil || !d.IsPositive() {
		return decimal.Zero, videobilling.ErrInvalid
	}
	return d, nil
}
func priceCellKey(c pricingCell) string {
	return fmt.Sprintf("%s/%s/%t/%s/%t", c.Mode, c.Resolution, c.InputVideo, c.ServiceTier, c.Draft)
}
func containsPrice(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func priceCellSupported(c pricingCell, cap ark.Capability) bool {
	return video.PublicationCellSupported(video.PublicationCell{
		Mode: c.Mode, Resolution: c.Resolution, InputVideo: c.InputVideo,
		ServiceTier: c.ServiceTier, Draft: c.Draft,
	}, cap)
}
func priceMatrix(cap ark.Capability, prices []videobilling.PricingRule) []pricingCell {
	resolutions := []string{"480p", "720p", "1080p", "4k"}
	if cap.PublicModel == ark.Model25 {
		resolutions = []string{"480p", "720p", "1080p"}
	}
	if ark.Is20Variant(cap.PublicModel) {
		resolutions = []string{"480p", "720p"}
	}
	cells := []pricingCell{}
	for _, res := range resolutions {
		for _, input := range []bool{false, true} {
			cells = append(cells, pricingCell{Mode: "generate", Resolution: res, InputVideo: input, ServiceTier: "default"})
		}
	}
	if cap.PublicModel == ark.Model25 {
		for _, mode := range []string{"edit", "extend"} {
			for _, res := range resolutions {
				cells = append(cells, pricingCell{Mode: mode, Resolution: res, InputVideo: true, ServiceTier: "default"})
			}
		}
		for _, input := range []bool{false, true} {
			cells = append(cells, pricingCell{Mode: "generate", Resolution: "480p", InputVideo: input, ServiceTier: "default", Draft: true})
			cells = append(cells, pricingCell{Mode: "draft_conversion", Resolution: "1080p", InputVideo: input, ServiceTier: "default"})
		}
	}
	// Complete replacement must preserve dimensions outside today's grid.
	known := map[string]bool{}
	for _, c := range cells {
		known[priceCellKey(c)] = true
	}
	for _, p := range prices {
		c := pricingCell{Mode: p.Mode, Resolution: p.Resolution, InputVideo: p.InputVideo, ServiceTier: p.ServiceTier, Draft: p.Draft}
		if !known[priceCellKey(c)] {
			cells = append(cells, c)
			known[priceCellKey(c)] = true
		}
	}
	for i := range cells {
		c := &cells[i]
		c.RiskMultiplier = "1"
		c.Available = priceCellSupported(*c, cap)
		if !c.Available {
			c.UnavailableReason = "capability_not_verified"
		}
		for _, p := range prices {
			if priceCellKey(*c) == priceCellKey(pricingCell{Mode: p.Mode, Resolution: p.Resolution, InputVideo: p.InputVideo, ServiceTier: p.ServiceTier, Draft: p.Draft}) {
				c.Enabled = p.Enabled
				c.RetailUSDPer1M = p.RetailUSDPer1M.String()
				c.SupplierCNYPer1M = p.SupplierCNYPer1M.String()
				c.RiskMultiplier = p.RiskMultiplier.String()
				c.MinimumRuleVersion = p.MinimumRuleVersion
			}
		}
	}
	return cells
}
func loadPricingContext(tx *gorm.DB, model string, lock bool) (video.ModelConfig, ark.Capability, video.ProviderScope, map[string]video.Dimensions, error) {
	var cfg video.ModelConfig
	var cap ark.Capability
	var scope video.ProviderScope
	var bounds map[string]video.Dimensions
	q := tx
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Where("public_model = ?", model).Take(&cfg).Error; e != nil {
		return cfg, cap, scope, bounds, e
	}
	if json.Unmarshal([]byte(cfg.CapabilityJSON), &cap) != nil || json.Unmarshal([]byte(cfg.BoundsJSON), &bounds) != nil || validateVideoCapability(cfg, cap, bounds) != nil {
		return cfg, cap, scope, bounds, videobilling.ErrInvalid
	}
	e := tx.Where("id = ? AND enabled = ?", cfg.ProviderScopeID, true).Take(&scope).Error
	return cfg, cap, scope, bounds, e
}
func validatePricingRequest(tx *gorm.DB, model string, in pricingRequest, now time.Time) (video.ModelConfig, []videobilling.PricingRule, []gin.H, error) {
	cfg, cap, scope, bounds, e := loadPricingContext(tx, model, true)
	if e != nil {
		return cfg, nil, nil, e
	}
	return validatePricingRequestWithContext(tx, model, in, now, cfg, cap, scope, bounds)
}

func validatePricingRequestWithContext(tx *gorm.DB, model string, in pricingRequest, now time.Time, cfg video.ModelConfig, cap ark.Capability, scope video.ProviderScope, bounds map[string]video.Dimensions) (video.ModelConfig, []videobilling.PricingRule, []gin.H, error) {
	current, e := currentPricingRules(tx, cfg)
	if e != nil {
		return cfg, nil, nil, e
	}
	in.Cells = defaultPricingRisk(in.Cells, current)
	if e = requireCompletePricingMatrix(in.Cells, current); e != nil {
		return cfg, nil, nil, e
	}
	if in.TemplateRef != nil && in.SourceSet == nil {
		return cfg, nil, nil, videobilling.ErrConflict
	}
	if in.SourceSet != nil {
		if e = validatePricingBinding(tx, cfg); e != nil {
			return cfg, nil, nil, e
		}
		expected, evidence, e := resolvePricingSources(tx, cfg, in.Cells, current)
		if e == nil {
			e = templatePricingSource(tx, cfg, in.TemplateRef, &expected)
		}
		if e != nil || !samePricingSources(*in.SourceSet, expected) || in.EvidenceHash != evidence {
			return cfg, nil, nil, videobilling.ErrConflict
		}
	}
	hash, he := hex.DecodeString(in.EvidenceHash)
	if cfg.Version != in.ExpectedConfigVersion || !priceVersion.MatchString(in.ConfigVersion) || !priceVersion.MatchString(in.PricingVersion) || in.ConfigVersion == cfg.Version || in.PricingVersion == cfg.PricingVersion || he != nil || len(hash) != 32 || len(in.Cells) < 1 || len(in.Cells) > 100 {
		return cfg, nil, nil, videobilling.ErrConflict
	}
	fx, e := decimal.NewFromString(scope.FXUSDPerCNY)
	if e != nil {
		return cfg, nil, nil, e
	}
	rules := []videobilling.PricingRule{}
	currentKeys := map[string]bool{}
	for _, p := range current {
		currentKeys[priceCellKey(cellFromRule(p))] = true
	}
	activeCount := 0
	preview := []gin.H{}
	seen := map[string]bool{}
	for i, c := range in.Cells {
		key := priceCellKey(c)
		if seen[key] {
			return cfg, nil, nil, videobilling.ErrConflict
		}
		seen[key] = true
		if !c.Enabled {
			// Retain configured, explicitly disabled rows in the new version so
			// subsequent edits cannot silently erase their dimensions or values.
			if currentKeys[key] || c.RetailUSDPer1M != "" {
				retail, re := parsePrice(c.RetailUSDPer1M)
				supplier, se := parsePrice(c.SupplierCNYPer1M)
				risk, ke := parsePrice(c.RiskMultiplier)
				if re != nil || se != nil || ke != nil || risk.LessThan(decimal.NewFromInt(1)) || risk.GreaterThan(decimal.NewFromInt(100)) {
					return cfg, nil, nil, videobilling.ErrInvalid
				}
				rules = append(rules, videobilling.PricingRule{RuleID: fmt.Sprintf("%s-%d", in.PricingVersion, i), Version: in.PricingVersion, PricingKey: videobilling.PricingKey{ProviderScopeID: cfg.ProviderScopeID, Model: model, Mode: c.Mode, Resolution: c.Resolution, InputVideo: c.InputVideo, ServiceTier: c.ServiceTier, Draft: c.Draft}, RetailUSDPer1M: retail, SupplierCNYPer1M: supplier, RiskMultiplier: risk, MinimumRuleVersion: c.MinimumRuleVersion, FormulaVersion: "ark-video-token-v1", SourceHash: in.EvidenceHash, EffectiveFrom: now, Enabled: false})
			}
			continue
		}
		activeCount++
		if !priceCellSupported(c, cap) {
			return cfg, nil, nil, videobilling.ErrPricingUnavailable
		}
		retail, e := parsePrice(c.RetailUSDPer1M)
		if e != nil {
			return cfg, nil, nil, e
		}
		supplier, e := parsePrice(c.SupplierCNYPer1M)
		if e != nil {
			return cfg, nil, nil, e
		}
		risk, e := parsePrice(c.RiskMultiplier)
		if e != nil || risk.LessThan(decimal.NewFromInt(1)) || risk.GreaterThan(decimal.NewFromInt(100)) {
			return cfg, nil, nil, videobilling.ErrInvalid
		}
		rule := videobilling.PricingRule{RuleID: fmt.Sprintf("%s-%d", in.PricingVersion, i), Version: in.PricingVersion, PricingKey: videobilling.PricingKey{ProviderScopeID: cfg.ProviderScopeID, Model: model, Mode: c.Mode, Resolution: c.Resolution, InputVideo: c.InputVideo, ServiceTier: c.ServiceTier, Draft: c.Draft}, RetailUSDPer1M: retail, SupplierCNYPer1M: supplier, RiskMultiplier: risk, MinimumRuleVersion: c.MinimumRuleVersion, FormulaVersion: "ark-video-token-v1", SourceHash: in.EvidenceHash, EffectiveFrom: now, Enabled: true}
		// Verify every admitted ratio/duration, not merely the default UI preview.
		var first *videobilling.QuoteResult
		for _, ratio := range cap.Ratios {
			if ratio == "adaptive" {
				continue
			}
			dim, ok := bounds[c.Resolution+"/"+ratio]
			if !ok {
				return cfg, nil, nil, videobilling.ErrPricingUnavailable
			}
			for d := cap.MinDuration; d <= cap.MaxDuration; d++ {
				meter := videobilling.MeteringInput{InputVideo: c.InputVideo, OutputDurationMS: int64(d) * 1000, Width: dim.Width, Height: dim.Height, FPSNumerator: int64(cap.FPS), FPSDenominator: 1, BoundsVerified: true, CapabilityVersion: cap.Version, EffectiveRatio: decimal.NewFromInt(1), FXUSDPerCNY: fx, FXVersion: scope.FXVersion, FXObservedAt: scope.FXObservedAt}
				if c.InputVideo {
					meter.InputDurationMS = cfg.MaxInputVideoDurationMS
				}
				q, e := videobilling.QuoteWithMinimumTx(tx, rule, meter, videobilling.MinimumContext{Resolution: c.Resolution, Ratio: ratio})
				if e != nil {
					return cfg, nil, nil, fmt.Errorf("%s: %w", key, e)
				}
				if first == nil || q.HoldUSD.GreaterThan(first.HoldUSD) {
					copy := q
					first = &copy
				}
			}
		}
		if first == nil {
			return cfg, nil, nil, videobilling.ErrPricingUnavailable
		}
		preview = append(preview, gin.H{"key": key, "maximum_hold_usd": first.HoldUSD.String(), "maximum_estimated_tokens": first.EstimatedTokens, "supplier_reserved_cny": first.SupplierReservedCNY.String(), "effective_ratio": "1", "minimum_rule_version": c.MinimumRuleVersion})
		rules = append(rules, rule)
	}
	if activeCount == 0 {
		return cfg, nil, nil, videobilling.ErrInvalid
	}
	return cfg, rules, preview, nil
}
func (s *Server) registerArkPricingConsole(su *gin.RouterGroup) {
	base := "/video/models/:model/pricing"
	su.GET("/video/pricing-templates", s.listArkPricingTemplates)
	su.POST(base+"/template-preview", s.previewArkPricingTemplate)
	su.GET(base, s.getArkPricingConsole)
	su.POST(base+"/prepare", s.prepareArkPricingConsole)
	su.POST(base+"/validate", s.validateArkPricingConsole)
	su.POST(base+"/publications", s.publishArkPricingConsole)
	su.GET(base+"/rebind-preview", s.previewArkPricingRebind)
	su.GET(base+"/initial-setup", s.getArkInitialPricingSetup)
	su.POST(base+"/initial-preview", s.previewArkInitialPricing)
	su.POST(base+"/rebind-publications", s.publishArkPricingRebind)
	su.GET(base+"/revisions", func(c *gin.Context) {
		if _, ok := s.videoAdminActor(c); !ok {
			return
		}
		rows := []pricingPublication{}
		if e := s.db.Where("model = ?", c.Param("model")).Order("id DESC").Limit(100).Find(&rows).Error; e != nil {
			fail(c, 503, "pricing history unavailable")
			return
		}
		c.JSON(200, gin.H{"items": rows})
	})
	su.GET(base+"/minimum-rules", s.listArkMinimumRules)
	su.POST(base+"/minimum-rules", s.importArkMinimumRules)
}
func (s *Server) getArkPricingConsole(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	tx := s.db.WithContext(c.Request.Context()).Begin()
	if tx.Error != nil {
		fail(c, 503, "pricing unavailable")
		return
	}
	defer tx.Rollback()
	cfg, cap, scope, _, e := loadPricingContext(tx, c.Param("model"), false)
	if e != nil {
		fail(c, 404, "verified model configuration unavailable")
		return
	}
	prices, e := currentPricingRules(tx, cfg)
	if e != nil {
		fail(c, 503, "pricing unavailable")
		return
	}
	cells := priceMatrix(cap, prices)
	var minimumSets []videobilling.MinimumRuleSet
	var minimumCells []videobilling.MinimumRuleCell
	if e = tx.Where("model = ?", cfg.PublicModel).Find(&minimumSets).Error; e != nil {
		fail(c, 503, "minimum evidence unavailable")
		return
	}
	ids := []uint64{}
	for _, m := range minimumSets {
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		if e = tx.Where("rule_set_id IN ?", ids).Find(&minimumCells).Error; e != nil {
			fail(c, 503, "minimum evidence unavailable")
			return
		}
	}
	for i := range cells {
		cell := &cells[i]
		if cell.Available && cell.InputVideo {
			rule := videobilling.PricingRule{PricingKey: videobilling.PricingKey{Model: cfg.PublicModel, Resolution: cell.Resolution, InputVideo: true}, MinimumRuleVersion: cell.MinimumRuleVersion}
			if _, e := videobilling.CatalogMinimumTokens(rule, minimumSets, minimumCells, videobilling.MinimumContext{Ratios: cap.Ratios, MinDuration: cap.MinDuration, MaxDuration: cap.MaxDuration}, int64(cap.FPS)); e != nil {
				cell.Available = false
				cell.UnavailableReason = "verified_minimum_coverage_missing"
			}
		}
	}
	if e = tx.Commit().Error; e != nil {
		fail(c, 503, "pricing unavailable")
		return
	}
	c.JSON(200, gin.H{"model": cfg.PublicModel, "channel_id": cfg.ChannelID, "config_version": cfg.Version, "pricing_version": cfg.PricingVersion, "capability_version": cap.Version, "enabled": cfg.Enabled, "cells": cells, "fx_usd_per_cny": scope.FXUSDPerCNY, "fx_version": scope.FXVersion, "fx_observed_at": scope.FXObservedAt, "ratios": cap.Ratios, "min_duration": cap.MinDuration, "max_duration": cap.MaxDuration, "input_token_semantics": "not_projected_separately_completion_tokens_authoritative"})
}
func (s *Server) validateArkPricingConsole(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	var in pricingRequest
	if videoAdminDecode(c, &in) != nil {
		fail(c, 422, "invalid pricing request")
		return
	}
	var previews []gin.H
	e := s.db.Transaction(func(tx *gorm.DB) error {
		_, _, p, e := validatePricingRequest(tx, c.Param("model"), in, s.now().UTC())
		previews = p
		return e
	})
	if e != nil {
		fail(c, 422, "pricing matrix, version or verified minimum coverage is invalid")
		return
	}
	c.JSON(200, gin.H{"valid": true, "preview": previews, "preview_scope": "maximum verified bounds; ratio 1; no upstream request"})
}
func (s *Server) publishArkPricingConsole(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	var in pricingRequest
	key := c.GetHeader("Idempotency-Key")
	if videoAdminDecode(c, &in) != nil || len(key) < 1 || len(key) > 128 {
		fail(c, 422, "strict pricing request and Idempotency-Key required")
		return
	}
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	var receipt pricingPublication
	e := s.db.Transaction(func(tx *gorm.DB) error {
		if in.TemplateRef != nil {
			if _, err := loadPricingTemplateRef(tx, c.Param("model"), *in.TemplateRef); err != nil {
				return err
			}
		}
		prior := tx.Where("created_by = ? AND idempotency_key = ?", actor.ID, key).Take(&receipt).Error
		if prior == nil {
			if receipt.BodyHash != hash || receipt.Model != c.Param("model") {
				return videobilling.ErrConflict
			}
			return nil
		}
		if !errors.Is(prior, gorm.ErrRecordNotFound) {
			return prior
		}
		now := s.now().UTC()
		cfg, rules, _, e := validatePricingRequest(tx, c.Param("model"), in, now)
		if e != nil {
			return e
		}
		var n int64
		if e = tx.Model(&videoConfigurationRevision{}).Where("public_model = ? AND (version = ? OR pricing_version = ?)", cfg.PublicModel, in.ConfigVersion, in.PricingVersion).Count(&n).Error; e != nil {
			return e
		}
		if n != 0 {
			return videobilling.ErrConflict
		}
		if e = tx.Model(&videobilling.PricingRule{}).Where("model = ? AND version = ?", cfg.PublicModel, in.PricingVersion).Count(&n).Error; e != nil {
			return e
		}
		if n != 0 {
			return videobilling.ErrConflict
		}
		if e = tx.Model(&videobilling.PricingRule{}).Where("model = ? AND provider_scope_id = ? AND enabled = ?", cfg.PublicModel, cfg.ProviderScopeID, true).Updates(map[string]any{"enabled": false, "effective_until": now}).Error; e != nil {
			return e
		}
		if e = tx.Create(&rules).Error; e != nil {
			return e
		}
		var cap ark.Capability
		_ = json.Unmarshal([]byte(cfg.CapabilityJSON), &cap)
		capRaw, _ := json.Marshal(cap)
		capHash := sha256.Sum256(capRaw)
		rev := videoConfigurationRevision{PublicModel: cfg.PublicModel, Version: in.ConfigVersion, CapabilityVersion: cap.Version, CapabilityHash: hex.EncodeToString(capHash[:]), PricingVersion: in.PricingVersion, ProviderScopeID: cfg.ProviderScopeID, ChannelID: cfg.ChannelID, CreatedBy: actor.ID, EvidenceHash: in.EvidenceHash, CreatedAt: now}
		if e = tx.Create(&rev).Error; e != nil {
			return e
		}
		if e = tx.Model(&cfg).Updates(map[string]any{"version": in.ConfigVersion, "pricing_version": in.PricingVersion, "updated_at": now}).Error; e != nil {
			return e
		}
		// Keep the request hash for replay, but record the effective defaults
		// used by the published rules in the immutable snapshot.
		in.Cells = defaultPricingRisk(in.Cells, rules)
		snapshot, _ := json.Marshal(in)
		receipt = pricingPublication{Model: cfg.PublicModel, Version: in.PricingVersion, ConfigVersion: in.ConfigVersion, IdempotencyKey: key, BodyHash: hash, SnapshotJSON: string(snapshot), CreatedBy: actor.ID, CreatedAt: now}
		if e = tx.Create(&receipt).Error; e != nil {
			return e
		}
		detail := "version=" + in.PricingVersion
		if in.TemplateRef != nil {
			detail += ";template=" + in.TemplateRef.Version + ";template_sha256=" + in.TemplateRef.SHA256
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.pricing.publish", Target: cfg.PublicModel, Detail: detail, CreatedAt: now.UnixMilli()}).Error
	})
	if e != nil {
		fail(c, 409, "pricing publication rejected: stale version, duplicate or invalid coverage")
		return
	}
	c.JSON(200, receipt)
}
func (s *Server) listArkMinimumRules(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	sets := []videobilling.MinimumRuleSet{}
	if e := s.db.Where("model = ?", c.Param("model")).Order("id DESC").Limit(100).Find(&sets).Error; e != nil {
		fail(c, 503, "minimum evidence unavailable")
		return
	}
	c.JSON(200, gin.H{"items": sets})
}
func (s *Server) importArkMinimumRules(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	var in struct {
		Version      string                         `json:"version"`
		SourceURL    string                         `json:"source_url"`
		EvidenceHash string                         `json:"evidence_hash"`
		ObservedAt   time.Time                      `json:"observed_at"`
		Cells        []videobilling.MinimumRuleCell `json:"cells"`
	}
	if videoAdminDecode(c, &in) != nil || in.ObservedAt.After(s.now().UTC()) {
		fail(c, 422, "invalid minimum evidence")
		return
	}
	set := videobilling.MinimumRuleSet{Model: c.Param("model"), Version: in.Version, SourceURL: in.SourceURL, EvidenceHash: in.EvidenceHash, ObservedAt: in.ObservedAt, VerifiedBy: actor.ID, VerifiedAt: s.now().UTC()}
	e := s.db.Transaction(func(tx *gorm.DB) error {
		var e error
		set, e = videobilling.PublishMinimumTx(tx, set, in.Cells)
		if e != nil {
			return e
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.minimum.verify", Target: set.Model, Detail: "operator_verified:" + set.Version, CreatedAt: s.now().UnixMilli()}).Error
	})
	if e != nil {
		fail(c, 422, "minimum table must be unique, complete, and traced to official evidence")
		return
	}
	c.JSON(201, set)
}
