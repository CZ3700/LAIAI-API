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
// 重复推送按 (task_id, body_hash) 折叠。
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
}

// P1 全链路回归（缩小版）：正常任务（有 id）hint 仅 ACK 不动轮询；
// submission_unknown（无 id）hint 三重证明后绑定 id，随后轮询照常结算。
func TestMySQLArkCallbackUnknownRecovery(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ArkCallbackBase = "https://callback.example"

	// ① 正常任务：dispatch 成功（有 id），回调到达 → ACK，任务照旧。
	bound := f.create(t, "callback-bound")
	f.dispatch(t, bound.ID)
	boundRow := taskRowFor(t, f, bound.ID)
	if w := postHint(t, f, f.s.HandleArkCallback, bound.ID, f.s.arkCallbackToken(bound.ID), `{"id":"`+boundRow.UpstreamTaskID+`","status":"succeeded"}`); w.Code != 200 {
		t.Fatalf("bound hint = %d", w.Code)
	}
	must(t, f.s.ProcessCallbackHints(context.Background()))
	if row := taskRowFor(t, f, bound.ID); row.Status != "queued" {
		t.Fatalf("bound hint changed polling ownership: %s", row.Status)
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
	must(t, f.s.ProcessCallbackHints(context.Background()))
	if row := taskRowFor(t, f, unknown.ID); row.UpstreamTaskID != upstream {
		t.Fatalf("unknown task not bound: upstream=%q", row.UpstreamTaskID)
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, unknown.ID))
	if row := taskRowFor(t, f, unknown.ID); row.Status != "succeeded" {
		t.Fatalf("recovered task did not settle: %s", row.Status)
	}
	var forged int64
	must(t, f.s.DB.Model(&CallbackHint{}).Where("task_id = ? AND result = 'forged_si'", unknown.ID).Count(&forged).Error)
	if forged != 1 {
		t.Fatal("forged hint result not recorded")
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
