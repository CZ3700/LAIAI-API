package video

import (
	"context"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Compatibility diagnostics are deliberately separate from normal progress:
// an unsupported task never authorizes JSON interpretation or schedule changes.
type queryCompatibilityAttempt struct {
	identity   queryRecoveryAttempt
	contract   videoruntime.Contract
	snapshotID uint64
}

func knownQueryReceiptFormat(row QueryReceipt) bool {
	c := videoruntime.LegacyV1()
	return row.RuntimeVersion == c.RuntimeVersion && row.AdapterVersion == c.AdapterVersion && row.ProtocolContractVersion == c.ProtocolContractVersion && row.ObservationSchemaVersion == videoruntime.ObservationSchemaV1
}

// Caller has already obtained the current Task and State locks, and observed a
// pure Unsupported from their contract check. Never derive this token from the
// caller's in-memory receipt, which may predate another consumer's progress.
func observeQueryCompatibilityTx(tx *gorm.DB, row task.Task, state State, requested QueryReceipt) (queryCompatibilityAttempt, error) {
	if row.OrgID != requested.OrgID || row.TaskID != requested.TaskID || state.OrgID != requested.OrgID || state.TaskID != requested.TaskID {
		return queryCompatibilityAttempt{}, ErrConflict
	}
	var current QueryReceipt
	if err := locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", requested.OrgID, requested.TaskID, requested.AttemptID).Take(&current).Error; err != nil {
		return queryCompatibilityAttempt{}, err
	}
	if err := tx.Statement.Context.Err(); err != nil {
		return queryCompatibilityAttempt{}, err
	}
	if current.OrgID != requested.OrgID || current.TaskID != requested.TaskID || current.AttemptID != requested.AttemptID {
		return queryCompatibilityAttempt{}, ErrConflict
	}
	if !knownQueryReceiptFormat(current) || current.AppliedAt != nil || current.RecoveryDueAt == nil {
		return queryCompatibilityAttempt{}, nil
	}
	identity, err := observeQueryRecovery(current)
	return queryCompatibilityAttempt{identity: identity, contract: stateRuntime(state), snapshotID: state.BillingSnapshotID}, err
}

func (s *Service) recordQueryCompatibilityFailure(ctx context.Context, observed queryCompatibilityAttempt, cause error) (queryRecoveryResult, error) {
	result := queryRecoveryResult{disposition: queryRecoveryNoop}
	if err := ctx.Err(); err != nil {
		return result, safeRecoveryErrors(err)
	}
	if !observed.identity.valid || !recoveryOnlyErrors(cause, videoruntime.ErrUnsupported) {
		return result, nil
	}
	phase, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := s.DB.Session(&gorm.Session{NewDB: true, Context: phase, Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		id := observed.identity
		row, state, contractErr := LockBillingTaskTx(tx, id.orgID, id.taskID)
		if contractErr == nil {
			return nil // A supported current task no longer needs the old label.
		}
		if !recoveryOnlyErrors(safeRecoveryErrors(contractErr, phase.Err()), videoruntime.ErrUnsupported) {
			return safeRecoveryErrors(contractErr, phase.Err())
		}
		if row.OrgID != id.orgID || row.TaskID != id.taskID || state.OrgID != id.orgID || state.TaskID != id.taskID || stateRuntime(state) != observed.contract || state.BillingSnapshotID != observed.snapshotID {
			return ErrConflict
		}
		var current QueryReceipt
		if err := locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", id.orgID, id.taskID, id.attemptID).Take(&current).Error; err != nil {
			return err
		}
		if err := phase.Err(); err != nil {
			return err
		}
		if current.OrgID != id.orgID || current.TaskID != id.taskID || current.AttemptID != id.attemptID {
			return ErrConflict
		}
		if !knownQueryReceiptFormat(current) || current.AppliedAt != nil || current.RecoveryDueAt == nil || current.RecoveryVersion != id.version {
			return nil
		}
		digest, err := queryEvidenceDigest(current)
		if err != nil {
			return err
		}
		if digest != id.evidence {
			return ErrConflict
		}
		if current.ApplyErrorCode == "runtime_contract_unsupported" {
			return nil
		}
		write := tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL", id.orgID, id.taskID, id.attemptID, id.version).Update("apply_error_code", "runtime_contract_unsupported")
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return ErrConflict
		}
		// Only the legacy label was committed: this counts diagnostic waiting,
		// not a new retry deadline/version or a permanent isolation.
		result = queryRecoveryResult{disposition: queryRecoveryDeferred, code: "runtime_contract_unsupported"}
		return nil
	})
	if err != nil {
		return queryRecoveryResult{disposition: queryRecoveryFailed}, safeRecoveryErrors(err, phase.Err())
	}
	return result, nil
}
