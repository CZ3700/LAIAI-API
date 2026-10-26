package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// InnoDB can abort an entire contending transaction. Reproduce the worker's
// persisted-receipt replay at that boundary; never retry a statement inside a
// rolled-back transaction or disguise duplicate-key/other failures.
func retryMySQLDeadlockedTransaction(db *gorm.DB, run func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = db.Transaction(run)
		var mysqlErr *mysqlDriver.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1213 {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * time.Millisecond)
	}
	return err
}

func TestMySQLSettlementReplayUsesCurrentFinancialFacts(t *testing.T) {
	for _, mode := range []string{"same_usage", "missing_usage", "conflicting_usage"} {
		t.Run(mode, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "current-facts-create")
			waiter := f.s.DB.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			must(t, waiter.Error)
			defer waiter.Rollback()
			// loadLockedSnapshot reads immutable identity before waiting for the
			// key lock. Preserve that repeatable-read view while another worker
			// commits the financial facts that the waiting worker must observe.
			var snapshot videobilling.Snapshot
			must(t, waiter.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&snapshot).Error)
			actual := int64(100)
			evidence := videobilling.UsageEvidence{EvidenceID: "ev_" + v.ID, Source: "task_get", SourceRecordID: "up_" + v.ID, SourceVersion: "current-facts-v1", CompletionTokens: &actual, ObservedAt: f.now}
			if mode == "missing_usage" {
				evidence.CompletionTokens = nil
			}
			input := videobilling.SettlementInput{OrgID: f.key.OrgID, TaskID: v.ID, Now: f.now, Evidence: evidence}
			must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
				_, err := videobilling.SettleUsageTx(tx, input)
				return err
			}))
			if mode == "conflicting_usage" {
				different := actual + 1
				input.Evidence.CompletionTokens = &different
			}
			result, err := videobilling.SettleUsageTx(waiter, input)
			if mode == "conflicting_usage" {
				if !errors.Is(err, videobilling.ErrConflict) {
					t.Fatal("conflicting usage was not compared with committed settlement", err)
				}
				must(t, waiter.Rollback().Error)
			} else {
				must(t, err)
				if mode == "same_usage" && (!result.Replay || result.State != videobilling.Settled) {
					t.Fatal("waiting transaction did not replay the committed settlement", result)
				}
				if mode == "missing_usage" && result.State != videobilling.PendingUsage {
					t.Fatal("missing usage was incorrectly finalized", result)
				}
				must(t, waiter.Commit().Error)
			}
			var entries, facts int64
			must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
			must(t, f.s.DB.Model(&videobilling.UsageEvidence{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&facts).Error)
			expectedEntries := int64(1)
			expectedHold := ledger.HoldCaptured
			if mode == "missing_usage" {
				expectedEntries, expectedHold = 0, ledger.HoldActive
			}
			if facts != 1 || entries != expectedEntries || f.hold(t, v.ID).Status != expectedHold || f.posts.Load() != 0 {
				t.Fatal("replay duplicated or changed immutable financial facts", facts, entries)
			}
		})
	}
}

func TestMySQLQueryReceiptRecoversDeadlockWithoutRepeatingProviderCalls(t *testing.T) {
	f := mysqlFixture(t)
	deliverySuccess(f)
	v := f.create(t, "query-deadlock-create")
	f.dispatch(t, v.ID)
	var failSettlement atomic.Bool
	failSettlement.Store(true)
	name := fmt.Sprintf("test:query-deadlock-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_billing_entries" && failSettlement.Load() {
			tx.AddError(&mysqlDriver.MySQLError{Number: 1213, Message: "injected settlement deadlock"})
		}
	}))
	defer f.s.DB.Callback().Create().Remove(name)
	err := f.s.pollTask(context.Background(), f.key.OrgID, v.ID)
	var mysqlErr *mysqlDriver.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1213 {
		t.Fatal("settlement did not exercise the retryable database failure", err)
	}
	var receipt QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&receipt).Error)
	view, err := f.s.Get(f.key.OrgID, v.ID)
	must(t, err)
	var entries, facts int64
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
	must(t, f.s.DB.Model(&videobilling.UsageEvidence{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&facts).Error)
	if receipt.PayloadRef == "" || receipt.AppliedAt != nil || entries != 0 || facts != 0 || view.Status != "queued" || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("failed settlement lost its receipt, partially charged or incorrectly finalized the task")
	}
	failSettlement.Store(false)
	must(t, f.s.recoverQueryReceipts(context.Background()))
	must(t, f.s.recoverQueryReceipts(context.Background()))
	must(t, f.s.DB.Where("org_id = ? AND attempt_id = ?", f.key.OrgID, receipt.AttemptID).First(&receipt).Error)
	view, err = f.s.Get(f.key.OrgID, v.ID)
	must(t, err)
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
	must(t, f.s.DB.Model(&videobilling.UsageEvidence{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&facts).Error)
	hold := f.hold(t, v.ID)
	if receipt.AppliedAt == nil || entries != 1 || facts != 1 || f.posts.Load() != 1 || f.gets.Load() != 1 || view.Status != "succeeded" || view.BillingState != "settled" || hold.Status != ledger.HoldCaptured || hold.CapturedAmount.String() != "0.0008" {
		t.Fatal("deadlock recovery duplicated provider calls or financial facts", view.Status, view.BillingState, entries, facts)
	}
	if view.ContentURL != testDeliveryURL || view.ArtifactStatus != "ready" {
		t.Fatal("recovery lost the durable delivery capsule")
	}
	if drift, err := ledger.CheckDrift(f.s.DB); err != nil || len(drift) != 0 {
		t.Fatal("deadlock recovery introduced ledger drift", drift, err)
	}
}

// These integration cases use mysqlFixture's in-memory HTTP transport. They
// require only the dedicated migrated ark_test database, never an Ark account.
func TestMySQLDefaultChannelConcurrencyIncludesAcceptedWithoutExtraHold(t *testing.T) {
	f := mysqlFixture(t)
	var cfg ModelConfig
	must(t, f.s.DB.Where("public_model = ?", ark.Model20).First(&cfg).Error)
	must(t, f.s.DB.Model(&relay.Channel{}).Where("id = ?", cfg.ChannelID).Update("max_concurrent_tasks", 0).Error)
	var tasks []TaskView
	for i := 0; i < 3; i++ {
		v := f.create(t, fmt.Sprintf("default-concurrency-%d", i))
		if v.Status != "accepted" || f.hold(t, v.ID).Status != ledger.HoldActive {
			t.Fatal("accepted work did not own a concurrency slot and hold", v)
		}
		tasks = append(tasks, v)
	}
	postedBefore, heldBefore, err := ledger.OrgBalances(f.s.DB, f.key.OrgID, ledger.AccountUserCredit)
	must(t, err)
	var keyBefore identity.APIKey
	must(t, f.s.DB.First(&keyBefore, f.key.ID).Error)
	counts := make(map[string]int64)
	for _, table := range []string{"tasks", "video_task_states", "video_billing_snapshots", "video_quota_reservations", "video_idempotency_records", "video_credential_snapshots", "video_operations"} {
		var n int64
		must(t, f.s.DB.Table(table).Where("org_id = ?", f.key.OrgID).Count(&n).Error)
		counts[table] = n
	}
	_, _, err = f.s.Create(context.Background(), f.key, f.requestID("fourth-rejected"), "default-concurrency-fourth", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "must not be frozen"}}})
	if !errors.Is(err, task.ErrConcurrencyLimit) {
		t.Fatal("fourth accepted task bypassed the default channel limit", err)
	}
	for table, before := range counts {
		var after int64
		must(t, f.s.DB.Table(table).Where("org_id = ?", f.key.OrgID).Count(&after).Error)
		if after != before {
			t.Fatalf("rejected admission changed %s: before=%d after=%d", table, before, after)
		}
	}
	postedAfter, heldAfter, err := ledger.OrgBalances(f.s.DB, f.key.OrgID, ledger.AccountUserCredit)
	must(t, err)
	var keyAfter identity.APIKey
	must(t, f.s.DB.First(&keyAfter, f.key.ID).Error)
	if !postedAfter.Equal(postedBefore) || !heldAfter.Equal(heldBefore) || !keyAfter.UsedAmount.Equal(keyBefore.UsedAmount) || f.posts.Load() != 0 {
		t.Fatal("rejected admission moved money, key quota or contacted the provider")
	}
	// A confirmed local cancellation releases exactly one slot.
	_, _, err = f.s.Cancel(context.Background(), f.key.OrgID, tasks[0].ID, f.requestID("free-slot"), "default-concurrency-cancel")
	must(t, err)
	v := f.create(t, "default-concurrency-after-cancel")
	if v.Status != "accepted" || f.posts.Load() != 0 {
		t.Fatal("confirmed local cancellation did not free a slot")
	}
}

func TestMySQLConcurrentCreateClaimsSendOnlyOnePOST(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "worker-race-create")
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").First(&op).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start, entered, release := make(chan struct{}), make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	f.response = func(req *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return reply(200, fmt.Sprintf(`{"id":%q}`, "up_"+v.ID)), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			done <- f.s.processOperation(ctx, op)
		}()
	}
	close(start)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("neither worker reached the local provider transport")
	}
	// The winning worker is still inside its HTTP call. The competing worker
	// must observe the committed claim and finish without another POST.
	select {
	case err := <-done:
		must(t, err)
	case <-ctx.Done():
		t.Fatal("second worker did not respect the in-flight create claim")
	}
	if f.posts.Load() != 1 {
		t.Fatalf("overlapping workers sent %d POSTs", f.posts.Load())
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		must(t, err)
	case <-ctx.Done():
		t.Fatal("claimed operation did not finish")
	}
	view, err := f.s.Get(f.key.OrgID, v.ID)
	must(t, err)
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&op).Error)
	var sends, bindings int64
	must(t, f.s.DB.Model(&Attempt{}).Where("org_id = ? AND task_id = ? AND phase = ?", f.key.OrgID, v.ID, "sending").Count(&sends).Error)
	must(t, f.s.DB.Model(&UpstreamBinding{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&bindings).Error)
	if f.posts.Load() != 1 || sends != 1 || bindings != 1 || op.Status != "confirmed" || view.Status != "queued" || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("concurrent claim duplicated work or corrupted the accepted task", sends, bindings, op.Status, view.Status)
	}
}

func TestMySQLCreateReceiptRecoveryDoesNotRepeatPOST(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "receipt-recovery-create")
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").First(&op).Error)
	var failApply atomic.Bool
	failApply.Store(true)
	injected := errors.New("injected create receipt application failure")
	name := fmt.Sprintf("test:create-receipt-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_upstream_bindings" && failApply.Load() {
			tx.AddError(injected)
		}
	}))
	defer f.s.DB.Callback().Create().Remove(name)
	if err := f.s.processOperation(context.Background(), op); !errors.Is(err, injected) {
		t.Fatal("receipt application failure was not exercised", err)
	}
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&op).Error)
	var before task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&before).Error)
	if f.posts.Load() != 1 || op.Status != "in_flight" || op.ReceiptRef == "" || op.ReceiptAppliedAt != nil || before.UpstreamTaskID != "" || before.Status != "submitting" {
		t.Fatal("response was lost or partially applied before recovery")
	}
	receipt := op.ReceiptRef
	failApply.Store(false)
	must(t, f.s.recoverOperations(context.Background()))
	must(t, f.s.recoverOperations(context.Background()))
	must(t, f.s.processOperation(context.Background(), op))
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&op).Error)
	var after task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&after).Error)
	var bindings, entries int64
	must(t, f.s.DB.Model(&UpstreamBinding{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&bindings).Error)
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
	if f.posts.Load() != 1 || f.gets.Load() != 0 || bindings != 1 || entries != 0 || op.Status != "confirmed" || op.ReceiptAppliedAt == nil || op.ReceiptRef != receipt || after.Status != "queued" || after.UpstreamTaskID == "" || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("receipt recovery resent, repriced or failed to restore the task")
	}
}

func TestMySQLExpiredCreateLeaseWithoutReceiptNeverResends(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "lease-crash-create")
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").First(&op).Error)
	// Simulate a process stopping after the claim transaction committed. There
	// is no persisted proof of whether its network operation reached Ark.
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).Updates(map[string]any{"status": "in_flight", "version": op.Version + 1, "attempt_id": "att_" + v.ID, "lease_until": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 180 SECOND)")}).Error; err != nil {
			return err
		}
		return tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("status", "submitting").Error
	}))
	must(t, f.s.recoverOperations(context.Background()))
	view, err := f.s.Get(f.key.OrgID, v.ID)
	must(t, err)
	if view.Status != "submitting" {
		t.Fatal("unexpired lease was treated as abandoned")
	}
	claimedLease := runtimeWorkerCreateOperation(t, f, v.ID)
	expireFixtureOperationLease(t, f.s.DB, claimedLease)
	must(t, f.s.recoverOperations(context.Background()))
	must(t, f.s.recoverOperations(context.Background()))
	must(t, f.s.processOperation(context.Background(), op))
	view, err = f.s.Get(f.key.OrgID, v.ID)
	must(t, err)
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&op).Error)
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	var queued []Operation
	must(t, f.s.pendingOperationCandidates(context.Background()).Where("o.org_id = ?", f.key.OrgID).Find(&queued).Error)
	var events, entries int64
	must(t, f.s.DB.Model(&Event{}).Where("org_id = ? AND task_id = ? AND type = ?", f.key.OrgID, v.ID, "task.submission_unknown").Count(&events).Error)
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
	if view.Status != "submission_unknown" || view.BillingState != "pending_usage" || st.SyncState != "needs_review" || st.ReviewDueAt == nil || op.Status != "unknown" || op.ReceiptRef != "" || len(queued) != 0 || events != 1 || entries != 0 || f.posts.Load() != 0 || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("expired ambiguous claim was resent, refunded or requeued", view.Status, op.Status, events, entries)
	}
}

// Pause after preparation has read the credential, before the dispatch guard.
// This exercises the real worker path without introducing production test hooks.
func pauseVideoCredentialRead(t *testing.T, f *fixture) (<-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	name := fmt.Sprintf("test:dispatch-pause-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_credential_snapshots" && paused.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}))
	t.Cleanup(func() {
		resume()
		f.s.DB.Callback().Query().Remove(name)
	})
	return entered, resume
}

func TestMySQLOperationDispatchRequiresCurrentClaim(t *testing.T) {
	for _, kind := range []string{"create_task", "cancel_task", "delete_task"} {
		for _, invalidation := range []string{"status", "version", "attempt", "lease_expired", "lease_recovered"} {
			t.Run(kind+"/"+invalidation, func(t *testing.T) {
				f := mysqlFixture(t)
				v := f.create(t, "dispatch-guard-create")
				var op Operation
				if kind != "create_task" {
					f.dispatch(t, v.ID)
				}
				switch kind {
				case "create_task":
					must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, kind).First(&op).Error)
				case "cancel_task":
					var err error
					op, _, err = f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("guard-cancel"), "dispatch-guard-cancel")
					must(t, err)
				case "delete_task":
					must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
					var err error
					op, _, err = f.s.Delete(context.Background(), f.key.OrgID, v.ID, f.requestID("guard-delete"))
					must(t, err)
				}
				holdBefore := f.hold(t, v.ID)
				postsBefore := f.posts.Load()
				var deletes atomic.Int32
				f.response = func(req *http.Request) (*http.Response, error) {
					if req.Method == http.MethodDelete {
						deletes.Add(1)
					}
					return reply(200, `{}`), nil
				}
				entered, resume := pauseVideoCredentialRead(t, f)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- f.s.processOperation(ctx, op) }()
				select {
				case <-entered:
				case err := <-done:
					t.Fatal("worker returned before the preparation pause", err)
				case <-ctx.Done():
					t.Fatal("worker did not reach the preparation pause")
				}
				var claimed Operation
				must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&claimed).Error)
				if claimed.Status != "in_flight" || claimed.AttemptID == "" {
					t.Fatal("test did not pause a claimed operation")
				}
				updates := map[string]any{}
				switch invalidation {
				case "status":
					updates["status"] = "unknown"
				case "version":
					updates["version"] = claimed.Version + 1
				case "attempt":
					updates["attempt_id"] = "att_replaced_" + v.ID
				case "lease_expired", "lease_recovered":
					// Expire only the captured claim using database time.
					expireFixtureOperationLease(t, f.s.DB, claimed)
				}
				if len(updates) > 0 {
					must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).Updates(updates).Error)
				}
				if invalidation == "lease_recovered" {
					must(t, f.s.recoverOperations(context.Background()))
				}
				resume()
				select {
				case err := <-done:
					must(t, err)
				case <-ctx.Done():
					t.Fatal("resumed worker did not return")
				}
				var after Operation
				must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&after).Error)
				holdAfter := f.hold(t, v.ID)
				if f.posts.Load() != postsBefore || deletes.Load() != 0 || after.ReceiptRef != "" || after.ReceiptAppliedAt != nil || after.Status == "rejected" || after.Status == "confirmed" || holdAfter.Status != holdBefore.Status || !holdAfter.CapturedAmount.Equal(holdBefore.CapturedAmount) {
					t.Fatal("invalidated worker sent a request, fabricated a receipt or changed its hold", after.Status)
				}
				if invalidation == "lease_expired" || invalidation == "lease_recovered" {
					must(t, f.s.recoverOperations(context.Background()))
					must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&after).Error)
					must(t, f.s.processOperation(context.Background(), after))
					if after.Status != "unknown" || f.posts.Load() != postsBefore || deletes.Load() != 0 || f.hold(t, v.ID).Status != holdBefore.Status {
						t.Fatal("expired permission was retried or refunded")
					}
				}
			})
		}
	}
}

func TestMySQLLateCreateReceiptSurvivesExpiredLeaseRecovery(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "late-create-receipt")
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").First(&op).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once, enteredOnce sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	defer resume()
	f.response = func(req *http.Request) (*http.Response, error) {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			return reply(200, fmt.Sprintf(`{"id":%q}`, "up_"+v.ID)), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	done := make(chan error, 1)
	go func() { done <- f.s.processOperation(ctx, op) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker did not enter its single provider request")
	}
	claimedLease := runtimeWorkerCreateOperation(t, f, v.ID)
	expireFixtureOperationLease(t, f.s.DB, claimedLease)
	must(t, f.s.recoverOperations(context.Background()))
	var recovered Operation
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&recovered).Error)
	if recovered.Status != "unknown" || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("expired HTTP request did not retain the uncertain hold")
	}
	resume()
	select {
	case err := <-done:
		must(t, err)
	case <-ctx.Done():
		t.Fatal("late provider response did not finish")
	}
	must(t, f.s.recoverOperations(context.Background()))
	must(t, f.s.processOperation(context.Background(), recovered))
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&recovered).Error)
	view, err := f.s.Get(f.key.OrgID, v.ID)
	must(t, err)
	var receipts, bindings int64
	must(t, f.s.DB.Model(&Attempt{}).Where("org_id = ? AND task_id = ? AND phase = ?", f.key.OrgID, v.ID, "response_received").Count(&receipts).Error)
	must(t, f.s.DB.Model(&UpstreamBinding{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&bindings).Error)
	if f.posts.Load() != 1 || receipts != 1 || bindings != 1 || recovered.ReceiptRef == "" || recovered.ReceiptAppliedAt == nil || recovered.Status != "confirmed" || view.Status != "queued" || view.BillingState != "reserved" || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("late receipt was lost, resent or treated as a refund", view.Status, recovered.Status)
	}
}

func TestMySQLOperationDispatchGuardDatabaseFailureNeverSends(t *testing.T) {
	for _, kind := range []string{"create_task", "cancel_task", "delete_task"} {
		t.Run(kind, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "dispatch-db-failure-create")
			var op Operation
			if kind != "create_task" {
				f.dispatch(t, v.ID)
			}
			switch kind {
			case "create_task":
				must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, kind).First(&op).Error)
			case "cancel_task":
				var err error
				op, _, err = f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("db-failure-cancel"), "dispatch-db-failure-cancel")
				must(t, err)
			case "delete_task":
				must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
				var err error
				op, _, err = f.s.Delete(context.Background(), f.key.OrgID, v.ID, f.requestID("db-failure-delete"))
				must(t, err)
			}
			holdBefore := f.hold(t, v.ID)
			postsBefore := f.posts.Load()
			var deletes atomic.Int32
			f.response = func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodDelete {
					deletes.Add(1)
				}
				return reply(200, `{}`), nil
			}
			injected := errors.New("injected dispatch permission database failure")
			var failed atomic.Bool
			name := fmt.Sprintf("test:dispatch-db-failure-%d", f.key.OrgID)
			// Fail the permission read even when its count was populated. A
			// database error must take precedence over a seemingly valid claim.
			must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				// The final guard uses an aliased operation joined to the task
				// contract and snapshot. GORM exposes the alias as Table, so
				// target the actual permission query rather than the old name.
				sql := strings.ReplaceAll(strings.ToLower(tx.Statement.SQL.String()), "`", "")
				if strings.Contains(sql, "count(*)") && strings.Contains(sql, "from video_operations as o") && strings.Contains(sql, "join video_task_states v") && failed.CompareAndSwap(false, true) {
					tx.AddError(injected)
				}
			}))
			t.Cleanup(func() { f.s.DB.Callback().Query().Remove(name) })
			if err := f.s.processOperation(context.Background(), op); !failed.Load() || !errors.Is(err, injected) {
				t.Fatal("dispatch did not propagate its permission read failure", err)
			}
			var after Operation
			must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&after).Error)
			holdAfter := f.hold(t, v.ID)
			if f.posts.Load() != postsBefore || deletes.Load() != 0 || after.Status != "in_flight" || after.ReceiptRef != "" || after.ReceiptAppliedAt != nil || holdAfter.Status != holdBefore.Status || !holdAfter.CapturedAmount.Equal(holdBefore.CapturedAmount) {
				t.Fatal("permission read failure sent a request, fabricated a receipt or changed the hold")
			}
			expireFixtureOperationLease(t, f.s.DB, after)
			must(t, f.s.recoverOperations(context.Background()))
			must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&after).Error)
			must(t, f.s.processOperation(context.Background(), after))
			if after.Status != "unknown" || f.posts.Load() != postsBefore || deletes.Load() != 0 || f.hold(t, v.ID).Status != holdBefore.Status {
				t.Fatal("permission read failure became a retry or refund during recovery")
			}
		})
	}
}

func TestMySQLQueryCandidatesExcludeLiveLeasesBeforeBatchLimit(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "query-candidate-after-leases")
	f.dispatch(t, v.ID)
	lease := gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 60 SECOND)")
	for i := 0; i < 40; i++ {
		// A real accepted fixture supplies the supported runtime and its exact
		// billing snapshot. Only the live lease may exclude this earlier row.
		leased := f.create(t, fmt.Sprintf("query-leased-%02d", i))
		must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, leased.ID).Updates(map[string]any{"status": "running", "upstream_task_id": "up_" + leased.ID}).Error)
		must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, leased.ID).Updates(map[string]any{"next_poll_at": gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)"), "query_lease_until": lease}).Error)
		must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, leased.ID, "create_task").Update("status", "confirmed").Error)
	}
	for _, boundary := range []string{"no_lease", "expired_lease"} {
		var expiry any
		if boundary == "expired_lease" {
			expiry = gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)")
		}
		must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("query_lease_until", expiry).Error)
		var rows []State
		must(t, f.s.pendingQueryCandidates(context.Background()).Where("v.org_id = ?", f.key.OrgID).Order("v.next_poll_at, v.task_id").Limit(40).Scan(&rows).Error)
		if len(rows) != 1 || rows[0].TaskID != v.ID {
			t.Fatal("live leases consumed the bounded batch and hid the eligible task", boundary, len(rows))
		}
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	if f.gets.Load() != 1 {
		t.Fatal("eligible candidate was not queried")
	}
}
