package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// mockArk：可编程的 ModelArk 任务上游（提交计数 + 查询状态可切换）
type mockArk struct {
	srv        *httptest.Server
	submits    atomic.Int64
	queryState atomic.Value // string: JSON 响应体
}

func newMockArk(t *testing.T) *mockArk {
	t.Helper()
	m := &mockArk{}
	m.queryState.Store(`{"id":"cgt-1","status":"queued"}`)
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.Header.Get("Authorization") != "Bearer sk-upstream-secret" {
				t.Errorf("ark got credential %q", r.Header.Get("Authorization"))
			}
			n := m.submits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"cgt-%d"}`, n)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, m.queryState.Load().(string))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// setupTaskEnv：volcengine 渠道 + seedance 定价（$10/1M output，input 0 便于心算）+ task service
func setupTaskEnv(t *testing.T, srv *gin999, db *gorm.DB, arkURL string) *task.Service {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ark", "provider": "volcengine", "base_url": arkURL,
		"api_key": "sk-upstream-secret", "models": []string{"seedance-test"},
	})
	if w.Code != 201 {
		t.Fatalf("create ark channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "seedance-test", "input_usd_per_1m": "0", "output_usd_per_1m": "10",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	return task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func submitVideoTask(t *testing.T, srv *gin999, apiKey string) string {
	t.Helper()
	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model":   "seedance-test",
		"content": []map[string]any{{"type": "text", "text": "a cat surfing"}},
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	if out["status"] != task.StatusQueued {
		t.Fatalf("initial status = %v", out["status"])
	}
	return out["id"].(string)
}

func TestTaskLifecycleSuccess(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	svc := setupTaskEnv(t, srv, db, ark.srv.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "video@example.com", "10")

	taskID := submitVideoTask(t, srv, apiKey)

	// 提交后：hold = 315,900 est（默认 5s×1080p 保守基准×24fps/1024×1.3，真机标定+对抗 review
	// 封堵后的口径——无显式 resolution 按 1080p 覆盖家族默认差异与 prompt 指令）× $10/1M = $3.159
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.Equal(decimal.RequireFromString("3.159")) {
		t.Fatalf("held after submit = %s, want 3.159", acc.HeldAmount)
	}

	// 上游进入 running → PollOnce → 状态转移 + started_at
	ark.queryState.Store(`{"id":"cgt-1","status":"running"}`)
	svc.PollOnce(context.Background())
	w, out := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if w.Code != 200 || out["status"] != task.StatusRunning {
		t.Fatalf("after running poll: %d %v", w.Code, out)
	}

	// 上游成功：100k tokens → 实收 $1，差额 $4 自动解冻
	ark.queryState.Store(`{"id":"cgt-1","status":"succeeded",
		"content":{"video_url":"https://cdn.example.com/v.mp4"},
		"usage":{"completion_tokens":100000}}`)
	svc.PollOnce(context.Background())

	w, out = srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if w.Code != 200 || out["status"] != task.StatusSucceeded {
		t.Fatalf("after success poll: %d %v", w.Code, out)
	}
	if out["result_url"] != "https://cdn.example.com/v.mp4" {
		t.Fatalf("result_url = %v", out["result_url"])
	}
	if out["completion_tokens"] != float64(100000) {
		t.Fatalf("completion_tokens = %v", out["completion_tokens"])
	}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after settle: posted=%s held=%s, want 9/0", acc.PostedBalance, acc.HeldAmount)
	}

	// CAS 防双结：再 Poll 两轮（任务已终态不会再被查询/结算）
	svc.PollOnce(context.Background())
	svc.PollOnce(context.Background())
	var captures int64
	db.Model(&ledger.JournalEntry{}).Where("kind = ? AND ref_type = ?", ledger.KindCapture, "task").Count(&captures)
	if captures != 1 {
		t.Fatalf("task captures = %d, want exactly 1 (double settle!)", captures)
	}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9")) {
		t.Fatalf("balance drifted after repolls: %s", acc.PostedBalance)
	}
}

func TestTaskFailureFullRefund(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	svc := setupTaskEnv(t, srv, db, ark.srv.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "video-fail@example.com", "10")

	taskID := submitVideoTask(t, srv, apiKey)
	ark.queryState.Store(`{"id":"cgt-1","status":"failed","error":{"message":"content policy violation"}}`)
	svc.PollOnce(context.Background())

	w, out := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if w.Code != 200 || out["status"] != task.StatusFailed {
		t.Fatalf("failed task: %d %v", w.Code, out)
	}
	if out["fail_reason"] != "content policy violation" {
		t.Fatalf("fail_reason = %v", out["fail_reason"])
	}
	// 全退：余额 10，无冻结，无 capture
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("10")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after failure: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
	}
}

func TestTaskConcurrencyGate(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "video-gate@example.com", "100")

	for i := 0; i < 3; i++ { // 默认 max_concurrent_tasks = 3
		submitVideoTask(t, srv, apiKey)
	}
	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": "one too many"}},
	})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th submit = %d %v, want 429", w.Code, out)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 missing Retry-After")
	}
	// 第 4 个被拒后资金无残渣：3 个 hold × $3.159 = $9.477 冻结，仅此而已
	acc := &ledger.Account{}
	_ = db.Raw("SELECT * FROM ledger_accounts LIMIT 1").Scan(acc)
	if !acc.HeldAmount.Equal(decimal.RequireFromString("9.477")) {
		t.Fatalf("held = %s, want 9.477 (3 holds only)", acc.HeldAmount)
	}
	// key quota 同样无残渣（变异实测守卫）：被并发闸拒绝的第 4 笔必须退还预留，
	// 否则每次 429 永久泄漏 used_amount → 带 quota_limit 的 key 被虚耗额度收假 402
	var used decimal.Decimal
	_ = db.Raw("SELECT used_amount FROM api_keys ORDER BY id DESC LIMIT 1").Scan(&used)
	if !used.Equal(decimal.RequireFromString("9.477")) {
		t.Fatalf("used_amount = %s, want 9.477 (rejected 4th submit must refund key quota reserve)", used)
	}
}

func TestTaskTimeoutSweep(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	svc := setupTaskEnv(t, srv, db, ark.srv.URL)
	svc.TaskTimeout = time.Nanosecond // 立即视为超时
	apiKey, orgID := seedUserWithCredit(t, srv, "video-timeout@example.com", "10")

	taskID := submitVideoTask(t, srv, apiKey)
	time.Sleep(5 * time.Millisecond) // 跨过 submitted_at + 1ns
	svc.PollOnce(context.Background())

	w, out := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if w.Code != 200 || out["status"] != task.StatusFailed {
		t.Fatalf("timed-out task: %d %v", w.Code, out)
	}
	// 超时全退
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("10")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after timeout sweep: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
	}
}

func TestTaskOrgIsolation(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	apiKeyA, _ := seedUserWithCredit(t, srv, "video-a@example.com", "10")
	apiKeyB, _ := seedUserWithCredit(t, srv, "video-b@example.com", "10")

	taskID := submitVideoTask(t, srv, apiKeyA)
	w, _ := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKeyB, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-org task read = %d, want 404", w.Code)
	}
}

// Seedance 2.0 顶层参数透传（§3.8，真机验证 2026-07-05）：generate_audio/ratio/duration/
// watermark 直达上游提交体；hold 按 duration/resolution 感知缩放（1080p 11s > 默认档）。
func TestTaskParamPassthroughAndScaledHold(t *testing.T) {
	srv, db := setupRelayServer(t)
	var gotBody atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			gotBody.Store(m)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"cgt-p1"}`)
			return
		}
		fmt.Fprint(w, `{"id":"cgt-p1","status":"running"}`)
	}))
	defer up.Close()
	_ = setupTaskEnv(t, srv, db, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "video-params@example.com", "20")

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model":   "seedance-test",
		"content": []map[string]any{{"type": "text", "text": "apple ad"}},
		"generate_audio": true, "ratio": "16:9", "duration": 11,
		"resolution": "1080p", "watermark": false,
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit with params = %d %v", w.Code, out)
	}
	body, _ := gotBody.Load().(map[string]any)
	if body == nil {
		t.Fatal("upstream did not receive submit")
	}
	// 参数原样到达上游（此前会被静默丢弃——createReq 只绑 model/content）
	if body["generate_audio"] != true || body["ratio"] != "16:9" ||
		body["duration"] != float64(11) || body["watermark"] != false || body["resolution"] != "1080p" {
		t.Fatalf("params not passed through to upstream: %v", body)
	}
	if body["model"] != "seedance-test" {
		t.Fatalf("model = %v", body["model"])
	}
	// hold 按参数缩放：1080p 11s → 694,980 tokens × $10/1M = $6.9498（旧固定 500k 只会冻 $5，
	// 真实 usage ~534k 会 capture>hold）
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.Equal(decimal.RequireFromString("6.9498")) {
		t.Fatalf("held for 1080p 11s = %s, want 6.9498 (duration/resolution-aware)", acc.HeldAmount)
	}
}

// json 序列化不暴露内部字段（hold/渠道/上游 id 是平台内部信息）
func TestTaskJSONShape(t *testing.T) {
	raw, _ := json.Marshal(task.Task{TaskID: "task_x", HoldID: 9, ChannelID: 7, UpstreamTaskID: "cgt"})
	for _, internal := range []string{"hold_id", "channel_id", "upstream", "HoldID"} {
		if strings.Contains(string(raw), internal) {
			t.Fatalf("internal field %q leaked in task JSON: %s", internal, raw)
		}
	}
}
