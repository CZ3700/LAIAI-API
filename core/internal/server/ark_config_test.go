package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type arkConfigGate struct{}

func (arkConfigGate) GateKey(*identity.APIKey, string, string) (int, string, bool) {
	return 0, "", true
}
func (arkConfigGate) AllowModelRate(uint64, string) (bool, int) { return true, 1000 }

func TestVideoAdminRejectsAmbiguousJSON(t *testing.T) {
	for _, body := range []string{`{"Version":"a","version":"b"}`, `{"Version":"a","nested":{"x":1,"x":2}}`, `{"unknown":1}`, `{"Version":"a"} {}`} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		var in struct{ Version string }
		if videoAdminDecode(c, &in) == nil {
			t.Fatalf("accepted ambiguous document: %s", body)
		}
	}
}

// Exercises administrative publication and the same public routing function
// used by main. Every provider request is forbidden by an injected transport.
func TestMySQLArkConfigurationPublicationAndPublicRoutes(t *testing.T) {
	for _, tc := range []struct {
		model, upstream string
		maxDuration     int
	}{
		{ark.Model25, "doubao-seedance-2-5-260628", 30},
		{ark.Model20Fast, "doubao-seedance-2-0-fast-260128", 15},
		{ark.Model20Mini, "doubao-seedance-2-0-mini-260615", 15},
	} {
		t.Run(tc.model, func(t *testing.T) {
			testMySQLArkConfigurationPublicationAndPublicRoutes(t, tc.model, tc.upstream, tc.maxDuration)
		})
	}
}

func testMySQLArkConfigurationPublicationAndPublicRoutes(t *testing.T, model, upstream string, maxDuration int) {
	f := arkBillMySQLFixture(t, "running")
	svc, e := video.NewService(f.db, f.s.keyring, []byte(strings.Repeat("h", 32)), nil)
	arkBillMust(t, e)
	svc.Now = func() time.Time { return f.now }
	svc.Gate = arkConfigGate{}
	svc.Client = &http.Client{Transport: configNoNetwork{t}}
	svc.ArtifactService = &videoartifact.Service{}
	arkBillMust(t, svc.ConfigureAssets(video.AssetIntegrationOptions{}))
	f.s.video = svc
	su := f.r.Group("/api/admin", f.s.adminRequired(identity.PlatRoleAdmin), f.s.requireSuperadmin())
	f.s.registerVideoConfiguration(su)
	f.s.registerArkAssetAdmin(su)
	svc.RegisterRoutes(f.r.Group(""), task.NewService(f.db, f.s.keyring, nil))
	call := func(method, path, token string, body any, want int) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			var e error
			raw, e = json.Marshal(body)
			arkBillMust(t, e)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "config-integration-"+f.uid+path)
		w := httptest.NewRecorder()
		f.r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	cipher, e := f.s.keyring.Encrypt("test-official-never-sent")
	arkBillMust(t, e)
	ch := relay.Channel{Name: "Config test", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", CredentialEnc: cipher, Models: model, Status: 1, Weight: 1, MaxConcurrentTasks: 20}
	arkBillMust(t, f.db.Create(&ch).Error)
	cap := ark.Capability{PublicModel: model, UpstreamModel: upstream, Version: "config-cap-v1", Enabled: true, Verified: true, Resolutions: []string{"720p"}, Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: maxDuration, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4}
	capJSON, _ := json.Marshal(cap)
	var scope video.ProviderScope
	arkBillMust(t, f.db.First(&scope, f.scope).Error)
	scope.FXUSDPerCNY = "0.14"
	version := f.uid + "-1"
	limit := decimal.NewFromInt(1000)
	in := videoPublication{Configuration: video.ModelConfig{PublicModel: model, Version: version, ChannelID: ch.ID, Enabled: true, CapabilityJSON: string(capJSON), PricingVersion: version, BoundsJSON: `{"720p/16:9":{"width":1280,"height":720}}`, NoChargeStatuses: "failed,cancelled"}, Scope: scope, EvidenceHash: strings.Repeat("a", 64), Prices: []videobilling.PricingRule{{RuleID: version, Version: version, PricingKey: videobilling.PricingKey{Model: model, Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "test-formula", EffectiveFrom: f.now.Add(-time.Hour)}}, SupplierBudgets: []videobilling.QuotaPolicy{{Scope: videobilling.ScopeProvider, Metric: videobilling.MetricCNY, Period: videobilling.PeriodDay, Timezone: "UTC", LimitAmount: &limit, Version: version}, {Scope: videobilling.ScopeProvider, Metric: videobilling.MetricCNY, Period: videobilling.PeriodMonth, Timezone: "UTC", LimitAmount: &limit, Version: version}}}
	call("POST", "/api/admin/video/configurations", f.tokens[0], in, 201)
	originalVersion := in.Configuration.PricingVersion
	// New configuration identity may not silently reuse a changed price version.
	in.Configuration.Version = f.uid + "-2"
	in.Prices[0].RetailUSDPer1M = decimal.NewFromInt(9)
	call("POST", "/api/admin/video/configurations", f.tokens[0], in, 422)
	var active int64
	arkBillMust(t, f.db.Model(&videobilling.PricingRule{}).Where("provider_scope_id=? AND model=? AND enabled=?", scope.ID, model, true).Count(&active).Error)
	if active != 1 {
		t.Fatal("failed publication changed prices")
	}
	// Changing capability semantics under the same version is also rejected.
	cap.MaxDuration = 20
	changed, _ := json.Marshal(cap)
	in.Configuration.CapabilityJSON = string(changed)
	in.Configuration.PricingVersion = f.uid + "-2"
	in.Prices[0].Version = f.uid + "-2"
	in.Prices[0].RuleID = f.uid + "-2"
	call("POST", "/api/admin/video/configurations", f.tokens[0], in, 422)
	in.Configuration.CapabilityJSON = string(capJSON)
	for i := range in.SupplierBudgets {
		in.SupplierBudgets[i].Version = f.uid + "-2"
	}
	call("POST", "/api/admin/video/configurations", f.tokens[0], in, 201)
	arkBillMust(t, f.db.Model(&videobilling.PricingRule{}).Where("provider_scope_id=? AND model=? AND enabled=?", scope.ID, model, true).Count(&active).Error)
	if active != 1 {
		t.Fatal("publication left overlapping active prices")
	}
	call("PUT", fmt.Sprintf("/api/admin/video/org-policies/%d", f.org), f.tokens[0], gin.H{"async_execution_allowed": true, "capture_history": false, "retention_days": 1}, 204)
	var plain string
	arkBillMust(t, f.db.Transaction(func(tx *gorm.DB) error {
		var e error
		plain, _, e = identity.IssueKey(tx, identity.IssueKeyInput{OrgID: f.org, Name: "public-config", Scopes: "video:*"})
		return e
	}))
	request := gin.H{"model": model, "content": []gin.H{{"type": "text", "text": "a test landscape"}}, "expected_pricing_version": originalVersion}
	call("POST", "/v1/video/generations", plain, request, 409)
	request["expected_pricing_version"] = in.Configuration.PricingVersion
	created := call("POST", "/v1/video/generations", plain, request, 202)
	var view video.TaskView
	arkBillMust(t, json.Unmarshal(created.Body.Bytes(), &view))
	if view.ID == "" || view.Status != "accepted" || view.BillingState != "reserved" {
		t.Fatalf("bad create view %+v", view)
	}
	repeated := call("POST", "/v1/video/generations", plain, request, 202)
	if repeated.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("replay failed")
	}
	var before []videobilling.QuotaBucket
	arkBillMust(t, f.db.Where("org_id=0 AND policy_id IN (?)", f.db.Model(&videobilling.QuotaPolicy{}).Select("id").Where("scope=? AND owner_id=?", videobilling.ScopeProvider, scope.ID)).Find(&before).Error)
	if len(before) != 2 || !before[0].ReservedAmount.IsPositive() {
		t.Fatal("supplier reservation missing")
	}
	// An alternative display key cannot create a fresh budget for the same
	// official account/project. Database uniqueness covers concurrent operators.
	in.Configuration.Version = f.uid + "-3"
	in.Configuration.PricingVersion = f.uid + "-3"
	in.Prices[0].Version = f.uid + "-3"
	in.Prices[0].RuleID = f.uid + "-3"
	for i := range in.SupplierBudgets {
		in.SupplierBudgets[i].Version = f.uid + "-3"
	}
	in.Scope.ID = 0
	in.Scope.ScopeKey = f.uid + "-duplicate"
	call("POST", "/api/admin/video/configurations", f.tokens[0], in, 422)
	duplicate := scope
	duplicate.ID = 0
	duplicate.ScopeKey = f.uid + "-direct-duplicate"
	if f.db.Create(&duplicate).Error == nil {
		t.Fatal("database accepted duplicate financial scope")
	}
	in.Scope = scope
	in.Scope.FXVersion = f.uid + "-fx2"
	in.Scope.FXUSDPerCNY = "0.15"
	call("POST", "/api/admin/video/configurations", f.tokens[0], in, 201)
	for _, old := range before {
		var after videobilling.QuotaBucket
		arkBillMust(t, f.db.First(&after, old.ID).Error)
		if !after.ReservedAmount.Equal(old.ReservedAmount) || !after.ConsumedAmount.Equal(old.ConsumedAmount) {
			t.Fatal("publication reset supplier usage")
		}
	}
	var snap videobilling.Snapshot
	arkBillMust(t, f.db.Where("org_id=? AND task_id=?", f.org, view.ID).First(&snap).Error)
	if !snap.FXUSDPerCNY.Equal(decimal.RequireFromString("0.14")) {
		t.Fatal("FX revision changed historical snapshot")
	}
	for _, suffix := range []string{"", "/billing", "/events"} {
		call("GET", "/v1/video/generations/"+view.ID+suffix, plain, nil, 200)
	}
	call("GET", "/v1/video/models", plain, nil, 200)
	call("GET", "/v1/video/assets", plain, nil, 200)
	call("GET", "/v1/video/portrait-authorizations", plain, nil, 200)
	cancelled := call("POST", "/v1/video/generations/"+view.ID+"/cancel", plain, nil, 202)
	var op video.Operation
	arkBillMust(t, json.Unmarshal(cancelled.Body.Bytes(), &op))
	call("GET", "/v1/video/operations/"+op.ID, plain, nil, 200)
	call("DELETE", "/v1/video/generations/"+view.ID, plain, nil, 202)
	call("DELETE", "/v1/video/generations/"+view.ID, plain, nil, 204)
	call("GET", "/v1/video/generations/"+view.ID, plain, nil, 404)
	// A shared emergency credential cannot upload private media or attest licenses.
	req := httptest.NewRequest("POST", "/api/admin/video/asset-scopes", strings.NewReader(`{}`))
	req.Header.Set("X-Admin-Token", "shared-root-test")
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("shared administrator accepted: %d", w.Code)
	}
	// Disabling the new runtime must never expose official tasks through the
	// legacy handler, including its weaker read scopes and absent soft-delete.
	disabled := New(f.db, Options{AuthSecret: f.s.secret, Keyring: f.s.keyring})
	for _, id := range []string{view.ID, f.taskID} {
		for _, suffix := range []string{"", "/content"} {
			req := httptest.NewRequest("GET", "/v1/video/generations/"+id+suffix, nil)
			req.Header.Set("Authorization", "Bearer "+plain)
			w := httptest.NewRecorder()
			disabled.ServeHTTP(w, req)
			if w.Code != 404 {
				t.Fatalf("disabled official task leaked through legacy route: %d %s", w.Code, w.Body.String())
			}
		}
	}
}

type configNoNetwork struct{ t *testing.T }

func (n configNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Error("public acceptance must not contact provider")
	return nil, fmt.Errorf("network forbidden")
}
