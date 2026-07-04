package server

// 渠道级 Header/Param Override e2e（指令 §3.4）：
// 验收——可为渠道固定添加上游私有字段（service_tier/reasoning 等）、按渠道删除上游不支持字段
// （parallel_tool_calls/store/嵌套 stream_options.include_obfuscation）、自定义认证头 +
// {{credential}} 占位、受保护键与禁改头写入即拒、override 变更审计只记 keys。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// captureHeaderUpstream：记录请求体 + 请求头。
func captureHeaderUpstream(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Value) {
	t.Helper()
	var lastBody, lastHeader atomic.Value
	lastBody.Store(map[string]any{})
	lastHeader.Store(http.Header{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		lastBody.Store(body)
		lastHeader.Store(r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-x","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastBody, &lastHeader
}

// param override：增上游私有字段 + 删客户端字段（顶层与嵌套）；客户端原字段不受其它渠道污染。
func TestChannelParamOverride(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up, lastBody, _ := captureHeaderUpstream(t)
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "dialect", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
		"param_override": `{"service_tier":"flex","reasoning":{"effort":"low"},"parallel_tool_calls":null,"store":null}`,
	})
	if w.Code != 201 {
		t.Fatalf("create channel with override = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, _ := seedUserWithCredit(t, srv, "ovr-param@example.com", "10")

	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 50,
		"messages":            []map[string]any{{"role": "user", "content": "hi"}},
		"parallel_tool_calls": true, "store": true, // 客户端带了上游不支持的字段
	})
	if w.Code != 200 {
		t.Fatalf("chat with override = %d", w.Code)
	}
	got := lastBody.Load().(map[string]any)
	if got["service_tier"] != "flex" {
		t.Fatalf("service_tier not injected: %v", got["service_tier"])
	}
	if got["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("reasoning not injected: %v", got["reasoning"])
	}
	if _, ok := got["parallel_tool_calls"]; ok {
		t.Fatalf("parallel_tool_calls not stripped: %v", got)
	}
	if _, ok := got["store"]; ok {
		t.Fatalf("store not stripped: %v", got)
	}
	// 客户端消息原样到达（受保护键 messages 不可被 override 波及）
	if msgs := got["messages"].([]any); len(msgs) != 1 {
		t.Fatalf("messages altered: %v", msgs)
	}
}

// header override：自定义认证头 + {{credential}} 占位 + 删默认 Authorization。
func TestChannelHeaderOverride(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up, _, lastHeader := captureHeaderUpstream(t)
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "custom-auth", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
		"header_override": `{"X-Api-Key":"{{credential}}","Authorization":null,"X-Upstream-Tenant":"acme"}`,
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, _ := seedUserWithCredit(t, srv, "ovr-header@example.com", "10")

	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chat with header override = %d %s", w.Code, w.Body.String())
	}
	h := lastHeader.Load().(http.Header)
	if got := h.Get("X-Api-Key"); got != "sk-upstream-secret" {
		t.Fatalf("credential placeholder = %q", got)
	}
	if got := h.Get("Authorization"); got != "" {
		t.Fatalf("default Authorization not removed: %q", got)
	}
	if got := h.Get("X-Upstream-Tenant"); got != "acme" {
		t.Fatalf("custom header = %q", got)
	}
}

// 校验拒绝：受保护键/禁改头/非法 JSON → 400（create 与 update 两路）。
func TestChannelOverrideValidation(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up, _, _ := captureHeaderUpstream(t)
	base := map[string]any{
		"name": "v", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
	}
	for name, patch := range map[string]map[string]any{
		"protected max_tokens": {"param_override": `{"max_tokens":999999}`},
		"protected model":      {"param_override": `{"model":"evil"}`},
		"protected stream":     {"param_override": `{"stream":false}`},
		"param not json":       {"param_override": `oops`},
		"forbidden host":       {"header_override": `{"Host":"evil"}`},
		"header non-string":    {"header_override": `{"X-N":1}`},
	} {
		body := map[string]any{}
		for k, v := range base {
			body[k] = v
		}
		for k, v := range patch {
			body[k] = v
		}
		if w, out := srv.doAdmin(t, "POST", "/api/admin/channels", body); w.Code != 400 {
			t.Fatalf("%s: create = %d %v, want 400", name, w.Code, out)
		}
	}

	// update 路同样拒绝
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", base)
	if w.Code != 201 {
		t.Fatalf("create clean channel = %d %v", w.Code, out)
	}
	id := fmt.Sprint(int(out["id"].(float64)))
	if w, out := srv.doAdmin(t, "PUT", "/api/admin/channels/"+id, map[string]any{
		"param_override": `{"n":9}`,
	}); w.Code != 400 {
		t.Fatalf("update protected n = %d %v, want 400", w.Code, out)
	}
}

// 审计：override 变更记 keys 列表，绝不记值。
func TestChannelOverrideAuditKeysOnly(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up, _, _ := captureHeaderUpstream(t)
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "aud", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
	})
	if w.Code != 201 {
		t.Fatalf("create = %d %v", w.Code, out)
	}
	id := fmt.Sprint(int(out["id"].(float64)))
	if w, out := srv.doAdmin(t, "PUT", "/api/admin/channels/"+id, map[string]any{
		"param_override": `{"service_tier":"super-secret-tier-value"}`,
	}); w.Code != 200 {
		t.Fatalf("update override = %d %v", w.Code, out)
	}
	_, logs := srv.doAdmin(t, "GET", "/api/admin/audit-logs", nil)
	raw, _ := json.Marshal(logs)
	s := string(raw)
	if !strings.Contains(s, "param_override keys: [service_tier]") {
		t.Fatalf("audit missing override keys: %s", s)
	}
	if strings.Contains(s, "super-secret-tier-value") {
		t.Fatalf("audit leaked override VALUE: %s", s)
	}
}
