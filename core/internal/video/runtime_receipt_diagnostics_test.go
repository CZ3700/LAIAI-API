package video

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// A failed applier releases its business locks before recording diagnostics.
// Another applier may commit the receipt in that window. The late diagnostic
// transaction must inspect the committed winner without dirtying its state.
func TestMySQLRuntimeReceiptLateDiagnosticPreservesSuccessfulReplay(t *testing.T) {
	f := mysqlFixture(t)
	deliverySuccess(f)
	v := f.create(t, "runtime-late-diagnostic")
	f.dispatch(t, v.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	injected := errors.New("injected application rollback before late diagnostic")
	var failOnce, awaitingDiagnostic, winnerComplete atomic.Bool
	var successfulReplays, lateWrites atomic.Int32
	failOnce.Store(true)
	var winnerState string
	var winnerReceipt QueryReceipt

	createHook := fmt.Sprintf("test:runtime-diagnostic-rollback-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(createHook, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_billing_entries" && failOnce.CompareAndSwap(true, false) {
			awaitingDiagnostic.Store(true)
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { f.s.DB.Callback().Create().Remove(createHook) })
	queryHook := fmt.Sprintf("test:runtime-diagnostic-winner-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(queryHook, func(tx *gorm.DB) {
		if tx.Statement.Table != "tasks" || !awaitingDiagnostic.CompareAndSwap(true, false) {
			return
		}
		// The first application transaction has rolled back, and the diagnostic
		// has not acquired its first Task lock. Use an independent connection;
		// clear the one-shot flag before recursing through the real applier.
		must(t, f.s.DB.WithContext(ctx).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&winnerReceipt).Error)
		must(t, f.s.applyQueryReceipt(ctx, winnerReceipt))
		must(t, f.s.DB.WithContext(ctx).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, winnerReceipt.AttemptID).Take(&winnerReceipt).Error)
		winnerState = mutationRuntimeSnapshot(t, f, v.ID)
		successfulReplays.Add(1)
		winnerComplete.Store(true)
	}))
	t.Cleanup(func() { f.s.DB.Callback().Query().Remove(queryHook) })
	updateHook := fmt.Sprintf("test:runtime-diagnostic-no-late-write-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Update().Before("gorm:update").Register(updateHook, func(tx *gorm.DB) {
		if winnerComplete.Load() && (tx.Statement.Table == "video_task_states" || tx.Statement.Table == "video_query_receipts") {
			lateWrites.Add(1)
		}
	}))
	t.Cleanup(func() { f.s.DB.Callback().Update().Remove(updateHook) })

	err := f.s.pollTask(ctx, f.key.OrgID, v.ID)
	if !errors.Is(err, injected) || successfulReplays.Load() != 1 || lateWrites.Load() != 0 {
		t.Fatal("did not preserve a successful replay before the late diagnostic lock", err, successfulReplays.Load(), lateWrites.Load())
	}
	if winnerState == "" || mutationRuntimeSnapshot(t, f, v.ID) != winnerState {
		t.Fatal("late diagnostic changed the winner's durable state or timestamp")
	}
	var receipt QueryReceipt
	var state State
	must(t, f.s.DB.Where("org_id = ? AND attempt_id = ?", f.key.OrgID, winnerReceipt.AttemptID).Take(&receipt).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&state).Error)
	if receipt.AppliedAt == nil || winnerReceipt.AppliedAt == nil || !receipt.AppliedAt.Equal(*winnerReceipt.AppliedAt) || receipt.ApplyErrorCode != "" || state.SyncState != "fresh" || state.BillingState != "settled" {
		t.Fatal("late diagnostic overwrote the completed receipt or financial projection")
	}
	var entries, evidence int64
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
	must(t, f.s.DB.Model(&videobilling.UsageEvidence{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&evidence).Error)
	hold := f.hold(t, v.ID)
	if entries != 1 || evidence != 1 || hold.Status != ledger.HoldCaptured || hold.CapturedAmount.String() != "0.0008" || f.posts.Load() != 1 || f.gets.Load() != 1 {
		t.Fatal("concurrent recovery repeated provider calls or financial facts")
	}
}
