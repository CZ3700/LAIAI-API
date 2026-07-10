package relay

// 顶级审计修（2026-07）两处系统性少收：
//   1) Gemini 思考模型 thoughtsTokenCount 漏计——completion 必须 = candidates + thoughts；
//   2) Anthropic cache_creation 实收 1.25x(5m)/2x(1h) 输入价，promptTotal 只计 1.0x，溢价补收。

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// Gemini 非流：completion = candidates + thoughts，reasoning 子集单独可见。
func TestGeminiPipeJSONCountsThoughtTokens(t *testing.T) {
	c, _ := newSSECtx()
	body := `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50,"thoughtsTokenCount":2000},` +
		`"modelVersion":"gemini-2.5-pro"}`
	u, _, err := geminiAdapter{}.pipeJSON(c, strings.NewReader(body))
	if err != nil {
		t.Fatalf("pipeJSON err: %v", err)
	}
	if u.CompletionTokens != 2050 {
		t.Fatalf("completion = %d, want 2050 (candidates+thoughts，漏 thoughts=系统性少收)", u.CompletionTokens)
	}
	if u.CompletionTokensDetails.ReasoningTokens != 2000 {
		t.Fatalf("reasoning = %d, want 2000", u.CompletionTokensDetails.ReasoningTokens)
	}
}

// Gemini 流式：最后一帧 usageMetadata 含 thoughts 时同样折入 completion。
func TestGeminiPipeSSECountsThoughtTokens(t *testing.T) {
	c, _ := newSSECtx()
	stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]," +
		"\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":50,\"thoughtsTokenCount\":700}}\n\n"
	u, _, err := geminiAdapter{}.pipeSSE(c, strings.NewReader(stream))
	if err != nil {
		t.Fatalf("pipeSSE err: %v", err)
	}
	if u.CompletionTokens != 750 {
		t.Fatalf("stream completion = %d, want 750 (candidates+thoughts)", u.CompletionTokens)
	}
	if u.CompletionTokensDetails.ReasoningTokens != 700 {
		t.Fatalf("stream reasoning = %d, want 700", u.CompletionTokensDetails.ReasoningTokens)
	}
}

// Anthropic 非流：cache_creation 无分列明细 → 全按 5m 档；有分列 → 按分列。
func TestAnthropicPipeJSONCacheWrites(t *testing.T) {
	c, _ := newSSECtx()
	body := `{"id":"x","model":"claude","content":[{"type":"text","text":"hi"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50,` +
		`"cache_read_input_tokens":30,"cache_creation_input_tokens":20}}`
	u, _, err := anthropicAdapter{}.pipeJSON(c, strings.NewReader(body))
	if err != nil {
		t.Fatalf("pipeJSON err: %v", err)
	}
	if w5m, w1h := u.CacheWrites(); w5m != 20 || w1h != 0 {
		t.Fatalf("cacheWrites = (%d,%d), want (20,0)（无明细全按 5m 低档，不猜 2x 多收）", w5m, w1h)
	}

	c2, _ := newSSECtx()
	body2 := `{"id":"x","model":"claude","content":[{"type":"text","text":"hi"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50,` +
		`"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":5}}}`
	u2, _, err := anthropicAdapter{}.pipeJSON(c2, strings.NewReader(body2))
	if err != nil {
		t.Fatalf("pipeJSON err: %v", err)
	}
	if w5m, w1h := u2.CacheWrites(); w5m != 15 || w1h != 5 {
		t.Fatalf("cacheWrites = (%d,%d), want (15,5)（按上游分列明细）", w5m, w1h)
	}
}

// 对抗 review CONFIRMED：明细和 > legacy 总数（坏中转渠道）→ 回退 (总数,0) 全按 5m 低档。
// promptTotal 的 1.0x 基价只计总数，全信明细=对幽灵 token 收溢价=多收客户（红线）。
func TestAnthropicCacheWritesReconciliation(t *testing.T) {
	u := anthropicUsage{InputTokens: 10000, CacheCreationInputTokens: 100}
	u.CacheCreation = &struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	}{Ephemeral5m: 0, Ephemeral1h: 8000}
	if w5m, w1h := u.cacheWrites(); w5m != 100 || w1h != 0 {
		t.Fatalf("inconsistent detail = (%d,%d), want (100,0)（钳回 legacy 总数，宁少不多）", w5m, w1h)
	}
	// 明细和 ≤ 总数：正常采信明细
	u2 := anthropicUsage{CacheCreationInputTokens: 20}
	u2.CacheCreation = &struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	}{Ephemeral5m: 15, Ephemeral1h: 5}
	if w5m, w1h := u2.cacheWrites(); w5m != 15 || w1h != 5 {
		t.Fatalf("consistent detail = (%d,%d), want (15,5)", w5m, w1h)
	}
	// 负值钳 0
	u3 := anthropicUsage{CacheCreationInputTokens: -7}
	if w5m, w1h := u3.cacheWrites(); w5m != 0 || w1h != 0 {
		t.Fatalf("negative legacy = (%d,%d), want (0,0)", w5m, w1h)
	}
}

// 对抗 review CONFIRMED：负 thoughtsTokenCount/candidates 不得拉低计费或算出负 actual。
func TestGeminiCompletionTotalNegativeClamp(t *testing.T) {
	g := geminiUsage{CandidatesTokenCount: 50, ThoughtsTokenCount: -2000}
	if g.completionTotal() != 50 {
		t.Fatalf("negative thoughts: completionTotal = %d, want 50", g.completionTotal())
	}
	g2 := geminiUsage{CandidatesTokenCount: -50, ThoughtsTokenCount: 100}
	if g2.completionTotal() != 100 {
		t.Fatalf("negative candidates: completionTotal = %d, want 100", g2.completionTotal())
	}
}

// Anthropic 流式：message_start 携带的 cache_creation 进入计费字段。
func TestAnthropicPipeSSECacheWrites(t *testing.T) {
	c, _ := newSSECtx()
	stream := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100," +
		"\"cache_creation_input_tokens\":40,\"cache_creation\":{\"ephemeral_5m_input_tokens\":10,\"ephemeral_1h_input_tokens\":30}}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	u, _, err := anthropicAdapter{}.pipeSSE(c, strings.NewReader(stream))
	if err != nil {
		t.Fatalf("pipeSSE err: %v", err)
	}
	if w5m, w1h := u.CacheWrites(); w5m != 10 || w1h != 30 {
		t.Fatalf("stream cacheWrites = (%d,%d), want (10,30)", w5m, w1h)
	}
}

// 溢价计价：5m 补 0.25x、1h 补 1.0x 输入价；按次固定价模型不补；零写恒零。
func TestCacheWriteSurcharge(t *testing.T) {
	p := &ModelPrice{InputUSDPer1M: decimal.RequireFromString("10")}
	// 1000×5m：10 × 0.25 × 1000 / 1M = 0.0025
	if got := p.CacheWriteSurcharge(1000, 0); !got.Equal(decimal.RequireFromString("0.0025")) {
		t.Fatalf("5m surcharge = %s, want 0.0025", got)
	}
	// 1000×1h：10 × 1.0 × 1000 / 1M = 0.01
	if got := p.CacheWriteSurcharge(0, 1000); !got.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("1h surcharge = %s, want 0.01", got)
	}
	if !p.CacheWriteSurcharge(0, 0).IsZero() {
		t.Fatal("zero writes must cost zero")
	}
	flat := &ModelPrice{InputUSDPer1M: decimal.RequireFromString("10"), PerCallUSD: decimal.RequireFromString("0.02")}
	if !flat.CacheWriteSurcharge(1000, 1000).IsZero() {
		t.Fatal("per-call 固定价模型不补写缓存溢价（与 ChatCost 忽略 token 同语义）")
	}
}

// CacheWrites 钳制：总量不得超过 PromptTokens（防上游异常数据把溢价算飞）。
func TestUsageCacheWritesClamp(t *testing.T) {
	u := usage{PromptTokens: 100, CacheWrite5m: 90, CacheWrite1h: 50}
	if w5m, w1h := u.CacheWrites(); w5m != 90 || w1h != 10 {
		t.Fatalf("clamp = (%d,%d), want (90,10)", w5m, w1h)
	}
	u2 := usage{PromptTokens: 100, CacheWrite5m: 200}
	if w5m, w1h := u2.CacheWrites(); w5m != 100 || w1h != 0 {
		t.Fatalf("clamp = (%d,%d), want (100,0)", w5m, w1h)
	}
	u3 := usage{PromptTokens: 100, CacheWrite5m: -5, CacheWrite1h: -7}
	if w5m, w1h := u3.CacheWrites(); w5m != 0 || w1h != 0 {
		t.Fatalf("negative clamp = (%d,%d), want (0,0)", w5m, w1h)
	}
}
