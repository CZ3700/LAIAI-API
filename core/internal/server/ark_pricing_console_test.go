package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

const pricingConsoleSchema = `
CREATE TABLE video_model_configs(public_model TEXT PRIMARY KEY,version TEXT NOT NULL,provider_scope_id INTEGER NOT NULL,channel_id INTEGER NOT NULL,enabled INTEGER NOT NULL,capability_json TEXT NOT NULL,pricing_version TEXT NOT NULL,no_charge_statuses TEXT NOT NULL,max_concurrent_tasks INTEGER NOT NULL DEFAULT 0,create_rpm INTEGER NOT NULL DEFAULT 0,max_input_video_duration_ms INTEGER NOT NULL,bounds_json TEXT NOT NULL,verified_at DATETIME,updated_at DATETIME NOT NULL);
CREATE TABLE video_provider_scopes(id INTEGER PRIMARY KEY,scope_key TEXT NOT NULL,account_ref TEXT NOT NULL,region TEXT NOT NULL,project_name TEXT NOT NULL,enabled INTEGER NOT NULL,fx_usd_per_cny TEXT NOT NULL,fx_version TEXT NOT NULL,fx_observed_at DATETIME NOT NULL);
CREATE TABLE video_pricing_rules(id INTEGER PRIMARY KEY AUTOINCREMENT,rule_id TEXT NOT NULL,version TEXT NOT NULL,provider_scope_id INTEGER NOT NULL,model TEXT NOT NULL,mode TEXT NOT NULL,resolution TEXT NOT NULL,input_video INTEGER NOT NULL,service_tier TEXT NOT NULL,draft INTEGER NOT NULL,retail_usd_per_1m NUMERIC NOT NULL,supplier_cny_per_1m NUMERIC NOT NULL,minimum_tokens INTEGER NOT NULL,minimum_rule_version TEXT NOT NULL,risk_multiplier NUMERIC NOT NULL,formula_version TEXT NOT NULL,source_hash TEXT NOT NULL,effective_from DATETIME NOT NULL,effective_until DATETIME,enabled INTEGER NOT NULL,UNIQUE(rule_id,version));
CREATE TABLE video_configuration_revisions(id INTEGER PRIMARY KEY AUTOINCREMENT,public_model TEXT NOT NULL,version TEXT NOT NULL,capability_version TEXT NOT NULL,capability_hash TEXT NOT NULL,pricing_version TEXT NOT NULL,provider_scope_id INTEGER NOT NULL,channel_id INTEGER NOT NULL,created_by INTEGER NOT NULL,evidence_hash TEXT NOT NULL,created_at DATETIME NOT NULL,UNIQUE(public_model,version));
CREATE TABLE video_pricing_publications(id INTEGER PRIMARY KEY AUTOINCREMENT,model TEXT NOT NULL,version TEXT NOT NULL,config_version TEXT NOT NULL,idempotency_key TEXT NOT NULL,body_hash TEXT NOT NULL,snapshot_json TEXT NOT NULL,created_by INTEGER NOT NULL,created_at DATETIME NOT NULL,UNIQUE(model,version),UNIQUE(created_by,idempotency_key));
CREATE TABLE video_minimum_rule_sets(id INTEGER PRIMARY KEY AUTOINCREMENT,model TEXT NOT NULL,version TEXT NOT NULL,source_url TEXT NOT NULL,evidence_hash TEXT NOT NULL,observed_at DATETIME NOT NULL,verified_by INTEGER NOT NULL,verified_at DATETIME NOT NULL,UNIQUE(model,version));
CREATE TABLE video_minimum_rule_cells(id INTEGER PRIMARY KEY AUTOINCREMENT,rule_set_id INTEGER NOT NULL,resolution TEXT NOT NULL,ratio TEXT NOT NULL,output_duration_ms INTEGER NOT NULL,fps INTEGER NOT NULL,minimum_tokens INTEGER NOT NULL,UNIQUE(rule_set_id,resolution,ratio,output_duration_ms,fps));`

func pricingFixture(t *testing.T) (*gin.Engine, *gorm.DB, pricingRequest) {
	t.Helper()
	f, db := setupServer(t)
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	if e := db.Exec(pricingConsoleSchema).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Exec("INSERT INTO users(id,email,email_canonical,role,status) VALUES(1,'admin@test.invalid','admin@test.invalid',?,?)", identity.PlatRoleSuperadmin, identity.StatusActive).Error; e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	cap := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "cap-v1", Enabled: true, Verified: true, Resolutions: []string{"480p"}, Ratios: []string{"1:1"}, MinDuration: 4, MaxDuration: 5, DefaultDuration: 4, DefaultResolution: "480p", DefaultRatio: "1:1", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4, VideoInput: true, MaxVideos: 1}
	raw, _ := json.Marshal(cap)
	cfg := video.ModelConfig{PublicModel: ark.Model20, Version: "config-v1", ProviderScopeID: 1, ChannelID: 99, Enabled: true, CapabilityJSON: string(raw), PricingVersion: "price-v1", BoundsJSON: `{"480p/1:1":{"width":640,"height":640}}`, NoChargeStatuses: "failed,cancelled", MaxInputVideoDurationMS: 15000, VerifiedAt: &now, UpdatedAt: now}
	for _, v := range []any{&cfg, &video.ProviderScope{ID: 1, ScopeKey: "stable", AccountRef: "official-account", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "fx-v1", FXObservedAt: now}} {
		if e := db.Create(v).Error; e != nil {
			t.Fatal(e)
		}
	}
	r := gin.New()
	group := r.Group("/api/admin", func(c *gin.Context) {
		if c.GetHeader("Test-Actor") == "1" {
			c.Set("actor_id", uint64(1))
		}
	})
	f.srv.registerArkPricingConsole(group)
	in := pricingRequest{ExpectedConfigVersion: "config-v1", ConfigVersion: "config-v2", PricingVersion: "price-v2", EvidenceHash: strings.Repeat("a", 64), Cells: []pricingCell{{Mode: "generate", Resolution: "480p", ServiceTier: "default", Enabled: true, RetailUSDPer1M: "8", SupplierCNYPer1M: "46", RiskMultiplier: "1"}}}
	return r, db, in
}
func pricingCall(t *testing.T, r *gin.Engine, path string, in any, key, actor string, want int) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(in)
	req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Test-Actor", actor)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s got %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w
}
func TestPricingConsolePublicationPreservesScopeAndReplay(t *testing.T) {
	r, db, in := pricingFixture(t)
	base := "/api/admin/video/models/seedance-2.0/pricing"
	pricingCall(t, r, base+"/validate", in, "", "1", 200)
	pricingCall(t, r, base+"/publications", in, "pub1", "", 403)
	pricingCall(t, r, base+"/publications", in, "pub1", "1", 200)
	pricingCall(t, r, base+"/publications", in, "pub1", "1", 200)
	var cfg video.ModelConfig
	db.First(&cfg)
	if cfg.ProviderScopeID != 1 || cfg.ChannelID != 99 || cfg.Version != "config-v2" || cfg.PricingVersion != "price-v2" {
		t.Fatalf("scope or version changed incorrectly %#v", cfg)
	}
	var n int64
	db.Model(&pricingPublication{}).Count(&n)
	if n != 1 {
		t.Fatal("duplicate publication")
	}
	in.Cells[0].RetailUSDPer1M = "9"
	pricingCall(t, r, base+"/publications", in, "pub1", "1", 409)
	pricingCall(t, r, base+"/publications", in, "new-stale", "1", 409)
	var prices []videobilling.PricingRule
	db.Find(&prices)
	if len(prices) != 1 || prices[0].RetailUSDPer1M.String() != "8" {
		t.Fatal("failed publication changed price")
	}
}
func TestPricingConsoleRejectsMissingMinimumAndUnsafeDecimals(t *testing.T) {
	r, _, in := pricingFixture(t)
	path := "/api/admin/video/models/seedance-2.0/pricing/validate"
	for _, amount := range []string{"1e2", "-1", "NaN", "0", "01", "1.0000000000001", "9999999999999"} {
		in.Cells[0].RetailUSDPer1M = amount
		pricingCall(t, r, path, in, "", "1", 422)
	}
	in.Cells[0].RetailUSDPer1M = "8"
	in.Cells[0].InputVideo = true
	in.Cells[0].MinimumRuleVersion = "missing"
	pricingCall(t, r, path, in, "", "1", 422)
	in.Cells[0].InputVideo = false
	in.Cells[0].Resolution = "1080p"
	pricingCall(t, r, path, in, "", "1", 422)
	in.Cells[0].Resolution = "480p"
	in.Cells = append(in.Cells, in.Cells[0])
	pricingCall(t, r, path, in, "", "1", 422)
}
func TestPricingMatrixEnumeratesWithoutEnabling(t *testing.T) {
	for _, tc := range []struct {
		model string
		base  int
	}{{ark.Model20, 8}, {ark.Model25, 6}} {
		cells := priceMatrix(ark.Capability{PublicModel: tc.model}, nil)
		n := 0
		for _, c := range cells {
			if c.Mode == "generate" && !c.Draft {
				n++
			}
			if c.Enabled || c.Available || c.RetailUSDPer1M != "" || c.RiskMultiplier != "1" {
				t.Fatal("invented capability or commercial price")
			}
		}
		if n != tc.base {
			t.Fatal(tc.model, n)
		}
	}
}

func TestPricingConsoleDefaultsOnlyMissingRiskAndKeepsEmptyCells(t *testing.T) {
	for _, value := range []string{"", "2.5"} {
		t.Run("risk_"+value, func(t *testing.T) {
			r, db, in := pricingFixture(t)
			in.Cells[0].RiskMultiplier = value
			in.Cells = append(in.Cells, pricingCell{Mode: "generate", Resolution: "720p", ServiceTier: "default", RiskMultiplier: "1"})
			base := "/api/admin/video/models/seedance-2.0/pricing"
			pricingCall(t, r, base+"/validate", in, "", "1", 200)
			pricingCall(t, r, base+"/publications", in, "default-risk", "1", 200)
			var rules []videobilling.PricingRule
			if err := db.Find(&rules).Error; err != nil {
				t.Fatal(err)
			}
			want := value
			if want == "" {
				want = "1"
			}
			if len(rules) != 1 || rules[0].RiskMultiplier.String() != want {
				t.Fatalf("empty cell materialized or explicit factor changed: %#v", rules)
			}
			var receipt pricingPublication
			if err := db.First(&receipt).Error; err != nil {
				t.Fatal(err)
			}
			var snapshot pricingRequest
			if err := json.Unmarshal([]byte(receipt.SnapshotJSON), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Cells[0].RiskMultiplier != want {
				t.Fatal("snapshot omitted effective factor")
			}
		})
	}
}
