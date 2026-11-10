package videobilling

import (
	"errors"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func TestUserBudgetSharedAcrossKeys(t *testing.T) {
	db, quote := setup(t, "10")
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeUser, OwnerID: 42, Metric: MetricUSD, LimitAmount: amount("1")})
	create := func(key uint64, user uint64, id string) error {
		return db.Transaction(func(tx *gorm.DB) error {
			_, err := ReserveTx(tx, ReserveInput{BillingSchemaVersion: videoruntime.BillingV1, OrgID: 10, UserID: user, APIKeyID: key, TaskID: id, RequestHMAC: strings.Repeat("a", 64), Quote: quote, Now: testAt})
			return err
		})
	}
	if err := create(1, 42, "user-budget-1"); err != nil {
		t.Fatal(err)
	}
	if err := create(3, 42, "user-budget-2"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("second key bypassed user budget: %v", err)
	}
	if err := create(3, 0, "org-key-budget"); err != nil {
		t.Fatalf("org-scoped key should not match user 42 policy: %v", err)
	}
}
