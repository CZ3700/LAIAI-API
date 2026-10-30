package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type arkVerificationStatus struct {
	Model             string                   `json:"model"`
	ProfileVersion    string                   `json:"profile_version"`
	EvidenceSHA256    string                   `json:"evidence_sha256"`
	ConfigVersion     string                   `json:"config_version"`
	CapabilityVersion string                   `json:"capability_version"`
	MinimumVersion    string                   `json:"minimum_version"`
	MinimumCellCount  int                      `json:"minimum_cell_count"`
	Resolutions       []string                 `json:"resolutions"`
	Ratios            []string                 `json:"ratios"`
	MinDuration       int                      `json:"min_duration"`
	MaxDuration       int                      `json:"max_duration"`
	Verified          bool                     `json:"verified"`
	VerificationKind  string                   `json:"verification_kind"`
	MinimumPrecision  string                   `json:"minimum_precision"`
	LiveTested        bool                     `json:"live_tested"`
	Sources           []arkverification.Source `json:"sources"`
}

func (s *Server) registerArkVerification(su *gin.RouterGroup) {
	su.GET("/video/models/:model/verification", s.getArkVerification)
	su.POST("/video/models/:model/verification", s.syncArkVerification)
}

func verificationDigest(v any) string {
	raw, _ := json.Marshal(v)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func verificationSubset(old, next []string) bool {
	for _, v := range old {
		if !videoContains(next, v) {
			return false
		}
	}
	return true
}

// A reviewed profile may expand an older configuration, but cannot silently
// replace a newer or broader configuration with the bundled profile.
func verificationTarget(cfg video.ModelConfig, old ark.Capability, oldBounds map[string]video.Dimensions, p arkverification.Profile) (ark.Capability, map[string]video.Dimensions, error) {
	next := p.Capability
	model, reason := seedanceIdentity(old.UpstreamModel)
	if model != cfg.PublicModel || reason != "" || old.PublicModel != cfg.PublicModel || next.PublicModel != cfg.PublicModel {
		return next, nil, videobilling.ErrConflict
	}
	if !verificationSubset(old.Resolutions, next.Resolutions) || !verificationSubset(old.Ratios, next.Ratios) || !verificationSubset(old.OmniReferenceTaskTypes, next.OmniReferenceTaskTypes) || !verificationSubset(old.OutputFormats, next.OutputFormats) || old.MinDuration < next.MinDuration || old.MaxDuration > next.MaxDuration || old.FPS != next.FPS || old.MaxContentItems > next.MaxContentItems || old.MaxImages > next.MaxImages || old.MaxVideos > next.MaxVideos || old.MaxAudios > next.MaxAudios || old.MinExecutionExpiresAfter < next.MinExecutionExpiresAfter || old.MaxExecutionExpiresAfter > next.MaxExecutionExpiresAfter || cfg.MaxInputVideoDurationMS > p.MaxInputVideoDurationMS {
		return next, nil, videobilling.ErrConflict
	}
	a, b := reflect.ValueOf(old), reflect.ValueOf(next)
	for i := 0; i < a.NumField(); i++ {
		name := a.Type().Field(i).Name
		if a.Field(i).Kind() == reflect.Bool && name != "Enabled" && name != "Verified" && !strings.HasPrefix(name, "Default") && a.Field(i).Bool() && !b.Field(i).Bool() {
			return next, nil, videobilling.ErrConflict
		}
	}
	if old.Frames && (next.FrameStep <= 0 || old.MinFrames < next.MinFrames || old.MaxFrames > next.MaxFrames || (old.MinFrames-next.MinFrames)%next.FrameStep != 0 || old.FrameStep%next.FrameStep != 0) {
		return next, nil, videobilling.ErrConflict
	}
	bounds := make(map[string]video.Dimensions, len(p.Bounds))
	for k, d := range p.Bounds {
		bounds[k] = video.Dimensions{Width: d.Width, Height: d.Height}
	}
	for k, d := range oldBounds {
		n, ok := bounds[k]
		if !ok || d.Width > n.Width || d.Height > n.Height {
			return next, nil, videobilling.ErrConflict
		}
	}
	next.UpstreamModel, next.Enabled = old.UpstreamModel, old.Enabled
	if videoContains(next.Resolutions, old.DefaultResolution) {
		next.DefaultResolution = old.DefaultResolution
	}
	if videoContains(next.Ratios, old.DefaultRatio) {
		next.DefaultRatio = old.DefaultRatio
	}
	if old.DefaultDuration >= next.MinDuration && old.DefaultDuration <= next.MaxDuration || old.DefaultDuration == -1 && next.AutomaticDuration {
		next.DefaultDuration = old.DefaultDuration
	}
	if old.DefaultSeed >= -1 && old.DefaultSeed <= 2147483647 {
		next.DefaultSeed = old.DefaultSeed
	}
	if old.DefaultExecutionExpiresAfter >= next.MinExecutionExpiresAfter && old.DefaultExecutionExpiresAfter <= next.MaxExecutionExpiresAfter {
		next.DefaultExecutionExpiresAfter = old.DefaultExecutionExpiresAfter
	}
	next.DefaultGenerateAudio = old.DefaultGenerateAudio
	next.DefaultWatermark = old.DefaultWatermark
	next.DefaultReturnLastFrame = old.DefaultReturnLastFrame
	next.DefaultDraft = old.DefaultDraft
	next.Version = ""
	next.Version = "arkv-" + verificationDigest(next)[:40]
	check := cfg
	check.MaxInputVideoDurationMS = p.MaxInputVideoDurationMS
	if validateVideoCapability(check, next, bounds) != nil {
		return next, nil, videobilling.ErrInvalid
	}
	return next, bounds, nil
}

func verificationBinding(tx *gorm.DB, cfg video.ModelConfig) error {
	var channel relay.Channel
	if e := tx.Session(&gorm.Session{}).Select("id,provider,base_url,status,owner_org_id").Where("id = ?", cfg.ChannelID).Take(&channel).Error; e != nil {
		return e
	}
	if channel.OwnerOrgID != 0 || channel.Status != 1 || (channel.Provider != "volcengine" && channel.Provider != "modelark") || channel.BaseURL != "https://ark.cn-beijing.volces.com/api/v3" {
		return videobilling.ErrConflict
	}
	var scope video.ProviderScope
	if e := tx.Session(&gorm.Session{}).Where("id = ? AND enabled = ?", cfg.ProviderScopeID, true).Take(&scope).Error; e != nil {
		return e
	}
	if scope.AccountRef == "" || scope.ProjectName == "" || scope.Region != "cn-beijing" {
		return videobilling.ErrConflict
	}
	return nil
}

func verificationMinimumCells(p arkverification.Profile) []videobilling.MinimumRuleCell {
	cells := make([]videobilling.MinimumRuleCell, 0, len(p.MinimumCells))
	for _, c := range p.MinimumCells {
		cells = append(cells, videobilling.MinimumRuleCell{Resolution: c.Resolution, Ratio: c.Ratio, OutputDurationMS: c.OutputDurationMS, FPS: c.FPS, MinimumTokens: c.MinimumTokens})
	}
	return cells
}

func verificationMinimumCoverage(tx *gorm.DB, model string, p arkverification.Profile, cap ark.Capability) error {
	var set videobilling.MinimumRuleSet
	if e := tx.Where("model = ? AND version = ?", model, p.MinimumVersion).Take(&set).Error; e != nil {
		return e
	}
	var cells []videobilling.MinimumRuleCell
	if e := tx.Where("rule_set_id = ?", set.ID).Find(&cells).Error; e != nil {
		return e
	}
	for _, resolution := range cap.Resolutions {
		probe := videobilling.PricingRule{PricingKey: videobilling.PricingKey{Model: model, Resolution: resolution, InputVideo: true}, MinimumRuleVersion: p.MinimumVersion}
		if _, e := videobilling.CatalogMinimumTokens(probe, []videobilling.MinimumRuleSet{set}, cells, videobilling.MinimumContext{Resolution: resolution, Ratios: cap.Ratios, MinDuration: cap.MinDuration, MaxDuration: cap.MaxDuration}, int64(cap.FPS)); e != nil {
			return e
		}
	}
	return nil
}

func matchingVerificationMinimum(tx *gorm.DB, model string, p arkverification.Profile) (bool, error) {
	var set videobilling.MinimumRuleSet
	e := tx.Where("model = ? AND version = ?", model, p.MinimumVersion).Take(&set).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if set.SourceURL != p.SourceURL || set.EvidenceHash != p.EvidenceSHA256 || !set.ObservedAt.Equal(p.ObservedAt) || set.VerifiedBy == 0 || set.VerifiedAt.Before(set.ObservedAt) {
		return false, videobilling.ErrConflict
	}
	var rows []videobilling.MinimumRuleCell
	if e := tx.Where("rule_set_id = ?", set.ID).Find(&rows).Error; e != nil {
		return false, e
	}
	wanted := map[videobilling.MinimumRuleCell]bool{}
	for _, c := range verificationMinimumCells(p) {
		wanted[c] = true
	}
	if len(wanted) != len(p.MinimumCells) || len(rows) != len(wanted) {
		return false, videobilling.ErrConflict
	}
	for _, c := range rows {
		c.ID, c.RuleSetID = 0, 0
		if !wanted[c] {
			return false, videobilling.ErrConflict
		}
		delete(wanted, c)
	}
	return len(wanted) == 0, nil
}

func verificationView(cfg video.ModelConfig, current ark.Capability, p arkverification.Profile, verified bool) arkVerificationStatus {
	return arkVerificationStatus{Model: cfg.PublicModel, ProfileVersion: p.Version, EvidenceSHA256: p.EvidenceSHA256, ConfigVersion: cfg.Version, CapabilityVersion: current.Version, MinimumVersion: p.MinimumVersion, MinimumCellCount: len(p.MinimumCells), Resolutions: p.Capability.Resolutions, Ratios: p.Capability.Ratios, MinDuration: p.Capability.MinDuration, MaxDuration: p.Capability.MaxDuration, Verified: verified, VerificationKind: "official_documentation", MinimumPrecision: "estimated", LiveTested: false, Sources: p.Sources}
}

func verificationMatches(cfg video.ModelConfig, old, next ark.Capability, oldBounds, bounds map[string]video.Dimensions, p arkverification.Profile) bool {
	return cfg.VerifiedAt != nil && cfg.MaxInputVideoDurationMS == p.MaxInputVideoDurationMS && reflect.DeepEqual(old, next) && reflect.DeepEqual(oldBounds, bounds)
}

func (s *Server) getArkVerification(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	var p arkverification.Profile
	var e error
	switch c.Query("profile") {
	case "":
		p, e = arkverification.Load(c.Param("model"))
	case "draft":
		p, e = arkverification.LoadDraft(c.Param("model"))
	default:
		fail(c, 422, "unknown verification profile")
		return
	}
	if e != nil {
		fail(c, 404, "official verification profile unavailable")
		return
	}
	var out arkVerificationStatus
	e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var cfg video.ModelConfig
		if e := tx.Where("public_model = ?", c.Param("model")).Take(&cfg).Error; e != nil {
			return e
		}
		var old ark.Capability
		var oldBounds map[string]video.Dimensions
		if json.Unmarshal([]byte(cfg.CapabilityJSON), &old) != nil || json.Unmarshal([]byte(cfg.BoundsJSON), &oldBounds) != nil {
			return videobilling.ErrInvalid
		}
		next, bounds, targetErr := verificationTarget(cfg, old, oldBounds, p)
		minimum, minimumErr := matchingVerificationMinimum(tx, cfg.PublicModel, p)
		if minimumErr != nil && !errors.Is(minimumErr, videobilling.ErrConflict) {
			return minimumErr
		}
		out = verificationView(cfg, old, p, targetErr == nil && minimumErr == nil && minimum && verificationBinding(tx, cfg) == nil && verificationMatches(cfg, old, next, oldBounds, bounds, p))
		return nil
	})
	if e != nil {
		fail(c, 409, "current verification context unavailable")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}

func (s *Server) syncArkVerification(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	var in struct {
		ExpectedConfigVersion string `json:"expected_config_version"`
		ProfileVersion        string `json:"profile_version"`
	}
	if videoAdminDecode(c, &in) != nil || in.ExpectedConfigVersion == "" {
		fail(c, 422, "expected configuration and profile versions required")
		return
	}
	p, e := arkverification.Load(c.Param("model"))
	if e != nil {
		fail(c, 404, "official verification profile unavailable")
		return
	}
	if p.Version != in.ProfileVersion {
		if draft, draftErr := arkverification.LoadDraft(c.Param("model")); draftErr == nil && draft.Version == in.ProfileVersion {
			p = draft
		}
	}
	if p.Version != in.ProfileVersion {
		fail(c, 409, "official verification profile changed")
		return
	}
	now := s.now().UTC()
	if p.ObservedAt.After(now) {
		fail(c, 409, "official verification evidence is not yet effective")
		return
	}
	var out arkVerificationStatus
	e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var cfg video.ModelConfig
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("public_model = ?", c.Param("model")).Take(&cfg).Error; e != nil {
			return e
		}
		if e := verificationBinding(tx.Clauses(clause.Locking{Strength: "UPDATE"}), cfg); e != nil {
			return e
		}
		var old ark.Capability
		var oldBounds map[string]video.Dimensions
		if json.Unmarshal([]byte(cfg.CapabilityJSON), &old) != nil || json.Unmarshal([]byte(cfg.BoundsJSON), &oldBounds) != nil {
			return videobilling.ErrInvalid
		}
		next, bounds, e := verificationTarget(cfg, old, oldBounds, p)
		if e != nil {
			return e
		}
		matched, e := matchingVerificationMinimum(tx, cfg.PublicModel, p)
		if e != nil {
			return e
		}
		version := "verify-" + verificationDigest([]any{cfg.PublicModel, in.ExpectedConfigVersion, p.Version, p.EvidenceSHA256, next, bounds})[:40]
		already := matched && verificationMatches(cfg, old, next, oldBounds, bounds, p)
		if cfg.Version != in.ExpectedConfigVersion {
			if !already || cfg.Version != version {
				return videobilling.ErrConflict
			}
			var count int64
			if e := tx.Model(&videoConfigurationRevision{}).Where("public_model = ? AND version = ? AND capability_hash = ? AND evidence_hash = ?", cfg.PublicModel, version, verificationDigest(next), p.EvidenceSHA256).Count(&count).Error; e != nil {
				return e
			}
			if count != 1 {
				return videobilling.ErrConflict
			}
			out = verificationView(cfg, old, p, true)
			return nil
		}
		if already {
			out = verificationView(cfg, old, p, true)
			return nil
		}
		if !matched {
			set := videobilling.MinimumRuleSet{Model: cfg.PublicModel, Version: p.MinimumVersion, SourceURL: p.SourceURL, EvidenceHash: p.EvidenceSHA256, ObservedAt: p.ObservedAt, VerifiedBy: actor.ID, VerifiedAt: now}
			if _, e := videobilling.PublishMinimumTx(tx, set, verificationMinimumCells(p)); e != nil {
				return e
			}
		}
		if e := verificationMinimumCoverage(tx, cfg.PublicModel, p, next); e != nil {
			return e
		}
		// Existing enabled reference-video tariffs keep their exact minimum
		// version. Refuse expanding admission if that version lacks coverage.
		var prices []videobilling.PricingRule
		if e := tx.Where("model = ? AND provider_scope_id = ? AND version = ? AND enabled = ?", cfg.PublicModel, cfg.ProviderScopeID, cfg.PricingVersion, true).Find(&prices).Error; e != nil {
			return e
		}
		for _, price := range prices {
			if price.InputVideo {
				if e := videobilling.ValidateMinimumCoverageTx(tx, price, videobilling.MinimumContext{Resolution: price.Resolution, Ratios: next.Ratios, MinDuration: next.MinDuration, MaxDuration: next.MaxDuration}, int64(next.FPS)); e != nil {
					return e
				}
			}
		}
		capRaw, _ := json.Marshal(next)
		boundsRaw, _ := json.Marshal(bounds)
		cfg.Version, cfg.CapabilityJSON, cfg.BoundsJSON, cfg.MaxInputVideoDurationMS = version, string(capRaw), string(boundsRaw), p.MaxInputVideoDurationMS
		cfg.VerifiedAt, cfg.UpdatedAt = &now, now
		updated := tx.Model(&video.ModelConfig{}).Where("public_model = ? AND version = ?", cfg.PublicModel, in.ExpectedConfigVersion).Updates(map[string]any{"version": cfg.Version, "capability_json": cfg.CapabilityJSON, "bounds_json": cfg.BoundsJSON, "max_input_video_duration_ms": cfg.MaxInputVideoDurationMS, "verified_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return videobilling.ErrConflict
		}
		if _, _, _, _, e := loadPricingContext(tx, cfg.PublicModel, false); e != nil {
			return e
		}
		if e := tx.Create(&videoConfigurationRevision{PublicModel: cfg.PublicModel, Version: cfg.Version, CapabilityVersion: next.Version, CapabilityHash: verificationDigest(next), PricingVersion: cfg.PricingVersion, ProviderScopeID: cfg.ProviderScopeID, ChannelID: cfg.ChannelID, CreatedBy: actor.ID, EvidenceHash: p.EvidenceSHA256, CreatedAt: now}).Error; e != nil {
			return e
		}
		if e := tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.verification.sync", Target: cfg.PublicModel, Detail: "profile=" + p.Version + ";evidence_sha256=" + p.EvidenceSHA256 + ";config=" + cfg.Version, CreatedAt: now.UnixMilli()}).Error; e != nil {
			return e
		}
		out = verificationView(cfg, next, p, true)
		return nil
	})
	if e != nil {
		fail(c, 409, "verification synchronization rejected: stale context, incompatible binding, broader capability or minimum coverage conflict")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}
