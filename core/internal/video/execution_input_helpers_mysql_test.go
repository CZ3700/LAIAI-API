package video

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func executionFixtureNow(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	now, err := authorityFixtureNow(db)
	must(t, err)
	return now
}

// All expiry facts are chosen BEFORE encryption. This is the generic N-1
// writer, not a test override of the new factory's mandatory DB + 24h policy.
func executionOldPayload(t *testing.T, f *fixture, requestID, attemptID string, schema uint32, body []byte, expires time.Time) *invocation.PreparedVideoPayload {
	t.Helper()
	old, err := invocation.NewVideoStore(f.s.DB, f.s.Keyring, invocation.VideoStoreOptions{Now: func() time.Time { return expires.Add(-24 * time.Hour) }})
	must(t, err)
	p, err := old.Prepare(invocation.VideoPayloadInput{OrgID: f.key.OrgID, RequestID: requestID, AttemptID: attemptID,
		Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeExecution, SchemaVersion: schema,
		Body: body, CaptureEnabled: true, ExpiresAt: expires})
	must(t, err)
	return p
}

func executionOldStage(t *testing.T, f *fixture, tx *gorm.DB, p *invocation.PreparedVideoPayload, expires time.Time) (string, error) {
	t.Helper()
	old, err := invocation.NewVideoStore(f.s.DB, f.s.Keyring, invocation.VideoStoreOptions{Now: func() time.Time { return expires.Add(-24 * time.Hour) }})
	if err != nil {
		return "", err
	}
	return old.StageTx(tx, p)
}

// Before any claim/EA exists, establish an accepted legacy input and its exact
// request identity. The original new-factory capsule remains untouched. This
// fixture is not proof of a new-factory write or a production rebinding API.
func executionOldCreateInput(t *testing.T, f *fixture, id string, expires time.Time) invocation.VideoExecutionIdentity {
	t.Helper()
	var out invocation.VideoExecutionIdentity
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		row, st, err := loadTask(tx, f.key.OrgID, id, true)
		if err != nil {
			return err
		}
		var op Operation
		if err := locked(tx).Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, id, "create_task").Take(&op).Error; err != nil {
			return err
		}
		if row.Status != "accepted" || op.Status != "pending" {
			return ErrState
		}
		if err := requireNoExecutionAttemptTx(tx, op.OrgID, op.ID); err != nil {
			return err
		}
		body, err := f.s.Evidence.ReadForExecutionSchema(context.Background(), f.key.OrgID, op.PayloadRef, 1,
			invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "old-writer-fixture", ReasonCode: "dispatch", OperationID: op.ID})
		if err != nil {
			return err
		}
		requestID, err := randomID("")
		if err != nil {
			return err
		}
		p := executionOldPayload(t, f, requestID, "", 1, body, expires)
		ref, err := executionOldStage(t, f, tx, p, expires)
		if err != nil {
			return err
		}
		if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", st.OrgID, st.TaskID).Updates(map[string]any{"create_request_id": requestID, "effective_payload_ref": ref}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND status = ?", op.OrgID, op.ID, "pending").Update("payload_ref", ref).Error; err != nil {
			return err
		}
		out, err = invocation.ReadVideoExecutionIdentityTx(tx, f.key.OrgID, ref, 1)
		return err
	}))
	return out
}

// N-1 schema2 writer, distilled from 9a6fd942 registerMutationAttemptTx. It
// stages through the generic old API and computes the FIRST immutable identity
// HMAC in the same claim transaction. It never updates an existing EA/input.
// New producer/factory atomicity is exercised separately through production.
func executionOldMutationClaim(t *testing.T, m *mutationConsumerFixture, action string, expires time.Time) (ExecutionAttempt, Operation) {
	t.Helper()
	op := m.pending(t, action)
	var err error
	op.AttemptID, err = randomID("att_")
	must(t, err)
	op.Version++
	op.Status = "in_flight"
	row, st := m.rows(t)
	var scope ProviderScope
	must(t, m.s.DB.Where("id = ?", st.ProviderScopeID).Take(&scope).Error)
	input := actionInput(row, st, op, scope)
	body, err := json.Marshal(input)
	must(t, err)
	p := executionOldPayload(t, m.fixture, op.ID, op.AttemptID, 2, body, expires)
	var a ExecutionAttempt
	must(t, m.s.DB.Transaction(func(tx *gorm.DB) error {
		currentTask, fixed, err := loadTask(tx, op.OrgID, op.ResourceID, true)
		if err != nil {
			return err
		}
		var current Operation
		if err := locked(tx).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Take(&current).Error; err != nil {
			return err
		}
		if current.Status != "pending" || current.Version+1 != op.Version || current.PayloadRef != "" || actionInput(currentTask, fixed, op, scope) != input {
			return ErrConflict
		}
		if err := requireNoExecutionAttemptTx(tx, op.OrgID, op.ID); err != nil {
			return err
		}
		if err := requireTaskRuntimeTx(tx, fixed); err != nil {
			return err
		}
		if _, err := videoruntime.ResolveActionContract(mutationContract(fixed, action)); err != nil {
			return err
		}
		if action == "cancel_task" && currentTask.Status != "queued" || action == "delete_task" && (!terminal(currentTask.Status) || !financialFinal(fixed.BillingState) || fixed.DeletionOperationID != op.ID) {
			return ErrState
		}
		var binding UpstreamBinding
		if err := tx.Where("provider_scope_id = ? AND upstream_task_id = ?", fixed.ProviderScopeID, currentTask.UpstreamTaskID).Take(&binding).Error; err != nil {
			return err
		}
		if binding.OrgID != op.OrgID || binding.TaskID != op.ResourceID || binding.UpstreamTaskID != input.TargetUpstreamTaskID {
			return ErrConflict
		}
		var credential CredentialSnapshot
		if err := tx.Select("id", "org_id", "task_id", "provider_scope_id").Where("id = ? AND org_id = ? AND task_id = ? AND provider_scope_id = ?", fixed.CredentialSnapshotID, fixed.OrgID, fixed.TaskID, fixed.ProviderScopeID).Take(&credential).Error; err != nil {
			return err
		}
		if err := m.s.validateResponseHashVersionTx(tx, "v1"); err != nil {
			return err
		}
		ref, err := executionOldStage(t, m.fixture, tx, p, expires)
		if err != nil {
			return err
		}
		identity, err := invocation.ReadVideoActionInputIdentityTx(tx, op.OrgID, ref)
		if err != nil {
			return err
		}
		if identity.RequestID != op.ID || identity.AttemptID != op.AttemptID {
			return ErrConflict
		}
		now, err := authorityFixtureNow(tx)
		if err != nil {
			return err
		}
		lease := now.Add(3 * time.Minute)
		op.LeaseUntil, op.PayloadRef = &lease, ref
		if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND status = ?", current.OrgID, current.ID, current.Version, "pending").Updates(map[string]any{"status": op.Status, "version": op.Version, "attempt_id": op.AttemptID, "lease_until": lease, "payload_ref": ref}).Error; err != nil {
			return err
		}
		target := input.TargetUpstreamTaskID
		a = ExecutionAttempt{AttemptID: op.AttemptID, OrgID: op.OrgID, TaskID: op.ResourceID, OperationID: op.ID, Action: action,
			StorageProtocol: videoruntime.ActionStorageProtocolV1, TargetUpstreamTaskID: &target, ProviderScopeID: fixed.ProviderScopeID,
			CredentialSnapshotID: fixed.CredentialSnapshotID, BillingSnapshotID: fixed.BillingSnapshotID, InputPayloadRef: ref,
			InputIdentityHMAC: m.s.mutationInputHMAC(identity, input), InputHMACVersion: "v1", EvidenceHMACVersion: "v1",
			RuntimeVersion: fixed.RuntimeVersion, AdapterVersion: fixed.AdapterVersion, ProtocolContractVersion: fixed.ProtocolContractVersion, BillingSchemaVersion: fixed.BillingSchemaVersion,
			ExecutionSchemaVersion: 2, ObservationSchemaVersion: 1, OperationClaimVersion: op.Version, CaptureHistory: fixed.CaptureHistory, RetentionDays: fixed.RetentionDays, RegisteredAt: now}
		return tx.Create(&a).Error
	}))
	must(t, m.s.DB.Transaction(func(tx *gorm.DB) error { return m.s.validateMutationAttemptInputTx(tx, a) }))
	return a, m.operation(t, op.ID)
}

func executionWaitExpired(t *testing.T, db *gorm.DB, expires time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if !expires.After(executionFixtureNow(t, db)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("legitimate old capsule did not naturally expire within the bounded fixture wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func executionCreateRows(t *testing.T, f *fixture, id string) (task.Task, State, Operation) {
	t.Helper()
	row, st, err := loadTask(f.s.DB, f.key.OrgID, id, false)
	must(t, err)
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, id, "create_task").Take(&op).Error)
	return row, st, op
}
