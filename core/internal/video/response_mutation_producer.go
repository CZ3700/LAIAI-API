package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

const (
	mutationPreparationDeferredCode = "action_prepare_deferred"
	mutationPreparationCooldown     = 30 * time.Second
)

func mutationPreparationDeferred(op Operation, now time.Time) bool {
	return op.Status == "pending" && op.ErrorCode == mutationPreparationDeferredCode && op.UpdatedAt.Add(mutationPreparationCooldown).After(now)
}

// A pending cancellation can become locally decidable while credentials are
// unavailable. These outcomes precede preparation/cooldown and never send HTTP.
func mutationLocalOutcome(t task.Task, op Operation) (status, code string, wait bool) {
	if op.Kind != "cancel_task" {
		return "", "", false
	}
	switch t.Status {
	case "submitting", "submission_unknown":
		return "", "", true
	case "cancelled":
		return "confirmed", "", false
	case "queued":
		return "", "", false
	default:
		return "rejected", "invalid_state", false
	}
}

func mutationPendingIdentity(st State, candidate, current Operation) error {
	if candidate.OrgID != current.OrgID || candidate.ID != current.ID || candidate.ResourceID != current.ResourceID || candidate.Kind != current.Kind || candidate.Version != current.Version || candidate.PayloadRef != current.PayloadRef || candidate.AttemptID != current.AttemptID || current.Version == 0 || current.Version == math.MaxUint64 {
		return ErrConflict
	}
	if err := requireOperationRuntime(st, candidate); err != nil {
		return err
	}
	return requireOperationRuntime(st, current)
}

// This is deliberately separate from legacy processMutation. Once this branch
// is selected there is no fallback to the old DELETE/persistResponse sequence.
func (s *Service) processMutationInbox(ctx context.Context, candidate Operation) error {
	if candidate.Kind != "cancel_task" && candidate.Kind != "delete_task" {
		return ErrInvalid
	}
	if s.DispatchPaused() {
		return ErrDispatchPaused
	}
	if s.responseDB == nil || s.Evidence == nil || s.Keyring == nil || s.Now == nil {
		return ErrUnavailable
	}
	t, st, err := loadTask(s.DB.WithContext(ctx), candidate.OrgID, candidate.ResourceID, false)
	if err != nil {
		return err
	}
	var current Operation
	if err := s.DB.WithContext(ctx).Where("org_id = ? AND id = ? AND resource_id = ?", candidate.OrgID, candidate.ID, candidate.ResourceID).Take(&current).Error; err != nil {
		return err
	}
	if current.Status != "pending" {
		return nil
	}
	if err := mutationPendingIdentity(st, candidate, current); err != nil {
		return err
	}
	if err := requireNoExecutionAttemptTx(s.DB.WithContext(ctx), candidate.OrgID, candidate.ID); err != nil {
		return err
	}
	if status, _, wait := mutationLocalOutcome(t, current); status != "" || wait {
		return s.finishPendingMutationLocally(ctx, current)
	}
	if mutationPreparationDeferred(current, s.Now().UTC()) {
		return nil
	}
	if current.Kind == "delete_task" && (!terminal(t.Status) || !financialFinal(st.BillingState) || st.DeletionOperationID != current.ID) {
		return ErrState
	}
	client, err := s.clientFor(ctx, t, st)
	if err != nil || strings.TrimSpace(client.Credential) == "" {
		return s.deferMutationPreparation(ctx, current)
	}
	preparedClaim := current
	preparedClaim.Version++
	preparedClaim.AttemptID, err = randomID("att_")
	if err != nil {
		return s.deferMutationPreparation(ctx, current)
	}
	prepared, err := s.prepareMutationInput(ctx, t, st, preparedClaim, client.Credential)
	if err != nil {
		return s.deferMutationPreparation(ctx, current)
	}
	var attempt ExecutionAttempt
	var claim Operation
	claimed := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		freshTask, fixed, err := loadTask(tx, current.OrgID, current.ResourceID, true)
		if err != nil {
			return err
		}
		var lockedOp Operation
		if err := locked(tx).Where("org_id = ? AND id = ?", current.OrgID, current.ID).Take(&lockedOp).Error; err != nil {
			return err
		}
		if err := requireNoExecutionAttemptTx(tx, current.OrgID, current.ID); err != nil {
			return err
		}
		if lockedOp.Status != "pending" {
			return nil
		}
		if err := requireTaskRuntimeTx(tx, fixed); err != nil {
			return err
		}
		if err := mutationPendingIdentity(fixed, current, lockedOp); err != nil {
			return err
		}
		now := s.Now().UTC()
		if status, code, wait := mutationLocalOutcome(freshTask, lockedOp); status != "" || wait {
			return mutationLocalUpdateTx(tx, lockedOp, status, code, now)
		}
		if mutationPreparationDeferred(lockedOp, now) {
			return nil
		}
		if s.DispatchPaused() {
			return ErrDispatchPaused
		}
		if lockedOp.Kind == "delete_task" && (!terminal(freshTask.Status) || !financialFinal(fixed.BillingState) || fixed.DeletionOperationID != lockedOp.ID) {
			return ErrState
		}
		allowed, err := s.rateTx(tx, fixed.ProviderScopeID, "delete", now)
		if err != nil || !allowed {
			return err
		}
		if s.DispatchPaused() {
			return ErrDispatchPaused
		}
		claim = preparedClaim
		lease := now.Add(3 * time.Minute)
		claim.Status, claim.LeaseUntil, claim.ErrorCode = "in_flight", &lease, ""
		if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND status = ?", lockedOp.OrgID, lockedOp.ID, lockedOp.Version, "pending").Updates(map[string]any{"status": "in_flight", "version": claim.Version, "attempt_id": claim.AttemptID, "lease_until": lease, "error_code": "", "updated_at": now}).Error; err != nil {
			return err
		}
		// register rechecks the captured State against the locked current facts;
		// never replace captured credentials/target/base URL with newer values.
		attempt, err = s.registerMutationAttemptTx(tx, t, st, claim, prepared)
		if err != nil {
			return err
		}
		claim.PayloadRef = attempt.InputPayloadRef
		if err := tx.Create(&Attempt{OrgID: claim.OrgID, TaskID: claim.ResourceID, AttemptID: claim.AttemptID, OperationID: claim.ID, RequestID: claim.AttemptID, Phase: "delete_sending", EvidenceRef: claim.PayloadRef, ObservedAt: now}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	input, err := s.readMutationAction(ctx, claim, attempt, client.Credential)
	if err != nil {
		// Unknown formats/identity corruption never become a definite rejection.
		// Expired or unavailable known input can record this producer's no-send,
		// but only after current claim and immutable metadata are revalidated.
		if errors.Is(err, invocation.ErrVideoExpired) || errors.Is(err, invocation.ErrVideoRetentionDenied) || errors.Is(err, invocation.ErrVideoStorage) || errors.Is(err, invocation.ErrVideoAuditUnavailable) {
			return s.saveMutationNotSent(ctx, claim, attempt, client.Credential)
		}
		return err
	}
	allowed, err := s.mutationOperationMaySend(ctx, claim, attempt)
	if err != nil || !allowed {
		return err
	}
	started := time.Now()
	response := client.Delete(ctx, input.TargetUpstreamTaskID)
	kind := "delete"
	if claim.Kind == "cancel_task" {
		kind = "cancel"
	}
	s.observeAdapterCall(kind, response, started)
	// Saving remains inside the producer's tracked call stack. Caller/lease
	// loss cannot erase evidence or let shutdown close its pool prematurely.
	receipt, err := s.saveMutationResponse(ctx, attempt, response, client.Credential)
	if err != nil {
		return err
	}
	return s.applyClaimedMutationInbox(ctx, claim, receipt.ReceiptID)
}

func mutationLocalUpdateTx(tx *gorm.DB, op Operation, status, code string, now time.Time) error {
	if status == "" {
		return nil
	}
	return tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND status = ?", op.OrgID, op.ID, op.Version, "pending").Updates(map[string]any{"status": status, "error_code": code, "updated_at": now}).Error
}

func (s *Service) finishPendingMutationLocally(ctx context.Context, op Operation) error {
	return s.withPendingMutation(ctx, op, func(tx *gorm.DB, t task.Task, st State, current Operation) error {
		status, code, _ := mutationLocalOutcome(t, current)
		return mutationLocalUpdateTx(tx, current, status, code, s.Now().UTC())
	})
}

func (s *Service) withPendingMutation(ctx context.Context, op Operation, apply func(*gorm.DB, task.Task, State, Operation) error) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t, st, err := loadTask(tx, op.OrgID, op.ResourceID, true)
		if err != nil {
			return err
		}
		var current Operation
		if err := locked(tx).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Take(&current).Error; err != nil {
			return err
		}
		if err := requireNoExecutionAttemptTx(tx, op.OrgID, op.ID); err != nil {
			return err
		}
		if current.Status != "pending" {
			return nil
		}
		if err := requireTaskRuntimeTx(tx, st); err != nil {
			return err
		}
		if err := mutationPendingIdentity(st, op, current); err != nil {
			return err
		}
		return apply(tx, t, st, current)
	})
}

func (s *Service) deferMutationPreparation(ctx context.Context, op Operation) error {
	deferred := false
	err := s.withPendingMutation(ctx, op, func(tx *gorm.DB, t task.Task, st State, current Operation) error {
		now := s.Now().UTC()
		if status, code, wait := mutationLocalOutcome(t, current); status != "" || wait {
			return mutationLocalUpdateTx(tx, current, status, code, now)
		}
		if mutationPreparationDeferred(current, now) {
			return nil
		}
		deferred = true
		return tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND status = ?", current.OrgID, current.ID, current.Version, "pending").Updates(map[string]any{"error_code": mutationPreparationDeferredCode, "updated_at": now}).Error
	})
	if err != nil {
		return err
	}
	if deferred {
		return ErrUnavailable
	}
	return nil
}

// This validation uses only bounded-context APIs for Invocation metadata and
// snapshot identity. It neither decrypts historical evidence nor takes Key locks.
func (s *Service) mutationSendIdentityTx(tx *gorm.DB, claim Operation, attempt ExecutionAttempt) (mutationActionInput, time.Time, error) {
	var empty mutationActionInput
	if _, err := videoruntime.ResolveActionContract(actionAttemptContract(attempt)); err != nil {
		return empty, time.Time{}, err
	}
	if claim.OrgID != attempt.OrgID || claim.ID != attempt.OperationID || claim.ResourceID != attempt.TaskID || claim.Kind != attempt.Action || claim.AttemptID != attempt.AttemptID || claim.Version != attempt.OperationClaimVersion || claim.PayloadRef != attempt.InputPayloadRef || attempt.TargetUpstreamTaskID == nil {
		return empty, time.Time{}, ErrConflict
	}
	var stored ExecutionAttempt
	if err := tx.Where("org_id = ? AND attempt_id = ?", attempt.OrgID, attempt.AttemptID).Take(&stored).Error; err != nil {
		return empty, time.Time{}, err
	}
	if !sameExecutionAttempt(stored, attempt) {
		return empty, time.Time{}, ErrConflict
	}
	t, st, err := loadTask(tx, attempt.OrgID, attempt.TaskID, false)
	if err != nil {
		return empty, time.Time{}, err
	}
	if err := requireTaskRuntimeTx(tx, st); err != nil {
		return empty, time.Time{}, err
	}
	if err := requireOperationRuntime(st, claim); err != nil {
		return empty, time.Time{}, err
	}
	var current Operation
	if err := tx.Where("org_id = ? AND id = ? AND resource_id = ?", claim.OrgID, claim.ID, claim.ResourceID).Take(&current).Error; err != nil {
		return empty, time.Time{}, err
	}
	if err := requireOperationRuntime(st, current); err != nil {
		return empty, time.Time{}, err
	}
	if current.Status != "in_flight" || current.Version != claim.Version || current.AttemptID != claim.AttemptID || current.Kind != claim.Kind || current.PayloadRef != attempt.InputPayloadRef || current.LeaseUntil == nil || !current.LeaseUntil.After(s.Now().UTC()) || t.UpstreamTaskID != *attempt.TargetUpstreamTaskID || st.ProviderScopeID != attempt.ProviderScopeID || st.CredentialSnapshotID != attempt.CredentialSnapshotID || st.BillingSnapshotID != attempt.BillingSnapshotID || stateRuntime(st) != actionAttemptContract(attempt).Contract || st.CaptureHistory != attempt.CaptureHistory || st.RetentionDays != attempt.RetentionDays {
		return empty, time.Time{}, ErrConflict
	}
	if claim.Kind == "cancel_task" && t.Status != "queued" || claim.Kind == "delete_task" && (!terminal(t.Status) || !financialFinal(st.BillingState) || st.DeletionOperationID != claim.ID) {
		return empty, time.Time{}, ErrState
	}
	var binding UpstreamBinding
	if err := tx.Where("provider_scope_id = ? AND upstream_task_id = ? AND OCTET_LENGTH(upstream_task_id) = ?", attempt.ProviderScopeID, t.UpstreamTaskID, len(t.UpstreamTaskID)).Take(&binding).Error; err != nil {
		return empty, time.Time{}, err
	}
	if binding.OrgID != claim.OrgID || binding.TaskID != claim.ResourceID || binding.UpstreamTaskID != t.UpstreamTaskID {
		return empty, time.Time{}, ErrConflict
	}
	var credential CredentialSnapshot
	if err := tx.Select("id", "org_id", "task_id", "provider_scope_id").Where("id = ? AND org_id = ? AND task_id = ? AND provider_scope_id = ?", attempt.CredentialSnapshotID, attempt.OrgID, attempt.TaskID, attempt.ProviderScopeID).Take(&credential).Error; err != nil {
		return empty, time.Time{}, err
	}
	if err := s.validateMutationAttemptInputTx(tx, attempt); err != nil {
		return empty, time.Time{}, err
	}
	identity, err := invocation.ReadVideoActionInputIdentityTx(tx, attempt.OrgID, attempt.InputPayloadRef)
	if err != nil {
		return empty, time.Time{}, err
	}
	var scope ProviderScope
	if err := tx.Where("id = ?", attempt.ProviderScopeID).Take(&scope).Error; err != nil {
		return empty, time.Time{}, err
	}
	return actionInput(t, st, current, scope), identity.ExpiresAt, nil
}

func (s *Service) readMutationAction(ctx context.Context, op Operation, attempt ExecutionAttempt, credential string) (mutationActionInput, error) {
	var expected mutationActionInput
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		expected, _, err = s.mutationSendIdentityTx(tx, op, attempt)
		return err
	})
	if err != nil {
		return mutationActionInput{}, err
	}
	body, err := s.Evidence.ReadForActionExecution(ctx, op.OrgID, op.PayloadRef, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "video-dispatch", ReasonCode: "dispatch", OperationID: op.ID})
	if err != nil {
		return mutationActionInput{}, err
	}
	input, err := decodeMutationAction(body)
	if err != nil {
		return mutationActionInput{}, err
	}
	if strings.TrimSpace(credential) == "" || input != expected || input.TargetUpstreamTaskID == "" || responseIdentifier(input.TargetUpstreamTaskID, credential, 128) != input.TargetUpstreamTaskID {
		return mutationActionInput{}, ErrConflict
	}
	return input, nil
}

func decodeMutationAction(body []byte) (mutationActionInput, error) {
	var input mutationActionInput
	if len(body) == 0 || len(body) > 16<<10 || !utf8.Valid(body) {
		return input, ErrInvalid
	}
	fields := map[string]bool{}
	for _, field := range strings.Fields("schema_version action task_id operation_id target_upstream_task_id provider_scope_id provider_account_ref provider_region provider_project credential_snapshot_id billing_snapshot_id provider_base_url upstream_model runtime_version adapter_version protocol_contract_version billing_schema_version") {
		fields[field] = false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return input, ErrInvalid
	}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return input, ErrInvalid
		}
		name, ok := token.(string)
		seen, exists := fields[name]
		if !ok || !exists || seen {
			return input, ErrInvalid
		}
		fields[name] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return input, ErrInvalid
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return input, ErrInvalid
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return input, ErrInvalid
	}
	for _, seen := range fields {
		if !seen {
			return input, ErrInvalid
		}
	}
	if err := json.Unmarshal(body, &input); err != nil || input.SchemaVersion != invocation.VideoActionInputSchemaVersion {
		return mutationActionInput{}, ErrInvalid
	}
	return input, nil
}

func (s *Service) saveMutationNotSent(ctx context.Context, op Operation, attempt ExecutionAttempt, credential string) error {
	if err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, _, err := s.mutationSendIdentityTx(tx, op, attempt)
		return err
	}); err != nil {
		return err
	}
	receipt, err := s.saveMutationResponse(ctx, attempt, ark.Result{Action: ark.ActionDelete, Outcome: ark.OutcomeNotSent, Error: &ark.Error{Code: "execution_unavailable"}}, credential)
	if err != nil {
		return err
	}
	return s.applyClaimedMutationInbox(ctx, op, receipt.ReceiptID)
}

func (s *Service) mutationOperationMaySend(ctx context.Context, op Operation, attempt ExecutionAttempt) (bool, error) {
	deny := func(err error) (bool, error) { s.observeGuard(op.Kind, false, err); return false, err }
	if s.DispatchPaused() {
		return deny(ErrDispatchPaused)
	}
	var input mutationActionInput
	var expires time.Time
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		input, expires, err = s.mutationSendIdentityTx(tx, op, attempt)
		return err
	})
	if err != nil {
		return deny(err)
	}
	if !expires.After(s.Now().UTC()) || op.LeaseUntil == nil || !op.LeaseUntil.After(s.Now().UTC()) {
		return deny(ErrConflict)
	}
	// Metadata reads may block. This fresh query and clock check happen last;
	// only this captured attempt can pass, never an arbitrary registered row.
	query := supportedMutationInboxRuntime(s.DB.WithContext(ctx).Table("video_operations AS o").
		Joins("JOIN video_task_states v ON v.org_id = o.org_id AND v.task_id = o.resource_id").
		Joins("JOIN tasks t ON t.org_id = o.org_id AND t.task_id = o.resource_id").
		Joins("JOIN video_execution_attempts ea ON ea.org_id = o.org_id AND ea.operation_id = o.id AND ea.attempt_id = o.attempt_id").
		Joins("JOIN video_upstream_bindings ub ON ub.provider_scope_id = v.provider_scope_id AND ub.upstream_task_id = t.upstream_task_id AND ub.org_id = t.org_id AND ub.task_id = t.task_id").
		Joins("JOIN video_provider_scopes ps ON ps.id = v.provider_scope_id").
		Joins("JOIN video_credential_snapshots cs ON cs.id = v.credential_snapshot_id AND cs.org_id = v.org_id AND cs.task_id = v.task_id AND cs.provider_scope_id = v.provider_scope_id")).
		Where("o.org_id = ? AND o.id = ? AND o.resource_id = ? AND o.kind = ? AND o.status = ? AND o.version = ? AND o.attempt_id = ? AND o.lease_until > ? AND v.deleted_at IS NULL AND t.provider = ?", op.OrgID, op.ID, op.ResourceID, op.Kind, "in_flight", op.Version, op.AttemptID, s.Now().UTC(), Provider).
		Where("ea.attempt_id = ? AND ea.operation_claim_version = ? AND ea.input_identity_hmac = ? AND ea.input_payload_ref = ? AND ea.target_upstream_task_id = ? AND t.upstream_task_id = ? AND OCTET_LENGTH(t.upstream_task_id) = ? AND OCTET_LENGTH(ub.upstream_task_id) = ?", attempt.AttemptID, attempt.OperationClaimVersion, attempt.InputIdentityHMAC, attempt.InputPayloadRef, input.TargetUpstreamTaskID, input.TargetUpstreamTaskID, len(input.TargetUpstreamTaskID), len(input.TargetUpstreamTaskID)).
		Where("OCTET_LENGTH(ea.target_upstream_task_id) = ? AND OCTET_LENGTH(ea.input_payload_ref) = ? AND OCTET_LENGTH(ea.input_identity_hmac) = ? AND OCTET_LENGTH(ea.attempt_id) = ? AND OCTET_LENGTH(o.attempt_id) = ?", len(input.TargetUpstreamTaskID), len(attempt.InputPayloadRef), len(attempt.InputIdentityHMAC), len(attempt.AttemptID), len(op.AttemptID)).
		Where("OCTET_LENGTH(o.id) = ? AND OCTET_LENGTH(o.resource_id) = ? AND OCTET_LENGTH(ea.operation_id) = ? AND OCTET_LENGTH(ea.task_id) = ? AND OCTET_LENGTH(t.task_id) = ? AND OCTET_LENGTH(v.task_id) = ? AND OCTET_LENGTH(ub.task_id) = ?", len(op.ID), len(op.ResourceID), len(op.ID), len(op.ResourceID), len(op.ResourceID), len(op.ResourceID), len(op.ResourceID)).
		Where("v.provider_scope_id = ? AND v.credential_snapshot_id = ? AND v.billing_snapshot_id = ? AND v.provider_base_url = ? AND v.upstream_model = ? AND ps.account_ref = ? AND ps.region = ? AND ps.project_name = ? AND v.capture_history = ? AND v.retention_days = ?", input.ProviderScopeID, input.CredentialSnapshotID, input.BillingSnapshotID, input.ProviderBaseURL, input.UpstreamModel, input.ProviderAccountRef, input.ProviderRegion, input.ProviderProject, attempt.CaptureHistory, attempt.RetentionDays).
		Where("OCTET_LENGTH(v.provider_base_url) = ? AND OCTET_LENGTH(v.upstream_model) = ? AND OCTET_LENGTH(ps.account_ref) = ? AND OCTET_LENGTH(ps.region) = ? AND OCTET_LENGTH(ps.project_name) = ?", len(input.ProviderBaseURL), len(input.UpstreamModel), len(input.ProviderAccountRef), len(input.ProviderRegion), len(input.ProviderProject))
	if op.Kind == "cancel_task" {
		query = query.Where("t.status = ?", "queued")
	} else {
		query = query.Where("t.status IN ? AND v.billing_state IN ? AND v.deletion_operation_id = ? AND OCTET_LENGTH(v.deletion_operation_id) = ?", []string{"succeeded", "failed", "cancelled", "expired"}, []string{"settled", "released", "adjusted"}, op.ID, len(op.ID))
	}
	var count int64
	err = query.Count(&count).Error
	if err != nil {
		return deny(err)
	}
	if s.DispatchPaused() {
		return deny(ErrDispatchPaused)
	}
	if !op.LeaseUntil.After(s.Now().UTC()) || !expires.After(s.Now().UTC()) {
		return deny(ErrConflict)
	}
	s.observeGuard(op.Kind, count == 1, nil)
	return count == 1, nil
}
