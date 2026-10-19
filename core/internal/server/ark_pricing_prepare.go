package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/arkpricetemplate"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// Source references identify provenance, not proof of capability or minimum coverage.
// RawSHA256 hashes a source; SHA256 hashes this ordered, typed reference set.
type pricingSourceRef struct {
	Kind      string `json:"kind"`
	CellKey   string `json:"cell_key"`
	Version   string `json:"version"`
	SourceID  string `json:"source_id"`
	Row       int    `json:"row"`
	RawSHA256 string `json:"raw_sha256"`
}
type pricingSourceSet struct {
	SchemaVersion int                `json:"schema_version"`
	SHA256        string             `json:"sha256"`
	Refs          []pricingSourceRef `json:"refs"`
}
type pricingDiff struct {
	Key    string       `json:"key"`
	Change string       `json:"change"`
	Before *pricingCell `json:"before"`
	After  *pricingCell `json:"after"`
}

func currentPricingRules(tx *gorm.DB, cfg video.ModelConfig) ([]videobilling.PricingRule, error) {
	rows := []videobilling.PricingRule{}
	e := tx.Where("model = ? AND provider_scope_id = ? AND version = ?", cfg.PublicModel, cfg.ProviderScopeID, cfg.PricingVersion).Order("id").Find(&rows).Error
	seen := map[string]bool{}
	for _, r := range rows {
		k := priceCellKey(cellFromRule(r))
		if seen[k] {
			return nil, videobilling.ErrConflict
		}
		seen[k] = true
	}
	return rows, e
}
func requireCompletePricingMatrix(cells []pricingCell, current []videobilling.PricingRule) error {
	if len(cells) == 0 || len(cells) > 100 {
		return videobilling.ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range cells {
		k := priceCellKey(c)
		if seen[k] {
			return videobilling.ErrConflict
		}
		seen[k] = true
	}
	for _, r := range current {
		if !seen[priceCellKey(cellFromRule(r))] {
			return fmt.Errorf("complete pricing matrix required: %w", videobilling.ErrConflict)
		}
	}
	return nil
}
func cellFromRule(p videobilling.PricingRule) pricingCell {
	return pricingCell{Mode: p.Mode, Resolution: p.Resolution, InputVideo: p.InputVideo, ServiceTier: p.ServiceTier, Draft: p.Draft, Enabled: p.Enabled, RetailUSDPer1M: p.RetailUSDPer1M.String(), SupplierCNYPer1M: p.SupplierCNYPer1M.String(), RiskMultiplier: p.RiskMultiplier.String(), MinimumRuleVersion: p.MinimumRuleVersion}
}
func validatePricingBinding(tx *gorm.DB, cfg video.ModelConfig) error {
	var scope video.ProviderScope
	if e := tx.Where("id = ? AND enabled = ?", cfg.ProviderScopeID, true).Take(&scope).Error; e != nil {
		return e
	}
	if scope.Region != "cn-beijing" || scope.AccountRef == "" || scope.ProjectName == "" {
		return videobilling.ErrPricingUnavailable
	}
	var ch relay.Channel
	if e := tx.Select("id,provider,base_url,status,owner_org_id").Where("id = ?", cfg.ChannelID).Take(&ch).Error; e != nil {
		return e
	}
	if !cfg.Enabled || cfg.VerifiedAt == nil || ch.Status != 1 || ch.OwnerOrgID != 0 || (ch.Provider != "volcengine" && ch.Provider != "modelark") || ch.BaseURL != "https://ark.cn-beijing.volces.com/api/v3" {
		return videobilling.ErrPricingUnavailable
	}
	return nil
}
func resolvePricingSources(tx *gorm.DB, cfg video.ModelConfig, cells []pricingCell, current []videobilling.PricingRule) (pricingSourceSet, string, error) {
	out := pricingSourceSet{SchemaVersion: 1, Refs: []pricingSourceRef{}}
	ref, e := arkpricecatalog.ReferenceCandidates(cfg.PublicModel)
	if e != nil {
		return out, "", e
	}
	sourceMap := map[string]string{}
	for _, s := range ref.Sources {
		sourceMap[s.ID] = s.RawSHA256
	}
	currentMap := map[string]videobilling.PricingRule{}
	for _, p := range current {
		currentMap[priceCellKey(cellFromRule(p))] = p
	}
	minimums := []videobilling.MinimumRuleSet{}
	if e := tx.Where("model = ?", cfg.PublicModel).Find(&minimums).Error; e != nil {
		return out, "", e
	}
	minMap := map[string]videobilling.MinimumRuleSet{}
	for _, m := range minimums {
		minMap[m.Version] = m
	}
	evidence := ""
	for _, c := range cells {
		k := priceCellKey(c)
		priceSource := false
		if p, ok := currentMap[k]; ok {
			priceSource = true
			out.Refs = append(out.Refs, pricingSourceRef{Kind: "current_rule", CellKey: k, Version: p.Version, SourceID: p.RuleID, RawSHA256: p.SourceHash})
		}
		for _, p := range ref.Prices {
			if p.Key == k {
				priceSource = true
				for _, r := range p.SourceRows {
					out.Refs = append(out.Refs, pricingSourceRef{Kind: "official_catalog", CellKey: k, Version: ref.CatalogVersion, SourceID: r.SourceID, Row: r.Row, RawSHA256: sourceMap[r.SourceID]})
				}
			}
		}
		if c.Enabled && !priceSource {
			return out, "", videobilling.ErrPricingUnavailable
		}
		if c.MinimumRuleVersion != "" {
			if m, ok := minMap[c.MinimumRuleVersion]; ok {
				out.Refs = append(out.Refs, pricingSourceRef{Kind: "verified_minimum", CellKey: k, Version: m.Version, SourceID: fmt.Sprint(m.ID), RawSHA256: m.EvidenceHash})
			}
		}
	}
	sealPricingSources(&out)
	// Keep legacy evidence_hash a source digest, never a synthesized body/set digest.
	for _, r := range out.Refs {
		b, e := hex.DecodeString(r.RawSHA256)
		if e == nil && len(b) == 32 {
			evidence = r.RawSHA256
			break
		}
	}
	if evidence == "" {
		return out, "", videobilling.ErrPricingUnavailable
	}
	return out, evidence, nil
}
func sealPricingSources(out *pricingSourceSet) {
	sort.Slice(out.Refs, func(i, j int) bool {
		a, _ := json.Marshal(out.Refs[i])
		b, _ := json.Marshal(out.Refs[j])
		return string(a) < string(b)
	})
	raw, _ := json.Marshal(struct {
		SchemaVersion int                `json:"schema_version"`
		Refs          []pricingSourceRef `json:"refs"`
	}{out.SchemaVersion, out.Refs})
	h := sha256.Sum256(raw)
	out.SHA256 = hex.EncodeToString(h[:])
}
func samePricingSources(a, b pricingSourceSet) bool {
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	return string(ar) == string(br)
}
func preparePricingDiff(before, after []pricingCell) []pricingDiff {
	old := map[string]pricingCell{}
	for _, c := range before {
		old[priceCellKey(c)] = c
	}
	out := []pricingDiff{}
	for _, c := range after {
		k := priceCellKey(c)
		d := pricingDiff{Key: k, Change: "added", After: &c}
		if b, ok := old[k]; ok {
			d.Before = &b
			d.Change = "changed"
			beforeFields, afterFields := b, c
			beforeFields.Available = false
			beforeFields.UnavailableReason = ""
			afterFields.Available = false
			afterFields.UnavailableReason = ""
			if beforeFields == afterFields {
				d.Change = "unchanged"
			} else if b.Enabled && !c.Enabled {
				d.Change = "disabled"
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func (s *Server) prepareArkPricingConsole(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	var in struct {
		ExpectedConfigVersion string                      `json:"expected_config_version"`
		ChannelID             uint64                      `json:"channel_id"`
		Cells                 []pricingCell               `json:"cells"`
		TemplateRef           *arkpricetemplate.Reference `json:"template_ref,omitempty"`
	}
	if videoAdminDecode(c, &in) != nil {
		fail(c, 422, "invalid complete pricing matrix")
		return
	}
	var request pricingRequest
	var diff []pricingDiff
	e := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		cfg, cap, _, _, e := loadPricingContext(tx, c.Param("model"), true)
		if e != nil {
			return e
		}
		if cfg.Version != in.ExpectedConfigVersion || cfg.ChannelID != in.ChannelID {
			return videobilling.ErrConflict
		}
		if e := validatePricingBinding(tx, cfg); e != nil {
			return e
		}
		current, e := currentPricingRules(tx, cfg)
		if e != nil {
			return e
		}
		if e := requireCompletePricingMatrix(in.Cells, current); e != nil {
			return e
		}
		source, evidence, e := resolvePricingSources(tx, cfg, in.Cells, current)
		if e != nil {
			return e
		}
		if e := templatePricingSource(tx, cfg, in.TemplateRef, &source); e != nil {
			return e
		}
		var token [16]byte
		if _, e := rand.Read(token[:]); e != nil {
			return e
		}
		version := hex.EncodeToString(token[:])
		request = pricingRequest{ExpectedConfigVersion: cfg.Version, ConfigVersion: "cfg-" + version, PricingVersion: "price-" + version, EvidenceHash: evidence, Cells: in.Cells, SourceSet: &source, TemplateRef: in.TemplateRef}
		diff = preparePricingDiff(priceMatrix(cap, current), in.Cells)
		return nil
	})
	if e != nil {
		fail(c, 409, "pricing preparation rejected: stale context, incomplete matrix or unavailable binding")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"request": request, "diff": diff, "sources": request.SourceSet.Refs})
}
