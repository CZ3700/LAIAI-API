package video

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func scheduleDatabaseTime(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	var now time.Time
	must(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		now, err = authorityFixtureNow(tx)
		return err
	}))
	return now
}

func scheduleOtherConnection(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.Open(testMutationProducerDSN(t)), &gorm.Config{Logger: logger.Discard})
	must(t, err)
	pool, err := db.DB()
	must(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { must(t, pool.Close()) })
	return db
}

func scheduleWaitForDatabase(t *testing.T, db *gorm.DB, until time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		now := scheduleDatabaseTime(t, db)
		if !now.Before(until) {
			t.Logf("natural fixture expiry: database_until=%s observed_database_time=%s", until.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("database did not cross the bounded fixture expiry")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func makeFixtureTaskQueryDue(t *testing.T, f *fixture, id string) {
	t.Helper()
	var state State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&state).Error)
	makeFixtureQueryDue(t, f.s.DB, state.OrgID, state.TaskID, state.QuerySequence, false)
	makeFixtureProviderRateDue(t, f.s.DB, state.ProviderScopeID, "query")
}

func makeFixtureTaskRateDue(t *testing.T, f *fixture, id, action string) {
	t.Helper()
	var state State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&state).Error)
	makeFixtureProviderRateDue(t, f.s.DB, state.ProviderScopeID, action)
}

func TestMySQLDatabaseAuthoritySharedRateUsesPrimaryTime(t *testing.T) {
	for _, action := range []string{"create", "delete", "query"} {
		t.Run(action, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "clock-shared-rate-"+action)
			var st State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
			funds := authorityFinancialFacts(t, f, v.ID)
			before := scheduleDatabaseTime(t, f.s.DB)
			start := make(chan struct{})
			type result struct {
				allowed bool
				err     error
			}
			results := make(chan result, 2)
			for _, offset := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
				clock := before.Add(offset)
				// No Service containing mutexes is copied. These two rate callers
				// share the real primary database but disagree about host time.
				s := &Service{DB: f.s.DB, Now: func() time.Time { return clock }}
				go func() {
					<-start
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					var allowed bool
					err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
						var err error
						allowed, err = s.rateTx(tx, st.ProviderScopeID, action)
						return err
					})
					results <- result{allowed, err}
				}()
			}
			close(start)
			allowed := 0
			for i := 0; i < 2; i++ {
				r := <-results
				must(t, r.err)
				if r.allowed {
					allowed++
				}
			}
			var rate ProviderRate
			must(t, f.s.DB.Where("scope_id = ? AND action = ?", st.ProviderScopeID, action).First(&rate).Error)
			after := scheduleDatabaseTime(t, f.s.DB)
			if allowed != 1 || rate.NextAllowedAt.Before(before.Add(time.Second)) || rate.NextAllowedAt.After(after.Add(time.Second)) {
				t.Fatalf("shared rate did not use one primary-time permit: allowed=%d before=%s next=%s after=%s", allowed, before, rate.NextAllowedAt, after)
			}
			if authorityFinancialFacts(t, f, v.ID) != funds || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("rate arbitration changed money or contacted provider")
			}
			if action == "query" {
				// One real short fixture expiry proves passage of DB time. The
				// production rate above remains exactly one second.
				r := f.s.DB.Model(&ProviderRate{}).Where("scope_id = ? AND action = ?", st.ProviderScopeID, action).Update("next_allowed_at", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 250000 MICROSECOND)"))
				must(t, r.Error)
				if r.RowsAffected != 1 {
					t.Fatal("rate fixture identity did not match exactly one row")
				}
				must(t, f.s.DB.Where("scope_id = ? AND action = ?", st.ProviderScopeID, action).First(&rate).Error)
				if !rate.NextAllowedAt.After(scheduleDatabaseTime(t, f.s.DB)) {
					t.Fatal("short rate fixture already expired before observation")
				}
				scheduleWaitForDatabase(t, f.s.DB, rate.NextAllowedAt)
				must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
					ok, err := f.s.rateTx(tx, st.ProviderScopeID, action)
					if err == nil && !ok {
						return errors.New("naturally expired shared rate still denied")
					}
					return err
				}))
			}
		})
	}
	t.Run("delivery_shares_query_gate", func(t *testing.T) {
		m := newMutationConsumerFixture(t, "succeeded")
		row, st := m.rows(t)
		funds := authorityFinancialFacts(t, m.fixture, m.taskID)
		must(t, m.s.DB.Where("scope_id = ? AND action = ?", st.ProviderScopeID, "query").Delete(&ProviderRate{}).Error)
		must(t, m.s.DB.Transaction(func(tx *gorm.DB) error {
			ok, err := m.s.rateTx(tx, st.ProviderScopeID, "query")
			if err == nil && !ok {
				return errors.New("fresh query gate was denied")
			}
			return err
		}))
		// Both clocks remain within the 30-day delivery retention window.
		m.s.Now = func() time.Time { return m.now.Add(24 * time.Hour) }
		if err := m.s.refreshDelivery(context.Background(), row, st); !errors.Is(err, videoartifact.ErrSourceDeferred) {
			t.Fatalf("delivery bypassed the shared primary-time query gate: %v", err)
		}
		if m.httpCalls.Load() != 0 || authorityFinancialFacts(t, m.fixture, m.taskID) != funds {
			t.Fatal("deferred delivery made a GET or changed funds")
		}
	})
}

func TestMySQLDatabaseAuthorityRecoveryRechecksLockedLease(t *testing.T) {
	t.Run("legacy_candidate_lease_changes_before_lock", func(t *testing.T) {
		f := mysqlFixture(t)
		v := f.create(t, "clock-recovery-recheck")
		op := runtimeWorkerCreateOperation(t, f, v.ID)
		// This is a controlled legacy recovery fixture, not a claimed lease
		// renewal feature. Actual producer claim timing is covered separately.
		must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND status = ?", op.OrgID, op.ID, op.Version, "pending").Updates(map[string]any{"status": "in_flight", "attempt_id": "att_clock_" + op.ID, "lease_until": gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)")}).Error)
		must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("status", "submitting").Error)
		op = runtimeWorkerCreateOperation(t, f, v.ID)
		funds := authorityFinancialFacts(t, f, v.ID)
		other := scheduleOtherConnection(t)
		var hit atomic.Int32
		name := "clock:recovery-candidate-lease-changed"
		// Other tests retain their historical operations in the shared schema.
		// Scope only the candidate's organization; the production lease/runtime
		// predicate, actual row selection, and locked recovery all still execute.
		must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name+"-scope", func(q *gorm.DB) {
			if q.Statement.Table == "o" {
				q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Table: "o", Name: "org_id"}, Value: op.OrgID}}})
			}
		}))
		t.Cleanup(func() { f.s.DB.Callback().Query().Remove(name + "-scope") })
		must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
			if q.Statement.Table != "o" || !strings.Contains(q.Statement.SQL.String(), "o.lease_until <= UTC_TIMESTAMP(3)") {
				return
			}
			selected, ok := q.Statement.Dest.(*[]Operation)
			if !ok {
				return
			}
			found := false
			for _, candidate := range *selected {
				if candidate.OrgID == op.OrgID && candidate.ID == op.ID && candidate.Version == op.Version && candidate.AttemptID == op.AttemptID {
					found = true
				}
			}
			if !found || !hit.CompareAndSwap(0, 1) {
				return
			}
			r := other.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND attempt_id = ? AND status = ?", op.OrgID, op.ID, op.Version, op.AttemptID, "in_flight").Update("lease_until", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 5 MINUTE)"))
			if r.Error != nil {
				q.AddError(r.Error)
			} else if r.RowsAffected != 1 {
				q.AddError(errors.New("concurrent exact lease fixture missed its row"))
			}
		}))
		t.Cleanup(func() { f.s.DB.Callback().Query().Remove(name) })
		must(t, f.s.recoverOperations(context.Background()))
		after := runtimeWorkerCreateOperation(t, f, v.ID)
		if hit.Load() != 1 || after.Status != "in_flight" || after.Version != op.Version || after.AttemptID != op.AttemptID || after.LeaseUntil == nil || !after.LeaseUntil.After(scheduleDatabaseTime(t, f.s.DB)) {
			t.Fatal("recovery used the stale candidate lease after locking current operation")
		}
		if authorityFinancialFacts(t, f, v.ID) != funds || f.posts.Load() != 0 || f.gets.Load() != 0 {
			t.Fatal("lease recheck changed money or made HTTP")
		}
	})
	t.Run("registered_cancel_natural_short_expiry", func(t *testing.T) {
		m := newMutationConsumerFixture(t, "")
		_, claim := m.register(t, "cancel_task")
		funds := authorityFinancialFacts(t, m.fixture, m.taskID)
		const name = "clock:natural-expiry-candidate-scope"
		must(t, m.s.DB.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
			if q.Statement.Table == "ea" {
				q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Table: "ea", Name: "org_id"}, Value: claim.OrgID}}})
			}
		}))
		t.Cleanup(func() { m.s.DB.Callback().Row().Remove(name) })
		r := m.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND attempt_id = ? AND status = ?", claim.OrgID, claim.ID, claim.Version, claim.AttemptID, "in_flight").Update("lease_until", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 2 SECOND)"))
		must(t, r.Error)
		if r.RowsAffected != 1 {
			t.Fatal("short lease fixture did not match exact claim")
		}
		current := m.operation(t, claim.ID)
		if current.LeaseUntil == nil || !current.LeaseUntil.After(scheduleDatabaseTime(t, m.s.DB)) {
			t.Fatal("fixture lease was not observed live")
		}
		m.s.Now = func() time.Time { return m.now.Add(24 * time.Hour) }
		must(t, m.s.recoverExpiredMutationAttempts(context.Background()))
		if m.operation(t, claim.ID).Status != "in_flight" {
			t.Fatal("host clock expired a still-live primary lease")
		}
		scheduleWaitForDatabase(t, m.s.DB, *current.LeaseUntil)
		m.s.Now = func() time.Time { return m.now.Add(-24 * time.Hour) }
		must(t, m.s.recoverExpiredMutationAttempts(context.Background()))
		after := m.operation(t, claim.ID)
		if after.Status != "unknown" || after.ErrorCode != "submission_uncertain" || authorityFinancialFacts(t, m.fixture, m.taskID) != funds || m.httpCalls.Load() != 0 || m.posts.Load() != 0 || m.gets.Load() != 0 {
			t.Fatalf("natural expiry lost unknown/hold barrier or repeated HTTP: %s/%s", after.Status, after.ErrorCode)
		}
	})
}
