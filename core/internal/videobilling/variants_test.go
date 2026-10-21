package videobilling

import (
	"errors"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"gorm.io/gorm"
)

func variantMinimumFixture(model string) (MinimumRuleSet, MinimumRuleCell) {
	// Synthetic evidence for financial-state tests; official evidence is tested
	// separately by arkverification and is not asserted by these fixture hashes.
	return MinimumRuleSet{Model: model, Version: "variant-minimum-v1", SourceURL: "https://docs.volcengine.com/fixture-only", EvidenceHash: strings.Repeat("a", 64), ObservedAt: testAt, VerifiedAt: testAt, VerifiedBy: 1},
		MinimumRuleCell{Resolution: "480p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 70308}
}

func TestVariantMinimumRequiresIndependentModelEvidence(t *testing.T) {
	db, _ := setup(t, "100")
	if err := db.Exec(minimumTestSchema).Error; err != nil {
		t.Fatal(err)
	}
	r, m := ruleFixture(), meterFixture()
	r.Resolution, r.InputVideo, r.MinimumRuleVersion = "480p", true, "variant-minimum-v1"
	m.Width, m.Height, m.InputDurationMS, m.OutputDurationMS, m.InputVideo = 864, 496, 2000, 4000, true
	ctx := MinimumContext{Resolution: "480p", Ratio: "16:9"}
	// Identical version labels and cells must not authorize another model.
	for _, model := range []string{ark.Model20, ark.Model20Fast, ark.Model20Mini} {
		if model != ark.Model20 {
			r.Model = model
			if err := db.Transaction(func(tx *gorm.DB) error { _, e := QuoteWithMinimumTx(tx, r, m, ctx); return e }); !errors.Is(err, ErrPricingUnavailable) {
				t.Fatal("another model's minimum evidence was reused", model, err)
			}
		}
		set, cell := variantMinimumFixture(model)
		if err := db.Transaction(func(tx *gorm.DB) error { _, e := PublishMinimumTx(tx, set, []MinimumRuleCell{cell}); return e }); err != nil {
			t.Fatal(err)
		}
		r.Model = model
		q := minimumQuote(t, db, r, m, ctx)
		if q.Metering.MinimumTokens != cell.MinimumTokens || q.Metering.MinimumRuleSetID == 0 || q.Rule.Model != model {
			t.Fatal("own minimum evidence was not selected")
		}
	}
	if count(t, db, "video_minimum_rule_sets") != 3 {
		t.Fatal("model-specific evidence was overwritten")
	}
}

func TestVariantMinimumDomainCannotExpandToStandardOr25(t *testing.T) {
	for _, model := range []string{ark.Model20Fast, ark.Model20Mini} {
		for _, change := range []func(*MinimumRuleCell){
			func(c *MinimumRuleCell) { c.Resolution = "1080p" },
			func(c *MinimumRuleCell) { c.Resolution = "4k" },
			func(c *MinimumRuleCell) { c.OutputDurationMS = 16000 },
			func(c *MinimumRuleCell) { c.FPS = 30 },
		} {
			db, _ := setup(t, "100")
			if err := db.Exec(minimumTestSchema).Error; err != nil {
				t.Fatal(err)
			}
			set, cell := variantMinimumFixture(model)
			change(&cell)
			if err := db.Transaction(func(tx *gorm.DB) error { _, e := PublishMinimumTx(tx, set, []MinimumRuleCell{cell}); return e }); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid variant minimum accepted", err)
			}
			if count(t, db, "video_minimum_rule_sets") != 0 || count(t, db, "video_minimum_rule_cells") != 0 {
				t.Fatal("partial invalid evidence persisted")
			}
		}
	}
}

func TestVariantReservationSettlesActualUsageWithoutMinimumFloor(t *testing.T) {
	for _, tc := range []struct{ model, cny, retail string }{{ark.Model20Fast, "22", "3.08"}, {ark.Model20Mini, "14", "1.96"}} {
		t.Run(tc.model, func(t *testing.T) {
			db, _ := setup(t, "100")
			if err := db.Exec(minimumTestSchema).Error; err != nil {
				t.Fatal(err)
			}
			set, cell := variantMinimumFixture(tc.model)
			r, m := ruleFixture(), meterFixture()
			r.RuleID, r.Model, r.Resolution, r.InputVideo, r.MinimumRuleVersion = tc.model, tc.model, "480p", true, set.Version
			r.SupplierCNYPer1M, r.RetailUSDPer1M, r.RiskMultiplier = d(tc.cny), d(tc.retail), d("4")
			m.InputVideo, m.InputDurationMS, m.OutputDurationMS, m.Width, m.Height = true, 2000, 4000, 864, 496
			if err := db.Transaction(func(tx *gorm.DB) error {
				if _, err := PublishMinimumTx(tx, set, []MinimumRuleCell{cell}); err != nil {
					return err
				}
				return tx.Create(&r).Error
			}); err != nil {
				t.Fatal(err)
			}
			policy(t, db, QuotaPolicy{Scope: ScopeProvider, OwnerID: r.ProviderScopeID, Metric: MetricCNY, LimitAmount: amount("20")})
			q := minimumQuote(t, db, r, m, MinimumContext{Resolution: "480p", Ratio: "16:9"})
			if q.EstimatedTokens != 281232 || q.Metering.MinimumTokens != cell.MinimumTokens {
				t.Fatal("minimum and risk did not bind reserve")
			}
			got := reserve(t, db, q, "task_variant")
			checkBalance(t, db, "100", q.HoldUSD.String())
			if got.Snapshot.Model != tc.model || got.Snapshot.MinimumRuleVersion != set.Version {
				t.Fatal("reservation lost model or minimum identity")
			}
			if pending := settle(t, db, "task_variant", nil, "missing"); pending.State != PendingUsage {
				t.Fatal("missing usage settled")
			}
			checkBalance(t, db, "100", q.HoldUSD.String())
			actual := int64(50000) // Deliberately below the reservation's minimum.
			settled := settle(t, db, "task_variant", &actual, "actual")
			wantUSD, wantCNY := d(tc.retail).Mul(d("0.05")), d(tc.cny).Mul(d("0.05"))
			if settled.Entry == nil || !settled.Entry.CapturedUSD.Equal(wantUSD) || !settled.Entry.SupplierCNY.Equal(wantCNY) || !settled.Entry.ReleasedUSD.Equal(q.HoldUSD.Sub(wantUSD)) {
				t.Fatal("settlement used estimated minimum/risk rather than actual usage")
			}
			checkBalance(t, db, d("100").Sub(wantUSD).String(), "0")
			if replay := settle(t, db, "task_variant", &actual, "actual"); !replay.Replay || count(t, db, "video_billing_entries") != 1 {
				t.Fatal("settlement replay charged again")
			}
			var bucket QuotaBucket
			if err := db.First(&bucket).Error; err != nil {
				t.Fatal(err)
			}
			if !bucket.ReservedAmount.IsZero() || !bucket.ConsumedAmount.Equal(wantCNY) {
				t.Fatal("shared provider budget did not settle actual cost")
			}
		})
	}
}

func TestVariantQuoteRejectsUnsupportedMeteringAndPriceModes(t *testing.T) {
	for _, model := range []string{ark.Model20Fast, ark.Model20Mini} {
		for _, change := range []func(*PricingRule, *MeteringInput){
			func(r *PricingRule, _ *MeteringInput) { r.Resolution = "1080p" },
			func(r *PricingRule, _ *MeteringInput) { r.Mode = "edit" },
			func(r *PricingRule, _ *MeteringInput) { r.ServiceTier = "flex" },
			func(r *PricingRule, _ *MeteringInput) { r.Draft = true },
			func(_ *PricingRule, m *MeteringInput) { m.OutputDurationMS = 16000 },
			func(_ *PricingRule, m *MeteringInput) { m.OutputDurationMS = 4500 },
			func(_ *PricingRule, m *MeteringInput) { m.FPSNumerator = 30 },
			func(_ *PricingRule, m *MeteringInput) { m.OutputDurationMS, m.OutputFrames = 0, 121 },
		} {
			r, m := ruleFixture(), meterFixture()
			r.Model = model
			change(&r, &m)
			if _, err := Quote(r, m); !errors.Is(err, ErrInvalid) {
				t.Fatal("unsupported variant quote accepted", model, err)
			}
		}
	}
}
