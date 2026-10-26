package video

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type authorityCandidateSQL struct {
	SQL  string
	Vars []any
}

// Capture executed statements, not a copied predicate or DryRun oracle. The
// additional exact organization predicate isolates the shared ark_test schema.
func authorityCaptureCandidate(t *testing.T, f *fixture, run func() error) authorityCandidateSQL {
	t.Helper()
	var captured authorityCandidateSQL
	name := fmt.Sprintf("authority-candidate-capture-%d", f.key.OrgID)
	capture := func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") && strings.Contains(q.Statement.SQL.String(), "LIMIT") {
			if captured.SQL != "" {
				t.Error("candidate issued more than one bounded SELECT")
			}
			captured = authorityCandidateSQL{q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...)}
		}
	}
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, capture))
	must(t, f.s.DB.Callback().Row().After("gorm:row").Register(name, capture))
	defer func() { _ = f.s.DB.Callback().Query().Remove(name); _ = f.s.DB.Callback().Row().Remove(name) }()
	must(t, run())
	if captured.SQL == "" {
		t.Fatal("candidate did not execute its bounded primary-time SELECT")
	}
	return captured
}

func authorityExplainCandidate(t *testing.T, f *fixture, captured authorityCandidateSQL) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var plan []map[string]any
	must(t, f.s.DB.WithContext(ctx).Raw("EXPLAIN "+captured.SQL, captured.Vars...).Scan(&plan).Error)
	if len(plan) == 0 {
		t.Fatal("actual candidate SQL has no EXPLAIN result")
	}
	// All variables are the test-only fixture's generated IDs, versions, statuses
	// and fixed limits. There is no body, URL, DSN or credential in this statement.
	raw, err := json.Marshal(struct {
		SQL  string           `json:"sql"`
		Vars []any            `json:"vars"`
		Plan []map[string]any `json:"plan"`
	}{captured.SQL, captured.Vars, plan})
	must(t, err)
	t.Log("candidate_sql_explain=" + string(raw))
}

// The inline expired-attempt query has no public builder. Stop immediately
// after reading the real rows, then return a sentinel before any reducer.
// The exact executed SQL/bindings are retained for EXPLAIN, not reimplemented.
func authorityReadExpiredCandidates(t *testing.T, f *fixture) ([]ExecutionAttempt, authorityCandidateSQL) {
	t.Helper()
	stopped := errors.New("candidate observation complete")
	name := fmt.Sprintf("authority-expired-observe-%d", f.key.OrgID)
	var captured authorityCandidateSQL
	var selected []ExecutionAttempt
	must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name+"-scope", func(q *gorm.DB) {
		if q.Statement.Table == "ea" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Table: "ea", Name: "org_id"}, Value: f.key.OrgID}}})
		}
	}))
	must(t, f.s.DB.Callback().Row().After("gorm:row").Register(name+"-stop", func(q *gorm.DB) {
		if q.Statement.Table != "ea" || !strings.Contains(q.Statement.SQL.String(), "o.lease_until <= UTC_TIMESTAMP(3)") {
			return
		}
		captured = authorityCandidateSQL{q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...)}
		rows, ok := q.Statement.Dest.(*sql.Rows)
		if !ok || rows == nil {
			t.Error("expired candidate did not execute a real row query")
			return
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			q.AddError(err)
			return
		}
		attemptColumn := -1
		for i, column := range columns {
			if column == "attempt_id" {
				attemptColumn = i
			}
		}
		if attemptColumn < 0 {
			_ = rows.Close()
			q.AddError(errors.New("candidate attempt column missing"))
			return
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				q.AddError(err)
				return
			}
			var id string
			switch value := values[attemptColumn].(type) {
			case []byte:
				id = string(value)
			case string:
				id = value
			default:
				_ = rows.Close()
				q.AddError(errors.New("candidate attempt value invalid"))
				return
			}
			selected = append(selected, ExecutionAttempt{AttemptID: id})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			q.AddError(err)
			return
		}
		if err := rows.Close(); err != nil {
			q.AddError(err)
			return
		}
		q.AddError(stopped)
	}))
	err := f.s.recoverExpiredInboxAttempts(context.Background())
	must(t, f.s.DB.Callback().Row().Remove(name+"-scope"))
	must(t, f.s.DB.Callback().Row().Remove(name+"-stop"))
	if !errors.Is(err, stopped) || captured.SQL == "" {
		t.Fatal("inline candidate observation did not stop before reducer", err)
	}
	return selected, captured
}

func authorityCandidateCancel(t *testing.T, f *fixture, id string, i int, register bool) (Operation, ExecutionAttempt) {
	t.Helper()
	op, _, err := f.s.Cancel(context.Background(), f.key.OrgID, id, fmt.Sprintf("candidate-cancel-request-%d", i), fmt.Sprintf("candidate-cancel-key-%d", i))
	must(t, err)
	if op.Status != "pending" {
		t.Fatal("candidate fixture did not create a genuine pending cancel")
	}
	if !register {
		return op, ExecutionAttempt{}
	}
	m := &mutationConsumerFixture{fixture: f, taskID: id}
	row, st := m.rows(t)
	op.AttemptID, err = randomID("att_")
	must(t, err)
	op.Version++
	op.Status = "in_flight"
	now, err := authorityFixtureNow(f.s.DB)
	must(t, err)
	lease := now.Add(3 * time.Minute)
	op.LeaseUntil = &lease
	prepared, err := f.s.prepareMutationInput(context.Background(), row, st, op, "official-test-credential")
	must(t, err)
	ea, err := m.commitClaim(context.Background(), op, prepared)
	must(t, err)
	return m.operation(t, op.ID), ea
}

// Leave a real legacy producer claim after its successful final COUNT, before
// HTTP. The sentinel models a failed send-permission read; it does not forge a
// claim, execution identity or upstream response.
func authorityCandidateLegacyClaim(t *testing.T, f *fixture, id string) Operation {
	t.Helper()
	op := runtimeWorkerCreateOperation(t, f, id)
	_, st, err := loadTask(f.s.DB, f.key.OrgID, id, false)
	must(t, err)
	var rateCount int64
	must(t, f.s.DB.Model(&ProviderRate{}).Where("scope_id = ? AND action = ?", st.ProviderScopeID, "create").Count(&rateCount).Error)
	if rateCount == 1 {
		makeFixtureProviderRateDue(t, f.s.DB, st.ProviderScopeID, "create")
	} else if rateCount != 0 {
		t.Fatal("ambiguous fixture rate identity")
	}
	stop := errors.New("candidate legacy pre-HTTP boundary")
	name := fmt.Sprintf("authority-candidate-legacy-%s", op.ID)
	hit := false
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		sqlText := q.Statement.SQL.String()
		if strings.Contains(strings.ToLower(sqlText), "count(*)") && strings.Contains(sqlText, "o.lease_until > UTC_TIMESTAMP(3)") {
			count, ok := q.Statement.Dest.(*int64)
			if !ok || count == nil || *count != 1 {
				t.Error("legacy fixture did not reach an allowed final guard")
			}
			hit = true
			q.AddError(stop)
		}
	}))
	err = f.s.processOperation(context.Background(), op)
	must(t, f.s.DB.Callback().Query().Remove(name))
	if !hit || !errors.Is(err, stop) || f.posts.Load() != 0 {
		t.Fatal("legacy fixture did not stop an actual claimed producer before HTTP", err)
	}
	claimed := runtimeWorkerCreateOperation(t, f, id)
	if claimed.Status != "in_flight" || claimed.AttemptID == "" || claimed.LeaseUntil == nil || responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", f.key.OrgID, op.ID) != 0 {
		t.Fatal("candidate fixture lost the real legacy-only claim")
	}
	return claimed
}

// Pin only a candidate fixture's active lease far enough into the future
// for slow setup. Identity, status and version are read and checked unchanged.
func authorityCandidateFutureLease(t *testing.T, f *fixture, op Operation) {
	t.Helper()
	authorityFixtureTx(t, f.s.DB, func(tx *gorm.DB) error {
		where := "org_id = ? AND id = ? AND version = ? AND attempt_id = ?"
		args := []any{op.OrgID, op.ID, op.Version, op.AttemptID}
		var before, after Operation
		if err := locked(tx).Where(where, args...).Take(&before).Error; err != nil {
			return err
		}
		if before.Status != "in_flight" || before.LeaseUntil == nil {
			return errors.New("future candidate fixture is not a current claim")
		}
		changed := tx.Model(&Operation{}).Where(where, args...).UpdateColumn("lease_until", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 HOUR)"))
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return errors.New("future candidate fixture did not update one claim")
		}
		if err := tx.Where(where, args...).Take(&after).Error; err != nil {
			return err
		}
		now, err := authorityFixtureNow(tx)
		if err != nil {
			return err
		}
		if after.LeaseUntil == nil || !after.LeaseUntil.After(now) {
			return errors.New("future candidate lease is not future in the primary database")
		}
		comparison := after
		comparison.LeaseUntil = before.LeaseUntil
		if !reflect.DeepEqual(comparison, before) {
			return errors.New("future candidate fixture changed unrelated fields")
		}
		return nil
	})
}

func TestMySQLDatabaseAuthorityCandidatesIgnoreServiceClock(t *testing.T) {
	// Forty-one older blockers exceed every production batch (20/30/40).
	// Only scheduling predicates are exercised under skew; Prepare/Stage/Append
	// are completed first with normal input-retention time.
	for _, family := range []string{"pending_operation", "pending_query", "legacy_recovery", "create_inbox", "mutation_inbox", "expired_registered"} {
		t.Run(family, func(t *testing.T) {
			f := mysqlFixture(t)
			f.s.MutationResponseInboxEnabled = true
			const blockers = 41
			ids := make([]string, 0, blockers+1)
			// Candidate-only synthetic pending rows must not be dispatched by unrelated
			// later global recovery tests. This is exact local fixture cleanup, no money
			// mutation and no reset of other organizations or their protocols.
			t.Cleanup(func() {
				if len(ids) == 0 {
					return
				}
				if err := f.s.DB.Model(&State{}).Where("org_id = ? AND task_id IN ?", f.key.OrgID, ids).UpdateColumn("runtime_version", "authority-candidate-fixture-only").Error; err != nil {
					t.Error(err)
				}
			})
			var expected string
			for i := 0; i <= blockers; i++ {
				id := f.create(t, fmt.Sprintf("clock-candidate-%s-%d", family, i)).ID
				ids = append(ids, id)
				valid := i == blockers
				// The valid row sorts last even if random task IDs are reversed.
				orderTime := gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 2 HOUR)")
				if valid {
					orderTime = gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 HOUR)")
				}
				op := runtimeWorkerCreateOperation(t, f, id)
				switch family {
				case "pending_operation", "pending_query", "mutation_inbox":
					ea, claim := responseInboxRegister(t, f, id)
					receipt := responseInboxAppend(t, f, ea, responseInboxResult(ark.OutcomeAccepted, "up_"+id))
					must(t, f.s.applyClaimedCreateInbox(context.Background(), claim, receipt.ReceiptID))
					if family == "pending_query" {
						updates := map[string]any{"next_poll_at": orderTime, "query_lease_until": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 HOUR)")}
						if valid || i%2 == 0 {
							updates["query_lease_until"] = gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)")
						}
						if !valid && i%2 == 0 {
							updates["runtime_version"] = "future-candidate-runtime"
						}
						must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).UpdateColumns(updates).Error)
						if valid {
							expected = id
						}
						continue
					}
					op, ea = authorityCandidateCancel(t, f, id, i, family == "mutation_inbox")
					if family == "pending_operation" {
						updates := map[string]any{"created_at": orderTime, "error_code": mutationPreparationDeferredCode, "updated_at": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 HOUR)")}
						if valid || i%2 == 0 {
							updates["updated_at"] = gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 31 SECOND)")
						}
						if !valid && i%2 == 0 {
							updates["runtime_version"] = "future-candidate-runtime"
						}
						must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ?", op.OrgID, op.ID, op.Version).UpdateColumns(updates).Error)
						if valid {
							expected = op.ID
						}
						continue
					}
					mutationFixture := &mutationConsumerFixture{fixture: f, taskID: id}
					receipt = mutationFixture.append(t, ea, mutationResponse(ark.OutcomeAccepted, 204))
					must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", f.key.OrgID, receipt.ReceiptID).UpdateColumn("next_attempt_at", orderTime).Error)
					if valid || i%2 == 0 {
						expireFixtureOperationLease(t, f.s.DB, op)
					} else {
						// A slow fixture setup must not consume this synthetic
						// future blocker. This is not production lease renewal.
						authorityCandidateFutureLease(t, f, op)
					}
					if !valid && i%2 == 0 {
						must(t, f.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, ea.AttemptID).UpdateColumn("storage_protocol", "future-candidate-inbox").Error)
						must(t, f.s.DB.Model(&ResponseInbox{}).Where("org_id = ? AND receipt_id = ?", f.key.OrgID, receipt.ReceiptID).UpdateColumn("storage_protocol", "future-candidate-inbox").Error)
					}
					if valid {
						expected = receipt.ReceiptID
					}
				case "legacy_recovery":
					op = authorityCandidateLegacyClaim(t, f, id)
					updates := map[string]any{"updated_at": orderTime, "lease_until": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 HOUR)")}
					if valid || i%2 == 0 {
						expireFixtureOperationLease(t, f.s.DB, op)
						delete(updates, "lease_until")
					}
					if !valid && i%2 == 0 {
						updates["runtime_version"] = "future-candidate-runtime"
					}
					must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND attempt_id = ?", op.OrgID, op.ID, op.Version, op.AttemptID).UpdateColumns(updates).Error)
					if valid {
						expected = op.ID
					}
				case "create_inbox", "expired_registered":
					ea, claim := responseInboxRegister(t, f, id)
					if valid || i%2 == 0 {
						expireFixtureOperationLease(t, f.s.DB, claim)
					} else {
						authorityCandidateFutureLease(t, f, claim)
					}
					if family == "create_inbox" {
						receipt := responseInboxAppend(t, f, ea, responseInboxResult(ark.OutcomeAccepted, "up_"+id))
						must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", f.key.OrgID, receipt.ReceiptID).UpdateColumn("next_attempt_at", orderTime).Error)
						if !valid && i%2 == 0 {
							must(t, f.s.DB.Model(&ResponseInbox{}).Where("org_id = ? AND receipt_id = ?", f.key.OrgID, receipt.ReceiptID).UpdateColumn("storage_protocol", "future-candidate-inbox").Error)
						}
						if valid {
							expected = receipt.ReceiptID
						}
					} else if valid {
						expected = ea.AttemptID
					}
					if !valid && i%2 == 0 {
						must(t, f.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, ea.AttemptID).UpdateColumn("storage_protocol", "future-candidate-inbox").Error)
					}
				}
			}
			before := authorityFinancialFacts(t, f, ids[len(ids)-1])
			original := f.now
			var previous authorityCandidateSQL
			for _, shift := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
				f.now = original.Add(shift)
				var actual []string
				var captured authorityCandidateSQL
				if family == "expired_registered" {
					rows, q := authorityReadExpiredCandidates(t, f)
					captured = q
					for _, row := range rows {
						actual = append(actual, row.AttemptID)
					}
				} else {
					captured = authorityCaptureCandidate(t, f, func() error {
						switch family {
						case "pending_operation":
							var rows []Operation
							queued := f.s.pendingOperationCandidates(context.Background()).Where("o.org_id = ?", f.key.OrgID)
							err := f.s.DB.Table("(?) AS eligible", queued).Where("scope_rank = 1").Order("created_at, id").Limit(20).Find(&rows).Error
							for _, row := range rows {
								actual = append(actual, row.ID)
							}
							return err
						case "pending_query":
							var rows []State
							err := f.s.pendingQueryCandidates(context.Background()).Where("v.org_id = ?", f.key.OrgID).Order("v.next_poll_at, v.task_id").Limit(40).Scan(&rows).Error
							for _, row := range rows {
								actual = append(actual, row.TaskID)
							}
							return err
						case "legacy_recovery":
							var rows []Operation
							err := f.s.recoveryOperationCandidates(context.Background()).Where("o.org_id = ?", f.key.OrgID).Order("o.updated_at, o.id").Limit(30).Find(&rows).Error
							for _, row := range rows {
								actual = append(actual, row.ID)
							}
							return err
						default:
							var rows []ReceiptApplication
							q := f.s.responseInboxCandidates(context.Background())
							if family == "mutation_inbox" {
								q = f.s.mutationResponseInboxCandidates(context.Background())
							}
							err := q.Where("ap.org_id = ?", f.key.OrgID).Order("ap.next_attempt_at, ap.receipt_id").Limit(30).Scan(&rows).Error
							for _, row := range rows {
								actual = append(actual, row.ReceiptID)
							}
							return err
						}
					})
				}
				if !reflect.DeepEqual(actual, []string{expected}) {
					t.Fatalf("%s skew %s: expected the sole supported due row; got %d rows", family, shift, len(actual))
				}
				if previous.SQL != "" && (captured.SQL != previous.SQL || !reflect.DeepEqual(captured.Vars, previous.Vars)) {
					t.Fatal("Service.Now skew changed database scheduling SQL or bindings")
				}
				if previous.SQL == "" {
					authorityExplainCandidate(t, f, captured)
				}
				previous = captured
				if f.posts.Load() != 0 || f.gets.Load() != 0 || authorityFinancialFacts(t, f, ids[len(ids)-1]) != before {
					t.Fatal("candidate observation contacted provider or changed financial facts")
				}
			}
			f.now = original
		})
	}
}
