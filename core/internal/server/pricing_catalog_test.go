package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func pricingCatalogFixture(t *testing.T) (*gin999, *gorm.DB, map[uint64]string) {
	t.Helper()
	f, db := setupServer(t)
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	for _, schema := range []string{relayTestSchema, pricingConsoleSchema} {
		if e := db.Exec(schema).Error; e != nil {
			t.Fatal(e)
		}
	}
	tokens := map[uint64]string{}
	for _, u := range []struct {
		id     uint64
		role   string
		status int
	}{{1, identity.PlatRoleSuperadmin, 1}, {2, identity.PlatRoleAdmin, 1}, {3, identity.PlatRoleSuperadmin, 0}, {4, identity.PlatRoleUser, 1}} {
		email := fmt.Sprintf("pricing-%d@test.invalid", u.id)
		if e := db.Exec("INSERT INTO users(id,email,email_canonical,role,status) VALUES(?,?,?,?,?)", u.id, email, email, u.role, u.status).Error; e != nil {
			t.Fatal(e)
		}
		token, _, e := identity.IssueSessionToken(testSecret, u.id, 1, time.Hour, time.Now())
		if e != nil {
			t.Fatal(e)
		}
		tokens[u.id] = token
	}
	f.srv.adminToken = []byte("fixture-emergency-token")
	cap := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "cap-v1", Enabled: true, Verified: true, Resolutions: []string{"480p"}, Ratios: []string{"1:1"}, MinDuration: 4, MaxDuration: 5, DefaultDuration: 4, DefaultResolution: "480p", DefaultRatio: "1:1", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4, VideoInput: true, MaxVideos: 1}
	raw, _ := json.Marshal(cap)
	now := time.Now().UTC()
	cfg := video.ModelConfig{PublicModel: ark.Model20, Version: "config-v1", ProviderScopeID: 1, ChannelID: 99, Enabled: true, CapabilityJSON: string(raw), PricingVersion: "price-v1", BoundsJSON: `{"480p/1:1":{"width":640,"height":640}}`, NoChargeStatuses: "failed,cancelled", MaxInputVideoDurationMS: 15000, VerifiedAt: &now, UpdatedAt: now}
	items := []any{&cfg, &video.ProviderScope{ID: 1, ScopeKey: "scope", AccountRef: "fixture", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "fx-v1", FXObservedAt: now},
		&relay.Channel{ID: 99, Name: "official", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Status: 1, Models: "seedance-2.0,official-alias,doubao-seedance-2-0-260128,seedance-2.0", ModelMapping: `{"official-alias":"doubao-seedance-2-0-260128"}`},
		&relay.Channel{ID: 100, Name: "unbound", Provider: "openai", Status: 0, Models: "seedance-2.0,third-party-alias", ModelMapping: `{"third-party-alias":"doubao-seedance-2-0-260128"}`},
		&relay.Channel{ID: 101, Name: "generic", Provider: "openai", Status: 1, Models: "priced,unpriced-image-name,seedance-2.0-fast,seedance-2.0-mini,seedance-1.5-pro"},
		&relay.Channel{ID: 102, Name: "legacy task", Provider: "kling", Status: 1, Models: "legacy-video"},
		&relay.Channel{ID: 103, Name: "replicate", Provider: "replicate", Status: 1, Models: "existing-image"},
		&relay.Channel{ID: 104, Name: "MJ", Provider: "midjourney", Status: 1, Models: "mj_imagine"},
		&ModelMeta{Model: "unpriced-image-name", Category: "image", Vendor: "display-only"},
		&relay.ModelPrice{Model: "priced", InputUSDPer1M: decimal.RequireFromString("1.12"), OutputUSDPer1M: decimal.RequireFromString("2.23"), CachedInputUSDPer1M: decimal.RequireFromString("3.34"), ImageUSD: decimal.RequireFromString("4.45"), PerCallUSD: decimal.RequireFromString("5.56"), TTSPer1KChars: decimal.RequireFromString("6.67"), TranscriptionPerCall: decimal.RequireFromString("7.78"), AudioInputUSDPer1M: decimal.RequireFromString("8.89"), AudioOutputUSDPer1M: decimal.RequireFromString("9.91")},
		&relay.ModelPrice{Model: "legacy-video", ImageUSD: decimal.NewFromInt(50), OutputUSDPer1M: decimal.NewFromInt(8)},
		&videobilling.PricingRule{RuleID: "rule-current", Version: "price-v1", PricingKey: videobilling.PricingKey{ProviderScopeID: 1, Model: ark.Model20, Mode: "generate", Resolution: "480p", ServiceTier: "default"}, RetailUSDPer1M: decimal.RequireFromString("9.125"), SupplierCNYPer1M: decimal.NewFromInt(999), RiskMultiplier: decimal.NewFromInt(4), MinimumRuleVersion: "preserved-minimum-version", Enabled: true, EffectiveFrom: now, FormulaVersion: "ark-video-token-v1", SourceHash: strings.Repeat("a", 64)},
	}
	for _, item := range items {
		if e := db.Create(item).Error; e != nil {
			t.Fatal(e)
		}
	}
	return f, db, tokens
}

func catalogGET(t *testing.T, f *gin999, path, token, emergency string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if emergency != "" {
		r.Header.Set("X-Admin-Token", emergency)
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s: %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w
}

func TestPricingCatalogVariantPriceEvidenceDoesNotOpenPricingOrPublicModels(t *testing.T) {
	f, db, tokens := pricingCatalogFixture(t)
	models := "seedance-2.0-fast,seedance-2.0-mini,fast-alias,wrong-variant,seedance-2.5-fast,seedance-2.0,mini-alias"
	mapping := `{"fast-alias":"doubao-seedance-2-0-fast-260128","wrong-variant":"unknown-upstream","seedance-2.0":"doubao-seedance-2-0-fast-260128","mini-alias":"doubao-seedance-2-0-mini-260615"}`
	if e := db.Model(&relay.Channel{}).Where("id=99").Updates(map[string]any{"models": models, "model_mapping": mapping}).Error; e != nil {
		t.Fatal(e)
	}
	for _, ch := range []relay.Channel{
		{ID: 110, Name: "conflicting", Provider: "modelark", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Status: 1, Models: "seedance-2.0-fast", ModelMapping: `{"seedance-2.0-fast":"doubao-seedance-2-0-mini-260615"}`},
		{ID: 111, Name: "byok", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Status: 1, OwnerOrgID: 42, Models: "seedance-2.0-fast"},
		{ID: 112, Name: "nonofficial-base", Provider: "volcengine", BaseURL: "https://proxy.invalid", Status: 1, Models: "seedance-2.0-fast"},
	} {
		if e := db.Create(&ch).Error; e != nil {
			t.Fatal(e)
		}
	}
	before := verificationSnapshot(t, db, "channels", "video_model_configs", "video_pricing_rules", "model_prices")
	for _, actor := range []uint64{1, 2} {
		w := catalogGET(t, f, "/api/admin/pricing-catalog", tokens[actor], "", 200)
		var out struct {
			Models []catalogModel `json:"models"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		seen := map[string]bool{}
		for _, row := range out.Models {
			want := actor == 1 && (row.Key == "99:seedance-2.0-fast" || row.Key == "99:seedance-2.0-mini" || row.Key == "99:fast-alias" || row.Key == "99:mini-alias")
			if (row.OfficialPriceEvidence != nil) != want {
				t.Fatalf("wrong price badge for %s role %d", row.Key, actor)
			}
			if want {
				seen[row.Key] = true
				if row.Pricing.Status != "blocked" || row.VideoModel == nil || row.VideoConfiguration != nil || row.UnsupportedReason != "official_video_configuration_missing" || len(row.Pricing.Rates) != 0 || len(row.Pricing.Combinations) != 0 || len(row.OfficialPriceEvidence.Prices) != 4 {
					t.Fatalf("price evidence changed capability or retail: %#v", row)
				}
			}
		}
		if actor == 1 && len(seen) != 4 {
			t.Fatal("missing explicit official variant rows")
		}
	}
	public := catalogGET(t, f, "/api/pricing", "", "", 200)
	if strings.Contains(public.Body.String(), "official_price_evidence") || strings.Contains(public.Body.String(), "seedance-2.0-fast") || strings.Contains(public.Body.String(), "seedance-2.0-mini") {
		t.Fatal("documentary reference leaked into public callable catalog")
	}
	for _, model := range []string{"seedance-2.0-fast", "seedance-2.0-mini"} {
		catalogGET(t, f, "/api/admin/video/models/"+model+"/pricing/reference-candidates", tokens[1], "", 200)
		catalogGET(t, f, "/api/admin/video/models/"+model+"/verification", tokens[1], "", 409)
	}
	if before != verificationSnapshot(t, db, "channels", "video_model_configs", "video_pricing_rules", "model_prices") {
		t.Fatal("read-only variant projection wrote configuration")
	}
}

func TestOfficialVariantTariffIdentityPinsReviewedDeployments(t *testing.T) {
	channel := relay.Channel{Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Status: 1}
	for _, tc := range []struct {
		model, upstream, want string
	}{
		{"seedance-2.0-fast", "doubao-seedance-2-0-fast-260128", "seedance-2.0-fast"},
		{"seedance-2.0-mini", "doubao-seedance-2-0-mini-260615", "seedance-2.0-mini"},
		{"fast-alias", "seedance-2.0-fast", "seedance-2.0-fast"},
		{"mini-alias", "seedance-2.0-mini", "seedance-2.0-mini"},
		{"doubao-seedance-2-0-fast-260128", "doubao-seedance-2-0-fast-260128", "seedance-2.0-fast"},
		{"doubao-seedance-2-0-mini-260615", "doubao-seedance-2-0-mini-260615", "seedance-2.0-mini"},
		{"fast-alias", "doubao-seedance-2-0-fast-270101", ""},
		{"mini-alias", "doubao-seedance-2-0-mini-270101", ""},
		{"seedance-2.0-fast-270101", "doubao-seedance-2-0-fast-260128", ""},
		{"seedance-2.0-mini-270101", "doubao-seedance-2-0-mini-260615", ""},
		{"fast-alias", "doubao-seedance-2-0-fast-260615", ""},
		{"mini-alias", "doubao-seedance-2-0-mini-260128", ""},
		{"seedance-2.0-fast", "doubao-seedance-2-0-mini-260615", ""},
		{"seedance-2.0-mini", "doubao-seedance-2-0-fast-260128", ""},
		{"seedance-2.5-fast", "doubao-seedance-2-0-fast-260128", ""},
		{"seedance-2.0-fast", "doubao-seedance-2-5-fast-260128", ""},
		{"seedance-2.0", "doubao-seedance-2-0-fast-260128", ""},
	} {
		t.Run(tc.model+"/"+tc.upstream, func(t *testing.T) {
			if got := officialVariantTariffModel(catalogModel{Model: tc.model, UpstreamModel: tc.upstream}, channel); got != tc.want {
				t.Fatalf("identity = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPricingCatalogPermissionsAliasesAndFullPriceProjection(t *testing.T) {
	f, db, tokens := pricingCatalogFixture(t)
	writes := 0
	deny := func(tx *gorm.DB) { writes++; tx.AddError(assertCatalogWrite{}) }
	if e := db.Callback().Create().Before("gorm:create").Register("pricing:no_create", deny); e != nil {
		t.Fatal(e)
	}
	if e := db.Callback().Update().Before("gorm:update").Register("pricing:no_update", deny); e != nil {
		t.Fatal(e)
	}
	if e := db.Callback().Delete().Before("gorm:delete").Register("pricing:no_delete", deny); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		user      uint64
		emergency string
		want      int
		details   bool
	}{{0, "", 401, false}, {4, "", 403, false}, {3, "", 401, false}, {2, "", 200, false}, {0, "fixture-emergency-token", 200, false}, {1, "", 200, true}} {
		w := catalogGET(t, f, "/api/admin/pricing-catalog", tokens[tc.user], tc.emergency, tc.want)
		if tc.want != 200 {
			continue
		}
		var out struct {
			Models      []catalogModel    `json:"models"`
			Providers   []catalogProvider `json:"providers"`
			Channels    []catalogChannel  `json:"channels"`
			Permissions struct {
				VideoDetails bool `json:"video_details"`
			} `json:"permissions"`
			PriceScope string `json:"price_scope"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		if out.Permissions.VideoDetails != tc.details || out.PriceScope != "public_model_global" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("wrong access/scope")
		}
		rows := map[string]catalogModel{}
		for _, row := range out.Models {
			if _, exists := rows[row.Key]; exists {
				t.Fatal("duplicate row")
			}
			rows[row.Key] = row
			if !tc.details && row.VideoConfiguration != nil {
				t.Fatal("configuration leaked")
			}
		}
		for _, key := range []string{"99:seedance-2.0", "99:official-alias", "99:doubao-seedance-2-0-260128"} {
			r := rows[key]
			if r.PricingKind != "video" || r.RecognitionSource != "video_config" || r.VideoModel == nil || *r.VideoModel != ark.Model20 || r.UnsupportedReason != "" || (r.VideoConfiguration != nil) != tc.details {
				t.Fatalf("wrong official alias %#v", r)
			}
		}
		for _, key := range []string{"100:seedance-2.0", "100:third-party-alias"} {
			r := rows[key]
			if r.PricingKind != "video" || r.UnsupportedReason != "official_video_channel_mismatch" || r.VideoConfiguration != nil {
				t.Fatalf("wrong binding %#v", r)
			}
		}
		for _, name := range []string{"seedance-2.0-fast", "seedance-2.0-mini", "seedance-1.5-pro"} {
			r := rows["101:"+name]
			if r.PricingKind != "video" || r.VideoConfiguration != nil || r.UnsupportedReason == "" || name == "seedance-1.5-pro" && r.VideoModel != nil {
				t.Fatalf("variant accepted %#v", r)
			}
		}
		if r := rows["101:unpriced-image-name"]; r.PricingKind != "unknown" || r.GenericPrice != nil || r.Meta == nil {
			t.Fatal("metadata/name inferred billing")
		}
		if r := rows["102:legacy-video"]; r.PricingKind != "generic_video" || len(r.BillingModes) != 1 || r.BillingModes[0] != "task_output_units" {
			t.Fatal("legacy video mislabeled as image/token")
		}
		if rows["103:existing-image"].PricingKind != "image" || rows["104:mj_imagine"].PricingKind != "per_call" {
			t.Fatal("provider contract ignored")
		}
		p := rows["101:priced"].GenericPrice
		if p == nil {
			t.Fatal("price missing")
		}
		raw, _ := json.Marshal(p)
		var fields map[string]string
		_ = json.Unmarshal(raw, &fields)
		want := map[string]string{"model": "priced", "input_usd_per_1m": "1.12", "output_usd_per_1m": "2.23", "cached_input_usd_per_1m": "3.34", "image_usd": "4.45", "per_call_usd": "5.56", "tts_usd_per_1k_chars": "6.67", "transcription_usd_per_call": "7.78", "audio_input_usd_per_1m": "8.89", "audio_output_usd_per_1m": "9.91"}
		if len(fields) != len(want) {
			t.Fatal("price field loss")
		}
		for k, v := range want {
			if fields[k] != v {
				t.Fatalf("price %s=%s", k, fields[k])
			}
		}
		if strings.Contains(w.Body.String(), "credential") || strings.Contains(w.Body.String(), "supplier_cny") {
			t.Fatal("secret/cost leaked in general projection")
		}
	}
	if writes != 0 {
		t.Fatal("catalog mutated database")
	}
}

func TestPricingCatalogProvidersAlignWithChannelSetupWithoutInventingModels(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty_channels=%t", empty), func(t *testing.T) {
			f, db, tokens := pricingCatalogFixture(t)
			if empty {
				if e := db.Exec("DELETE FROM channels").Error; e != nil {
					t.Fatal(e)
				}
			} else {
				for _, ch := range []relay.Channel{
					{ID: 105, Name: "custom active", Provider: "custom-provider", BaseURL: "https://custom.example/v1", Status: 1, Models: "custom-model"},
					{ID: 106, Name: "custom disabled", Provider: "custom-provider", BaseURL: "https://custom.example/v1", Status: 0, Models: "custom-model"},
				} {
					if e := db.Create(&ch).Error; e != nil {
						t.Fatal(e)
					}
				}
			}
			writes := 0
			deny := func(tx *gorm.DB) { writes++; tx.AddError(assertCatalogWrite{}) }
			for _, register := range []func() error{
				func() error {
					return db.Callback().Create().Before("gorm:create").Register("alignment:no_create", deny)
				},
				func() error {
					return db.Callback().Update().Before("gorm:update").Register("alignment:no_update", deny)
				},
				func() error {
					return db.Callback().Delete().Before("gorm:delete").Register("alignment:no_delete", deny)
				},
			} {
				if e := register(); e != nil {
					t.Fatal(e)
				}
			}
			var setup struct {
				Providers []catalogProvider `json:"providers"`
			}
			w := catalogGET(t, f, "/api/admin/providers", tokens[1], "", 200)
			if e := json.Unmarshal(w.Body.Bytes(), &setup); e != nil {
				t.Fatal(e)
			}
			var catalog struct {
				Providers []catalogProvider `json:"providers"`
				Channels  []catalogChannel  `json:"channels"`
				Models    []catalogModel    `json:"models"`
			}
			w = catalogGET(t, f, "/api/admin/pricing-catalog", tokens[1], "", 200)
			if e := json.Unmarshal(w.Body.Bytes(), &catalog); e != nil {
				t.Fatal(e)
			}
			registered := []catalogProvider{}
			customCount := 0
			for i, p := range catalog.Providers {
				if i > 0 && catalog.Providers[i-1].Name >= p.Name {
					t.Fatal("provider names are not unique and sorted")
				}
				if p.Name == "custom-provider" {
					customCount++
					if p.DisplayName != p.Name {
						t.Fatal("custom provider identity was rewritten")
					}
				} else {
					registered = append(registered, p)
				}
			}
			if len(setup.Providers) == 0 || !reflect.DeepEqual(registered, setup.Providers) {
				t.Fatal("pricing providers differ from channel setup names, display names or order")
			}
			if empty {
				if customCount != 0 || len(catalog.Channels) != 0 || len(catalog.Models) != 0 {
					t.Fatal("empty channel catalog acquired a custom provider, channel or model")
				}
			} else {
				if customCount != 1 {
					t.Fatal("existing custom provider missing or duplicated")
				}
				customModels := map[uint64]bool{}
				for _, m := range catalog.Models {
					if m.Provider == "anthropic" {
						t.Fatal("unconnected registered provider acquired a model")
					}
					if m.Provider == "custom-provider" {
						if m.Model != "custom-model" || m.UpstreamModel != "custom-model" || m.GenericPrice != nil {
							t.Fatal("custom model or price was changed")
						}
						customModels[m.ChannelID] = true
					}
				}
				if !reflect.DeepEqual(customModels, map[uint64]bool{105: true, 106: true}) {
					t.Fatal("custom models no longer belong to their actual channels")
				}
			}
			if writes != 0 {
				t.Fatal("provider alignment mutated database")
			}
		})
	}
}

func TestArkReferenceCandidatesPermissionsPreservationAndNoWrites(t *testing.T) {
	f, db, tokens := pricingCatalogFixture(t)
	writes := 0
	deny := func(tx *gorm.DB) { writes++; tx.AddError(assertCatalogWrite{}) }
	for _, cb := range []func() error{func() error {
		return db.Callback().Create().Before("gorm:create").Register("candidate:no_create", deny)
	}, func() error {
		return db.Callback().Update().Before("gorm:update").Register("candidate:no_update", deny)
	}, func() error {
		return db.Callback().Delete().Before("gorm:delete").Register("candidate:no_delete", deny)
	}} {
		if e := cb(); e != nil {
			t.Fatal(e)
		}
	}
	base := "/api/admin/video/models/"
	for _, tc := range []struct {
		user      uint64
		emergency string
		want      int
	}{{0, "", 401}, {2, "", 403}, {3, "", 401}, {0, "fixture-emergency-token", 403}} {
		catalogGET(t, f, base+ark.Model20+"/pricing/reference-candidates", tokens[tc.user], tc.emergency, tc.want)
	}
	for _, model := range []string{"seedance-1.5", "seedance-2.0-fast-270101", "seedance-2.0-mini-270101", "official-alias"} {
		catalogGET(t, f, base+model+"/pricing/reference-candidates", tokens[1], "", 404)
	}
	for _, model := range []string{ark.Model20, ark.Model25} {
		w := catalogGET(t, f, base+model+"/pricing/reference-candidates", tokens[1], "", 200)
		var out struct {
			Config              *catalogVideoConfiguration        `json:"config"`
			Prices              []arkPriceCandidate               `json:"prices"`
			Minimum             arkpricecatalog.ReferenceMinimum  `json:"minimum"`
			Sources             []arkpricecatalog.CandidateSource `json:"sources"`
			InputTokenSemantics string                            `json:"input_token_semantics"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		want := 8
		if model == ark.Model25 {
			want = 6
		}
		if len(out.Prices) != want || out.InputTokenSemantics != "not_projected_separately_completion_tokens_authoritative" {
			t.Fatal("wrong candidate response")
		}
		if out.Minimum.Purpose != "hold_estimation_only" || len(out.Minimum.Cells) == 0 || len(out.Sources) == 0 {
			t.Fatal("lost evidence")
		}
		for _, p := range out.Prices {
			if model == ark.Model20 && p.Key == "generate/480p/false/default/false" {
				if !p.Configured || p.RetailUSDPer1M != "9.125" || !p.Enabled || p.RiskMultiplier != "4" || p.MinimumRuleVersion != "preserved-minimum-version" || p.SupplierCNYPer1M != "46" || !p.Available {
					t.Fatalf("current tariff lost: %#v", p)
				}
			} else {
				if p.Configured || p.RetailUSDPer1M != "" || p.Enabled || p.RiskMultiplier != "" || p.MinimumRuleVersion != "" {
					t.Fatalf("invented retail %#v", p)
				}
			}
			if model == ark.Model25 && p.Available {
				t.Fatal("unconfigured capability enabled")
			}
		}
		if model == ark.Model25 && out.Config != nil {
			t.Fatal("invented model config")
		}
	}
	var saved videobilling.PricingRule
	if e := db.Where("rule_id = ?", "rule-current").Take(&saved).Error; e != nil {
		t.Fatal(e)
	}
	if saved.SupplierCNYPer1M.String() != "999" || saved.RetailUSDPer1M.String() != "9.125" || writes != 0 {
		t.Fatal("readonly candidate changed pricing")
	}
}

func TestArkReferenceCandidatesRejectBindingMismatchAndCurrentDuplicate(t *testing.T) {
	for _, kind := range []string{"upstream_series", "upstream_variant", "duplicate_price"} {
		t.Run(kind, func(t *testing.T) {
			f, db, tokens := pricingCatalogFixture(t)
			if kind == "duplicate_price" {
				var p videobilling.PricingRule
				if e := db.First(&p).Error; e != nil {
					t.Fatal(e)
				}
				p.ID = 0
				p.RuleID = "ambiguous"
				if e := db.Create(&p).Error; e != nil {
					t.Fatal(e)
				}
			} else {
				var cfg video.ModelConfig
				db.First(&cfg)
				var cap ark.Capability
				_ = json.Unmarshal([]byte(cfg.CapabilityJSON), &cap)
				cap.UpstreamModel = "doubao-seedance-2-5-260128"
				if kind == "upstream_variant" {
					cap.UpstreamModel = "doubao-seedance-2-0-fast-260128"
				}
				raw, _ := json.Marshal(cap)
				if e := db.Model(&cfg).Update("capability_json", string(raw)).Error; e != nil {
					t.Fatal(e)
				}
			}
			catalogGET(t, f, "/api/admin/video/models/seedance-2.0/pricing/reference-candidates", tokens[1], "", 503)
		})
	}
}

func TestPricingCatalogConflictingStandardAliasIsNotEditable(t *testing.T) {
	f, db, tokens := pricingCatalogFixture(t)
	if e := db.Model(&relay.Channel{}).Where("id = ?", 99).Updates(map[string]any{"models": "seedance-2.0,seedance-1.5-pro", "model_mapping": `{"seedance-2.0":"doubao-seedance-2-5-260128","seedance-1.5-pro":"doubao-seedance-2-0-260128"}`}).Error; e != nil {
		t.Fatal(e)
	}
	w := catalogGET(t, f, "/api/admin/pricing-catalog", tokens[1], "", 200)
	var out struct {
		Models []catalogModel `json:"models"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	for _, row := range out.Models {
		if row.ChannelID == 99 {
			if row.PricingKind != "video" || row.VideoConfiguration != nil || row.VideoModel != nil || row.UnsupportedReason == "" {
				t.Fatalf("unsafe alias %#v", row)
			}
		}
	}
}

func TestSeedanceIdentityExcludesCapturedLegacySeries(t *testing.T) {
	for _, model := range []string{"seedance-1.0-pro", "seedance-1.0-pro-fast", "seedance-1.5-pro", "doubao-seedance-1-0-pro-fast-250528", "doubao-seedance-1-5-pro-251215"} {
		canonical, reason := seedanceIdentity(model)
		if canonical != "" || reason != "seedance_1x_excluded" {
			t.Fatalf("legacy model admitted: %s", model)
		}
	}
}
