package server

// §3.8 批次 2（kling/vidu）平台集成 e2e（mock 上游说各家方言）。证明 adaptor 正确接入框架：
// 凭证解密（kling ak|sk→JWT Bearer）、action 编码进 upstream_task_id 的落库往返、
// 按查询响应 usage（kling final_unit_deduction / vidu credits）精确结算。
// 真机形态验证走 real_batch2_task_smoke_test.go（env 门控）。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupBatch2Env(t *testing.T, srv *gin999, db *gorm.DB, provider, baseURL, apiKey, model string) *task.Service {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": provider, "provider": provider, "base_url": baseURL,
		"api_key": apiKey, "models": []string{model},
	})
	if w.Code != 201 {
		t.Fatalf("create %s channel = %d %v", provider, w.Code, out)
	}
	// 1M 伪 token = 1 厂商单位，$1/unit
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "0", "output_usd_per_1m": "1",
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	return task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

// Kling：ak|sk 凭证 → JWT Bearer；提交 /v1/videos/text2video（无图）；查询 URL 复用 action；
// succeed 带 final_unit_deduction "2" → 结算 2M token × $1/1M = $2。
func TestKlingTaskE2E(t *testing.T) {
	srv, db := setupRelayServer(t)
	var submitPath, queryPath, gotAuth, gotPrompt atomic.Value
	submitPath.Store("")
	queryPath.Store("")
	gotAuth.Store("")
	gotPrompt.Store("")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			submitPath.Store(r.URL.Path)
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotPrompt.Store(b["prompt"])
			_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"cgt-k1"}}`))
			return
		}
		queryPath.Store(r.URL.Path)
		_, _ = w.Write([]byte(`{"code":0,"data":{"task_status":"succeed",
			"task_result":{"videos":[{"url":"https://cdn.kling/v.mp4","duration":"5"}]},
			"final_unit_deduction":"2"}}`))
	}))
	defer up.Close()

	svc := setupBatch2Env(t, srv, db, "kling", up.URL, "my-ak|my-sk", "kling-v1")
	apiKey, orgID := seedUserWithCredit(t, srv, "kling-e2e@example.com", "50")

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "kling-v1", "content": []map[string]any{{"type": "text", "text": "a cat"}},
		"mode": "std", "duration": "5",
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	taskID := out["id"].(string)
	if submitPath.Load() != "/v1/videos/text2video" {
		t.Fatalf("submit path = %v, want /v1/videos/text2video (无图→text2video)", submitPath.Load())
	}
	// prompt 从 content 数组带到上游体（对抗 review CONFIRMED 回归）
	if gotPrompt.Load() != "a cat" {
		t.Fatalf("upstream prompt = %v, want 'a cat' (从 content 提取)", gotPrompt.Load())
	}
	// 凭证解密 → JWT：Authorization 是 Bearer <三段 JWT>，iss=my-ak
	auth, _ := gotAuth.Load().(string)
	assertKlingJWT(t, auth, "my-ak")

	// action 编码进 upstream_task_id 落库
	var row struct{ UpstreamTaskID string }
	db.Raw("SELECT upstream_task_id FROM tasks WHERE task_id = ?", taskID).Scan(&row)
	if row.UpstreamTaskID != "text2video:cgt-k1" {
		t.Fatalf("upstream_task_id = %q, want text2video:cgt-k1 (action 编码)", row.UpstreamTaskID)
	}

	svc.PollOnce(context.Background())
	// 查询 URL 复用 action：/v1/videos/text2video/cgt-k1
	if queryPath.Load() != "/v1/videos/text2video/cgt-k1" {
		t.Fatalf("query path = %v, want .../text2video/cgt-k1 (action 复用)", queryPath.Load())
	}
	w, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] != task.StatusSucceeded || got["result_url"] != "https://cdn.kling/v.mp4" {
		t.Fatalf("final = %d %v", w.Code, got)
	}
	// 结算：final_unit_deduction 2 → 2M token × $1/1M = $2；余额 48
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("48")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after settle: posted=%s held=%s, want 48/0 (settle by final_unit_deduction)", acc.PostedBalance, acc.HeldAmount)
	}
}

// Vidu：Token 头（非 Bearer）；GET 查询；success 带 credits 5 → 结算 5M × $1/1M = $5。
func TestViduTaskE2E(t *testing.T) {
	srv, db := setupRelayServer(t)
	var gotAuth, gotPrompt atomic.Value
	gotAuth.Store("")
	gotPrompt.Store("")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotPrompt.Store(b["prompt"])
			_, _ = w.Write([]byte(`{"task_id":"vidu-1","state":"created"}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"success","credits":5,
			"creations":[{"id":"c1","url":"https://cdn.vidu/v.mp4"}]}`))
	}))
	defer up.Close()

	svc := setupBatch2Env(t, srv, db, "vidu", up.URL, "vidu-secret", "viduq1")
	apiKey, orgID := seedUserWithCredit(t, srv, "vidu-e2e@example.com", "50")

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "viduq1", "content": []map[string]any{{"type": "text", "text": "a dog"}},
		"duration": 5, "resolution": "1080p",
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	taskID := out["id"].(string)
	// 方言：Authorization: Token <key>（非 Bearer）
	if auth, _ := gotAuth.Load().(string); auth != "Token vidu-secret" {
		t.Fatalf("auth = %q, want 'Token vidu-secret' (非 Bearer)", auth)
	}
	if gotPrompt.Load() != "a dog" {
		t.Fatalf("upstream prompt = %v, want 'a dog' (从 content 提取)", gotPrompt.Load())
	}

	svc.PollOnce(context.Background())
	w, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] != task.StatusSucceeded || got["result_url"] != "https://cdn.vidu/v.mp4" {
		t.Fatalf("final = %d %v", w.Code, got)
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("45")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after settle: posted=%s held=%s, want 45/0 (settle by credits)", acc.PostedBalance, acc.HeldAmount)
	}
}

// capture 永不超过 hold（资金红线①，对抗 review CONFIRMED 修复）：上游回报的 usage 远超 hold 时，
// settle 封顶在 hold，用户余额绝不为负；平台吸收超出部分。
func TestTaskSettleCappedAtHold(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"cgt-big"}}`))
			return
		}
		// 上游回报天量扣费单位（远超 hold 上界）
		_, _ = w.Write([]byte(`{"code":0,"data":{"task_status":"succeed",
			"task_result":{"videos":[{"url":"https://cdn.kling/v.mp4"}]},
			"final_unit_deduction":"9999"}}`))
	}))
	defer up.Close()
	svc := setupBatch2Env(t, srv, db, "kling", up.URL, "ak|sk", "kling-v1")
	apiKey, orgID := seedUserWithCredit(t, srv, "cap@example.com", "5") // 余额仅 $5

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "kling-v1", "content": []map[string]any{{"type": "text", "text": "x"}},
		"mode": "std", "duration": "5", // est 4 单位 → hold $4
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	taskID := out["id"].(string)
	svc.PollOnce(context.Background())

	w, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] != task.StatusSucceeded {
		t.Fatalf("status = %v", got["status"])
	}
	// final_unit_deduction 9999 → actual $9999 会 > hold $4；cap 后只收 $4，余额 = 5-4 = 1（≥0）
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if acc.PostedBalance.IsNegative() {
		t.Fatalf("balance negative %s — capture>hold 未封顶（资金红线①）", acc.PostedBalance)
	}
	if !acc.PostedBalance.Equal(decimal.RequireFromString("1")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after capped settle: posted=%s held=%s, want 1/0 (capped at $4 hold)", acc.PostedBalance, acc.HeldAmount)
	}
}

// assertKlingJWT：Authorization 是 Bearer + 三段 JWT，claims.iss == 期望 ak。
func assertKlingJWT(t *testing.T, auth, wantIss string) {
	t.Helper()
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		t.Fatalf("auth = %q, want Bearer <jwt>", auth)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("jwt claims decode: %v", err)
	}
	var claims map[string]any
	_ = json.Unmarshal(cb, &claims)
	if claims["iss"] != wantIss {
		t.Fatalf("jwt iss = %v, want %s", claims["iss"], wantIss)
	}
}
