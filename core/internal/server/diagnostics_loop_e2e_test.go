package server

// 排障闭环 e2e（顶级审计修排名 5，迁移 0045）：
// 详情字段真实落库（finish_reason/protocol/cached/reasoning）+ 上游错误码白名单
// + /api/usage?request_id= 服务端精确查 + upstream_request_id 对 org 可见。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvocationDiagnosticFields(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "up-req-42")
		fmt.Fprint(w, `{"id":"cmpl-1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"length"}],`+
			`"usage":{"prompt_tokens":100,"completion_tokens":50,`+
			`"prompt_tokens_details":{"cached_tokens":30},"completion_tokens_details":{"reasoning_tokens":20}}}`)
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "diagloop@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chat = %d %v", w.Code, out)
	}
	inv := waitInvocation(t, db, "model = ? AND status = ?", "test-model", "success")
	if inv.FinishReason != "length" {
		t.Fatalf("finish_reason = %q, want length（真实值，非硬编码 stop）", inv.FinishReason)
	}
	if inv.Protocol != "openai" {
		t.Fatalf("protocol = %q, want openai", inv.Protocol)
	}
	if inv.CachedTokens != 30 || inv.ReasoningTokens != 20 {
		t.Fatalf("cached/reasoning = %d/%d, want 30/20", inv.CachedTokens, inv.ReasoningTokens)
	}
	if inv.UpstreamRequestID != "up-req-42" {
		t.Fatalf("upstream_request_id = %q, want up-req-42", inv.UpstreamRequestID)
	}

	// /api/usage?request_id= 服务端精确查（此前只在当前页本地筛——闭环断裂）
	// + upstream_request_id 对 org 可见、渠道归属不可见
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "diagloop@example.com", "password": "longenough1"})
	token := login["token"].(string)
	wu, uout := srv.do(t, "GET", "/api/usage?request_id="+inv.RequestID, token, nil)
	if wu.Code != 200 {
		t.Fatalf("usage query = %d %v", wu.Code, uout)
	}
	items, _ := uout["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("request_id exact query items = %d, want 1", len(items))
	}
	row := items[0].(map[string]any)
	if row["upstream_request_id"] != "up-req-42" {
		t.Fatalf("upstream_request_id not visible to owner: %v", row["upstream_request_id"])
	}
	if _, has := row["provider"]; has {
		t.Fatal("provider (渠道归属) must stay hidden")
	}
	if row["finish_reason"] != "length" || row["protocol"] != "openai" {
		t.Fatalf("api echo finish/protocol = %v/%v", row["finish_reason"], row["protocol"])
	}
}

func TestInvocationUpstreamErrorCode(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","type":"invalid_request_error",`+
			`"message":"This model's maximum context length is 8192 tokens, your prompt SECRET-PROMPT-FRAGMENT..."}}`)
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "diagerr@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code == 200 {
		t.Fatal("expected upstream failure")
	}
	inv := waitInvocation(t, db, "model = ? AND status <> ?", "test-model", "success")
	if inv.UpstreamErrorCode != "context_length_exceeded" {
		t.Fatalf("upstream_error_code = %q, want context_length_exceeded（白名单枚举）", inv.UpstreamErrorCode)
	}
	// 红线 7/8：自由文本 message 绝不落库
	if strings.Contains(inv.ErrorBrief, "SECRET") || strings.Contains(inv.UpstreamErrorCode, "SECRET") {
		t.Fatalf("upstream message leaked: brief=%q code=%q", inv.ErrorBrief, inv.UpstreamErrorCode)
	}
}
