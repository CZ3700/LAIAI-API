package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/video"
)

// The owner of the common process loop supplies an already strictly decoded
// command and registered primary seed. No arbitrary route/amount/ID is accepted.
func (a *phaseAdminAdapter) executeAdmin(ctx context.Context, caseID string, c phaseCommand) (phaseAdminResult, *phaseAdminRaceProof, error) {
	if c.Task != "primary" || c.Case != caseID {
		return phaseAdminResult{}, nil, errors.New("admin_command_identity")
	}
	if caseID != "F05_admin_first" && caseID != "F05_settlement_first" && caseID != "F05_admin_adjust_delete_then_fresh_B" {
		return phaseAdminResult{}, nil, errors.New("admin_case_invalid")
	}
	if c.Command == "race_admin_B" {
		want := "admin"
		if caseID == "F05_settlement_first" {
			want = "settlement"
		}
		if caseID == "F05_admin_adjust_delete_then_fresh_B" || c.Winner != want {
			return phaseAdminResult{}, nil, errors.New("admin_race_case_invalid")
		}
		proof, err := a.race(ctx, c.Winner)
		return proof.Admin, &proof, err
	}
	if c.Command != "admin" {
		return phaseAdminResult{}, nil, errors.New("admin_command_unsupported")
	}
	switch c.Action {
	case "prepare_initial_100":
		r, err := a.prepareInitial(ctx)
		return r, nil, err
	case "settle_100":
		if caseID != "F05_admin_adjust_delete_then_fresh_B" {
			return phaseAdminResult{}, nil, errors.New("admin_race_required")
		}
		before, err := a.readOracle(ctx)
		if err != nil {
			return phaseAdminResult{}, nil, err
		}
		if err = before.requirePendingSeed(); err != nil {
			return phaseAdminResult{}, nil, err
		}
		r, err := a.settleInitial(ctx)
		if err != nil {
			return r, nil, err
		}
		if r.Replay {
			return r, nil, errors.New("admin_first_unexpected_replay")
		}
		after, err := a.readOracle(ctx)
		if err != nil {
			return r, nil, err
		}
		if err = after.require100("admin", *a.initial); err != nil {
			return r, nil, err
		}
		if !reflect.DeepEqual(before.Receipt, after.Receipt) || !reflect.DeepEqual(before.Pin, after.Pin) {
			return r, nil, errors.New("admin_initial_changed_B_progress")
		}
		if err = phaseAdminEventDelta(before, after, []string{"billing.settled"}, []string{r.RequestID}, []string{a.initial.PayloadRef}); err != nil {
			return r, nil, err
		}
		if err = a.requireAudits(ctx, r.Requests); err != nil {
			return r, nil, err
		}
		return r, nil, nil
	case "adjust_90":
		if caseID != "F05_admin_adjust_delete_then_fresh_B" {
			return phaseAdminResult{}, nil, errors.New("admin_adjustment_case_invalid")
		}
		r, err := a.adjust90(ctx)
		return r, nil, err
	default:
		return phaseAdminResult{}, nil, errors.New("admin_action_invalid")
	}
}

type phaseAdminDeleteProof struct {
	Status      int               `json:"status"`
	RequestID   string            `json:"request_id"`
	OperationID string            `json:"operation_id"`
	BodySHA     string            `json:"body_sha256"`
	Before      map[string]string `json:"before"`
	After       map[string]string `json:"after"`
}

func (a *phaseAdminAdapter) publicDelete(ctx context.Context, plainKey string) (phaseAdminDeleteProof, error) {
	out := phaseAdminDeleteProof{}
	if !a.adjusted || plainKey == "" || !a.service.MutationResponseInboxEnabled {
		return out, errors.New("admin_delete_precondition")
	}
	before, err := a.readOracle(ctx)
	if err != nil {
		return out, err
	}
	if before.Receipt.AppliedAt != nil || before.Receipt.RecoveryVersion != 1 || before.State.DeletedAt != nil || before.State.BillingState != "adjusted" || len(before.Applications) != 1 || len(before.Invocations) != 0 || before.State.TotalTokens != nil {
		return out, errors.New("admin_delete_not_pending_B")
	}
	// A separate router avoids mixing admin actor context into the real API-key
	// middleware. Only this fixed DELETE route is invoked by the adapter.
	router := gin.New()
	a.service.RegisterRoutes(router.Group(""), nil)
	req := httptest.NewRequest(http.MethodDelete, "/v1/video/generations/"+a.seed.TaskID, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+plainKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	out.Status, out.RequestID = w.Code, w.Header().Get("X-Request-Id")
	h := sha256.Sum256(w.Body.Bytes())
	out.BodySHA = hex.EncodeToString(h[:])
	if w.Code != http.StatusAccepted || out.RequestID == "" {
		return out, errors.New("admin_public_delete_status")
	}
	var op video.Operation
	if err = phaseAdminDecode(w.Body.Bytes(), &op); err != nil || op.ID == "" || op.Kind != "delete_task" {
		return out, errors.New("admin_public_delete_operation")
	}
	out.OperationID = op.ID
	// This is dispatch only. In particular it never invokes the new independent
	// settlement method and cannot consume the pending query B as a shortcut.
	if err = a.service.ProcessOnce(ctx); err != nil {
		return out, err
	}
	var actual video.Operation
	if err = a.db.WithContext(ctx).Where("org_id=? AND id=? AND resource_id=?", a.seed.OrgID, op.ID, a.seed.TaskID).Take(&actual).Error; err != nil {
		return out, err
	}
	if actual.Kind != "delete_task" || actual.Status != "confirmed" || actual.ReceiptAppliedAt == nil {
		return out, errors.New("admin_delete_not_acknowledged")
	}
	after, err := a.readOracle(ctx)
	if err != nil {
		return out, err
	}
	if after.State.DeletedAt == nil || after.State.DeletionOperationID != op.ID || after.Task.Status != "succeeded" || after.State.BillingState != "adjusted" || !reflect.DeepEqual(before.Receipt, after.Receipt) || !reflect.DeepEqual(before.Pin, after.Pin) {
		return out, errors.New("admin_delete_projection_or_B_progress")
	}
	if err = phaseAdminSameMoney(before, after); err != nil {
		return out, err
	}
	if before.Task.ActualTokens != after.Task.ActualTokens || !before.Task.CostUSD.Equal(after.Task.CostUSD) || !reflect.DeepEqual(before.State.ObservedCompletionTokens, after.State.ObservedCompletionTokens) || !reflect.DeepEqual(before.State.TotalTokens, after.State.TotalTokens) {
		return out, errors.New("admin_delete_changed_90")
	}
	if len(after.Events) != len(before.Events)+2 || len(after.Outbox) != len(before.Outbox)+2 {
		return out, errors.New("admin_delete_event_count")
	}
	updated := after.Events[len(before.Events)+1]
	if updated.RequestID == "" || updated.EvidenceRef == "" {
		return out, errors.New("admin_delete_ack_event_source")
	}
	var sourceCount int64
	if err = a.db.WithContext(ctx).Table("video_response_inbox").Where("org_id=? AND task_id=? AND operation_id=? AND receipt_id=? AND payload_ref=?", a.seed.OrgID, a.seed.TaskID, op.ID, updated.RequestID, updated.EvidenceRef).Count(&sourceCount).Error; err != nil {
		return out, err
	}
	if sourceCount != 1 {
		return out, errors.New("admin_delete_ack_receipt_identity")
	}
	if err = phaseAdminEventDelta(before, after, []string{"task.deletion_requested", "task.operation_updated"}, []string{out.RequestID, updated.RequestID}, []string{"", updated.EvidenceRef}); err != nil {
		return out, err
	}
	out.Before, out.After = before.StorageHashes, after.StorageHashes
	return out, nil
}

// Safe selected fields; no request/response body, token, credential or SQL.
func (o phaseAdminOracle) safeMoney() map[string]string {
	return map[string]string{"org_id": strconv.FormatUint(o.Seed.OrgID, 10), "key_id": strconv.FormatUint(o.Seed.KeyID, 10), "posted_usd": o.Posted.StringFixed(8), "held_usd": o.Held.StringFixed(8), "key_used_usd": o.Key.UsedAmount.StringFixed(8), "task_cost_usd": o.Task.CostUSD.StringFixed(8), "task_actual_tokens": strconv.FormatInt(o.Task.ActualTokens, 10), "hold_captured_usd": o.Hold.CapturedAmount.StringFixed(8), "initial_count": strconv.Itoa(len(o.Entries)), "usage_evidence_count": strconv.Itoa(len(o.Evidence)), "invocation_count": strconv.Itoa(len(o.Invocations)), "adjustment_count": strconv.Itoa(len(o.Applications)), "journal_count": strconv.Itoa(len(o.Journals))}
}
