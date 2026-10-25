package video

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func operationView(op Operation) Operation { op.PollURL = "/v1/video/operations/" + op.ID; return op }

func (s *Service) GetOperation(org uint64, id string) (Operation, error) {
	var op Operation
	err := s.DB.Where("org_id = ? AND id = ? AND kind <> ?", org, id, "create_task").First(&op).Error
	return operationView(op), err
}

func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled" || status == "expired"
}
func financialFinal(status string) bool {
	return status == "settled" || status == "released" || status == "adjusted"
}

// Cancel and dispatch lock the task in the same order; cancellation before
// dispatch consumes the create operation and releases its hold in this commit.
func (s *Service) Cancel(ctx context.Context, org uint64, id, requestID, key string) (Operation, bool, error) {
	opID, err := randomID("op_")
	if err != nil {
		return Operation{}, false, err
	}
	now := s.Now().UTC()
	var op Operation
	replay := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		idem, repeat, e := claimIdempotencyTx(tx, s.Secret, org, http.MethodPost, "/v1/video/generations/"+id+"/cancel", key, []byte(`{}`), id, opID, now)
		if e != nil {
			return e
		}
		if repeat {
			replay = true
			return tx.Where("org_id = ? AND id = ?", org, idem.OperationID).First(&op).Error
		}
		t, st, e := loadTask(tx, org, id, true)
		if e != nil {
			return e
		}
		// A second idempotency key is another reference to the same unresolved
		// cancellation, never authority to send another DELETE after uncertainty.
		e = locked(tx).Where("org_id = ? AND resource_id = ? AND kind = ? AND status IN ?", org, id, "cancel_task", []string{"pending", "in_flight", "unknown"}).Order("created_at, id").First(&op).Error
		if e == nil {
			replay = true
			return tx.Model(&IdempotencyRecord{}).Where("org_id = ? AND id = ?", org, idem.ID).Update("operation_id", op.ID).Error
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		decision := ark.CancellationDecision(ark.Status(t.Status), t.Status != "accepted", nil)
		if decision.Error != nil {
			return ErrState
		}
		op = Operation{ID: opID, OrgID: org, ResourceID: id, Kind: "cancel_task", Status: decision.OperationStatus, Version: 1, CreatedAt: now, UpdatedAt: now}
		if decision.CancelLocally {
			changed := tx.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ? AND status = ?", org, id, "create_task", "pending").Updates(map[string]any{"status": "rejected", "error_code": "cancelled_before_dispatch", "updated_at": now})
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return ErrState
			}
			evidence, err := randomID("ev_")
			if err != nil {
				return err
			}
			if e = tx.Create(&Attempt{OrgID: org, TaskID: id, RequestID: requestID, OperationID: opID, Phase: "not_dispatched_cancel", EvidenceRef: evidence, ObservedAt: now}).Error; e != nil {
				return e
			}
			if e = s.afterAssetsTerminalTx(tx, org, id); e != nil {
				return e
			}
			if _, e = videobilling.ReleaseConfirmedTx(tx, videobilling.ReleaseInput{OrgID: org, TaskID: id, EvidenceID: evidence, Reason: videobilling.ReleaseNotDispatched, Confirmed: true, Now: now}); e != nil {
				return e
			}
			if e = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", org, id).Updates(map[string]any{"status": "cancelled", "finished_at": now}).Error; e != nil {
				return e
			}
			st.BillingState = "released"
			if e = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, id).Update("billing_state", "released").Error; e != nil {
				return e
			}
			if e = appendEventTx(tx, &t, &st, "task.cancelled", t.Status, "cancelled", requestID, evidence, now); e != nil {
				return e
			}
			t.Status = "cancelled"
			if e = s.afterObservationTx(tx, &t, &st, false, false, now); e != nil {
				return e
			}
		}
		return tx.Create(&op).Error
	})
	return operationView(op), replay, err
}

func (s *Service) Delete(ctx context.Context, org uint64, id, requestID string) (Operation, bool, error) {
	var op Operation
	already := false
	now := s.Now().UTC()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t task.Task
		var st State
		if e := locked(tx).Where("org_id = ? AND task_id = ? AND provider = ?", org, id, Provider).First(&t).Error; e != nil {
			return e
		}
		if e := locked(tx).Where("org_id = ? AND task_id = ?", org, id).First(&st).Error; e != nil {
			return e
		}
		if st.DeletedAt != nil {
			already = true
			return nil
		}
		if st.DeletionOperationID != "" {
			return tx.Where("org_id = ? AND id = ?", org, st.DeletionOperationID).First(&op).Error
		}
		if !terminal(t.Status) || !financialFinal(st.BillingState) {
			return ErrState
		}
		opID, e := randomID("op_")
		if e != nil {
			return e
		}
		op = Operation{ID: opID, OrgID: org, ResourceID: id, Kind: "delete_task", Status: "pending", Version: 1, CreatedAt: now, UpdatedAt: now}
		if t.Status == "cancelled" || t.UpstreamTaskID == "" {
			op.Status = "confirmed"
			st.DeletedAt = &now
		}
		if e = tx.Create(&op).Error; e != nil {
			return e
		}
		if e = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, id).Updates(map[string]any{"deletion_operation_id": opID, "deleted_at": st.DeletedAt, "updated_at": now}).Error; e != nil {
			return e
		}
		if st.DeletedAt != nil {
			if e = s.afterDeletionTx(tx, &t, &st, now); e != nil {
				return e
			}
		}
		return appendEventTx(tx, &t, &st, "task.deletion_requested", t.Status, t.Status, requestID, "", now)
	})
	return operationView(op), already, err
}

// A mutation is sent at most once per operation. A cancel acknowledgement is
// never sufficient proof of cancellation; a subsequent authenticated GET must
// confirm it, or the operation remains unknown for reconciliation.
func (s *Service) processMutation(ctx context.Context, op Operation) error {
	if op.Kind != "cancel_task" && op.Kind != "delete_task" {
		return nil
	}
	var t task.Task
	var st State
	claimed := false
	now := s.Now().UTC()
	attempt, e := randomID("att_")
	if e != nil {
		return e
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		t, st, e = loadTask(tx, op.OrgID, op.ResourceID, true)
		if e != nil {
			return e
		}
		var current Operation
		if e = locked(tx).Where("org_id = ? AND id = ?", op.OrgID, op.ID).First(&current).Error; e != nil {
			return e
		}
		if current.Status != "pending" {
			return nil
		}
		if op.Kind == "cancel_task" {
			if t.Status == "submitting" || t.Status == "submission_unknown" {
				return nil
			}
			if t.Status == "cancelled" {
				return tx.Model(&current).Updates(map[string]any{"status": "confirmed", "updated_at": now}).Error
			}
			if t.Status != "queued" {
				return tx.Model(&current).Updates(map[string]any{"status": "rejected", "error_code": "invalid_state", "updated_at": now}).Error
			}
		} else if !terminal(t.Status) || !financialFinal(st.BillingState) {
			return ErrState
		}
		allowed, e := s.rateTx(tx, st.ProviderScopeID, "delete", now)
		if e != nil || !allowed {
			return e
		}
		op.Version = current.Version + 1
		op.AttemptID = attempt
		if e = tx.Model(&current).Updates(map[string]any{"status": "in_flight", "version": op.Version, "attempt_id": attempt, "lease_until": now.Add(3 * time.Minute), "updated_at": now}).Error; e != nil {
			return e
		}
		if e = tx.Create(&Attempt{OrgID: t.OrgID, TaskID: t.TaskID, AttemptID: attempt, OperationID: op.ID, RequestID: attempt, Phase: "delete_sending", ObservedAt: now}).Error; e != nil {
			return e
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	client, err := s.clientFor(ctx, t, st)
	if err != nil {
		return err
	}
	allowed, err := s.operationMaySend(ctx, op)
	if err != nil || !allowed {
		return err
	}
	response := client.Delete(ctx, t.UpstreamTaskID)
	ref, err := s.persistResponse(ctx, t, op, response, client.Credential)
	if err != nil {
		return err
	}
	op.ReceiptRef = ref
	return s.applyMutationReceipt(ctx, op)
}

func (s *Service) applyMutationReceipt(ctx context.Context, op Operation) error {
	now := s.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t, st, e := loadTask(tx, op.OrgID, op.ResourceID, true)
		if e != nil {
			return e
		}
		var current Operation
		if e = locked(tx).Where("org_id = ? AND id = ?", op.OrgID, op.ID).First(&current).Error; e != nil {
			return e
		}
		if current.ReceiptAppliedAt != nil || current.Status == "confirmed" || current.Status == "rejected" {
			return nil
		}
		status := "unknown"
		code := "upstream_outcome_unknown"
		if current.ReceivedOutcome == string(ark.OutcomeRejected) && current.HTTPStatus != 404 {
			status = "rejected"
			code = "upstream_rejected"
		}
		if current.ReceivedOutcome == string(ark.OutcomeNotSent) {
			status = "rejected"
			code = "request_not_sent"
		}
		if current.Kind == "delete_task" && current.ReceivedOutcome == string(ark.OutcomeAccepted) {
			status = "confirmed"
			code = ""
			if e = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; e != nil {
				return e
			}
			if e = s.afterDeletionTx(tx, &t, &st, now); e != nil {
				return e
			}
		}
		if current.Kind == "cancel_task" && status == "unknown" {
			if !financialFinal(st.BillingState) {
				st.BillingState = "pending_usage"
			}
			if e = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"next_poll_at": now, "sync_state": "needs_review", "billing_state": st.BillingState}).Error; e != nil {
				return e
			}
		}
		if e = tx.Model(&current).Updates(map[string]any{"status": status, "error_code": code, "receipt_applied_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		return appendEventTx(tx, &t, &st, "task.operation_updated", t.Status, t.Status, current.AttemptID, current.ReceiptRef, now)
	})
}

// Operations survive soft deletion; billing and event reads use an explicit
// ownership check that includes tombstones, unlike ordinary task reads.
func (s *Service) owned(org uint64, id string) error {
	var t task.Task
	e := s.DB.Select("id").Where("org_id = ? AND task_id = ? AND provider = ?", org, id, Provider).First(&t).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return e
}
