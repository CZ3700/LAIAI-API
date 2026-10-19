package server

import (
	"encoding/json"
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

type arkPriceCandidate struct {
	pricingCell
	Key        string                      `json:"key"`
	Unit       string                      `json:"unit"`
	Precision  string                      `json:"precision"`
	Configured bool                        `json:"configured"`
	SourceRows []arkpricecatalog.SourceRow `json:"source_rows"`
}

func (s *Server) getArkReferenceCandidates(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	reference, e := arkpricecatalog.ReferenceCandidates(c.Param("model"))
	if errors.Is(e, arkpricecatalog.ErrUnsupportedModel) {
		fail(c, 404, "official reference candidates unavailable for this model")
		return
	}
	if e != nil {
		fail(c, 503, "official reference candidates are incomplete or conflicting")
		return
	}
	var config *catalogVideoConfiguration
	var cap ark.Capability
	current := map[string]videobilling.PricingRule{}
	contextAvailable := false
	e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var cfg video.ModelConfig
		if e := tx.Where("public_model = ?", reference.Model).Take(&cfg).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil
			}
			return e
		}
		var bounds map[string]video.Dimensions
		if json.Unmarshal([]byte(cfg.CapabilityJSON), &cap) != nil {
			return videobilling.ErrInvalid
		}
		standard, reason := seedanceIdentity(cap.UpstreamModel)
		if standard != reference.Model || reason != "" || cap.PublicModel != reference.Model {
			return videobilling.ErrConflict
		}
		capOK := json.Unmarshal([]byte(cfg.BoundsJSON), &bounds) == nil && validateVideoCapability(cfg, cap, bounds) == nil
		config = videoConfigProjection(cfg, cap)
		prices := []videobilling.PricingRule{}
		if e := tx.Where("model = ? AND provider_scope_id = ? AND version = ?", cfg.PublicModel, cfg.ProviderScopeID, cfg.PricingVersion).Find(&prices).Error; e != nil {
			return e
		}
		for _, p := range prices {
			key := priceCellKey(pricingCell{Mode: p.Mode, Resolution: p.Resolution, InputVideo: p.InputVideo, ServiceTier: p.ServiceTier, Draft: p.Draft})
			if _, ok := current[key]; ok {
				return videobilling.ErrConflict
			}
			current[key] = p
		}
		var scope video.ProviderScope
		if e := tx.Select("id,enabled").Where("id = ?", cfg.ProviderScopeID).Take(&scope).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil
			}
			return e
		}
		var channel relay.Channel
		if e := tx.Select("id,provider,base_url,status,owner_org_id").Where("id = ?", cfg.ChannelID).Take(&channel).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil
			}
			return e
		}
		contextAvailable = cfg.Enabled && capOK && scope.Enabled && channel.Status == 1 && channel.OwnerOrgID == 0 && (channel.Provider == "volcengine" || channel.Provider == "modelark") && channel.BaseURL == "https://ark.cn-beijing.volces.com/api/v3"
		return nil
	})
	if e != nil {
		fail(c, 503, "current pricing configuration is unavailable or ambiguous")
		return
	}
	prices := make([]arkPriceCandidate, 0, len(reference.Prices))
	for _, ref := range reference.Prices {
		cell := pricingCell{Mode: ref.Mode, Resolution: ref.Resolution, InputVideo: ref.InputVideo, ServiceTier: ref.ServiceTier, Draft: ref.Draft, SupplierCNYPer1M: ref.SupplierCNYPer1M}
		cell.Available = contextAvailable && priceCellSupported(cell, cap)
		if !cell.Available {
			cell.UnavailableReason = "capability_not_verified"
		}
		p, configured := current[ref.Key]
		if configured {
			cell.RetailUSDPer1M = p.RetailUSDPer1M.String()
			cell.Enabled = p.Enabled
			cell.RiskMultiplier = p.RiskMultiplier.String()
			cell.MinimumRuleVersion = p.MinimumRuleVersion
		}
		prices = append(prices, arkPriceCandidate{pricingCell: cell, Key: ref.Key, Unit: ref.Unit, Precision: ref.Precision, Configured: configured, SourceRows: ref.SourceRows})
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"model": reference.Model, "catalog_version": reference.CatalogVersion, "manifest_sha256": reference.ManifestSHA256, "usage": reference.Usage, "input_token_semantics": "not_projected_separately_completion_tokens_authoritative", "config": config, "prices": prices, "minimum": reference.Minimum, "sources": reference.Sources})
}
