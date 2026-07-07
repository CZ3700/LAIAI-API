package relay

import (
	"testing"

	"github.com/shopspring/decimal"
)

// RealtimeCost：text/audio 分列计价 + 缓存子集折扣 + 音频价未配回退文本价。
func TestRealtimeCost(t *testing.T) {
	p := &ModelPrice{
		InputUSDPer1M:       decimal.NewFromInt(10),
		OutputUSDPer1M:      decimal.NewFromInt(30),
		AudioInputUSDPer1M:  decimal.NewFromInt(100),
		AudioOutputUSDPer1M: decimal.NewFromInt(200),
		CachedInputUSDPer1M: decimal.NewFromInt(1),
	}
	// text_in=1000, text_out=1000, audio_in=10000, audio_out=10000, cached=0
	// = 1000*10/1M + 1000*30/1M + 10000*100/1M + 10000*200/1M = 0.01+0.03+1+2 = 3.04
	got := p.RealtimeCost(RealtimeUsage{TextInput: 1000, TextOutput: 1000, AudioInput: 10000, AudioOutput: 10000})
	if !got.Equal(decimal.RequireFromString("3.04")) {
		t.Fatalf("cost = %s, want 3.04", got)
	}
	// 缓存子集：text_in=1000 中 400 命中缓存（cached 价 1）→ (600*10 + 400*1)/1M = 6400/1M = 0.0064
	got = p.RealtimeCost(RealtimeUsage{TextInput: 1000, CachedInput: 400})
	if !got.Equal(decimal.RequireFromString("0.0064")) {
		t.Fatalf("cached cost = %s, want 0.0064", got)
	}
	// cached 不超过 text_in（防御）
	got = p.RealtimeCost(RealtimeUsage{TextInput: 100, CachedInput: 999})
	if !got.Equal(decimal.RequireFromString("0.0001")) { // 全按缓存价 100*1/1M
		t.Fatalf("cached clamp cost = %s, want 0.0001", got)
	}

	// 音频价未配 → 音频计 0（对抗 review：绝不回退更便宜的文本价冒充；由建连预检 fail-closed 拦住）
	p2 := &ModelPrice{InputUSDPer1M: decimal.NewFromInt(10), OutputUSDPer1M: decimal.NewFromInt(30)}
	if p2.RealtimeAudioPriced() {
		t.Fatal("model without audio price must not pass RealtimeAudioPriced (pre-check gate)")
	}
	got = p2.RealtimeCost(RealtimeUsage{AudioInput: 1000, AudioOutput: 1000})
	if !got.IsZero() {
		t.Fatalf("unpriced audio cost = %s, want 0 (绝不按文本价冒充)", got)
	}
	// 音频价配齐 → RealtimeAudioPriced 通过
	if !p.RealtimeAudioPriced() {
		t.Fatal("model with audio in+out price must pass RealtimeAudioPriced")
	}
	// 只配一侧（in 有 out 无）→ 不通过（须双侧）
	p3 := &ModelPrice{InputUSDPer1M: decimal.NewFromInt(10), OutputUSDPer1M: decimal.NewFromInt(30), AudioInputUSDPer1M: decimal.NewFromInt(100)}
	if p3.RealtimeAudioPriced() {
		t.Fatal("audio in-only must not pass (须 in+out 双侧)")
	}
}

// parseRealtimeDone：只从 response.done 且带 usage 的帧抽 token；其余帧 (,false)。
func TestParseRealtimeDone(t *testing.T) {
	done := `{"type":"response.done","response":{"usage":{` +
		`"input_token_details":{"text_tokens":100,"audio_tokens":200,"cached_tokens":30},` +
		`"output_token_details":{"text_tokens":40,"audio_tokens":500}}}}`
	u, ok := parseRealtimeDone([]byte(done))
	if !ok {
		t.Fatal("response.done with usage must parse")
	}
	if u.TextInput != 100 || u.AudioInput != 200 || u.CachedInput != 30 || u.TextOutput != 40 || u.AudioOutput != 500 {
		t.Fatalf("parsed usage = %+v", u)
	}
	// 非 done 事件 → false
	for _, ev := range []string{
		`{"type":"response.audio.delta","delta":"..."}`,
		`{"type":"session.created","session":{}}`,
		`{"type":"response.done","response":{}}`, // done 但无 usage
		`not json`,
	} {
		if _, ok := parseRealtimeDone([]byte(ev)); ok {
			t.Fatalf("event %q must not parse as billable done", ev)
		}
	}
}
