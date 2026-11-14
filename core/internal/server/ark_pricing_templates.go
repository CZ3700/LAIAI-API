package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricetemplate"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func loadPricingTemplateRef(tx *gorm.DB, model string, ref arkpricetemplate.Reference) (arkpricetemplate.Template, error) {
	if ref.Model != model || len(ref.SHA256) != 64 {
		return arkpricetemplate.Template{}, videobilling.ErrConflict
	}
	t, err := arkpricetemplate.Load(tx, model, ref.Version)
	if err != nil {
		return t, err
	}
	if t.BodySHA256 != ref.SHA256 {
		return t, videobilling.ErrConflict
	}
	return t, nil
}

func templatePricingCell(c arkpricetemplate.Cell) pricingCell {
	return pricingCell{Mode: c.Mode, Resolution: c.Resolution, InputVideo: c.InputVideo, ServiceTier: c.ServiceTier, Draft: c.Draft, Enabled: c.Enabled, RetailUSDPer1M: c.RetailUSDPer1M, SupplierCNYPer1M: c.SupplierCNYPer1M, RiskMultiplier: c.RiskMultiplier, MinimumRuleVersion: c.MinimumRuleVersion}
}

// Compatibility includes metering inputs, not just a model name. A narrower
// current capability may reuse prices, but its admitted pixel pairs and input
// duration bound must still match the reviewed profile exactly.
func templateCapability(cfg video.ModelConfig, t arkpricetemplate.Template, p arkverification.Profile) (ark.Capability, map[string]video.Dimensions, error) {
	var cap ark.Capability
	var bounds map[string]video.Dimensions
	if p.Version != t.ProfileVersion || p.EvidenceSHA256 != t.ProfileSHA256 || json.Unmarshal([]byte(cfg.CapabilityJSON), &cap) != nil || json.Unmarshal([]byte(cfg.BoundsJSON), &bounds) != nil || cap.UpstreamModel != t.UpstreamModel {
		return cap, nil, videobilling.ErrConflict
	}
	next, nextBounds, err := verificationTarget(cfg, cap, bounds, p)
	if err != nil {
		return next, nil, err
	}
	if cap.VideoInput && cfg.MaxInputVideoDurationMS != p.MaxInputVideoDurationMS {
		return next, nil, videobilling.ErrConflict
	}
	for _, resolution := range cap.Resolutions {
		for _, ratio := range cap.Ratios {
			actual, exists := bounds[resolution+"/"+ratio]
			expected, verified := p.Bounds[resolution+"/"+ratio]
			if !exists || !verified || actual.Width != expected.Width || actual.Height != expected.Height {
				return next, nil, videobilling.ErrConflict
			}
		}
	}
	return next, nextBounds, nil
}

func validateTemplateContext(tx *gorm.DB, cfg video.ModelConfig, t arkpricetemplate.Template) error {
	p, err := arkverification.Load(t.Model)
	if err != nil {
		return err
	}
	if _, _, err := templateCapability(cfg, t, p); err != nil {
		return err
	}
	// Missing evidence may be previewed as disabled prices. Existing evidence
	// under the template's version must never differ from the reviewed source.
	_, err = matchingVerificationMinimum(tx, cfg.PublicModel, p)
	return err
}

func (s *Server) listArkPricingTemplates(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	items, err := arkpricetemplate.List(s.db.WithContext(c.Request.Context()), c.Query("model"))
	if err != nil {
		fail(c, 503, "installed pricing templates unavailable or incompatible")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"items": items})
}

func (s *Server) previewArkPricingTemplate(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	var in struct {
		ChannelID             uint64 `json:"channel_id"`
		ExpectedConfigVersion string `json:"expected_config_version"`
		TemplateVersion       string `json:"template_version"`
		Mode                  string `json:"mode"`
	}
	if videoAdminDecode(c, &in) != nil || (in.Mode != "fill_missing" && in.Mode != "replace") {
		fail(c, 422, "template version, context and merge mode required")
		return
	}
	var out gin.H
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		cfg, cap, _, _, err := loadPricingContext(tx, c.Param("model"), false)
		if err != nil {
			return err
		}
		if cfg.ChannelID != in.ChannelID || cfg.Version != in.ExpectedConfigVersion {
			return videobilling.ErrConflict
		}
		if err := validatePricingBinding(tx, cfg); err != nil {
			return err
		}
		t, err := arkpricetemplate.Load(tx, cfg.PublicModel, in.TemplateVersion)
		if err != nil {
			return err
		}
		if err := validateTemplateContext(tx, cfg, t); err != nil {
			return err
		}
		current, err := currentPricingRules(tx, cfg)
		if err != nil {
			return err
		}
		before := priceMatrix(cap, current)
		cells := append([]pricingCell(nil), before...)
		configured := map[string]bool{}
		for _, p := range current {
			configured[priceCellKey(cellFromRule(p))] = true
		}
		byKey := map[string]arkpricetemplate.Cell{}
		for _, cell := range t.Cells {
			byKey[cell.Key()] = cell
		}
		for i, cell := range cells {
			templateCell, exists := byKey[priceCellKey(cell)]
			if !exists || in.Mode == "fill_missing" && configured[priceCellKey(cell)] {
				continue
			}
			next := templatePricingCell(templateCell)
			next.Available, next.UnavailableReason = cell.Available, cell.UnavailableReason
			if configured[priceCellKey(cell)] {
				// Even an explicit price replacement does not re-enable a row.
				next.Enabled = cell.Enabled
			} else {
				// Filling a price is not permission to open a new combination.
				next.Enabled = false
			}
			cells[i] = next
		}
		out = gin.H{"model": cfg.PublicModel, "channel_id": cfg.ChannelID, "config_version": cfg.Version, "template_ref": t.Reference(), "mode": in.Mode, "cells": cells, "diff": preparePricingDiff(before, cells)}
		return nil
	})
	if err != nil {
		fail(c, 409, "template preview rejected: stale context or incompatible template or binding")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}

func templatePricingSource(tx *gorm.DB, cfg video.ModelConfig, ref *arkpricetemplate.Reference, sources *pricingSourceSet) error {
	if ref == nil {
		return nil
	}
	t, err := loadPricingTemplateRef(tx, cfg.PublicModel, *ref)
	if err != nil {
		return err
	}
	if err := validateTemplateContext(tx, cfg, t); err != nil {
		return err
	}
	sources.Refs = append(sources.Refs, pricingSourceRef{Kind: "pricing_template", Version: t.Version, SourceID: t.Model, RawSHA256: t.BodySHA256})
	sealPricingSources(sources)
	return nil
}

// Templates supply money and reviewed evidence only. The caller still supplies
// a real channel, account scope, explicit day/month budgets and admission state.
func resolveInitialPricingTemplate(tx *gorm.DB, in *videoPublication, now time.Time) (arkpricetemplate.Template, arkverification.Profile, error) {
	t, err := loadPricingTemplateRef(tx, in.Configuration.PublicModel, *in.PricingTemplate)
	if err != nil {
		return t, arkverification.Profile{}, err
	}
	p, err := arkverification.Load(t.Model)
	if err != nil || p.Version != t.ProfileVersion || p.EvidenceSHA256 != t.ProfileSHA256 || p.ObservedAt.After(now) {
		return t, p, videobilling.ErrConflict
	}
	cfg := &in.Configuration
	if cfg.CapabilityJSON == "" && cfg.BoundsJSON == "" {
		capability := p.Capability
		capability.UpstreamModel = t.UpstreamModel
		capRaw, _ := json.Marshal(capability)
		boundsRaw, _ := json.Marshal(p.Bounds)
		cfg.CapabilityJSON, cfg.BoundsJSON = string(capRaw), string(boundsRaw)
		cfg.MaxInputVideoDurationMS = p.MaxInputVideoDurationMS
	}
	cap, bounds, err := templateCapability(*cfg, t, p)
	if err != nil {
		return t, p, err
	}
	var requested ark.Capability
	var requestedBounds map[string]video.Dimensions
	if json.Unmarshal([]byte(cfg.CapabilityJSON), &requested) != nil || json.Unmarshal([]byte(cfg.BoundsJSON), &requestedBounds) != nil {
		return t, p, videobilling.ErrInvalid
	}
	requested.Version = cap.Version
	if !reflect.DeepEqual(requested, cap) || !reflect.DeepEqual(requestedBounds, bounds) {
		// Do not expand an explicitly narrower first-admission configuration.
		return t, p, videobilling.ErrConflict
	}
	capRaw, _ := json.Marshal(cap)
	boundsRaw, _ := json.Marshal(bounds)
	cfg.CapabilityJSON, cfg.BoundsJSON = string(capRaw), string(boundsRaw)
	in.EvidenceHash = p.EvidenceSHA256
	in.Prices = make([]videobilling.PricingRule, 0, len(t.Cells))
	for i, c := range t.Cells {
		cell := templatePricingCell(c)
		if !priceCellSupported(cell, cap) {
			return t, p, videobilling.ErrPricingUnavailable
		}
		retail, re := parsePrice(c.RetailUSDPer1M)
		cost, ce := parsePrice(c.SupplierCNYPer1M)
		risk, ke := parsePrice(c.RiskMultiplier)
		if re != nil || ce != nil || ke != nil {
			return t, p, videobilling.ErrInvalid
		}
		in.Prices = append(in.Prices, videobilling.PricingRule{RuleID: fmt.Sprintf("%s-%d", cfg.PricingVersion, i), Version: cfg.PricingVersion, PricingKey: videobilling.PricingKey{Model: cfg.PublicModel, Mode: c.Mode, Resolution: c.Resolution, InputVideo: c.InputVideo, ServiceTier: c.ServiceTier, Draft: c.Draft}, RetailUSDPer1M: retail, SupplierCNYPer1M: cost, RiskMultiplier: risk, MinimumRuleVersion: c.MinimumRuleVersion, FormulaVersion: t.FormulaVersion, SourceHash: p.EvidenceSHA256, EffectiveFrom: now, Enabled: c.Enabled})
	}
	return t, p, nil
}

func importTemplateMinimum(tx *gorm.DB, p arkverification.Profile, actor uint64, now time.Time) error {
	matched, err := matchingVerificationMinimum(tx, p.Capability.PublicModel, p)
	if err != nil || matched {
		return err
	}
	_, err = videobilling.PublishMinimumTx(tx, videobilling.MinimumRuleSet{Model: p.Capability.PublicModel, Version: p.MinimumVersion, SourceURL: p.SourceURL, EvidenceHash: p.EvidenceSHA256, ObservedAt: p.ObservedAt, VerifiedBy: actor, VerifiedAt: now}, verificationMinimumCells(p))
	return err
}
