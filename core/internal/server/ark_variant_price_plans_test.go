package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const variantPlanSchema = `CREATE TABLE video_variant_price_plans (
 id INTEGER PRIMARY KEY AUTOINCREMENT,channel_id INTEGER NOT NULL,model TEXT NOT NULL,
 upstream_model TEXT NOT NULL,canonical_model TEXT NOT NULL,provider_scope_id INTEGER NOT NULL,
 version TEXT NOT NULL,created_by INTEGER NOT NULL,idempotency_key TEXT NOT NULL,body_hash TEXT NOT NULL,
 proposal_json TEXT NOT NULL,context_json TEXT NOT NULL,created_at DATETIME NOT NULL,
 UNIQUE(created_by,idempotency_key),UNIQUE(channel_id,model,version));`

func variantPlanFixture(t *testing.T) (*gin999, *gorm.DB, map[uint64]string) {
	t.Helper()
	f, db, tokens := pricingCatalogFixture(t)
	arkBillMust(t, db.Exec(variantPlanSchema).Error)
	arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Updates(map[string]any{
		"models":         "seedance-2.0,seedance-2.0-fast,seedance-2.0-mini,doubao-seedance-2-0-fast-260128,doubao-seedance-2-0-mini-260615",
		"model_mapping":  `{"seedance-2.0-fast":"doubao-seedance-2-0-fast-260128","seedance-2.0-mini":"doubao-seedance-2-0-mini-260615"}`,
		"credential_enc": "do-not-read-credential-column",
	}).Error)
	return f, db, tokens
}

func variantCall(t *testing.T, engine http.Handler, method, path, token, key string, body any, want int) variantPricePlanView {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var out variantPricePlanView
	if want == 200 {
		arkBillMust(t, json.Unmarshal(w.Body.Bytes(), &out))
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("missing private cache policy")
		}
		if strings.Contains(w.Body.String(), "do-not-read-credential-column") || strings.Contains(w.Body.String(), "credential_enc") {
			t.Fatal("credential exposed")
		}
	}
	return out
}

func TestVariantPricePlansExactPreviewSaveReplayAndIsolation(t *testing.T) {
	f, db, tokens := variantPlanFixture(t)
	immutable := []string{"video_model_configs", "video_pricing_rules", "video_provider_scopes", "channels", "ledger_accounts", "ledger_holds", "ledger_journal", "api_keys"}
	before := verificationSnapshot(t, db, immutable...)
	publicBefore := catalogGET(t, f, "/api/pricing", "", "", 200).Body.String()
	for _, tc := range []struct{ model, canonical, plain, input string }{
		{"seedance-2.0-fast", "seedance-2.0-fast", "5.180000000000", "3.080000000000"},
		{"doubao-seedance-2-0-mini-260615", "seedance-2.0-mini", "3.220000000000", "1.960000000000"},
	} {
		path := "/api/admin/video/variant-price-plans/99/" + tc.model
		preview := variantCall(t, f.engine, "GET", path, tokens[1], "", nil, 200)
		if preview.Invocable || preview.AdapterStatus != "not_integrated" || preview.Saved != nil || preview.SavedMatchesProposal || preview.CanonicalModel != tc.canonical || len(preview.ContextSHA256) != 64 {
			t.Fatal("wrong preview identity", preview)
		}
		p := preview.Proposal
		if p.MinimumStatus != "unverified" || p.RiskSource != "existing_standard_2_0_policy" || len(p.RiskSourceRules) != 1 || p.RiskSourceRules[0].RuleID != "rule-current" || len(p.Sources) != 2 || len(p.Prices) != 4 || p.FXUSDPerCNY != "0.140000000000" {
			t.Fatal("source metadata incomplete", p)
		}
		for _, price := range p.Prices {
			want := tc.plain
			if price.InputVideo {
				want = tc.input
			}
			if price.RetailUSDPer1M != want || price.RiskMultiplier != "4" || price.SupplierCNYPer1M == "999" {
				t.Fatal("wrong conversion/risk", price)
			}
		}
		body := map[string]string{"expected_context_sha256": preview.ContextSHA256}
		saved := variantCall(t, f.engine, "POST", path, tokens[1], tc.model, body, 200)
		if saved.Saved == nil || !saved.SavedMatchesProposal || saved.Invocable || saved.Saved.CreatedBy != 1 || saved.ContextSHA256 == preview.ContextSHA256 {
			t.Fatal("bad save receipt", saved)
		}
		again := variantCall(t, f.engine, "POST", path, tokens[1], tc.model, body, 200)
		if verificationDigest(saved) != verificationDigest(again) {
			t.Fatal("replay changed receipt")
		}
		read := variantCall(t, f.engine, "GET", path, tokens[1], "", nil, 200)
		if verificationDigest(read) != verificationDigest(saved) {
			t.Fatal("save and read disagree")
		}
		variantCall(t, f.engine, "POST", path, tokens[1], "stale-"+tc.model, body, 409)
		variantCall(t, f.engine, "POST", path, tokens[1], tc.model, map[string]string{"expected_context_sha256": saved.ContextSHA256}, 409)
	}
	var plans, audits int64
	arkBillMust(t, db.Model(&variantPricePlan{}).Count(&plans).Error)
	arkBillMust(t, db.Model(&AuditLog{}).Where("action=?", "video.variant_price_plan.save").Count(&audits).Error)
	if plans != 2 || audits != 2 {
		t.Fatal("replay wrote duplicate plan or audit", plans, audits)
	}
	if before != verificationSnapshot(t, db, immutable...) {
		t.Fatal("plan mutated billing/capability state")
	}
	if publicBefore != catalogGET(t, f, "/api/pricing", "", "", 200).Body.String() {
		t.Fatal("plan changed public prices")
	}
	var rows []variantPricePlan
	arkBillMust(t, db.Find(&rows).Error)
	for _, row := range rows {
		if strings.Contains(row.ContextJSON, "credential") || strings.Contains(row.ProposalJSON, "minimum_rule_version") {
			t.Fatal("secret or invented minimum in plan")
		}
	}
}

func TestVariantPricePlansPermissionsAndStrictRequests(t *testing.T) {
	f, _, tokens := variantPlanFixture(t)
	path := "/api/admin/video/variant-price-plans/99/seedance-2.0-fast"
	for _, tc := range []struct {
		actor  uint64
		status int
	}{{0, 401}, {2, 403}, {3, 401}, {4, 403}} {
		for _, method := range []string{"GET", "POST"} {
			variantCall(t, f.engine, method, path, tokens[tc.actor], "key", map[string]string{"expected_context_sha256": strings.Repeat("a", 64)}, tc.status)
		}
	}
	catalogGET(t, f, path, "", "fixture-emergency-token", 403)
	preview := variantCall(t, f.engine, "GET", path, tokens[1], "", nil, 200)
	for _, body := range []any{map[string]any{"expected_context_sha256": preview.ContextSHA256, "enabled": true}, map[string]any{"expected_context_sha256": preview.ContextSHA256, "retail_usd_per_1m": "1"}, map[string]string{"expected_context_sha256": "invalid"}, map[string]string{}} {
		variantCall(t, f.engine, "POST", path, tokens[1], "key", body, 422)
	}
	variantCall(t, f.engine, "POST", path, tokens[1], "", map[string]string{"expected_context_sha256": preview.ContextSHA256}, 422)
	variantCall(t, f.engine, "GET", "/api/admin/video/variant-price-plans/0/x", tokens[1], "", nil, 422)
}

func TestVariantPricePlansSourceChangesRejectStalePreview(t *testing.T) {
	for _, change := range []string{"fx", "fx_time", "config", "cost", "risk", "mapping", "scope"} {
		t.Run(change, func(t *testing.T) {
			f, db, tokens := variantPlanFixture(t)
			path := "/api/admin/video/variant-price-plans/99/seedance-2.0-fast"
			p := variantCall(t, f.engine, "GET", path, tokens[1], "", nil, 200)
			switch change {
			case "fx":
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Updates(map[string]any{"fx_usd_per_cny": "0.15", "fx_version": "changed"}).Error)
			case "fx_time":
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Update("fx_observed_at", time.Now().UTC().Add(-time.Minute)).Error)
			case "config":
				arkBillMust(t, db.Model(&video.ModelConfig{}).Where("public_model=?", ark.Model20).Update("version", "changed").Error)
			case "cost":
				arkBillMust(t, db.Model(&videobilling.PricingRule{}).Where("rule_id=?", "rule-current").Update("supplier_cny_per_1m", 998).Error)
			case "risk":
				arkBillMust(t, db.Model(&videobilling.PricingRule{}).Where("rule_id=?", "rule-current").Update("risk_multiplier", 3).Error)
			case "mapping":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Update("model_mapping", `{"seedance-2.0-fast":"doubao-seedance-2-0-mini-260615"}`).Error)
			case "scope":
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Update("project_name", "changed").Error)
			}
			variantCall(t, f.engine, "POST", path, tokens[1], "stale", map[string]string{"expected_context_sha256": p.ContextSHA256}, 409)
			var count int64
			arkBillMust(t, db.Model(&variantPricePlan{}).Count(&count).Error)
			if count != 0 {
				t.Fatal("stale context created plan")
			}
		})
	}
}

func TestVariantPricePlansRejectUnverifiedIdentityAndPolicy(t *testing.T) {
	for _, change := range []string{"future", "undated", "other_variant", "unlisted", "byok", "provider", "base", "channel_disabled", "wrong_binding", "scope_disabled", "fx_zero", "fx_future", "fx_precision", "fx_overflow", "risk_zero", "risk_overflow", "risk_inconsistent", "expired", "future_rule", "disabled_rule", "source_hash"} {
		t.Run(change, func(t *testing.T) {
			f, db, tokens := variantPlanFixture(t)
			switch change {
			case "future", "undated", "other_variant":
				id := map[string]string{"future": "doubao-seedance-2-0-fast-270101", "undated": "seedance-2.0-fast", "other_variant": "doubao-seedance-2-0-mini-260615"}[change]
				mapping, _ := json.Marshal(map[string]string{"seedance-2.0-fast": id})
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Update("model_mapping", string(mapping)).Error)
			case "unlisted":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Update("models", "seedance-2.0").Error)
			case "byok":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Update("owner_org_id", 42).Error)
			case "provider":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Update("provider", "openai").Error)
			case "base":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Update("base_url", "https://proxy.invalid").Error)
			case "channel_disabled":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Update("status", 0).Error)
			case "wrong_binding":
				arkBillMust(t, db.Model(&video.ModelConfig{}).Where("public_model=?", ark.Model20).Update("channel_id", 100).Error)
			case "scope_disabled":
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Update("enabled", false).Error)
			case "fx_zero", "fx_precision", "fx_overflow":
				value := map[string]string{"fx_zero": "0", "fx_precision": "0.1234567890123", "fx_overflow": "999999999999.999999999999"}[change]
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Update("fx_usd_per_cny", value).Error)
			case "fx_future":
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Update("fx_observed_at", time.Now().Add(time.Hour)).Error)
			case "risk_zero", "risk_overflow":
				v := 0
				if change == "risk_overflow" {
					v = 101
				}
				arkBillMust(t, db.Model(&videobilling.PricingRule{}).Where("rule_id=?", "rule-current").Update("risk_multiplier", v).Error)
			case "risk_inconsistent":
				var r videobilling.PricingRule
				arkBillMust(t, db.First(&r).Error)
				r.ID = 0
				r.RuleID = "other"
				r.InputVideo = true
				r.RiskMultiplier = decimal.NewFromInt(3)
				arkBillMust(t, db.Create(&r).Error)
			case "expired":
				arkBillMust(t, db.Model(&videobilling.PricingRule{}).Where("rule_id=?", "rule-current").Update("effective_until", time.Now().Add(-time.Hour)).Error)
			case "future_rule":
				arkBillMust(t, db.Model(&videobilling.PricingRule{}).Where("rule_id=?", "rule-current").Update("effective_from", time.Now().Add(time.Hour)).Error)
			case "disabled_rule":
				arkBillMust(t, db.Model(&videobilling.PricingRule{}).Where("rule_id=?", "rule-current").Update("enabled", false).Error)
			case "source_hash":
				arkBillMust(t, db.Model(&videobilling.PricingRule{}).Where("rule_id=?", "rule-current").Update("source_hash", "unverified").Error)
			}
			variantCall(t, f.engine, "GET", "/api/admin/video/variant-price-plans/99/seedance-2.0-fast", tokens[1], "", nil, 409)
		})
	}
}

func TestVariantPricePlansPrecisionAuditRollbackAndHistoricalReplay(t *testing.T) {
	f, db, tokens := variantPlanFixture(t)
	arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Update("fx_usd_per_cny", "0.123456789012").Error)
	path := "/api/admin/video/variant-price-plans/99/seedance-2.0-fast"
	p := variantCall(t, f.engine, "GET", path, tokens[1], "", nil, 200)
	if p.Proposal.Prices[0].RetailUSDPer1M != "4.567901193444" {
		t.Fatal("decimal conversion lost precision", p.Proposal.Prices)
	}
	body := map[string]string{"expected_context_sha256": p.ContextSHA256}
	arkBillMust(t, db.Callback().Create().Before("gorm:create").Register("variant:audit_fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}))
	variantCall(t, f.engine, "POST", path, tokens[1], "same-key", body, 409)
	var count int64
	arkBillMust(t, db.Model(&variantPricePlan{}).Count(&count).Error)
	if count != 0 {
		t.Fatal("audit failure retained plan")
	}
	arkBillMust(t, db.Callback().Create().Remove("variant:audit_fail"))
	saved := variantCall(t, f.engine, "POST", path, tokens[1], "same-key", body, 200)
	arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Updates(map[string]any{"fx_usd_per_cny": "0.15", "fx_version": "v2"}).Error)
	fresh := variantCall(t, f.engine, "GET", path, tokens[1], "", nil, 200)
	if fresh.SavedMatchesProposal || fresh.Saved.ID != saved.Saved.ID {
		t.Fatal("changed FX incorrectly matches saved plan")
	}
	replayed := variantCall(t, f.engine, "POST", path, tokens[1], "same-key", body, 200)
	if verificationDigest(saved) != verificationDigest(replayed) {
		t.Fatal("policy change altered accepted replay")
	}
	newSaved := variantCall(t, f.engine, "POST", path, tokens[1], "next-key", map[string]string{"expected_context_sha256": fresh.ContextSHA256}, 200)
	if newSaved.Saved.ID == saved.Saved.ID || newSaved.Saved.Proposal.Prices[0].RetailUSDPer1M != "5.550000000000" {
		t.Fatal("new revision did not append")
	}
	variantCall(t, f.engine, "POST", "/api/admin/video/variant-price-plans/99/seedance-2.0-mini", tokens[1], "same-key", body, 409)
}

func TestVariantPricePlansSavedMatchIncludesBindingContext(t *testing.T) {
	for _, change := range []string{"scope_identity", "channel_mapping"} {
		t.Run(change, func(t *testing.T) {
			f, db, tokens := variantPlanFixture(t)
			path := "/api/admin/video/variant-price-plans/99/seedance-2.0-fast"
			preview := variantCall(t, f.engine, "GET", path, tokens[1], "", nil, 200)
			body := map[string]string{"expected_context_sha256": preview.ContextSHA256}
			saved := variantCall(t, f.engine, "POST", path, tokens[1], "saved-context", body, 200)
			if change == "scope_identity" {
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id=1").Update("project_name", "rebound-project").Error)
			} else {
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id=99").Update("model_mapping", `{"seedance-2.0-fast":"doubao-seedance-2-0-fast-260128","seedance-2.0-mini":"doubao-seedance-2-0-mini-260615","another-alias":"doubao-seedance-2-0-fast-260128"}`).Error)
			}
			current := variantCall(t, f.engine, "GET", path, tokens[1], "", nil, 200)
			if current.SavedMatchesProposal || current.ContextSHA256 == saved.ContextSHA256 || current.Saved.ID != saved.Saved.ID || verificationDigest(current.Proposal) != verificationDigest(saved.Proposal) {
				t.Fatal("binding-only change did not invalidate unchanged price preview")
			}
			replay := variantCall(t, f.engine, "POST", path, tokens[1], "saved-context", body, 200)
			if verificationDigest(replay) != verificationDigest(saved) {
				t.Fatal("binding change altered accepted replay")
			}
		})
	}
}

func TestMySQLVariantPricePlanConcurrentSaveAndAuditRollback(t *testing.T) {
	f := arkBillMySQLFixture(t, "running")
	ch := relay.Channel{Name: "variant " + f.uid, Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Models: "seedance-2.0,doubao-seedance-2-0-fast-260128", Status: 1, Weight: 1}
	arkBillMust(t, f.db.Create(&ch).Error)
	cap := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "variant-cap", Enabled: true, Verified: true, Resolutions: []string{"720p"}, Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: 15, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4}
	raw, _ := json.Marshal(cap)
	cfg := video.ModelConfig{PublicModel: ark.Model20, Version: "variant-" + f.uid, ProviderScopeID: f.scope, ChannelID: ch.ID, Enabled: true, CapabilityJSON: string(raw), PricingVersion: "v1", BoundsJSON: `{"720p/16:9":{"width":1280,"height":720}}`, NoChargeStatuses: "failed,cancelled", VerifiedAt: &f.now, UpdatedAt: f.now}
	arkBillMust(t, f.db.Save(&cfg).Error)
	su := f.r.Group("/api/admin", f.s.adminRequired(identity.PlatRoleAdmin), f.s.requireSuperadmin())
	f.s.registerArkVariantPricePlans(su)
	path := fmt.Sprintf("/api/admin/video/variant-price-plans/%d/doubao-seedance-2-0-fast-260128", ch.ID)
	p := variantCall(t, f.r, "GET", path, f.tokens[0], "", nil, 200)
	body, _ := json.Marshal(map[string]string{"expected_context_sha256": p.ContextSHA256})
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	ids := make(chan uint64, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", path, bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+f.tokens[0])
			r.Header.Set("Idempotency-Key", "variant-race-"+f.uid)
			w := httptest.NewRecorder()
			f.r.ServeHTTP(w, r)
			codes <- w.Code
			var out variantPricePlanView
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			if out.Saved != nil {
				ids <- out.Saved.ID
			}
		}()
	}
	wg.Wait()
	close(codes)
	close(ids)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent same-key response %d", code)
		}
	}
	var first uint64
	for id := range ids {
		if first != 0 && first != id {
			t.Fatal("duplicate concurrent plans")
		}
		first = id
	}
	var count int64
	arkBillMust(t, f.db.Model(&variantPricePlan{}).Where("channel_id=?", ch.ID).Count(&count).Error)
	if count != 1 {
		t.Fatal("concurrent append count", count)
	}
	arkBillMust(t, f.db.Transaction(func(tx *gorm.DB) error {
		_, _, _, e := loadVariantPricePlan(tx, ch.ID, "doubao-seedance-2-0-fast-260128", f.now, false)
		return e
	}))
	p = variantCall(t, f.r, "GET", path, f.tokens[0], "", nil, 200)
	arkBillMust(t, f.db.Callback().Create().Before("gorm:create").Register("variant:mysql_audit_fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}))
	variantCall(t, f.r, "POST", path, f.tokens[0], "variant-rollback-"+f.uid, map[string]string{"expected_context_sha256": p.ContextSHA256}, 409)
	arkBillMust(t, f.db.Callback().Create().Remove("variant:mysql_audit_fail"))
	arkBillMust(t, f.db.Model(&variantPricePlan{}).Where("channel_id=?", ch.ID).Count(&count).Error)
	if count != 1 {
		t.Fatal("MySQL rollback retained plan")
	}
}
