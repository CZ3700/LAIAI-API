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

// anthropic 渠道 + 定价：claude-test $3/1M in, $15/1M out
func setupAnthropicChannel(t *testing.T, srv *gin999, baseURL string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "anthropic-mock", "provider": "anthropic", "base_url": baseURL,
		"api_key": "sk-ant-secret", "models": []string{"claude-test"},
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "claude-test", "input_usd_per_1m": "3", "output_usd_per_1m": "15",
	})
	if w.Code != 200 {
		t.Fatalf("upsert price = %d %v", w.Code, out)
	}
}

// 真实 Anthropic Messages API 响应形态（fixture 纪律：红线 4 泛化）
const anthropicJSONFixture = `{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "type": "message",
  "role": "assistant",
  "model": "claude-test",
  "content": [{"type": "text", "text": "Hello from Claude!"}],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 2000, "output_tokens": 1000}
}`

func TestAnthropicRequestConversion(t *testing.T) {
	srv, db := setupRelayServer(t)
	var gotPath, gotAPIKey, gotVersion string
	var gotBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicJSONFixture)
	}))
	defer up.Close()
	setupAnthropicChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "claude@example.com", "10")

	// OpenAI 格式请求：system 消息 + stop 字符串
	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "claude-test", "max_tokens": 500, "stop": "END",
		"messages": []map[string]string{
			{"role": "system", "content": "You are concise."},
			{"role": "user", "content": "hi"},
		},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d %v", w.Code, out)
	}

	// —— 请求侧转换断言 ——
	if gotPath != "/messages" {
		t.Errorf("upstream path = %q, want /messages", gotPath)
	}
	if gotAPIKey != "sk-ant-secret" || gotVersion != "2023-06-01" {
		t.Errorf("auth headers: x-api-key=%q version=%q", gotAPIKey, gotVersion)
	}
	if gotBody["system"] != "You are concise." {
		t.Errorf("system not lifted: %v", gotBody["system"])
	}
	if msgs, _ := gotBody["messages"].([]any); len(msgs) != 1 {
		t.Errorf("messages should exclude system: %v", gotBody["messages"])
	}
	if gotBody["max_tokens"] != float64(500) {
		t.Errorf("max_tokens = %v", gotBody["max_tokens"])
	}
	if ss, _ := gotBody["stop_sequences"].([]any); len(ss) != 1 || ss[0] != "END" {
		t.Errorf("stop_sequences = %v", gotBody["stop_sequences"])
	}

	// —— 响应侧转换断言：客户端拿到 OpenAI 格式 ——
	if out["object"] != "chat.completion" {
		t.Errorf("object = %v", out["object"])
	}
	choices := out["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "Hello from Claude!" {
		t.Errorf("content = %v", msg["content"])
	}
	if choices[0].(map[string]any)["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v", choices[0].(map[string]any)["finish_reason"])
	}
	usageOut := out["usage"].(map[string]any)
	if usageOut["prompt_tokens"] != float64(2000) || usageOut["completion_tokens"] != float64(1000) {
		t.Errorf("usage = %v", usageOut)
	}

	// —— 计费断言：2000/1M*3 + 1000/1M*15 = 0.006 + 0.015 = 0.021 ——
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.979")) {
		t.Fatalf("balance = %s, want 9.979", acc.PostedBalance)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held = %s", acc.HeldAmount)
	}
}

// 真实 Anthropic SSE 事件流形态
var anthropicSSEFixture = strings.Join([]string{
	`event: message_start`,
	`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1500,"output_tokens":1}}}`,
	``,
	`event: content_block_start`,
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	``,
	`event: content_block_delta`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
	``,
	`event: content_block_delta`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo!"}}`,
	``,
	`event: content_block_stop`,
	`data: {"type":"content_block_stop","index":0}`,
	``,
	`event: message_delta`,
	`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":800}}`,
	``,
	`event: message_stop`,
	`data: {"type":"message_stop"}`,
	``,
}, "\n")

func TestAnthropicStreamingConversion(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("stream not set on upstream request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, anthropicSSEFixture)
	}))
	defer up.Close()
	setupAnthropicChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "claude-stream@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "claude-test", "stream": true, "max_tokens": 1000,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("stream relay = %d %s", w.Code, w.Body.String())
	}
	out := w.Body.String()

	// 客户端收到的是 OpenAI chunk 流
	if !strings.Contains(out, `"content":"Hel"`) || !strings.Contains(out, `"content":"lo!"`) {
		t.Fatalf("text deltas not converted: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"length"`) { // max_tokens → length
		t.Fatalf("finish_reason not mapped: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("no [DONE]: %s", out)
	}
	if strings.Contains(out, "message_start") || strings.Contains(out, "content_block_delta") {
		t.Fatalf("anthropic raw events leaked to client: %s", out)
	}

	// 计费：1500/1M*3 + 800/1M*15 = 0.0045 + 0.012 = 0.0165
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.9835")) {
		t.Fatalf("balance = %s, want 9.9835", acc.PostedBalance)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held = %s", acc.HeldAmount)
	}
}
