package server

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

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
	var requestedChannel uint64
	if value, exists := c.GetQuery("channel_id"); exists {
		var err error
		requestedChannel, err = strconv.ParseUint(value, 10, 64)
		if err != nil || requestedChannel == 0 {
			fail(c, 422, "invalid target channel")
			return
		}
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
	var targetScope *video.ProviderScope
	e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if requestedChannel != 0 {
			var channel relay.Channel
			if err := tx.Select("id,models,model_mapping").Where("id = ?", requestedChannel).Take(&channel).Error; err != nil {
				return err
			}
			mapping := map[string]string{}
			if channel.ModelMapping != "" && json.Unmarshal([]byte(channel.ModelMapping), &mapping) != nil {
				return videobilling.ErrInvalid
			}
			matched := false
			for _, listed := range strings.Split(channel.Models, ",") {
				listed = strings.TrimSpace(listed)
				upstream := listed
				if mapped, exists := mapping[listed]; exists {
					upstream = mapped
				}
				model, reason := seedanceIdentity(upstream)
				publicModel, publicReason := seedanceIdentity(listed)
				if model == reference.Model && reason == "" && publicReason == "" && (publicModel == "" || publicModel == reference.Model) {
					matched = true
				}
			}
			if !matched {
				return videobilling.ErrConflict
			}
		}
		var cfg video.ModelConfig
		if e := tx.Where("public_model = ?", reference.Model).Take(&cfg).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil
			}
			return e
		}
		if requestedChannel != 0 && cfg.ChannelID != requestedChannel {
			// A global model configuration belongs to its bound channel; a
			// different channel inherits official defaults, never that policy.
			return nil
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
		if e := tx.Select("id,enabled,fx_usd_per_cny,fx_version,fx_observed_at").Where("id = ?", cfg.ProviderScopeID).Take(&scope).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil
			}
			return e
		}
		if requestedChannel != 0 && scope.Enabled {
			if _, err := parsePrice(scope.FXUSDPerCNY); err == nil && priceVersion.MatchString(scope.FXVersion) && !scope.FXObservedAt.IsZero() && !scope.FXObservedAt.After(s.now().UTC()) {
				targetScope = &scope
			}
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
		cell := pricingCell{Mode: ref.Mode, Resolution: ref.Resolution, InputVideo: ref.InputVideo, ServiceTier: ref.ServiceTier, Draft: ref.Draft, SupplierCNYPer1M: ref.SupplierCNYPer1M, RiskMultiplier: "1"}
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
	out := gin.H{"model": reference.Model, "catalog_version": reference.CatalogVersion, "manifest_sha256": reference.ManifestSHA256, "usage": reference.Usage, "input_token_semantics": "not_projected_separately_completion_tokens_authoritative", "config": config, "prices": prices, "minimum": reference.Minimum, "sources": reference.Sources}
	if requestedChannel != 0 {
		out["channel_id"] = requestedChannel
	}
	if targetScope != nil {
		out["fx_usd_per_cny"], out["fx_version"], out["fx_observed_at"] = targetScope.FXUSDPerCNY, targetScope.FXVersion, targetScope.FXObservedAt
	}
	c.JSON(200, out)
}
