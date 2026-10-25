package video

import (
	"context"
	"crypto/hmac"
	"errors"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/dberr"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// The caller establishes the claim in its Task -> State -> Operation
// transaction. Input staging, the operation's input reference and registration
// commit together. No production producer calls this consumer-only entry yet.
func (s *Service) registerMutationAttemptTx(tx *gorm.DB, t task.Task, st State, op Operation, p *preparedMutationInput) (ExecutionAttempt, error) {
	var out ExecutionAttempt
	if err := responseTransaction(tx); err != nil {
		return out, err
	}
	if s == nil || p == nil || p.payload == nil || p.orgID != t.OrgID || p.attemptID != op.AttemptID || p.claimVersion != op.Version || !hmac.Equal([]byte(p.seal), []byte(s.mutationPreparationSeal(p))) {
		return out, ErrConflict
	}
	currentTask, fixed, err := loadTask(tx, t.OrgID, t.TaskID, true)
	if err != nil {
		return out, err
	}
	if currentTask.OrgID != st.OrgID || st.OrgID != op.OrgID || currentTask.TaskID != st.TaskID || st.TaskID != op.ResourceID || currentTask.Provider != Provider || currentTask.UpstreamTaskID != t.UpstreamTaskID || stateRuntime(st) != stateRuntime(fixed) || st.ProviderScopeID != fixed.ProviderScopeID || st.CredentialSnapshotID != fixed.CredentialSnapshotID || st.BillingSnapshotID != fixed.BillingSnapshotID || st.ProviderBaseURL != fixed.ProviderBaseURL || st.UpstreamModel != fixed.UpstreamModel || st.CaptureHistory != fixed.CaptureHistory || st.RetentionDays != fixed.RetentionDays || fixed.RetentionDays < 1 || fixed.RetentionDays > 30 {
		return out, ErrConflict
	}
	if _, err := videoruntime.ResolveActionContract(mutationContract(fixed, op.Kind)); err != nil {
		return out, err
	}
	if err := requireTaskRuntimeTx(tx, fixed); err != nil {
		return out, err
	}
	if err := requireOperationRuntime(fixed, op); err != nil {
		return out, err
	}
	var current Operation
	if err := locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", op.OrgID, op.ID, op.ResourceID).Take(&current).Error; err != nil {
		return out, err
	}
	if err := requireOperationRuntime(fixed, current); err != nil {
		return out, err
	}
	if current.Status != "in_flight" || current.Version != op.Version || current.AttemptID != op.AttemptID || current.Kind != op.Kind || current.LeaseUntil == nil || !current.LeaseUntil.After(s.Now().UTC()) || current.PayloadRef != "" && current.PayloadRef != p.payload.PayloadID() {
		return out, ErrConflict
	}
	if current.Kind == "cancel_task" && currentTask.Status != "queued" || current.Kind == "delete_task" && (!terminal(currentTask.Status) || !financialFinal(fixed.BillingState) || fixed.DeletionOperationID != op.ID) {
		return out, ErrState
	}
	if currentTask.UpstreamTaskID == "" || responseIdentifier(currentTask.UpstreamTaskID, "", 128) != currentTask.UpstreamTaskID {
		return out, ErrConflict
	}
	var binding UpstreamBinding
	if err := tx.Where("provider_scope_id = ? AND upstream_task_id = ? AND OCTET_LENGTH(upstream_task_id) = ?", fixed.ProviderScopeID, currentTask.UpstreamTaskID, len(currentTask.UpstreamTaskID)).Take(&binding).Error; err != nil {
		return out, err
	}
	if binding.OrgID != fixed.OrgID || binding.TaskID != fixed.TaskID || binding.UpstreamTaskID != currentTask.UpstreamTaskID {
		return out, ErrConflict
	}
	var scope ProviderScope
	if err := tx.Where("id = ?", fixed.ProviderScopeID).Take(&scope).Error; err != nil {
		return out, err
	}
	if p.input != actionInput(currentTask, fixed, current, scope) {
		return out, ErrConflict
	}
	var credential CredentialSnapshot
	if err := tx.Select("id", "org_id", "task_id", "provider_scope_id").Where("id = ? AND org_id = ? AND task_id = ? AND provider_scope_id = ?", fixed.CredentialSnapshotID, fixed.OrgID, fixed.TaskID, fixed.ProviderScopeID).Take(&credential).Error; err != nil {
		return out, err
	}
	if err := s.validateResponseHashVersionTx(tx, "v1"); err != nil {
		return out, err
	}
	ref, err := s.Evidence.StageTx(tx, p.payload)
	if err != nil {
		return out, err
	}
	if ref != p.payload.PayloadID() {
		return out, ErrConflict
	}
	identity, err := invocation.ReadVideoActionInputIdentityTx(tx, fixed.OrgID, ref)
	if err != nil {
		return out, err
	}
	if identity.RequestID != current.ID || identity.AttemptID != current.AttemptID {
		return out, ErrConflict
	}
	now, err := responseDatabaseTimeTx(tx)
	if err != nil {
		return out, err
	}
	target := currentTask.UpstreamTaskID
	out = ExecutionAttempt{AttemptID: op.AttemptID, OrgID: fixed.OrgID, TaskID: fixed.TaskID, OperationID: op.ID, Action: op.Kind,
		StorageProtocol: videoruntime.ActionStorageProtocolV1, TargetUpstreamTaskID: &target, ProviderScopeID: fixed.ProviderScopeID,
		CredentialSnapshotID: fixed.CredentialSnapshotID, BillingSnapshotID: fixed.BillingSnapshotID, InputPayloadRef: ref,
		InputIdentityHMAC: s.mutationInputHMAC(identity, p.input), InputHMACVersion: "v1", EvidenceHMACVersion: "v1",
		RuntimeVersion: fixed.RuntimeVersion, AdapterVersion: fixed.AdapterVersion, ProtocolContractVersion: fixed.ProtocolContractVersion, BillingSchemaVersion: fixed.BillingSchemaVersion,
		ExecutionSchemaVersion: 2, ObservationSchemaVersion: 1, OperationClaimVersion: op.Version, CaptureHistory: fixed.CaptureHistory, RetentionDays: fixed.RetentionDays, RegisteredAt: now}
	if err := tx.Create(&out).Error; err != nil {
		if !dberr.IsDuplicate(err) && !errors.Is(err, gorm.ErrDuplicatedKey) {
			return ExecutionAttempt{}, err
		}
		var previous ExecutionAttempt
		if err := locked(tx).Where("org_id = ? AND attempt_id = ?", out.OrgID, out.AttemptID).Take(&previous).Error; err != nil {
			return ExecutionAttempt{}, ErrConflict
		}
		out.RegisteredAt = previous.RegisteredAt
		if !sameExecutionAttempt(previous, out) {
			return ExecutionAttempt{}, ErrConflict
		}
		out = previous
	}
	if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND attempt_id = ?", op.OrgID, op.ID, op.Version, op.AttemptID).Update("payload_ref", ref).Error; err != nil {
		return ExecutionAttempt{}, err
	}
	return out, nil
}

func (s *Service) prepareMutationResponse(attempt ExecutionAttempt, result ark.Result, credential string) (*preparedResponseReceipt, error) {
	if result.Action != ark.ActionDelete || attempt.TargetUpstreamTaskID == nil || *attempt.TargetUpstreamTaskID == "" || responseIdentifier(*attempt.TargetUpstreamTaskID, credential, 128) != *attempt.TargetUpstreamTaskID || (attempt.Action != "cancel_task" && attempt.Action != "delete_task") {
		return nil, ErrInvalid
	}
	return s.prepareCapturedResponse(attempt, result, credential, true)
}

// Saving an already observed response does not depend on a producer flag or
// claim authority. Interpretation and application happen separately.
func (s *Service) saveMutationResponse(ctx context.Context, attempt ExecutionAttempt, result ark.Result, credential string) (ResponseInbox, error) {
	return s.savePreparedResponse(ctx, func() (*preparedResponseReceipt, error) {
		return s.prepareMutationResponse(attempt, result, credential)
	})
}
