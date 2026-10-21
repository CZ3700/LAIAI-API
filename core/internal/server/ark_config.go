package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/arkpricetemplate"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// These administration APIs require an individual active superadmin session;
// the shared emergency token is not an auditable evidence reviewer identity.
func (s *Server) videoAdminActor(c *gin.Context) (*identity.User, bool) {
	value, _ := c.Get("actor_id")
	id, _ := value.(uint64)
	var actor identity.User
	if id == 0 || s.db.Where("id = ? AND status = ? AND role = ?", id, identity.StatusActive, identity.PlatRoleSuperadmin).First(&actor).Error != nil {
		fail(c, 403, "an active superadmin user session is required")
		return nil, false
	}
	return &actor, true
}
func videoAdminDecode(c *gin.Context, dst any) error {
	raw, e := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20+1))
	if e != nil || len(raw) > 1<<20 {
		return video.ErrInvalid
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return video.ErrInvalid
	}
	// Reject duplicate keys recursively before typed decoding.
	scan := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, e := scan.Token()
		if e != nil {
			return e
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for scan.More() {
				key, e := scan.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				name = strings.ToLower(name)
				if !ok || seen[name] {
					return video.ErrInvalid
				}
				seen[name] = true
				if e = walk(); e != nil {
					return e
				}
			}
		case '[':
			for scan.More() {
				if e = walk(); e != nil {
					return e
				}
			}
		default:
			return video.ErrInvalid
		}
		_, e = scan.Token()
		return e
	}
	if e = walk(); e != nil {
		return e
	}
	if _, e = scan.Token(); e != io.EOF {
		return video.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}
func (s *Server) videoAdminReply(c *gin.Context, e error) {
	if e != nil {
		fail(c, 422, "video configuration or evidence is invalid or unavailable")
		return
	}
	c.Status(204)
}

type videoPublication struct {
	Configuration   video.ModelConfig           `json:"configuration"`
	Scope           video.ProviderScope         `json:"provider_scope"`
	Prices          []videobilling.PricingRule  `json:"prices"`
	SupplierBudgets []videobilling.QuotaPolicy  `json:"supplier_budgets"`
	EvidenceHash    string                      `json:"evidence_hash"`
	PricingTemplate *arkpricetemplate.Reference `json:"pricing_template,omitempty"`
	InitialSetup    *initialPricingIntent       `json:"initial_setup,omitempty"`
}

type videoConfigurationRevision struct {
	ID                                                                      uint64 `gorm:"primaryKey"`
	PublicModel, Version, CapabilityVersion, CapabilityHash, PricingVersion string
	ProviderScopeID, ChannelID, CreatedBy                                   uint64
	EvidenceHash                                                            string
	CreatedAt                                                               time.Time
}

func (videoConfigurationRevision) TableName() string { return "video_configuration_revisions" }

func (s *Server) registerVideoConfiguration(su *gin.RouterGroup) {
	su.POST("/video/configurations", s.publishVideoConfiguration)
	su.PUT("/video/orgs/:org_id/quota-policies", s.publishVideoQuota)
	su.PUT("/video/org-policies/:org_id", func(c *gin.Context) {
		actor, ok := s.videoAdminActor(c)
		if !ok {
			return
		}
		if s.video == nil {
			fail(c, 503, "video gateway unavailable")
			return
		}
		org, e := strconv.ParseUint(c.Param("org_id"), 10, 64)
		if e != nil || org == 0 {
			fail(c, 422, "invalid organization")
			return
		}
		var body struct {
			Async   bool `json:"async_execution_allowed"`
			History bool `json:"capture_history"`
			Days    int  `json:"retention_days"`
		}
		if e = videoAdminDecode(c, &body); e != nil || body.Days < 1 || body.Days > 30 {
			fail(c, 422, "invalid retention policy")
			return
		}
		e = s.db.Transaction(func(tx *gorm.DB) error {
			if e := identity.VerifyOrgActive(tx, org); e != nil {
				return e
			}
			policy := video.Policy{OrgID: org, AsyncExecutionAllowed: body.Async, CaptureHistory: body.History, RetentionDays: body.Days, UpdatedAt: s.now().UTC()}
			if e := tx.Save(&policy).Error; e != nil {
				return e
			}
			return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.policy.publish", Target: fmt.Sprint(org), Detail: "explicit_async_and_history_policy", CreatedAt: s.now().UnixMilli()}).Error
		})
		s.videoAdminReply(c, e)
	})
	su.POST("/video/model-configs/:model/disable", func(c *gin.Context) {
		actor, ok := s.videoAdminActor(c)
		if !ok {
			return
		}
		e := s.db.Transaction(func(tx *gorm.DB) error {
			if e := tx.Model(&video.ModelConfig{}).Where("public_model = ?", c.Param("model")).Update("enabled", false).Error; e != nil {
				return e
			}
			return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.model.disable", Target: c.Param("model"), Detail: "new_admission_disabled", CreatedAt: s.now().UnixMilli()}).Error
		})
		s.videoAdminReply(c, e)
	})
}

func (s *Server) publishVideoConfiguration(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	// Direct delivery needs the video runtime and encrypted evidence store;
	// generated media is no longer copied into a platform artifact service.
	if s.video == nil || s.video.Evidence == nil {
		fail(c, 503, "video runtime must be configured first")
		return
	}
	var in videoPublication
	if videoAdminDecode(c, &in) != nil || in.Configuration.Version == "" || len(in.Prices) > 100 || len(in.SupplierBudgets) < 2 || len(in.SupplierBudgets) > 20 {
		fail(c, 422, "versioned verified configuration, prices and supplier budgets required")
		return
	}
	if err := s.validateInitialPublication(in, actor.ID); err != nil {
		initialPricingFail(c, 422, err)
		return
	}
	var template arkpricetemplate.Template
	var templateProfile arkverification.Profile
	var templateBodyHash string
	inheritOfficialPricing := in.PricingTemplate == nil && len(in.Prices) == 0
	var inheritedCatalog arkpricecatalog.Candidates
	templateReplayed := false
	templateKey := c.GetHeader("Idempotency-Key")
	if in.PricingTemplate != nil {
		if len(in.Prices) != 0 || len(templateKey) < 1 || len(templateKey) > 128 {
			fail(c, 422, "template and explicit prices are mutually exclusive; Idempotency-Key required")
			return
		}
		raw, _ := json.Marshal(in)
		hash := sha256.Sum256(raw)
		templateBodyHash = hex.EncodeToString(hash[:])
		var err error
		template, templateProfile, err = resolveInitialPricingTemplate(s.db.WithContext(c.Request.Context()), &in, s.now().UTC())
		if err != nil {
			fail(c, 422, "installed pricing template or verified profile unavailable")
			return
		}
		if in.InitialSetup != nil && in.InitialSetup.PricingMode == "official" {
			if err := officialInitialPrices(&in); err != nil {
				initialPricingFail(c, 422, err)
				return
			}
		}
	}
	if len(in.EvidenceHash) != 64 || len(in.Prices) == 0 && !inheritOfficialPricing {
		fail(c, 422, "verified evidence and prices required")
		return
	}
	cfg := in.Configuration
	scope := in.Scope
	if _, e := hex.DecodeString(in.EvidenceHash); e != nil {
		fail(c, 422, "invalid evidence digest")
		return
	}
	if !ark.IsPublicModel(cfg.PublicModel) {
		fail(c, 422, "unsupported public model")
		return
	}
	var cap ark.Capability
	var bounds map[string]video.Dimensions
	if json.Unmarshal([]byte(cfg.CapabilityJSON), &cap) != nil || cap.PublicModel != cfg.PublicModel || cap.Version == "" || !cap.Verified || !cap.Enabled || json.Unmarshal([]byte(cfg.BoundsJSON), &bounds) != nil || scope.ScopeKey == "" || scope.AccountRef == "" || scope.Region != "cn-beijing" || scope.ProjectName == "" {
		fail(c, 422, "verified official capability and account scope required")
		return
	}
	if validateVideoCapability(cfg, cap, bounds) != nil {
		fail(c, 422, "invalid official capability bounds")
		return
	}
	fx, e := decimal.NewFromString(scope.FXUSDPerCNY)
	if e != nil || !fx.IsPositive() || scope.FXVersion == "" || scope.FXObservedAt.IsZero() || scope.FXObservedAt.After(s.now().UTC()) {
		fail(c, 422, "invalid FX snapshot")
		return
	}
	now := s.now().UTC()
	scope.Enabled = true
	cfg.VerifiedAt = &now
	cfg.UpdatedAt = now
	e = s.db.Transaction(func(tx *gorm.DB) error {
		if in.PricingTemplate != nil {
			// Re-read the installed template inside the publication transaction.
			if _, err := loadPricingTemplateRef(tx, cfg.PublicModel, *in.PricingTemplate); err != nil {
				return err
			}
			var receipt pricingPublication
			prior := tx.Where("created_by = ? AND idempotency_key = ?", actor.ID, templateKey).Take(&receipt).Error
			if prior == nil {
				if receipt.Model != cfg.PublicModel || receipt.BodyHash != templateBodyHash {
					return video.ErrConflict
				}
				var published videoConfigurationRevision
				if err := tx.Where("public_model = ? AND version = ? AND pricing_version = ?", cfg.PublicModel, receipt.ConfigVersion, receipt.Version).Take(&published).Error; err != nil {
					return err
				}
				cfg.ProviderScopeID, scope.ID = published.ProviderScopeID, published.ProviderScopeID
				templateReplayed = true
				return nil
			}
			if !errors.Is(prior, gorm.ErrRecordNotFound) {
				return prior
			}
		}
		// Admission locks the configuration before channel and quota rows too.
		var oldConfig video.ModelConfig
		prior := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("public_model = ?", cfg.PublicModel).First(&oldConfig).Error
		if prior != nil && !errors.Is(prior, gorm.ErrRecordNotFound) {
			return prior
		}
		if prior == nil && oldConfig.Version == cfg.Version {
			return video.ErrConflict
		}
		if prior == nil && (in.PricingTemplate != nil || inheritOfficialPricing) {
			// Existing prices must use the version-checked template preview and
			// normal pricing publication flow, never the first-admission route.
			return video.ErrConflict
		}
		if err := s.validateInitialPublicationContext(tx, in, template, templateProfile, now); err != nil {
			return err
		}
		capBytes, _ := json.Marshal(cap)
		capDigest := sha256.Sum256(capBytes)
		capHash := hex.EncodeToString(capDigest[:])
		if prior == nil {
			var oldCap ark.Capability
			if json.Unmarshal([]byte(oldConfig.CapabilityJSON), &oldCap) != nil {
				return video.ErrConflict
			}
			if oldCap.Version == cap.Version {
				raw, _ := json.Marshal(oldCap)
				oldHash := sha256.Sum256(raw)
				if oldHash != capDigest {
					return video.ErrConflict
				}
			}
		}
		var conflicting int64
		if e := tx.Model(&videoConfigurationRevision{}).Where("public_model = ? AND (version = ? OR pricing_version = ? OR (capability_version = ? AND capability_hash <> ?))", cfg.PublicModel, cfg.Version, cfg.PricingVersion, cap.Version, capHash).Count(&conflicting).Error; e != nil {
			return e
		}
		if conflicting > 0 {
			return video.ErrConflict
		}
		var ch relay.Channel
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND owner_org_id = ? AND status = ?", cfg.ChannelID, 0, 1).First(&ch).Error; e != nil {
			return e
		}
		if ch.BaseURL != "https://ark.cn-beijing.volces.com/api/v3" || (ch.Provider != "volcengine" && ch.Provider != "modelark") {
			return video.ErrInvalid
		}
		if scope.ID == 0 {
			var canonical int64
			if e := tx.Model(&video.ProviderScope{}).Where("account_ref=? AND region=? AND project_name=?", scope.AccountRef, scope.Region, scope.ProjectName).Count(&canonical).Error; e != nil {
				return e
			}
			if canonical != 0 {
				return video.ErrConflict
			}
			if e := tx.Create(&scope).Error; e != nil {
				return e
			}
		} else {
			var old video.ProviderScope
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&old, scope.ID).Error; e != nil {
				return e
			}
			oldFX, parseErr := decimal.NewFromString(old.FXUSDPerCNY)
			if parseErr != nil || old.ScopeKey != scope.ScopeKey || old.AccountRef != scope.AccountRef || old.Region != scope.Region || old.ProjectName != scope.ProjectName || !old.Enabled || (old.FXVersion == scope.FXVersion && !oldFX.Equal(fx)) {
				return video.ErrConflict
			}
			if (in.PricingTemplate != nil || inheritOfficialPricing) && (old.FXVersion != scope.FXVersion || !oldFX.Equal(fx) || !old.FXObservedAt.Equal(scope.FXObservedAt)) {
				return video.ErrConflict
			}
			// FX revisions do not create a new financial scope or reset its quota
			// buckets. Accepted tasks already own immutable FX snapshots.
			if in.PricingTemplate == nil && !inheritOfficialPricing {
				if e := tx.Model(&old).Updates(map[string]any{"fx_usd_per_cny": fx.StringFixed(12), "fx_version": scope.FXVersion, "fx_observed_at": scope.FXObservedAt}).Error; e != nil {
					return e
				}
				old.FXUSDPerCNY = fx.StringFixed(12)
				old.FXVersion = scope.FXVersion
				old.FXObservedAt = scope.FXObservedAt
			}
			scope = old
		}
		cfg.ProviderScopeID = scope.ID
		if inheritOfficialPricing {
			var err error
			in.Prices, inheritedCatalog, err = initialOfficialPricing(tx, cfg, cap, scope, now)
			if err != nil || len(in.Prices) == 0 {
				return video.ErrInvalid
			}
		}
		var existingPrice int64
		if e := tx.Model(&videobilling.PricingRule{}).Where("model = ? AND version = ?", cfg.PublicModel, cfg.PricingVersion).Count(&existingPrice).Error; e != nil {
			return e
		}
		if existingPrice > 0 {
			return video.ErrConflict
		}
		preserveBudgets := in.InitialSetup != nil && in.Scope.ID != 0
		day, month := false, false
		budgetKeys := map[string]bool{}
		for _, p := range in.SupplierBudgets {
			if preserveBudgets {
				// The signed preview and locked context above require an exact
				// match. Do not revise policies or their existing quota buckets.
				continue
			}
			if p.ID != 0 || p.Scope != videobilling.ScopeProvider || p.OrgID != 0 || p.Version == "" || p.Model != "" || (p.Metric != videobilling.MetricCNY && p.Metric != videobilling.MetricUSD) || p.LimitAmount == nil || !p.LimitAmount.IsPositive() || p.LimitUnits != nil {
				return video.ErrInvalid
			}
			if p.Period == videobilling.PeriodDay {
				day = true
			} else if p.Period == videobilling.PeriodMonth {
				month = true
			} else {
				return video.ErrInvalid
			}
			budgetKey := p.Metric + "/" + p.Period
			if budgetKeys[budgetKey] {
				return video.ErrInvalid
			}
			budgetKeys[budgetKey] = true
			if _, e := time.LoadLocation(p.Timezone); e != nil {
				return video.ErrInvalid
			}
			p.OwnerID = scope.ID
			p.Enabled = true
			if e := tx.Model(&videobilling.QuotaPolicy{}).Where("org_id=0 AND scope=? AND owner_id=? AND metric=? AND period=?", p.Scope, p.OwnerID, p.Metric, p.Period).Update("enabled", false).Error; e != nil {
				return e
			}
			if e := tx.Create(&p).Error; e != nil {
				return e
			}
		}
		if !preserveBudgets && (!day || !month) {
			return video.ErrInvalid
		}
		if in.PricingTemplate != nil {
			if err := importTemplateMinimum(tx, templateProfile, actor.ID, now); err != nil {
				return err
			}
		}
		// Old tasks retain immutable rate snapshots. New admission sees one active
		// price version after the whole publication commits.
		if e := tx.Model(&videobilling.PricingRule{}).Where("provider_scope_id = ? AND model = ?", scope.ID, cfg.PublicModel).Update("enabled", false).Error; e != nil {
			return e
		}
		priceKeys := map[string]bool{}
		for _, p := range in.Prices {
			if p.ID != 0 || p.Version != cfg.PricingVersion || p.Model != cfg.PublicModel {
				return video.ErrInvalid
			}
			p.ProviderScopeID = scope.ID
			if !inheritOfficialPricing {
				p.SourceHash = in.EvidenceHash
				p.Enabled = true
			} else if !p.Enabled {
				// Inheriting prices does not enable unsupported combinations or
				// manufacture missing minimum-token evidence.
				if e := tx.Create(&p).Error; e != nil {
					return e
				}
				continue
			}
			if p.EffectiveFrom.IsZero() || p.EffectiveFrom.After(now) || (p.EffectiveUntil != nil && !p.EffectiveUntil.After(now)) || !priceCellSupported(pricingCell{Mode: p.Mode, Resolution: p.Resolution, InputVideo: p.InputVideo, ServiceTier: p.ServiceTier, Draft: p.Draft}, cap) || (p.InputVideo && cfg.MaxInputVideoDurationMS <= 0) {
				return video.ErrInvalid
			}
			priceKey := fmt.Sprintf("%s/%s/%t/%s/%t", p.Mode, p.Resolution, p.InputVideo, p.ServiceTier, p.Draft)
			if priceKeys[priceKey] {
				return video.ErrInvalid
			}
			priceKeys[priceKey] = true
			dimension, ok := bounds[p.Resolution+"/"+cap.DefaultRatio]
			if !ok {
				return video.ErrInvalid
			}
			input := int64(0)
			if p.InputVideo {
				input = cfg.MaxInputVideoDurationMS
				if e := videobilling.ValidateMinimumCoverageTx(tx, p, videobilling.MinimumContext{Resolution: p.Resolution, Ratios: cap.Ratios, MinDuration: cap.MinDuration, MaxDuration: cap.MaxDuration}, int64(cap.FPS)); e != nil {
					return e
				}
			}
			_, e := videobilling.QuoteWithMinimumTx(tx, p, videobilling.MeteringInput{InputVideo: p.InputVideo, InputDurationMS: input, OutputDurationMS: int64(cap.MaxDuration) * 1000, Width: dimension.Width, Height: dimension.Height, FPSNumerator: int64(cap.FPS), FPSDenominator: 1, BoundsVerified: true, CapabilityVersion: cap.Version, EffectiveRatio: decimal.NewFromInt(1), FXUSDPerCNY: fx, FXVersion: scope.FXVersion, FXObservedAt: scope.FXObservedAt}, videobilling.MinimumContext{Resolution: p.Resolution, Ratio: cap.DefaultRatio})
			if e != nil {
				return e
			}
			if e = tx.Create(&p).Error; e != nil {
				return e
			}
		}
		if e := tx.Create(&videoConfigurationRevision{PublicModel: cfg.PublicModel, Version: cfg.Version, CapabilityVersion: cap.Version, CapabilityHash: capHash, PricingVersion: cfg.PricingVersion, ProviderScopeID: scope.ID, ChannelID: cfg.ChannelID, CreatedBy: actor.ID, EvidenceHash: in.EvidenceHash, CreatedAt: now}).Error; e != nil {
			return e
		}
		if e := tx.Save(&cfg).Error; e != nil {
			return e
		}
		detail := "version=" + cfg.Version + ";evidence_sha256=" + in.EvidenceHash
		detail += initialPricingAudit(in)
		if inheritOfficialPricing {
			detail += ";official_catalog=" + inheritedCatalog.CatalogVersion + ";catalog_sha256=" + inheritedCatalog.ManifestSHA256
		}
		if in.PricingTemplate != nil {
			cells := make([]pricingCell, 0, len(in.Prices))
			for _, p := range in.Prices {
				cells = append(cells, cellFromRule(p))
			}
			sources, _, err := resolvePricingSources(tx, cfg, cells, nil)
			if err != nil {
				return err
			}
			if err := templatePricingSource(tx, cfg, in.PricingTemplate, &sources); err != nil {
				return err
			}
			snapshot, _ := json.Marshal(pricingRequest{ConfigVersion: cfg.Version, PricingVersion: cfg.PricingVersion, EvidenceHash: in.EvidenceHash, Cells: cells, SourceSet: &sources, TemplateRef: in.PricingTemplate})
			if err := tx.Create(&pricingPublication{Model: cfg.PublicModel, Version: cfg.PricingVersion, ConfigVersion: cfg.Version, IdempotencyKey: templateKey, BodyHash: templateBodyHash, SnapshotJSON: string(snapshot), CreatedBy: actor.ID, CreatedAt: now}).Error; err != nil {
				return err
			}
			detail += ";template=" + template.Version + ";template_sha256=" + template.BodySHA256
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.configuration.publish", Target: cfg.PublicModel, Detail: detail, CreatedAt: s.now().UnixMilli()}).Error
	})
	if e != nil {
		if in.InitialSetup != nil {
			initialPricingFail(c, 422, e)
			return
		}
		s.videoAdminReply(c, e)
		return
	}
	out := gin.H{"public_model": cfg.PublicModel, "version": cfg.Version, "provider_scope_id": scope.ID}
	if in.PricingTemplate != nil {
		out["pricing_template"], out["pricing_version"] = in.PricingTemplate, cfg.PricingVersion
		if templateReplayed {
			c.Header("Idempotency-Replayed", "true")
		}
	}
	c.JSON(http.StatusCreated, out)
}

func videoContains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func (s *Server) publishVideoQuota(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	org, e := strconv.ParseUint(c.Param("org_id"), 10, 64)
	var p videobilling.QuotaPolicy
	if e != nil || org == 0 || videoAdminDecode(c, &p) != nil || p.ID != 0 || p.Version == "" || len(p.Version) > 64 || !videoContains([]string{videobilling.ScopeOrg, videobilling.ScopeKey, videobilling.ScopeOrgModel}, p.Scope) || !videoContains([]string{videobilling.PeriodDay, videobilling.PeriodMonth, videobilling.PeriodLifetime}, p.Period) {
		fail(c, 422, "invalid quota policy")
		return
	}
	if _, e = time.LoadLocation(p.Timezone); e != nil {
		fail(c, 422, "invalid quota timezone")
		return
	}
	switch p.Metric {
	case videobilling.MetricUSD:
		if p.LimitUnits != nil || p.LimitAmount == nil || p.LimitAmount.IsNegative() || !p.LimitAmount.Equal(p.LimitAmount.Truncate(8)) {
			fail(c, 422, "invalid monetary limit")
			return
		}
	case videobilling.MetricTasks, videobilling.MetricTokens:
		if p.LimitAmount != nil || p.LimitUnits == nil || *p.LimitUnits < 0 {
			fail(c, 422, "invalid unit limit")
			return
		}
	default:
		fail(c, 422, "invalid quota metric")
		return
	}
	if p.Scope == videobilling.ScopeOrgModel && !ark.IsPublicModel(p.Model) || p.Scope != videobilling.ScopeOrgModel && p.Model != "" {
		fail(c, 422, "invalid quota model")
		return
	}
	p.OrgID = org
	p.Enabled = true
	if p.Scope != videobilling.ScopeKey {
		p.OwnerID = org
	}
	e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if e := identity.VerifyOrgActive(tx, org); e != nil {
			return e
		}
		// Serialize concurrent revisions on the owning organization.
		var owner identity.Organization
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", org).First(&owner).Error; e != nil {
			return e
		}
		if p.Scope == videobilling.ScopeKey {
			var key identity.APIKey
			if e := tx.Where("id=? AND org_id=?", p.OwnerID, org).First(&key).Error; e != nil {
				return e
			}
		}
		if e := tx.Model(&videobilling.QuotaPolicy{}).Where("org_id=? AND scope=? AND owner_id=? AND model=? AND metric=? AND period=?", org, p.Scope, p.OwnerID, p.Model, p.Metric, p.Period).Update("enabled", false).Error; e != nil {
			return e
		}
		if e := tx.Create(&p).Error; e != nil {
			return e
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.quota.publish", Target: fmt.Sprint(org), Detail: fmt.Sprintf("policy_id=%d", p.ID), CreatedAt: s.now().UnixMilli()}).Error
	})
	s.videoAdminReply(c, e)
}
func validateVideoCapability(cfg video.ModelConfig, cap ark.Capability, bounds map[string]video.Dimensions) error {
	maxDuration := 15
	if cfg.PublicModel == ark.Model25 {
		maxDuration = 30
	}
	if cfg.PricingVersion == "" || len(cfg.Version) > 64 || len(cap.Version) > 64 || !ark.ValidUpstreamModel(cfg.PublicModel, cap.UpstreamModel) || cap.MinDuration < 4 || cap.MaxDuration > maxDuration || cap.MaxDuration < cap.MinDuration || cap.FPS <= 0 || cap.FPS > 120 || !videoContains(cap.Resolutions, cap.DefaultResolution) || !videoContains(cap.Ratios, cap.DefaultRatio) || cfg.MaxInputVideoDurationMS < 0 {
		return video.ErrInvalid
	}
	if ark.Is20Variant(cfg.PublicModel) {
		profile, err := arkverification.Load(cfg.PublicModel)
		if err != nil || cfg.MaxInputVideoDurationMS > profile.MaxInputVideoDurationMS || cap.VideoInput && cfg.MaxInputVideoDurationMS != profile.MaxInputVideoDurationMS {
			return video.ErrInvalid
		}
		// Raw reference URLs are metered at the official aggregate duration
		// bound. A smaller administrative value must not under-reserve them.
		for _, resolution := range cap.Resolutions {
			for _, ratio := range cap.Ratios {
				d, ok := profile.Bounds[resolution+"/"+ratio]
				got, exists := bounds[resolution+"/"+ratio]
				if !ok || !exists || got.Width != d.Width || got.Height != d.Height {
					return video.ErrInvalid
				}
			}
		}
	}
	for _, resolution := range cap.Resolutions {
		for _, ratio := range cap.Ratios {
			d, ok := bounds[resolution+"/"+ratio]
			if !ok || d.Width <= 0 || d.Height <= 0 || d.Width > 16384 || d.Height > 16384 {
				return video.ErrInvalid
			}
		}
	}
	seen := map[string]bool{}
	for _, status := range strings.Split(cfg.NoChargeStatuses, ",") {
		if status == "" {
			continue
		}
		if seen[status] || (status != "failed" && status != "cancelled" && status != "expired") {
			return video.ErrInvalid
		}
		seen[status] = true
	}
	_, e := ark.Normalize(ark.CreateRequest{Model: cfg.PublicModel, Content: []ark.ContentItem{{Type: "text", Text: "capability validation"}}}, cap, ark.Bindings{})
	return e
}
