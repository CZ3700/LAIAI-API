package relay

import (
	"strings"
	"testing"
)

// P2-9：Anthropic 把 cache_read/cache_creation 计入总 prompt，且 cache_read 作为缓存子集
// （按折扣价单独计费）。input_tokens 不含缓存部分，故 prompt = 100+30+20=150，cached=30。
func TestAnthropicPipeJSONExtractsCachedTokens(t *testing.T) {
	c, _ := newSSECtx()
	body := `{"id":"x","model":"claude","content":[{"type":"text","text":"hi"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50,` +
		`"cache_read_input_tokens":30,"cache_creation_input_tokens":20}}`
	u, _, err := anthropicAdapter{}.pipeJSON(c, strings.NewReader(body))
	if err != nil {
		t.Fatalf("pipeJSON err: %v", err)
	}
	if u.PromptTokens != 150 {
		t.Fatalf("prompt = %d, want 150 (input+cache_read+cache_creation)", u.PromptTokens)
	}
	if u.Cached() != 30 {
		t.Fatalf("cached = %d, want 30 (cache_read)", u.Cached())
	}
	if u.CompletionTokens != 50 {
		t.Fatalf("completion = %d, want 50", u.CompletionTokens)
	}
}

// P2-9：Gemini promptTokenCount 已含缓存，cachedContentTokenCount 是其子集（折扣价）。
func TestGeminiPipeJSONExtractsCachedTokens(t *testing.T) {
	c, _ := newSSECtx()
	body := `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":200,"candidatesTokenCount":50,"cachedContentTokenCount":80},` +
		`"modelVersion":"gemini-2.0"}`
	u, _, err := geminiAdapter{}.pipeJSON(c, strings.NewReader(body))
	if err != nil {
		t.Fatalf("pipeJSON err: %v", err)
	}
	if u.PromptTokens != 200 {
		t.Fatalf("prompt = %d, want 200 (promptTokenCount, already includes cached)", u.PromptTokens)
	}
	if u.Cached() != 80 {
		t.Fatalf("cached = %d, want 80 (cachedContentTokenCount)", u.Cached())
	}
}
