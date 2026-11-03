package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func verificationFixture(t *testing.T, model string) (*gin999, *gorm.DB, map[uint64]string, arkverification.Profile) {
	t.Helper()
	f, db, tokens := pricingCatalogFixture(t)
	p, e := arkverification.Load(model)
	if e != nil {
		t.Fatal(e)
	}
	var cfg video.ModelConfig
	if e := db.First(&cfg).Error; e != nil {
		t.Fatal(e)
	}
	var cap ark.Capability
	if e := json.Unmarshal([]byte(cfg.CapabilityJSON), &cap); e != nil {
		t.Fatal(e)
	}
	cap.PublicModel = model
	if model == ark.Model25 {
		cap.UpstreamModel = "doubao-seedance-2-5-260628"
	}
	if model == ark.Model20Fast {
		cap.UpstreamModel = "doubao-seedance-2-0-fast-260128"
	}
	if model == ark.Model20Mini {
		cap.UpstreamModel = "doubao-seedance-2-0-mini-260615"
	}
	raw, _ := json.Marshal(cap)
	if e := db.Model(&video.ModelConfig{}).Where("public_model = ?", cfg.PublicModel).Updates(map[string]any{"public_model": model, "capability_json": string(raw)}).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&videobilling.PricingRule{}).Where("model = ?", cfg.PublicModel).Update("model", model).Error; e != nil {
		t.Fatal(e)
	}
	// Actual ledger and independent supplier budget state must survive verification.
	for _, sql := range []string{
		`CREATE TABLE video_quota_policies(id INTEGER PRIMARY KEY,scope TEXT,owner_id INTEGER,limit_amount NUMERIC,version TEXT);`,
		`CREATE TABLE video_quota_buckets(id INTEGER PRIMARY KEY,policy_id INTEGER,reserved_amount NUMERIC,consumed_amount NUMERIC,blocked INTEGER);`,
		`INSERT INTO video_quota_policies VALUES(1,'provider',1,20,'budget-v1');`,
		`INSERT INTO video_quota_buckets VALUES(1,1,4.21,9.83,0);`,
		`INSERT INTO ledger_accounts(id,org_id,type,posted_balance,held_amount) VALUES(1,1,'cash',27.123,5.4);`,
		`INSERT INTO ledger_journal(id,idempotency_key,account_id,kind,delta,balance_after) VALUES(1,'fixture-credit',1,'credit',27.123,27.123);`,
	} {
		if e := db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	return f, db, tokens, p
}

func verificationSnapshot(t *testing.T, db *gorm.DB, tables ...string) string {
	t.Helper()
	out := map[string]any{}
	for _, table := range tables {
		var rows []map[string]any
		if e := db.Table(table).Find(&rows).Error; e != nil {
			t.Fatal(e)
		}
		out[table] = rows
	}
	raw, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}

func TestArkVerificationPreservesPricesBudgetsDefaultsAndReplay(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model25, ark.Model20Fast, ark.Model20Mini} {
		t.Run(model, func(t *testing.T) {
			f, db, tokens, p := verificationFixture(t, model)
			path := "/api/admin/video/models/" + model + "/verification"
			immutable := []string{"video_pricing_rules", "video_provider_scopes", "video_quota_policies", "video_quota_buckets", "channels", "ledger_accounts", "ledger_journal", "ledger_holds", "api_keys"}
			before := verificationSnapshot(t, db, immutable...)
			for _, method := range []string{"GET", "POST"} {
				for _, id := range []uint64{2, 3, 4} {
					w, _ := f.do(t, method, path, tokens[id], map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version})
					if w.Code != 403 && w.Code != 401 {
						t.Fatalf("%s role %d: %d", method, id, w.Code)
					}
				}
			}
			catalogGET(t, f, path, "", "fixture-emergency-token", 403)
			w, out := f.do(t, "GET", path, tokens[1], nil)
			if w.Code != 200 || out["verified"] != false {
				t.Fatal(w.Code, out)
			}
			if out["verification_kind"] != "official_documentation" || out["minimum_precision"] != "estimated" || out["live_tested"] != false {
				t.Fatal("verification provenance missing", out)
			}
			in := map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version}
			for i := 0; i < 2; i++ {
				w, out = f.do(t, "POST", path, tokens[1], in)
				if w.Code != 200 || out["verified"] != true {
					t.Fatalf("sync %d: %d %s", i, w.Code, w.Body.String())
				}
			}
			var cfg video.ModelConfig
			if e := db.Where("public_model = ?", model).Take(&cfg).Error; e != nil {
				t.Fatal(e)
			}
			var cap ark.Capability
			if e := json.Unmarshal([]byte(cfg.CapabilityJSON), &cap); e != nil {
				t.Fatal(e)
			}
			if cfg.PricingVersion != "price-v1" || cfg.ProviderScopeID != 1 || cfg.ChannelID != 99 || !cfg.Enabled || cfg.NoChargeStatuses != "failed,cancelled" || cfg.Version == "config-v1" {
				t.Fatal("unrelated config fields changed", cfg)
			}
			if cap.DefaultDuration != 4 || cap.DefaultRatio != "1:1" || cap.DefaultResolution != "480p" || cap.DefaultSeed != -1 || cap.DefaultExecutionExpiresAfter != 172800 || !reflect.DeepEqual(cap.Resolutions, p.Capability.Resolutions) {
				t.Fatal("defaults lost or profile not applied", cap)
			}
			in["expected_config_version"] = cfg.Version
			if e := db.Exec("UPDATE users SET role = (SELECT role FROM users WHERE id = 1) WHERE id = 2").Error; e != nil {
				t.Fatal(e)
			}
			w, _ = f.do(t, "POST", path, tokens[2], in)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var source videobilling.MinimumRuleSet
			if e := db.First(&source).Error; e != nil || source.VerifiedBy != 1 {
				t.Fatal("replay replaced original verification actor", e)
			}
			w, out = f.do(t, "GET", path, tokens[1], nil)
			if w.Code != 200 || out["verified"] != true {
				t.Fatal(w.Code, out)
			}
			for table, want := range map[string]int64{"video_minimum_rule_sets": 1, "video_minimum_rule_cells": int64(len(p.MinimumCells)), "video_configuration_revisions": 1, "audit_logs": 1} {
				var count int64
				if e := db.Table(table).Count(&count).Error; e != nil || count != want {
					t.Fatalf("%s count %d want %d: %v", table, count, want, e)
				}
			}
			if before != verificationSnapshot(t, db, immutable...) {
				t.Fatal("verification changed prices, channels, FX, budgets or balances")
			}
			// A later pricing publication invalidates the old synchronization request,
			// even if the capability profile itself remains applied.
			if e := db.Model(&cfg).Update("version", "later-pricing-publication").Error; e != nil {
				t.Fatal(e)
			}
			in["expected_config_version"] = "config-v1"
			w, _ = f.do(t, "POST", path, tokens[1], in)
			if w.Code != 409 {
				t.Fatal("stale context replay accepted", w.Code)
			}
		})
	}
}

func TestArkDraftVerificationIsExplicitAndDoesNotPublishPrices(t *testing.T) {
	f, db, tokens, _ := verificationFixture(t, ark.Model25)
	p, err := arkverification.LoadDraft(ark.Model25)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/video/models/seedance-2.5/verification"
	w, out := f.do(t, "GET", path, tokens[1], nil)
	if w.Code != 200 || out["profile_version"] == p.Version {
		t.Fatalf("legacy profile changed: %d %v", w.Code, out)
	}
	w, out = f.do(t, "GET", path+"?profile=draft", tokens[1], nil)
	if w.Code != 200 || out["profile_version"] != p.Version || out["verified"] != false {
		t.Fatalf("Draft profile unavailable: %d %v", w.Code, out)
	}
	before := verificationSnapshot(t, db, "video_pricing_rules")
	w, out = f.do(t, "POST", path, tokens[1], map[string]string{
		"expected_config_version": "config-v1", "profile_version": p.Version,
	})
	if w.Code != 200 || out["verified"] != true {
		t.Fatalf("Draft verification rejected: %d %s", w.Code, w.Body.String())
	}
	var cfg video.ModelConfig
	if err := db.Where("public_model = ?", ark.Model25).Take(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	var cap ark.Capability
	if err := json.Unmarshal([]byte(cfg.CapabilityJSON), &cap); err != nil || !cap.Draft || !cap.DraftConversion {
		t.Fatalf("Draft capability not published: %v %+v", err, cap)
	}
	if before != verificationSnapshot(t, db, "video_pricing_rules") {
		t.Fatal("verification silently published Draft prices")
	}
}

func TestArkDraftPricingPreparationAndValidationUseOfficialSourceRows(t *testing.T) {
	f, db, tokens, _ := verificationFixture(t, ark.Model25)
	p, err := arkverification.LoadDraft(ark.Model25)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/video/models/seedance-2.5/verification"
	w, _ := f.do(t, "POST", path, tokens[1], map[string]string{
		"expected_config_version": "config-v1", "profile_version": p.Version,
	})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var request pricingRequest
	err = db.Transaction(func(tx *gorm.DB) error {
		cfg, cap, scope, bounds, err := loadPricingContext(tx, ark.Model25, false)
		if err != nil {
			return err
		}
		current, err := currentPricingRules(tx, cfg)
		if err != nil {
			return err
		}
		cells := priceMatrix(cap, current)
		costs := map[string]string{
			"generate/480p/false/default/true":           "70",
			"generate/480p/true/default/true":            "42",
			"draft_conversion/1080p/false/default/false": "77",
			"draft_conversion/1080p/true/default/false":  "46",
		}
		for i := range cells {
			if cost, ok := costs[priceCellKey(cells[i])]; ok {
				cells[i].Enabled = true
				cells[i].SupplierCNYPer1M = cost
				cells[i].RetailUSDPer1M = cost
				if cells[i].InputVideo {
					cells[i].MinimumRuleVersion = p.MinimumVersion
				}
			}
		}
		sources, evidence, err := resolvePricingSources(tx, cfg, cells, current)
		if err != nil {
			return err
		}
		covered := map[string]bool{}
		for _, source := range sources.Refs {
			if source.Kind == "official_draft_pricing" && source.RawSHA256 == "c93a73de8f9db86686a4499065a401c08b04dcd3b972fe51b8ff3f2c43908231" {
				covered[source.CellKey] = true
			}
		}
		if len(covered) != 4 {
			t.Fatalf("four Draft price cells lack official pricing provenance: %v", covered)
		}
		request = pricingRequest{ExpectedConfigVersion: cfg.Version, ConfigVersion: "draft-config-v1", PricingVersion: "draft-price-v1", EvidenceHash: evidence, Cells: cells, SourceSet: &sources}
		_, rules, preview, err := validatePricingRequestWithContext(tx, ark.Model25, request, f.srv.now().UTC(), cfg, cap, scope, bounds)
		if err != nil {
			return err
		}
		if len(preview) != 5 || len(rules) < 5 {
			t.Fatalf("Draft prices were not previewed: %d previews, %d rules", len(preview), len(rules))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pricingPath := "/api/admin/video/models/seedance-2.5/pricing"
	w, _ = f.do(t, "POST", pricingPath+"/validate", tokens[1], request)
	if w.Code != 200 {
		t.Fatalf("Draft price validation rejected: %d %s", w.Code, w.Body.String())
	}
	raw, _ := json.Marshal(request)
	httpRequest := httptest.NewRequest("POST", pricingPath+"/publications", bytes.NewReader(raw))
	httpRequest.Header.Set("Authorization", "Bearer "+tokens[1])
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Idempotency-Key", "draft-price-publication")
	w = httptest.NewRecorder()
	f.engine.ServeHTTP(w, httpRequest)
	if w.Code != 200 {
		t.Fatalf("Draft price publication rejected: %d %s", w.Code, w.Body.String())
	}
	var published []videobilling.PricingRule
	if err := db.Where("model = ? AND version = ? AND enabled = ?", ark.Model25, request.PricingVersion, true).Find(&published).Error; err != nil {
		t.Fatal(err)
	}
	draftCount := 0
	for _, rule := range published {
		if rule.Draft || rule.Mode == "draft_conversion" {
			draftCount++
		}
	}
	if draftCount != 4 {
		t.Fatalf("published %d Draft price cells, want four", draftCount)
	}
}

func TestArkVerificationRejectsBroaderAndNonOfficialBindings(t *testing.T) {
	cases := map[string]func(*gorm.DB){
		"wrong_provider": func(db *gorm.DB) { db.Model(&relay.Channel{}).Where("id=99").Update("provider", "openai") },
		"wrong_base": func(db *gorm.DB) {
			db.Model(&relay.Channel{}).Where("id=99").Update("base_url", "https://other.invalid")
		},
		"byok":             func(db *gorm.DB) { db.Model(&relay.Channel{}).Where("id=99").Update("owner_org_id", 77) },
		"disabled_channel": func(db *gorm.DB) { db.Model(&relay.Channel{}).Where("id=99").Update("status", 0) },
		"larger_bounds": func(db *gorm.DB) {
			db.Model(&video.ModelConfig{}).Where("public_model=?", ark.Model20).Update("bounds_json", `{"480p/1:1":{"width":16384,"height":16384}}`)
		},
		"longer_input": func(db *gorm.DB) {
			db.Model(&video.ModelConfig{}).Where("public_model=?", ark.Model20).Update("max_input_video_duration_ms", 600000)
		},
		"different_fps": func(db *gorm.DB) { changeVerificationCap(t, db, func(c *ark.Capability) { c.FPS = 60 }) },
		"draft":         func(db *gorm.DB) { changeVerificationCap(t, db, func(c *ark.Capability) { c.Draft = true }) },
		"fast_variant": func(db *gorm.DB) {
			changeVerificationCap(t, db, func(c *ark.Capability) { c.UpstreamModel = "doubao-seedance-2-0-fast-260128" })
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f, db, tokens, p := verificationFixture(t, ark.Model20)
			change(db)
			before := verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules")
			w, _ := f.do(t, "POST", "/api/admin/video/models/seedance-2.0/verification", tokens[1], map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version})
			if w.Code != 409 {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			assertVerificationUnchanged(t, db, before)
		})
	}
}

func changeVerificationCap(t *testing.T, db *gorm.DB, change func(*ark.Capability)) {
	t.Helper()
	var cfg video.ModelConfig
	if e := db.First(&cfg).Error; e != nil {
		t.Fatal(e)
	}
	var cap ark.Capability
	if e := json.Unmarshal([]byte(cfg.CapabilityJSON), &cap); e != nil {
		t.Fatal(e)
	}
	change(&cap)
	raw, _ := json.Marshal(cap)
	if e := db.Model(&cfg).Update("capability_json", string(raw)).Error; e != nil {
		t.Fatal(e)
	}
}

func assertVerificationUnchanged(t *testing.T, db *gorm.DB, before string) {
	t.Helper()
	if before != verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules") {
		t.Fatal("rejected sync changed config or prices")
	}
	for _, table := range []string{"video_minimum_rule_sets", "video_minimum_rule_cells", "video_configuration_revisions", "audit_logs"} {
		var n int64
		if e := db.Table(table).Count(&n).Error; e != nil || n != 0 {
			t.Fatalf("%s was not rolled back: %d %v", table, n, e)
		}
	}
}

func TestArkVerificationAuditFailureRollsBackAndVersionConflicts(t *testing.T) {
	f, db, tokens, p := verificationFixture(t, ark.Model20)
	path := "/api/admin/video/models/seedance-2.0/verification"
	before := verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules")
	for _, in := range []map[string]string{{"expected_config_version": "stale", "profile_version": p.Version}, {"expected_config_version": "config-v1", "profile_version": "old-profile"}} {
		w, _ := f.do(t, "POST", path, tokens[1], in)
		if w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
		assertVerificationUnchanged(t, db, before)
	}
	if e := db.Callback().Create().Before("gorm:create").Register("verification:fail_audit", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(errors.New("injected audit failure"))
		}
	}); e != nil {
		t.Fatal(e)
	}
	w, _ := f.do(t, "POST", path, tokens[1], map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version})
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertVerificationUnchanged(t, db, before)
}

func TestArkVerificationKeepsEnabledReferenceRuleMinimumOrRejects(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "incomplete-existing-minimum", true: "complete-existing-minimum"}[complete], func(t *testing.T) {
			f, db, tokens, p := verificationFixture(t, ark.Model20)
			set := videobilling.MinimumRuleSet{Model: ark.Model20, Version: "legacy-verified", SourceURL: p.SourceURL, EvidenceHash: strings.Repeat("b", 64), ObservedAt: p.ObservedAt, VerifiedBy: 1, VerifiedAt: time.Now().UTC()}
			cells := verificationMinimumCells(p)
			if !complete {
				cells = cells[:1]
			}
			if e := db.Transaction(func(tx *gorm.DB) error { _, e := videobilling.PublishMinimumTx(tx, set, cells); return e }); e != nil {
				t.Fatal(e)
			}
			if e := db.Model(&videobilling.PricingRule{}).Where("model=?", ark.Model20).Updates(map[string]any{"input_video": true, "minimum_rule_version": set.Version}).Error; e != nil {
				t.Fatal(e)
			}
			before := verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_minimum_rule_sets", "video_minimum_rule_cells")
			prices := verificationSnapshot(t, db, "video_pricing_rules")
			w, _ := f.do(t, "POST", "/api/admin/video/models/seedance-2.0/verification", tokens[1], map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version})
			want := 409
			if complete {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
			}
			if prices != verificationSnapshot(t, db, "video_pricing_rules") {
				t.Fatal("changed the published rule's minimum version")
			}
			if !complete && before != verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_minimum_rule_sets", "video_minimum_rule_cells") {
				t.Fatal("coverage failure did not rollback")
			}
		})
	}
}

func TestArkVerificationDoesNotEnableDisabledModel(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model25, ark.Model20Fast, ark.Model20Mini} {
		t.Run(model, func(t *testing.T) { testArkVerificationDoesNotEnableDisabledModel(t, model) })
	}
}

func testArkVerificationDoesNotEnableDisabledModel(t *testing.T, model string) {
	f, db, tokens, p := verificationFixture(t, model)
	if e := db.Model(&video.ModelConfig{}).Where("public_model=?", model).Update("enabled", false).Error; e != nil {
		t.Fatal(e)
	}
	w, _ := f.do(t, "POST", "/api/admin/video/models/"+model+"/verification", tokens[1], map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var cfg video.ModelConfig
	if e := db.First(&cfg).Error; e != nil || cfg.Enabled {
		t.Fatal("disabled model was enabled", e)
	}
}

func TestArkVerificationRejectsChangedMinimumEvidence(t *testing.T) {
	f, db, tokens, p := verificationFixture(t, ark.Model20)
	set := videobilling.MinimumRuleSet{Model: ark.Model20, Version: p.MinimumVersion, SourceURL: p.SourceURL, EvidenceHash: strings.Repeat("c", 64), ObservedAt: p.ObservedAt, VerifiedBy: 1, VerifiedAt: time.Now().UTC()}
	if e := db.Transaction(func(tx *gorm.DB) error {
		_, e := videobilling.PublishMinimumTx(tx, set, verificationMinimumCells(p))
		return e
	}); e != nil {
		t.Fatal(e)
	}
	before := verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_minimum_rule_sets", "video_minimum_rule_cells")
	path := "/api/admin/video/models/seedance-2.0/verification"
	w, out := f.do(t, "GET", path, tokens[1], nil)
	if w.Code != 200 || out["verified"] != false {
		t.Fatal("conflicting evidence marked verified", w.Code, out)
	}
	w, _ = f.do(t, "POST", path, tokens[1], map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version})
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if before != verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_minimum_rule_sets", "video_minimum_rule_cells") {
		t.Fatal("same-version source evidence was overwritten")
	}
}

func TestArkVerificationConcurrentReplay(t *testing.T) {
	f, db, tokens, p := verificationFixture(t, ark.Model20)
	before := verificationSnapshot(t, db, "video_pricing_rules", "video_provider_scopes", "video_quota_policies", "video_quota_buckets", "ledger_accounts", "ledger_journal")
	raw, _ := json.Marshal(map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version})
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			req := httptest.NewRequest("POST", "/api/admin/video/models/seedance-2.0/verification", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+tokens[1])
			w := httptest.NewRecorder()
			f.engine.ServeHTTP(w, req)
			results <- w
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		w := <-results
		if w.Code != 200 {
			t.Fatalf("concurrent sync returned %d: %s", w.Code, w.Body.String())
		}
	}
	for table, want := range map[string]int64{"video_configuration_revisions": 1, "video_minimum_rule_sets": 1, "video_minimum_rule_cells": int64(len(p.MinimumCells)), "audit_logs": 1} {
		var n int64
		if e := db.Table(table).Count(&n).Error; e != nil || n != want {
			t.Fatalf("concurrent %s count %d want %d: %v", table, n, want, e)
		}
	}
	if before != verificationSnapshot(t, db, "video_pricing_rules", "video_provider_scopes", "video_quota_policies", "video_quota_buckets", "ledger_accounts", "ledger_journal") {
		t.Fatal("concurrent synchronization changed financial state")
	}
}
