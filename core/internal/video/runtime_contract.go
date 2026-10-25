package video

import (
	"context"
	"errors"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// Recheck after local preparation and before a query. This is a compatibility
// gate, not an atomic HTTP fence; the response still records its captured
// producer contract if the task changes while the HTTP call is in flight.
func (s *Service) requireCurrentTaskRuntime(ctx context.Context, captured State) error {
	if err := requireStateRuntime(captured); err != nil {
		return err
	}
	var count int64
	err := supportedTaskRuntime(s.DB.WithContext(ctx).Table("video_task_states AS v")).Where("v.org_id = ? AND v.task_id = ? AND v.billing_snapshot_id = ?", captured.OrgID, captured.TaskID, captured.BillingSnapshotID).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return videoruntime.ErrUnsupported
	}
	return nil
}

func stateRuntime(st State) videoruntime.Contract {
	return videoruntime.Contract{RuntimeVersion: st.RuntimeVersion, AdapterVersion: st.AdapterVersion, ProtocolContractVersion: st.ProtocolContractVersion, BillingSchemaVersion: st.BillingSchemaVersion}
}

func requireStateRuntime(st State) error {
	_, err := videoruntime.Resolve(stateRuntime(st))
	return err
}

// This check takes no Key or ledger locks. Every financial entrypoint also
// validates the immutable snapshot under its own established lock order.
func requireTaskRuntimeTx(tx *gorm.DB, st State) error {
	if err := requireStateRuntime(st); err != nil {
		return err
	}
	return videobilling.ValidateSnapshotContractTx(tx, st.OrgID, st.TaskID, st.BillingSnapshotID, st.BillingSchemaVersion)
}

func inheritOperationRuntime(st State, op *Operation) {
	op.RuntimeVersion, op.AdapterVersion, op.ProtocolContractVersion = st.RuntimeVersion, st.AdapterVersion, st.ProtocolContractVersion
}

func requireOperationRuntime(st State, op Operation) error {
	if err := requireStateRuntime(st); err != nil {
		return err
	}
	if op.OrgID != st.OrgID || op.ResourceID != st.TaskID || op.RuntimeVersion != st.RuntimeVersion || op.AdapterVersion != st.AdapterVersion || op.ProtocolContractVersion != st.ProtocolContractVersion {
		return videoruntime.ErrUnsupported
	}
	return nil
}

func requireQueryReceiptRuntime(st State, receipt QueryReceipt) error {
	d, err := videoruntime.Resolve(stateRuntime(st))
	if err != nil {
		return err
	}
	if receipt.OrgID != st.OrgID || receipt.TaskID != st.TaskID || receipt.RuntimeVersion != st.RuntimeVersion || receipt.AdapterVersion != st.AdapterVersion || receipt.ProtocolContractVersion != st.ProtocolContractVersion || receipt.ObservationSchemaVersion != d.ObservationSchemaVersion {
		return videoruntime.ErrUnsupported
	}
	return nil
}

// Aliases are internal constants, never request data. Apply these scopes before
// ranking/LIMIT so unsupported work cannot starve a supported contract's batch.
// The migration uses NO PAD, case-sensitive version columns to make SQL's
// comparison agree with Resolve (including empty/trailing-space values).
func supportedTaskRuntime(db *gorm.DB) *gorm.DB {
	c := videoruntime.LegacyV1()
	return videobilling.WithTaskSnapshotContract(db).
		Where("v.runtime_version = ? AND v.adapter_version = ? AND v.protocol_contract_version = ? AND v.billing_schema_version = ?", c.RuntimeVersion, c.AdapterVersion, c.ProtocolContractVersion, c.BillingSchemaVersion)
}

func supportedOperationRuntime(db *gorm.DB) *gorm.DB {
	return supportedTaskRuntime(db).Where("o.runtime_version = v.runtime_version AND o.adapter_version = v.adapter_version AND o.protocol_contract_version = v.protocol_contract_version")
}

func supportedQueryReceiptRuntime(db *gorm.DB) *gorm.DB {
	return supportedTaskRuntime(db).Where("r.runtime_version = v.runtime_version AND r.adapter_version = v.adapter_version AND r.protocol_contract_version = v.protocol_contract_version AND r.observation_schema_version = ?", videoruntime.ObservationSchemaV1)
}

// The operation's identity is immutable. Check its current locked row as well
// as the candidate before a claim/refund: candidates may be stale by then.
func requireCurrentOperationRuntimeTx(tx *gorm.DB, st State, candidate Operation) error {
	if err := requireTaskRuntimeTx(tx, st); err != nil {
		return err
	}
	if err := requireOperationRuntime(st, candidate); err != nil {
		return err
	}
	var current Operation
	if err := locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", candidate.OrgID, candidate.ID, candidate.ResourceID).Take(&current).Error; err != nil {
		return err
	}
	if err := requireOperationRuntime(st, current); err != nil {
		return err
	}
	if current.Kind != candidate.Kind || current.PayloadRef != candidate.PayloadRef {
		return ErrConflict
	}
	if current.Kind == "create_task" {
		return requireExecutionRuntimeTx(tx, st, current)
	}
	return nil
}

func requireExecutionRuntimeTx(tx *gorm.DB, st State, op Operation) error {
	if op.PayloadRef == "" || op.PayloadRef != st.EffectivePayloadRef {
		return videoruntime.ErrUnsupported
	}
	d, err := videoruntime.Resolve(stateRuntime(st))
	if err != nil {
		return err
	}
	err = invocation.ValidateVideoExecutionSchemaTx(tx, st.OrgID, op.PayloadRef, d.ExecutionInputSchemaVersion)
	if errors.Is(err, invocation.ErrVideoSchemaUnsupported) {
		return videoruntime.ErrUnsupported
	}
	return err
}
