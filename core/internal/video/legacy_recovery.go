package video

import (
	"context"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func (s *Service) recoverOperations(ctx context.Context) error {
	_, err := s.recoverOperationsProgress(ctx)
	return err
}

func (s *Service) recoverOperationsProgress(ctx context.Context) (recoveryLaneResult, error) {
	var result recoveryLaneResult
	var failures []error
	var rows []Operation
	if err := s.recoveryOperationCandidates(ctx).Order("o.updated_at, o.id").Limit(30).Find(&rows).Error; err != nil {
		result.Failed++
		return result, safeRecoveryErrors(err)
	}
	result.Selected = int64(len(rows))
	for _, op := range rows {
		if err := ctx.Err(); err != nil {
			return result, safeRecoveryErrors(append(failures, err)...)
		}
		disposition := recoveryUnchanged
		if op.ReceiptRef != "" {
			var err error
			if op.Kind == "create_task" {
				disposition, err = s.applyCreateReceiptProgress(ctx, op)
			} else {
				disposition, err = s.applyMutationReceiptProgress(ctx, op)
			}
			if err != nil {
				// Only the existing proven permanent identity boundary may isolate a
				// create operation. A failed receipt is never permission to fall through
				// to the lease-expiry transition, regardless of operation kind.
				if op.Kind == "create_task" && recoveryPermanentCreateError(err) {
					isolated, isolationErr := s.isolateInvalidCreateExecution(ctx, op, err)
					if isolated {
						disposition = recoveryReview
					}
					err = safeRecoveryErrors(err, isolationErr)
				}
				if disposition == recoveryUnchanged && recoveryOnlyErrors(err, videoruntime.ErrUnsupported) && ctx.Err() == nil {
					err = nil
				}
			}
			result.record(disposition, err)
			failures = append(failures, err)
			continue
		}
		if op.Status != "in_flight" {
			result.Noop++
			continue
		}
		err := s.recoveryDB(ctx).Transaction(func(tx *gorm.DB) error {
			t, st, e := loadTask(tx, op.OrgID, op.ResourceID, true)
			if e != nil {
				return e
			}
			if e = requireCurrentOperationRuntimeTx(tx, st, op); e != nil {
				return e
			}
			if e = requireNoExecutionAttemptTx(tx, op.OrgID, op.ID); e != nil {
				return e
			}
			var current Operation
			if e = locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", op.OrgID, op.ID, op.ResourceID).Take(&current).Error; e != nil {
				return e
			}
			now, e := authorityTimeTx(tx)
			if e != nil {
				return e
			}
			if current.Version != op.Version || current.Status != "in_flight" || current.AttemptID != op.AttemptID || current.LeaseUntil == nil || current.LeaseUntil.After(now) {
				return nil
			}
			result := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND status = ? AND attempt_id = ?", op.OrgID, op.ID, op.Version, "in_flight", op.AttemptID).Updates(map[string]any{"status": "unknown", "error_code": "submission_uncertain", "updated_at": now})
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			previous := t.Status
			if op.Kind == "create_task" && !terminal(t.Status) {
				t.Status = "submission_unknown"
				if e = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Update("status", t.Status).Error; e != nil {
					return e
				}
			}
			st.SyncState = "needs_review"
			if !financialFinal(st.BillingState) {
				st.BillingState = "pending_usage"
			}
			review := now.Add(time.Hour)
			if e = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"sync_state": st.SyncState, "billing_state": st.BillingState, "review_due_at": review, "updated_at": now}).Error; e != nil {
				return e
			}
			if err := appendEventTx(tx, &t, &st, "task.submission_unknown", previous, t.Status, st.CreateRequestID, "", now); err != nil {
				return err
			}
			disposition = recoveryDeferred
			return nil
		})
		if err != nil {
			disposition = recoveryUnchanged
			if recoveryPermanentCreateError(err) {
				isolated, isolationErr := s.isolateInvalidCreateExecution(ctx, op, err)
				if isolated {
					disposition = recoveryReview
				}
				err = safeRecoveryErrors(err, isolationErr)
			}
		}
		result.record(disposition, err)
		failures = append(failures, err)
	}
	return result, safeRecoveryErrors(append(failures, ctx.Err())...)
}
