package videobilling

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// All minimum values here are deliberately synthetic fixture evidence.
func minimumReservationFixture(t *testing.T) (*gorm.DB, PricingRule, MeteringInput, MinimumRuleSet) {
	t.Helper()
	db, _ := setup(t, "100")
	if err := db.Exec(minimumTestSchema).Error; err != nil {
		t.Fatal(err)
	}
	r := ruleFixture()
	r.InputVideo, r.MinimumRuleVersion = true, "minimum-reserve-v1"
	if err := db.Model(&PricingRule{}).Where("rule_id = ?", r.RuleID).Updates(map[string]any{"input_video": true, "minimum_rule_version": r.MinimumRuleVersion}).Error; err != nil {
		t.Fatal(err)
	}
	set := MinimumRuleSet{Model: r.Model, Version: r.MinimumRuleVersion, SourceURL: "https://docs.volcengine.com/fixture-only", EvidenceHash: strings.Repeat("a", 64), ObservedAt: testAt, VerifiedAt: testAt, VerifiedBy: 77}
	// The shorter duration can have a larger minimum. Never assume monotonicity.
	cells := []MinimumRuleCell{
		{Resolution: "720p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 500000},
		{Resolution: "720p", Ratio: "16:9", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 400000},
		{Resolution: "720p", Ratio: "1:1", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 800000},
		{Resolution: "720p", Ratio: "1:1", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 700000},
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		set, err = PublishMinimumTx(tx, set, cells)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m := meterFixture()
	m.InputVideo, m.InputDurationMS, m.OutputDurationMS = true, 4000, 4000
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("100")})
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeKey, OwnerID: 1, Metric: MetricTokens, LimitUnits: token(10000000)})
	return db, r, m, set
}

func minimumQuote(t *testing.T, db *gorm.DB, r PricingRule, m MeteringInput, ctx MinimumContext) QuoteResult {
	t.Helper()
	var q QuoteResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		q, err = QuoteWithMinimumTx(tx, r, m, ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestMinimumReservationMatchesQuoteAndImmutableSnapshot(t *testing.T) {
	db, r, m, set := minimumReservationFixture(t)
	q := minimumQuote(t, db, r, m, MinimumContext{Resolution: "720p", Ratio: "16:9"})
	if q.EstimatedTokens != 500000 || !q.HoldUSD.Equal(d("4")) || !q.SupplierReservedCNY.Equal(d("23")) {
		t.Fatalf("minimum did not dominate geometry: %+v", q)
	}
	got := reserve(t, db, q, "task_minimum")
	var snap Snapshot
	if err := db.First(&snap, got.Snapshot.ID).Error; err != nil {
		t.Fatal(err)
	}
	var meter MeteringInput
	if err := json.Unmarshal([]byte(snap.MeteringJSON), &meter); err != nil {
		t.Fatal(err)
	}
	if snap.EstimatedTokens != q.EstimatedTokens || !snap.HoldUSD.Equal(q.HoldUSD) || !snap.SupplierReservedCNY.Equal(q.SupplierReservedCNY) || meter.MinimumRuleSetID != set.ID || meter.MinimumEvidenceHash != set.EvidenceHash || meter.MinimumTokens != 500000 || meter.Ratio != "16:9" || meter.OutputDurationMS != 4000 || snap.MinimumRuleVersion != set.Version {
		t.Fatalf("hold/snapshot disagrees with estimate: %+v / %+v", snap, meter)
	}
	checkBalance(t, db, "100", "4")
	if !keyUsed(t, db).Equal(d("4")) || count(t, db, "video_quota_reservations") != 2 {
		t.Fatal("key/policy reservations disagree with hold")
	}
	// Replays must retain the accepted snapshot even after current config changes.
	if err := db.Model(&MinimumRuleSet{}).Where("id = ?", set.ID).Update("verified_by", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PricingRule{}).Where("rule_id = ?", r.RuleID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	replay := reserve(t, db, QuoteResult{}, "task_minimum")
	if !replay.Replay || replay.Snapshot.ID != snap.ID || replay.Snapshot.MeteringJSON != snap.MeteringJSON || count(t, db, "ledger_holds") != 1 || count(t, db, "video_billing_snapshots") != 1 || count(t, db, "video_quota_reservations") != 2 {
		t.Fatal("replay replaced or duplicated immutable reservation")
	}
	checkBalance(t, db, "100", "4")
	settled := settle(t, db, "task_minimum", token(100000), "1")
	if settled.Entry == nil || !settled.Entry.CapturedUSD.Equal(d("0.8")) || !settled.Entry.ReleasedUSD.Equal(d("3.2")) || !settled.Entry.SupplierCNY.Equal(d("4.6")) {
		t.Fatalf("actual usage did not settle from original price: %+v", settled)
	}
	checkBalance(t, db, "99.2", "0")
	if !keyUsed(t, db).Equal(d("0.8")) {
		t.Fatal("key did not release unused minimum reservation")
	}
}

func TestMinimumReservationKeepsAdaptiveAutomaticDomain(t *testing.T) {
	db, r, m, _ := minimumReservationFixture(t)
	m.OutputDurationMS = 5000
	ctx := MinimumContext{Resolution: "720p", Ratio: "adaptive", Ratios: []string{"16:9", "1:1", "adaptive"}, AutomaticDuration: true, MinDuration: 4, MaxDuration: 5}
	q := minimumQuote(t, db, r, m, ctx)
	if q.EstimatedTokens != 800000 {
		t.Fatal("did not cover shorter duration and every adaptive ratio")
	}
	// The quotation owns its domain; mutating the caller's slice cannot shrink it.
	ctx.Ratios[1] = "16:9"
	got := reserve(t, db, q, "task_adaptive")
	if got.Snapshot.EstimatedTokens != 800000 || !got.Snapshot.HoldUSD.Equal(d("6.4")) {
		t.Fatal("automatic/adaptive domain was lost while reserving")
	}
	checkBalance(t, db, "100", "6.4")
}

func TestMinimumReservationRejectsChangedEvidenceOrQuote(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*gorm.DB, *QuoteResult) error
		want   error
	}{
		{"missing source", func(db *gorm.DB, q *QuoteResult) error {
			return db.Where("id = ?", q.Metering.MinimumRuleSetID).Delete(&MinimumRuleSet{}).Error
		}, ErrPricingUnavailable},
		{"unverified source", func(db *gorm.DB, q *QuoteResult) error {
			return db.Model(&MinimumRuleSet{}).Where("id = ?", q.Metering.MinimumRuleSetID).Update("verified_by", 0).Error
		}, ErrPricingUnavailable},
		{"untrusted source URL", func(db *gorm.DB, q *QuoteResult) error {
			return db.Model(&MinimumRuleSet{}).Where("id = ?", q.Metering.MinimumRuleSetID).Update("source_url", "https://example.invalid/forged").Error
		}, ErrPricingUnavailable},
		{"source hash changed", func(db *gorm.DB, q *QuoteResult) error {
			return db.Model(&MinimumRuleSet{}).Where("id = ?", q.Metering.MinimumRuleSetID).Update("evidence_hash", strings.Repeat("b", 64)).Error
		}, ErrConflict},
		{"missing selected cell", func(db *gorm.DB, q *QuoteResult) error {
			return db.Where("rule_set_id = ? AND ratio = ? AND output_duration_ms = ?", q.Metering.MinimumRuleSetID, "16:9", 4000).Delete(&MinimumRuleCell{}).Error
		}, ErrPricingUnavailable},
		{"selected minimum changed", func(db *gorm.DB, q *QuoteResult) error {
			return db.Model(&MinimumRuleCell{}).Where("rule_set_id = ? AND ratio = ? AND output_duration_ms = ?", q.Metering.MinimumRuleSetID, "16:9", 4000).Update("minimum_tokens", 500001).Error
		}, ErrConflict},
		{"price disabled", func(db *gorm.DB, q *QuoteResult) error {
			return db.Model(&PricingRule{}).Where("rule_id = ?", q.Rule.RuleID).Update("enabled", false).Error
		}, ErrPricingUnavailable},
		{"price version changed", func(db *gorm.DB, q *QuoteResult) error {
			return db.Model(&PricingRule{}).Where("rule_id = ?", q.Rule.RuleID).Update("version", "v2").Error
		}, ErrConflict},
		{"price amount changed", func(db *gorm.DB, q *QuoteResult) error {
			return db.Model(&PricingRule{}).Where("rule_id = ?", q.Rule.RuleID).Update("retail_usd_per_1m", "9").Error
		}, ErrConflict},
		{"minimum version changed", func(db *gorm.DB, q *QuoteResult) error {
			return db.Model(&PricingRule{}).Where("rule_id = ?", q.Rule.RuleID).Update("minimum_rule_version", "other-version").Error
		}, ErrConflict},
		{"unverified plain quote", func(_ *gorm.DB, q *QuoteResult) error {
			var err error
			*q, err = Quote(q.Rule, q.Metering)
			return err
		}, ErrPricingUnavailable},
		{"decoded client quote", func(_ *gorm.DB, q *QuoteResult) error {
			body, err := json.Marshal(q)
			if err != nil {
				return err
			}
			*q = QuoteResult{}
			return json.Unmarshal(body, q)
		}, ErrPricingUnavailable},
		{"forged minimum scalar", func(_ *gorm.DB, q *QuoteResult) error { q.Metering.MinimumTokens = 1; return nil }, ErrConflict},
		{"forged source ID", func(_ *gorm.DB, q *QuoteResult) error { q.Metering.MinimumRuleSetID++; return nil }, ErrConflict},
		{"forged source hash", func(_ *gorm.DB, q *QuoteResult) error {
			q.Metering.MinimumEvidenceHash = strings.Repeat("b", 64)
			return nil
		}, ErrConflict},
		{"forged amount", func(_ *gorm.DB, q *QuoteResult) error { q.HoldUSD = d("0.01"); return nil }, ErrConflict},
		{"changed ratio", func(_ *gorm.DB, q *QuoteResult) error { q.Metering.Ratio = "1:1"; return nil }, ErrConflict},
		{"changed resolution", func(_ *gorm.DB, q *QuoteResult) error { q.Metering.Resolution = "480p"; return nil }, ErrConflict},
		{"changed duration", func(_ *gorm.DB, q *QuoteResult) error { q.Metering.OutputDurationMS = 5000; return nil }, ErrConflict},
		{"changed frame rate", func(_ *gorm.DB, q *QuoteResult) error { q.Metering.FPSNumerator = 30; return nil }, ErrConflict},
		{"changed generation mode", func(_ *gorm.DB, q *QuoteResult) error { q.Metering.Mode = "edit"; return nil }, ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, r, m, _ := minimumReservationFixture(t)
			q := minimumQuote(t, db, r, m, MinimumContext{Resolution: "720p", Ratio: "16:9"})
			if err := tc.change(db, &q); err != nil {
				t.Fatal(err)
			}
			assertMinimumReserveRejected(t, db, q, tc.want)
		})
	}
}

func TestMinimumReservationRequiresEveryAdaptiveAutomaticCell(t *testing.T) {
	db, r, m, set := minimumReservationFixture(t)
	m.OutputDurationMS = 5000
	q := minimumQuote(t, db, r, m, MinimumContext{Resolution: "720p", Ratio: "adaptive", Ratios: []string{"16:9", "1:1", "adaptive"}, AutomaticDuration: true, MinDuration: 4, MaxDuration: 5})
	// Remove a non-maximum cell: retaining the previous maximum is insufficient.
	if err := db.Where("rule_set_id = ? AND ratio = ? AND output_duration_ms = ?", set.ID, "16:9", 5000).Delete(&MinimumRuleCell{}).Error; err != nil {
		t.Fatal(err)
	}
	assertMinimumReserveRejected(t, db, q, ErrPricingUnavailable)
}

func TestMinimumReservationChecksEvidenceWhenGeometryDominates(t *testing.T) {
	db, r, m, set := minimumReservationFixture(t)
	m.InputDurationMS = 24000
	q := minimumQuote(t, db, r, m, MinimumContext{Resolution: "720p", Ratio: "16:9"})
	if q.EstimatedTokens <= q.Metering.MinimumTokens {
		t.Fatal("fixture must have geometry greater than minimum")
	}
	// The monetary quote would remain identical, but stale provenance must fail.
	if err := db.Model(&MinimumRuleCell{}).Where("rule_set_id = ? AND ratio = ? AND output_duration_ms = ?", set.ID, "16:9", 4000).Update("minimum_tokens", 499999).Error; err != nil {
		t.Fatal(err)
	}
	assertMinimumReserveRejected(t, db, q, ErrConflict)
}

func TestMinimumAutomaticDurationRequiresVerifiedUpperBound(t *testing.T) {
	db, r, m, _ := minimumReservationFixture(t)
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := QuoteWithMinimumTx(tx, r, m, MinimumContext{Resolution: "720p", Ratio: "16:9", AutomaticDuration: true, MinDuration: 4, MaxDuration: 5})
		return err
	})
	if !errors.Is(err, ErrPricingUnavailable) {
		t.Fatalf("automatic duration accepted a geometric bound below its domain: %v", err)
	}
	checkBalance(t, db, "100", "0")
}

func TestNonInputReservationDoesNotRequireMinimumEvidence(t *testing.T) {
	// setup intentionally has no minimum tables. Text-only admission remains
	// independent of the optional input-video minimum evidence subsystem.
	db, _ := setup(t, "100")
	rule := ruleFixture()
	rule.MinimumTokens = 1000000 // Legacy publication may contain this scalar.
	if err := db.Model(&PricingRule{}).Where("rule_id = ?", rule.RuleID).Update("minimum_tokens", rule.MinimumTokens).Error; err != nil {
		t.Fatal(err)
	}
	q := minimumQuote(t, db, rule, meterFixture(), MinimumContext{Resolution: "720p", Ratio: "16:9"})
	if q.minimumSelection != nil || q.Metering.MinimumTokens != 0 || q.Metering.MinimumRuleSetID != 0 || q.Metering.MinimumEvidenceHash != "" {
		t.Fatal("text-only quotation acquired input-video minimum evidence")
	}
	if q.EstimatedTokens != 108000 {
		t.Fatal("text-only quotation used the input-video scalar minimum")
	}
	reserve(t, db, q, "text_only")
	checkBalance(t, db, "100", "0.864")
}

func assertMinimumReserveRejected(t *testing.T, db *gorm.DB, q QuoteResult, want error) {
	t.Helper()
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: "rejected", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
		return err
	})
	if !errors.Is(err, want) {
		t.Fatalf("reservation error %v, want %v", err, want)
	}
	checkBalance(t, db, "100", "0")
	if !keyUsed(t, db).IsZero() || count(t, db, "video_billing_snapshots") != 0 || count(t, db, "video_quota_reservations") != 0 || count(t, db, "video_quota_buckets") != 0 || count(t, db, "ledger_holds") != 0 || count(t, db, "ledger_journal") != 1 {
		t.Fatal("failed minimum verification persisted financial effects")
	}
}
