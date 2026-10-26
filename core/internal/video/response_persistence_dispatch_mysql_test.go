package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

func responsePersistenceFailAppends(t *testing.T, f *fixture) (*atomic.Int32, func()) {
	t.Helper()
	var calls atomic.Int32
	name := fmt.Sprintf("test:persistence-unavailable-%d", f.key.OrgID)
	must(t, f.s.responseDB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_response_inbox" {
			calls.Add(1)
			tx.AddError(&mysqlDriver.MySQLError{Number: 1213, Message: "synthetic append unavailable"})
		}
	}))
	remove := func() { must(t, f.s.responseDB.Callback().Create().Remove(name)) }
	t.Cleanup(func() { _ = f.s.responseDB.Callback().Create().Remove(name) })
	return &calls, remove
}

// Ensure this fixture is eligible in the actual bounded global queues, even
// when CI deliberately reruns the suite against the same populated database.
// Only this fixture's due times change; no production query filters are added.
func responsePersistencePrioritizeInbox(t *testing.T, f *fixture, r ResponseInbox) {
	t.Helper()
	responseInboxMakeDue(t, f, r)
	var first []ReceiptApplication
	must(t, f.s.responseInboxCandidates(context.Background()).Order("ap.next_attempt_at, ap.receipt_id").Limit(1).Scan(&first).Error)
	if len(first) != 1 {
		t.Fatal("saved response is absent from the compatible recovery queue")
	}
	must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", r.OrgID, r.ReceiptID).Update("next_attempt_at", first[0].NextAttemptAt.Add(-time.Millisecond)).Error)
}

func responsePersistencePrioritizeQuery(t *testing.T, f *fixture, id string) {
	t.Helper()
	var first []State
	must(t, f.s.pendingQueryCandidates(context.Background()).Order("v.next_poll_at, v.task_id").Limit(1).Scan(&first).Error)
	if len(first) != 1 {
		t.Fatal("active fixture is absent from the query queue")
	}
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Update("next_poll_at", first[0].NextPollAt.Add(-time.Millisecond)).Error)
}

func responsePersistencePrioritizeExpiredAttempt(t *testing.T, f *fixture, id string) {
	t.Helper()
	// Match the expired, compatible, response-free candidates, not all leases:
	// unrelated unknown protocols or tombstones must not define this fixture.
	// Build a fresh statement for each scan: reusing a finished GORM chain can
	// retain its built JOIN clauses and append the same aliases a second time.
	query := func() *gorm.DB {
		return supportedInboxRuntime(f.s.DB.WithContext(context.Background()).Table("video_execution_attempts ea").
			Joins("JOIN video_operations o ON o.id = ea.operation_id AND o.org_id = ea.org_id AND o.resource_id = ea.task_id").
			Joins("JOIN video_task_states v ON v.org_id = ea.org_id AND v.task_id = ea.task_id")).
			Where("v.deleted_at IS NULL AND o.attempt_id = ea.attempt_id AND o.status = ? AND o.lease_until <= UTC_TIMESTAMP(3)", "in_flight").
			Where("NOT EXISTS (SELECT 1 FROM video_response_inbox r WHERE r.org_id = ea.org_id AND r.attempt_id = ea.attempt_id)").
			Order("o.lease_until, ea.attempt_id")
	}
	var oldest []struct{ LeaseUntil time.Time }
	must(t, query().Select("o.lease_until").Limit(1).Scan(&oldest).Error)
	if len(oldest) != 1 {
		t.Fatal("fixture has no eligible response-free expired attempt")
	}
	claim := runtimeWorkerCreateOperation(t, f, id)
	updated := f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ? AND resource_id = ? AND version = ? AND attempt_id = ?", claim.OrgID, claim.ID, claim.ResourceID, claim.Version, claim.AttemptID).UpdateColumn("lease_until", oldest[0].LeaseUntil.Add(-time.Millisecond))
	must(t, updated.Error)
	if updated.RowsAffected != 1 {
		t.Fatal("priority fixture did not update its exact current claim")
	}
	var batch []ExecutionAttempt
	must(t, query().Select("ea.*").Limit(30).Scan(&batch).Error)
	if len(batch) == 0 || batch[0].OrgID != f.key.OrgID || batch[0].TaskID != id {
		t.Fatal("fixture's expired attempt did not enter the first recovery batch")
	}
}

func responsePersistencePauseAfterPermissionCount(t *testing.T, f *fixture, cancel context.CancelFunc, workerStopped <-chan struct{}) (<-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	name := fmt.Sprintf("test:persistence-count-pause-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		statement := strings.ReplaceAll(strings.ToLower(tx.Statement.SQL.String()), "`", "")
		if tx.Error != nil || !strings.Contains(statement, "count(*)") || !strings.Contains(statement, "from video_operations as o") || !strings.Contains(statement, "join video_task_states v") || !paused.CompareAndSwap(false, true) {
			return
		}
		count, ok := tx.Statement.Dest.(*int64)
		if !ok || *count != 1 {
			tx.AddError(errors.New("test permission count did not establish a live send claim"))
			return
		}
		// The real SQL has already authorized this claim. A later latch must
		// override that successful count before any adapter HTTP is invoked.
		close(entered)
		<-release
	}))
	t.Cleanup(func() {
		if err := stopPausedVideoWorker(cancel, resume, workerStopped, 5*time.Second, func() error {
			return f.s.DB.Callback().Query().Remove(name)
		}); err != nil {
			t.Error(err)
		}
	})
	return entered, resume
}

func TestMySQLResponsePersistenceExhaustionPausesAllSendingEntrypoints(t *testing.T) {
	f := mysqlFixture(t)
	executionRecoveryScope(t, f, []uint64{f.key.OrgID})
	f.s.ResponseInboxEnabled = true
	create := f.create(t, "persistence-pending-create")
	cancelTask, _, _ := responseInboxAcceptWithoutHTTP(t, f, "persistence-pending-cancel")
	cancelOp, _, err := f.s.Cancel(context.Background(), f.key.OrgID, cancelTask.ID, f.requestID("cancel"), "persistence-cancel")
	must(t, err)
	deleteTask, _, _ := responseInboxAcceptWithoutHTTP(t, f, "persistence-pending-delete")
	tokens := int64(100)
	must(t, f.s.applyQueryReceipt(context.Background(), f.receipt(t, deleteTask.ID, 1, &tokens)))
	deleteOp, _, err := f.s.Delete(context.Background(), f.key.OrgID, deleteTask.ID, f.requestID("delete"))
	must(t, err)
	lost := f.create(t, "persistence-lost-response")
	before := responseInboxFunds(t, f, lost.ID)
	var deletes atomic.Int32
	f.response = func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPost:
			return reply(200, `{"id":"up_`+lost.ID+`"}`), nil
		case http.MethodDelete:
			deletes.Add(1)
			return reply(200, `{}`), nil
		case http.MethodGet:
			return reply(200, `{"id":"`+path.Base(r.URL.Path)+`","status":"running"}`), nil
		default:
			return nil, errors.New("unexpected mock method")
		}
	}
	failures, restore := responsePersistenceFailAppends(t, f)
	err = f.s.processOperation(context.Background(), runtimeWorkerCreateOperation(t, f, lost.ID))
	restore()
	if !errors.Is(err, ErrResponseSaveUnconfirmed) || failures.Load() != 3 || !f.s.DispatchPaused() || f.posts.Load() != 1 {
		t.Fatal("unconfirmed response did not stop after three appends and latch sending pause", err)
	}
	if responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND task_id = ?", f.key.OrgID, lost.ID) != 0 || responseInboxFunds(t, f, lost.ID) != before {
		t.Fatal("exhausted response append left partial evidence or released money")
	}
	for _, op := range []Operation{runtimeWorkerCreateOperation(t, f, create.ID), cancelOp, deleteOp} {
		snapshot := mutationRuntimeSnapshot(t, f, op.ResourceID)
		if op.Kind == "create_task" {
			err = f.s.processOperation(context.Background(), op)
		} else {
			err = f.s.processMutation(context.Background(), op)
		}
		if !errors.Is(err, ErrDispatchPaused) || mutationRuntimeSnapshot(t, f, op.ResourceID) != snapshot {
			t.Fatal("direct sending entry ignored pause or changed its unclaimed task", op.Kind, err)
		}
	}
	// Restoring database writes and disabling new inbox registrations do not
	// clear the process latch. Existing GETs still run without dispatching any
	// of the pending create/cancel/delete operations.
	expireFixtureOperationLease(t, f.s.DB, runtimeWorkerCreateOperation(t, f, lost.ID))
	f.s.ResponseInboxEnabled = false
	responsePersistencePrioritizeExpiredAttempt(t, f, lost.ID)
	responsePersistencePrioritizeQuery(t, f, cancelTask.ID)
	must(t, f.s.ProcessOnce(context.Background()))
	if !f.s.DispatchPaused() || f.posts.Load() != 1 || deletes.Load() != 0 || f.gets.Load() == 0 || responseInboxFunds(t, f, lost.ID) != before {
		t.Fatal("paused ProcessOnce resent a mutation, released funds or blocked existing GETs")
	}
	for _, id := range []string{create.ID, cancelTask.ID, lost.ID} {
		if f.hold(t, id).Status != ledger.HoldActive {
			t.Fatal("paused dispatch released a nonfinal reservation")
		}
	}
	if runtimeWorkerCreateOperation(t, f, lost.ID).Status != "unknown" || responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND task_id = ?", f.key.OrgID, lost.ID) != 1 {
		t.Fatal("expired registered send was not retained as unknown")
	}
}

func TestMySQLResponsePersistencePauseAtFinalSendGuardStopsPreparedCalls(t *testing.T) {
	for _, tc := range []struct{ kind, barrier string }{
		{"create_task", "credential"}, {"cancel_task", "credential"}, {"delete_task", "credential"},
		{"create_task", "permission_count"}, {"cancel_task", "permission_count"}, {"delete_task", "permission_count"},
	} {
		t.Run(tc.kind+"/"+tc.barrier, func(t *testing.T) {
			kind := tc.kind
			f := mysqlFixture(t)
			f.s.ResponseInboxEnabled = true
			v := f.create(t, "persistence-final-send-guard")
			var op Operation
			if kind == "create_task" {
				op = runtimeWorkerCreateOperation(t, f, v.ID)
			} else {
				attempt, claim := responseInboxRegister(t, f, v.ID)
				r := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
				must(t, f.s.applyClaimedCreateInbox(context.Background(), claim, r.ReceiptID))
				var err error
				if kind == "cancel_task" {
					op, _, err = f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("cancel"), "persistence-final-cancel")
				} else {
					tokens := int64(100)
					must(t, f.s.applyQueryReceipt(context.Background(), f.receipt(t, v.ID, 1, &tokens)))
					op, _, err = f.s.Delete(context.Background(), f.key.OrgID, v.ID, f.requestID("delete"))
				}
				must(t, err)
			}
			fault := f.create(t, "persistence-guard-trigger")
			faultAttempt, _ := responseInboxRegister(t, f, fault.ID)
			before := responseInboxFunds(t, f, v.ID)
			var deletes atomic.Int32
			f.response = func(*http.Request) (*http.Response, error) {
				deletes.Add(1)
				return reply(200, `{}`), nil
			}
			var entered <-chan struct{}
			var resume func()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			stopped := make(chan struct{})
			if tc.barrier == "permission_count" {
				entered, resume = responsePersistencePauseAfterPermissionCount(t, f, cancel, stopped)
			} else {
				entered, resume = pauseVideoCredentialRead(t, f, cancel, stopped)
			}
			done := make(chan error, 1)
			go func() {
				defer close(stopped)
				done <- f.s.processOperation(ctx, op)
			}()
			select {
			case <-entered:
			case err := <-done:
				t.Fatal("worker did not reach its pre-send barrier", err)
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not reach its pre-send barrier")
			}
			failures, restore := responsePersistenceFailAppends(t, f)
			_, err := f.s.saveCreateResponse(context.Background(), faultAttempt, responseInboxResult(ark.OutcomeAccepted, "up_"+fault.ID), "official-test-credential")
			restore()
			if !errors.Is(err, ErrResponseSaveUnconfirmed) || failures.Load() != 3 {
				t.Fatal("parallel save failure did not exhaust its bounded retries", err)
			}
			resume()
			select {
			case err := <-done:
				if !errors.Is(err, ErrDispatchPaused) {
					t.Fatal("prepared worker bypassed the final pause guard", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("prepared worker did not finish after barrier release")
			}
			if f.posts.Load() != 0 || deletes.Load() != 0 || responseInboxFunds(t, f, v.ID) != before {
				t.Fatal("late process pause allowed HTTP or altered reserved funds")
			}
		})
	}
}

func TestMySQLResponsePersistencePauseStopsAlreadySelectedOperationBatch(t *testing.T) {
	f := mysqlFixture(t)
	executionRecoveryScope(t, f, []uint64{f.key.OrgID})
	f.s.ResponseInboxEnabled = true
	lost := f.create(t, "persistence-first-selected-create")
	deleteTask, _, _ := responseInboxAcceptWithoutHTTP(t, f, "persistence-second-selected-delete")
	tokens := int64(100)
	must(t, f.s.applyQueryReceipt(context.Background(), f.receipt(t, deleteTask.ID, 1, &tokens)))
	deleteOp, _, err := f.s.Delete(context.Background(), f.key.OrgID, deleteTask.ID, f.requestID("delete"))
	must(t, err)
	// Production admits one candidate per scope/action. These two use create
	// and delete, so both belong to the same selected batch before the failure.
	var first []Operation
	q := f.s.pendingOperationCandidates(context.Background())
	must(t, f.s.DB.Table("(?) AS eligible", q).Where("scope_rank = 1").Order("created_at, id").Limit(1).Find(&first).Error)
	if len(first) != 1 {
		t.Fatal("fixture did not produce dispatchable operations")
	}
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, lost.ID, "create_task").Update("created_at", first[0].CreatedAt.Add(-2*time.Millisecond)).Error)
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, deleteOp.ID).Update("created_at", first[0].CreatedAt.Add(-time.Millisecond)).Error)
	var batch []Operation
	q = f.s.pendingOperationCandidates(context.Background())
	must(t, f.s.DB.Table("(?) AS eligible", q).Where("scope_rank = 1").Order("created_at, id").Limit(20).Find(&batch).Error)
	if len(batch) < 2 || batch[0].ResourceID != lost.ID || batch[1].ID != deleteOp.ID {
		t.Fatal("fixture mutations were not both selected before the persistence failure")
	}
	before := responseInboxFunds(t, f, lost.ID)
	var deletes atomic.Int32
	f.response = func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPost:
			return reply(200, `{"id":"up_`+lost.ID+`"}`), nil
		case http.MethodDelete:
			deletes.Add(1)
			return reply(200, `{}`), nil
		case http.MethodGet:
			return reply(200, `{"id":"`+path.Base(r.URL.Path)+`","status":"running"}`), nil
		default:
			return nil, errors.New("unexpected mock method")
		}
	}
	failures, restore := responsePersistenceFailAppends(t, f)
	must(t, f.s.ProcessOnce(context.Background()))
	restore()
	var current Operation
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, deleteOp.ID).Take(&current).Error)
	if failures.Load() != 3 || !f.s.DispatchPaused() || f.posts.Load() != 1 || deletes.Load() != 0 || current.Status != "pending" || current.Version != deleteOp.Version || responseInboxFunds(t, f, lost.ID) != before {
		t.Fatal("new pause failed to stop the remainder of an already selected batch")
	}
}

func TestMySQLResponsePersistencePauseStillRecoversDurableEvidence(t *testing.T) {
	f := mysqlFixture(t)
	executionRecoveryScope(t, f, []uint64{f.key.OrgID})
	queryTask, _, _ := responseInboxAcceptWithoutHTTP(t, f, "persistence-query-recovery")
	tokens := int64(100)
	query := f.receipt(t, queryTask.ID, 1, &tokens)
	getTask, _, _ := responseInboxAcceptWithoutHTTP(t, f, "persistence-get-continues")
	inboxTask := f.create(t, "persistence-inbox-recovery")
	attempt, _ := responseInboxRegister(t, f, inboxTask.ID)
	inbox := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+inboxTask.ID))
	legacy := f.create(t, "persistence-legacy-recovery")
	legacyError := errors.New("synthetic legacy application failure")
	name := fmt.Sprintf("test:persistence-legacy-apply-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if event, ok := tx.Statement.Dest.(*Event); ok && event.TaskID == legacy.ID && event.ToStatus == "queued" {
			tx.AddError(legacyError)
		}
	}))
	f.response = func(*http.Request) (*http.Response, error) { return reply(200, `{"id":"up_`+legacy.ID+`"}`), nil }
	err := f.s.processOperation(context.Background(), runtimeWorkerCreateOperation(t, f, legacy.ID))
	must(t, f.s.DB.Callback().Create().Remove(name))
	if !errors.Is(err, legacyError) || runtimeWorkerCreateOperation(t, f, legacy.ID).ReceiptRef == "" {
		t.Fatal("legacy fixture did not leave an independently persisted receipt", err)
	}
	f.s.ResponseInboxEnabled = true
	lost := f.create(t, "persistence-recovery-pause")
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			return reply(200, `{"id":"up_`+lost.ID+`"}`), nil
		}
		if r.Method == http.MethodGet {
			return reply(200, `{"id":"`+path.Base(r.URL.Path)+`","status":"running"}`), nil
		}
		return nil, errors.New("unexpected mutation during recovery")
	}
	makeFixtureTaskRateDue(t, f, lost.ID, "create")
	failures, restore := responsePersistenceFailAppends(t, f)
	err = f.s.processOperation(context.Background(), runtimeWorkerCreateOperation(t, f, lost.ID))
	restore()
	if !errors.Is(err, ErrResponseSaveUnconfirmed) || failures.Load() != 3 {
		t.Fatal("recovery fixture did not latch a real failed response save", err)
	}
	expireFixtureOperationLease(t, f.s.DB, runtimeWorkerCreateOperation(t, f, lost.ID))
	expireFixtureOperationLease(t, f.s.DB, runtimeWorkerCreateOperation(t, f, inboxTask.ID))
	f.s.ResponseInboxEnabled = false
	responsePersistencePrioritizeInbox(t, f, inbox)
	responsePersistencePrioritizeQuery(t, f, getTask.ID)
	// Prioritize only this fixture's older legacy and query receipts too; their
	// consumers use the real recovery paths scoped to this fixture Org inside ProcessOnce.
	var old []Operation
	must(t, f.s.recoveryOperationCandidates(context.Background()).Order("o.updated_at, o.id").Limit(1).Find(&old).Error)
	if len(old) != 1 {
		t.Fatal("legacy recovery fixture is ineligible")
	}
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, legacy.ID, "create_task").Update("updated_at", old[0].UpdatedAt.Add(-time.Millisecond)).Error)
	var prior []QueryReceipt
	must(t, f.s.recoveryQueryCandidates(context.Background()).Order("r.recovery_due_at, r.received_at, r.attempt_id").Limit(1).Find(&prior).Error)
	if len(prior) != 1 || prior[0].RecoveryDueAt == nil {
		t.Fatal("query recovery fixture is ineligible")
	}
	var original QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND attempt_id = ?", query.OrgID, query.TaskID, query.AttemptID).Take(&original).Error)
	// Move only this receipt's scheduling time before the actual eligible
	// queue. ReceivedAt remains immutable evidence, never a retry timestamp.
	authorityFixtureTx(t, f.s.DB, func(tx *gorm.DB) error {
		now, err := authorityFixtureNow(tx)
		if err != nil {
			return err
		}
		due := now
		if prior[0].RecoveryDueAt.Before(due) {
			due = *prior[0].RecoveryDueAt
		}
		where := "org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL"
		args := []any{original.OrgID, original.TaskID, original.AttemptID, original.RecoveryVersion}
		update := tx.Model(&QueryReceipt{}).Where(where, args...).UpdateColumn("recovery_due_at", due.Add(-time.Millisecond))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errors.New("query priority fixture did not update exactly one receipt")
		}
		var scheduled QueryReceipt
		if err := tx.Where(where, args...).Take(&scheduled).Error; err != nil {
			return err
		}
		if scheduled.RecoveryDueAt == nil || !scheduled.RecoveryDueAt.Equal(due.Add(-time.Millisecond)) {
			return errors.New("query priority fixture did not persist its precise due time")
		}
		scheduled.RecoveryDueAt = original.RecoveryDueAt
		if !reflect.DeepEqual(scheduled, original) {
			return errors.New("query priority scheduling changed evidence or other progress fields")
		}
		return nil
	})
	var firstQueryBatch []QueryReceipt
	must(t, f.s.recoveryQueryCandidates(context.Background()).Order("r.recovery_due_at, r.received_at, r.attempt_id").Limit(40).Find(&firstQueryBatch).Error)
	if len(firstQueryBatch) == 0 || firstQueryBatch[0].OrgID != query.OrgID || firstQueryBatch[0].TaskID != query.TaskID || firstQueryBatch[0].AttemptID != query.AttemptID {
		t.Fatal("query receipt did not become first in the real bounded recovery queue")
	}
	posts := f.posts.Load()
	must(t, f.s.ProcessOnce(context.Background()))
	if !f.s.DispatchPaused() || responseInboxApp(t, f, inbox).State != "applied" || runtimeWorkerCreateOperation(t, f, legacy.ID).Status != "confirmed" || f.hold(t, queryTask.ID).Status != ledger.HoldCaptured || f.hold(t, queryTask.ID).CapturedAmount.String() != "0.0008" || f.gets.Load() == 0 || f.posts.Load() != posts {
		t.Fatal("process pause blocked durable recovery/GET or retried a mutating request")
	}
	settled := responseInboxFunds(t, f, lost.ID)
	must(t, f.s.ProcessOnce(context.Background()))
	if !f.s.DispatchPaused() || responseInboxFunds(t, f, lost.ID) != settled || f.posts.Load() != posts || f.hold(t, lost.ID).Status != ledger.HoldActive || f.hold(t, inboxTask.ID).Status != ledger.HoldActive || f.hold(t, legacy.ID).Status != ledger.HoldActive {
		t.Fatal("recovery repeated settlement, cleared pause or released unknown funds")
	}
	var getState task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, getTask.ID).Take(&getState).Error)
	if getState.Status != "running" {
		t.Fatal("paused ProcessOnce did not refresh the existing upstream task")
	}
}

func TestMySQLResponsePersistenceApplicationFailureDoesNotPauseSending(t *testing.T) {
	f := mysqlFixture(t)
	executionRecoveryScope(t, f, []uint64{f.key.OrgID})
	f.s.ResponseInboxEnabled = true
	v := f.create(t, "persistence-application-only-failure")
	injected := errors.New("synthetic application transaction failure")
	name := fmt.Sprintf("test:persistence-application-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if event, ok := tx.Statement.Dest.(*Event); ok && event.TaskID == v.ID && event.ToStatus == "queued" {
			tx.AddError(injected)
		}
	}))
	f.response = func(*http.Request) (*http.Response, error) { return reply(200, `{"id":"up_`+v.ID+`"}`), nil }
	err := f.s.processOperation(context.Background(), runtimeWorkerCreateOperation(t, f, v.ID))
	must(t, f.s.DB.Callback().Create().Remove(name))
	r := responsePersistenceOneReceipt(t, f, v.ID)
	if !errors.Is(err, injected) || f.s.DispatchPaused() || responseInboxApp(t, f, r).State != "pending" || f.posts.Load() != 1 {
		t.Fatal("application failure was mistaken for lost response persistence", err)
	}
	expireFixtureOperationLease(t, f.s.DB, runtimeWorkerCreateOperation(t, f, v.ID))
	responsePersistencePrioritizeInbox(t, f, r)
	must(t, f.s.recoverResponseInbox(context.Background()))
	if responseInboxApp(t, f, r).State != "applied" || f.s.DispatchPaused() || f.posts.Load() != 1 {
		t.Fatal("durable application failure did not recover without HTTP or process pause")
	}
}
