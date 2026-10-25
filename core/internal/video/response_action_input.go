package video

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// This capsule contains only execution identity. In particular it cannot carry
// credentials, prompts, source media or result URLs. It has its own lifetime,
// independent of the original create payload's retention window.
type mutationActionInput struct {
	SchemaVersion           uint32 `json:"schema_version"`
	Action                  string `json:"action"`
	TaskID                  string `json:"task_id"`
	OperationID             string `json:"operation_id"`
	TargetUpstreamTaskID    string `json:"target_upstream_task_id"`
	ProviderScopeID         uint64 `json:"provider_scope_id"`
	ProviderAccountRef      string `json:"provider_account_ref"`
	ProviderRegion          string `json:"provider_region"`
	ProviderProject         string `json:"provider_project"`
	CredentialSnapshotID    uint64 `json:"credential_snapshot_id"`
	BillingSnapshotID       uint64 `json:"billing_snapshot_id"`
	ProviderBaseURL         string `json:"provider_base_url"`
	UpstreamModel           string `json:"upstream_model"`
	RuntimeVersion          string `json:"runtime_version"`
	AdapterVersion          string `json:"adapter_version"`
	ProtocolContractVersion string `json:"protocol_contract_version"`
	BillingSchemaVersion    string `json:"billing_schema_version"`
}

type preparedMutationInput struct {
	input        mutationActionInput
	orgID        uint64
	attemptID    string
	claimVersion uint64
	payload      *invocation.PreparedVideoPayload
	seal         string
}

func mutationContract(st State, action string) videoruntime.ActionContractKey {
	return videoruntime.ActionContractKey{StorageProtocol: videoruntime.ActionStorageProtocolV1, Action: action, Contract: stateRuntime(st),
		ExecutionInputSchemaVersion: videoruntime.ActionExecutionInputSchemaV2, ObservationSchemaVersion: 1, DeliverySchemaVersion: 0}
}

func actionInput(t task.Task, st State, op Operation, scope ProviderScope) mutationActionInput {
	return mutationActionInput{SchemaVersion: 2, Action: op.Kind, TaskID: t.TaskID, OperationID: op.ID, TargetUpstreamTaskID: t.UpstreamTaskID,
		ProviderScopeID: st.ProviderScopeID, ProviderAccountRef: scope.AccountRef, ProviderRegion: scope.Region, ProviderProject: scope.ProjectName,
		CredentialSnapshotID: st.CredentialSnapshotID, BillingSnapshotID: st.BillingSnapshotID, ProviderBaseURL: st.ProviderBaseURL, UpstreamModel: st.UpstreamModel,
		RuntimeVersion: st.RuntimeVersion, AdapterVersion: st.AdapterVersion, ProtocolContractVersion: st.ProtocolContractVersion, BillingSchemaVersion: st.BillingSchemaVersion}
}

func (s *Service) mutationPreparationSeal(p *preparedMutationInput) string {
	encoded, _ := json.Marshal(struct {
		Input   mutationActionInput
		Org     uint64
		Attempt string
		Claim   uint64
	}{p.input, p.orgID, p.attemptID, p.claimVersion})
	return digest(s.Secret, "prepared-action-input:v2", string(encoded), p.payload.PayloadID())
}

// Called outside all task/operation locks. Only a later transaction can accept
// this prepared input as the current operation's execution identity.
func (s *Service) prepareMutationInput(ctx context.Context, t task.Task, st State, op Operation, credential string) (*preparedMutationInput, error) {
	if s == nil || s.DB == nil || s.Evidence == nil || s.Now == nil || len(s.Secret) < 32 || ctx == nil || t.OrgID == 0 || t.OrgID != st.OrgID || t.OrgID != op.OrgID || t.TaskID != st.TaskID || t.TaskID != op.ResourceID || t.Provider != Provider || op.ID == "" || op.AttemptID == "" || op.Version == 0 || responseIdentifier(t.UpstreamTaskID, credential, 128) != t.UpstreamTaskID || t.UpstreamTaskID == "" {
		return nil, ErrInvalid
	}
	if _, err := videoruntime.ResolveActionContract(mutationContract(st, op.Kind)); err != nil {
		return nil, err
	}
	if err := requireOperationRuntime(st, op); err != nil {
		return nil, err
	}
	var scope ProviderScope
	if err := s.DB.WithContext(ctx).Where("id = ?", st.ProviderScopeID).Take(&scope).Error; err != nil {
		return nil, err
	}
	input := actionInput(t, st, op, scope)
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	payload, err := s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: t.OrgID, RequestID: op.ID, AttemptID: op.AttemptID,
		Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeExecution, SchemaVersion: invocation.VideoActionInputSchemaVersion,
		Body: body, ExpiresAt: s.Now().UTC().Add(24 * time.Hour), CaptureEnabled: true})
	if err != nil {
		return nil, err
	}
	p := &preparedMutationInput{input: input, orgID: t.OrgID, attemptID: op.AttemptID, claimVersion: op.Version, payload: payload}
	p.seal = s.mutationPreparationSeal(p)
	return p, nil
}

func (s *Service) mutationInputHMAC(identity invocation.VideoExecutionIdentity, input mutationActionInput) string {
	encoded, _ := json.Marshal(struct {
		Identity invocation.VideoExecutionIdentity
		Input    mutationActionInput
	}{identity, input})
	return digest(s.Secret, "action-execution-metadata-identity:v2", string(encoded))
}

// Validation only reads immutable Invocation metadata. Expiry or deletion of
// encrypted staging cannot prevent recovery, and never authorizes decryption.
func (s *Service) validateMutationAttemptInputTx(tx *gorm.DB, attempt ExecutionAttempt) error {
	if err := responseTransaction(tx); err != nil {
		return err
	}
	if attempt.TargetUpstreamTaskID == nil || *attempt.TargetUpstreamTaskID == "" {
		return ErrConflict
	}
	if err := s.validateResponseHashVersionTx(tx, attempt.InputHMACVersion); err != nil {
		return err
	}
	identity, err := invocation.ReadVideoActionInputIdentityTx(tx, attempt.OrgID, attempt.InputPayloadRef)
	if err != nil {
		return err
	}
	if identity.RequestID != attempt.OperationID || identity.AttemptID != attempt.AttemptID {
		return ErrConflict
	}
	var st State
	if err := tx.Where("org_id = ? AND task_id = ?", attempt.OrgID, attempt.TaskID).Take(&st).Error; err != nil {
		return err
	}
	var scope ProviderScope
	if err := tx.Where("id = ?", attempt.ProviderScopeID).Take(&scope).Error; err != nil {
		return err
	}
	input := actionInput(task.Task{TaskID: attempt.TaskID, UpstreamTaskID: *attempt.TargetUpstreamTaskID}, st, Operation{ID: attempt.OperationID, Kind: attempt.Action}, scope)
	if !hmac.Equal([]byte(attempt.InputIdentityHMAC), []byte(s.mutationInputHMAC(identity, input))) {
		return ErrConflict
	}
	return nil
}
