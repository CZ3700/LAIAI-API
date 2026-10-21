package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
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
		if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).Updates(map[string]any{"status": "in_flight", "version": op.Version + 1, "attempt_id": "att_" + v.ID, "lease_until": f.now.Add(3 * time.Minute)}).Error; err != nil {
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
	f.now = f.now.Add(4 * time.Minute)
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
