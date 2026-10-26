package video

import (
	"context"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// Both the producer and recovery consumer enter the same reducer. Recovery
// never borrows the producer's old claim, and the deployment write switch does
// not disable consumption of evidence already committed under inbox-v1.
func (s *Service) applyClaimedCreateInbox(ctx context.Context, claim Operation, receiptID string) error {
	return s.applyCreateInbox(ctx, claim.OrgID, receiptID, &claim)
}

func (s *Service) recoverResponseInbox(ctx context.Context) error {
	_, err := s.recoverResponseInboxProgress(ctx)
	return err
}

func (s *Service) recoverResponseInboxProgress(ctx context.Context) (recoveryLaneResult, error) {
	result, expiredErr := s.recoverExpiredInboxAttemptsProgress(ctx)
	if err := ctx.Err(); err != nil {
		return result, safeRecoveryErrors(expiredErr, err)
	}
	var rows []ReceiptApplication
	if err := s.responseInboxCandidates(ctx).Order("ap.next_attempt_at, ap.receipt_id").Limit(30).Scan(&rows).Error; err != nil {
		result.Failed++
		return result, safeRecoveryErrors(expiredErr, err)
	}
	return s.recoverInboxApplications(ctx, rows, result, expiredErr, s.applyCreateInboxProgress)
}

func supportedInboxRuntime(db *gorm.DB) *gorm.DB {
	return supportedOperationRuntime(db).
		Where("ea.target_upstream_task_id IS NULL").
		Where("ea.storage_protocol = ? AND ea.action = ? AND ea.runtime_version = v.runtime_version AND ea.adapter_version = v.adapter_version AND ea.protocol_contract_version = v.protocol_contract_version AND ea.billing_schema_version = v.billing_schema_version AND ea.execution_schema_version = ? AND ea.observation_schema_version = ?", responseInboxProtocol, "create_task", 1, 1).
		Where("ea.provider_scope_id = v.provider_scope_id AND ea.credential_snapshot_id = v.credential_snapshot_id AND ea.billing_snapshot_id = v.billing_snapshot_id AND ea.input_payload_ref = v.effective_payload_ref AND o.payload_ref = ea.input_payload_ref AND ea.input_hmac_version = ? AND ea.evidence_hmac_version = ?", "v1", "v1")
}

func (s *Service) responseInboxCandidates(ctx context.Context) *gorm.DB {
	db := s.recoveryDB(ctx).Table("video_receipt_applications AS ap").Select("ap.*").
		Joins("JOIN video_response_inbox r ON r.receipt_id = ap.receipt_id AND r.org_id = ap.org_id AND r.task_id = ap.task_id AND r.attempt_id = ap.attempt_id").
		Joins("JOIN video_execution_attempts ea ON ea.attempt_id = r.attempt_id AND ea.org_id = r.org_id AND ea.task_id = r.task_id AND ea.operation_id = r.operation_id").
		Joins("JOIN video_operations o ON o.id = ea.operation_id AND o.org_id = ea.org_id AND o.resource_id = ea.task_id").
		Joins("JOIN video_task_states v ON v.task_id = ea.task_id AND v.org_id = ea.org_id")
	if s.DB.Dialector.Name() != "mysql" {
		db.AddError(ErrUnavailable)
		return db
	}
	db = supportedInboxRuntime(db).Where("ap.state IN ? AND r.storage_protocol = ea.storage_protocol AND r.action = ea.action AND r.runtime_version = ea.runtime_version AND r.adapter_version = ea.adapter_version AND r.protocol_contract_version = ea.protocol_contract_version AND r.billing_schema_version = ea.billing_schema_version AND r.execution_schema_version = ea.execution_schema_version AND r.observation_schema_version = ea.observation_schema_version", []string{"pending", "blocked"}).
		Where("r.provider_scope_id = ea.provider_scope_id AND r.credential_snapshot_id = ea.credential_snapshot_id AND r.billing_snapshot_id = ea.billing_snapshot_id AND r.evidence_hmac_version = ea.evidence_hmac_version").
		Where("o.status IN ? OR (o.status = ? AND o.lease_until <= UTC_TIMESTAMP(3))", []string{"unknown", "confirmed", "rejected"}, "in_flight")
	return db.Where("ap.next_attempt_at <= UTC_TIMESTAMP(3)")
}

func (s *Service) applyCreateInbox(ctx context.Context, org uint64, receiptID string, claim *Operation) error {
	_, err := s.applyCreateInboxProgress(ctx, org, receiptID, claim)
	return err
}

func (s *Service) applyCreateInboxProgress(ctx context.Context, org uint64, receiptID string, claim *Operation) (recoveryDisposition, error) {
	disposition := recoveryUnchanged
	var receipt ResponseInbox
	if err := s.recoveryDB(ctx).Where("org_id = ? AND receipt_id = ?", org, receiptID).Take(&receipt).Error; err != nil {
		return recoveryUnchanged, err
	}
	var applyErr error
	err := s.recoveryDB(ctx).Transaction(func(tx *gorm.DB) error {
		// Read tombstones explicitly. Saving/applying progress must not revive
		// a deleted task, nor make an already saved response disappear.
		var t task.Task
		var st State
		var op Operation
		if err := locked(tx).Where("org_id = ? AND task_id = ? AND provider = ?", org, receipt.TaskID, Provider).Take(&t).Error; err != nil {
			return err
		}
		if err := locked(tx).Where("org_id = ? AND task_id = ?", org, receipt.TaskID).Take(&st).Error; err != nil {
			return err
		}
		if err := locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", org, receipt.OperationID, receipt.TaskID).Take(&op).Error; err != nil {
			return err
		}
		var app ReceiptApplication
		if err := locked(tx).Where("org_id = ? AND receipt_id = ? AND task_id = ? AND attempt_id = ?", org, receiptID, receipt.TaskID, receipt.AttemptID).Take(&app).Error; err != nil {
			return err
		}
		var attempt ExecutionAttempt
		if err := tx.Where("org_id = ? AND attempt_id = ? AND operation_id = ? AND task_id = ?", org, receipt.AttemptID, op.ID, t.TaskID).Take(&attempt).Error; err != nil {
			return err
		}
		mark := func(state, code string, cause error) error {
			applyErr = cause
			// Completed or quarantined progress must not return to automatic
			// recovery because a later validation is temporarily unavailable.
			// Definite conflicting evidence can still require manual review.
			if (app.State == "applied" || app.State == "review") && state != "review" {
				return nil
			}
			if app.State == state && app.ErrorCode == code && state == "review" {
				return nil
			}
			due, err := responseDatabaseTimeTx(tx)
			if err != nil {
				return err
			}
			result := tx.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ? AND version = ?", org, receiptID, app.Version).Updates(map[string]any{"state": state, "error_code": code, "version": app.Version + 1, "next_attempt_at": due.Add(30 * time.Second), "updated_at": due})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
			disposition = recoveryDeferred
			if state == "review" {
				disposition = recoveryReview
			}
			return nil
		}
		if err := s.validateResponseInboxTx(tx, attempt, receipt); err != nil {
			return mark("blocked", "response_identity_unavailable", err)
		}
		if attempt.StorageProtocol != responseInboxProtocol || attempt.Action != "create_task" || attempt.ExecutionSchemaVersion != 1 || attempt.ObservationSchemaVersion != 1 || receipt.StorageProtocol != attempt.StorageProtocol || attempt.TargetUpstreamTaskID != nil {
			return mark("blocked", "runtime_contract_unsupported", videoruntime.ErrUnsupported)
		}
		if err := requireTaskRuntimeTx(tx, st); err != nil {
			if recoveryOnlyErrors(err, videoruntime.ErrUnsupported) {
				return mark("blocked", "runtime_contract_unsupported", err)
			}
			return err
		}
		if err := requireOperationRuntime(st, op); err != nil {
			return mark("blocked", "runtime_contract_unsupported", err)
		}
		if attempt.RuntimeVersion != st.RuntimeVersion || attempt.AdapterVersion != st.AdapterVersion || attempt.ProtocolContractVersion != st.ProtocolContractVersion || attempt.BillingSchemaVersion != st.BillingSchemaVersion || attempt.ProviderScopeID != st.ProviderScopeID || attempt.CredentialSnapshotID != st.CredentialSnapshotID || attempt.BillingSnapshotID != st.BillingSnapshotID || attempt.InputPayloadRef != st.EffectivePayloadRef || op.PayloadRef != attempt.InputPayloadRef {
			return mark("blocked", "runtime_contract_unsupported", videoruntime.ErrUnsupported)
		}
		// Gather input and sibling diagnostics before taking the final lease
		// observation. These reads can wait; an expired sender must not newly
		// quarantine the task after a permission check made before the wait.
		diagnosticState, diagnosticCode, diagnosticErr := func() (string, string, error) {
			if err := s.validateExecutionAttemptInputTx(tx, attempt); err != nil {
				if recoveryOnlyErrors(err, ErrConflict) {
					return "review", "execution_identity_conflict", err
				}
				return "blocked", "execution_identity_unavailable", err
			}
			if op.AttemptID != attempt.AttemptID || op.Kind != attempt.Action {
				return "review", "attempt_identity_conflict", ErrConflict
			}
			// A second definite result must be compared even after the first one
			// reached confirmed/rejected or this application's AppliedAt is set.
			var definite []ResponseInbox
			if err := tx.Select("outcome", "upstream_task_id").Where("org_id = ? AND attempt_id = ? AND outcome IN ?", org, attempt.AttemptID, []string{string(ark.OutcomeAccepted), string(ark.OutcomeRejected), string(ark.OutcomeNotSent)}).Find(&definite).Error; err != nil {
				return "", "", err
			}
			conflict := false
			for i := 1; i < len(definite); i++ {
				if !equivalentCreateResult(definite[0].Outcome, definite[0].UpstreamTaskID, definite[i].Outcome, definite[i].UpstreamTaskID) {
					conflict = true
					break
				}
			}
			if op.Status == "confirmed" || op.Status == "rejected" {
				for _, result := range definite {
					if !equivalentCreateResult(op.ReceivedOutcome, op.ReceivedTaskID, result.Outcome, result.UpstreamTaskID) {
						conflict = true
						break
					}
				}
			}
			if conflict {
				return "review", "response_evidence_conflict", ErrConflict
			}
			// Completed/tombstoned observations keep their existing precedence.
			// Only a live reducer candidate needs the create quarantine read.
			if app.State != "applied" && app.State != "review" && op.Status != "confirmed" && op.Status != "rejected" && !(op.Status == "unknown" && op.ReceiptAppliedAt != nil && receipt.Outcome == string(ark.OutcomeUnknown)) && st.DeletedAt == nil && t.UpstreamTaskID == "" && !terminal(t.Status) && !financialFinal(st.BillingState) {
				if err := requireNoCreateInboxReviewTx(tx, org, t.TaskID); err != nil {
					if recoveryOnlyErrors(err, errCreateInboxReview) {
						return "review", "create_identity_review", err
					}
					return "", "", err
				}
			}
			return "", "", nil
		}()
		if diagnosticErr != nil && diagnosticState == "" {
			return diagnosticErr
		}
		// Identity/runtime reads may wait. Check the primary's clock only after
		// those reads and the current task/state/operation/application locks.
		now, err := authorityTimeTx(tx)
		if err != nil {
			return err
		}
		// A stale producer may preserve evidence/progress, but cannot diagnose
		// task state or apply a conflicting response with its expired claim.
		// The recovery path obtains current authority, including for a completed
		// operation whose newly arrived evidence must still be compared.
		if claim != nil {
			if claim.OrgID != org || claim.ID != op.ID || claim.ResourceID != t.TaskID || claim.Kind != op.Kind || claim.AttemptID != op.AttemptID || claim.Version != op.Version || op.Status != "in_flight" || op.LeaseUntil == nil || !op.LeaseUntil.After(now) {
				if app.State == "applied" {
					return nil
				}
				if app.State == "review" {
					return ErrConflict
				}
				return mark("pending", "claim_changed", ErrConflict)
			}
		} else if op.Status != "confirmed" && op.Status != "rejected" && op.Status != "unknown" && !(op.Status == "in_flight" && op.LeaseUntil != nil && !op.LeaseUntil.After(now)) {
			return mark("pending", "claim_active", ErrConflict)
		}
		if diagnosticErr != nil {
			if diagnosticCode == "response_evidence_conflict" && st.DeletedAt == nil && st.SyncState != "needs_review" {
				updated := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, t.TaskID).Update("sync_state", "needs_review")
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrConflict
				}
				disposition = recoveryReview
			}
			return mark(diagnosticState, diagnosticCode, diagnosticErr)
		}
		if app.State == "applied" {
			return nil
		}
		if app.State == "review" {
			return ErrConflict
		}
		complete := func(eventID string) error {
			result := tx.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ? AND version = ?", org, receiptID, app.Version).Updates(map[string]any{"state": "applied", "error_code": "", "version": app.Version + 1, "applied_at": now, "applied_event_id": eventID, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
			disposition = recoveryApplied
			return nil
		}
		if op.Status == "confirmed" || op.Status == "rejected" {
			// Equivalent definite or weaker unknown evidence is retained without
			// replaying a state event or any financial action.
			return complete("")
		}
		if op.Status == "unknown" && op.ReceiptAppliedAt != nil && receipt.Outcome == string(ark.OutcomeUnknown) {
			return complete("")
		}
		if st.DeletedAt != nil {
			return mark("review", "task_deleted", ErrState)
		}
		if t.UpstreamTaskID != "" || terminal(t.Status) || financialFinal(st.BillingState) {
			return mark("review", "task_fact_conflict", ErrConflict)
		}
		current := op
		current.ReceivedOutcome, current.ReceivedTaskID, current.HTTPStatus = receipt.Outcome, receipt.UpstreamTaskID, receipt.HTTPStatus
		current.ReceiptRef = receipt.PayloadRef
		if err := tx.Transaction(func(applyTx *gorm.DB) error {
			return s.applyCreateResultTx(applyTx, t, st, current, current, receiptID, now)
		}); err != nil {
			if recoveryOnlyErrors(err, ErrConflict) {
				return mark("review", "upstream_binding_conflict", err)
			}
			return err
		}
		var event Event
		if err := tx.Select("id").Where("org_id = ? AND task_id = ? AND request_id = ? AND evidence_ref = ?", org, t.TaskID, receiptID, receipt.PayloadRef).Take(&event).Error; err != nil {
			return err
		}
		return complete(event.ID)
	})
	if err != nil {
		return recoveryUnchanged, safeRecoveryErrors(applyErr, err)
	}
	return disposition, applyErr
}

func equivalentCreateResult(outcome, taskID, otherOutcome, otherTaskID string) bool {
	if outcome == string(ark.OutcomeAccepted) || otherOutcome == string(ark.OutcomeAccepted) {
		return outcome == otherOutcome && taskID != "" && taskID == otherTaskID
	}
	noTask := func(v string) bool { return v == string(ark.OutcomeRejected) || v == string(ark.OutcomeNotSent) }
	return noTask(outcome) && noTask(otherOutcome)
}

// An expired registered attempt without a response is not an unsent request.
// Keep its immutable identity and all reservations; never requeue its POST.
func (s *Service) recoverExpiredInboxAttempts(ctx context.Context) error {
	_, err := s.recoverExpiredInboxAttemptsProgress(ctx)
	return err
}

func (s *Service) recoverExpiredInboxAttemptsProgress(ctx context.Context) (recoveryLaneResult, error) {
	var result recoveryLaneResult
	var failures []error
	if s.DB.Dialector.Name() != "mysql" {
		result.Failed++
		return result, ErrUnavailable
	}
	var attempts []ExecutionAttempt
	q := supportedInboxRuntime(s.recoveryDB(ctx).Table("video_execution_attempts ea").Select("ea.*").
		Joins("JOIN video_operations o ON o.id = ea.operation_id AND o.org_id = ea.org_id AND o.resource_id = ea.task_id").
		Joins("JOIN video_task_states v ON v.org_id = ea.org_id AND v.task_id = ea.task_id")).
		Where("v.deleted_at IS NULL AND o.attempt_id = ea.attempt_id AND o.status = ? AND o.lease_until <= UTC_TIMESTAMP(3)", "in_flight").
		Where("NOT EXISTS (SELECT 1 FROM video_response_inbox r WHERE r.org_id = ea.org_id AND r.attempt_id = ea.attempt_id)")
	if err := q.Order("o.lease_until, ea.attempt_id").Limit(30).Scan(&attempts).Error; err != nil {
		result.Failed++
		return result, err
	}
	result.Selected = int64(len(attempts))
	for _, attempt := range attempts {
		if err := ctx.Err(); err != nil {
			return result, safeRecoveryErrors(append(failures, err)...)
		}
		disposition := recoveryUnchanged
		err := s.recoveryDB(ctx).Transaction(func(tx *gorm.DB) error {
			t, st, err := loadTask(tx, attempt.OrgID, attempt.TaskID, true)
			if err != nil {
				return err
			}
			var op Operation
			if err := locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", attempt.OrgID, attempt.OperationID, attempt.TaskID).Take(&op).Error; err != nil {
				return err
			}
			if op.Status != "in_flight" || op.Version != attempt.OperationClaimVersion || op.AttemptID != attempt.AttemptID || op.Kind != attempt.Action || op.LeaseUntil == nil {
				return nil
			}
			var responses int64
			if err := tx.Model(&ResponseInbox{}).Where("org_id = ? AND attempt_id = ?", attempt.OrgID, attempt.AttemptID).Count(&responses).Error; err != nil {
				return err
			}
			if responses != 0 {
				return nil
			}
			if err := requireCurrentOperationRuntimeTx(tx, st, op); err != nil {
				return err
			}
			now, err := authorityTimeTx(tx)
			if err != nil {
				return err
			}
			if op.LeaseUntil.After(now) {
				return nil
			}
			updated := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ?", op.OrgID, op.ID, op.Version).Updates(map[string]any{"status": "unknown", "error_code": "submission_uncertain", "updated_at": now})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrConflict
			}
			previous := t.Status
			if !terminal(t.Status) {
				t.Status = "submission_unknown"
				if err := tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Update("status", t.Status).Error; err != nil {
					return err
				}
			}
			st.SyncState = "needs_review"
			if !financialFinal(st.BillingState) {
				st.BillingState = "pending_usage"
			}
			if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", st.OrgID, st.TaskID).Updates(map[string]any{"sync_state": st.SyncState, "billing_state": st.BillingState, "review_due_at": now.Add(time.Hour), "updated_at": now}).Error; err != nil {
				return err
			}
			if err := appendEventTx(tx, &t, &st, "task.submission_unknown", previous, t.Status, attempt.AttemptID, "", now); err != nil {
				return err
			}
			disposition = recoveryDeferred
			return nil
		})
		if err != nil {
			disposition = recoveryUnchanged
		}
		if recoveryOnlyErrors(err, videoruntime.ErrUnsupported, gorm.ErrRecordNotFound) && ctx.Err() == nil {
			err = nil
		}
		itemErr := safeRecoveryErrors(err, ctx.Err())
		result.record(disposition, itemErr)
		failures = append(failures, itemErr)
	}
	return result, safeRecoveryErrors(failures...)
}
