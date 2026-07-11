package relay

import (
	"testing"

	"github.com/shopspring/decimal"
)

// G2：chat 里的音频 token（gpt-4o-audio）必须按 audio 价补差额，不能按文本价漏收。
func TestAudioSurcharge(t *testing.T) {
	dec := decimal.RequireFromString
	// 文本 $2.5/1M in、$10/1M out；音频 $40/1M in、$80/1M out（gpt-4o-audio 量级）
	p := ModelPrice{
		InputUSDPer1M: dec("2.5"), OutputUSDPer1M: dec("10"),
		AudioInputUSDPer1M: dec("40"), AudioOutputUSDPer1M: dec("80"),
	}
	// 1000 音频输入的差额 = (40-2.5)/1M×1000 = 0.0375；500 音频输出差额 = (80-10)/1M×500 = 0.035
	got := p.AudioSurcharge(1000, 500)
	if !got.Equal(dec("0.0725")) {
		t.Fatalf("audio surcharge = %s, want 0.0725", got)
	}
	// 与基价合计 = 全部按 audio 价：1000×40/1M + 500×80/1M = 0.04+0.04 = 0.08
	// 基价（Cost 按文本价）+ surcharge 应 = audio 全价
	base := p.Cost(1000, 0, 500) // 1000×2.5/1M + 500×10/1M = 0.0025+0.005 = 0.0075
	total := base.Add(got)
	if !total.Equal(dec("0.08")) {
		t.Fatalf("base+surcharge = %s, want 0.08 (= audio full price)", total)
	}

	// 未配音频价（0）→ 不补（丝滑：按文本价，不拦截请求）
	pNoAudio := ModelPrice{InputUSDPer1M: dec("2.5"), OutputUSDPer1M: dec("10")}
	if s := pNoAudio.AudioSurcharge(1000, 500); !s.IsZero() {
		t.Fatalf("no audio price → surcharge must be 0, got %s", s)
	}
	// 只配输入音频价：只补输入侧
	pInOnly := ModelPrice{InputUSDPer1M: dec("2.5"), OutputUSDPer1M: dec("10"), AudioInputUSDPer1M: dec("40")}
	if s := pInOnly.AudioSurcharge(1000, 500); !s.Equal(dec("0.0375")) {
		t.Fatalf("input-only audio surcharge = %s, want 0.0375", s)
	}
	// 按次固定价忽略 token
	pPerCall := p
	pPerCall.PerCallUSD = dec("0.02")
	if s := pPerCall.AudioSurcharge(1000, 500); !s.IsZero() {
		t.Fatalf("per-call → audio surcharge must be 0, got %s", s)
	}
	// 无音频 token → 0（非音频请求零开销）
	if s := p.AudioSurcharge(0, 0); !s.IsZero() {
		t.Fatalf("no audio tokens → 0, got %s", s)
	}
}

// usage 的 audio token 访问器钳制（防上游异常数据算飞）。
func TestUsageAudioClamp(t *testing.T) {
	var u usage
	u.PromptTokens = 100
	u.CompletionTokens = 50
	u.PromptTokensDetails.AudioTokens = 999 // 超 prompt
	u.CompletionTokensDetails.AudioTokens = -5
	if u.AudioIn() != 100 {
		t.Fatalf("AudioIn clamp = %d, want 100", u.AudioIn())
	}
	if u.AudioOut() != 0 {
		t.Fatalf("AudioOut negative → 0, got %d", u.AudioOut())
	}
}
