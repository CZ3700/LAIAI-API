package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricetemplate"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func seedPricingTemplates(t *testing.T, db *gorm.DB) map[string]arkpricetemplate.Template {
	t.Helper()
	if err := db.Exec(`CREATE TABLE video_pricing_templates(id INTEGER PRIMARY KEY AUTOINCREMENT,version TEXT NOT NULL,model TEXT NOT NULL,body_hash TEXT NOT NULL,body_json TEXT NOT NULL,created_at DATETIME NOT NULL,UNIQUE(version,model));`).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "arkpricetemplate", "data", "seedance-20261019-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var templates []arkpricetemplate.Template
	if err := json.Unmarshal(raw, &templates); err != nil {
		t.Fatal(err)
	}
	out := map[string]arkpricetemplate.Template{}
	for _, template := range templates {
		body, _ := json.Marshal(template)
		digest := sha256.Sum256(body)
		row := arkpricetemplate.Record{Version: template.Version, Model: template.Model, BodyJSON: string(body), BodyHash: hex.EncodeToString(digest[:]), CreatedAt: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		loaded, err := arkpricetemplate.Load(db, row.Model, row.Version)
		if err != nil {
			t.Fatal(err)
		}
		out[row.Model] = loaded
	}
	return out
}

func templateCall(t *testing.T, f *gin999, path, token, key string, body any, want int) []byte {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s got %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w.Body.Bytes()
}

func TestPricingTemplateReadOnlyPreviewPermissionsAndMerge(t *testing.T) {
	f, db, tokens := pricingCatalogFixture(t)
	templates := seedPricingTemplates(t, db)
	var old videobilling.PricingRule
	if err := db.First(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&old).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	hidden := old
	hidden.ID, hidden.RuleID, hidden.Mode, hidden.Enabled = 0, "hidden-template", "extend", false
	if err := db.Create(&hidden).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/video/models/seedance-2.0/pricing/template-preview"
	in := map[string]any{"channel_id": 99, "expected_config_version": "config-v1", "template_version": arkpricetemplate.InitialVersion, "mode": "fill_missing"}
	for _, actor := range []uint64{2, 4} {
		catalogGET(t, f, "/api/admin/video/pricing-templates?model=seedance-2.0", tokens[actor], "", 403)
		templateCall(t, f, path, tokens[actor], "", in, 403)
	}
	catalogGET(t, f, "/api/admin/video/pricing-templates?model=seedance-2.0", tokens[3], "", 401)
	templateCall(t, f, path, tokens[3], "", in, 401)
	catalogGET(t, f, "/api/admin/video/pricing-templates", "", "fixture-emergency-token", 403)
	catalogGET(t, f, "/api/admin/video/pricing-templates?model=seedance-1.5", tokens[1], "", 503)
	before := verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_configuration_revisions", "video_pricing_publications", "video_pricing_templates", "audit_logs")
	catalogGET(t, f, "/api/admin/video/pricing-templates?model=seedance-2.0", tokens[1], "", 200)
	for _, mode := range []string{"fill_missing", "replace"} {
		in["mode"] = mode
		var out struct {
			Cells       []pricingCell              `json:"cells"`
			Diff        []pricingDiff              `json:"diff"`
			TemplateRef arkpricetemplate.Reference `json:"template_ref"`
		}
		if err := json.Unmarshal(templateCall(t, f, path, tokens[1], "", in, 200), &out); err != nil {
			t.Fatal(err)
		}
		if out.TemplateRef != templates[ark.Model20].Reference() || len(out.Cells) != 9 || len(out.Diff) != 9 {
			t.Fatalf("template identity or complete matrix lost: %#v", out)
		}
		for _, c := range out.Cells {
			switch {
			case c.Mode == "extend":
				if c.Enabled || c.RetailUSDPer1M != "9.125" || c.SupplierCNYPer1M != "999" {
					t.Fatal("hidden row overwritten")
				}
			case c.Resolution == "480p" && !c.InputVideo:
				if c.Enabled {
					t.Fatal("disabled row re-enabled")
				}
				if mode == "fill_missing" && (c.RetailUSDPer1M != "9.125" || c.SupplierCNYPer1M != "999") {
					t.Fatal("existing price overwritten")
				}
				if mode == "replace" && c.SupplierCNYPer1M == "999" {
					t.Fatal("explicit replace ignored")
				}
			case c.Resolution == "480p" && c.InputVideo:
				if c.Enabled || c.MinimumRuleVersion != templates[ark.Model20].MinimumRuleVersion {
					t.Fatal("missing reference combination not filled")
				}
			default:
				if c.Enabled {
					t.Fatal("unsupported capability opened by template")
				}
			}
		}
	}
	in["channel_id"] = 100
	templateCall(t, f, path, tokens[1], "", in, 409)
	in["channel_id"], in["expected_config_version"] = 99, "stale"
	templateCall(t, f, path, tokens[1], "", in, 409)
	in["expected_config_version"], in["template_version"] = "config-v1", "missing"
	templateCall(t, f, path, tokens[1], "", in, 409)
	if before != verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_configuration_revisions", "video_pricing_publications", "video_pricing_templates", "audit_logs") {
		t.Fatal("template read/preview mutated state")
	}
}

func TestPricingTemplatePreparePublicationProvenanceReplayAndTamper(t *testing.T) {
	f, db, tokens := pricingCatalogFixture(t)
	templates := seedPricingTemplates(t, db)
	template := templates[ark.Model20]
	p, err := arkverification.Load(ark.Model20)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return importTemplateMinimum(tx, p, 1, time.Now().UTC()) }); err != nil {
		t.Fatal(err)
	}
	base := "/api/admin/video/models/seedance-2.0/pricing"
	var preview struct {
		Cells []pricingCell              `json:"cells"`
		Ref   arkpricetemplate.Reference `json:"template_ref"`
	}
	if err := json.Unmarshal(templateCall(t, f, base+"/template-preview", tokens[1], "", map[string]any{"channel_id": 99, "expected_config_version": "config-v1", "template_version": template.Version, "mode": "fill_missing"}, 200), &preview); err != nil {
		t.Fatal(err)
	}
	in := map[string]any{"channel_id": 99, "expected_config_version": "config-v1", "cells": preview.Cells, "template_ref": preview.Ref}
	var prepared struct {
		Request pricingRequest `json:"request"`
	}
	if err := json.Unmarshal(templateCall(t, f, base+"/prepare", tokens[1], "", in, 200), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Request.TemplateRef == nil || *prepared.Request.TemplateRef != template.Reference() {
		t.Fatal("template ref omitted")
	}
	found := false
	for _, r := range prepared.Request.SourceSet.Refs {
		if r.Kind == "pricing_template" && r.Version == template.Version && r.RawSHA256 == template.BodySHA256 {
			found = true
		}
	}
	if !found || prepared.Request.EvidenceHash == template.BodySHA256 {
		t.Fatal("template source missing or confused with official evidence")
	}
	bad := prepared.Request
	badRef := *bad.TemplateRef
	badRef.Model = ark.Model25
	bad.TemplateRef = &badRef
	templateCall(t, f, base+"/validate", tokens[1], "", bad, 422)
	badRef = *prepared.Request.TemplateRef
	badRef.SHA256 = strings.Repeat("0", 64)
	templateCall(t, f, base+"/publications", tokens[1], "forged", bad, 409)
	templateCall(t, f, base+"/validate", tokens[1], "", prepared.Request, 200)
	scopeBefore := verificationSnapshot(t, db, "video_provider_scopes")
	templateCall(t, f, base+"/publications", tokens[1], "template-publish", prepared.Request, 200)
	templateCall(t, f, base+"/publications", tokens[1], "template-publish", prepared.Request, 200)
	var publications []pricingPublication
	if err := db.Find(&publications).Error; err != nil {
		t.Fatal(err)
	}
	if len(publications) != 1 || !strings.Contains(publications[0].SnapshotJSON, template.BodySHA256) || !strings.Contains(publications[0].SnapshotJSON, "pricing_template") {
		t.Fatal("publication provenance/replay lost")
	}
	if scopeBefore != verificationSnapshot(t, db, "video_provider_scopes") {
		t.Fatal("template changed target FX")
	}
	if err := db.Model(&arkpricetemplate.Record{}).Where("model = ?", ark.Model20).Update("body_hash", strings.Repeat("0", 64)).Error; err != nil {
		t.Fatal(err)
	}
	templateCall(t, f, base+"/publications", tokens[1], "template-publish", prepared.Request, 409)
	catalogGET(t, f, "/api/admin/video/pricing-templates?model=seedance-2.0", tokens[1], "", 503)
}

func TestInitialPricingTemplatePublicationUsesTargetScopeAndAtomicEvidence(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model20Fast, ark.Model20Mini, ark.Model25} {
		t.Run(model, func(t *testing.T) {
			f, db, tokens := pricingCatalogFixture(t)
			templates := seedPricingTemplates(t, db)
			if model == ark.Model20 {
				if err := db.Where("public_model = ?", model).Delete(&video.ModelConfig{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Where("model = ?", model).Delete(&videobilling.PricingRule{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			configureDirectVideoRuntime(t, f.srv)
			if err := db.Exec(`CREATE TABLE video_quota_policies(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,scope TEXT NOT NULL,owner_id INTEGER NOT NULL,model TEXT NOT NULL,metric TEXT NOT NULL,period TEXT NOT NULL,timezone TEXT NOT NULL,limit_amount NUMERIC,limit_units INTEGER,version TEXT NOT NULL,enabled INTEGER NOT NULL);`).Error; err != nil {
				t.Fatal(err)
			}
			var scope video.ProviderScope
			if err := db.First(&scope, 1).Error; err != nil {
				t.Fatal(err)
			}
			ref := templates[model].Reference()
			limit := decimal.NewFromInt(20)
			in := videoPublication{Configuration: video.ModelConfig{PublicModel: model, Version: "template-config-v1", PricingVersion: "template-prices-v1", ChannelID: 99, Enabled: true, NoChargeStatuses: "failed,cancelled"}, Scope: scope, PricingTemplate: &ref, SupplierBudgets: []videobilling.QuotaPolicy{{Scope: videobilling.ScopeProvider, Metric: videobilling.MetricCNY, Period: videobilling.PeriodDay, Timezone: "UTC", LimitAmount: &limit, Version: "template-budget-v1"}, {Scope: videobilling.ScopeProvider, Metric: videobilling.MetricCNY, Period: videobilling.PeriodMonth, Timezone: "UTC", LimitAmount: &limit, Version: "template-budget-v1"}}}
			path := "/api/admin/video/configurations"
			for _, actor := range []uint64{2, 4} {
				templateCall(t, f, path, tokens[actor], "first-template", in, 403)
			}
			before := verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies", "audit_logs")
			bad := in
			bad.SupplierBudgets = nil
			templateCall(t, f, path, tokens[1], "first-template", bad, 422)
			bad = in
			bad.Scope.FXVersion, bad.Scope.FXUSDPerCNY = "template-must-not-reprice-fx", "0.2"
			templateCall(t, f, path, tokens[1], "first-template", bad, 422)
			bad = in
			bad.Prices = []videobilling.PricingRule{{}}
			templateCall(t, f, path, tokens[1], "first-template", bad, 422)
			bad = in
			bad.Configuration.ChannelID = 100
			templateCall(t, f, path, tokens[1], "first-template", bad, 422)
			if before != verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies", "audit_logs") {
				t.Fatal("failed template publication mutated state")
			}
			// A failure after minimum cells, quotas and prices were inserted must
			// roll the entire first-admission operation back.
			if err := db.Callback().Create().Before("gorm:create").Register("template:receipt_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "video_pricing_publications" {
					tx.AddError(assertCatalogWrite{})
				}
			}); err != nil {
				t.Fatal(err)
			}
			templateCall(t, f, path, tokens[1], "first-template", in, 422)
			if err := db.Callback().Create().Remove("template:receipt_failure"); err != nil {
				t.Fatal(err)
			}
			if before != verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies", "audit_logs") {
				t.Fatal("failed late receipt did not roll back prices/evidence/budgets")
			}
			p, err := arkverification.Load(model)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Transaction(func(tx *gorm.DB) error { return importTemplateMinimum(tx, p, 1, time.Now().UTC()) }); err != nil {
				t.Fatal(err)
			}
			var minimum videobilling.MinimumRuleSet
			if err := db.Where("model = ?", model).Take(&minimum).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&videobilling.MinimumRuleCell{}).Where("rule_set_id = ?", minimum.ID).Update("minimum_tokens", 1).Error; err != nil {
				t.Fatal(err)
			}
			conflictBefore := verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies", "audit_logs")
			templateCall(t, f, path, tokens[1], "first-template", in, 422)
			if conflictBefore != verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies", "audit_logs") {
				t.Fatal("conflicting minimum was overwritten or partially published")
			}
			if err := db.Where("rule_set_id = ?", minimum.ID).Delete(&videobilling.MinimumRuleCell{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Delete(&minimum).Error; err != nil {
				t.Fatal(err)
			}
			templateCall(t, f, path, tokens[1], "first-template", in, 201)
			var verified arkVerificationStatus
			verification := catalogGET(t, f, "/api/admin/video/models/"+model+"/verification", tokens[1], "", 200)
			if err := json.Unmarshal(verification.Body.Bytes(), &verified); err != nil {
				t.Fatal(err)
			}
			if !verified.Verified {
				t.Fatal("fresh template publication requires redundant verification sync")
			}
			after := verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies", "audit_logs")
			templateCall(t, f, path, tokens[1], "first-template", in, 201)
			if after != verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies", "audit_logs") {
				t.Fatal("replayed template publication wrote again")
			}
			var cfg video.ModelConfig
			if err := db.Where("public_model = ?", model).Take(&cfg).Error; err != nil {
				t.Fatal(err)
			}
			prices, err := currentPricingRules(db, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(prices) != len(templates[model].Cells) || cfg.ChannelID != 99 || cfg.ProviderScopeID != 1 {
				t.Fatal("template binding or matrix incomplete")
			}
			for _, p := range prices {
				matched := false
				for _, c := range templates[model].Cells {
					if priceCellKey(cellFromRule(p)) == c.Key() {
						matched = true
						if !p.RetailUSDPer1M.Equal(decimal.RequireFromString(c.RetailUSDPer1M)) || !p.Enabled || p.MinimumRuleVersion != c.MinimumRuleVersion {
							t.Fatal("template retail recalculated or minimum lost")
						}
					}
				}
				if !matched {
					t.Fatal("unexpected published dimension")
				}
			}
			var updatedScope video.ProviderScope
			if err := db.First(&updatedScope, 1).Error; err != nil {
				t.Fatal(err)
			}
			if !decimal.RequireFromString(updatedScope.FXUSDPerCNY).Equal(decimal.RequireFromString(scope.FXUSDPerCNY)) || updatedScope.FXVersion != scope.FXVersion {
				t.Fatal("source FX replaced production FX")
			}
			var publication pricingPublication
			if err := db.Where("model = ?", model).Take(&publication).Error; err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(publication.SnapshotJSON, ref.SHA256) || strings.Contains(publication.SnapshotJSON, "account_ref") {
				t.Fatal("missing template trace or scope identity exposed in snapshot")
			}
			var audit AuditLog
			if err := db.Where("action = ? AND target = ?", "video.configuration.publish", model).Take(&audit).Error; err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(audit.Detail, ref.SHA256) || !strings.Contains(audit.Detail, ref.Version) {
				t.Fatal("template audit identity missing")
			}
			var snap pricingRequest
			if err := json.Unmarshal([]byte(publication.SnapshotJSON), &snap); err != nil {
				t.Fatal(err)
			}
			if snap.EvidenceHash != p.EvidenceSHA256 {
				t.Fatal("first publication snapshot differs from published rule evidence")
			}
			templateCall(t, f, "/api/admin/video/models/"+model+"/pricing/publications", tokens[1], "first-template", snap, 409)
			if after != verificationSnapshot(t, db, "video_model_configs", "video_provider_scopes", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies", "audit_logs") {
				t.Fatal("cross-endpoint idempotency key changed state")
			}
			in.Configuration.Version, in.Configuration.PricingVersion = "template-config-v2", "template-price-v2"
			templateCall(t, f, path, tokens[1], "existing-template-forbidden", in, 422)
		})
	}
}

func TestPricingTemplateMissingMigrationFailsClosed(t *testing.T) {
	f, db, tokens, in := prepareFixture(t)
	base := "/api/admin/video/models/seedance-2.0/pricing"
	catalogGET(t, f, "/api/admin/video/pricing-templates?model=seedance-2.0", tokens[1], "", 503)
	templateCall(t, f, base+"/template-preview", tokens[1], "", map[string]any{"channel_id": 99, "expected_config_version": "config-v1", "template_version": arkpricetemplate.InitialVersion, "mode": "fill_missing"}, 409)
	in["template_ref"] = arkpricetemplate.Reference{Model: ark.Model20, Version: arkpricetemplate.InitialVersion, SHA256: strings.Repeat("a", 64)}
	before := verificationSnapshot(t, db, "video_pricing_rules", "video_model_configs", "video_pricing_publications")
	templateCall(t, f, base+"/prepare", tokens[1], "", in, 409)
	if before != verificationSnapshot(t, db, "video_pricing_rules", "video_model_configs", "video_pricing_publications") {
		t.Fatal("missing migration mutated state")
	}
	delete(in, "template_ref")
	templateCall(t, f, base+"/prepare", tokens[1], "", in, 200)
}

func TestPricingTemplateRejectsIncompatibleMeteringAndConflictingMinimum(t *testing.T) {
	for _, issue := range []string{"fps", "bounds", "input_duration", "minimum_cells", "minimum_digest"} {
		t.Run(issue, func(t *testing.T) {
			f, db, tokens, in := prepareFixture(t)
			templates := seedPricingTemplates(t, db)
			p, err := arkverification.Load(ark.Model20)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Transaction(func(tx *gorm.DB) error { return importTemplateMinimum(tx, p, 1, time.Now().UTC()) }); err != nil {
				t.Fatal(err)
			}
			in["template_ref"] = templates[ark.Model20].Reference()
			base := "/api/admin/video/models/seedance-2.0/pricing"
			var prepared struct {
				Request pricingRequest `json:"request"`
			}
			if err := json.Unmarshal(templateCall(t, f, base+"/prepare", tokens[1], "", in, 200), &prepared); err != nil {
				t.Fatal(err)
			}
			var cfg video.ModelConfig
			if err := db.Where("public_model = ?", ark.Model20).Take(&cfg).Error; err != nil {
				t.Fatal(err)
			}
			switch issue {
			case "fps":
				var cap ark.Capability
				if err := json.Unmarshal([]byte(cfg.CapabilityJSON), &cap); err != nil {
					t.Fatal(err)
				}
				cap.FPS = 12
				raw, _ := json.Marshal(cap)
				if err := db.Model(&cfg).Update("capability_json", string(raw)).Error; err != nil {
					t.Fatal(err)
				}
			case "bounds":
				if err := db.Model(&cfg).Update("bounds_json", `{"480p/1:1":{"width":320,"height":320}}`).Error; err != nil {
					t.Fatal(err)
				}
			case "input_duration":
				if err := db.Model(&cfg).Update("max_input_video_duration_ms", 7500).Error; err != nil {
					t.Fatal(err)
				}
			case "minimum_cells":
				if err := db.Model(&videobilling.MinimumRuleCell{}).Where("id > 0").Update("minimum_tokens", 1).Error; err != nil {
					t.Fatal(err)
				}
			case "minimum_digest":
				if err := db.Model(&videobilling.MinimumRuleSet{}).Where("model = ?", ark.Model20).Update("evidence_hash", strings.Repeat("a", 64)).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := verificationSnapshot(t, db, "video_pricing_rules", "video_model_configs", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "audit_logs")
			templateCall(t, f, base+"/template-preview", tokens[1], "", map[string]any{"channel_id": 99, "expected_config_version": "config-v1", "template_version": arkpricetemplate.InitialVersion, "mode": "fill_missing"}, 409)
			templateCall(t, f, base+"/prepare", tokens[1], "", in, 409)
			templateCall(t, f, base+"/validate", tokens[1], "", prepared.Request, 422)
			templateCall(t, f, base+"/publications", tokens[1], "incompatible-template", prepared.Request, 409)
			if before != verificationSnapshot(t, db, "video_pricing_rules", "video_model_configs", "video_pricing_publications", "video_minimum_rule_sets", "video_minimum_rule_cells", "audit_logs") {
				t.Fatal("incompatible template operation changed state")
			}
		})
	}
}
