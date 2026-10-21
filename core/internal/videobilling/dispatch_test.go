package videobilling

import (
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestDispatchBudgetChecksExistingReservationsWithoutRepricing(t *testing.T) {
	db, q := setup(t, "100")
	policy(t, db, QuotaPolicy{Scope: ScopeProvider, OwnerID: q.Rule.ProviderScopeID, Metric: MetricCNY, Period: PeriodLifetime, LimitAmount: amount("20")})
	reserve(t, db, q, "task_dispatch")
	check := func() error {
		return db.Transaction(func(tx *gorm.DB) error { return ValidateDispatchBudgetTx(tx, 10, "task_dispatch") })
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDispatchBudgetTx(db, 10, "task_dispatch"); !errors.Is(err, ErrTransactionRequired) {
		t.Fatal("dispatch check allowed outside transaction")
	}
	// Accepted work owns its original reservation; disabling/revising the policy
	// does not move it into a new window or erase the reserved budget.
	if err := db.Model(&QuotaPolicy{}).Where("owner_id = ?", q.Rule.ProviderScopeID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&QuotaBucket{}).Where("org_id = 0").Update("blocked", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := check(); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatal("blocked dispatch allowed", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return ValidateDispatchBudgetTx(tx, 20, "task_dispatch") }); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-organization reservation exposed", err)
	}
	checkBalance(t, db, "100", q.HoldUSD.String())
	if count(t, db, "video_billing_entries") != 0 || count(t, db, "video_quota_reservations") != 1 {
		t.Fatal("read-only dispatch gate moved money or reservations")
	}
}

func TestDispatchBudgetMissingOrCorruptEvidenceIsNotConfirmedBlock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*gorm.DB) error
	}{
		{"missing reservation", func(db *gorm.DB) error {
			return db.Where("task_id = ?", "task_dispatch").Delete(&QuotaReservation{}).Error
		}},
		{"missing bucket", func(db *gorm.DB) error { return db.Where("org_id = 0").Delete(&QuotaBucket{}).Error }},
		{"inactive reservation", func(db *gorm.DB) error {
			return db.Model(&QuotaReservation{}).Where("task_id = ?", "task_dispatch").Update("state", "consumed").Error
		}},
		{"missing supplier binding", func(db *gorm.DB) error {
			return db.Model(&QuotaReservation{}).Where("task_id = ?", "task_dispatch").Update("supplier", false).Error
		}},
		{"wrong provider scope", func(db *gorm.DB) error {
			return db.Model(&QuotaPolicy{}).Where("scope = ?", ScopeProvider).Update("owner_id", 999).Error
		}},
		{"unbounded supplier policy", func(db *gorm.DB) error {
			return db.Model(&QuotaBucket{}).Where("org_id = 0").Update("limit_amount", nil).Error
		}},
		{"reservation exceeds bucket", func(db *gorm.DB) error {
			return db.Model(&QuotaBucket{}).Where("org_id = 0").Update("reserved_amount", 0).Error
		}},
		{"database failure", func(db *gorm.DB) error { return db.Exec("DROP TABLE video_quota_buckets").Error }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, q := setup(t, "100")
			policy(t, db, QuotaPolicy{Scope: ScopeProvider, OwnerID: q.Rule.ProviderScopeID, Metric: MetricCNY, Period: PeriodLifetime, LimitAmount: amount("20")})
			reserve(t, db, q, "task_dispatch")
			if err := tc.corrupt(db); err != nil {
				t.Fatal(err)
			}
			err := db.Transaction(func(tx *gorm.DB) error { return ValidateDispatchBudgetTx(tx, 10, "task_dispatch") })
			if err == nil || errors.Is(err, ErrQuotaExceeded) {
				t.Fatal("invalid evidence granted dispatch or was presented as a confirmed quota block", err)
			}
			checkBalance(t, db, "100", q.HoldUSD.String())
			if count(t, db, "video_billing_entries") != 0 {
				t.Fatal("uncertain gate failure released money")
			}
		})
	}
}
