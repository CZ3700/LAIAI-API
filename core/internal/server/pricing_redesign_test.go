package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func prepareFixture(t *testing.T) (*gin999, *gorm.DB, map[uint64]string, map[string]any) {
	t.Helper()
	f, db, tokens := pricingCatalogFixture(t)
	var cfg video.ModelConfig
	db.First(&cfg)
	var cap ark.Capability
	_ = json.Unmarshal([]byte(cfg.CapabilityJSON), &cap)
	current, e := currentPricingRules(db, cfg)
	if e != nil {
		t.Fatal(e)
	}
	cells := priceMatrix(cap, current)
	return f, db, tokens, map[string]any{"expected_config_version": cfg.Version, "channel_id": cfg.ChannelID, "cells": cells}
}
func TestPricingPrepareSourceValidationCompleteMatrixAndNoWrites(t *testing.T) {
	f, db, tokens, in := prepareFixture(t)
	base := "/api/admin/video/models/seedance-2.0/pricing"
	for _, token := range []string{tokens[2], tokens[4]} {
		w, _ := f.do(t, "POST", base+"/prepare", token, in)
		if w.Code != 403 {
			t.Fatalf("permission %d", w.Code)
		}
	}
	writes := 0
	deny := func(tx *gorm.DB) { writes++; tx.AddError(assertCatalogWrite{}) }
	for _, e := range []error{db.Callback().Create().Before("gorm:create").Register("prepare:no_create", deny), db.Callback().Update().Before("gorm:update").Register("prepare:no_update", deny), db.Callback().Delete().Before("gorm:delete").Register("prepare:no_delete", deny)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	w, out := f.do(t, "POST", base+"/prepare", tokens[1], in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, _ := json.Marshal(out["request"])
	var req pricingRequest
	if e := json.Unmarshal(raw, &req); e != nil {
		t.Fatal(e)
	}
	if req.SourceSet == nil || len(req.SourceSet.Refs) == 0 || req.SourceSet.SHA256 == req.EvidenceHash || req.ConfigVersion == "config-v1" {
		t.Fatal("source/version preparation missing")
	}
	w, _ = f.do(t, "POST", base+"/validate", tokens[1], req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	req.SourceSet.Refs[0].RawSHA256 = strings.Repeat("b", 64)
	w, _ = f.do(t, "POST", base+"/validate", tokens[1], req)
	if w.Code != 422 {
		t.Fatal("forged source accepted")
	}
	in["channel_id"] = 100
	w, _ = f.do(t, "POST", base+"/prepare", tokens[1], in)
	if w.Code != 409 {
		t.Fatal("wrong channel accepted")
	}
	in["channel_id"] = 99
	in["expected_config_version"] = "stale"
	w, _ = f.do(t, "POST", base+"/prepare", tokens[1], in)
	if w.Code != 409 {
		t.Fatal("stale baseline accepted")
	}
	in["expected_config_version"] = "config-v1"
	in["cells"] = in["cells"].([]pricingCell)[1:]
	w, _ = f.do(t, "POST", base+"/prepare", tokens[1], in)
	if w.Code != 409 {
		t.Fatal("missing current row accepted")
	}
	if writes != 0 {
		t.Fatal("prepare/validate wrote data", writes)
	}
}

func TestPricingPreparePublishRetainsHiddenDisabledRowsAndSnapshot(t *testing.T) {
	f, db, tokens, in := prepareFixture(t)
	base := "/api/admin/video/models/seedance-2.0/pricing"
	var old videobilling.PricingRule
	db.First(&old)
	hidden := old
	hidden.ID = 0
	hidden.RuleID = "hidden-flex"
	hidden.ServiceTier = "flex"
	hidden.Enabled = false
	if e := db.Create(&hidden).Error; e != nil {
		t.Fatal(e)
	}
	w := catalogGET(t, f, base, tokens[1], "", 200)
	var matrix struct {
		Cells []pricingCell `json:"cells"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &matrix)
	found := false
	for i := range matrix.Cells {
		if matrix.Cells[i].ServiceTier == "flex" {
			found = true
		}
		if matrix.Cells[i].Enabled {
			matrix.Cells[i].RetailUSDPer1M = "10.75"
		}
	}
	if !found {
		t.Fatal("hidden tier lost in get")
	}
	in["cells"] = matrix.Cells
	w, out := f.do(t, "POST", base+"/prepare", tokens[1], in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	request := out["request"]
	raw, _ := json.Marshal(request)
	call := func(body []byte, key string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", base+"/publications", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+tokens[1])
		r.Header.Set("Idempotency-Key", key)
		rw := httptest.NewRecorder()
		f.engine.ServeHTTP(rw, r)
		if rw.Code != want {
			t.Fatal(rw.Code, rw.Body.String())
		}
	}
	call(raw, "prepared-publication", 200)
	call(raw, "prepared-publication", 200)
	var cfg video.ModelConfig
	db.First(&cfg)
	rules, e := currentPricingRules(db, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if len(rules) != 2 {
		t.Fatal("hidden rule lost", len(rules))
	}
	for _, p := range rules {
		if p.ServiceTier == "flex" && (p.Enabled || p.RetailUSDPer1M.String() != "9.125") {
			t.Fatal("hidden rule changed")
		}
		if p.ServiceTier == "default" && p.RetailUSDPer1M.String() != "10.75" {
			t.Fatal("new retail not published")
		}
	}
	var receipts []pricingPublication
	db.Find(&receipts)
	if len(receipts) != 1 || !strings.Contains(receipts[0].SnapshotJSON, "source_set") {
		t.Fatal("replay/snapshot lost")
	}
	// A new key cannot publish a stale prepared request, and legacy callers also
	// cannot accidentally omit an existing disabled row.
	call(raw, "stale-prepared-publication", 409)
	var req pricingRequest
	_ = json.Unmarshal(raw, &req)
	req.ExpectedConfigVersion = cfg.Version
	req.ConfigVersion = "third-config"
	req.PricingVersion = "third-price"
	req.SourceSet = nil
	req.Cells = req.Cells[:len(req.Cells)-1]
	w, _ = f.do(t, "POST", base+"/validate", tokens[1], req)
	if w.Code != 422 {
		t.Fatal("legacy incomplete replacement accepted")
	}
}

func TestPricingPrepareDefaultsNewRiskAndPreservesConfiguredFactor(t *testing.T) {
	f, db, tokens, in := prepareFixture(t)
	base := "/api/admin/video/models/seedance-2.0/pricing"
	cells := in["cells"].([]pricingCell)
	for i := range cells {
		cells[i].RiskMultiplier = ""
	}
	in["cells"] = cells
	var prepared struct {
		Request pricingRequest `json:"request"`
	}
	if err := json.Unmarshal(templateCall(t, f, base+"/prepare", tokens[1], "", in, 200), &prepared); err != nil {
		t.Fatal(err)
	}
	for _, cell := range prepared.Request.Cells {
		want := "1"
		if cell.Enabled {
			want = "4"
		}
		if cell.RiskMultiplier != want {
			t.Fatalf("wrong factor for %s: %s", priceCellKey(cell), cell.RiskMultiplier)
		}
	}
	templateCall(t, f, base+"/publications", tokens[1], "defaults-preserve-existing", prepared.Request, 200)
	var cfg video.ModelConfig
	if err := db.First(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	rules, err := currentPricingRules(db, cfg)
	if err != nil || len(rules) != 1 || rules[0].RiskMultiplier.String() != "4" {
		t.Fatal("existing factor changed or blank defaults created price rules", rules, err)
	}
}

func TestPublicVideoPricingOnlyCanonicalSellableAndNoInternalFields(t *testing.T) {
	f, db, _ := pricingCatalogFixture(t)
	f.srv.video = &video.Service{DB: db, Now: time.Now}
	if e := db.Exec(`CREATE TABLE video_org_policies(org_id INTEGER PRIMARY KEY,async_execution_allowed BOOLEAN NOT NULL,capture_history BOOLEAN NOT NULL,retention_days INTEGER NOT NULL,updated_at DATETIME NOT NULL);`).Error; e != nil {
		t.Fatal(e)
	}
	// A misleading generic price for an upstream alias must not bypass official admission.
	if e := db.Create(&relay.ModelPrice{Model: "official-alias", OutputUSDPer1M: decimal.NewFromInt(1)}).Error; e != nil {
		t.Fatal(e)
	}
	rows, e := f.srv.publicPricing(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var actual gin.H
	for _, r := range rows {
		if r["model"] == "official-alias" || r["model"] == "doubao-seedance-2-0-260128" {
			t.Fatal("upstream alias leaked")
		}
		if r["model"] == ark.Model20 {
			actual = r
		}
	}
	if actual == nil {
		t.Fatal("valid official model missing")
	}
	p := actual["pricing"].(pricingDescriptor)
	if p.Unit != "USD/1M_video_tokens" || len(p.Combinations) != 1 || p.Combinations[0].Amount != "9.125" || len(p.Rates) != 0 {
		t.Fatalf("bad video DTO %#v", p)
	}
	raw, _ := json.Marshal(actual)
	for _, secret := range []string{"supplier", "scope", "channel", "account", "risk", "evidence", "minimum", "upstream", "999"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("internal field in public response", secret)
		}
	}
	// SDK discovery uses the same canonical data and honors key model limits.
	if e := db.Exec("INSERT INTO organizations(id,slug,name,status) VALUES(1,'catalog','catalog',1)").Error; e != nil {
		t.Fatal(e)
	}
	key, _, e := identity.IssueKey(db, identity.IssueKeyInput{OrgID: 1, ModelLimits: ark.Model20, Scopes: "video:read"})
	if e != nil {
		t.Fatal(e)
	}
	r := gin.New()
	r.GET("/v1/models", f.srv.listPricedModels)
	unauthorized := httptest.NewRecorder()
	r.ServeHTTP(unauthorized, httptest.NewRequest("GET", "/v1/models", nil))
	if unauthorized.Code != 401 || !strings.Contains(unauthorized.Body.String(), `"type":"laiai_relay_error"`) {
		t.Fatal("SDK error envelope changed")
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 200 || len(result.Data) != 1 || result.Data[0].ID != ark.Model20 {
		t.Fatal("SDK identity mismatch", w.Code, w.Body.String())
	}
}

func TestPublicVideoPricingFailsClosedForInvalidBindingsAndRules(t *testing.T) {
	cases := map[string]func(*gorm.DB){
		"disabled channel": func(db *gorm.DB) { db.Model(&relay.Channel{}).Where("id = 99").Update("status", 0) },
		"private channel":  func(db *gorm.DB) { db.Model(&relay.Channel{}).Where("id = 99").Update("owner_org_id", 77) },
		"wrong provider":   func(db *gorm.DB) { db.Model(&relay.Channel{}).Where("id = 99").Update("provider", "doubao") },
		"wrong base": func(db *gorm.DB) {
			db.Model(&relay.Channel{}).Where("id = 99").Update("base_url", "https://example.invalid")
		},
		"disabled config": func(db *gorm.DB) {
			db.Model(&video.ModelConfig{}).Where("public_model = ?", ark.Model20).Update("enabled", false)
		},
		"unverified config": func(db *gorm.DB) {
			db.Model(&video.ModelConfig{}).Where("public_model = ?", ark.Model20).Update("verified_at", nil)
		},
		"disabled scope": func(db *gorm.DB) { db.Model(&video.ProviderScope{}).Where("id = 1").Update("enabled", false) },
		"expired": func(db *gorm.DB) {
			db.Model(&videobilling.PricingRule{}).Where("id = 1").Update("effective_until", time.Now().Add(-time.Hour))
		},
		"future": func(db *gorm.DB) {
			db.Model(&videobilling.PricingRule{}).Where("id = 1").Update("effective_from", time.Now().Add(time.Hour))
		},
		"different version": func(db *gorm.DB) {
			db.Model(&videobilling.PricingRule{}).Where("id = 1").Update("version", "future-version")
		},
		"missing minimum": func(db *gorm.DB) { db.Model(&videobilling.PricingRule{}).Where("id = 1").Update("input_video", true) },
		"overlap": func(db *gorm.DB) {
			var p videobilling.PricingRule
			db.First(&p)
			p.ID = 0
			p.RuleID = "overlap"
			p.Version = "overlap-version"
			db.Create(&p)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f, db, _ := pricingCatalogFixture(t)
			f.srv.video = &video.Service{}
			mutate(db)
			rows, e := f.srv.publicPricing(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			for _, r := range rows {
				if r["model"] == ark.Model20 {
					t.Fatal("unavailable model sold")
				}
			}
		})
	}
}

func TestPublicVideoPricingRequiresCompleteVerifiedReferenceVideoMinimum(t *testing.T) {
	f, db, _ := pricingCatalogFixture(t)
	f.srv.video = &video.Service{}
	var original videobilling.PricingRule
	db.First(&original)
	reference := original
	reference.ID = 0
	reference.RuleID = "verified-reference-video"
	reference.InputVideo = true
	reference.MinimumRuleVersion = "minimum-verified"
	if e := db.Create(&reference).Error; e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	e := db.Transaction(func(tx *gorm.DB) error {
		_, e := videobilling.PublishMinimumTx(tx, videobilling.MinimumRuleSet{Model: ark.Model20, Version: reference.MinimumRuleVersion, SourceURL: "https://docs.volcengine.com/fixture", EvidenceHash: strings.Repeat("a", 64), ObservedAt: now, VerifiedAt: now, VerifiedBy: 1}, []videobilling.MinimumRuleCell{{Resolution: "480p", Ratio: "1:1", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 40000}, {Resolution: "480p", Ratio: "1:1", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 50000}})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	count := func() int {
		t.Helper()
		rows, e := f.srv.publicPricing(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range rows {
			if r["model"] == ark.Model20 {
				return len(r["pricing"].(pricingDescriptor).Combinations)
			}
		}
		return 0
	}
	if count() != 2 {
		t.Fatal("verified reference-video combination missing")
	}
	if e := db.Where("output_duration_ms = 4000").Delete(&videobilling.MinimumRuleCell{}).Error; e != nil {
		t.Fatal(e)
	}
	if count() != 1 {
		t.Fatal("partial minimum coverage was advertised as callable")
	}
}

func TestPublicProviderSpecificPricesDoNotUseHiddenTokenRates(t *testing.T) {
	f, db, _ := pricingCatalogFixture(t)
	for _, name := range []string{"existing-image", "mj_imagine"} {
		if e := db.Create(&relay.ModelPrice{Model: name, InputUSDPer1M: decimal.NewFromInt(7)}).Error; e != nil {
			t.Fatal(e)
		}
	}
	rows, e := f.srv.publicPricing(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range rows {
		if r["model"] == "existing-image" || r["model"] == "mj_imagine" {
			t.Fatal("exclusive provider advertised an unrelated token price")
		}
	}
	if e := db.Model(&relay.ModelPrice{}).Where("model = ?", "existing-image").Update("image_usd", "0.04").Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&relay.ModelPrice{}).Where("model = ?", "mj_imagine").Update("per_call_usd", "0.12").Error; e != nil {
		t.Fatal(e)
	}
	rows, e = f.srv.publicPricing(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	seen := 0
	for _, r := range rows {
		if r["model"] == "existing-image" || r["model"] == "mj_imagine" {
			seen++
			d := r["pricing"].(pricingDescriptor)
			if len(d.Rates) != 1 || d.Rates[0].Key == "input_usd_per_1m" {
				t.Fatal("irrelevant token rates projected")
			}
			if r["model"] == "existing-image" && d.Endpoints[0] != "/v1/images/generations" {
				t.Fatal("replicate endpoint mismatch")
			}
		}
	}
	if seen != 2 {
		t.Fatal("configured provider prices missing")
	}
}

func TestPublicTokenGuidesUseExplicitCategoryAndTransport(t *testing.T) {
	f, db, _ := pricingCatalogFixture(t)
	for _, x := range []struct{ name, category string }{{"vector-plain", "embedding"}, {"rank-plain", "rerank"}, {"type-unset", ""}} {
		if e := db.Create(&relay.ModelPrice{Model: x.name, InputUSDPer1M: decimal.NewFromInt(1)}).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Create(&ModelMeta{Model: x.name, Category: x.category}).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := db.Model(&relay.Channel{}).Where("id = 101").Update("models", "vector-plain,rank-plain,type-unset").Error; e != nil {
		t.Fatal(e)
	}
	rows, e := f.srv.publicPricing(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		name := row["model"].(string)
		d := row["pricing"].(pricingDescriptor)
		switch name {
		case "vector-plain":
			if len(d.Endpoints) != 1 || d.Endpoints[0] != "/v1/embeddings" {
				t.Fatal("embedding guide mismatch", d.Endpoints)
			}
		case "rank-plain":
			if len(d.Endpoints) != 1 || d.Endpoints[0] != "/v1/rerank" {
				t.Fatal("rerank guide mismatch", d.Endpoints)
			}
		case "type-unset":
			if len(d.Endpoints) != 0 {
				t.Fatal("unverified type assigned a guide", d.Endpoints)
			}
		}
	}
	if e := db.Model(&relay.Channel{}).Where("id = 101").Update("provider", "anthropic").Error; e != nil {
		t.Fatal(e)
	}
	rows, e = f.srv.publicPricing(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		if row["model"] == "vector-plain" && len(row["pricing"].(pricingDescriptor).Endpoints) != 0 {
			t.Fatal("unsupported embeddings transport advertised")
		}
	}
}

func TestPublicZeroPriceIsNotAdvertisedAsFree(t *testing.T) {
	f, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1, 1)
	defer up.Close()
	setupChannelAndPrice(t, f, up.URL)
	key, _ := seedUserWithCredit(t, f, "zero-price-catalog@test.invalid", "10")
	if e := db.Model(&relay.ModelPrice{}).Where("model = ?", "test-model").Updates(map[string]any{"input_usd_per_1m": "0", "output_usd_per_1m": "0"}).Error; e != nil {
		t.Fatal(e)
	}
	w, _ := f.do(t, "POST", "/v1/chat/completions", key, map[string]any{"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}})
	if w.Code != 500 {
		t.Fatal("unexpected zero-hold engine behavior", w.Code)
	}
	rows, e := f.srv.publicPricing(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		if row["model"] == "test-model" {
			t.Fatal("uncallable zero-hold model advertised as free")
		}
	}
	var count int64
	if e := db.Table("ledger_holds").Count(&count).Error; e != nil {
		t.Fatal(e)
	}
	if count != 0 {
		t.Fatal("zero-price request created a hold")
	}
}
