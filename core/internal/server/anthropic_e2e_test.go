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

// 工具调用全双向：请求侧 tools/tool_choice/历史 tool_calls/tool 结果合并；响应侧 tool_use→tool_calls
func TestAnthropicToolCallsRoundtrip(t *testing.T) {
	srv, _ := setupRelayServer(t)
	var gotBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		// 真实 Claude tool_use 响应形态
		fmt.Fprint(w, `{
		  "id": "msg_tool1", "type": "message", "role": "assistant", "model": "claude-test",
		  "content": [{"type": "tool_use", "id": "toolu_01A", "name": "get_weather",
		               "input": {"location": "Tokyo", "unit": "celsius"}}],
		  "stop_reason": "tool_use",
		  "usage": {"input_tokens": 500, "output_tokens": 60}
		}`)
	}))
	defer up.Close()
	setupAnthropicChannel(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "tools@example.com", "10")

	// OpenAI 格式：工具定义 + 指定工具 + 含历史工具调用与结果的对话
	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "claude-test", "max_tokens": 200,
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name": "get_weather", "description": "Get weather",
				"parameters": map[string]any{"type": "object",
					"properties": map[string]any{"location": map[string]any{"type": "string"}}},
			},
		}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
		"messages": []map[string]any{
			{"role": "user", "content": "weather in tokyo?"},
			{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{
				"id": "toolu_00X", "type": "function",
				"function": map[string]any{"name": "get_weather", "arguments": `{"location":"Osaka"}`},
			}}},
			{"role": "tool", "tool_call_id": "toolu_00X", "content": "rainy 18C"},
			{"role": "user", "content": "and tokyo?"},
		},
	})
	if w.Code != 200 {
		t.Fatalf("tool relay = %d %v", w.Code, out)
	}

	// —— 请求侧断言 ——
	tools := gotBody["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	if tool0["name"] != "get_weather" || tool0["input_schema"] == nil {
		t.Fatalf("tools not converted to claude shape: %v", tool0)
	}
	tc := gotBody["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != "get_weather" {
		t.Fatalf("tool_choice = %v, want {type:tool,name:get_weather}", tc)
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4 (user/assistant-tool_use/user-tool_result/user)", len(msgs))
	}
	asst := msgs[1].(map[string]any)
	asstBlocks := asst["content"].([]any)
	tu := asstBlocks[0].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "toolu_00X" {
		t.Fatalf("assistant history not converted to tool_use: %v", tu)
	}
	if input := tu["input"].(map[string]any); input["location"] != "Osaka" {
		t.Fatalf("tool_use input not parsed from arguments string: %v", tu["input"])
	}
	toolResultMsg := msgs[2].(map[string]any)
	if toolResultMsg["role"] != "user" {
		t.Fatalf("tool result not wrapped in user message: %v", toolResultMsg)
	}
	tr := toolResultMsg["content"].([]any)[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "toolu_00X" || tr["content"] != "rainy 18C" {
		t.Fatalf("tool_result block: %v", tr)
	}

	// —— 响应侧断言 ——
	choice := out["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	msg := choice["message"].(map[string]any)
	if msg["content"] != nil {
		t.Fatalf("pure tool call should have null content, got %v", msg["content"])
	}
	calls := msg["tool_calls"].([]any)
	call0 := calls[0].(map[string]any)
	fn := call0["function"].(map[string]any)
	if call0["id"] != "toolu_01A" || fn["name"] != "get_weather" {
		t.Fatalf("tool_call = %v", call0)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil || args["location"] != "Tokyo" {
		t.Fatalf("arguments not a valid JSON string: %v", fn["arguments"])
	}
}

// 并发（多个）tool calls 流式：block index → tool index 状态机
var anthropicParallelToolsSSE = strings.Join([]string{
	`data: {"type":"message_start","message":{"id":"msg_p","usage":{"input_tokens":800,"output_tokens":1}}}`,
	``,
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	``,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"checking..."}}`,
	``,
	`data: {"type":"content_block_stop","index":0}`,
	``,
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_A","name":"get_weather"}}`,
	``,
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}`,
	``,
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Tokyo\"}"}}`,
	``,
	`data: {"type":"content_block_stop","index":1}`,
	``,
	`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_B","name":"get_time"}}`,
	``,
	`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"JST\"}"}}`,
	``,
	`data: {"type":"content_block_stop","index":2}`,
	``,
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":90}}`,
	``,
	`data: {"type":"message_stop"}`,
	``,
}, "\n")

func TestAnthropicStreamingParallelToolCalls(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, anthropicParallelToolsSSE)
	}))
	defer up.Close()
	setupAnthropicChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "ptools@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "claude-test", "stream": true, "max_tokens": 500,
		"messages": []map[string]string{{"role": "user", "content": "weather and time in tokyo"}},
	})
	if w.Code != 200 {
		t.Fatalf("stream = %d %s", w.Code, w.Body.String())
	}
	out := w.Body.String()

	// 两个工具各自正确的 OpenAI tool index（0 和 1，不是 Claude 的 block index 1 和 2）
	if !strings.Contains(out, `"index":0,"id":"toolu_A"`) && !strings.Contains(out, `"id":"toolu_A"`) {
		t.Fatalf("tool A start chunk missing: %s", out)
	}
	for _, want := range []string{
		`"name":"get_weather"`, `"name":"get_time"`,
		`{\"location\":`, `\"Tokyo\"}`, `{\"tz\":\"JST\"}`,
		`"finish_reason":"tool_calls"`,
		"data: [DONE]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q in: %s", want, out)
		}
	}
	// tool index 必须是 0/1（工具序），断言 toolu_B 的 start chunk 带 index 1
	var sawB bool
	for _, line := range strings.Split(out, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
			var chunk struct {
				Choices []struct {
					Delta struct {
						ToolCalls []struct {
							Index int    `json:"index"`
							ID    string `json:"id"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(data), &chunk) == nil && len(chunk.Choices) > 0 {
				for _, tc := range chunk.Choices[0].Delta.ToolCalls {
					if tc.ID == "toolu_B" {
						sawB = true
						if tc.Index != 1 {
							t.Fatalf("toolu_B index = %d, want 1 (tool-ordinal, not block index 2)", tc.Index)
						}
					}
					if tc.ID == "toolu_A" && tc.Index != 0 {
						t.Fatalf("toolu_A index = %d, want 0", tc.Index)
					}
				}
			}
		}
	}
	if !sawB {
		t.Fatal("toolu_B start chunk not found")
	}
	// 计费：800/1M*3 + 90/1M*15 = 0.0024 + 0.00135 = 0.00375
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.99625")) {
		t.Fatalf("balance = %s, want 9.99625", acc.PostedBalance)
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
