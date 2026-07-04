package server

// OpenAI Responses 原生入站（/v1/responses）：复用 chat 计费管道，请求/响应/流式双向转换（指令 §3.2）。
// 验证：Responses 格式进出正确、计费与 /v1/chat/completions 等价、错误走 Responses 包络、无状态边界拒绝。

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

// captureUpstream：记录收到的最后一个请求体（验证 ingress 转换），返回可编程 JSON 响应。
func captureUpstream(t *testing.T, respBody string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var last atomic.Value
	last.Store(map[string]any{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		last.Store(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

// 非流式文本：Responses 请求 → 上游 chat 完成 → Responses 响应对象；计费与 chat 等价。
func TestResponsesInboundNonStream(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "resp@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model",
		"input": "hi",
	})
	if w.Code != 200 {
		t.Fatalf("responses relay = %d %v", w.Code, out)
	}
	if out["object"] != "response" || out["status"] != "completed" {
		t.Fatalf("not responses shape: %v", out)
	}
	if !strings.HasPrefix(out["id"].(string), "resp_") {
		t.Fatalf("id = %v, want resp_ prefix", out["id"])
	}
	output := out["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %v", output)
	}
	msg := output[0].(map[string]any)
	if msg["type"] != "message" || msg["role"] != "assistant" {
		t.Fatalf("output item = %v", msg)
	}
	content := msg["content"].([]any)
	part := content[0].(map[string]any)
	if part["type"] != "output_text" || part["text"] != "hello" {
		t.Fatalf("content part = %v", part)
	}
	usage := out["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 1000 || usage["output_tokens"].(float64) != 500 {
		t.Fatalf("usage = %v", usage)
	}
	// Content-Length 必须等于转换后 body 长度（适配器钉的旧 OpenAI 长度会让真实 socket 截断，CRITICAL）
	if cl := w.Header().Get("Content-Length"); cl != "" && cl != fmt.Sprint(w.Body.Len()) {
		t.Fatalf("Content-Length=%s != body len %d — wire would truncate", cl, w.Body.Len())
	}
	// 计费等价：1000/1M*2 + 500/1M*6 = 0.005 → 9.995
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.995")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("balance = %s held = %s, want 9.995/0", acc.PostedBalance, acc.HeldAmount)
	}
}

// ingress 转换：instructions + input 数组（含 function_call_output）+ 扁平 tools → 正确的 chat 请求。
func TestResponsesInboundIngressConversion(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up, last := captureUpstream(t, `{"id":"cmpl-x","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "resp-ingress@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model":        "test-model",
		"instructions": "You are helpful",
		"input": []map[string]any{
			{"role": "user", "content": "hi"},
			{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "sunny"},
		},
		"tools": []map[string]any{
			{"type": "function", "name": "get_weather", "description": "w", "parameters": map[string]any{"type": "object"}},
			{"type": "web_search_preview"}, // 托管工具 → 应被丢弃
		},
		"tool_choice":       map[string]any{"type": "function", "name": "get_weather"},
		"max_output_tokens": 100,
	})
	if w.Code != 200 {
		t.Fatalf("ingress = %d", w.Code)
	}
	got := last.Load().(map[string]any)
	msgs := got["messages"].([]any)
	// system(instructions) + user + assistant(tool_calls) + tool
	if len(msgs) != 4 {
		t.Fatalf("messages = %d: %v", len(msgs), msgs)
	}
	if m0 := msgs[0].(map[string]any); m0["role"] != "system" || m0["content"] != "You are helpful" {
		t.Fatalf("system msg = %v", m0)
	}
	if m2 := msgs[2].(map[string]any); m2["role"] != "assistant" {
		t.Fatalf("assistant tool_calls msg = %v", m2)
	} else if _, ok := m2["tool_calls"].([]any); !ok {
		t.Fatalf("missing tool_calls: %v", m2)
	}
	if m3 := msgs[3].(map[string]any); m3["role"] != "tool" || m3["tool_call_id"] != "call_1" || m3["content"] != "sunny" {
		t.Fatalf("tool result msg = %v", m3)
	}
	// tools：扁平 function → chat 嵌套；web_search_preview 丢弃（只剩 1 个）
	tools := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want 1 (managed tool dropped)", tools)
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("tool fn = %v", fn)
	}
	// tool_choice：{type:function,name} → chat {type:function,function:{name}}
	tc := got["tool_choice"].(map[string]any)
	if tc["type"] != "function" || tc["function"].(map[string]any)["name"] != "get_weather" {
		t.Fatalf("tool_choice = %v", tc)
	}
	// max_output_tokens → max_tokens
	if got["max_tokens"].(float64) != 100 {
		t.Fatalf("max_tokens = %v", got["max_tokens"])
	}
}

// 多模态入站：input_image（裸 url 与 {url}）→ chat image_url parts。
func TestResponsesInboundMultimodal(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up, last := captureUpstream(t, `{"id":"c","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "resp-mm@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model",
		"input": []map[string]any{{"role": "user", "content": []map[string]any{
			{"type": "input_text", "text": "what is this"},
			{"type": "input_image", "image_url": "https://ex.com/a.png"},
			{"type": "input_image", "image_url": map[string]any{"url": "https://ex.com/b.png"}, "detail": "high"},
		}}},
	})
	if w.Code != 200 {
		t.Fatalf("multimodal = %d", w.Code)
	}
	msgs := last.Load().(map[string]any)["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	if len(parts) != 3 {
		t.Fatalf("content parts = %v", parts)
	}
	img1 := parts[1].(map[string]any)
	if img1["type"] != "image_url" || img1["image_url"].(map[string]any)["url"] != "https://ex.com/a.png" {
		t.Fatalf("image 1 = %v", img1)
	}
	img2 := parts[2].(map[string]any)
	if img2["image_url"].(map[string]any)["detail"] != "high" {
		t.Fatalf("image 2 detail = %v", img2)
	}
}

// 工具调用（非流）：上游 OpenAI tool_calls → Responses output 里的 function_call item。
func TestResponsesInboundToolCall(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up, _ := captureUpstream(t, `{"id":"cmpl-t","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "resp-tool@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model", "input": "weather?",
		"tools": []map[string]any{{"type": "function", "name": "get_weather", "parameters": map[string]any{"type": "object"}}},
	})
	if w.Code != 200 {
		t.Fatalf("tool call = %d %v", w.Code, out)
	}
	output := out["output"].([]any)
	fc := output[0].(map[string]any)
	if fc["type"] != "function_call" || fc["name"] != "get_weather" || fc["call_id"] != "call_1" {
		t.Fatalf("function_call item = %v", fc)
	}
	if fc["arguments"] != `{"city":"Paris"}` {
		t.Fatalf("arguments = %v", fc["arguments"])
	}
}

// 流式文本：Responses stream=true → 事件序列（created→output_item.added→content_part.added→
// output_text.delta→...→completed），无 [DONE]（Responses 协议靠 EOF 结束）；计费精确。
func TestResponsesInboundStream(t *testing.T) {
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
	apiKey, orgID := seedUserWithCredit(t, srv, "resp-stream@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model", "stream": true, "input": "hi",
	})
	if w.Code != 200 {
		t.Fatalf("responses stream = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"event: response.created", "event: response.output_item.added",
		"event: response.content_part.added", "event: response.output_text.delta",
		"event: response.output_text.done", "event: response.completed",
		`"delta":"he"`, `"delta":"llo"`, `"text":"hello"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("responses SSE missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("responses SSE must NOT contain [DONE]:\n%s", body)
	}
	// usage 在 completed 事件里
	if !strings.Contains(body, `"input_tokens":2000`) || !strings.Contains(body, `"output_tokens":1000`) {
		t.Fatalf("completed usage missing:\n%s", body)
	}
	// 计费：2000/1M*2 + 1000/1M*6 = 0.01 → 9.99
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.99")) {
		t.Fatalf("balance after stream = %s, want 9.99", acc.PostedBalance)
	}
}

// 流式工具调用：OpenAI tool_calls delta → function_call item + arguments.delta + arguments.done。
func TestResponsesInboundStreamToolCall(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"cmpl-x\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_9\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"\"}}]}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"q\\\":1}\"}}]}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "resp-stool@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model", "stream": true, "input": "go",
		"tools": []map[string]any{{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}},
	})
	if w.Code != 200 {
		t.Fatalf("stream tool = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"type":"function_call"`, `"call_id":"call_9"`, `"name":"lookup"`,
		"event: response.function_call_arguments.delta", "event: response.function_call_arguments.done",
		`"arguments":"{\"q\":1}"`, "event: response.completed",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream tool SSE missing %q in:\n%s", want, body)
		}
	}
}

// 无状态边界：previous_response_id → 400（无法续接已存储会话）。
func TestResponsesInboundStatelessBoundary(t *testing.T) {
	srv, _ := setupRelayServer(t)
	apiKey, _ := seedUserWithCredit(t, srv, "resp-state@example.com", "10")
	w, out := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model", "input": "hi", "previous_response_id": "resp_abc",
	})
	if w.Code != 400 {
		t.Fatalf("previous_response_id = %d, want 400", w.Code)
	}
	errObj := out["error"].(map[string]any)
	if errObj["type"] != "invalid_request_error" || !strings.Contains(errObj["message"].(string), "stateless") {
		t.Fatalf("error = %v", errObj)
	}
}

// 错误包络：余额不足 → Responses error 形态 + 402（非 OpenAI chat 形态）。
func TestResponsesInboundErrorEnvelope(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := mockUpstreamJSON(t, 10, 10)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "resp-poor@example.com", "") // 余额 0

	w, out := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model", "input": "hi",
	})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("insufficient balance = %d, want 402", w.Code)
	}
	errObj, ok := out["error"].(map[string]any)
	if !ok || errObj["type"] != "invalid_request_error" || errObj["message"] == "" {
		t.Fatalf("responses error = %v", out)
	}
	if cl := w.Header().Get("Content-Length"); cl != "" && cl != fmt.Sprint(w.Body.Len()) {
		t.Fatalf("Content-Length=%s != body len %d", cl, w.Body.Len())
	}
}

// 截断流：上游中途断（无 [DONE]）→ response.failed 事件，绝不伪造 completed。
func TestResponsesInboundStreamTruncated(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"cmpl-x\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		f.Flush()
		// 连接直接断，无 finish/无 [DONE]（httptest 关闭时截断）
	}))
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "resp-trunc@example.com", "10")
	// 关键：请求返回后关闭上游模拟截断——用同步 handler 已 flush 部分后 handler 返回即 EOF
	up.Close()

	w, _ := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model", "stream": true, "input": "hi",
	})
	body := w.Body.String()
	// 上游不可达/截断都必须落 response.failed（或 error），绝不出现 completed
	if strings.Contains(body, "event: response.completed") {
		t.Fatalf("truncated stream must NOT emit completed:\n%s", body)
	}
}

// usage 缺失兜底：上游 200 但无 usage → 走按字段估算（沿用 chat 的 BUG-H2 纪律，绝不少收）。
func TestResponsesInboundUsageMissingFallback(t *testing.T) {
	srv, db := setupRelayServer(t)
	up, _ := captureUpstream(t, `{"id":"cmpl-x","choices":[{"message":{"role":"assistant","content":"hello world"},"finish_reason":"stop"}]}`)
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "resp-nousage@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": "test-model", "input": "hi",
	})
	if w.Code != 200 {
		t.Fatalf("no-usage = %d %v", w.Code, out)
	}
	// 兜底扣费 > 0（不因上游缺 usage 而漏收）
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.LessThan(decimal.RequireFromString("10")) {
		t.Fatalf("balance = %s, want < 10 (estimated fallback billing)", acc.PostedBalance)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held = %s, want 0", acc.HeldAmount)
	}
}
