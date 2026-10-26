package video

import (
	"context"
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const executionIdentityReviewCode = "execution_identity_conflict"

// Inspect every error leaf: an identity denial joined to a cancelled/failed
// database read must never become permission to change a durable operation.
func permanentCreateExecutionIdentityError(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		leaves := joined.Unwrap()
		if len(leaves) == 0 {
			return false
		}
		for _, leaf := range leaves {
			if !permanentCreateExecutionIdentityError(leaf) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		// Storage/authority wrappers retain their semantic denial even when
		// their underlying error happens to be a recognized identity sentinel.
		if errors.Is(err, invocation.ErrVideoStorage) || errors.Is(err, invocation.ErrVideoExecutionAuthorityUnavailable) || errors.Is(err, ErrUnavailable) {
			return false
		}
		return permanentCreateExecutionIdentityError(wrapped.Unwrap())
	}
	for _, permanent := range []error{ErrConflict, videoruntime.ErrUnsupported, invocation.ErrVideoSchemaUnsupported, invocation.ErrVideoAccessDenied, invocation.ErrVideoNotFound, invocation.ErrVideoInvalid, invocation.ErrVideoCorrupt, invocation.ErrVideoConflict} {
		if err == permanent {
			return true
		}
	}
	return false
}

// isolateInvalidCreateExecution is a recovery-only diagnostic, not a refund or
// send permission. Direct dispatch/cancel calls retain their zero-write denial.
// false,nil means the exact narrow quarantine authority was not established.
func (s *Service) isolateInvalidCreateExecution(ctx context.Context, candidate Operation, cause error) (bool, error) {
	if ctx == nil {
		return false, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return false, authorityClockFailure(err)
	}
	if !permanentCreateExecutionIdentityError(cause) || candidate.Kind != "create_task" {
		return false, nil
	}
	if s == nil || s.DB == nil || s.DB.Dialector == nil || s.DB.Dialector.Name() != "mysql" || s.DB.DryRun {
		return false, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	isolated := false
	err := s.DB.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		t, st, err := loadTask(tx, candidate.OrgID, candidate.ResourceID, true)
		if err != nil {
			return err
		}
		var current Operation
		if err = locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", candidate.OrgID, candidate.ID, candidate.ResourceID).Take(&current).Error; err != nil {
			return err
		}
		// Validate the supported task and financial contract separately from
		// execution identity. A future contract is opaque to this binary.
		if err = requireTaskRuntimeTx(tx, st); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == videoruntime.ErrUnsupported {
				return nil
			}
			return err
		}
		if requireOperationRuntime(st, current) != nil || requireOperationRuntime(st, candidate) != nil {
			return nil
		}
		if t.Provider != Provider || t.OrgID != st.OrgID || t.TaskID != st.TaskID || current.ID != candidate.ID || current.OrgID != candidate.OrgID || current.ResourceID != candidate.ResourceID || current.Kind != "create_task" || current.Kind != candidate.Kind || current.Version != candidate.Version || current.AttemptID != candidate.AttemptID || current.PayloadRef != candidate.PayloadRef || current.Status != candidate.Status || current.ReceiptRef != candidate.ReceiptRef || current.ReceiptAppliedAt != nil {
			return nil
		}
		var registered int64
		if err = tx.Model(&ExecutionAttempt{}).Where("org_id = ? AND operation_id = ?", current.OrgID, current.ID).Count(&registered).Error; err != nil {
			return err
		}
		if registered != 0 {
			return nil
		}
		switch current.Status {
		case "pending":
			if t.Status != "accepted" || t.UpstreamTaskID != "" || current.AttemptID != "" || current.LeaseUntil != nil || current.ReceiptRef != "" {
				return nil
			}
			var priorAttempts int64
			if err = tx.Model(&Attempt{}).Where("org_id = ? AND operation_id = ?", current.OrgID, current.ID).Count(&priorAttempts).Error; err != nil {
				return err
			}
			if priorAttempts != 0 {
				return nil
			}
		case "in_flight", "unknown":
			if current.AttemptID == "" || current.LeaseUntil == nil {
				return nil
			}
			if current.ReceiptRef != "" {
				if current.ReceivedOutcome != string(ark.OutcomeNotSent) {
					return nil
				}
			} else if current.Status != "in_flight" {
				return nil
			}
		default:
			return nil
		}
		// The original RR transaction may already have a snapshot. SHARE makes
		// this a current metadata read, held until the diagnostic commits.
		_, identityErr := requireCreateExecutionIdentityTx(tx.Clauses(clause.Locking{Strength: "SHARE"}), t, st, current)
		if err = ctx.Err(); err != nil {
			return err
		}
		if identityErr == nil {
			return nil
		}
		if !permanentCreateExecutionIdentityError(identityErr) {
			return identityErr
		}
		clockCtx, stopClock := context.WithTimeout(ctx, 200*time.Millisecond)
		now, clockErr := authorityTimeTx(tx.Session(&gorm.Session{NewDB: true, Context: clockCtx}))
		stopClock()
		if clockErr != nil {
			return clockErr
		}
		if current.Status != "pending" && current.LeaseUntil.After(now) {
			return nil
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		changed := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND resource_id = ? AND kind = ? AND status = ? AND version = ? AND attempt_id = ? AND payload_ref = ? AND receipt_ref = ? AND receipt_applied_at IS NULL", current.OrgID, current.ID, current.ResourceID, current.Kind, current.Status, current.Version, current.AttemptID, current.PayloadRef, current.ReceiptRef).
			Updates(map[string]any{"status": "needs_review", "error_code": executionIdentityReviewCode, "updated_at": now})
		if changed.Error != nil {
			return changed.Error
		}
		isolated = changed.RowsAffected == 1
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return false, authorityClockFailure(ctx.Err())
		}
		return false, authorityClockFailure(err)
	}
	// A confirmed commit is not retroactively undone by a caller cancellation.
	return isolated, nil
}
