package server

// §3.8 批次 3 平台集成 e2e：重点验 ResultFetcher 二跳框架（hailuo file_id→download_url）+ B 类结算。
// 真机形态验证走 real_batch3_task_smoke_test.go（env 门控）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
)

// hailuo：submit(base_resp ok)→poll(Success + file_id)→二跳 /v1/files/retrieve→download_url→结算。
// B 类：无 usage 回执 → 按 EstTokens 结算（= hold）。验证二跳框架 + prompt 携带 + bearer。
func TestHailuoTaskE2E_SecondHop(t *testing.T) {
	srv, db := setupRelayServer(t)
	var retrieveHit, gotPrompt, gotAuth atomic.Value
	retrieveHit.Store(false)
	gotPrompt.Store("")
	gotAuth.Store("")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/video_generation"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotPrompt.Store(b["prompt"])
			_, _ = w.Write([]byte(`{"task_id":"h-e2e","base_resp":{"status_code":0}}`))
		case strings.Contains(r.URL.Path, "/query/video_generation"):
			// succeeded 只给 file_id（无 URL）
			_, _ = w.Write([]byte(`{"status":"Success","file_id":"file-e2e","base_resp":{"status_code":0}}`))
		case strings.Contains(r.URL.Path, "/files/retrieve"):
			retrieveHit.Store(true)
			if r.URL.Query().Get("file_id") != "file-e2e" {
				t.Errorf("retrieve file_id = %q", r.URL.Query().Get("file_id"))
			}
			_, _ = w.Write([]byte(`{"file":{"download_url":"https://cdn.minimax/v.mp4"},"base_resp":{"status_code":0}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer up.Close()

	svc := setupBatch2Env(t, srv, db, "hailuo", up.URL, "mm-key", "MiniMax-Hailuo-02")
	apiKey, orgID := seedUserWithCredit(t, srv, "hailuo-e2e@example.com", "50")

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "MiniMax-Hailuo-02", "content": []map[string]any{{"type": "text", "text": "a whale"}},
		"resolution": "1080P",
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	taskID := out["id"].(string)
	if gotPrompt.Load() != "a whale" {
		t.Fatalf("prompt = %v, want 'a whale'", gotPrompt.Load())
	}
	if auth, _ := gotAuth.Load().(string); auth != "Bearer mm-key" {
		t.Fatalf("auth = %q, want Bearer mm-key", auth)
	}

	svc.PollOnce(context.Background())
	// 二跳必须发生 + 最终 URL 是 download_url（非 file_id）
	if !retrieveHit.Load().(bool) {
		t.Fatal("second-hop /files/retrieve was not called (ResultFetcher not wired)")
	}
	w, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] != task.StatusSucceeded || got["result_url"] != "https://cdn.minimax/v.mp4" {
		t.Fatalf("final = %d %v (want succeeded + download_url from second hop)", w.Code, got)
	}
	// B 类结算：无 usage → 按 est（1080P=2.5 单位）× $1/1M = $2.5；余额 47.5
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("47.5")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after settle: posted=%s held=%s, want 47.5/0", acc.PostedBalance, acc.HeldAmount)
	}
}

// 二跳未就绪（download_url 空）时不结算、下轮重试（视频已生成，取件抖动不误判失败）。
func TestHailuoSecondHopRetry(t *testing.T) {
	srv, db := setupRelayServer(t)
	var retrieveReady atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/video_generation"):
			_, _ = w.Write([]byte(`{"task_id":"h-r","base_resp":{"status_code":0}}`))
		case strings.Contains(r.URL.Path, "/query/video_generation"):
			_, _ = w.Write([]byte(`{"status":"Success","file_id":"f","base_resp":{"status_code":0}}`))
		case strings.Contains(r.URL.Path, "/files/retrieve"):
			if !retrieveReady.Load() { // 第一轮取件抖动：base_resp 失败
				_, _ = w.Write([]byte(`{"base_resp":{"status_code":1000,"status_msg":"not ready"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"file":{"download_url":"https://cdn/v.mp4"},"base_resp":{"status_code":0}}`))
		}
	}))
	defer up.Close()
	svc := setupBatch2Env(t, srv, db, "hailuo", up.URL, "k", "MiniMax-Hailuo-02")
	apiKey, orgID := seedUserWithCredit(t, srv, "hailuo-retry@example.com", "50")
	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey,
		map[string]any{"model": "MiniMax-Hailuo-02", "content": []map[string]any{{"type": "text", "text": "x"}}})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	taskID := out["id"].(string)

	// 第一轮：二跳抖动 → 任务不终态、hold 仍在（不误判失败退款）
	svc.PollOnce(context.Background())
	_, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] == task.StatusSucceeded || got["status"] == task.StatusFailed {
		t.Fatalf("first poll terminal %v — 二跳抖动误判终态", got["status"])
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if acc.HeldAmount.IsZero() {
		t.Fatal("hold released on second-hop hiccup — 应保持冻结待重试")
	}
	// 第二轮：取件就绪 → 结算成功
	retrieveReady.Store(true)
	svc.PollOnce(context.Background())
	_, got = srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] != task.StatusSucceeded || got["result_url"] != "https://cdn/v.mp4" {
		t.Fatalf("second poll = %v, want succeeded", got)
	}
}

// succeeded 但产物 URL 空（对抗 review CONFIRMED）：绝不结算/终态——本轮重试，产物就绪后才收费。
// suno 上游"状态先到、audio_url 后到"窗口用此路径。
func TestTaskSucceededEmptyURLRetries(t *testing.T) {
	srv, db := setupRelayServer(t)
	var urlReady atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/suno/submit/MUSIC") {
			_, _ = w.Write([]byte(`{"code":"success","message":"","data":"s-empty"}`))
			return
		}
		// /suno/fetch：SUCCESS 但 audio_url 先空后满
		if urlReady.Load() {
			_, _ = w.Write([]byte(`{"code":"success","data":[{"status":"SUCCESS","data":[{"audio_url":"https://cdn.suno/a.mp3"}]}]}`))
		} else {
			_, _ = w.Write([]byte(`{"code":"success","data":[{"status":"SUCCESS","data":[{"audio_url":""}]}]}`))
		}
	}))
	defer up.Close()
	svc := setupBatch2Env(t, srv, db, "suno", up.URL, "k", "suno_music")
	apiKey, orgID := seedUserWithCredit(t, srv, "suno-empty@example.com", "50")
	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey,
		map[string]any{"model": "suno_music", "content": []map[string]any{{"type": "text", "text": "lofi"}}})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	taskID := out["id"].(string)

	// 第一轮：SUCCESS 但 audio_url 空 → 不结算、不终态、hold 仍在（绝不为取不回产物收费）
	svc.PollOnce(context.Background())
	_, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] == task.StatusSucceeded {
		t.Fatalf("empty-URL success was settled — 为取不回产物按次收费（红线）")
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if acc.HeldAmount.IsZero() {
		t.Fatal("hold released before product ready")
	}
	// 第二轮：audio_url 就绪 → 结算成功
	urlReady.Store(true)
	svc.PollOnce(context.Background())
	_, got = srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] != task.StatusSucceeded || got["result_url"] != "https://cdn.suno/a.mp3" {
		t.Fatalf("second poll = %v, want succeeded with url", got)
	}
}
