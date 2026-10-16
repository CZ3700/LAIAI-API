package video

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProcessOnce has bounded work. Unknown creates are never placed back on the
// dispatch queue. A persisted response can be replayed without another POST.
func (s *Service) ProcessOnce(ctx context.Context) error {
	if err := s.recoverOperations(ctx); err != nil {
		return err
	}
	if err := s.recoverQueryReceipts(ctx); err != nil {
		return err
	}
	var ops []Operation
	// Pick at most one eligible operation per provider scope/action. Waiting
	// cancels and an account's backlog cannot monopolize the bounded batch.
	queued := s.pendingOperationCandidates(ctx)
	if err := s.DB.WithContext(ctx).Table("(?) AS eligible", queued).Where("scope_rank = 1").Order("created_at, id").Limit(20).Find(&ops).Error; err != nil {
		return err
	}
	for _, op := range ops {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.processOperation(ctx, op); err != nil {
			s.Log.Warn("video operation needs recovery", "operation_id", op.ID)
		}
	}
	var rows []State
	if err := s.DB.WithContext(ctx).Table("video_task_states AS v").Select("v.*").Joins("JOIN tasks t ON t.task_id = v.task_id AND t.org_id = v.org_id").Where("v.deleted_at IS NULL AND v.next_poll_at <= ? AND t.upstream_task_id <> '' AND (t.status IN ? OR v.billing_state = ?)", s.Now().UTC(), []string{"queued", "running"}, "pending_usage").Order("v.next_poll_at, v.task_id").Limit(40).Scan(&rows).Error; err != nil {
		return err
	}
	for _, st := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.pollTask(ctx, st.OrgID, st.TaskID); err != nil {
			s.Log.Warn("video query needs recovery", "task_id", st.TaskID)
		}
	}
	return nil
}

func (s *Service) pendingOperationCandidates(ctx context.Context) *gorm.DB {
	return s.DB.WithContext(ctx).Table("video_operations AS o").Select("o.*, ROW_NUMBER() OVER (PARTITION BY v.provider_scope_id, CASE WHEN o.kind = 'create_task' THEN 'create' ELSE 'delete' END ORDER BY o.created_at, o.id) AS scope_rank").Joins("JOIN tasks t ON t.org_id = o.org_id AND t.task_id = o.resource_id").Joins("JOIN video_task_states v ON v.org_id = o.org_id AND v.task_id = o.resource_id").Joins("LEFT JOIN video_provider_rates p ON p.scope_id = v.provider_scope_id AND p.action = CASE WHEN o.kind = 'create_task' THEN 'create' ELSE 'delete' END").Where("o.status = ? AND t.provider = ? AND v.deleted_at IS NULL AND (p.next_allowed_at IS NULL OR p.next_allowed_at <= ?)", "pending", Provider, s.Now().UTC()).Where("(o.kind = ? AND t.status = ?) OR (o.kind = ? AND t.status NOT IN ?) OR (o.kind = ? AND t.status IN ?)", "create_task", "accepted", "cancel_task", []string{"submitting", "submission_unknown"}, "delete_task", []string{"succeeded", "failed", "cancelled", "expired"})
}

func (s *Service) recoverOperations(ctx context.Context) error {
	var rows []Operation
	now := s.Now().UTC()
	if err := s.DB.WithContext(ctx).Where("(status IN ? AND receipt_ref <> '' AND receipt_applied_at IS NULL) OR (status = ? AND lease_until <= ?)", []string{"in_flight", "unknown"}, "in_flight", now).Order("updated_at, id").Limit(30).Find(&rows).Error; err != nil {
		return err
	}
	for _, op := range rows {
		if op.ReceiptRef != "" && op.Kind != "create_task" {
			if err := s.applyMutationReceipt(ctx, op); err == nil {
				continue
			}
		}
		if op.ReceiptRef != "" && op.Kind == "create_task" {
			if err := s.applyCreateReceipt(ctx, op); err == nil {
				continue
			}
		}
		if op.Status != "in_flight" {
			continue
		}
		if err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			t, st, e := loadTask(tx, op.OrgID, op.ResourceID, true)
			if e != nil {
				return e
			}
			result := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND status = ?", op.OrgID, op.ID, op.Version, "in_flight").Updates(map[string]any{"status": "unknown", "error_code": "submission_uncertain", "updated_at": now})
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			previous := t.Status
			if op.Kind == "create_task" && !terminal(t.Status) {
				t.Status = "submission_unknown"
				if e = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Update("status", t.Status).Error; e != nil {
					return e
				}
			}
			st.SyncState = "needs_review"
			if !financialFinal(st.BillingState) {
				st.BillingState = "pending_usage"
			}
			review := now.Add(time.Hour)
			if e = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"sync_state": st.SyncState, "billing_state": st.BillingState, "review_due_at": review, "updated_at": now}).Error; e != nil {
				return e
			}
			return appendEventTx(tx, &t, &st, "task.submission_unknown", previous, t.Status, st.CreateRequestID, "", now)
		}); err != nil {
			return err
		}
	}
	return nil
}

// A shared DB gate limits aggregate account traffic even if more than one
// worker runs. Conservative 1 request/s per action until account quotas exist.
func (s *Service) rateTx(tx *gorm.DB, scope uint64, action string, now time.Time) (bool, error) {
	initial := ProviderRate{ScopeID: scope, Action: action, NextAllowedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
		return false, err
	}
	result := tx.Model(&ProviderRate{}).Where("scope_id = ? AND action = ? AND next_allowed_at <= ?", scope, action, now).Update("next_allowed_at", now.Add(time.Second))
	return result.RowsAffected == 1, result.Error
}

func (s *Service) processOperation(ctx context.Context, op Operation) error {
	if op.Kind != "create_task" {
		return s.processMutation(ctx, op)
	}
	var t task.Task
	var st State
	claimed := false
	now := s.Now().UTC()
	attemptID, err := randomID("att_")
	if err != nil {
		return err
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		t, st, e = loadTask(tx, op.OrgID, op.ResourceID, true)
		if e != nil {
			return e
		}
		if t.Status != "accepted" {
			return nil
		}
		allowed, e := s.rateTx(tx, st.ProviderScopeID, "create", now)
		if e != nil || !allowed {
			return e
		}
		expires := now.Add(3 * time.Minute)
		update := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND status = ? AND version = ?", op.OrgID, op.ID, "pending", op.Version).Updates(map[string]any{"status": "in_flight", "version": op.Version + 1, "attempt_id": attemptID, "lease_until": expires, "updated_at": now})
		if update.Error != nil || update.RowsAffected == 0 {
			return update.Error
		}
		op.Version++
		op.AttemptID = attemptID
		if e = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ? AND status = ?", t.OrgID, t.TaskID, "accepted").Update("status", "submitting").Error; e != nil {
			return e
		}
		if e = tx.Create(&Attempt{OrgID: t.OrgID, TaskID: t.TaskID, AttemptID: attemptID, RequestID: st.CreateRequestID, OperationID: op.ID, Phase: "sending", EvidenceRef: op.PayloadRef, ObservedAt: now}).Error; e != nil {
			return e
		}
		if e = appendEventTx(tx, &t, &st, "task.submitting", "accepted", "submitting", st.CreateRequestID, op.PayloadRef, now); e != nil {
			return e
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	body, err := s.Evidence.ReadForExecution(ctx, t.OrgID, op.PayloadRef, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "video-dispatch", ReasonCode: "dispatch", OperationID: op.ID})
	if err != nil {
		return s.knownUnsent(ctx, t, st, op, "execution_unavailable")
	}
	client, err := s.clientFor(ctx, t, st)
	if err != nil {
		return s.knownUnsent(ctx, t, st, op, "credential_unavailable")
	}
	// Body was validated, priced and encrypted at acceptance. Never reconstruct
	// it from today's capability/defaults after a configuration change.
	response := client.Submit(ctx, ark.NormalizedRequest{Body: body, PublicModel: t.Model, UpstreamModel: st.UpstreamModel, CapabilityVersion: st.CapabilityVersion})
	if response.Outcome == ark.OutcomeNotSent {
		return s.knownUnsent(ctx, t, st, op, "request_not_sent")
	}
	ref, err := s.persistResponse(ctx, t, op, response, client.Credential)
	if err != nil {
		return err
	}
	op.ReceiptRef = ref
	op.HTTPStatus = response.HTTPStatus
	return s.applyCreateReceipt(ctx, op)
}

func (s *Service) persistResponse(ctx context.Context, t task.Task, op Operation, response ark.Result, secret string) (string, error) {
	var state State
	if err := s.DB.WithContext(ctx).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).First(&state).Error; err != nil {
		return "", err
	}
	body := response.Body
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	payload, err := s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: t.OrgID, RequestID: op.AttemptID, AttemptID: op.AttemptID, Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: body, ExpiresAt: s.Now().UTC().Add(time.Duration(state.RetentionDays) * 24 * time.Hour), CaptureEnabled: state.CaptureHistory, SecretValues: []string{secret}})
	if err != nil {
		return "", err
	}
	var ref string
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		ref, e = s.Evidence.StageTx(tx, payload)
		if e != nil {
			return e
		}
		if e = tx.Create(&Attempt{OrgID: t.OrgID, TaskID: t.TaskID, AttemptID: op.AttemptID, RequestID: op.AttemptID, OperationID: op.ID, Phase: "response_received", HTTPStatus: response.HTTPStatus, UpstreamRequestID: response.RequestID, EvidenceRef: ref, ObservedAt: s.Now().UTC()}).Error; e != nil {
			return e
		}
		updated := tx.Model(&Operation{}).Where("id = ? AND org_id = ? AND version = ?", op.ID, t.OrgID, op.Version).Updates(map[string]any{"receipt_ref": ref, "http_status": response.HTTPStatus, "received_outcome": string(response.Outcome), "received_task_id": response.TaskID, "updated_at": s.Now().UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	return ref, err
}

func (s *Service) applyCreateReceipt(ctx context.Context, op Operation) error {
	now := s.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t, st, err := loadTask(tx, op.OrgID, op.ResourceID, true)
		if err != nil {
			return err
		}
		var current Operation
		if err := locked(tx).Where("org_id = ? AND id = ?", op.OrgID, op.ID).First(&current).Error; err != nil {
			return err
		}
		if current.Status == "confirmed" || current.Status == "rejected" {
			return nil
		}
		if current.ReceiptAppliedAt != nil {
			return nil
		}
		response := ark.Result{Outcome: ark.Outcome(current.ReceivedOutcome), TaskID: current.ReceivedTaskID}
		from := t.Status
		switch response.Outcome {
		case ark.OutcomeAccepted:
			if response.TaskID == "" {
				return ErrUnavailable
			}
			// Same provider scope cannot bind one upstream task to two customer tasks.
			binding := UpstreamBinding{ProviderScopeID: st.ProviderScopeID, UpstreamTaskID: response.TaskID, OrgID: t.OrgID, TaskID: t.TaskID, CreatedAt: now}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&binding).Error; err != nil {
				return err
			}
			var existing UpstreamBinding
			if err := locked(tx).Where("provider_scope_id = ? AND upstream_task_id = ?", st.ProviderScopeID, response.TaskID).First(&existing).Error; err != nil {
				return err
			}
			if existing.OrgID != t.OrgID || existing.TaskID != t.TaskID {
				return ErrConflict
			}
			if !terminal(t.Status) {
				t.Status = "queued"
			}
			st.SyncState = "fresh"
			if !financialFinal(st.BillingState) {
				st.BillingState = "reserved"
			}
			current.Status = "confirmed"
			if err := tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"status": t.Status, "upstream_task_id": response.TaskID}).Error; err != nil {
				return err
			}
		case ark.OutcomeRejected:
			// A definite synchronous rejection did not create an accepted task.
			if _, err := videobilling.ReleaseConfirmedTx(tx, videobilling.ReleaseInput{OrgID: t.OrgID, TaskID: t.TaskID, EvidenceID: op.ReceiptRef, Reason: videobilling.ReleaseProviderNoCharge, Confirmed: true, Now: now}); err != nil {
				return err
			}
			t.Status = "failed"
			st.BillingState = "released"
			current.Status = "rejected"
			if err := tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"status": t.Status, "finished_at": now}).Error; err != nil {
				return err
			}
		default:
			if !terminal(t.Status) {
				t.Status = "submission_unknown"
			}
			st.SyncState = "needs_review"
			if !financialFinal(st.BillingState) {
				st.BillingState = "pending_usage"
			}
			current.Status = "unknown"
			if err := tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Update("status", t.Status).Error; err != nil {
				return err
			}
		}
		st.ResponsePayloadRef = op.ReceiptRef
		st.UpdatedAt = now
		st.NextPollAt = now
		st.RowVersion++
		if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"sync_state": st.SyncState, "billing_state": st.BillingState, "response_payload_ref": op.ReceiptRef, "row_version": st.RowVersion, "updated_at": now, "next_poll_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Operation{}).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Updates(map[string]any{"status": current.Status, "receipt_applied_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if terminal(t.Status) {
			if err := s.afterObservationTx(tx, &t, &st, false, false, now); err != nil {
				return err
			}
		}
		typ := "task.status_changed"
		if t.Status == "submission_unknown" {
			typ = "task.submission_unknown"
		}
		return appendEventTx(tx, &t, &st, typ, from, t.Status, st.CreateRequestID, op.ReceiptRef, now)
	})
}

func (s *Service) knownUnsent(ctx context.Context, t task.Task, st State, op Operation, code string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		t, st, e = loadTask(tx, t.OrgID, t.TaskID, true)
		if e != nil {
			return e
		}
		updated := tx.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND status = ?", op.OrgID, op.ID, op.Version, "in_flight").Updates(map[string]any{"status": "rejected", "error_code": code, "updated_at": s.Now().UTC()})
		if updated.Error != nil || updated.RowsAffected == 0 {
			return updated.Error
		}
		evidence, err := randomID("ev_")
		if err != nil {
			return err
		}
		if err := tx.Create(&Attempt{OrgID: t.OrgID, TaskID: t.TaskID, AttemptID: op.AttemptID, RequestID: st.CreateRequestID, OperationID: op.ID, Phase: "not_sent", EvidenceRef: evidence, ErrorCode: code, ObservedAt: s.Now().UTC()}).Error; err != nil {
			return err
		}
		if _, err := videobilling.ReleaseConfirmedTx(tx, videobilling.ReleaseInput{OrgID: t.OrgID, TaskID: t.TaskID, EvidenceID: evidence, Reason: videobilling.ReleaseNotDispatched, Confirmed: true, Now: s.Now().UTC()}); err != nil {
			return err
		}
		if err := tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"status": "failed", "finished_at": s.Now().UTC()}).Error; err != nil {
			return err
		}
		st.BillingState = "released"
		if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Update("billing_state", "released").Error; err != nil {
			return err
		}
		t.Status = "failed"
		if err := s.afterObservationTx(tx, &t, &st, false, false, s.Now().UTC()); err != nil {
			return err
		}
		return appendEventTx(tx, &t, &st, "task.status_changed", "submitting", "failed", st.CreateRequestID, evidence, s.Now().UTC())
	})
}

func (s *Service) pollTask(ctx context.Context, org uint64, id string) error {
	var t task.Task
	var st State
	attempt, err := randomID("att_")
	if err != nil {
		return err
	}
	now := s.Now().UTC()
	claimed := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		t, st, e = loadTask(tx, org, id, true)
		if e != nil {
			return e
		}
		if t.UpstreamTaskID == "" || st.NextPollAt.After(now) || (st.QueryLeaseUntil != nil && st.QueryLeaseUntil.After(now)) {
			return nil
		}
		allowed, e := s.rateTx(tx, st.ProviderScopeID, "query", now)
		if e != nil || !allowed {
			return e
		}
		st.QuerySequence++
		updated := tx.Model(&State{}).Where("org_id = ? AND task_id = ? AND query_sequence = ?", org, id, st.QuerySequence-1).Updates(map[string]any{"next_poll_at": now.Add(10 * time.Second), "query_lease_until": now.Add(3 * time.Minute), "query_sequence": st.QuerySequence})
		if updated.Error != nil || updated.RowsAffected == 0 {
			return updated.Error
		}
		if e := tx.Create(&Attempt{OrgID: org, TaskID: id, AttemptID: attempt, RequestID: attempt, Phase: "query_sending", ObservedAt: now}).Error; e != nil {
			return e
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	client, err := s.clientFor(ctx, t, st)
	if err != nil {
		return err
	}
	response := client.Query(ctx, t.UpstreamTaskID)
	received := s.Now().UTC()
	body := response.Body
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	payload, err := s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: org, RequestID: attempt, AttemptID: attempt, Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: body, ExpiresAt: now.Add(time.Duration(st.RetentionDays) * 24 * time.Hour), CaptureEnabled: st.CaptureHistory, SecretValues: []string{client.Credential}})
	if err != nil {
		return err
	}
	receipt := QueryReceipt{OrgID: org, TaskID: id, AttemptID: attempt, Sequence: st.QuerySequence, TaskRowVersion: st.RowVersion, HTTPStatus: response.HTTPStatus, Outcome: string(response.Outcome), ProviderRequestID: response.RequestID, ReceivedAt: received}
	if response.Observation != nil {
		o := response.Observation
		// This typed whitelist is the only unencrypted response projection. URLs,
		// raw provider errors and arbitrary fields remain in Invocation Store.
		meta, e := json.Marshal(queryObservation{TaskID: o.TaskID, Model: o.Model, Status: o.Status, CompletionTokens: o.Usage.CompletionTokens, TotalTokens: o.Usage.TotalTokens, HasError: o.HasError})
		if e != nil {
			return e
		}
		receipt.ObservationJSON = string(meta)
		receipt.HasVideo, receipt.HasLastFrame = o.VideoURL != "", o.LastFrameURL != ""
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ref, e := s.Evidence.StageTx(tx, payload)
		if e != nil {
			return e
		}
		receipt.PayloadRef = ref
		if e = tx.Create(&Attempt{OrgID: org, TaskID: id, AttemptID: attempt, RequestID: attempt, Phase: "query_response", HTTPStatus: response.HTTPStatus, UpstreamRequestID: response.RequestID, EvidenceRef: ref, ObservedAt: received}).Error; e != nil {
			return e
		}
		return tx.Create(&receipt).Error
	})
	if err != nil {
		return err
	}
	return s.applyQueryReceipt(ctx, receipt)
}

type queryObservation struct {
	TaskID           string     `json:"task_id"`
	Model            string     `json:"model"`
	Status           ark.Status `json:"status"`
	CompletionTokens *int64     `json:"completion_tokens"`
	TotalTokens      *int64     `json:"total_tokens"`
	HasError         bool       `json:"has_error"`
}

func (s *Service) recoverQueryReceipts(ctx context.Context) error {
	var rows []QueryReceipt
	if err := s.DB.WithContext(ctx).Where("applied_at IS NULL AND apply_error_code <> ?", "settlement_conflict").Order("received_at, attempt_id").Limit(40).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.applyQueryReceipt(ctx, row); err != nil {
			s.Log.Warn("video query receipt needs recovery", "attempt_id", row.AttemptID)
		}
	}
	return nil
}

func (s *Service) applyQueryReceipt(ctx context.Context, receipt QueryReceipt) error {
	now := s.Now().UTC()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t, st, e := LockBillingTaskTx(tx, receipt.OrgID, receipt.TaskID)
		if e != nil {
			return e
		}
		if e = locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", receipt.OrgID, receipt.TaskID, receipt.AttemptID).First(&receipt).Error; e != nil {
			return e
		}
		if receipt.AppliedAt != nil {
			return nil
		}
		finish := func(code string) error {
			return tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ?", receipt.OrgID, receipt.TaskID, receipt.AttemptID).Updates(map[string]any{"applied_at": now, "apply_error_code": code}).Error
		}
		if st.DeletedAt != nil {
			return finish("task_deleted")
		}
		if receipt.Sequence <= st.AppliedQuerySequence {
			return finish("stale_query_response")
		}
		response := ark.Result{Action: ark.ActionQuery, Outcome: ark.Outcome(receipt.Outcome), HTTPStatus: receipt.HTTPStatus, RequestID: receipt.ProviderRequestID}
		if receipt.ObservationJSON != "" {
			var o queryObservation
			if e = json.Unmarshal([]byte(receipt.ObservationJSON), &o); e != nil {
				return e
			}
			response.Observation = &ark.Observation{TaskID: o.TaskID, Model: o.Model, Status: o.Status, Usage: ark.Usage{CompletionTokens: o.CompletionTokens, TotalTokens: o.TotalTokens}, HasError: o.HasError}
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
				if receipt.HasVideo && st.ArtifactStatus != "ready" {
					st.ResultPayloadRef = ref
					st.ArtifactStatus = "pending"
				}
			case "failed", "cancelled", "expired":
				if t.Status == "cancelled" {
					if e = tx.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ? AND status IN ?", org, id, "cancel_task", []string{"pending", "unknown", "in_flight"}).Updates(map[string]any{"status": "confirmed", "updated_at": now}).Error; e != nil {
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
	})
	if err == nil {
		return nil
	}
	// The response transaction already committed. A settlement/event failure
	// cannot remove the evidence, and recovery can retry it without another GET.
	code := "query_apply_failed"
	if errors.Is(err, videobilling.ErrConflict) {
		code = "settlement_conflict"
	}
	markErr := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND applied_at IS NULL", receipt.OrgID, receipt.TaskID, receipt.AttemptID).Update("apply_error_code", code).Error; e != nil {
			return e
		}
		return tx.Model(&State{}).Where("org_id = ? AND task_id = ? AND deleted_at IS NULL", receipt.OrgID, receipt.TaskID).Update("sync_state", "needs_review").Error
	})
	return errors.Join(err, markErr)
}

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrNotFound)
}
