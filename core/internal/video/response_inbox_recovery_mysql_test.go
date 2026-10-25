package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

func TestMySQLResponseInboxLegacyAndNewQueuesExcludeUnknownProtocolBeforeLimit(t *testing.T) {
	f := mysqlFixture(t)
	var unsupported []ResponseInbox
	// More than the consumer's batch limit: a post-LIMIT protocol check
	// would starve the supported receipt deliberately placed behind them.
	for i := range 31 {
		v := f.create(t, fmt.Sprintf("inbox-future-protocol-%d", i))
		attempt, _ := responseInboxRegister(t, f, v.ID)
		r := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
		must(t, f.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", attempt.OrgID, attempt.AttemptID).Update("storage_protocol", "future-inbox").Error)
		must(t, f.s.DB.Model(&ResponseInbox{}).Where("org_id = ? AND receipt_id = ?", r.OrgID, r.ReceiptID).Update("storage_protocol", "future-inbox").Error)
		must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", r.OrgID, r.ReceiptID).Update("next_attempt_at", f.now.Add(-2*time.Hour)).Error)
		unsupported = append(unsupported, r)
	}
	newTask := f.create(t, "inbox-supported-behind-future")
	newAttempt, newOp := responseInboxRegister(t, f, newTask.ID)
	supported := responseInboxAppend(t, f, newAttempt, responseInboxResult(ark.OutcomeAccepted, "up_"+newTask.ID))
	must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", supported.OrgID, supported.ReceiptID).Update("next_attempt_at", f.now.Add(-time.Hour)).Error)

	// Produce a genuine legacy HTTP receipt whose application rolls back.
	// It must be recovered by the legacy reducer only, without a second POST.
	legacyTask := f.create(t, "inbox-legacy-mixed")
	f.s.ResponseInboxEnabled = false
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			return nil, errors.New("unexpected query in creation recovery")
		}
		return reply(200, fmt.Sprintf(`{"id":%q}`, "up_"+legacyTask.ID)), nil
	}
	injected := errors.New("synthetic legacy apply failure")
	name := fmt.Sprintf("test:inbox-legacy-mix-%d", f.key.OrgID)
	fired := false
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_upstream_bindings" {
			fired = true
			tx.AddError(injected)
		}
	}))
	err := f.s.processOperation(context.Background(), runtimeWorkerCreateOperation(t, f, legacyTask.ID))
	must(t, f.s.DB.Callback().Create().Remove(name))
	if !fired || !errors.Is(err, injected) || f.posts.Load() != 1 {
		t.Fatal("legacy receipt fixture did not fail after one real Mock POST")
	}
	legacyOp := runtimeWorkerCreateOperation(t, f, legacyTask.ID)
	if legacyOp.ReceiptRef == "" || legacyOp.ReceiptAppliedAt != nil || responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", f.key.OrgID, legacyOp.ID) != 0 {
		t.Fatal("legacy fixture acquired new-protocol ownership or lost its receipt")
	}
	before := responseInboxFunds(t, f, newTask.ID)
	f.now = f.now.Add(4 * time.Minute)

	var legacy []Operation
	must(t, f.s.recoveryOperationCandidates(context.Background()).Where("o.org_id = ?", f.key.OrgID).Order("o.updated_at, o.id").Limit(30).Find(&legacy).Error)
	if len(legacy) != 1 || legacy[0].ID != legacyOp.ID {
		t.Fatal("new or unknown protocol entered the bounded legacy queue")
	}
	var candidates []ReceiptApplication
	must(t, f.s.responseInboxCandidates(context.Background()).Where("ap.org_id = ?", f.key.OrgID).Order("ap.next_attempt_at, ap.receipt_id").Limit(30).Scan(&candidates).Error)
	if len(candidates) != 1 || candidates[0].ReceiptID != supported.ReceiptID {
		t.Fatal("unknown protocols consumed the bounded new recovery queue")
	}
	if err := f.s.applyCreateReceipt(context.Background(), newOp); !errors.Is(err, ErrConflict) {
		t.Fatal("direct legacy application accepted a registered operation", err)
	}
	must(t, f.s.recoverOperations(context.Background()))
	if runtimeWorkerCreateOperation(t, f, legacyTask.ID).Status != "confirmed" || responseInboxApp(t, f, supported).State != "pending" {
		t.Fatal("legacy recovery did not remain disjoint from new evidence")
	}
	must(t, f.s.recoverResponseInbox(context.Background()))
	must(t, f.s.recoverResponseInbox(context.Background()))
	if responseInboxApp(t, f, supported).State != "applied" || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id IN ?", f.key.OrgID, []string{newTask.ID, legacyTask.ID}) != 2 {
		t.Fatal("mixed recovery did not converge to one binding per supported task")
	}
	for _, r := range unsupported {
		if responseInboxApp(t, f, r).State != "pending" || runtimeWorkerCreateOperation(t, f, r.TaskID).Status != "in_flight" || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, r.TaskID) != 0 {
			t.Fatal("unsupported registered attempt was interpreted or sent to legacy recovery")
		}
	}
	if responseInboxFunds(t, f, newTask.ID) != before || f.posts.Load() != 1 || f.gets.Load() != 0 {
		t.Fatal("mixed recovery repeated HTTP or changed a reservation")
	}
}

func TestMySQLResponseInboxExpiredTombstonesDoNotStarveLiveAttempts(t *testing.T) {
	f := mysqlFixture(t)
	var deleted []ExecutionAttempt
	for i := range 31 {
		v := f.create(t, fmt.Sprintf("inbox-expired-tombstone-%d", i))
		attempt, _ := responseInboxRegister(t, f, v.ID)
		must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("deleted_at", f.now).Error)
		// Sort all deleted attempts before the valid candidate at the same
		// expiry boundary, independent of their random attempt identifiers.
		must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, attempt.OperationID).Update("lease_until", f.now.Add(time.Minute)).Error)
		deleted = append(deleted, attempt)
	}
	v := f.create(t, "inbox-live-after-tombstones")
	_, _ = responseInboxRegister(t, f, v.ID)
	before := responseInboxFunds(t, f, v.ID)
	f.now = f.now.Add(4 * time.Minute)
	must(t, f.s.recoverExpiredInboxAttempts(context.Background()))
	if runtimeWorkerCreateOperation(t, f, v.ID).Status != "unknown" {
		t.Fatal("deleted attempts consumed the expired-attempt recovery limit")
	}
	for _, attempt := range deleted {
		if runtimeWorkerCreateOperation(t, f, attempt.TaskID).Status != "in_flight" {
			t.Fatal("no-response recovery revived or changed a tombstoned task")
		}
	}
	if responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("expired-attempt recovery resent or released reservations")
	}
}

func TestMySQLResponseInboxDifferentCurrentAttemptCannotTakeOldEvidence(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "inbox-current-attempt-replaced")
	attempt, op := responseInboxRegister(t, f, v.ID)
	r := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
	before := responseInboxFunds(t, f, v.ID)
	replacement, err := randomID("att_")
	must(t, err)
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Updates(map[string]any{"attempt_id": replacement, "version": op.Version + 1, "status": "unknown"}).Error)
	f.now = f.now.Add(4 * time.Minute)
	must(t, f.s.recoverResponseInbox(context.Background()))
	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&row).Error)
	app := responseInboxApp(t, f, r)
	if app.State != "review" || app.AppliedAt != nil || row.UpstreamTaskID != "" || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 0 || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("a different execution attempt consumed the original response or reservation")
	}
}

func TestMySQLResponseInboxTemporaryBlockersBackOffAndAllowNextBatch(t *testing.T) {
	f := mysqlFixture(t)
	var blocked []ResponseInbox
	inputs := map[string]bool{}
	for i := range 30 {
		v := f.create(t, fmt.Sprintf("inbox-retryable-blocker-%d", i))
		attempt, _ := responseInboxRegister(t, f, v.ID)
		r := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
		must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", r.OrgID, r.ReceiptID).Update("next_attempt_at", f.now.Add(-2*time.Hour)).Error)
		inputs[attempt.InputPayloadRef] = true
		blocked = append(blocked, r)
	}
	v := f.create(t, "inbox-after-retryable-blockers")
	attempt, _ := responseInboxRegister(t, f, v.ID)
	valid := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
	must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", valid.OrgID, valid.ReceiptID).Update("next_attempt_at", f.now.Add(-time.Hour)).Error)
	before := responseInboxFunds(t, f, v.ID)
	f.now = f.now.Add(4 * time.Minute)
	f.s.ResponseInboxEnabled = false
	name := fmt.Sprintf("test:inbox-temporary-read-%d", f.key.OrgID)
	fired := 0
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		meta, ok := tx.Statement.Dest.(*invocation.VideoPayload)
		if ok && meta.Purpose == invocation.VideoPurposeExecution && inputs[meta.PayloadID] {
			fired++
			tx.AddError(errors.New("synthetic temporary execution identity read failure"))
		}
	}))
	t.Cleanup(func() { _ = f.s.DB.Callback().Query().Remove(name) })
	must(t, f.s.recoverResponseInbox(context.Background()))
	if fired != 30 || responseInboxApp(t, f, valid).State != "pending" {
		t.Fatal("first batch did not exercise all temporary blockers before the valid receipt")
	}
	for _, r := range blocked {
		app := responseInboxApp(t, f, r)
		if app.State != "blocked" || app.ErrorCode != "execution_identity_unavailable" || responseInboxCount(t, f, &ReceiptApplication{}, "org_id = ? AND receipt_id = ? AND next_attempt_at > UTC_TIMESTAMP(3)", r.OrgID, r.ReceiptID) != 1 {
			t.Fatal("retryable failure did not defer itself using database time")
		}
	}
	must(t, f.s.recoverResponseInbox(context.Background()))
	if responseInboxApp(t, f, valid).State != "applied" || fired != 30 {
		t.Fatal("deferred blockers starved the next compatible receipt or hot-looped")
	}
	// A recurring failure with the same code must renew its backoff too.
	prior := responseInboxApp(t, f, blocked[0])
	responseInboxMakeDue(t, f, blocked[0])
	if err := f.s.applyCreateInbox(context.Background(), blocked[0].OrgID, blocked[0].ReceiptID, nil); !errors.Is(err, invocation.ErrVideoStorage) {
		t.Fatal("temporary input metadata failure did not remain retryable", err)
	}
	after := responseInboxApp(t, f, blocked[0])
	if after.State != "blocked" || after.Version <= prior.Version || fired != 31 || responseInboxCount(t, f, &ReceiptApplication{}, "org_id = ? AND receipt_id = ? AND next_attempt_at > UTC_TIMESTAMP(3)", blocked[0].OrgID, blocked[0].ReceiptID) != 1 {
		t.Fatal("an unchanged failure code skipped renewing the recovery delay")
	}
	must(t, f.s.DB.Callback().Query().Remove(name))
	responseInboxMakeDue(t, f, blocked[0])
	must(t, f.s.recoverResponseInbox(context.Background()))
	if responseInboxApp(t, f, blocked[0]).State != "applied" || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("restored input metadata did not recover without another HTTP or money change")
	}
}

func TestMySQLResponseInboxUpstreamBindingConflictStopsAutomaticReplay(t *testing.T) {
	f := mysqlFixture(t)
	firstTask := f.create(t, "inbox-binding-owner")
	firstAttempt, firstOp := responseInboxRegister(t, f, firstTask.ID)
	first := responseInboxAppend(t, f, firstAttempt, responseInboxResult(ark.OutcomeAccepted, "up_"+firstTask.ID))
	must(t, f.s.applyClaimedCreateInbox(context.Background(), firstOp, first.ReceiptID))
	secondTask := f.create(t, "inbox-binding-conflict")
	secondAttempt, secondOp := responseInboxRegister(t, f, secondTask.ID)
	second := responseInboxAppend(t, f, secondAttempt, responseInboxResult(ark.OutcomeAccepted, first.UpstreamTaskID))
	before := responseInboxFunds(t, f, secondTask.ID)
	events := responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, secondTask.ID)
	err := f.s.applyClaimedCreateInbox(context.Background(), secondOp, second.ReceiptID)
	if !errors.Is(err, ErrConflict) {
		t.Fatal("a second task took ownership of the first task's provider ID", err)
	}
	app := responseInboxApp(t, f, second)
	if app.State != "review" || app.AppliedAt != nil || app.ErrorCode != "upstream_binding_conflict" {
		t.Fatal("binding conflict was left in the automatic retry queue")
	}
	f.now = f.now.Add(4 * time.Minute)
	must(t, f.s.recoverResponseInbox(context.Background()))
	must(t, f.s.recoverResponseInbox(context.Background()))
	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, secondTask.ID).Take(&row).Error)
	if row.UpstreamTaskID != "" || responseInboxApp(t, f, second).Version != app.Version || responseInboxCount(t, f, &UpstreamBinding{}, "provider_scope_id = ? AND upstream_task_id = ?", first.ProviderScopeID, first.UpstreamTaskID) != 1 || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, secondTask.ID) != 0 || responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, secondTask.ID) != events || responseInboxFunds(t, f, secondTask.ID) != before || f.posts.Load() != 0 {
		t.Fatal("permanent binding conflict partially applied or remained hot in recovery")
	}
}
