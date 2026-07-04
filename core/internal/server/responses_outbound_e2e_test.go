package server

// 出站 openai-responses adapter（chat 客户端 → 只支持 /v1/responses 的上游，指令 §3.2）：
// 与入站是精确镜像。验证 chat 请求转 Responses 到达上游、Responses 响应/流转回 chat、计费等价。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// setupResponsesChannel：建一个 provider=openai-responses 的渠道 + 定价。
func setupResponsesChannel(t *testing.T, srv *gin999, baseURL string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "resp-up", "provider": "openai-responses", "base_url": baseURL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
	})
	if w.Code != 201 {
		t.Fatalf("create responses channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
}

// captureResponsesUpstream：断言收到的请求打在 /responses 路径且记录请求体，返回可编程响应。
func captureResponsesUpstream(t *testing.T, respBody string, contentType string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var last atomic.Value
	last.Store(map[string]any{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("upstream path = %s, want .../responses", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-upstream-secret" {
			t.Errorf("upstream auth = %q", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		last.Store(body)
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

// 非流式：chat 请求 → Responses 上游 → chat 完成；请求正确转 Responses，计费等价。
func TestResponsesOutboundNonStream(t *testing.T) {
	srv, db := setupRelayServer(t)
	respBody := `{"id":"resp_abc","object":"response","created_at":1751600000,"model":"test-model","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":1000,"output_tokens":500,"input_tokens_details":{"cached_tokens":0}}}`
	up, last := captureResponsesUpstream(t, respBody, "application/json")
	setupResponsesChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "rout@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model":      "test-model",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "system", "content": "be brief"}, {"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chat via responses = %d %v", w.Code, out)
	}
	// chat 完成形态
	if out["object"] != "chat.completion" {
		t.Fatalf("not chat.completion: %v", out)
	}
	choices := out["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "hello" {
		t.Fatalf("content = %v", msg["content"])
	}
	usage := out["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) != 1000 || usage["completion_tokens"].(float64) != 500 {
		t.Fatalf("usage = %v", usage)
	}
	// 上游收到的是 Responses 请求：system → instructions，user → input，max_tokens → max_output_tokens
	got := last.Load().(map[string]any)
	if got["instructions"] != "be brief" {
		t.Fatalf("instructions = %v", got["instructions"])
	}
	if got["max_output_tokens"].(float64) != 100 {
		t.Fatalf("max_output_tokens = %v", got["max_output_tokens"])
	}
	if got["store"] != false {
		t.Fatalf("store = %v, want false", got["store"])
	}
	input := got["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)["role"] != "user" {
		t.Fatalf("input = %v", input)
	}
	// 计费等价：1000/1M*2 + 500/1M*6 = 0.005 → 9.995
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.995")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("balance = %s held = %s, want 9.995/0", acc.PostedBalance, acc.HeldAmount)
	}
}

// 工具调用往返：chat tools/tool_calls/tool 消息 → Responses；Responses function_call → chat tool_calls。
func TestResponsesOutboundToolRoundTrip(t *testing.T) {
	srv, _ := setupRelayServer(t)
	respBody := `{"id":"resp_t","object":"response","created_at":1,"model":"test-model","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_9","name":"lookup","arguments":"{\"q\":1}"}],"usage":{"input_tokens":10,"output_tokens":5}}`
	up, last := captureResponsesUpstream(t, respBody, "application/json")
	setupResponsesChannel(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "rout-tool@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 50,
		"messages": []map[string]any{
			{"role": "user", "content": "search"},
			{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "call_9", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}},
			{"role": "tool", "tool_call_id": "call_9", "content": "result"},
		},
		"tools": []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}},
	})
	if w.Code != 200 {
		t.Fatalf("tool round-trip = %d %v", w.Code, out)
	}
	// 上游收到扁平 tools + function_call/function_call_output input items
	got := last.Load().(map[string]any)
	tools := got["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "function" || tools[0].(map[string]any)["name"] != "lookup" {
		t.Fatalf("flat tools = %v", tools)
	}
	input := got["input"].([]any)
	var sawFC, sawFCO bool
	for _, it := range input {
		m := it.(map[string]any)
		if m["type"] == "function_call" && m["call_id"] == "call_9" {
			sawFC = true
		}
		if m["type"] == "function_call_output" && m["call_id"] == "call_9" && m["output"] == "result" {
			sawFCO = true
		}
	}
	if !sawFC || !sawFCO {
		t.Fatalf("input items = %v (fc=%v fco=%v)", input, sawFC, sawFCO)
	}
	// 响应：Responses function_call → chat tool_calls
	choices := out["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	tc := msg["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "call_9" || tc["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("chat tool_calls = %v", tc)
	}
	if choices[0].(map[string]any)["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v", choices[0])
	}
}

// 流式：Responses 事件流 → chat chunk 流 + [DONE]；计费精确。
func TestResponsesOutboundStream(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_x\",\"model\":\"test-model\"}}\n\n")
		f.Flush()
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"he\"}\n\n")
		f.Flush()
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"llo\"}\n\n")
		f.Flush()
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"usage\":{\"input_tokens\":2000,\"output_tokens\":1000,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
		f.Flush()
		// Responses 无 [DONE]，靠 EOF
	}))
	defer up.Close()
	setupResponsesChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "rout-stream@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "stream": true, "max_tokens": 2000,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("stream via responses = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"chat.completion.chunk", `"role":"assistant"`, `"content":"he"`, `"content":"llo"`,
		`"finish_reason":"stop"`, "data: [DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("chat SSE missing %q in:\n%s", want, body)
		}
	}
	// 计费：2000/1M*2 + 1000/1M*6 = 0.01 → 9.99
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.99")) {
		t.Fatalf("balance after stream = %s, want 9.99", acc.PostedBalance)
	}
}

// 流式工具调用：Responses function_call 事件 → chat tool_calls chunk。
func TestResponsesOutboundStreamTool(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_5\",\"name\":\"go\",\"arguments\":\"\"}}\n\n")
		f.Flush()
		fmt.Fprint(w, "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"a\\\":1}\"}\n\n")
		f.Flush()
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n")
		f.Flush()
	}))
	defer up.Close()
	setupResponsesChannel(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "rout-stool@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "stream": true, "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "go"}},
	})
	if w.Code != 200 {
		t.Fatalf("stream tool = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"tool_calls"`, `"id":"call_5"`, `"name":"go"`, `"arguments":"{\"a\":1}"`,
		`"finish_reason":"tool_calls"`, "data: [DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream tool SSE missing %q in:\n%s", want, body)
		}
	}
}

// 流式截断：Responses 上游中途断（无 completed）→ chat error 帧，不可计 success。
func TestResponsesOutboundStreamTruncated(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		f.Flush()
	}))
	setupResponsesChannel(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "rout-trunc@example.com", "10")
	up.Close() // 立即关闭模拟截断

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "stream": true, "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	// 上游截断/不可达：不得出现正常 [DONE] 收尾（应是 error 帧或换渠道后失败）
	if strings.Contains(w.Body.String(), `"finish_reason":"stop"`) {
		t.Fatalf("truncated stream must not emit normal finish:\n%s", w.Body.String())
	}
}
