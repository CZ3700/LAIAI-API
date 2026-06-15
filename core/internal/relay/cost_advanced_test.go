package relay

import (
	"testing"

	"github.com/shopspring/decimal"
)

// 按次固定价：PerCallUSD>0 时 ChatCost 忽略 token 返回固定价；=0 时回退 token 计价。
func TestChatCostPerCall(t *testing.T) {
	dec := decimal.RequireFromString
	// 无按次价 → 按 token
	p := ModelPrice{InputUSDPer1M: dec("10"), OutputUSDPer1M: dec("30")}
	if got := p.ChatCost(1000, 0, 500); !got.Equal(dec("0.025")) { // 1000×10/1M + 500×30/1M = 0.01+0.015
		t.Fatalf("token ChatCost = %s, want 0.025", got)
	}
	// 有按次价 → 固定，忽略 token 多少
	p.PerCallUSD = dec("0.02")
	if got := p.ChatCost(1000, 0, 500); !got.Equal(dec("0.02")) {
		t.Fatalf("per-call ChatCost(1000,0,500) = %s, want 0.02", got)
	}
	if got := p.ChatCost(999999, 0, 999999); !got.Equal(dec("0.02")) {
		t.Fatalf("per-call ChatCost(huge) = %s, want 0.02 (flat)", got)
	}
}

// TTS 按字符计价：tts_usd_per_1k_chars × chars / 1000。
func TestSpeechCost(t *testing.T) {
	dec := decimal.RequireFromString
	p := ModelPrice{TTSPer1KChars: dec("0.015")}
	if got := p.SpeechCost(1000); !got.Equal(dec("0.015")) {
		t.Fatalf("SpeechCost(1000) = %s, want 0.015", got)
	}
	if got := p.SpeechCost(500); !got.Equal(dec("0.0075")) {
		t.Fatalf("SpeechCost(500) = %s, want 0.0075", got)
	}
	if got := p.SpeechCost(0); !got.IsZero() {
		t.Fatalf("SpeechCost(0) = %s, want 0", got)
	}
}
