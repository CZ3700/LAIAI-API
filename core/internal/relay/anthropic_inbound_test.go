package relay

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 流截断（对抗评审 HIGH）：适配器注入 OpenAI error 帧 + [DONE] → 必须发 Anthropic error 事件，
// 且**绝不**伪造干净 message_stop（截断不得被掩盖成成功）。
func TestAnthropicWriterTruncatedStream(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	aw := &anthropicWriter{ResponseWriter: c.Writer, stream: true}
	aw.WriteHeader(200)
	aw.WriteString("data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	// emitStreamError 注入的帧：
	aw.WriteString("data: {\"error\":{\"message\":\"upstream stream stalled\",\"type\":\"upstream_error\"}}\n\n")
	aw.WriteString("data: [DONE]\n\n")
	aw.finalize()

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "upstream stream stalled") {
		t.Fatalf("truncated stream must emit anthropic error event:\n%s", body)
	}
	if strings.Contains(body, "event: message_stop") {
		t.Fatalf("truncated stream must NOT emit a clean message_stop:\n%s", body)
	}
}

// 正常流收尾对照：完整 [DONE] → 干净 message_stop，无 error 事件。
func TestAnthropicWriterCleanStream(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	aw := &anthropicWriter{ResponseWriter: c.Writer, stream: true}
	aw.WriteHeader(200)
	aw.WriteString("data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	aw.WriteString("data: {\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
	aw.WriteString("data: [DONE]\n\n")
	aw.finalize()

	body := rec.Body.String()
	if !strings.Contains(body, "event: message_stop") || strings.Contains(body, "event: error") {
		t.Fatalf("clean stream must end with message_stop and no error:\n%s", body)
	}
}

// ingress 边角（对抗评审 MEDIUM-1 / LOW-3）：cache_control 透传 + tool_result 对象内容不丢。
func TestAnthropicIngressEdges(t *testing.T) {
	raw := []byte(`{
      "model":"m","max_tokens":100,
      "messages":[
        {"role":"user","content":[{"type":"text","text":"ctx","cache_control":{"type":"ephemeral"}}]},
        {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{"a":1}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":{"ok":true}}]}
      ]}`)
	body, stream, err := anthropicToOpenAIRequest(raw)
	if err != nil || stream {
		t.Fatalf("err=%v stream=%v", err, stream)
	}
	var out map[string]any
	if json.Unmarshal(body, &out) != nil {
		t.Fatal("output not valid json")
	}
	msgs := out["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3 (user/assistant/tool)", len(msgs))
	}
	// cache_control 文本块 → parts 形态保留 cache_control
	parts, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok || parts[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("cache_control must be preserved in parts form: %v", msgs[0])
	}
	// assistant tool_use → tool_calls
	if msgs[1].(map[string]any)["tool_calls"] == nil {
		t.Fatalf("tool_use not converted to tool_calls: %v", msgs[1])
	}
	// tool_result 对象内容 → role:tool，内容序列化不丢
	m2 := msgs[2].(map[string]any)
	if m2["role"] != "tool" {
		t.Fatalf("tool_result not split into role:tool: %v", m2)
	}
	if s, _ := m2["content"].(string); !strings.Contains(s, "ok") {
		t.Fatalf("tool_result object content lost: %v", m2["content"])
	}
}

// egress JSON：OpenAI 完成 → Anthropic Messages 形态 + usage remainder 形式。
func TestAnthropicEgressJSON(t *testing.T) {
	raw := []byte(`{"id":"c1","model":"m","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40}}}`)
	out, ok := openaiRespToAnthropic(raw)
	if !ok {
		t.Fatal("convert failed")
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["type"] != "message" || m["stop_reason"] != "end_turn" {
		t.Fatalf("shape/stop_reason = %v", m)
	}
	u := m["usage"].(map[string]any)
	// input_tokens = prompt(100) - cached(40) = 60（remainder 形式，避免再转发时 promptTotal 重复计）
	if u["input_tokens"].(float64) != 60 || u["cache_read_input_tokens"].(float64) != 40 || u["output_tokens"].(float64) != 20 {
		t.Fatalf("usage remainder wrong: %v", u)
	}
}
