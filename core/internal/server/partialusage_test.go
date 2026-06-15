package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// BUG-H2：上游只给了 prompt、completion=0（如 Anthropic 流式中途断连），且有输出内容时，
// 必须按字节估算 completion 而非以 0 结算（否则已下发输出近乎免费）。用 openai 兼容 mock
// 返回 completion_tokens=0 + 非空 content 复现该分支。
func TestPartialUsageCompletionEstimated(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// prompt_tokens>0 但 completion_tokens=0，content 非空（已下发输出）
		fmt.Fprint(w, `{"id":"x","choices":[{"message":{"role":"assistant","content":"this is a fairly long assistant reply that clearly cost output tokens"}}],"usage":{"prompt_tokens":50,"completion_tokens":0}}`)
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "partial@gmail.com", "100")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d %v", w.Code, out)
	}
	inv := waitInvocation(t, db, "org_id = ?", uint64(orgID))
	if inv.CompletionTokens == 0 {
		t.Fatalf("completion_tokens = 0; expected byte-estimated >0 (output must not bill free when upstream omits completion)")
	}
}
