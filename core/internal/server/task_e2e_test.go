package server

import (
	"context"
	"encoding/json"
	"fmt"
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

	// 提交后：hold = 500k est × $10/1M = $5；余额可用 = 5
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("held after submit = %s, want 5", acc.HeldAmount)
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
	// 第 4 个被拒后资金无残渣：3 个 hold × $5 = $15 冻结，仅此而已
	acc := &ledger.Account{}
	_ = db.Raw("SELECT * FROM ledger_accounts LIMIT 1").Scan(acc)
	if !acc.HeldAmount.Equal(decimal.RequireFromString("15")) {
		t.Fatalf("held = %s, want 15 (3 holds only)", acc.HeldAmount)
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

// json 序列化不暴露内部字段（hold/渠道/上游 id 是平台内部信息）
func TestTaskJSONShape(t *testing.T) {
	raw, _ := json.Marshal(task.Task{TaskID: "task_x", HoldID: 9, ChannelID: 7, UpstreamTaskID: "cgt"})
	for _, internal := range []string{"hold_id", "channel_id", "upstream", "HoldID"} {
		if strings.Contains(string(raw), internal) {
			t.Fatalf("internal field %q leaked in task JSON: %s", internal, raw)
		}
	}
}
