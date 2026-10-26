package video

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

// Financial observations include the provider's org_id=0 buckets by exact
// reservation identity, not merely buckets belonging to the customer org.
func authorityFinancialFacts(t *testing.T, f *fixture, taskID string) string {
	t.Helper()
	var reservations []videobilling.QuotaReservation
	must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Order("id").Find(&reservations).Error)
	ids := make([]uint64, 0, len(reservations))
	for _, row := range reservations {
		ids = append(ids, row.BucketID)
	}
	var buckets []videobilling.QuotaBucket
	if len(ids) > 0 {
		must(t, f.s.DB.Where("id IN ?", ids).Order("id").Find(&buckets).Error)
	}
	var used struct{ UsedAmount decimal.Decimal }
	must(t, f.s.DB.Model(&identity.APIKey{}).Select("used_amount").Where("id = ? AND org_id = ?", f.key.ID, f.key.OrgID).Take(&used).Error)
	var operations []ledger.Operation
	var movements []ledger.Movement
	var events []ledger.OperationEvent
	must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Order("id").Find(&operations).Error)
	must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Order("id").Find(&movements).Error)
	operationIDs := make([]uint64, 0, len(operations))
	for _, op := range operations {
		operationIDs = append(operationIDs, op.ID)
	}
	if len(operationIDs) > 0 {
		must(t, f.s.DB.Where("operation_id IN ?", operationIDs).Order("id").Find(&events).Error)
	}
	raw, err := json.Marshal([]any{responseInboxFunds(t, f, taskID), reservations, buckets, used.UsedAmount, operations, movements, events})
	must(t, err)
	return string(raw)
}

func authorityTaskFacts(t *testing.T, f *fixture, taskID string) string {
	t.Helper()
	var row task.Task
	var state State
	var operations []Operation
	var events []Event
	var outbox []Outbox
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, taskID).Take(&row).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, taskID).Take(&state).Error)
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ?", f.key.OrgID, taskID).Order("id").Find(&operations).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, taskID).Order("id").Find(&events).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, taskID).Order("event_id").Find(&outbox).Error)
	raw, err := json.Marshal([]any{row, state, operations, events, outbox})
	must(t, err)
	return string(raw)
}

func TestMySQLDatabaseAuthorityClockContract(t *testing.T) {
	f := mysqlFixture(t)
	if value, err := authorityTimeTx(f.s.DB); !value.IsZero() || !errors.Is(err, ErrUnavailable) {
		t.Fatal("nontransactional primary handle became time authority")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx := f.s.DB.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	must(t, tx.Error)
	defer tx.Rollback()
	var connectionBefore, connectionAfter uint64
	must(t, tx.Raw("SELECT CONNECTION_ID()").Scan(&connectionBefore).Error)
	first, err := authorityTimeTx(tx)
	must(t, err)
	var slept int
	must(t, tx.Raw("SELECT SLEEP(0.01)").Scan(&slept).Error)
	second, err := responseDatabaseTimeTx(tx)
	must(t, err)
	must(t, tx.Raw("SELECT CONNECTION_ID()").Scan(&connectionAfter).Error)
	if first.IsZero() || !second.After(first) || first.Location() != time.UTC || second.Location() != time.UTC || first.Nanosecond()%int(time.Millisecond) != 0 || second.Nanosecond()%int(time.Millisecond) != 0 || connectionBefore == 0 || connectionBefore != connectionAfter {
		t.Fatal("clock did not use independent UTC millisecond statements on the caller transaction")
	}
	var equality struct{ Valid, Expired int }
	must(t, tx.Raw("SELECT (clock.database_time > clock.database_time) AS valid, (clock.database_time <= clock.database_time) AS expired FROM (SELECT UTC_TIMESTAMP(3) AS database_time) clock").Scan(&equality).Error)
	if equality.Valid != 0 || equality.Expired != 1 {
		t.Fatal("equal database lease time was not strictly expired")
	}
	must(t, tx.Commit().Error)
	// Actual driver error, not AddError or a fabricated MySQL result.
	var hit atomic.Int32
	name := fmt.Sprintf("authority-clock-contract-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") {
			hit.Add(1)
			q.Statement.SQL.Reset()
			q.Statement.SQL.WriteString("SELECT b1a_missing_clock_column AS database_time")
			q.Statement.Vars = nil
		}
	}))
	err = f.s.DB.Transaction(func(failed *gorm.DB) error {
		value, err := authorityTimeTx(failed)
		if !value.IsZero() {
			t.Error("driver clock error returned a fallback value")
		}
		return err
	})
	must(t, f.s.DB.Callback().Row().Remove(name))
	if hit.Load() != 1 || !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() || errors.Unwrap(err) == nil {
		t.Fatal("actual SQL clock error was not safely propagated")
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		cancelCtx, stop := context.WithCancel(context.Background())
		if cause == context.DeadlineExceeded {
			stop()
			cancelCtx, stop = context.WithDeadline(context.Background(), time.Unix(1, 0))
		} else {
			stop()
		}
		clean := f.s.DB.Begin()
		must(t, clean.Error)
		value, err := authorityTimeTx(clean.WithContext(cancelCtx))
		stop()
		must(t, clean.Rollback().Error)
		if !value.IsZero() || !errors.Is(err, cause) || !errors.Is(err, ErrUnavailable) {
			t.Fatal("cancelled authority lookup lost its cause or returned time")
		}
	}
	t.Logf("database clock progressed %s on one connection; equality expired", second.Sub(first))
}

func TestMySQLDatabaseAuthorityInboxApplyUsesLockedTime(t *testing.T) {
	for _, action := range []string{"create_task", "cancel_task", "delete_task"} {
		t.Run(action, func(t *testing.T) {
			var f *fixture
			var taskID string
			var attempt ExecutionAttempt
			var claim Operation
			var receipt ResponseInbox
			var apply func(context.Context, *Operation) error
			var appendAgain func() ResponseInbox
			if action == "create_task" {
				f = mysqlFixture(t)
				taskID = f.create(t, "clock-create-apply").ID
				attempt, claim = responseInboxRegister(t, f, taskID)
				result := responseInboxResult(ark.OutcomeAccepted, "up_"+taskID)
				receipt = responseInboxAppend(t, f, attempt, result)
				appendAgain = func() ResponseInbox { return responseInboxAppend(t, f, attempt, result) }
				apply = func(ctx context.Context, sender *Operation) error {
					return f.s.applyCreateInbox(ctx, f.key.OrgID, receipt.ReceiptID, sender)
				}
			} else {
				final := ""
				if action == "delete_task" {
					final = "succeeded"
				}
				m := newMutationConsumerFixture(t, final)
				f, taskID = m.fixture, m.taskID
				attempt, claim = m.register(t, action)
				result := mutationResponse(ark.OutcomeAccepted, 204)
				receipt = m.append(t, attempt, result)
				appendAgain = func() ResponseInbox { return m.append(t, attempt, result) }
				apply = func(ctx context.Context, sender *Operation) error {
					return f.s.applyMutationInbox(ctx, f.key.OrgID, receipt.ReceiptID, sender)
				}
			}
			funds := authorityFinancialFacts(t, f, taskID)
			before := authorityTaskFacts(t, f, taskID)
			originalNow := f.now
			// The input was legitimately prepared before shifting Service.Now.
			// Metadata recovery ignores ciphertext TTL; this is not a host/Purge test.
			f.now = originalNow.Add(24 * time.Hour)
			if err := apply(context.Background(), nil); !errors.Is(err, ErrConflict) || responseInboxApp(t, f, receipt).ErrorCode != "claim_active" {
				t.Fatal("ahead-of-database consumer stole an active claim", err)
			}
			if authorityTaskFacts(t, f, taskID) != before || authorityFinancialFacts(t, f, taskID) != funds {
				t.Fatal("active-claim recovery changed task or financial facts")
			}
			expireFixtureOperationLease(t, f.s.DB, claim)
			before = authorityTaskFacts(t, f, taskID)
			f.now = originalNow.Add(-24 * time.Hour)
			if err := apply(context.Background(), &claim); !errors.Is(err, ErrConflict) || responseInboxApp(t, f, receipt).ErrorCode != "claim_changed" {
				t.Fatal("behind-database sender applied an expired claim", err)
			}
			if authorityTaskFacts(t, f, taskID) != before || authorityFinancialFacts(t, f, taskID) != funds {
				t.Fatal("expired sender changed task or financial facts")
			}
			if late := appendAgain(); late.ReceiptID != receipt.ReceiptID {
				t.Fatal("lease expiry prevented independent duplicate evidence preservation")
			}
			must(t, apply(context.Background(), nil))
			app := responseInboxApp(t, f, receipt)
			if app.State != "applied" || app.AppliedAt == nil {
				t.Fatal("database-expired claim did not become recoverable")
			}
			after := authorityTaskFacts(t, f, taskID)
			must(t, apply(context.Background(), nil))
			if authorityTaskFacts(t, f, taskID) != after || authorityFinancialFacts(t, f, taskID) != funds || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("recovery replay repeated effects, HTTP or financial actions")
			}
			var stored ResponseInbox
			must(t, f.s.DB.Where("org_id = ? AND receipt_id = ?", f.key.OrgID, receipt.ReceiptID).Take(&stored).Error)
			if !reflect.DeepEqual(stored, receipt) {
				t.Fatal("application changed immutable response evidence")
			}
			var op Operation
			must(t, f.s.DB.Where("org_id = ? AND id = ?", claim.OrgID, claim.ID).Take(&op).Error)
			want := "confirmed"
			if action == "cancel_task" {
				want = "unknown" // ACK is not an authoritative cancellation.
			}
			if op.Status != want {
				t.Fatal("clock change altered the existing reducer outcome", op.Status)
			}
		})
	}
}

func TestMySQLDatabaseAuthorityClockFailureHasNoFinancialSideEffect(t *testing.T) {
	for _, phase := range []string{"claim_lock_sql", "final_send_sql", "inbox_apply_cancel"} {
		t.Run(phase, func(t *testing.T) {
			f := mysqlFixture(t)
			f.s.ResponseInboxEnabled = true
			id := f.create(t, "clock-failure").ID
			op := runtimeWorkerCreateOperation(t, f, id)
			var receipt ResponseInbox
			var attempt ExecutionAttempt
			if phase == "inbox_apply_cancel" {
				attempt, op = responseInboxRegister(t, f, id)
				receipt = responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+id))
			}
			funds := authorityFinancialFacts(t, f, id)
			before := authorityTaskFacts(t, f, id)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var hit atomic.Int32
			name := fmt.Sprintf("authority-failure-%d", f.key.OrgID)
			if phase == "final_send_sql" {
				must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
					callbacks.BuildQuerySQL(q)
					sqlText := q.Statement.SQL.String()
					if strings.Contains(strings.ToLower(sqlText), "count(*)") && strings.Contains(sqlText, "o.lease_until > UTC_TIMESTAMP(3)") && hit.CompareAndSwap(0, 1) {
						q.Statement.SQL.Reset()
						q.Statement.SQL.WriteString(strings.ReplaceAll(sqlText, "UTC_TIMESTAMP(3)", "b1a_missing_clock_function()"))
					}
				}))
			} else {
				must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
					if !strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") || !hit.CompareAndSwap(0, 1) {
						return
					}
					if phase == "inbox_apply_cancel" {
						cancel()
					} else {
						q.Statement.SQL.Reset()
						q.Statement.SQL.WriteString("SELECT b1a_missing_clock_column AS database_time")
						q.Statement.Vars = nil
					}
				}))
			}
			var err error
			if phase == "inbox_apply_cancel" {
				err = f.s.applyClaimedCreateInbox(ctx, op, receipt.ReceiptID)
			} else {
				err = f.s.processOperation(ctx, op)
			}
			if phase == "final_send_sql" {
				must(t, f.s.DB.Callback().Query().Remove(name))
			} else {
				must(t, f.s.DB.Callback().Row().Remove(name))
			}
			if hit.Load() != 1 || err == nil || f.posts.Load() != 0 || f.gets.Load() != 0 || authorityFinancialFacts(t, f, id) != funds {
				t.Fatal("clock fault was not reached or caused HTTP/financial effects", phase)
			}
			if phase == "inbox_apply_cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("clock cancellation lost errors.Is cause")
			}
			if phase != "inbox_apply_cancel" && (strings.Contains(err.Error(), "b1a_") || strings.Contains(err.Error(), "SELECT")) {
				t.Fatal("clock failure exposed SQL diagnostics")
			}
			if phase != "final_send_sql" && authorityTaskFacts(t, f, id) != before {
				t.Fatal("failed clock transaction partially changed task/operation state")
			}
			if phase == "claim_lock_sql" {
				if responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND task_id = ?", f.key.OrgID, id) != 0 {
					t.Fatal("failed claim retained an execution registration")
				}
				// Fault gone: the still-pending original request can claim normally.
				must(t, f.s.processOperation(context.Background(), op))
				if f.posts.Load() != 1 {
					t.Fatal("restored database could not dispatch the unclaimed task")
				}
				return
			}
			if phase == "final_send_sql" {
				op = runtimeWorkerCreateOperation(t, f, id)
				must(t, f.s.DB.Where("org_id = ? AND operation_id = ?", f.key.OrgID, op.ID).Take(&attempt).Error)
				if op.Status != "in_flight" || attempt.AttemptID != op.AttemptID || responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND operation_id = ?", f.key.OrgID, op.ID) != 0 {
					t.Fatal("failed final guard fabricated a receipt or lost registration")
				}
				receipt = responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+id))
			} else if responseInboxApp(t, f, receipt).State != "pending" {
				t.Fatal("cancelled application changed durable receipt progress")
			}
			expireFixtureOperationLease(t, f.s.DB, op)
			must(t, f.s.applyCreateInbox(context.Background(), f.key.OrgID, receipt.ReceiptID, nil))
			must(t, f.s.applyCreateInbox(context.Background(), f.key.OrgID, receipt.ReceiptID, nil))
			if responseInboxApp(t, f, receipt).State != "applied" || authorityFinancialFacts(t, f, id) != funds || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("clock recovery resent a registered request or changed funds")
			}
		})
	}
}
