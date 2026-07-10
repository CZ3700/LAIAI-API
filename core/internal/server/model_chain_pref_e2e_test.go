package server

// 模型级 fallback 链（models:[a,b,c]）+ 请求级 provider preference e2e（设计 §4.4，2026-07-11）。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// countingUpstream：openai 形态 mock，记命中次数。
type countingUpstream struct {
	srv  *httptest.Server
	hits atomic.Int64
}

func newCountingUpstream(t *testing.T, model string) *countingUpstream {
	t.Helper()
	u := &countingUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"cmpl-1","model":%q,"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, model)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func addChannel(t *testing.T, srv *gin999, name, provider, baseURL string, models []string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": name, "provider": provider, "base_url": baseURL,
		"api_key": "sk-upstream-secret", "models": models,
	})
	if w.Code != 201 {
		t.Fatalf("create channel %s = %d %v", name, w.Code, out)
	}
}

func addPrice(t *testing.T, srv *gin999, model string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "10", "output_usd_per_1m": "30",
	})
	if w.Code != 200 {
		t.Fatalf("price %s = %d %v", model, w.Code, out)
	}
}

// 主模型无渠道 → 自动降级 models 里的下一个；资金按成功模型结算、hold 无泄漏。
func TestModelFallbackChainDegrades(t *testing.T) {
	srv, db := setupRelayServer(t)
	backup := newCountingUpstream(t, "model-b")
	addChannel(t, srv, "ch-b", "openai", backup.srv.URL, []string{"model-b"})
	addPrice(t, srv, "model-a") // model-a 有价但**无渠道**
	addPrice(t, srv, "model-b")
	apiKey, orgID := seedUserWithCredit(t, srv, "chain@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model":  "model-a",
		"models": []string{"model-b"},
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chain = %d %v", w.Code, out)
	}
	if out["model"] != "model-b" {
		t.Fatalf("response model = %v, want model-b（降级结果对客户端可见，不冒充主模型）", out["model"])
	}
	if backup.hits.Load() != 1 {
		t.Fatalf("backup hits = %d, want 1", backup.hits.Load())
	}
	inv := waitInvocation(t, db, "status = ?", "success")
	if inv.Model != "model-b" {
		t.Fatalf("invocation model = %q, want model-b", inv.Model)
	}
	// 降级成功记录：provider 反映实际所用渠道（model-b 的 openai 渠道），不残留主模型值
	if inv.Provider != "openai" {
		t.Fatalf("inv provider = %q, want openai（实际所用渠道，不残留主模型）", inv.Provider)
	}
	// hold 无泄漏：模型 A 的 hold（若曾建）已释放，成功结算只留 model-b 的 capture
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held after chain settle = %s, want 0", acc.HeldAmount)
	}
	if !acc.PostedBalance.LessThan(decimal.RequireFromString("10")) {
		t.Fatalf("balance not charged: %s", acc.PostedBalance)
	}
}

// 客户端 400 类（上游判定请求不合法）绝不换模型：即便链里还有备选。
func TestModelChainNoFallbackOnClientError(t *testing.T) {
	srv, _ := setupRelayServer(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad request","type":"invalid_request_error"}}`)
	}))
	defer bad.Close()
	backup := newCountingUpstream(t, "model-b")
	addChannel(t, srv, "ch-a", "openai", bad.URL, []string{"model-a"})
	addChannel(t, srv, "ch-b", "openai", backup.srv.URL, []string{"model-b"})
	addPrice(t, srv, "model-a")
	addPrice(t, srv, "model-b")
	apiKey, _ := seedUserWithCredit(t, srv, "chain400@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model":  "model-a",
		"models": []string{"model-b"},
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("client-error chain = %d, want 400 透传", w.Code)
	}
	if backup.hits.Load() != 0 {
		t.Fatalf("backup hits = %d, want 0（400 绝不换模型）", backup.hits.Load())
	}
}

// provider preference：ignore 的厂商绝不被选中。
func TestProviderPrefIgnore(t *testing.T) {
	srv, _ := setupRelayServer(t)
	openaiUp := newCountingUpstream(t, "test-model")
	groqUp := newCountingUpstream(t, "test-model")
	addChannel(t, srv, "ch-openai", "openai", openaiUp.srv.URL, []string{"test-model"})
	addChannel(t, srv, "ch-groq", "groq", groqUp.srv.URL, []string{"test-model"})
	addPrice(t, srv, "test-model")
	apiKey, _ := seedUserWithCredit(t, srv, "pref@example.com", "10")

	for i := 0; i < 6; i++ {
		w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
			"model":    "test-model",
			"provider": map[string]any{"ignore": []string{"openai"}},
			"messages": []map[string]any{{"role": "user", "content": "hi"}},
		})
		if w.Code != 200 {
			t.Fatalf("req %d = %d %v", i, w.Code, out)
		}
	}
	if openaiUp.hits.Load() != 0 {
		t.Fatalf("ignored provider hits = %d, want 0", openaiUp.hits.Load())
	}
	if groqUp.hits.Load() != 6 {
		t.Fatalf("groq hits = %d, want 6", groqUp.hits.Load())
	}
}

// provider preference：only 过滤后无渠道 = 与"模型无渠道"同一失败面（不引入新错误形态）。
func TestProviderPrefOnlyNoMatch(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := newCountingUpstream(t, "test-model")
	addChannel(t, srv, "ch-openai", "openai", up.srv.URL, []string{"test-model"})
	addPrice(t, srv, "test-model")
	apiKey, _ := seedUserWithCredit(t, srv, "prefonly@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model":    "test-model",
		"provider": map[string]any{"only": []string{"anthropic"}},
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("only-no-match = %d, want 503（无渠道同失败面）", w.Code)
	}
	if up.hits.Load() != 0 {
		t.Fatalf("filtered provider hits = %d, want 0", up.hits.Load())
	}
}
