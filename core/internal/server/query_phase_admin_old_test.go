package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/gin-gonic/gin"
)

// Only the fixed N-server / F03_settled_delivery envelope calls this. The
// source and current-lock runtime checks remain the actual historical code.
func (a *phaseAdminAdapter) oldRuntimeAdmin(ctx context.Context, action string) (map[string]any, error) {
	out := map[string]any{"action": action, "task": "primary", "replay": false, "code": "", "error_classes": []string{}}
	if _, ok := any(a.service).(phaseSettlementConsumer); ok {
		return out, errors.New("N_has_settlement_capability")
	}
	before, e := a.readOracle(ctx)
	if e != nil {
		return out, e
	}
	if before.Task.Status != "succeeded" || before.State.BillingState != "settled" || before.Receipt.AppliedAt == nil || before.Receipt.GenerationAppliedAt == nil || before.Pin == nil || *before.Pin != a.seed.QueryAttemptID {
		return out, errors.New("N_admin_not_settled_V2")
	}
	start := a.requestCount()
	switch action {
	case "prepare_initial_100":
		if a.initial != nil {
			return out, errors.New("N_admin_preparation_repeated")
		}
		line, x := a.importBill(ctx, 1)
		if x != nil {
			return out, x
		}
		a.initial = &line
		out["status"] = 200
		if e = a.requireAudits(ctx, a.requestSlice(start)); e != nil {
			return out, e
		}
	case "settle_100":
		if a.initial == nil {
			return out, errors.New("N_admin_line_missing")
		}
		response, rid, x := a.call(ctx, 0, "ark_bill_settle", fmt.Sprintf("/orgs/%d/tasks/%s/settle-bill", a.seed.OrgID, a.seed.TaskID), gin.H{"line_id": a.initial.LineID}, 409)
		if x != nil {
			return out, x
		}
		var failure struct {
			Code string `json:"code"`
		}
		if x = phaseAdminDecode(response["error"], &failure); x != nil || failure.Code != "runtime_contract_unsupported" {
			return out, errors.New("N_admin_wrong_rejection")
		}
		// A failed transaction may not leave a success audit. This request ID is
		// still retained in the safe request proof; no DB/permission error is folded
		// into the supported runtime rejection.
		var n int64
		if x = a.db.WithContext(ctx).Table("audit_logs").Where("action=? AND JSON_UNQUOTE(JSON_EXTRACT(detail,'$.request_id'))=?", "ark_bill_settle", rid).Count(&n).Error; x != nil {
			return out, x
		}
		if n != 0 {
			return out, errors.New("N_admin_rejection_committed_audit")
		}
		out["status"] = 409
		out["code"] = failure.Code
		out["error_classes"] = []string{"unsupported_contract"}
		out["request_id"] = rid
	default:
		return out, errors.New("N_admin_action_invalid")
	}
	after, e := a.readOracle(ctx)
	if e != nil {
		return out, e
	}
	if e = phaseAdminSameMoney(before, after); e != nil {
		return out, e
	}
	if !reflect.DeepEqual(before.StorageHashes, after.StorageHashes) {
		return out, errors.New("N_admin_mutated_task_or_receipt")
	}
	requests := a.requestSlice(start)
	out["requests"] = requests
	out["before_storage"] = before.StorageHashes
	out["after_storage"] = after.StorageHashes
	out["before_money"] = before.safeMoney()
	out["after_money"] = after.safeMoney()
	// Force a safe JSON boundary locally; only closed literals and typed request
	// summaries enter this result, never a driver error or raw response body.
	if _, e = json.Marshal(out); e != nil {
		return nil, e
	}
	return out, nil
}
