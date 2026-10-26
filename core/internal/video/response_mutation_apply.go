package video

import (
	"context"
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func (s *Service) applyClaimedMutationInbox(ctx context.Context, claim Operation, receiptID string) error {
	return s.applyMutationInbox(ctx, claim.OrgID, receiptID, &claim)
}

// Fixed aliases, no request-provided SQL. Schema/protocol/claim filters run
// before LIMIT; incompatible attempts must never occupy the recovery batch.
func supportedMutationInboxRuntime(db *gorm.DB) *gorm.DB {
	return supportedOperationRuntime(db).
		Where("ea.storage_protocol = ? AND ((ea.action = ? AND o.kind = ? AND OCTET_LENGTH(o.kind) = ?) OR (ea.action = ? AND o.kind = ? AND OCTET_LENGTH(o.kind) = ?))", videoruntime.ActionStorageProtocolV1, "cancel_task", "cancel_task", len("cancel_task"), "delete_task", "delete_task", len("delete_task")).
		Where("ea.runtime_version = v.runtime_version AND ea.adapter_version = v.adapter_version AND ea.protocol_contract_version = v.protocol_contract_version AND ea.billing_schema_version = v.billing_schema_version AND ea.execution_schema_version = ? AND ea.observation_schema_version = ?", 2, 1).
		Where("ea.provider_scope_id = v.provider_scope_id AND ea.credential_snapshot_id = v.credential_snapshot_id AND ea.billing_snapshot_id = v.billing_snapshot_id AND o.payload_ref = ea.input_payload_ref AND OCTET_LENGTH(o.payload_ref) = OCTET_LENGTH(ea.input_payload_ref) AND o.attempt_id = ea.attempt_id AND OCTET_LENGTH(o.attempt_id) = OCTET_LENGTH(ea.attempt_id)").
		Where("ea.target_upstream_task_id IS NOT NULL AND OCTET_LENGTH(ea.target_upstream_task_id) BETWEEN 1 AND 128 AND ea.input_hmac_version = ? AND ea.evidence_hmac_version = ?", "v1", "v1")
}

func (s *Service) mutationResponseInboxCandidates(ctx context.Context) *gorm.DB {
	db := s.DB.WithContext(ctx).Table("video_receipt_applications AS ap").Select("ap.*").
		Joins("JOIN video_response_inbox r ON r.receipt_id = ap.receipt_id AND r.org_id = ap.org_id AND r.task_id = ap.task_id AND r.attempt_id = ap.attempt_id").
		Joins("JOIN video_execution_attempts ea ON ea.attempt_id = r.attempt_id AND ea.org_id = r.org_id AND ea.task_id = r.task_id AND ea.operation_id = r.operation_id").
		Joins("JOIN video_operations o ON o.id = ea.operation_id AND o.org_id = ea.org_id AND o.resource_id = ea.task_id").
		Joins("JOIN video_task_states v ON v.task_id = ea.task_id AND v.org_id = ea.org_id")
	if s.DB.Dialector.Name() != "mysql" {
		db.AddError(ErrUnavailable)
		return db
	}
	db = supportedMutationInboxRuntime(db).
		Where("ap.state IN ? AND r.storage_protocol = ea.storage_protocol AND r.action = ea.action AND r.runtime_version = ea.runtime_version AND r.adapter_version = ea.adapter_version AND r.protocol_contract_version = ea.protocol_contract_version AND r.billing_schema_version = ea.billing_schema_version AND r.execution_schema_version = ea.execution_schema_version AND r.observation_schema_version = ea.observation_schema_version", []string{"pending", "blocked"}).
		Where("r.provider_scope_id = ea.provider_scope_id AND r.credential_snapshot_id = ea.credential_snapshot_id AND r.billing_snapshot_id = ea.billing_snapshot_id AND r.evidence_hmac_version = ea.evidence_hmac_version").
		Where("o.status IN ? OR (o.status = ? AND o.lease_until <= UTC_TIMESTAMP(3))", []string{"unknown", "confirmed", "rejected", "needs_review"}, "in_flight")
	return db.Where("ap.next_attempt_at <= UTC_TIMESTAMP(3)")
}

func actionAttemptContract(attempt ExecutionAttempt) videoruntime.ActionContractKey {
	return videoruntime.ActionContractKey{StorageProtocol: attempt.StorageProtocol, Action: attempt.Action,
		Contract:                    videoruntime.Contract{RuntimeVersion: attempt.RuntimeVersion, AdapterVersion: attempt.AdapterVersion, ProtocolContractVersion: attempt.ProtocolContractVersion, BillingSchemaVersion: attempt.BillingSchemaVersion},
		ExecutionInputSchemaVersion: attempt.ExecutionSchemaVersion, ObservationSchemaVersion: attempt.ObservationSchemaVersion, DeliverySchemaVersion: 0}
}

// A 404 is absence acknowledgement, not proof that a generation was cancelled.
// The immutable target supplies association, never a fabricated returned ID.
func mutationEvidenceClass(outcome string, status int) string {
	if outcome == string(ark.OutcomeNotSent) {
		return "not_sent"
	}
	if status == 404 && (outcome == string(ark.OutcomeAccepted) || outcome == string(ark.OutcomeRejected)) || outcome == string(ark.OutcomeAccepted) && status >= 200 && status < 300 {
		return "ack"
	}
	if outcome == string(ark.OutcomeRejected) {
		return "rejected"
	}
	return ""
}

func (s *Service) applyMutationInbox(ctx context.Context, org uint64, receiptID string, claim *Operation) error {
	var receipt ResponseInbox
	if err := s.DB.WithContext(ctx).Where("org_id = ? AND receipt_id = ?", org, receiptID).Take(&receipt).Error; err != nil {
		return err
	}
	var applyErr error
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			if (app.State == "applied" || app.State == "review") && state != "review" {
				return nil
			}
			due, err := authorityTimeTx(tx)
			if err != nil {
				return err
			}
			if state == "review" && op.Status != "needs_review" {
				if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ?", org, op.ID).Updates(map[string]any{"status": "needs_review", "error_code": code, "updated_at": due}).Error; err != nil {
					return err
				}
			}
			if app.State == state && app.ErrorCode == code && state == "review" {
				return nil
			}
			return tx.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ? AND version = ?", org, receiptID, app.Version).Updates(map[string]any{"state": state, "error_code": code, "version": app.Version + 1, "next_attempt_at": due.Add(30 * time.Second), "updated_at": due}).Error
		}
		if err := s.validateResponseInboxTx(tx, attempt, receipt); err != nil {
			return mark("blocked", "response_identity_unavailable", err)
		}
		if _, err := videoruntime.ResolveActionContract(actionAttemptContract(attempt)); err != nil {
			return mark("blocked", "runtime_contract_unsupported", err)
		}
		if err := requireTaskRuntimeTx(tx, st); err != nil {
			return mark("blocked", "runtime_contract_unsupported", err)
		}
		if err := requireOperationRuntime(st, op); err != nil {
			return mark("blocked", "runtime_contract_unsupported", err)
		}
		if attempt.RuntimeVersion != st.RuntimeVersion || attempt.AdapterVersion != st.AdapterVersion || attempt.ProtocolContractVersion != st.ProtocolContractVersion || attempt.BillingSchemaVersion != st.BillingSchemaVersion || attempt.ProviderScopeID != st.ProviderScopeID || attempt.CredentialSnapshotID != st.CredentialSnapshotID || attempt.BillingSnapshotID != st.BillingSnapshotID || op.PayloadRef != attempt.InputPayloadRef {
			return mark("blocked", "runtime_contract_unsupported", videoruntime.ErrUnsupported)
		}
		// A previous owner may save evidence but must not quarantine or apply it.
		if op.AttemptID != attempt.AttemptID || op.Kind != attempt.Action {
			return mark("blocked", "claim_changed", ErrConflict)
		}
		// Finish immutable identity and conflict reads before observing lease
		// permission. Collect diagnostics without writes: a sender whose lease
		// elapsed during these reads must not newly quarantine the operation.
		diagnosticState, diagnosticCode, diagnosticErr := func() (string, string, error) {
			if attempt.TargetUpstreamTaskID == nil || *attempt.TargetUpstreamTaskID == "" || responseIdentifier(*attempt.TargetUpstreamTaskID, "", 128) != *attempt.TargetUpstreamTaskID || t.UpstreamTaskID != *attempt.TargetUpstreamTaskID {
				return "review", "action_target_conflict", ErrConflict
			}
			if err := s.validateMutationAttemptInputTx(tx, attempt); err != nil {
				if errors.Is(err, ErrConflict) {
					return "review", "execution_identity_conflict", err
				}
				return "blocked", "execution_identity_unavailable", err
			}
			var binding UpstreamBinding
			if err := tx.Where("provider_scope_id = ? AND upstream_task_id = ? AND OCTET_LENGTH(upstream_task_id) = ?", attempt.ProviderScopeID, *attempt.TargetUpstreamTaskID, len(*attempt.TargetUpstreamTaskID)).Take(&binding).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return "review", "action_binding_conflict", ErrConflict
				}
				return "blocked", "action_binding_unavailable", err
			}
			if binding.OrgID != org || binding.TaskID != t.TaskID || binding.UpstreamTaskID != *attempt.TargetUpstreamTaskID {
				return "review", "action_binding_conflict", ErrConflict
			}
			var observations []ResponseInbox
			if err := tx.Select("outcome", "http_status", "upstream_task_id").Where("org_id = ? AND attempt_id = ?", org, attempt.AttemptID).Find(&observations).Error; err != nil {
				return "", "", err
			}
			known := ""
			// A GET-confirmed operation has no mutation receipt yet. Its empty
			// ReceivedOutcome is not a prior DELETE acknowledgement.
			if op.ReceiptAppliedAt != nil && op.ReceivedOutcome != "" {
				known = mutationEvidenceClass(op.ReceivedOutcome, op.HTTPStatus)
			}
			for _, observation := range observations {
				if observation.UpstreamTaskID != "" && observation.UpstreamTaskID != *attempt.TargetUpstreamTaskID {
					return "review", "action_target_conflict", ErrConflict
				}
				kind := mutationEvidenceClass(observation.Outcome, observation.HTTPStatus)
				if kind == "" {
					continue
				}
				if known != "" && known != kind {
					return "review", "response_evidence_conflict", ErrConflict
				}
				known = kind
			}
			// Mutation review is local to the operation, never create quarantine.
			var reviewed int64
			if err := tx.Table("video_receipt_applications AS sibling").Joins("JOIN video_response_inbox AS evidence ON evidence.receipt_id = sibling.receipt_id AND evidence.org_id = sibling.org_id").Where("sibling.org_id = ? AND evidence.operation_id = ? AND sibling.state = ?", org, op.ID, "review").Count(&reviewed).Error; err != nil {
				return "", "", err
			}
			if reviewed != 0 || op.Status == "needs_review" {
				return "review", "operation_identity_review", ErrConflict
			}
			return "", "", nil
		}()
		if diagnosticErr != nil && diagnosticState == "" {
			return diagnosticErr
		}
		now, err := authorityTimeTx(tx)
		if err != nil {
			return err
		}
		if claim != nil {
			if claim.OrgID != org || claim.ID != op.ID || claim.ResourceID != t.TaskID || claim.Kind != op.Kind || claim.AttemptID != op.AttemptID || claim.Version != op.Version || op.Status != "in_flight" || op.LeaseUntil == nil || !op.LeaseUntil.After(now) {
				if app.State == "applied" {
					return nil
				}
				return mark("pending", "claim_changed", ErrConflict)
			}
		} else if op.Status != "confirmed" && op.Status != "rejected" && op.Status != "unknown" && op.Status != "needs_review" && !(op.Status == "in_flight" && op.LeaseUntil != nil && !op.LeaseUntil.After(now)) {
			return mark("pending", "claim_active", ErrConflict)
		}
		if diagnosticErr != nil {
			return mark(diagnosticState, diagnosticCode, diagnosticErr)
		}
		if app.State == "applied" {
			return nil
		}
		complete := func(eventID string) error {
			return tx.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ? AND version = ?", org, receiptID, app.Version).Updates(map[string]any{"state": "applied", "error_code": "", "version": app.Version + 1, "applied_at": now, "applied_event_id": eventID, "updated_at": now}).Error
		}
		kind := mutationEvidenceClass(receipt.Outcome, receipt.HTTPStatus)
		previousKind := mutationEvidenceClass(op.ReceivedOutcome, op.HTTPStatus)
		// A weaker observation cannot overwrite an already consumed ACK/reject.
		if op.ReceiptAppliedAt != nil && (kind == "" || kind == previousKind) {
			return complete("")
		}
		updates := map[string]any{"receipt_ref": receipt.PayloadRef, "http_status": receipt.HTTPStatus, "received_outcome": receipt.Outcome, "received_task_id": receipt.UpstreamTaskID, "receipt_applied_at": now, "updated_at": now}
		if op.Status == "confirmed" || op.Status == "rejected" {
			if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ?", org, op.ID).Updates(updates).Error; err != nil {
				return err
			}
			return complete("")
		}
		status, code := "unknown", "upstream_outcome_unknown"
		if kind == "not_sent" {
			status, code = "rejected", "request_not_sent"
		}
		if kind == "rejected" {
			status, code = "rejected", "upstream_rejected"
		}
		if op.Kind == "delete_task" && kind == "ack" {
			if !terminal(t.Status) || !financialFinal(st.BillingState) || st.DeletionOperationID != op.ID {
				return mark("review", "delete_fact_conflict", ErrConflict)
			}
			status, code = "confirmed", ""
			if st.DeletedAt == nil {
				if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, t.TaskID).Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
					return err
				}
				st.DeletedAt = &now
				if err := s.afterDeletionTx(tx, &t, &st, now); err != nil {
					return err
				}
			}
		}
		if op.Kind == "cancel_task" && status == "unknown" && st.DeletedAt == nil && !terminal(t.Status) {
			// Only schedule authoritative observation. No status regression,
			// billing-state mutation, release or settlement is allowed here.
			if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, t.TaskID).Update("next_poll_at", now).Error; err != nil {
				return err
			}
		}
		updates["status"], updates["error_code"] = status, code
		if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ?", org, op.ID).Updates(updates).Error; err != nil {
			return err
		}
		if err := appendEventTx(tx, &t, &st, "task.operation_updated", t.Status, t.Status, receiptID, receipt.PayloadRef, now); err != nil {
			return err
		}
		var event Event
		if err := tx.Select("id").Where("org_id = ? AND task_id = ? AND request_id = ? AND evidence_ref = ?", org, t.TaskID, receiptID, receipt.PayloadRef).Take(&event).Error; err != nil {
			return err
		}
		return complete(event.ID)
	})
	if err != nil {
		return err
	}
	return applyErr
}

func (s *Service) recoverMutationResponseInbox(ctx context.Context) error {
	if err := s.recoverExpiredMutationAttempts(ctx); err != nil {
		return err
	}
	var rows []ReceiptApplication
	if err := s.mutationResponseInboxCandidates(ctx).Order("ap.next_attempt_at, ap.receipt_id").Limit(30).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.applyMutationInbox(ctx, row.OrgID, row.ReceiptID, nil); err != nil {
			s.Log.Warn("video action inbox receipt needs recovery", "receipt_id", row.ReceiptID)
			if err := s.DB.WithContext(ctx).Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ? AND version = ? AND state IN ?", row.OrgID, row.ReceiptID, row.Version, []string{"pending", "blocked"}).Updates(map[string]any{"next_attempt_at": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 30 SECOND)"), "version": gorm.Expr("version + 1")}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) recoverExpiredMutationAttempts(ctx context.Context) error {
	if s.DB.Dialector.Name() != "mysql" {
		return ErrUnavailable
	}
	var attempts []ExecutionAttempt
	q := supportedMutationInboxRuntime(s.DB.WithContext(ctx).Table("video_execution_attempts ea").Select("ea.*").
		Joins("JOIN video_operations o ON o.id = ea.operation_id AND o.org_id = ea.org_id AND o.resource_id = ea.task_id").
		Joins("JOIN video_task_states v ON v.org_id = ea.org_id AND v.task_id = ea.task_id")).
		Where("o.status = ? AND o.lease_until <= UTC_TIMESTAMP(3)", "in_flight").
		Where("NOT EXISTS (SELECT 1 FROM video_response_inbox r WHERE r.org_id = ea.org_id AND r.attempt_id = ea.attempt_id)")
	if err := q.Order("o.lease_until, ea.attempt_id").Limit(30).Scan(&attempts).Error; err != nil {
		return err
	}
	for _, attempt := range attempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var t task.Task
			var st State
			var op Operation
			if err := locked(tx).Where("org_id = ? AND task_id = ? AND provider = ?", attempt.OrgID, attempt.TaskID, Provider).Take(&t).Error; err != nil {
				return err
			}
			if err := locked(tx).Where("org_id = ? AND task_id = ?", attempt.OrgID, attempt.TaskID).Take(&st).Error; err != nil {
				return err
			}
			if err := locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", attempt.OrgID, attempt.OperationID, attempt.TaskID).Take(&op).Error; err != nil {
				return err
			}
			now, err := authorityTimeTx(tx)
			if err != nil {
				return err
			}
			if op.Status != "in_flight" || op.AttemptID != attempt.AttemptID || op.Version != attempt.OperationClaimVersion || op.LeaseUntil == nil || op.LeaseUntil.After(now) {
				return nil
			}
			if _, err := videoruntime.ResolveActionContract(actionAttemptContract(attempt)); err != nil {
				return err
			}
			if err := requireTaskRuntimeTx(tx, st); err != nil {
				return err
			}
			if err := requireOperationRuntime(st, op); err != nil {
				return err
			}
			// Only this exact known claim can be diagnosed. A permanent broken
			// capsule is removed from automatic selection rather than starving
			// every later receipt at the head of a recovery batch.
			markIdentityReview := func() error {
				return tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND attempt_id = ?", op.OrgID, op.ID, op.Version, op.AttemptID).Updates(map[string]any{"status": "needs_review", "error_code": "execution_identity_conflict", "updated_at": now}).Error
			}
			if attempt.RuntimeVersion != st.RuntimeVersion || attempt.AdapterVersion != st.AdapterVersion || attempt.ProtocolContractVersion != st.ProtocolContractVersion || attempt.BillingSchemaVersion != st.BillingSchemaVersion || attempt.ProviderScopeID != st.ProviderScopeID || attempt.CredentialSnapshotID != st.CredentialSnapshotID || attempt.BillingSnapshotID != st.BillingSnapshotID || attempt.InputPayloadRef != op.PayloadRef || attempt.Action != op.Kind || attempt.TargetUpstreamTaskID == nil || *attempt.TargetUpstreamTaskID == "" || responseIdentifier(*attempt.TargetUpstreamTaskID, "", 128) != *attempt.TargetUpstreamTaskID || t.UpstreamTaskID != *attempt.TargetUpstreamTaskID {
				return markIdentityReview()
			}
			if err := s.validateMutationAttemptInputTx(tx, attempt); err != nil {
				if permanentMutationIdentityError(err) {
					return markIdentityReview()
				}
				return err
			}
			var count int64
			if err := tx.Model(&ResponseInbox{}).Where("org_id = ? AND attempt_id = ?", attempt.OrgID, attempt.AttemptID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return nil
			}
			if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ?", op.OrgID, op.ID, op.Version).Updates(map[string]any{"status": "unknown", "error_code": "submission_uncertain", "updated_at": now}).Error; err != nil {
				return err
			}
			return appendEventTx(tx, &t, &st, "task.operation_updated", t.Status, t.Status, attempt.AttemptID, "", now)
		})
		if err != nil && !errors.Is(err, videoruntime.ErrUnsupported) && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return nil
}

func permanentMutationIdentityError(err error) bool {
	for _, permanent := range []error{ErrConflict, invocation.ErrVideoSchemaUnsupported, invocation.ErrVideoAccessDenied, invocation.ErrVideoNotFound, invocation.ErrVideoInvalid, invocation.ErrVideoCorrupt, gorm.ErrRecordNotFound} {
		if errors.Is(err, permanent) {
			return true
		}
	}
	return false
}
