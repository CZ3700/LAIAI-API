package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// BUG-H1（资金红线）：chat 的 n 必须在**转发体**里被夹紧到 ≤128，否则上游按原始 n 产出全部候选，
// capture 按真实 ×n 结算 > ×128 hold，超出走 AllowNegative overage 把余额刷负。
func TestChatNClampedInForwardedBody(t *testing.T) {
	srv, _ := setupRelayServer(t)
	gotN := float64(-1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["n"].(float64); ok {
			gotN = v
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`)
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "nclamp@gmail.com", "100")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 10, "n": 500,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d %v", w.Code, out)
	}
	if gotN != 128 {
		t.Fatalf("upstream received n=%v, want 128 (forwarded body must be clamped)", gotN)
	}
}
