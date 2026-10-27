package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (h *phaseHarness) command(c phaseCommand) (string, map[string]any, error) {
	data := map[string]any{"command": c.Command}
	switch c.Command {
	case "seed_config":
		if e := h.seedConfiguration(c.Profile); e != nil {
			return "", nil, e
		}
		return "seeded", map[string]any{"profile": h.seed.Profile, "org_id": h.seed.OrgID, "key_id": h.seed.KeyID, "user_id": h.seed.UserID, "provider_scope_id": h.seed.ScopeID, "channel_id": h.seed.ChannelID, "admin_actor_ids": h.seed.Actors}, nil
	case "create":
		d, e := h.create(c)
		return "api_result", d, e
	case "api":
		d, e := h.api(c)
		return "api_result", d, e
	case "inspect":
		data["label"] = c.Label
		return "snapshot", data, nil
	case "direct":
		d, e := h.direct(c)
		return "direct_result", d, e
	case "barrier":
		d, e := h.barrierCommand(c)
		return "snapshot", d, e
	case "admin", "race_admin_B":
		return "", nil, errPhaseHarness // server-only, never fake success
	}
	return "", nil, errPhaseHarness
}

func (h *phaseHarness) publicHTTP(method, path, idem string, body []byte) (map[string]any, []byte, error) {
	if h.seed.Protocol == "" {
		return nil, nil, errPhaseHarness
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:45001"
	req.Header.Set("Authorization", "Bearer "+h.seed.PlainAPIKey)
	req.Header.Set("Content-Type", "application/json")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	raw := append([]byte(nil), w.Body.Bytes()...)
	if len(raw) > 1<<20 {
		return nil, nil, errPhaseHarness
	}
	sum := sha256.Sum256(raw)
	code := ""
	var decoded struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &decoded) == nil {
		code = decoded.Error.Code
	}
	d := map[string]any{"method": method, "http_status": w.Code, "error_code": code, "response_body_sha256": hex.EncodeToString(sum[:]), "request_id": w.Header().Get("X-Request-ID"), "replay": w.Header().Get("Idempotency-Replayed") == "true", "location_present": w.Header().Get("Location") != ""}
	return d, raw, nil
}
func (h *phaseHarness) create(c phaseCommand) (map[string]any, error) {
	item, exists := h.seed.Tasks[c.Task]
	if (c.Request == "new") == exists {
		return nil, errPhaseHarness
	}
	secondF04 := (h.e.caseID == "F04_inbox" || h.e.caseID == "F04_legacy") && c.Task == "legacy"
	if !strings.HasPrefix(h.e.caseID, "F01_") && c.Task != "primary" && !(h.e.caseID == "F06" && c.Task == "legacy") && !secondF04 {
		return nil, errPhaseHarness
	}
	oldSHA := ""
	if exists {
		oldSHA = item.BodySHA
	}
	body := phaseCreateBody(c.Task, c.Request == "changed_body")
	idem := "phase-" + h.e.run + "-" + c.Task
	if exists {
		idem = item.IdempotencyKey
	}
	d, raw, e := h.publicHTTP(http.MethodPost, "/v1/video/generations", idem, body)
	if e != nil {
		return nil, e
	}
	d["task"] = c.Task
	d["request"] = c.Request
	if d["http_status"] == 202 {
		var v TaskView
		if json.Unmarshal(raw, &v) != nil || v.ID == "" {
			return nil, errPhaseHarness
		}
		if exists {
			if v.ID != item.TaskID || d["replay"] != true || c.Request == "changed_body" {
				return nil, errPhaseHarness
			}
			d["original_body_sha256"] = oldSHA
		} else {
			if d["replay"] != false {
				return nil, errPhaseHarness
			}
			if e = h.rememberCreated(c.Task, v.ID, body, idem); e != nil {
				return nil, e
			}
		}
		d["task_id"] = v.ID
	} else if c.Request == "new" { // A denied admission is evidence, not a created handle.
		d["task_id"] = ""
	}
	return d, nil
}
func (h *phaseHarness) api(c phaseCommand) (map[string]any, error) {
	item, ok := h.seed.Tasks[c.Task]
	if !ok {
		return nil, errPhaseHarness
	}
	method, path, idem := http.MethodGet, "/v1/video/generations/"+item.TaskID, ""
	var body []byte
	switch c.Action {
	case "get":
	case "list":
		path = "/v1/video/generations"
	case "content", "billing", "events":
		path += "/" + c.Action
	case "operation":
		path = "/v1/video/operations/" + item.OperationID
	case "cancel":
		method = http.MethodPost
		path += "/cancel"
		idem = "phase-cancel-" + h.e.run + "-" + c.Task
		// The public cancellation contract requires an empty request body.
	case "delete":
		method = http.MethodDelete
		idem = "phase-delete-" + h.e.run + "-" + c.Task
	default:
		return nil, errPhaseHarness
	}
	d, raw, e := h.publicHTTP(method, path, idem, body)
	if e != nil {
		return nil, e
	}
	d["task"], d["action"] = c.Task, c.Action
	// Raw content/URL never enters events, but a safe boolean distinguishes old
	// N's metadata-only response from accidental result URL disclosure.
	var view any
	if json.Unmarshal(raw, &view) == nil {
		d["delivery_url_present"] = phaseContainsURL(view)
	} else {
		d["delivery_url_present"] = false
	}
	if e = h.refreshSeed(); e != nil {
		return nil, e
	}
	return d, nil
}
func phaseContainsURL(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://")
	case []any:
		for _, z := range x {
			if phaseContainsURL(z) {
				return true
			}
		}
	case map[string]any:
		for _, z := range x {
			if phaseContainsURL(z) {
				return true
			}
		}
	}
	return false
}

func (h *phaseHarness) executeCycle(cycle *phaseCycle) error {
	c := cycle.command
	var actual error
	capability := true
	switch c.Lane {
	case "dispatch":
		actual = h.s.ProcessOnce(cycle.ctx)
	case "evidence":
		consumer, ok := any(h.s).(phaseEvidenceConsumer)
		if !ok {
			capability = false
		} else {
			actual = consumer.ProcessPersistedEvidenceOnce(cycle.ctx)
		}
	case "settlement":
		consumer, ok := any(h.s).(phaseSettlementConsumer)
		if !ok {
			capability = false
		} else {
			actual = consumer.ProcessSettlementOnce(cycle.ctx)
		}
	default:
		return errPhaseHarness
	}
	if e := h.refreshSeed(); e != nil {
		return e
	}
	proof, e := h.inspect()
	if e != nil {
		return e
	}
	data := map[string]any{"command": "cycle", "lane": c.Lane, "capability_present": capability, "returned_error_classes": phaseErrorClasses(actual), "context_error_classes": phaseErrorClasses(cycle.ctx.Err()), "outcome": "returned"}
	if !capability {
		data["outcome"] = "capability_absent"
	}
	h.mu.Lock()
	b := h.barrier
	cancelledBarrier := b != nil && b.reached && b.boundary == "receipt_committed_before_G" && b.released
	h.mu.Unlock()
	if cancelledBarrier {
		if !errors.Is(cycle.ctx.Err(), context.Canceled) || !phasePendingProof(proof, b.task, h.e.caseID == "F03_create_pending") || proof.FinancialSHA != b.finance || proof.Network != b.network || b.proof == nil {
			return errPhaseHarness
		}
		for _, table := range []string{"tasks", "video_task_states", "video_operations", "video_attempts", "video_execution_attempts", "video_response_inbox", "video_receipt_applications", "video_query_receipts", "video_events", "video_outbox"} {
			if proof.Tables[table] != b.proof.Tables[table] {
				return errPhaseHarness
			}
		}
		data["outcome"] = "cancelled_after_receipt_commit"
	}
	return h.emit(c.ID, "cycle_result", data, &proof)
}
func (h *phaseHarness) direct(c phaseCommand) (map[string]any, error) {
	if c.Target == "purge_history" {
		return h.purgeHistory(c)
	}
	item, ok := h.seed.Tasks[c.Task]
	if !ok {
		return nil, errPhaseHarness
	}
	org, e := phaseID(h.seed.OrgID)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var actual error
	switch c.Target {
	case "query_receipt":
		var r QueryReceipt
		if item.QueryAttemptID == "" {
			return nil, errPhaseHarness
		}
		if e = h.db.WithContext(ctx).Where("org_id=? AND task_id=? AND attempt_id=?", org, item.TaskID, item.QueryAttemptID).Take(&r).Error; e != nil {
			return nil, e
		}
		actual = h.s.applyQueryReceipt(ctx, r)
	case "create_receipt", "mutation_receipt":
		kinds := []string{"create_task"}
		if c.Target == "mutation_receipt" {
			kinds = []string{"cancel_task", "delete_task"}
		}
		var ops []Operation
		if e = h.db.WithContext(ctx).Where("org_id=? AND resource_id=? AND kind IN ?", org, item.TaskID, kinds).Order("created_at DESC,id DESC").Limit(1).Find(&ops).Error; e != nil || len(ops) != 1 {
			return nil, errPhaseHarness
		}
		op := ops[0]
		var rows []ResponseInbox
		if e = h.db.WithContext(ctx).Where("org_id=? AND task_id=? AND operation_id=?", org, item.TaskID, op.ID).Order("received_at DESC,receipt_id DESC").Limit(1).Find(&rows).Error; e != nil {
			return nil, e
		}
		if len(rows) == 1 {
			if c.Target == "create_receipt" {
				actual = h.s.applyCreateInbox(ctx, org, rows[0].ReceiptID, nil)
			} else {
				actual = h.s.applyMutationInbox(ctx, org, rows[0].ReceiptID, nil)
			}
		} else {
			if op.ReceiptRef == "" {
				return nil, errPhaseHarness
			}
			if c.Target == "create_receipt" {
				actual = h.s.applyCreateReceipt(ctx, op)
			} else {
				actual = h.s.applyMutationReceipt(ctx, op)
			}
		}
	case "delivery_refresh":
		var row task.Task
		var st State
		if e = h.db.WithContext(ctx).Where("org_id=? AND task_id=?", org, item.TaskID).Take(&row).Error; e != nil {
			return nil, e
		}
		if e = h.db.WithContext(ctx).Where("org_id=? AND task_id=?", org, item.TaskID).Take(&st).Error; e != nil {
			return nil, e
		}
		actual = h.s.refreshDelivery(ctx, row, st)
	default:
		return nil, errPhaseHarness
	}
	if e = h.refreshSeed(); e != nil {
		return nil, e
	}
	return map[string]any{"command": "direct", "task": c.Task, "target": c.Target, "returned_error_classes": phaseErrorClasses(actual), "context_error_classes": phaseErrorClasses(ctx.Err())}, nil
}

type phaseCommitPool struct {
	*sql.DB
	h *phaseHarness
}

func (p *phaseCommitPool) BeginTx(ctx context.Context, o *sql.TxOptions) (gorm.ConnPool, error) {
	tx, e := p.DB.BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	return &phaseCommitTx{Tx: tx, h: p.h}, nil
}
func (p *phaseCommitPool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

type phaseCommitTx struct {
	*sql.Tx
	h *phaseHarness
}

func (tx *phaseCommitTx) Commit() error {
	e := tx.Tx.Commit()
	if e == nil {
		tx.h.afterCommit()
	}
	return e
}

func phasePendingProof(proof phaseSnapshot, handle string, createReceipt bool) bool {
	p, ok := proof.Tasks[handle]
	if !ok {
		return false
	}
	receipts, ok := p["receipts"].([]map[string]any)
	if !ok {
		return false
	}
	if !createReceipt {
		for _, r := range receipts {
			if r["applied_at"] == nil && r["generation_applied_at"] == nil && r["recovery_version"] == "0" {
				return true
			}
		}
		return false
	}
	// Create inbox receipt has independently committed before its reducer.
	storage, ok := p["storage"].(map[string]phaseTableProof)
	if !ok {
		return false
	}
	ops, _ := p["operations"].([]map[string]any)
	return storage["video_response_inbox"].Rows == 1 && len(ops) == 1 && ops[0]["receipt_applied_at"] == nil && p["task_status"] == "submitting"
}
func phaseGeneratedProof(proof phaseSnapshot, handle string) bool {
	p, ok := proof.Tasks[handle]
	if !ok {
		return false
	}
	pin, ok := p["settlement_receipt_id"].(*string)
	if !ok || pin == nil {
		return false
	}
	receipts, _ := p["receipts"].([]map[string]any)
	for _, r := range receipts {
		if r["attempt_id"] == *pin && r["generation_applied_at"] != nil && r["applied_at"] == nil && r["recovery_version"] == "1" && r["settlement_kind"] == "usage" && r["stored_outer_sha256"] == r["outer_evidence_sha256"] {
			return true
		}
	}
	return false
}
func (h *phaseHarness) afterCommit() {
	h.mu.Lock()
	b, cycle := h.barrier, h.active
	if b == nil || cycle == nil || b.reached {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	proof, e := h.inspect()
	if e != nil {
		h.mu.Lock()
		h.fatal = true
		h.mu.Unlock()
		cycle.cancel()
		return
	}
	matched := false
	if b.boundary == "receipt_committed_before_G" {
		matched = phasePendingProof(proof, b.task, h.e.caseID == "F03_create_pending")
	} else {
		matched = phaseGeneratedProof(proof, b.task)
	}
	if !matched {
		return
	}
	if proof.FinancialSHA != b.finance {
		h.mu.Lock()
		h.fatal = true
		h.mu.Unlock()
		cycle.cancel()
		return
	}
	h.mu.Lock()
	if h.barrier != b || h.active != cycle || b.reached {
		h.mu.Unlock()
		return
	}
	b.reached = true
	b.network = proof.Network
	b.proof = &proof
	h.mu.Unlock()
	if e = h.emit(cycle.command.ID, "barrier_reached", map[string]any{"boundary": b.boundary, "task": b.task, "armed_cmd_id": b.armedID, "commit_returned": true, "independent_connection": true}, &proof); e != nil {
		h.mu.Lock()
		h.fatal = true
		h.mu.Unlock()
		cycle.cancel()
		return
	}
	select {
	case <-b.release:
	case <-cycle.ctx.Done():
	}
}
func (h *phaseHarness) barrierCommand(c phaseCommand) (map[string]any, error) {
	if _, ok := h.seed.Tasks[c.Task]; !ok {
		return nil, errPhaseHarness
	}
	if c.Action == "arm" {
		proof, e := h.inspect()
		if e != nil {
			return nil, e
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.barrier != nil {
			return nil, errPhaseHarness
		}
		h.barrier = &phaseBarrier{task: c.Task, boundary: c.Boundary, finance: proof.FinancialSHA, armedID: c.ID, release: make(chan struct{})}
		return map[string]any{"command": "barrier", "action": "arm", "boundary": c.Boundary, "task": c.Task}, nil
	}
	h.mu.Lock()
	b, cycle := h.barrier, h.active
	if b == nil || cycle == nil || b.task != c.Task || b.boundary != c.Boundary || !b.reached || b.released {
		h.mu.Unlock()
		return nil, errPhaseHarness
	}
	if b.boundary == "receipt_committed_before_G" {
		cycle.cancel()
	}
	b.released = true
	close(b.release)
	h.mu.Unlock()
	select {
	case <-cycle.done:
	case <-time.After(7 * time.Second):
		return nil, errPhaseHarness
	}
	if cycle.err != nil {
		return nil, cycle.err
	}
	h.mu.Lock()
	h.active = nil
	h.barrier = nil
	h.mu.Unlock()
	return map[string]any{"command": "barrier", "action": "release", "boundary": c.Boundary, "task": c.Task, "joined": true}, nil
}

func (h *phaseHarness) purgeHistory(c phaseCommand) (map[string]any, error) {
	if c.Case != "F05_delivery" || c.Task != "primary" {
		return nil, errPhaseHarness
	}
	item, ok := h.seed.Tasks[c.Task]
	if !ok || item.QueryAttemptID == "" {
		return nil, errPhaseHarness
	}
	org, e := phaseID(h.seed.OrgID)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var receipt QueryReceipt
	var meta invocation.VideoPayload
	if e = h.verify.WithContext(ctx).Where("org_id=? AND task_id=? AND attempt_id=?", org, item.TaskID, item.QueryAttemptID).Take(&receipt).Error; e != nil {
		return nil, e
	}
	if e = h.verify.WithContext(ctx).Where("org_id=? AND payload_id=?", org, receipt.PayloadRef).Take(&meta).Error; e != nil {
		return nil, e
	}
	if meta.Purpose != invocation.VideoPurposeHistory || meta.WrappedKey == "" || len(meta.Nonce) == 0 {
		return nil, errPhaseHarness
	}
	before, e := h.inspect()
	if e != nil {
		return nil, e
	}
	others := map[string]phaseTableProof{}
	for _, table := range []string{"invocation_payloads", "invocation_payload_staging"} {
		p, e := h.tableProof(h.verify, table, "payload_id <> ?", receipt.PayloadRef)
		if e != nil {
			return nil, e
		}
		others[table] = p
	}
	purger, e := invocation.NewVideoStore(h.db, h.s.Keyring, invocation.VideoStoreOptions{Now: func() time.Time { return meta.ExpiresAt.Add(time.Second) }})
	if e != nil {
		return nil, e
	}
	name := "phase_purge_one_history"
	if e = h.db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement != nil && q.Statement.Table == "invocation_payloads" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "org_id", Value: org}, clause.Eq{Column: "payload_id", Value: receipt.PayloadRef}}})
		}
	}); e != nil {
		return nil, e
	}
	n, actual := purger.PurgeExpired(ctx, 100)
	remove := h.db.Callback().Query().Remove(name)
	if actual != nil || remove != nil || n != 1 {
		return nil, errPhaseHarness
	}
	var erased invocation.VideoPayload
	if e = h.verify.WithContext(ctx).Where("org_id=? AND payload_id=?", org, receipt.PayloadRef).Take(&erased).Error; e != nil {
		return nil, e
	}
	if erased.CaptureStatus != invocation.VideoCaptureExpired || erased.WrappedKey != "" || len(erased.Nonce) != 0 || !erased.ExpiresAt.Equal(meta.ExpiresAt) {
		return nil, errPhaseHarness
	}
	var staging int64
	if e = h.verify.Model(&invocation.VideoPayloadStaging{}).Where("org_id=? AND payload_id=?", org, receipt.PayloadRef).Count(&staging).Error; e != nil || staging != 0 {
		return nil, errPhaseHarness
	}
	for table, want := range others {
		p, e := h.tableProof(h.verify, table, "payload_id <> ?", receipt.PayloadRef)
		if e != nil || p != want {
			return nil, errPhaseHarness
		}
	}
	after, e := h.inspect()
	if e != nil || before.FinancialSHA != after.FinancialSHA || before.Tables["video_query_receipts"] != after.Tables["video_query_receipts"] {
		return nil, errPhaseHarness
	}
	return map[string]any{"command": "direct", "target": "purge_history", "task": c.Task, "purged": n, "payload_ref": receipt.PayloadRef, "history_policy_clock": meta.ExpiresAt.Add(time.Second).UTC().Format(time.RFC3339Nano), "original_expires_at": meta.ExpiresAt.UTC().Format(time.RFC3339Nano), "other_payloads": others, "returned_error_classes": []string{}, "context_error_classes": []string{}}, nil
}
