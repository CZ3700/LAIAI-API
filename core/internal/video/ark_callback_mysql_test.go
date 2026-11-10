package video

import (
	"context"
	"fmt"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
)

// P0 回归（2026-11-06）：failed/submission_unknown + pending_usage 超过 7 天
// 的任务由兜底 sweep 释放退款；未到期的保持冻结。
func TestMySQLStalePendingUsageSweepReleases(t *testing.T) {
	f := mysqlFixture(t)
	old := f.create(t, "sweep-old")
	keep := f.create(t, "sweep-keep")
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			seq := f.posts.Add(1)
			return reply(200, fmt.Sprintf(`{"id":"up_sweep_%d"}`, seq)), nil
		}
		return reply(200, `{}`), nil
	}
	f.dispatch(t, old.ID)
	f.dispatch(t, keep.ID)
	must(t, f.s.DB.Exec("UPDATE tasks SET status = 'failed', finished_at = ? WHERE org_id = ? AND task_id = ?", f.now.Add(-8*24*time.Hour), f.key.OrgID, old.ID).Error)
	must(t, f.s.DB.Exec("UPDATE tasks SET status = 'failed', finished_at = ? WHERE org_id = ? AND task_id = ?", f.now.Add(-24*time.Hour), f.key.OrgID, keep.ID).Error)
	for _, id := range []string{old.ID, keep.ID} {
		must(t, f.s.DB.Exec("UPDATE video_task_states SET billing_state = 'pending_usage' WHERE org_id = ? AND task_id = ?", f.key.OrgID, id).Error)
	}
	// 共享测试库的历史积压会让 Limit(100) 装不下本测试的候选：先把其他 org
	// 的 failed+pending_usage 让出（历史运行遗留，共享库不做清理）。
	must(t, f.s.DB.Exec("UPDATE video_task_states v JOIN tasks t ON t.org_id = v.org_id AND t.task_id = v.task_id SET v.billing_state = 'released' WHERE v.billing_state = 'pending_usage' AND t.org_id <> ?", f.key.OrgID).Error)
	must(t, f.s.ReleaseStalePendingUsage(context.Background()))
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, old.ID).First(&st).Error)
	if st.BillingState != "released" {
		t.Fatalf("stale task not released: %s", st.BillingState)
	}
	if !st.NextPollAt.IsZero() {
		t.Fatal("released task still carries a poll schedule")
	}
	var stKeep State
	if e := f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, keep.ID).First(&stKeep).Error; e != nil {
		var dump []State
		must(t, f.s.DB.Unscoped().Where("org_id = ?", f.key.OrgID).Find(&dump).Error)
		t.Fatalf("keep state missing: id=%q org=%d err=%v; ids=%v", keep.ID, f.key.OrgID, e, []string{old.ID, keep.ID})
	}
	if stKeep.BillingState != "pending_usage" {
		t.Fatalf("fresh pending_usage was swept: %s", stKeep.BillingState)
	}
}

func taskRowFor(t *testing.T, f *fixture, id string) task.Task {
	t.Helper()
	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).First(&row).Error)
	return row
}

func postHint(t *testing.T, f *fixture, handler gin.HandlerFunc, taskID, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/video/ark-callbacks/:task_id/:token", handler)
	req := httptest.NewRequest(http.MethodPost, "/v1/video/ark-callbacks/"+taskID+"/"+token, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func hintCount(t *testing.T, f *fixture, taskID string) int64 {
	t.Helper()
	var n int64
	f.s.DB.Model(&CallbackHint{}).Where("task_id = ?", taskID).Count(&n)
	return n
}

// 回调端点安全面：错误 token → 404 且不落 hint；正确 token → 200 落 hint；
// 重复推送原子更新同一任务的 hint。
func TestMySQLArkCallbackEndpointAuth(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ArkCallbackBase = "https://callback.example"
	v := f.create(t, "callback-auth")
	token := f.s.arkCallbackToken(v.ID)
	if w := postHint(t, f, f.s.HandleArkCallback, v.ID, strings.Repeat("f", 32), `{"id":"cgt-x"}`); w.Code != 404 {
		t.Fatalf("forged token = %d", w.Code)
	}
	if n := hintCount(t, f, v.ID); n != 0 {
		t.Fatal("forged token left a hint")
	}
	if w := postHint(t, f, f.s.HandleArkCallback, v.ID, token, `{"id":"cgt-x","status":"succeeded"}`); w.Code != 200 {
		t.Fatalf("valid token = %d", w.Code)
	}
	if w := postHint(t, f, f.s.HandleArkCallback, v.ID, token, `{"id":"cgt-x","status":"succeeded"}`); w.Code != 200 {
		t.Fatalf("duplicate = %d", w.Code)
	}
	if n := hintCount(t, f, v.ID); n != 1 {
		t.Fatalf("hints = %d, want 1 (duplicate collapsed)", n)
	}
	large := `{"padding":"` + strings.Repeat("x", 5000) + `","id":"cgt-large","safety_identifier":"proof"}`
	if w := postHint(t, f, f.s.HandleArkCallback, v.ID, token, large); w.Code != 200 {
		t.Fatalf("large hint = %d", w.Code)
	}
	var hint CallbackHint
	must(t, f.s.DB.Where("task_id = ?", v.ID).First(&hint).Error)
	if hint.BodyExcerpt != `{"id":"cgt-large","safety_identifier":"proof"}` {
		t.Fatalf("large hint lost recovery identity: %s", hint.BodyExcerpt)
	}
}

func TestMySQLArkCallbackRevisionPreservesNewDelivery(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ArkCallbackBase = "https://callback.example"
	v := f.create(t, "callback-revision")
	token := f.s.arkCallbackToken(v.ID)
	if w := postHint(t, f, f.s.HandleArkCallback, v.ID, token, `{"id":"first"}`); w.Code != 200 {
		t.Fatalf("first hint = %d", w.Code)
	}
	var old CallbackHint
	must(t, f.s.DB.Where("task_id = ?", v.ID).First(&old).Error)
	if w := postHint(t, f, f.s.HandleArkCallback, v.ID, token, `{"id":"second"}`); w.Code != 200 {
		t.Fatalf("second hint = %d", w.Code)
	}
	must(t, f.s.finishCallbackHint(context.Background(), old, callbackHintOutcome{result: "ack_final"}))
	var latest CallbackHint
	must(t, f.s.DB.Where("task_id = ?", v.ID).First(&latest).Error)
	if latest.ID != old.ID || latest.Revision <= old.Revision || latest.ProcessedAt != nil || latest.BodyExcerpt != `{"id":"second"}` {
		t.Fatalf("new delivery was lost after stale ACK: old=%+v latest=%+v", old, latest)
	}
	if n := hintCount(t, f, v.ID); n != 1 {
		t.Fatalf("hints = %d, want 1", n)
	}
}

func TestMySQLArkCallbackStoreFailureReturnsRetryableStatus(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ArkCallbackBase = "https://callback.example"
	v := f.create(t, "callback-store-failure")
	original := f.s.DB
	tx := original.Begin()
	must(t, tx.Error)
	must(t, tx.Rollback().Error)
	f.s.DB = tx
	defer func() { f.s.DB = original }()
	if w := postHint(t, f, f.s.HandleArkCallback, v.ID, f.s.arkCallbackToken(v.ID), `{"id":"cgt-x"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed persistence status = %d, want 503", w.Code)
	}
}

func TestMySQLArkCallbackWatchdogFallsBackToGet(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ArkCallbackBase = "https://callback.example"
	v := f.create(t, "callback-watchdog")
	f.dispatch(t, v.ID)
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	if st.NextPollAt.Before(time.Now().Add(100*time.Second)) || st.NextPollAt.After(time.Now().Add(140*time.Second)) {
		t.Fatalf("watchdog not about two minutes after create: %s", st.NextPollAt)
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	if f.gets.Load() != 0 {
		t.Fatal("normal query ran before callback watchdog due")
	}
	must(t, f.s.DB.Exec("UPDATE video_task_states SET next_poll_at = DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND) WHERE org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Error)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	if f.gets.Load() != 1 || taskRowFor(t, f, v.ID).Status != "succeeded" {
		t.Fatal("watchdog did not recover via authoritative GET")
	}
}

func TestMySQLArkCallbackDuringInflightQueryStaysPending(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ArkCallbackBase = "https://callback.example"
	v := f.create(t, "callback-inflight")
	f.dispatch(t, v.ID)
	row := taskRowFor(t, f, v.ID)
	must(t, f.s.DB.Exec("UPDATE video_task_states SET query_lease_until = DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 MINUTE) WHERE org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Error)
	if w := postHint(t, f, f.s.HandleArkCallback, v.ID, f.s.arkCallbackToken(v.ID), `{"id":"`+row.UpstreamTaskID+`","status":"succeeded"}`); w.Code != 200 {
		t.Fatalf("hint = %d", w.Code)
	}
	must(t, f.s.ProcessCallbackHints(context.Background()))
	var pending CallbackHint
	must(t, f.s.DB.Where("task_id = ?", v.ID).First(&pending).Error)
	if pending.ProcessedAt != nil || pending.RetryAt == nil || pending.Result != "query_inflight" {
		t.Fatalf("in-flight query consumed callback: %+v", pending)
	}
	must(t, f.s.DB.Exec("UPDATE video_task_states SET query_lease_until = NULL WHERE org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Error)
	must(t, f.s.DB.Exec("UPDATE video_callback_hints SET retry_at = DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND) WHERE task_id = ?", v.ID).Error)
	must(t, f.s.ProcessCallbackHints(context.Background()))
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	if st.NextPollAt.After(time.Now().Add(2 * time.Second)) {
		t.Fatalf("callback not scheduled after in-flight query: %s", st.NextPollAt)
	}
}

// P1 全链路回归（缩小版）：正常任务（有 id）hint 触发权威查询；
// submission_unknown（无 id）hint 三重证明后绑定 id，随后轮询照常结算。
func TestMySQLArkCallbackUnknownRecovery(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ArkCallbackBase = "https://callback.example"

	// ① 正常任务：dispatch 成功（有 id），回调到达 → 查询立即到期，
	// 仍由正常 GET 管道做状态和资金结算。
	bound := f.create(t, "callback-bound")
	f.dispatch(t, bound.ID)
	boundRow := taskRowFor(t, f, bound.ID)
	var before State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, bound.ID).First(&before).Error)
	if before.NextPollAt.Before(time.Now().Add(100 * time.Second)) {
		t.Fatalf("callback watchdog scheduled too early: %s", before.NextPollAt)
	}
	if w := postHint(t, f, f.s.HandleArkCallback, bound.ID, f.s.arkCallbackToken(bound.ID), `{"id":"`+boundRow.UpstreamTaskID+`","status":"succeeded"}`); w.Code != 200 {
		t.Fatalf("bound hint = %d", w.Code)
	}
	must(t, f.s.ProcessCallbackHints(context.Background()))
	if row := taskRowFor(t, f, bound.ID); row.Status != "queued" {
		t.Fatalf("bound hint bypassed authoritative polling: %s", row.Status)
	}
	var due State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, bound.ID).First(&due).Error)
	if due.NextPollAt.After(time.Now().Add(2 * time.Second)) {
		t.Fatalf("bound hint failed to trigger prompt GET: %s", due.NextPollAt)
	}
	// The shared MySQL fixture may contain more than one bounded worker batch
	// of older due tasks. Poll this task directly to verify the callback's
	// authoritative GET and settlement path without depending on queue rank.
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, bound.ID))
	if row := taskRowFor(t, f, bound.ID); row.Status != "succeeded" {
		t.Fatalf("authoritative GET did not settle bound task: %s", row.Status)
	}

	// ② 提交超时 → submission_unknown（无 id）：带 id + safety_identifier 的
	// 回调 → 三重证明 → 绑定 → 轮询管道权威结算。
	unknown := f.create(t, "callback-unknown")
	upstream := "cgt_recovery_" + unknown.ID
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return reply(200, `{"id":"`+upstream+`","status":"succeeded","usage":{"completion_tokens":100,"total_tokens":999},"safety_identifier":"`+f.s.arkSafetyIdentifier(unknown.ID)+`"}`), nil
		}
		return reply(200, `{"id":"up_initial"}`), nil
	}
	f.dispatch(t, unknown.ID)
	// 直接构造线上存量形态：submission_unknown + 无上游绑定 + pending_usage。
	must(t, f.s.DB.Exec("UPDATE tasks SET status = 'submission_unknown', upstream_task_id = '' WHERE org_id = ? AND task_id = ?", f.key.OrgID, unknown.ID).Error)
	must(t, f.s.DB.Exec("UPDATE video_task_states SET billing_state = 'pending_usage' WHERE org_id = ? AND task_id = ?", f.key.OrgID, unknown.ID).Error)
	must(t, f.s.DB.Exec("DELETE FROM video_upstream_bindings WHERE org_id = ? AND task_id = ?", f.key.OrgID, unknown.ID).Error)
	if row := taskRowFor(t, f, unknown.ID); row.Status != "submission_unknown" || row.UpstreamTaskID != "" {
		t.Fatalf("fixture not unknown: status=%s upstream=%q", row.Status, row.UpstreamTaskID)
	}
	// 伪造 safety_identifier：不绑定。
	if w := postHint(t, f, f.s.HandleArkCallback, unknown.ID, f.s.arkCallbackToken(unknown.ID), `{"id":"`+upstream+`","safety_identifier":"forged"}`); w.Code != 200 {
		t.Fatalf("forged si hint = %d", w.Code)
	}
	must(t, f.s.ProcessCallbackHints(context.Background()))
	if row := taskRowFor(t, f, unknown.ID); row.UpstreamTaskID != "" {
		t.Fatal("forged safety_identifier still bound")
	}
	// 正确 safety_identifier：绑定 + 权威查询 + 轮询结算。
	cb := `{"id":"` + upstream + `","status":"succeeded","safety_identifier":"` + f.s.arkSafetyIdentifier(unknown.ID) + `"}`
	if w := postHint(t, f, f.s.HandleArkCallback, unknown.ID, f.s.arkCallbackToken(unknown.ID), cb); w.Code != 200 {
		t.Fatalf("valid hint = %d", w.Code)
	}
	// The prior verification may have consumed the provider query slot. Make
	// this test's slot due rather than depending on wall-clock timing.
	must(t, f.s.DB.Exec("UPDATE video_provider_rates SET next_allowed_at = DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND) WHERE action = 'query'").Error)
	must(t, f.s.ProcessCallbackHints(context.Background()))
	if row := taskRowFor(t, f, unknown.ID); row.UpstreamTaskID != upstream {
		t.Fatalf("unknown task not bound: upstream=%q", row.UpstreamTaskID)
	}
	must(t, f.s.DB.Exec("UPDATE video_provider_rates SET next_allowed_at = DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND) WHERE action = 'query'").Error)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, unknown.ID))
	if row := taskRowFor(t, f, unknown.ID); row.Status != "succeeded" {
		t.Fatalf("recovered task did not settle: %s", row.Status)
	}
}

// 受理注入：开启特性后，提交体携带派生的 callback_url/safety_identifier；
// 特性关闭时不注入。
func TestMySQLCreateInjectsCallbackAnnotations(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "callback-injected-off")
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = 'create_task'", f.key.OrgID, v.ID).First(&op).Error)
	body := executionBody(t, f, v, op)
	if strings.Contains(body, "callback_url") || strings.Contains(body, "safety_identifier") {
		t.Fatal("annotations injected while feature disabled")
	}

	f.s.ArkCallbackBase = "https://callback.example"
	v2 := f.create(t, "callback-injected-on")
	var op2 Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = 'create_task'", f.key.OrgID, v2.ID).First(&op2).Error)
	body2 := executionBody(t, f, v2, op2)
	if !strings.Contains(body2, f.s.arkCallbackToken(v2.ID)) || !strings.Contains(body2, f.s.arkSafetyIdentifier(v2.ID)) {
		t.Fatalf("submit body lost the callback annotations: %s", body2)
	}
	if !strings.Contains(body2, "https://callback.example/v1/video/ark-callbacks/"+v2.ID+"/") {
		t.Fatalf("callback_url malformed: %s", body2)
	}
}

func executionBody(t *testing.T, f *fixture, v TaskView, op Operation) string {
	t.Helper()
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	body, e := f.s.Evidence.ReadForExecutionSchemaWithDatabaseTime(context.Background(), f.key.OrgID, st.EffectivePayloadRef, 1, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "test", ReasonCode: "dispatch", OperationID: op.ID})
	must(t, e)
	return string(body)
}
