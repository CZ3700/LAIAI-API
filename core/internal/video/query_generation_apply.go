package video

import (
	"encoding/hex"
	"math"
	"strings"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

// Generation evidence can select a financial source, but never authorizes a
// financial write. B independently validates that source and the real initial.
func queryGenerationEvidence(t task.Task, st State, row QueryReceipt) (queryObservation, ark.Transition, string, bool, error) {
	response := ark.Result{Action: ark.ActionQuery, Outcome: ark.Outcome(row.Outcome), HTTPStatus: row.HTTPStatus, RequestID: row.ProviderRequestID}
	var observation queryObservation
	if row.ObservationJSON != "" {
		var err error
		observation, err = decodeQueryObservation(row.ObservationJSON)
		if err != nil {
			return observation, ark.Transition{}, "", false, err
		}
		response.Observation = &ark.Observation{
			TaskID: observation.TaskID, Model: observation.Model, Status: observation.Status,
			Usage:      ark.Usage{CompletionTokens: observation.CompletionTokens, TotalTokens: observation.TotalTokens},
			Resolution: observation.Resolution, Ratio: observation.Ratio, Duration: observation.Duration,
			Frames: observation.Frames, FramesPerSecond: observation.FramesPerSecond, Seed: observation.Seed,
			CreatedAt: observation.CreatedAt, UpdatedAt: observation.UpdatedAt,
			ServiceTier: observation.ServiceTier, ExecutionExpiresAfter: observation.ExecutionExpiresAfter,
			GenerateAudio: observation.GenerateAudio, Draft: observation.Draft, Priority: observation.Priority,
			HasError: observation.HasError,
		}
	}
	transition := ark.ApplyObservation(ark.Status(t.Status), response)
	matched := response.Observation != nil && observation.TaskID == t.UpstreamTaskID && t.UpstreamTaskID != "" && (observation.Model == "" || observation.Model == st.UpstreamModel)
	if !matched {
		transition.NeedsReview, transition.Status = true, ark.Status(t.Status)
	}
	trusted := matched && row.Outcome == string(ark.OutcomeAccepted) && row.HTTPStatus >= 200 && row.HTTPStatus < 300 && !row.ReceivedAt.IsZero() && !observation.HasError &&
		(observation.CompletionTokens == nil || *observation.CompletionTokens >= 0) && (observation.TotalTokens == nil || *observation.TotalTokens >= 0) &&
		(observation.Status == ark.StatusQueued || observation.Status == ark.StatusRunning || terminal(string(observation.Status)))
	if !trusted {
		transition.NeedsReview, transition.Status = true, ark.Status(t.Status)
		return observation, transition, querySettlementNone, false, nil
	}
	kind := querySettlementNone
	if observation.Status == ark.StatusSucceeded && observation.CompletionTokens != nil {
		kind = querySettlementUsage
	} else if (observation.Status == ark.StatusFailed || observation.Status == ark.StatusCancelled || observation.Status == ark.StatusExpired) &&
		strings.Contains(","+st.NoChargeStatuses+",", ","+string(observation.Status)+",") && (observation.CompletionTokens == nil || *observation.CompletionTokens == 0) && row.PayloadRef != "" {
		kind = querySettlementProviderNoCharge
	}
	return observation, transition, kind, true, nil
}

// A pin can already be reviewed or completed. Reading it must not reopen its
// due/AppliedAt or borrow a B token; only its immutable original facts matter.
func queryGenerationPinnedEvidenceTx(tx *gorm.DB, t task.Task, st State) (QueryReceipt, queryObservation, error) {
	if st.SettlementReceiptID == nil || *st.SettlementReceiptID == "" {
		return QueryReceipt{}, queryObservation{}, ErrConflict
	}
	var pin QueryReceipt
	if err := locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", st.OrgID, st.TaskID, *st.SettlementReceiptID).Take(&pin).Error; err != nil {
		return pin, queryObservation{}, err
	}
	if pin.OrgID != st.OrgID || pin.TaskID != st.TaskID || pin.AttemptID != *st.SettlementReceiptID {
		return pin, queryObservation{}, ErrConflict
	}
	if err := requireQueryReceiptRuntime(st, pin); err != nil {
		return pin, queryObservation{}, err
	}
	if pin.GenerationAppliedAt == nil || pin.GenerationAppliedAt.Year() < 1000 || (pin.SettlementKind != querySettlementUsage && pin.SettlementKind != querySettlementProviderNoCharge) {
		return pin, queryObservation{}, ErrState
	}
	digest, err := settlementEvidenceDigest(t, st, pin, pin.SettlementKind)
	if err != nil {
		return pin, queryObservation{}, err
	}
	if pin.SettlementEvidenceSHA256 != hex.EncodeToString(digest[:]) {
		return pin, queryObservation{}, ErrConflict
	}
	o, _, kind, trusted, err := queryGenerationEvidence(t, st, pin)
	if err != nil {
		return pin, queryObservation{}, err
	}
	if !trusted || kind != pin.SettlementKind {
		return pin, queryObservation{}, ErrConflict
	}
	return pin, o, nil
}

func queryGenerationSourcesAgree(kind string, observed queryObservation, pin QueryReceipt, original queryObservation) bool {
	if kind != pin.SettlementKind || observed.Status != original.Status {
		return false
	}
	return kind != querySettlementUsage || (observed.CompletionTokens != nil && original.CompletionTokens != nil && *observed.CompletionTokens == *original.CompletionTokens)
}

// The coordinator has locked Task -> State -> current receipt. This method
// writes generation and phase only; it takes no Key/quota/ledger/billing lock.
func (s *Service) applyQueryGenerationTx(tx *gorm.DB, t *task.Task, st *State, receipt QueryReceipt) (queryRecoveryResult, error) {
	noop := queryRecoveryResult{disposition: queryRecoveryNoop}
	token, err := observeQueryPhaseRecovery(*t, *st, receipt)
	if err != nil {
		return noop, err
	}
	if token.phase != queryPhaseGenerationV2 || st.RowVersion == math.MaxUint64 {
		return noop, ErrConflict
	}
	o, transition, kind, trusted, err := queryGenerationEvidence(*t, *st, receipt)
	if err != nil {
		return noop, err
	}
	conflict := trusted && terminal(t.Status) && terminal(string(o.Status)) && string(o.Status) != t.Status
	late := receipt.Sequence <= st.AppliedQuerySequence
	// A stale observation is useful only as financial evidence for the same
	// terminal generation. It cannot turn an older terminal into a newer status.
	if late && (!terminal(t.Status) || string(o.Status) != t.Status) {
		kind = querySettlementNone
	}
	pinReview := false
	selectPin := false
	if !conflict && st.SettlementReceiptID != nil {
		pin, original, readErr := queryGenerationPinnedEvidenceTx(tx, *t, *st)
		if readErr != nil {
			return noop, readErr
		}
		pinReview = pin.RecoveryDueAt == nil
		if kind != querySettlementNone {
			conflict = !queryGenerationSourcesAgree(kind, o, pin, original)
			// Later compatible evidence never replaces or reopens the first pin.
			kind = querySettlementNone
		}
	} else if !conflict && kind != querySettlementNone {
		selectPin = true
	}
	if conflict {
		now, clockErr := queryRecoveryTimeTx(tx)
		if clockErr != nil {
			return noop, clockErr
		}
		return markQueryFinancialConflictTx(tx, *st, receipt, now)
	}
	canAdvance := !late && st.DeletedAt == nil
	if canAdvance && trusted && !transition.NeedsReview && terminal(string(transition.Status)) {
		if err = s.afterAssetsTerminalTx(tx, t.OrgID, t.TaskID); err != nil {
			return noop, err
		}
	}
	// Clock is after all preparation/locks. A failed clock rolls back asset
	// work as well; a host clock is never a fallback for phase progress.
	now, err := queryRecoveryTimeTx(tx)
	if err != nil {
		return noop, err
	}
	if err = tx.Statement.Context.Err(); err != nil {
		return noop, err
	}
	digest, err := settlementEvidenceDigest(*t, *st, receipt, kind)
	if err != nil {
		return noop, err
	}
	from, resultRef, artifact := t.Status, st.ResultPayloadRef, st.ArtifactStatus
	taskUpdates, stateUpdates := map[string]any{}, map[string]any{}
	applyCode := ""
	if canAdvance {
		if string(transition.Status) != t.Status {
			t.Status = string(transition.Status)
			taskUpdates["status"] = t.Status
		}
		if terminal(t.Status) && t.FinishedAt == nil {
			taskUpdates["finished_at"] = now
		}
		if transition.NeedsReview {
			st.SyncState, applyCode = "needs_review", "observation_needs_review"
		} else {
			if !pinReview {
				st.SyncState = "fresh"
			}
			st.LastSyncedAt = &receipt.ReceivedAt
			stateUpdates["last_synced_at"] = st.LastSyncedAt
		}
		stateUpdates["sync_state"] = st.SyncState
		if trusted && !transition.NeedsReview && t.Status == "succeeded" && o.DeliveryPayloadRef != "" {
			st.ResultPayloadRef = o.DeliveryPayloadRef
			stateUpdates["result_payload_ref"] = st.ResultPayloadRef
			if receipt.HasVideo {
				st.ArtifactStatus = "ready"
				stateUpdates["artifact_status"] = st.ArtifactStatus
				if err = s.watermarkRegisterTx(tx, t, st, now); err != nil {
					return noop, err
				}
			}
		}
		stateUpdates["applied_query_sequence"] = receipt.Sequence
	}
	if !financialFinal(st.BillingState) {
		billing := st.BillingState
		if selectPin {
			billing = "reserved"
		} else if st.SettlementReceiptID == nil && trusted && terminal(string(o.Status)) && !transition.NeedsReview {
			billing = "pending_usage"
			if canAdvance && o.Status != ark.StatusSucceeded {
				// A real failed generation without eligible no-charge proof keeps
				// the original unresolved-billing review; it is never a release.
				st.SyncState = "needs_review"
				stateUpdates["sync_state"] = st.SyncState
			}
		}
		if billing != st.BillingState {
			st.BillingState = billing
			stateUpdates["billing_state"] = billing
		}
	}
	if selectPin {
		pin := receipt.AttemptID
		st.SettlementReceiptID = &pin
		stateUpdates["settlement_receipt_id"] = pin
	}
	if receipt.Sequence == st.QuerySequence && st.QueryLeaseUntil != nil {
		stateUpdates["query_lease_until"] = nil
	}
	if len(taskUpdates) != 0 {
		if err = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(taskUpdates).Error; err != nil {
			return noop, err
		}
	}
	if len(stateUpdates) != 0 {
		stateUpdates["row_version"], stateUpdates["updated_at"] = st.RowVersion+1, now
		write := tx.Model(&State{}).Where("org_id = ? AND task_id = ? AND row_version = ?", st.OrgID, st.TaskID, st.RowVersion).Updates(stateUpdates)
		if write.Error != nil {
			return noop, write.Error
		}
		if write.RowsAffected != 1 {
			return noop, ErrConflict
		}
	}
	if canAdvance && trusted && !transition.NeedsReview && t.Status == "cancelled" {
		// Provider cancellation confirms the generation-side request, independently
		// of B's later local release. Never confirm another runtime's operation.
		if err = tx.Model(&Operation{}).
			Where("org_id = ? AND resource_id = ? AND kind = ? AND status IN ? AND runtime_version = ? AND adapter_version = ? AND protocol_contract_version = ?", t.OrgID, t.TaskID, "cancel_task", []string{"pending", "unknown", "in_flight"}, st.RuntimeVersion, st.AdapterVersion, st.ProtocolContractVersion).
			Where("OCTET_LENGTH(resource_id) = ? AND OCTET_LENGTH(kind) = ?", len(t.TaskID), len("cancel_task")).
			Where("((status = 'pending' AND OCTET_LENGTH(status) = 7) OR (status = 'unknown' AND OCTET_LENGTH(status) = 7) OR (status = 'in_flight' AND OCTET_LENGTH(status) = 9))").
			Updates(map[string]any{"status": "confirmed", "updated_at": now}).Error; err != nil {
			return noop, err
		}
	}
	if canAdvance && (from != t.Status || resultRef != st.ResultPayloadRef || artifact != st.ArtifactStatus) {
		if err = appendEventTx(tx, t, st, "task.status_changed", from, t.Status, receipt.AttemptID, receipt.PayloadRef, now); err != nil {
			return noop, err
		}
	}
	progress := map[string]any{"generation_applied_at": now, "settlement_kind": kind, "settlement_evidence_sha256": hex.EncodeToString(digest[:]), "recovery_version": receipt.RecoveryVersion + 1, "apply_error_code": applyCode}
	if selectPin {
		progress["recovery_due_at"] = now
	} else {
		progress["applied_at"] = now
	}
	q := tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL", receipt.OrgID, receipt.TaskID, receipt.AttemptID, receipt.RecoveryVersion)
	write := queryPhaseCAS(q, token).Updates(progress)
	if write.Error != nil {
		return noop, write.Error
	}
	if write.RowsAffected != 1 {
		return noop, ErrConflict
	}
	return queryRecoveryResult{disposition: queryRecoveryApplied, code: applyCode}, nil
}
