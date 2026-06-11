package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

func setupGeminiChannel(t *testing.T, srv *gin999, baseURL string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "gemini-mock", "provider": "gemini", "base_url": baseURL,
		"api_key": "g-secret", "models": []string{"gemini-test"},
	})
	if w.Code != 201 {
		t.Fatalf("create gemini channel = %d %v", w.Code, out)
	}
	// $1/1M in, $4/1M out
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "gemini-test", "input_usd_per_1m": "1", "output_usd_per_1m": "4",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
}

// 真实 Gemini generateContent 响应形态
const geminiJSONFixture = `{
  "candidates": [{
    "content": {"parts": [{"text": "Hello from Gemini!"}], "role": "model"},
    "finishReason": "STOP", "index": 0
  }],
  "usageMetadata": {"promptTokenCount": 3000, "candidatesTokenCount": 1500, "totalTokenCount": 4500},
  "modelVersion": "gemini-test-001"
}`

func TestGeminiNonStreamConversion(t *testing.T) {
	srv, db := setupRelayServer(t)
	var gotPath, gotKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, geminiJSONFixture)
	}))
	defer up.Close()
	setupGeminiChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "gem@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "gemini-test", "max_tokens": 100,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("gemini relay = %d %v", w.Code, out)
	}
	if !strings.Contains(gotPath, "/models/gemini-test:generateContent") || gotKey != "g-secret" {
		t.Fatalf("upstream call: path=%s key=%s", gotPath, gotKey)
	}
	choice := out["choices"].([]any)[0].(map[string]any)
	if choice["message"].(map[string]any)["content"] != "Hello from Gemini!" {
		t.Fatalf("content: %v", choice)
	}
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	u := out["usage"].(map[string]any)
	if u["prompt_tokens"] != float64(3000) || u["completion_tokens"] != float64(1500) {
		t.Fatalf("usage = %v", u)
	}
	// 计费：3000/1M*1 + 1500/1M*4 = 0.003 + 0.006 = 0.009
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.991")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after gemini call: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
	}
}

// 真实 Gemini 流式形态：SSE data 行、usageMetadata 随 chunk 累计、无 [DONE]
var geminiSSEFixture = strings.Join([]string{
	`data: {"candidates":[{"content":{"parts":[{"text":"Hel"}],"role":"model"},"index":0}],"usageMetadata":{"promptTokenCount":2000,"candidatesTokenCount":1}}`,
	``,
	`data: {"candidates":[{"content":{"parts":[{"text":"lo!"}],"role":"model"},"index":0}],"usageMetadata":{"promptTokenCount":2000,"candidatesTokenCount":5}}`,
	``,
	`data: {"candidates":[{"content":{"parts":[],"role":"model"},"finishReason":"MAX_TOKENS","index":0}],"usageMetadata":{"promptTokenCount":2000,"candidatesTokenCount":1000}}`,
	``,
}, "\n")

func TestGeminiStreamingConversion(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.String(), ":streamGenerateContent") || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("stream url wrong: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, geminiSSEFixture)
	}))
	defer up.Close()
	setupGeminiChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "gem-stream@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "gemini-test", "stream": true, "max_tokens": 1000,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("gemini stream = %d %s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	for _, want := range []string{
		`"content":"Hel"`, `"content":"lo!"`,
		`"finish_reason":"length"`, // MAX_TOKENS → length
		"data: [DONE]",             // Gemini 无 [DONE]，必须由我们合成
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q in: %s", want, out)
		}
	}
	if strings.Contains(out, "usageMetadata") || strings.Contains(out, "candidates") {
		t.Fatalf("gemini raw chunk shape leaked to client: %s", out)
	}
	// 计费取最后一次累计 usage：2000/1M*1 + 1000/1M*4 = 0.002 + 0.004 = 0.006
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.994")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after gemini stream: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
	}
}

// 厂商档案默认端点：创建渠道可省略 base_url
func TestChannelDefaultBaseURL(t *testing.T) {
	srv, _ := setupRelayServer(t)
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ds", "provider": "deepseek",
		"api_key": "sk-x", "models": []string{"deepseek-chat"},
	})
	if w.Code != 201 {
		t.Fatalf("create without base_url = %d %v", w.Code, out)
	}
	w, list := srv.doAdmin(t, "GET", "/api/admin/channels", nil)
	if w.Code != 200 || !strings.Contains(fmt.Sprint(list), "api.deepseek.com") {
		t.Fatalf("default base url not applied: %v", list)
	}
	// 无档案且无 base_url → 400
	w, _ = srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "unknown", "provider": "some-startup",
		"api_key": "sk-x", "models": []string{"m"},
	})
	if w.Code != 400 {
		t.Fatalf("unknown provider without base_url = %d, want 400", w.Code)
	}
}
