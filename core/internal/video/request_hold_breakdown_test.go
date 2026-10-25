package video

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func holdSnapshotFixture(t *testing.T) videobilling.Snapshot {
	t.Helper()
	meter := videobilling.MeteringInput{Resolution: "480p", Ratio: "1:1", Mode: "generate", ServiceTier: "default", OutputDurationMS: 4000, Width: 640, Height: 640, FPSNumerator: 24, FPSDenominator: 1, BoundsVerified: true, CapabilityVersion: "cap-mini-v1"}
	raw, err := json.Marshal(meter)
	must(t, err)
	return videobilling.Snapshot{OrgID: 1, TaskID: "task_mini", BillingSchemaVersion: videoruntime.BillingV1, ProviderScopeID: 11, Model: ark.Model20Mini, RuleID: "price-mini", RuleVersion: "price-v1", FormulaVersion: "ark-video-token-v1", CapabilityVersion: meter.CapabilityVersion, MeteringJSON: string(raw), EstimatedTokens: 153600, HoldUSD: decimal.RequireFromString("0.52555787"), RetailUSDPer1M: decimal.RequireFromString("3.421600714074"), SupplierCNYPer1M: decimal.NewFromInt(24), EffectiveRatio: decimal.NewFromInt(1), FXUSDPerCNY: decimal.RequireFromString("0.142566696420"), FXVersion: "fx-historical", FXObservedAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}
}

func TestRequestHoldBreakdownExplainsHistoricalMiniReservation(t *testing.T) {
	s := holdSnapshotFixture(t)
	got := requestHoldBreakdown(s)
	if got == nil || got.FormulaTokens != 38400 || got.MinimumTokens != 0 || got.BaseTokens != 38400 || got.ReservedTokens != 153600 || got.BaseAmountUSD != "0.13138947" || got.ReservationFactor != "4" {
		t.Fatalf("incorrect historical reservation: %+v", got)
	}
	if got.RiskMultiplier != nil || got.RiskMultiplierSource != "not_snapshotted" || got.FactorSource != "derived_from_snapshot" {
		t.Fatal("derived factor masquerades as policy", got)
	}
	encoded, err := json.Marshal(got)
	must(t, err)
	if !strings.Contains(string(encoded), `"risk_multiplier":null`) {
		t.Fatal("missing original multiplier must remain explicit null")
	}
	for _, private := range []string{"fx-historical", "0.142566696420", "supplier", "provider_scope", "price-mini"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private snapshot fact leaked", private)
		}
	}
}

func TestRequestHoldBreakdownKeepsMinimumAndRoundedFactorDistinct(t *testing.T) {
	s := holdSnapshotFixture(t)
	var meter videobilling.MeteringInput
	must(t, json.Unmarshal([]byte(s.MeteringJSON), &meter))
	meter.InputVideo, meter.InputDurationMS, meter.MinimumTokens = true, 2000, 70308
	s.MinimumRuleVersion = "min-historical"
	raw, err := json.Marshal(meter)
	must(t, err)
	s.MeteringJSON = string(raw)
	// Non-integral policy factors can produce a rounded reservation. The
	// observed ratio is not enough to recover the original risk multiplier.
	s.EstimatedTokens = 77339
	s.HoldUSD = s.RetailUSDPer1M.Mul(decimal.NewFromInt(s.EstimatedTokens)).Shift(-6).Mul(s.EffectiveRatio).RoundCeil(8)
	got := requestHoldBreakdown(s)
	if got == nil || got.FormulaTokens != 57600 || got.MinimumTokens != 70308 || got.BaseTokens != 70308 || got.ReservedTokens != 77339 || got.ReservationFactor != "1.100002844627" || got.RiskMultiplier != nil {
		t.Fatalf("minimum/rounded factor lost: %+v", got)
	}
	wantAmount := s.RetailUSDPer1M.Mul(decimal.NewFromInt(70308)).Shift(-6).RoundCeil(8).StringFixed(8)
	if got.BaseAmountUSD != wantAmount {
		t.Fatal("base amount used reserved tokens", got)
	}
}

func TestRequestHoldBreakdownReusesExactFrameAndRationalFormula(t *testing.T) {
	s := holdSnapshotFixture(t)
	s.Model = ark.Model20
	var meter videobilling.MeteringInput
	must(t, json.Unmarshal([]byte(s.MeteringJSON), &meter))
	meter.OutputDurationMS, meter.OutputFrames = 0, 97
	meter.FPSNumerator, meter.FPSDenominator = 24000, 1001
	raw, err := json.Marshal(meter)
	must(t, err)
	s.MeteringJSON, s.EstimatedTokens = string(raw), 77600
	s.HoldUSD = s.RetailUSDPer1M.Mul(decimal.NewFromInt(77600)).Shift(-6).RoundCeil(8)
	got := requestHoldBreakdown(s)
	if got == nil || got.FormulaTokens != 38800 || got.BaseTokens != 38800 || got.ReservationFactor != "2" {
		t.Fatalf("frame-based quote reconstructed incorrectly: %+v", got)
	}
}

func TestRequestHoldBreakdownDoesNotInventMissingOrInvalidFacts(t *testing.T) {
	cases := []struct {
		name string
		edit func(*videobilling.Snapshot)
	}{
		{"missing metering", func(s *videobilling.Snapshot) { s.MeteringJSON = "{}" }},
		{"malformed metering", func(s *videobilling.Snapshot) { s.MeteringJSON = "{" }},
		{"null metering", func(s *videobilling.Snapshot) { s.MeteringJSON = "null" }},
		{"unknown formula", func(s *videobilling.Snapshot) { s.FormulaVersion = "future-v2" }},
		{"missing formula", func(s *videobilling.Snapshot) { s.FormulaVersion = "" }},
		{"capability mismatch", func(s *videobilling.Snapshot) { s.CapabilityVersion = "current-capability" }},
		{"hold mismatch", func(s *videobilling.Snapshot) { s.HoldUSD = decimal.RequireFromString("0.52555786") }},
		{"no reserve", func(s *videobilling.Snapshot) { s.EstimatedTokens = 0 }},
		{"under-base reserve", func(s *videobilling.Snapshot) { s.EstimatedTokens = 38399 }},
		{"missing original price", func(s *videobilling.Snapshot) { s.RetailUSDPer1M = decimal.Zero }},
		{"empty billing schema", func(s *videobilling.Snapshot) { s.BillingSchemaVersion = "" }},
		{"unknown billing schema", func(s *videobilling.Snapshot) { s.BillingSchemaVersion = "usd-decimal-v2" }},
	}
	for _, field := range []string{"InputVideo", "Draft", "MinimumTokens", "InputDurationMS", "OutputDurationMS", "OutputFrames", "Width", "Height", "FPSNumerator", "FPSDenominator", "BoundsVerified", "CapabilityVersion", "Mode", "Resolution", "ServiceTier", "Ratio"} {
		cases = append(cases, struct {
			name string
			edit func(*videobilling.Snapshot)
		}{"absent " + field, func(s *videobilling.Snapshot) {
			var object map[string]json.RawMessage
			must(t, json.Unmarshal([]byte(s.MeteringJSON), &object))
			delete(object, field)
			raw, err := json.Marshal(object)
			must(t, err)
			s.MeteringJSON = string(raw)
		}})
	}
	for _, change := range []struct{ field, value string }{{"Width", "0"}, {"Height", "9223372036854775807"}, {"FPSDenominator", "0"}, {"BoundsVerified", "false"}, {"MinimumTokens", "-1"}, {"MinimumTokens", "null"}, {"MinimumTokens", "1000"}, {"InputDurationMS", "1000"}, {"OutputDurationMS", "-1"}, {"OutputFrames", "97"}, {"Ratio", `""`}} {
		cases = append(cases, struct {
			name string
			edit func(*videobilling.Snapshot)
		}{"invalid " + change.field + " " + change.value, func(s *videobilling.Snapshot) {
			var object map[string]json.RawMessage
			must(t, json.Unmarshal([]byte(s.MeteringJSON), &object))
			object[change.field] = json.RawMessage(change.value)
			raw, err := json.Marshal(object)
			must(t, err)
			s.MeteringJSON = string(raw)
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := holdSnapshotFixture(t)
			tc.edit(&s)
			if got := requestHoldBreakdown(s); got != nil {
				t.Fatalf("invalid snapshot fabricated a breakdown: %+v", got)
			}
		})
	}
}

func TestRequestBillingReadOnlyProjectionPreservesExactPriceAndMoney(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	must(t, err)
	sqlDB, err := db.DB()
	must(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	// TEXT preserves decimal input bytes; this is a read projection fixture,
	// not a replacement for production DECIMAL or a migration.
	must(t, db.Exec(`
CREATE TABLE video_billing_snapshots(id INTEGER PRIMARY KEY,org_id INTEGER,task_id TEXT,provider_scope_id INTEGER,model TEXT,rule_id TEXT,rule_version TEXT,formula_version TEXT,capability_version TEXT,metering_json TEXT,estimated_tokens INTEGER,hold_usd TEXT,retail_usd_per_1m TEXT,supplier_cny_per_1m TEXT,effective_ratio TEXT,fx_usd_per_cny TEXT,fx_version TEXT,fx_observed_at DATETIME,created_at DATETIME,billing_schema_version TEXT NOT NULL);
CREATE TABLE video_task_parameters(org_id INTEGER,task_id TEXT,parameters_json TEXT);
CREATE TABLE video_billing_entries(id INTEGER PRIMARY KEY,org_id INTEGER,task_id TEXT,state TEXT,captured_usd TEXT,released_usd TEXT,completion_tokens INTEGER,created_at DATETIME);
CREATE TABLE video_adjustment_applications(id INTEGER PRIMARY KEY,org_id INTEGER,task_id TEXT,net_usd TEXT,customer_delta_usd TEXT,completion_tokens INTEGER,created_at DATETIME);
INSERT INTO video_billing_entries VALUES(1,1,'task_mini','settled','0.13275811','0.39279976',38800,'2026-10-20 00:00:01');
`).Error)
	s := holdSnapshotFixture(t)
	must(t, db.Exec(`INSERT INTO video_billing_snapshots(id,org_id,task_id,provider_scope_id,model,rule_id,rule_version,formula_version,capability_version,metering_json,estimated_tokens,hold_usd,retail_usd_per_1m,supplier_cny_per_1m,effective_ratio,fx_usd_per_cny,fx_version,fx_observed_at,created_at,billing_schema_version) VALUES(1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.OrgID, s.TaskID, s.ProviderScopeID, s.Model, s.RuleID, s.RuleVersion, s.FormulaVersion, s.CapabilityVersion, s.MeteringJSON, s.EstimatedTokens, s.HoldUSD.String(), s.RetailUSDPer1M.String(), s.SupplierCNYPer1M.String(), s.EffectiveRatio.String(), s.FXUSDPerCNY.String(), s.FXVersion, s.FXObservedAt, s.FXObservedAt, s.BillingSchemaVersion).Error)
	// No price/config/ledger tables exist: a projection cannot consult current
	// policy or cause financial writes. SQLite rejects any attempted mutation.
	must(t, db.Exec("PRAGMA query_only=ON").Error)
	actual := int64(38800)
	b, err := requestBilling(db, 1, State{TaskID: s.TaskID, BillingState: "settled", ObservedCompletionTokens: &actual})
	must(t, err)
	if b.UnitPriceUSD != "3.421600714074" || b.ReservedAmount != "0.52555787" || b.SettledAmount == nil || *b.SettledAmount != "0.13275811" || b.ReleasedAmount != "0.39279976" || b.NetChargedAmount == nil || *b.NetChargedAmount != "0.13275811" {
		t.Fatalf("price lost precision or historical money changed: %+v", b)
	}
	if b.HoldBreakdown == nil || b.HoldBreakdown.ReservationFactor != "4" || b.HoldBreakdown.BaseAmountUSD != "0.13138947" || b.ActualUsage.CompletionTokens == nil || *b.ActualUsage.CompletionTokens != 38800 || b.BillableTokens == nil || *b.BillableTokens != 38800 || len(b.Entries) != 3 {
		t.Fatalf("reservation explanation altered settlement or entries: %+v", b)
	}
	// Unknown schema prevents formula interpretation, while amounts already
	// recorded in the financial journal remain a readable historical fact.
	must(t, db.Exec("PRAGMA query_only=OFF").Error)
	must(t, db.Exec("UPDATE video_billing_snapshots SET billing_schema_version = ? WHERE org_id = ? AND task_id = ?", "usd-decimal-v2", s.OrgID, s.TaskID).Error)
	must(t, db.Exec("PRAGMA query_only=ON").Error)
	unknown, err := requestBilling(db, 1, State{TaskID: s.TaskID, BillingState: "settled", ObservedCompletionTokens: &actual})
	must(t, err)
	if unknown.HoldBreakdown != nil || unknown.ReservedAmount != b.ReservedAmount || unknown.SettledAmount == nil || *unknown.SettledAmount != *b.SettledAmount || unknown.NetChargedAmount == nil || *unknown.NetChargedAmount != *b.NetChargedAmount || unknown.ReleasedAmount != b.ReleasedAmount {
		t.Fatal("unknown billing schema guessed the formula or hid persisted money")
	}
	if _, err := requestBilling(db, 2, State{TaskID: s.TaskID}); err != gorm.ErrRecordNotFound {
		t.Fatal("cross-tenant snapshot projection", err)
	}
}
