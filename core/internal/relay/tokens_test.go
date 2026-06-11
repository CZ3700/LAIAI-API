package relay

import "testing"

func chatReq(msgs ...map[string]any) map[string]any {
	arr := make([]any, len(msgs))
	for i, m := range msgs {
		arr[i] = m
	}
	return map[string]any{"model": "gpt-x", "messages": arr}
}

func TestCountChatTokensBasics(t *testing.T) {
	// "hello world" 在 cl100k 是 2 个 token；单条 user 消息 = 3(priming) + 4(开销) + 1(role) + 2 = 10
	req := chatReq(map[string]any{"role": "user", "content": "hello world"})
	if got := CountChatTokens(req, 100); got != 16 { // < 16 钳到下限 16
		// 10 < 16，应被钳到最小值
		t.Fatalf("tiny message tokens = %d, want floor 16", got)
	}

	// 长文本：tiktoken 计数应显著小于 chars/4 的中文场景或接近英文场景，且远大于下限
	long := ""
	for i := 0; i < 200; i++ {
		long += "The quick brown fox jumps over the lazy dog. "
	}
	req = chatReq(map[string]any{"role": "user", "content": long})
	got := CountChatTokens(req, len(long))
	if got < 1000 || got > 3000 {
		// 200 句 × ~10 token/句 ≈ 2000 ± 框架开销
		t.Fatalf("long text tokens = %d, expected ~2000", got)
	}

	// CJK：chars/4 会严重低估（一个汉字 ≈ 1-2 token 而非 0.25）；tiktoken 必须显著高于 chars/4
	cjk := ""
	for i := 0; i < 100; i++ {
		cjk += "聚合平台按量计费"
	}
	req = chatReq(map[string]any{"role": "user", "content": cjk})
	got = CountChatTokens(req, len(cjk))
	if naive := EstimateTokens(len(cjk)); got < naive {
		// utf-8 中文 3 字节/字，chars/4 按字节算约 0.75 token/字——tiktoken 应在 1+ token/字
		t.Fatalf("CJK tokens = %d not above naive byte estimate %d", got, naive)
	}
}

func TestCountChatTokensFallbacks(t *testing.T) {
	// 无 messages → 回退 chars/4
	got := CountChatTokens(map[string]any{"model": "x"}, 400)
	if got != EstimateTokens(400) {
		t.Fatalf("no-messages fallback = %d, want %d", got, EstimateTokens(400))
	}
	// 多模态数组 content 不崩溃且计入
	req := chatReq(map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
	}})
	if got := CountChatTokens(req, 200); got < 16 {
		t.Fatalf("multimodal tokens = %d", got)
	}
	// tools 定义计入 prompt
	withTools := chatReq(map[string]any{"role": "user", "content": "call the tool please"})
	base := CountChatTokens(withTools, 100)
	withTools["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{
		"name": "get_weather", "description": "Get weather for a location",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"location": map[string]any{"type": "string"}}},
	}}}
	if got := CountChatTokens(withTools, 100); got <= base {
		t.Fatalf("tools not counted: with=%d base=%d", got, base)
	}
}
