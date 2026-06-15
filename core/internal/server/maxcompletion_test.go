package server

import (
	"net/http"
	"testing"
)

// P0-2：传巨大 max_completion_tokens 时，hold 按输出上限冻结，余额不足应在调用上游前 402 拒绝，
// 而不是放行后借 overage 把余额刷成负数。
func TestMaxCompletionTokensHeldUpfront(t *testing.T) {
	srv, _ := setupRelayServer(t)
	setupChannelAndPrice(t, srv, "http://unused.invalid") // 价格 $2/1M in, $6/1M out
	apiKey, _ := seedUserWithCredit(t, srv, "mct@gmail.com", "1")  // 余额 $1

	// 默认 4096 输出 hold ≈ $0.025 < $1 本会放行；但 max_completion_tokens=1000 万
	// 钳到 1M 上限 → 1M×$6/1M ≈ $6.29 hold ＞ $1 → 必须 402（被输出冻结上限拦在上游调用之前）。
	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model":                 "test-model",
		"max_completion_tokens": 10000000,
		"messages":              []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("huge max_completion_tokens with $1 balance = %d %v, want 402 (held by output ceiling, P0-2)", w.Code, out)
	}

	// 对照：同样 $1 余额、不传上限（默认 4096）应放行到上游（这里上游不可达 → 非 402，证明不是被余额挡的）。
	w2, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model":    "test-model",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w2.Code == http.StatusPaymentRequired {
		t.Fatalf("default-cap request wrongly 402'd at $1 balance (hold should be ~$0.025)")
	}
}

// P0-2 同类：n（多候选）必须按 n 倍冻结——n 大 + 适中 max_completion_tokens 应被余额闸拦下。
func TestMultiChoiceNHeldByMultiplier(t *testing.T) {
	srv, _ := setupRelayServer(t)
	setupChannelAndPrice(t, srv, "http://unused.invalid") // $6/1M out
	apiKey, _ := seedUserWithCredit(t, srv, "nmul@gmail.com", "1") // $1

	// 单候选：100k 输出 × $6/1M = $0.6 < $1 → 放行（非 402）
	if w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_completion_tokens": 100000,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}); w.Code == http.StatusPaymentRequired {
		t.Fatalf("single-choice 100k out @ $1 wrongly 402 (hold ~$0.6)")
	}
	// n=5 同样 100k：5 × $0.6 = $3 hold ＞ $1 → 402（n 倍冻结生效）
	if w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_completion_tokens": 100000, "n": 5,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}); w.Code != http.StatusPaymentRequired {
		t.Fatalf("n=5 ×100k out @ $1 = %d %v, want 402 (hold must be n×, P0-2)", w.Code, out)
	}
}
