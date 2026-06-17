package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// Anthropic 原生入站（/anthropic/v1/messages）：复用 OpenAI 计费管道，请求/响应双向转换。
// 这些 E2E 验证：Anthropic 格式进出正确、计费与 /v1/chat/completions 等价、错误走 Anthropic 包络。

// 非流式：Anthropic 请求 → 上游 OpenAI 完成 → Anthropic Messages 响应；计费精确。
func TestAnthropicInboundNonStream(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500) // 上游真实 usage 1000 in / 500 out
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "anth@example.com", "10")

	w, out := srv.do(t, "POST", "/anthropic/v1/messages", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 100,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("anthropic relay = %d %v", w.Code, out)
	}
	// Anthropic Messages 形态
	if out["type"] != "message" || out["role"] != "assistant" {
		t.Fatalf("not anthropic message shape: %v", out)
	}
	content, ok := out["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %v", out["content"])
	}
	blk := content[0].(map[string]any)
	if blk["type"] != "text" || blk["text"] != "hello" {
		t.Fatalf("text block = %v", blk)
	}
	if out["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v, want end_turn", out["stop_reason"])
	}
	usage := out["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 1000 || usage["output_tokens"].(float64) != 500 {
		t.Fatalf("usage = %v", usage)
	}
	// Content-Length 必须等于转换后 Anthropic body 长度（适配器 c.Data 钉的旧 OpenAI 长度会让真实 socket 截断，CRITICAL）。
	if cl := w.Header().Get("Content-Length"); cl != "" && cl != fmt.Sprint(w.Body.Len()) {
		t.Fatalf("Content-Length=%s != body len %d — wire would truncate the response", cl, w.Body.Len())
	}
	// 计费与 OpenAI 路径等价：1000/1M*2 + 500/1M*6 = 0.005 → 余额 9.995
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.995")) {
		t.Fatalf("balance = %s, want 9.995", acc.PostedBalance)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held = %s, want 0", acc.HeldAmount)
	}
}

// 流式：Anthropic stream=true → Anthropic 事件流（message_start/content_block_delta/message_delta/message_stop），
// 无 [DONE]（Anthropic 协议无此哨兵）；计费精确。
func TestAnthropicInboundStream(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"cmpl-x\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2000,\"completion_tokens\":1000}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "anthstream@example.com", "10")

	w, _ := srv.do(t, "POST", "/anthropic/v1/messages", apiKey, map[string]any{
		"model": "test-model", "stream": true, "max_tokens": 2000,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("anthropic stream = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_start",
		"event: content_block_delta", "\"text_delta\"", "event: message_delta",
		"\"stop_reason\":\"end_turn\"", "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("anthropic SSE missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("anthropic SSE must NOT contain [DONE]:\n%s", body)
	}
	// "hello" 跨两帧拼接 → content_block_delta 各发 he/llo
	if !strings.Contains(body, "\"text\":\"he\"") || !strings.Contains(body, "\"text\":\"llo\"") {
		t.Fatalf("text deltas not split correctly:\n%s", body)
	}
	// 计费：2000/1M*2 + 1000/1M*6 = 0.01 → 9.99
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.99")) {
		t.Fatalf("balance after stream = %s, want 9.99", acc.PostedBalance)
	}
}

// 工具调用（非流）：上游 OpenAI tool_calls → Anthropic content 里的 tool_use 块。
func TestAnthropicInboundToolUse(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-t","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "anthtool@example.com", "10")

	w, out := srv.do(t, "POST", "/anthropic/v1/messages", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 100,
		"messages": []map[string]any{{"role": "user", "content": "weather?"}},
		"tools":    []map[string]any{{"name": "get_weather", "description": "w", "input_schema": map[string]any{"type": "object"}}},
	})
	if w.Code != 200 {
		t.Fatalf("anthropic tool = %d %v", w.Code, out)
	}
	content := out["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", content)
	}
	blk := content[0].(map[string]any)
	if blk["type"] != "tool_use" || blk["name"] != "get_weather" {
		t.Fatalf("tool_use block = %v", blk)
	}
	input := blk["input"].(map[string]any)
	if input["city"] != "Paris" {
		t.Fatalf("tool input = %v", input)
	}
	if out["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", out["stop_reason"])
	}
}

// 错误：余额不足 → Anthropic error 包络（非 OpenAI 形态）+ 402。
func TestAnthropicInboundErrorEnvelope(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := mockUpstreamJSON(t, 10, 10)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "anthpoor@example.com", "") // 无授信 → 余额 0

	w, out := srv.do(t, "POST", "/anthropic/v1/messages", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 100,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("insufficient balance = %d, want 402", w.Code)
	}
	if out["type"] != "error" {
		t.Fatalf("not anthropic error shape: %v", out)
	}
	errObj, ok := out["error"].(map[string]any)
	if !ok || errObj["type"] != "invalid_request_error" || errObj["message"] == "" {
		t.Fatalf("anthropic error = %v", out["error"])
	}
}

// 校验：max_tokens 缺失 → 400 Anthropic error（Anthropic 强制 max_tokens）。
func TestAnthropicInboundMissingMaxTokens(t *testing.T) {
	srv, _ := setupRelayServer(t)
	apiKey, _ := seedUserWithCredit(t, srv, "anthbad@example.com", "10")
	w, out := srv.do(t, "POST", "/anthropic/v1/messages", apiKey, map[string]any{
		"model":    "test-model",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 400 || out["type"] != "error" {
		t.Fatalf("missing max_tokens = %d %v, want 400 error", w.Code, out)
	}
}

// x-trace-id / x-user-id / x-agent-name 回显（DevEx 可观测性头透传承诺）。
func TestRelayEchoesTraceHeaders(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := mockUpstreamJSON(t, 10, 10)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "anthtrace@example.com", "10")

	body, _ := json.Marshal(map[string]any{
		"model": "test-model", "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Trace-Id", "checkout-smoke-001")
	req.Header.Set("X-Agent-Name", "claude-code")
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("relay = %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Trace-Id"); got != "checkout-smoke-001" {
		t.Fatalf("X-Trace-Id echo = %q, want checkout-smoke-001", got)
	}
	if got := w.Header().Get("X-Agent-Name"); got != "claude-code" {
		t.Fatalf("X-Agent-Name echo = %q", got)
	}
}
