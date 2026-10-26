package video

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
)

func catalogTestConfig(t *testing.T, model string) ModelConfig {
	t.Helper()
	p, err := arkverification.Load(model)
	if err != nil {
		t.Fatal(err)
	}
	upstreams := map[string]string{ark.Model20: "doubao-seedance-2-0-260128", ark.Model25: "doubao-seedance-2-5-260628", ark.Model20Fast: "doubao-seedance-2-0-fast-260128", ark.Model20Mini: "doubao-seedance-2-0-mini-260615"}
	p.Capability.UpstreamModel = upstreams[model]
	cap, _ := json.Marshal(p.Capability)
	bounds, _ := json.Marshal(p.Bounds)
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	return ModelConfig{PublicModel: model, Version: "test-config", ProviderScopeID: 1, ChannelID: 2, Enabled: true, CapabilityJSON: string(cap), PricingVersion: "test-price", NoChargeStatuses: "failed,cancelled", MaxInputVideoDurationMS: p.MaxInputVideoDurationMS, BoundsJSON: string(bounds), VerifiedAt: &now}
}

func TestPublicationCapabilityCanonicalCellsAndDetachedContext(t *testing.T) {
	for model, expected := range map[string]int{ark.Model20: 8, ark.Model25: 6, ark.Model20Fast: 4, ark.Model20Mini: 4} {
		t.Run(model, func(t *testing.T) {
			cfg := catalogTestConfig(t, model)
			p, err := ParsePublicationCapability(cfg)
			if err != nil {
				t.Fatal(err)
			}
			cells := p.Cells()
			if len(cells) != expected {
				t.Fatalf("unexpected supported cell count %d", len(cells))
			}
			for i, cell := range cells {
				if cell.Mode != "generate" || cell.ServiceTier != "default" || cell.Draft || !p.Supports(cell) || i > 0 && cells[i-1].Key() >= cell.Key() {
					t.Fatal("invalid or unstable publication matrix")
				}
			}
			if p.Supports(PublicationCell{Mode: "generate", Resolution: "720p", ServiceTier: "flex"}) {
				t.Fatal("unverified tier admitted")
			}
			context, err := p.BillingContext(ProviderScope{FXUSDPerCNY: "0.14", FXVersion: "fx-v1", FXObservedAt: *cfg.VerifiedAt})
			if err != nil {
				t.Fatal(err)
			}
			context.Ratios[0] = "not-a-ratio"
			context.Bounds[0].Width = 1
			again, err := p.BillingContext(ProviderScope{FXUSDPerCNY: "0.14", FXVersion: "fx-v1", FXObservedAt: *cfg.VerifiedAt})
			if err != nil || again.Ratios[0] == "not-a-ratio" || again.Bounds[0].Width == 1 {
				t.Fatal("caller mutated captured capability")
			}
		})
	}
	var zero PublicationCapability
	if len(zero.Cells()) != 0 || zero.Supports(PublicationCell{Mode: "generate"}) {
		t.Fatal("zero capability admitted")
	}
}

func TestPublicationInvalidCapabilityAndVariantIdentity(t *testing.T) {
	for _, mode := range []string{"alias", "wrong_upstream", "duplicate_resolution", "bad_bounds", "unknown_ratio", "empty_version", "frames_invalid", "input_bound"} {
		t.Run(mode, func(t *testing.T) {
			cfg := catalogTestConfig(t, ark.Model20Mini)
			var cap ark.Capability
			_ = json.Unmarshal([]byte(cfg.CapabilityJSON), &cap)
			switch mode {
			case "alias":
				cfg.PublicModel = "doubao-seedance-2-0-mini-260615"
			case "wrong_upstream":
				cap.UpstreamModel = "doubao-seedance-2-0-fast-260128"
			case "duplicate_resolution":
				cap.Resolutions = append(cap.Resolutions, cap.Resolutions[0])
			case "bad_bounds":
				cfg.BoundsJSON = `{"480p/1:1":{"width":1,"height":1}}`
			case "unknown_ratio":
				cap.Ratios = append(cap.Ratios, "future")
			case "empty_version":
				cfg.Version = ""
			case "frames_invalid":
				cap.Frames = true
				cap.MinFrames = 0
				cap.MaxFrames = 10
				cap.FrameStep = 1
			case "input_bound":
				cfg.MaxInputVideoDurationMS--
			}
			raw, _ := json.Marshal(cap)
			cfg.CapabilityJSON = string(raw)
			if _, err := ParsePublicationCapability(cfg); err == nil {
				t.Fatal("invalid publication capability admitted")
			}
		})
	}
}

func TestPublicationDisabledDiagnosticKeepsCapabilitiesAndVersions(t *testing.T) {
	cfg := catalogTestConfig(t, ark.Model20)
	cfg.Enabled = false
	row, err := catalogModel(cfg, ProviderScope{}, relay.VideoCatalogChannel{}, decimal.NewFromInt(1), videobilling.CatalogTariffSnapshot{})
	if err != nil || row.Enabled || !row.Verified || row.Publication.Status != "blocked" || len(row.Publication.Combinations) != 8 || len(row.Capabilities.Ratios) != 6 || len(row.Prices) != 0 {
		t.Fatal("disabled configuration lost diagnostic shape", err)
	}
	for _, cell := range row.Publication.Combinations {
		if cell.BillingRuleVersion != nil || cell.MinimumRuleVersion != nil || !publicationContains(cell.ReasonCodes, "model_disabled") {
			t.Fatal("unselected version was fabricated")
		}
	}
	row.Provider = "volcengine"
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"Provider"`) || strings.Contains(string(raw), `"provider"`) || strings.Contains(string(raw), "supplier") || strings.Contains(string(raw), "credential") {
		t.Fatal("private facts entered safe row JSON")
	}
}

func TestPublicationPricingVersionBoundKeepsInvalidDiagnostic(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, size := range []int{64, 65, 128} {
			cfg := catalogTestConfig(t, ark.Model20Mini)
			cfg.Enabled = enabled
			cfg.PricingVersion = strings.Repeat("v", size)
			_, err := ParsePublicationCapability(cfg)
			if size == 64 {
				if err != nil {
					t.Fatal("maximum valid pricing version rejected", err)
				}
				continue
			}
			if err == nil {
				t.Fatal("out-of-domain pricing version admitted")
			}
			row, err := catalogModel(cfg, ProviderScope{}, relay.VideoCatalogChannel{}, decimal.NewFromInt(1), videobilling.CatalogTariffSnapshot{})
			if err != nil || row.Publication.Status != "blocked" || !publicationContains(row.Publication.ReasonCodes, "capability_invalid") || len(row.Prices) != 0 || len(row.Publication.Combinations) != 0 || row.PricingVersion != cfg.PricingVersion {
				t.Fatal("invalid persisted version lost its safe diagnostic", err)
			}
			if !enabled && !publicationContains(row.Publication.ReasonCodes, "model_disabled") {
				t.Fatal("disabled diagnostic was lost")
			}
		}
	}
}

func TestPublicationActionsAreDistinctAndZeroObservationDenies(t *testing.T) {
	var zero CatalogObservation
	if zero.AllowsModel(ark.Model20) {
		t.Fatal("zero observation interpreted as unrestricted")
	}
	s, key := actionSubjectDB(t)
	must(t, s.DB.Exec("UPDATE api_keys SET model_limits=? WHERE id=2", "  "+ark.Model20+" , "+ark.Model20Mini+"  ").Error)
	allowed := CatalogObservation{subject: actionSubject(t, s, key)}
	if !allowed.AllowsModel(ark.Model20) || !allowed.AllowsModel(ark.Model20Mini) || allowed.AllowsModel(ark.Model25) || allowed.AllowsModel(strings.ToUpper(ark.Model20)) {
		t.Fatal("legacy exact whitelist changed")
	}
	policy := Policy{AsyncExecutionAllowed: true, RetentionDays: 30}
	for _, scope := range []string{"video:read", "video:write", "video:*", "*", ""} {
		a := catalogActions(identity.RoutingCandidateSubject{KeyScopes: scope, FeatureVideo: true}, policy, true, true, true)
		wantRead := scope == "video:read" || scope == "video:*" || scope == "*"
		wantWrite := scope == "video:write" || scope == "video:*" || scope == "*"
		if a.ModelsRead.Eligible != wantRead || a.Estimate.Eligible != wantWrite || a.Create.Eligible != wantWrite {
			t.Fatal("read/write scope cross-granted")
		}
	}
	a := catalogActions(identity.RoutingCandidateSubject{KeyScopes: "video:*", FeatureVideo: false}, Policy{}, false, true, false)
	if !a.ModelsRead.Eligible || !a.Estimate.Eligible || a.Create.Eligible || !reflect.DeepEqual(a.Create.ReasonCodes, []string{"video_feature_disabled", "video_execution_disabled", "credential_unavailable"}) {
		t.Fatal("create prerequisites changed estimate/read compatibility")
	}
	a = catalogActions(identity.RoutingCandidateSubject{KeyScopes: "*", FeatureVideo: true}, Policy{AsyncExecutionAllowed: true, RetentionDays: 31}, true, true, true)
	if a.Create.Eligible || !reflect.DeepEqual(a.Create.ReasonCodes, []string{"video_execution_policy_invalid"}) {
		t.Fatal("invalid persistent policy admitted")
	}
}

func TestPublicationChannelNeverUsesBYOKOrWrongOfficialBinding(t *testing.T) {
	valid := relay.VideoCatalogChannel{ID: 2, PlatformOwned: true, Enabled: true, OfficialArkEndpoint: true, Provider: "volcengine"}
	if !PublicationChannelUsable(valid) {
		t.Fatal("official binding rejected")
	}
	for _, mutate := range []func(*relay.VideoCatalogChannel){func(c *relay.VideoCatalogChannel) { c.ID = 0 }, func(c *relay.VideoCatalogChannel) { c.PlatformOwned = false }, func(c *relay.VideoCatalogChannel) { c.Enabled = false }, func(c *relay.VideoCatalogChannel) { c.OfficialArkEndpoint = false }, func(c *relay.VideoCatalogChannel) { c.Provider = "Volcengine" }} {
		ch := valid
		mutate(&ch)
		if PublicationChannelUsable(ch) {
			t.Fatal("invalid fixed channel admitted")
		}
	}
}
