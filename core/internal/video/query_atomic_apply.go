package video

import (
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// The pre-S2 V1 reducer keeps its original order, timestamp and atomic money
// transaction. The coordinator owns locks and the final COMMIT.
func (s *Service) applyQueryAtomicTx(tx *gorm.DB, taskRow *task.Task, stateRow *State, receipt QueryReceipt, now time.Time) (queryRecoveryResult, error) {
	t, st := *taskRow, *stateRow
	result := queryRecoveryResult{disposition: queryRecoveryNoop}
	var e error
	err := func() error {
		finish := func(code string) error {
			q := tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL", receipt.OrgID, receipt.TaskID, receipt.AttemptID, receipt.RecoveryVersion).
				Updates(map[string]any{"applied_at": now, "apply_error_code": code, "recovery_version": receipt.RecoveryVersion + 1})
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return ErrConflict
			}
			result = queryRecoveryResult{disposition: queryRecoveryApplied, code: code}
			return nil
		}
		if st.DeletedAt != nil {
			return finish("task_deleted")
		}
		if receipt.Sequence <= st.AppliedQuerySequence {
			return finish("stale_query_response")
		}
		response := ark.Result{Action: ark.ActionQuery, Outcome: ark.Outcome(receipt.Outcome), HTTPStatus: receipt.HTTPStatus, RequestID: receipt.ProviderRequestID}
		deliveryRef := ""
		if receipt.ObservationJSON != "" {
			o, decodeErr := decodeQueryObservation(receipt.ObservationJSON)
			if decodeErr != nil {
				return decodeErr
			}
			response.Observation = &ark.Observation{
				TaskID: o.TaskID, Model: o.Model, Status: o.Status,
				Usage:      ark.Usage{CompletionTokens: o.CompletionTokens, TotalTokens: o.TotalTokens},
				Resolution: o.Resolution, Ratio: o.Ratio, Duration: o.Duration, Frames: o.Frames,
				FramesPerSecond: o.FramesPerSecond, Seed: o.Seed, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
				ServiceTier: o.ServiceTier, ExecutionExpiresAfter: o.ExecutionExpiresAfter,
				GenerateAudio: o.GenerateAudio, Draft: o.Draft, Priority: o.Priority, HasError: o.HasError,
			}
			deliveryRef = o.DeliveryPayloadRef
		}
		org, id, attempt, ref := t.OrgID, t.TaskID, receipt.AttemptID, receipt.PayloadRef
		transition := ark.ApplyObservation(ark.Status(t.Status), response)
		if response.Observation == nil || response.Observation.TaskID != t.UpstreamTaskID || (response.Observation.Model != "" && response.Observation.Model != st.UpstreamModel) {
			transition.NeedsReview = true
			transition.Status = ark.Status(t.Status)
		}
		from := t.Status
		t.Status = string(transition.Status)
		applyCode := ""
		st.SyncState = "fresh"
		if transition.NeedsReview {
			st.SyncState = "needs_review"
		} else {
			st.LastSyncedAt = &receipt.ReceivedAt
		}
		observation := response.Observation
		if !transition.NeedsReview && observation != nil {
			// Asset admission takes the tenant lock before Key/quota/ledger locks.
			// Terminal release must use the same ordering within this transaction.
			if terminal(t.Status) {
				if e = s.afterAssetsTerminalTx(tx, org, id); e != nil {
					return e
				}
			}
			final := financialFinal(st.BillingState)
			if final {
				// An omitted or conflicting value is never permission to erase or
				// reprice the already confirmed financial projection.
				if observation.Usage.CompletionTokens != nil && (st.ObservedCompletionTokens == nil || *observation.Usage.CompletionTokens != *st.ObservedCompletionTokens) {
					st.SyncState, applyCode = "needs_review", "usage_conflicts_with_settlement"
				}
			} else if observation.Usage.CompletionTokens != nil {
				st.ObservedCompletionTokens = observation.Usage.CompletionTokens
			}
			if st.TotalTokens == nil {
				st.TotalTokens = observation.Usage.TotalTokens
			}
			switch t.Status {
			case "succeeded":
				if !final {
					st.BillingState = "pending_usage"
				}
				if !final && observation.Usage.CompletionTokens != nil {
					settled, e := videobilling.SettleUsageTx(tx, videobilling.SettlementInput{OrgID: org, TaskID: id, Evidence: videobilling.UsageEvidence{OrgID: org, TaskID: id, EvidenceID: attempt, Source: "task_get", SourceRecordID: t.UpstreamTaskID, SourceVersion: attempt, CompletionTokens: observation.Usage.CompletionTokens, PayloadRef: ref, ObservedAt: now}, Now: now})
					if e != nil {
						return e
					}
					st.BillingState = settled.State
					if settled.Entry != nil {
						t.ActualTokens = *observation.Usage.CompletionTokens
						t.CostUSD = settled.Entry.CapturedUSD
						if !settled.Replay {
							if e = invocation.RecordTx(tx, &invocation.Invocation{OrgID: org, UserID: t.UserID, APIKeyID: t.APIKeyID, RequestID: st.CreateRequestID, Model: t.Model, Modality: invocation.ModalityVideo, ChannelID: t.ChannelID, Provider: Provider, Status: invocation.StatusSuccess, StatusCode: 200, Attempts: 1, CompletionTokens: t.ActualTokens, CostUSD: t.CostUSD, UsageSource: invocation.UsageSourceUpstream, Protocol: "ark_v3", CreatedAt: now}); e != nil {
								return e
							}
						}
					}
				}
				if deliveryRef != "" {
					st.ResultPayloadRef = deliveryRef
					if receipt.HasVideo {
						st.ArtifactStatus = "ready"
					}
				}
			case "failed", "cancelled", "expired":
				if t.Status == "cancelled" {
					if e = tx.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ? AND status IN ? AND runtime_version = ? AND adapter_version = ? AND protocol_contract_version = ?", org, id, "cancel_task", []string{"pending", "unknown", "in_flight"}, st.RuntimeVersion, st.AdapterVersion, st.ProtocolContractVersion).Updates(map[string]any{"status": "confirmed", "updated_at": now}).Error; e != nil {
						return e
					}
				}
				if final {
					break
				}
				if strings.Contains(","+st.NoChargeStatuses+",", ","+t.Status+",") && (observation.Usage.CompletionTokens == nil || *observation.Usage.CompletionTokens == 0) {
					released, e := videobilling.ReleaseConfirmedTx(tx, videobilling.ReleaseInput{OrgID: org, TaskID: id, EvidenceID: ref, Reason: videobilling.ReleaseProviderNoCharge, Confirmed: true, Now: now})
					if e != nil {
						return e
					}
					st.BillingState = released.State
				} else {
					st.BillingState = "pending_usage"
					st.SyncState = "needs_review"
				}
			}
			if e = s.afterObservationTx(tx, &t, &st, receipt.HasVideo, receipt.HasLastFrame, now); e != nil {
				return e
			}
		}
		updates := map[string]any{"status": t.Status, "actual_tokens": t.ActualTokens, "cost_usd": t.CostUSD}
		if terminal(t.Status) && t.FinishedAt == nil {
			updates["finished_at"] = now
		}
		if e = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", org, id).Updates(updates).Error; e != nil {
			return e
		}
		stateUpdates := map[string]any{"billing_state": st.BillingState, "sync_state": st.SyncState, "artifact_status": st.ArtifactStatus, "result_payload_ref": st.ResultPayloadRef, "observed_completion_tokens": st.ObservedCompletionTokens, "total_tokens": st.TotalTokens, "last_synced_at": st.LastSyncedAt, "updated_at": now, "applied_query_sequence": receipt.Sequence, "row_version": st.RowVersion + 1}
		if receipt.Sequence == st.QuerySequence {
			stateUpdates["query_lease_until"] = nil
		}
		if e = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, id).Updates(stateUpdates).Error; e != nil {
			return e
		}
		if transition.NeedsReview {
			applyCode = "observation_needs_review"
		}
		if e = appendEventTx(tx, &t, &st, "task.status_changed", from, t.Status, attempt, ref, now); e != nil {
			return e
		}
		return finish(applyCode)
	}()
	return result, err
}
