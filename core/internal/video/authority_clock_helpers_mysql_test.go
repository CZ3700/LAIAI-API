package video

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Fixture-only changes to a precisely identified row. These helpers create a
// database-expired precondition; they do NOT claim natural passage of time.
// An existing transaction is reused, including callbacks that already own locks.
func authorityFixtureTx(t *testing.T, db *gorm.DB, apply func(*gorm.DB) error) {
	t.Helper()
	if db == nil || db.Config == nil || db.Statement == nil || db.Dialector == nil || db.DryRun || db.Dialector.Name() != "mysql" {
		t.Fatal("authority fixture requires a real MySQL handle")
	}
	run := func(tx *gorm.DB) error {
		var name string
		if err := tx.Raw("SELECT DATABASE()").Scan(&name).Error; err != nil {
			return err
		}
		if name != "ark_test" && !strings.HasPrefix(name, "ark_test_") {
			return fmt.Errorf("authority fixture requires an isolated test database")
		}
		return apply(tx)
	}
	var err error
	if _, inTx := db.Statement.ConnPool.(gorm.TxCommitter); inTx {
		err = run(db)
	} else {
		err = db.Transaction(run)
	}
	if err != nil {
		t.Fatal("precise authority fixture mutation failed", err)
	}
}

func authorityFixtureNow(tx *gorm.DB) (time.Time, error) {
	// Independent SQL oracle: do not use the production clock helper to prove
	// that the fixture value is actually expired in the database.
	var row struct{ DatabaseTime time.Time }
	if err := tx.Raw("SELECT UTC_TIMESTAMP(3) AS database_time").Scan(&row).Error; err != nil {
		return time.Time{}, err
	}
	if row.DatabaseTime.IsZero() {
		return time.Time{}, fmt.Errorf("test database returned no current time")
	}
	return row.DatabaseTime.UTC(), nil
}

func expireFixtureOperationLease(t *testing.T, db *gorm.DB, op Operation) Operation {
	t.Helper()
	if op.OrgID == 0 || op.ID == "" || op.ResourceID == "" || op.Version == 0 || op.AttemptID == "" {
		t.Fatal("operation lease fixture requires exact identity and claim")
	}
	var after Operation
	authorityFixtureTx(t, db, func(tx *gorm.DB) error {
		where := "org_id = ? AND id = ? AND resource_id = ? AND version = ? AND attempt_id = ?"
		args := []any{op.OrgID, op.ID, op.ResourceID, op.Version, op.AttemptID}
		var before Operation
		if err := locked(tx).Where(where, args...).Take(&before).Error; err != nil {
			return err
		}
		if before.LeaseUntil == nil {
			return fmt.Errorf("operation fixture has no existing lease")
		}
		update := tx.Model(&Operation{}).Where(where, args...).UpdateColumn("lease_until", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)"))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("operation fixture did not change exactly one claim")
		}
		if err := tx.Where(where, args...).Take(&after).Error; err != nil {
			return err
		}
		now, err := authorityFixtureNow(tx)
		if err != nil {
			return err
		}
		if after.LeaseUntil == nil || !after.LeaseUntil.Before(now) {
			return fmt.Errorf("operation lease fixture is not database-expired")
		}
		comparison := after
		comparison.LeaseUntil = before.LeaseUntil
		if !reflect.DeepEqual(comparison, before) {
			return fmt.Errorf("operation fixture changed more than its lease")
		}
		return nil
	})
	return after
}

func makeFixtureQueryDue(t *testing.T, db *gorm.DB, org uint64, taskID string, sequence uint64, expireLease bool) State {
	t.Helper()
	if org == 0 || taskID == "" {
		t.Fatal("query fixture requires exact task identity")
	}
	var after State
	authorityFixtureTx(t, db, func(tx *gorm.DB) error {
		where := "org_id = ? AND task_id = ? AND query_sequence = ?"
		args := []any{org, taskID, sequence}
		var before State
		if err := locked(tx).Where(where, args...).Take(&before).Error; err != nil {
			return err
		}
		updates := map[string]any{"next_poll_at": gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)")}
		if expireLease {
			if before.QueryLeaseUntil == nil {
				return fmt.Errorf("query fixture has no existing lease")
			}
			updates["query_lease_until"] = gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)")
		}
		update := tx.Model(&State{}).Where(where, args...).UpdateColumns(updates)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("query fixture did not change exactly one sequence")
		}
		if err := tx.Where(where, args...).Take(&after).Error; err != nil {
			return err
		}
		now, err := authorityFixtureNow(tx)
		if err != nil {
			return err
		}
		if !after.NextPollAt.Before(now) || expireLease && (after.QueryLeaseUntil == nil || !after.QueryLeaseUntil.Before(now)) {
			return fmt.Errorf("query fixture is not database-due")
		}
		comparison := after
		comparison.NextPollAt = before.NextPollAt
		if expireLease {
			comparison.QueryLeaseUntil = before.QueryLeaseUntil
		}
		if !reflect.DeepEqual(comparison, before) {
			return fmt.Errorf("query fixture changed unrelated state")
		}
		return nil
	})
	return after
}

func makeFixtureProviderRateDue(t *testing.T, db *gorm.DB, scope uint64, action string) {
	t.Helper()
	if scope == 0 || action != "create" && action != "delete" && action != "query" {
		t.Fatal("rate fixture requires exact provider scope and action")
	}
	authorityFixtureTx(t, db, func(tx *gorm.DB) error {
		var before, after ProviderRate
		if err := locked(tx).Where("scope_id = ? AND action = ?", scope, action).Take(&before).Error; err != nil {
			return err
		}
		update := tx.Model(&ProviderRate{}).Where("scope_id = ? AND action = ?", scope, action).UpdateColumn("next_allowed_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)"))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("rate fixture did not change exactly one row")
		}
		if err := tx.Where("scope_id = ? AND action = ?", scope, action).Take(&after).Error; err != nil {
			return err
		}
		now, err := authorityFixtureNow(tx)
		if err != nil {
			return err
		}
		if !after.NextAllowedAt.Before(now) || before.ScopeID != after.ScopeID || before.Action != after.Action {
			return fmt.Errorf("rate fixture changed identity or is not database-due")
		}
		return nil
	})
}

func expireFixturePreparationCooldown(t *testing.T, db *gorm.DB, op Operation) Operation {
	t.Helper()
	if op.OrgID == 0 || op.ID == "" || op.Version == 0 || op.Status != "pending" || op.ErrorCode != mutationPreparationDeferredCode {
		t.Fatal("cooldown fixture requires the exact deferred pending operation")
	}
	var after Operation
	authorityFixtureTx(t, db, func(tx *gorm.DB) error {
		where := "org_id = ? AND id = ? AND version = ? AND status = ? AND error_code = ?"
		args := []any{op.OrgID, op.ID, op.Version, "pending", mutationPreparationDeferredCode}
		var before Operation
		if err := locked(tx).Where(where, args...).Take(&before).Error; err != nil {
			return err
		}
		update := tx.Model(&Operation{}).Where(where, args...).UpdateColumn("updated_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 31 SECOND)"))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("cooldown fixture did not change exactly one operation")
		}
		if err := tx.Where(where, args...).Take(&after).Error; err != nil {
			return err
		}
		now, err := authorityFixtureNow(tx)
		if err != nil {
			return err
		}
		if after.UpdatedAt.Add(30 * time.Second).After(now) {
			return fmt.Errorf("cooldown fixture remains active in database time")
		}
		comparison := after
		comparison.UpdatedAt = before.UpdatedAt
		if !reflect.DeepEqual(comparison, before) {
			return fmt.Errorf("cooldown fixture changed unrelated operation state")
		}
		return nil
	})
	return after
}
