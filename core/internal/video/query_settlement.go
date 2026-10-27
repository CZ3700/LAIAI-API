package video

import (
	"context"
	"math"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// requested is only a locator. All authority, evidence and progress come from
// current locking reads. B never loads credentials, execution input or prices.
func (s *Service) applyQuerySettlement(ctx context.Context, requested QueryReceipt) (queryRecoveryResult, error) {
	result := queryRecoveryResult{disposition: queryRecoveryNoop}
	if requested.OrgID == 0 || requested.TaskID == "" || requested.AttemptID == "" {
		return result, safeRecoveryErrors(ErrInvalid)
	}
	var observed queryRecoveryAttempt
	db := s.DB.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard})
	err := db.Transaction(func(tx *gorm.DB) error {
		t, st, err := LockBillingTaskTx(tx, requested.OrgID, requested.TaskID)
		if err != nil {
			return err
		}
		var current QueryReceipt
		if err = locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", requested.OrgID, requested.TaskID, requested.AttemptID).Take(&current).Error; err != nil {
			return err
		}
		if t.OrgID != requested.OrgID || t.TaskID != requested.TaskID || st.OrgID != requested.OrgID || st.TaskID != requested.TaskID || current.OrgID != requested.OrgID || current.TaskID != requested.TaskID || current.AttemptID != requested.AttemptID {
			return ErrConflict
		}
		// A committed task marker must survive even subsequent format damage.
		if err = requireNoTaskSettlementConflictTx(tx, st); err != nil {
			return err
		}
		phase, err := queryPhaseOf(st, current)
		if err != nil {
			return err
		}
		if stateRuntime(st) != videoruntime.GenerationSettlementV2() {
			return videoruntime.ErrUnsupported
		}
		if phase == queryPhaseComplete {
			return nil
		}
		if phase == queryPhaseReview {
			return ErrConflict
		}
		if phase != queryPhaseSettlementV2 {
			return ErrState
		}
		eligibleAt, err := queryRecoveryTimeTx(tx)
		if err != nil {
			return err
		}
		if current.RecoveryDueAt.After(eligibleAt) {
			return nil
		}
		observed, err = observeQueryPhaseRecovery(t, st, current)
		if err != nil {
			return err
		}
		if err = requireNoCreateInboxReviewTx(tx, st.OrgID, st.TaskID); err != nil {
			return err
		}
		observation, err := readQuerySettlementEvidence(t, st, current)
		if err != nil {
			return err
		}
		initial, err := videobilling.ReadInitialSettlementTx(tx, videobilling.InitialSettlementRef{OrgID: st.OrgID, TaskID: st.TaskID, SnapshotID: st.BillingSnapshotID, BillingSchemaVersion: st.BillingSchemaVersion})
		if err != nil {
			return err
		}
		// The primary clock is read after the financial locks, not before a
		// possible Key/initial wait. It is not derived from Service.Now.
		now, err := queryRecoveryTimeTx(tx)
		if err != nil {
			return err
		}
		if current.RecoveryDueAt.After(now) {
			return nil
		}
		expected := videobilling.InitialSettlementExpectation{Kind: videobilling.InitialSettlementProviderNoChargeReleased}
		if current.SettlementKind == querySettlementUsage {
			expected.Kind, expected.CompletionTokens = videobilling.InitialSettlementUsageSettled, observation.CompletionTokens
		}
		relation, err := initial.Compare(expected)
		if err != nil {
			return err
		}
		if relation == videobilling.InitialSettlementRelationConflict {
			result, err = markQueryFinancialConflictTx(tx, st, current, now)
			return err
		}
		if relation == videobilling.InitialSettlementRelationAbsent {
			var settled videobilling.SettlementResult
			if current.SettlementKind == querySettlementUsage {
				settled, err = videobilling.SettleUsageTx(tx, videobilling.SettlementInput{OrgID: st.OrgID, TaskID: st.TaskID, Evidence: videobilling.UsageEvidence{
					OrgID: st.OrgID, TaskID: st.TaskID,
					EvidenceID: current.AttemptID, Source: "task_get", SourceRecordID: t.UpstreamTaskID, SourceVersion: current.AttemptID,
					CompletionTokens: observation.CompletionTokens, PayloadRef: current.PayloadRef, ObservedAt: current.ReceivedAt.UTC(),
				}, Now: now})
			} else {
				settled, err = videobilling.ReleaseConfirmedTx(tx, videobilling.ReleaseInput{OrgID: st.OrgID, TaskID: st.TaskID, EvidenceID: current.PayloadRef, Reason: videobilling.ReleaseProviderNoCharge, Confirmed: true, Now: now})
			}
			if err != nil {
				return err
			}
			// An absence/current-read mismatch is not permission to reconstruct
			// projections. Roll back this transaction, including any writer work.
			if settled.Replay || settled.Entry == nil {
				return videobilling.ErrInvariant
			}
			if err = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", st.OrgID, st.TaskID).Update("total_tokens", observation.TotalTokens).Error; err != nil {
				return err
			}
			st.ObservedCompletionTokens = observation.CompletionTokens
			if err = applyBillingSettlementLockedTx(tx, t, st, current.AttemptID, current.PayloadRef, settled, now); err != nil {
				return err
			}
			if current.SettlementKind == querySettlementUsage {
				if err = invocation.RecordTx(tx, &invocation.Invocation{OrgID: t.OrgID, UserID: t.UserID, APIKeyID: t.APIKeyID, RequestID: st.CreateRequestID, Model: t.Model, Modality: invocation.ModalityVideo, ChannelID: t.ChannelID, Provider: Provider, Status: invocation.StatusSuccess, StatusCode: 200, Attempts: 1, CompletionTokens: *observation.CompletionTokens, CostUSD: settled.Entry.CapturedUSD, UsageSource: invocation.UsageSourceUpstream, Protocol: "ark_v3", CreatedAt: now}); err != nil {
					return err
				}
			}
		} else if relation != videobilling.InitialSettlementRelationCompatible {
			return videobilling.ErrInvariant
		}
		q := queryPhaseCAS(tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at = ?", st.OrgID, st.TaskID, current.AttemptID, current.RecoveryVersion, *current.RecoveryDueAt), observed).
			Updates(map[string]any{"applied_at": now, "apply_error_code": "", "recovery_version": current.RecoveryVersion + 1})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return ErrConflict
		}
		result.disposition = queryRecoveryApplied
		return nil
	})
	if err == nil {
		if result.code == queryFinancialConflictCode {
			return result, safeRecoveryErrors(errQueryFinancialConflict)
		}
		return result, nil
	}
	// Nothing from a rolled back/uncertain transaction is a committed action.
	result = queryRecoveryResult{disposition: queryRecoveryFailed}
	err = safeRecoveryErrors(err, ctx.Err())
	if observed.valid {
		diagnostic, diagnosticErr := s.recordQueryRecoveryFailure(ctx, observed, err)
		return diagnostic, safeRecoveryErrors(err, diagnosticErr)
	}
	return result, err
}

// Called only after the caller has proved an incompatible V2 financial fact,
// while holding Task -> State -> current receipt. This is the one G/B writer
// for the task-wide barrier; ordinary SQL or generic conflicts cannot call it.
func markQueryFinancialConflictTx(tx *gorm.DB, st State, current QueryReceipt, now time.Time) (queryRecoveryResult, error) {
	result := queryRecoveryResult{disposition: queryRecoveryNoop}
	if err := responseTransaction(tx); err != nil {
		return result, err
	}
	if stateRuntime(st) != videoruntime.GenerationSettlementV2() || now.IsZero() || st.OrgID == 0 || st.TaskID == "" || current.OrgID != st.OrgID || current.TaskID != st.TaskID || current.AttemptID == "" {
		return result, ErrConflict
	}
	rows := []QueryReceipt{current}
	if st.SettlementReceiptID != nil && *st.SettlementReceiptID != current.AttemptID {
		if *st.SettlementReceiptID == "" {
			return result, ErrConflict
		}
		var pin QueryReceipt
		if err := locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", st.OrgID, st.TaskID, *st.SettlementReceiptID).Take(&pin).Error; err != nil {
			return result, err
		}
		if pin.OrgID != st.OrgID || pin.TaskID != st.TaskID || pin.AttemptID != *st.SettlementReceiptID {
			return result, ErrConflict
		}
		rows = append(rows, pin)
	}
	for _, row := range rows {
		if row.ApplyErrorCode == queryFinancialConflictCode {
			continue
		}
		if row.RecoveryVersion == math.MaxUint64 {
			return result, ErrState
		}
		if _, err := queryPhaseOf(st, row); err != nil {
			return result, err
		}
		q := queryPhaseCAS(tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND apply_error_code = ?", st.OrgID, st.TaskID, row.AttemptID, row.RecoveryVersion, row.ApplyErrorCode), queryRecoveryAttempt{progress: queryPhaseState(st, row)})
		if row.AppliedAt == nil {
			q = q.Where("applied_at IS NULL")
		} else {
			q = q.Where("applied_at = ?", *row.AppliedAt)
		}
		if row.RecoveryDueAt == nil {
			q = q.Where("recovery_due_at IS NULL")
		} else {
			q = q.Where("recovery_due_at = ?", *row.RecoveryDueAt)
		}
		updates := map[string]any{"apply_error_code": queryFinancialConflictCode, "recovery_version": row.RecoveryVersion + 1}
		if row.AppliedAt == nil {
			updates["recovery_due_at"] = nil
		}
		q = q.Updates(updates)
		if q.Error != nil {
			return result, q.Error
		}
		if q.RowsAffected != 1 {
			return result, ErrConflict
		}
		result = queryRecoveryResult{disposition: queryRecoveryReview, code: queryFinancialConflictCode}
	}
	return result, nil
}
