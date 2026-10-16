package video

import (
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// LockBillingTaskTx includes tombstones: customer-visible deletion cannot erase
// the financial audit trail or prevent a reviewed correction.
func LockBillingTaskTx(tx *gorm.DB, org uint64, id string) (task.Task, State, error) {
	var t task.Task
	var st State
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return t, st, videobilling.ErrTransactionRequired
	}
	if err := locked(tx).Where("org_id = ? AND task_id = ? AND provider = ?", org, id, Provider).First(&t).Error; err != nil {
		return t, st, err
	}
	err := locked(tx).Where("org_id = ? AND task_id = ?", org, id).First(&st).Error
	return t, st, err
}
func ApplyBillingSettlementTx(tx *gorm.DB, org uint64, id, requestID, evidenceRef string, result videobilling.SettlementResult, now time.Time) error {
	t, st, err := LockBillingTaskTx(tx, org, id)
	if err != nil {
		return err
	}
	if result.Replay {
		return nil
	}
	if financialFinal(st.BillingState) && st.BillingState != result.State {
		return ErrConflict
	}
	if result.Entry == nil {
		return nil
	}
	st.BillingState = result.State
	update := map[string]any{"cost_usd": result.Entry.CapturedUSD}
	if result.Entry.CompletionTokens != nil {
		update["actual_tokens"] = *result.Entry.CompletionTokens
		st.ObservedCompletionTokens = result.Entry.CompletionTokens
	}
	if err = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", org, id).Updates(update).Error; err != nil {
		return err
	}
	if err = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, id).Updates(map[string]any{"billing_state": st.BillingState, "observed_completion_tokens": st.ObservedCompletionTokens, "updated_at": now}).Error; err != nil {
		return err
	}
	return appendEventTx(tx, &t, &st, "billing.settled", t.Status, t.Status, requestID, evidenceRef, now)
}
func ApplyBillingAdjustmentTx(tx *gorm.DB, org uint64, id, requestID, evidenceRef string, result videobilling.AdjustmentResult, now time.Time) error {
	t, st, err := LockBillingTaskTx(tx, org, id)
	if err != nil {
		return err
	}
	if result.Replay {
		return nil
	}
	if result.Application.OrgID != org || result.Application.TaskID != id {
		return ErrForbidden
	}
	st.BillingState = "adjusted"
	update := map[string]any{"cost_usd": result.Application.NetUSD}
	if result.Application.CompletionTokens != nil {
		update["actual_tokens"] = *result.Application.CompletionTokens
		st.ObservedCompletionTokens = result.Application.CompletionTokens
	}
	if err = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", org, id).Updates(update).Error; err != nil {
		return err
	}
	if err = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, id).Updates(map[string]any{"billing_state": "adjusted", "observed_completion_tokens": st.ObservedCompletionTokens, "updated_at": now}).Error; err != nil {
		return err
	}
	return appendEventTx(tx, &t, &st, "billing.adjusted", t.Status, t.Status, requestID, evidenceRef, now)
}
func AppendBillingEventTx(tx *gorm.DB, org uint64, id, eventType, requestID, evidenceRef string, now time.Time) error {
	t, st, err := LockBillingTaskTx(tx, org, id)
	if err != nil {
		return err
	}
	return appendEventTx(tx, &t, &st, eventType, t.Status, t.Status, requestID, evidenceRef, now)
}
