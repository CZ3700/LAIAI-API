package video

import (
	"context"
	"errors"
	"fmt"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// A lost lease/claim is a normal send denial, but it must remain a conflict
// for every local NotSent/refund caller. Identity and database errors do not
// carry this marker and cannot be mistaken for an ordinary stale sender.
var errCreateSenderClaimLost = fmt.Errorf("video: create sender claim lost: %w", ErrConflict)

// Identity is deliberately independent of capture/expiry. An old input can
// still prove a received response's origin, but cannot authorize another send.
func readCreateExecutionIdentityTx(tx *gorm.DB, st State, op Operation) (invocation.VideoExecutionIdentity, error) {
	var empty invocation.VideoExecutionIdentity
	if err := requireOperationRuntime(st, op); err != nil {
		return empty, err
	}
	if op.PayloadRef == "" || op.PayloadRef != st.EffectivePayloadRef {
		return empty, videoruntime.ErrUnsupported
	}
	if op.Kind != "create_task" || op.ID == "" || st.CreateRequestID == "" {
		return empty, ErrConflict
	}
	descriptor, err := videoruntime.Resolve(stateRuntime(st))
	if err != nil {
		return empty, err
	}
	identity, err := invocation.ReadVideoExecutionIdentityTx(tx, st.OrgID, op.PayloadRef, descriptor.ExecutionInputSchemaVersion)
	if err != nil {
		return empty, err
	}
	if identity.OrgID != st.OrgID || identity.PayloadID != op.PayloadRef || identity.RequestID != st.CreateRequestID || identity.AttemptID != "" {
		return empty, ErrConflict
	}
	return identity, nil
}

func requireCreateExecutionIdentityTx(tx *gorm.DB, t task.Task, st State, op Operation) (invocation.VideoExecutionIdentity, error) {
	if t.OrgID == 0 || t.OrgID != st.OrgID || t.TaskID != st.TaskID || t.Provider != Provider {
		return invocation.VideoExecutionIdentity{}, ErrConflict
	}
	return readCreateExecutionIdentityTx(tx, st, op)
}

// The local producer must still own its exact claim after identity reads. This
// is used before synthesizing NotSent; recovery of actual evidence uses only
// the metadata check above, never a live-input or live-sender requirement.
func (s *Service) requireCreateSenderIdentityTx(tx *gorm.DB, t task.Task, st State, op Operation) (invocation.VideoExecutionIdentity, error) {
	var empty invocation.VideoExecutionIdentity
	if err := requireCurrentOperationRuntimeTx(tx, st, op); err != nil {
		return empty, err
	}
	var current Operation
	if err := locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", op.OrgID, op.ID, op.ResourceID).Take(&current).Error; err != nil {
		return empty, err
	}
	if current.Kind != op.Kind || current.PayloadRef != op.PayloadRef {
		return empty, ErrConflict
	}
	if current.Status != "in_flight" || current.Version != op.Version || current.AttemptID == "" || current.AttemptID != op.AttemptID || current.LeaseUntil == nil {
		return empty, errCreateSenderClaimLost
	}
	identity, err := requireCreateExecutionIdentityTx(tx, t, st, current)
	if err != nil {
		return empty, err
	}
	var attempts []ExecutionAttempt
	if err := tx.Where("org_id = ? AND operation_id = ?", op.OrgID, op.ID).Limit(2).Find(&attempts).Error; err != nil {
		return empty, err
	}
	if len(attempts) > 1 {
		return empty, ErrConflict
	}
	if len(attempts) == 1 {
		a := attempts[0]
		if a.StorageProtocol != responseInboxProtocol || a.Action != "create_task" || a.ExecutionSchemaVersion != 1 || a.ObservationSchemaVersion != 1 || a.TargetUpstreamTaskID != nil {
			return empty, videoruntime.ErrUnsupported
		}
		if a.AttemptID != op.AttemptID || a.TaskID != t.TaskID || a.OperationClaimVersion != op.Version || a.InputPayloadRef != identity.PayloadID || a.ProviderScopeID != st.ProviderScopeID || a.CredentialSnapshotID != st.CredentialSnapshotID || a.BillingSnapshotID != st.BillingSnapshotID || a.RuntimeVersion != st.RuntimeVersion || a.AdapterVersion != st.AdapterVersion || a.ProtocolContractVersion != st.ProtocolContractVersion || a.BillingSchemaVersion != st.BillingSchemaVersion {
			return empty, ErrConflict
		}
		if err := s.validateExecutionAttemptInputTx(tx, a); err != nil {
			return empty, err
		}
	}
	now, err := authorityTimeTx(tx)
	if err != nil {
		return empty, err
	}
	if !current.LeaseUntil.After(now) {
		return empty, errCreateSenderClaimLost
	}
	return identity, nil
}

// Only known lifecycle/storage failures may become a local no-send, and their
// caller must separately prove immutable identity and exact claim. Error joins
// must not let audit/storage wrappers hide unknown schema or authority failure.
func executionReadAllowsNotSent(err error) bool {
	for _, blocked := range []error{context.Canceled, context.DeadlineExceeded, invocation.ErrVideoExecutionAuthorityUnavailable, invocation.ErrVideoSchemaUnsupported, invocation.ErrVideoAccessDenied, invocation.ErrVideoCorrupt, invocation.ErrVideoConflict, invocation.ErrVideoNotFound, invocation.ErrVideoInvalid, videoruntime.ErrUnsupported, ErrConflict} {
		if errors.Is(err, blocked) {
			return false
		}
	}
	return errors.Is(err, invocation.ErrVideoExpired) || errors.Is(err, invocation.ErrVideoRetentionDenied) || errors.Is(err, invocation.ErrVideoStorage) || errors.Is(err, invocation.ErrVideoAuditUnavailable)
}
