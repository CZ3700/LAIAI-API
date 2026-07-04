package server

// §3.8 批次 4（sora/veo）平台集成 e2e：重点验产物代理端点（ContentStreamer + /content 路由）+
// result_ref 落库 + org 隔离 + A 类结算。真机形态验证走 real_batch4_task_smoke_test.go（env 门控）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
)

// sora 全链路：submit→poll(completed，回显 seconds/size)→result_url=平台代理路径→
// 用户 GET /content→平台用渠道凭证取上游二进制流下发。A 类按回显 seconds×size 结算。
func TestSoraTaskE2E_ContentProxy(t *testing.T) {
	srv, db := setupRelayServer(t)
	var contentAuth atomic.Value
	contentAuth.Store("")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/videos"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"video_e2e","status":"queued","seconds":"4","size":"720x1280"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/videos/video_e2e"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"video_e2e","status":"completed","seconds":"4","size":"720x1280"}`))
		case strings.HasSuffix(r.URL.Path, "/videos/video_e2e/content"):
			contentAuth.Store(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("SORA_MP4_STREAM"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer up.Close()

	svc := setupBatch2Env(t, srv, db, "sora", up.URL, "sk-sora", "sora-2")
	// 产物代理走 SSRF 守卫客户端；httptest 是 127.0.0.1，须开 allow_private 才可取流
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('advanced_custom_allow_private', 'true')")
	apiKey, orgID := seedUserWithCredit(t, srv, "sora-e2e@example.com", "50")

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "sora-2", "content": []map[string]any{{"type": "text", "text": "a fox in snow"}},
		"seconds": "4", "size": "720x1280",
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
	// 对外 result_url 是平台代理路径（不暴露上游 id/URL）
	wantURL := "/v1/video/generations/" + taskID + "/content"
	if got["result_url"] != wantURL {
		t.Fatalf("result_url = %v, want proxy path %s", got["result_url"], wantURL)
	}
	// A 类结算：4s×720档(1.0)×$1/1M = $4；余额 46
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("46")) {
		t.Fatalf("posted = %s, want 46", acc.PostedBalance)
	}

	// 用户拉产物：平台用渠道凭证代理取上游二进制流
	rec := srv.doRaw(t, "GET", wantURL, apiKey)
	if rec.Code != 200 || rec.Body.String() != "SORA_MP4_STREAM" {
		t.Fatalf("content proxy = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	// 上游 content 请求带的是渠道凭证（sk-sora），不是用户 apiKey
	if auth, _ := contentAuth.Load().(string); auth != "Bearer sk-sora" {
		t.Fatalf("upstream content auth = %q, want channel credential", auth)
	}

	// org 隔离：另一 org 的 key 不能拉本任务产物
	otherKey, _ := seedUserWithCredit(t, srv, "sora-other@example.com", "10")
	if rec := srv.doRaw(t, "GET", wantURL, otherKey); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-org content = %d, want 404", rec.Code)
	}
}

// veo 代理：URI 带渠道 key，绝不进对外 result_url；用户经代理端点取流（x-goog-api-key）。
func TestVeoTaskE2E_ContentProxyHidesKey(t *testing.T) {
	srv, db := setupRelayServer(t)
	var gotKeyHeader atomic.Value
	gotKeyHeader.Store("")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":predictLongRunning"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"models/veo-3.1-generate-preview/operations/opE2E"}`))
		case strings.Contains(r.URL.Path, "/operations/opE2E"):
			w.Header().Set("Content-Type", "application/json")
			// URI 指回本 mock 的 files 端点（带 key 语义）
			_, _ = w.Write([]byte(`{"done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"` + veoFilesURI(r) + `"}}]}}}`))
		case strings.Contains(r.URL.Path, "/files/dl"):
			gotKeyHeader.Store(r.Header.Get("x-goog-api-key"))
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("VEO_MP4_STREAM"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer up.Close()

	svc := setupBatch2Env(t, srv, db, "veo", up.URL, "goog-key-secret", "veo-3.1-generate-preview")
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('advanced_custom_allow_private', 'true')") // 产物代理守卫；httptest 127.0.0.1
	apiKey, _ := seedUserWithCredit(t, srv, "veo-e2e@example.com", "50")

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "veo-3.1-generate-preview", "content": []map[string]any{{"type": "text", "text": "aurora"}},
		"duration": 8,
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	taskID := out["id"].(string)
	svc.PollOnce(context.Background())
	_, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] != task.StatusSucceeded {
		t.Fatalf("status = %v", got["status"])
	}
	// 对外 result_url 是代理路径——绝不含带 key 的上游 URI
	rurl, _ := got["result_url"].(string)
	if !strings.HasSuffix(rurl, "/content") || strings.Contains(rurl, "goog-key-secret") || strings.Contains(rurl, "files/dl") {
		t.Fatalf("result_url leaks upstream URI/key: %q", rurl)
	}
	// 拉产物：平台用 x-goog-api-key 头代理取流
	rec := srv.doRaw(t, "GET", rurl, apiKey)
	if rec.Code != 200 || rec.Body.String() != "VEO_MP4_STREAM" {
		t.Fatalf("veo content proxy = %d %q", rec.Code, rec.Body.String())
	}
	if k, _ := gotKeyHeader.Load().(string); k != "goog-key-secret" {
		t.Fatalf("upstream key header = %q, want channel key", k)
	}
}

// veoFilesURI：让 LRO 响应里的产物 URI 指回同一 mock 服务器的 files 端点。
func veoFilesURI(r *http.Request) string {
	return "http://" + r.Host + "/files/dl"
}

// SSRF 守卫（对抗 review CONFIRMED HIGH）：恶意/被攻陷 veo 渠道返回内网/metadata 产物 URI 时，
// 平台代理端点必须被守卫拦截，绝不打内网、绝不回显。
func TestVeoContentProxyBlocksSSRF(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, ":predictLongRunning") {
			_, _ = w.Write([]byte(`{"name":"models/veo-3.1-generate-preview/operations/ssrf"}`))
			return
		}
		// LRO 返回指向云 metadata 的产物 URI（恶意渠道）
		_, _ = w.Write([]byte(`{"done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"http://169.254.169.254/latest/meta-data/"}}]}}}`))
	}))
	defer up.Close()
	svc := setupBatch2Env(t, srv, db, "veo", up.URL, "gk", "veo-3.1-generate-preview")
	// 即便 allow_private=true（放行 127.0.0.1），metadata IP 也恒拦
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('advanced_custom_allow_private', 'true')")
	apiKey, _ := seedUserWithCredit(t, srv, "veo-ssrf@example.com", "50")
	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey,
		map[string]any{"model": "veo-3.1-generate-preview", "content": []map[string]any{{"type": "text", "text": "x"}}})
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit = %d %v", w.Code, out)
	}
	taskID := out["id"].(string)
	svc.PollOnce(context.Background())
	_, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if got["status"] != task.StatusSucceeded {
		t.Fatalf("status = %v", got["status"]) // 任务本身成功（URI 落 result_ref）
	}
	// 拉产物：守卫必须拦截 metadata IP → 502，绝不回显 metadata 内容
	rec := srv.doRaw(t, "GET", "/v1/video/generations/"+taskID+"/content", apiKey)
	if rec.Code == 200 {
		t.Fatalf("SSRF to metadata was NOT blocked: %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "meta-data") || strings.Contains(rec.Body.String(), "169.254") {
		t.Fatalf("metadata content leaked: %q", rec.Body.String())
	}
}
