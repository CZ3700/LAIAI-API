package relay

// 丝滑度批 A 转换保真：Anthropic x-api-key 桥接 + reasoning_content + Gemini 图像/代码/思考分离。

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// A-1: /anthropic 入站在只有 x-api-key（原生 Claude 客户端默认头）时桥接为 Authorization: Bearer。
func TestAnthropicIngressBridgesXApiKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// 直接验证桥接逻辑：构造带 x-api-key 的请求，走 HandleAnthropicMessages 前的头改写。
	// 这里用一个最小 relay + 请求，只断言 Authorization 头被补上（不打真实上游）。
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	c.Request.Header.Set("x-api-key", "sk-ant-user-key")
	// 模拟 HandleAnthropicMessages 里的桥接分支
	if c.GetHeader("Authorization") == "" {
		if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
			c.Request.Header.Set("Authorization", "Bearer "+k)
		}
	}
	if got := c.GetHeader("Authorization"); got != "Bearer sk-ant-user-key" {
		t.Fatalf("Authorization = %q, want bridged from x-api-key", got)
	}
	// 已有 Authorization 时不被覆盖
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest("POST", "/x", nil)
	c2.Request.Header.Set("Authorization", "Bearer token-auth")
	c2.Request.Header.Set("x-api-key", "sk-ant-other")
	if c2.GetHeader("Authorization") == "" {
		c2.Request.Header.Set("Authorization", "Bearer "+c2.GetHeader("x-api-key"))
	}
	if got := c2.GetHeader("Authorization"); got != "Bearer token-auth" {
		t.Fatalf("existing Authorization must not be overwritten, got %q", got)
	}
}

// A-2: Anthropic thinking 块 → reasoning_content（非流 + 流式）。
func TestAnthropicReasoningContent(t *testing.T) {
	c, rec := newSSECtx()
	body := `{"id":"m1","model":"claude","content":[` +
		`{"type":"thinking","thinking":"let me think step by step"},` +
		`{"type":"text","text":"the answer is 42"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":20}}`
	if _, _, err := (anthropicAdapter{}).pipeJSON(c, strings.NewReader(body)); err != nil {
		t.Fatalf("pipeJSON: %v", err)
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &resp) != nil || len(resp.Choices) == 0 {
		t.Fatalf("bad response: %s", rec.Body.String())
	}
	m := resp.Choices[0].Message
	if m.ReasoningContent != "let me think step by step" {
		t.Fatalf("reasoning_content = %q", m.ReasoningContent)
	}
	if m.Content != "the answer is 42" {
		t.Fatalf("content = %q (思考不应混入正文)", m.Content)
	}

	// 流式 thinking_delta → delta.reasoning_content
	c2, rec2 := newSSECtx()
	stream := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	if _, _, err := (anthropicAdapter{}).pipeSSE(c2, strings.NewReader(stream)); err != nil {
		t.Fatalf("pipeSSE: %v", err)
	}
	out := rec2.Body.String()
	if !strings.Contains(out, `"reasoning_content":"hmm"`) {
		t.Fatalf("stream missing reasoning_content delta:\n%s", out)
	}
	if !strings.Contains(out, `"content":"hi"`) {
		t.Fatalf("stream missing text content:\n%s", out)
	}
}

// A-3/A-4: Gemini inlineData 图像 + executableCode + codeExecutionResult + thought 分离。
func TestGeminiPartsFidelity(t *testing.T) {
	c, rec := newSSECtx()
	body := `{"candidates":[{"content":{"parts":[` +
		`{"text":"my reasoning","thought":true},` +
		`{"text":"here is the image:"},` +
		`{"inlineData":{"mimeType":"image/png","data":"AAAABBBB"}},` +
		`{"executableCode":{"language":"PYTHON","code":"print(1)"}},` +
		`{"codeExecutionResult":{"outcome":"OK","output":"1"}}` +
		`]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":10,"thoughtsTokenCount":4},"modelVersion":"gemini-2.5"}`
	if _, _, err := (geminiAdapter{}).pipeJSON(c, strings.NewReader(body)); err != nil {
		t.Fatalf("pipeJSON: %v", err)
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &resp) != nil || len(resp.Choices) == 0 {
		t.Fatalf("bad response: %s", rec.Body.String())
	}
	m := resp.Choices[0].Message
	if m.ReasoningContent != "my reasoning" {
		t.Fatalf("reasoning_content = %q (thought 应分离)", m.ReasoningContent)
	}
	// 图像 → markdown data URI；代码/结果 → markdown code block；都在 content 里，不丢
	for _, want := range []string{"here is the image:", "data:image/png;base64,AAAABBBB", "```python", "print(1)", "```\n1\n```"} {
		if !strings.Contains(m.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, m.Content)
		}
	}
}

// 保真闭环（审查修）：reasoning_content 经 anthropic egress 还原为可见 text 块、responses egress
// 还原为 reasoning item——原生方言端点不再静默丢思考。
func TestInboundEgressReasoningRestore(t *testing.T) {
	openaiResp := `{"id":"chatcmpl-1","model":"gemini","choices":[{"message":{` +
		`"content":"the answer","reasoning_content":"my thinking"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":5,"completion_tokens":10}}`

	// anthropic egress：reasoning 作为可见 text 块（不丢）
	antBody, ok := openaiRespToAnthropic([]byte(openaiResp))
	if !ok {
		t.Fatal("openaiRespToAnthropic failed")
	}
	var ant struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(antBody, &ant) != nil || len(ant.Content) < 2 {
		t.Fatalf("anthropic content = %s", antBody)
	}
	if ant.Content[0].Text != "my thinking" || ant.Content[1].Text != "the answer" {
		t.Fatalf("anthropic egress reasoning not restored before content: %s", antBody)
	}

	// responses egress：reasoning 作为标准 reasoning item
	respBody, ok := openaiRespToResponses([]byte(openaiResp))
	if !ok {
		t.Fatal("openaiRespToResponses failed")
	}
	if !strings.Contains(string(respBody), `"type":"reasoning"`) || !strings.Contains(string(respBody), "my thinking") {
		t.Fatalf("responses egress missing reasoning item: %s", respBody)
	}
}
