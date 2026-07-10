package relay

// OpenAI→Anthropic response_format 保真（顶级审计修排名 4）：
// json_schema 哨兵工具注入 + 响应侧重写回 content；有客户端工具/thinking 时降级 system 指令。

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func anthropicBuildBody(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	ch := &Channel{BaseURL: "https://api.anthropic.com/v1"}
	upReq, err := anthropicAdapter{}.buildRequest(context.Background(), ch, "sk-test", req, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	raw, _ := io.ReadAll(upReq.Body)
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		t.Fatalf("body not json: %s", raw)
	}
	return out
}

func TestResponseFormatJSONSchemaInjectsSentinelTool(t *testing.T) {
	out := anthropicBuildBody(t, map[string]any{
		"model":    "claude-x",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "answer",
				"schema": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
			},
		},
	})
	tools, ok := out["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("want 1 injected tool, got %v", out["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != anthropicJSONEmuTool {
		t.Fatalf("tool name = %v, want sentinel", tool["name"])
	}
	schema := tool["input_schema"].(map[string]any)
	if _, ok := schema["properties"]; !ok {
		t.Fatalf("schema not carried: %v", schema)
	}
	tc, _ := out["tool_choice"].(map[string]any)
	if tc == nil || tc["type"] != "tool" || tc["name"] != anthropicJSONEmuTool {
		t.Fatalf("tool_choice = %v, want forced sentinel", out["tool_choice"])
	}
}

func TestResponseFormatDegradesWithClientToolsOrThinking(t *testing.T) {
	// 客户端自带工具：不注入哨兵（会饿死客户端工具），降级 system 指令
	out := anthropicBuildBody(t, map[string]any{
		"model":    "claude-x",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "get_weather", "parameters": map[string]any{"type": "object"}}}},
		"response_format": map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"schema": map[string]any{"type": "object"}}},
	})
	if tc, _ := out["tool_choice"].(map[string]any); tc != nil && tc["name"] == anthropicJSONEmuTool {
		t.Fatal("must not force sentinel when client has own tools")
	}
	sys, _ := out["system"].(string)
	if !strings.Contains(sys, "JSON Schema") {
		t.Fatalf("degraded system instruction missing: %q", sys)
	}

	// thinking：Claude 禁止 thinking+强制 tool_choice → 同样降级
	out2 := anthropicBuildBody(t, map[string]any{
		"model":    "claude-x",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(1024)},
		"response_format": map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"schema": map[string]any{"type": "object"}}},
	})
	if _, hasTools := out2["tools"]; hasTools {
		t.Fatal("must not inject sentinel tool alongside thinking")
	}
	if sys2, _ := out2["system"].(string); !strings.Contains(sys2, "JSON Schema") {
		t.Fatalf("thinking degrade instruction missing: %q", sys2)
	}
}

func TestResponseFormatJSONObjectSystemInstruction(t *testing.T) {
	out := anthropicBuildBody(t, map[string]any{
		"model":           "claude-x",
		"messages":        []any{map[string]any{"role": "system", "content": "be brief"}, map[string]any{"role": "user", "content": "hi"}},
		"response_format": map[string]any{"type": "json_object"},
	})
	sys, _ := out["system"].(string)
	if !strings.Contains(sys, "be brief") || !strings.Contains(sys, "valid JSON object") {
		t.Fatalf("json_object instruction must append after existing system: %q", sys)
	}
	if _, hasTools := out["tools"]; hasTools {
		t.Fatal("json_object must not inject tools")
	}
}

// 响应侧非流：哨兵 tool_use 的 input 重写回 content，finish_reason=stop，无 tool_calls。
func TestPipeJSONRewritesSentinelToolToContent(t *testing.T) {
	c, rec := newSSECtx()
	body := `{"id":"m1","model":"claude-x","content":[{"type":"tool_use","id":"tu1","name":"` + anthropicJSONEmuTool + `",` +
		`"input":{"a":"hello"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`
	_, _, err := anthropicAdapter{}.pipeJSON(c, strings.NewReader(body))
	if err != nil {
		t.Fatalf("pipeJSON: %v", err)
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []any  `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &resp) != nil || len(resp.Choices) == 0 {
		t.Fatalf("bad converted response: %s", rec.Body.String())
	}
	got := resp.Choices[0]
	var content map[string]any
	if json.Unmarshal([]byte(got.Message.Content), &content) != nil || content["a"] != "hello" {
		t.Fatalf("content = %q, want the structured JSON", got.Message.Content)
	}
	if len(got.Message.ToolCalls) != 0 {
		t.Fatal("sentinel must not surface as tool_calls")
	}
	if got.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop", got.FinishReason)
	}
}

// 响应侧流式：哨兵块的 input_json_delta 以 content 增量流出，收尾 finish_reason=stop。
func TestPipeSSEStreamsSentinelAsContent(t *testing.T) {
	c, rec := newSSECtx()
	stream := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu1\",\"name\":\"" + anthropicJSONEmuTool + "\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\\\":\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"ok\\\"}\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	_, _, err := anthropicAdapter{}.pipeSSE(c, strings.NewReader(stream))
	if err != nil {
		t.Fatalf("pipeSSE: %v", err)
	}
	out := rec.Body.String()
	if strings.Contains(out, "tool_calls") {
		t.Fatalf("sentinel leaked as tool_calls:\n%s", out)
	}
	// 拼出的 content 增量应还原完整 JSON
	if !strings.Contains(out, `{\"a\":`) || !strings.Contains(out, `\"ok\"}`) {
		t.Fatalf("json content deltas missing:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("finish_reason must be stop:\n%s", out)
	}
}
