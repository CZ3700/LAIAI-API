package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// 真实 OpenAI embeddings 响应形态
const embeddingsFixture = `{
  "object": "list",
  "data": [{"object": "embedding", "index": 0, "embedding": [0.001, -0.002, 0.003]}],
  "model": "embed-test",
  "usage": {"prompt_tokens": 8000, "total_tokens": 8000}
}`

func setupEmbeddingsChannel(t *testing.T, srv *gin999, baseURL string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "embed-mock", "provider": "openai", "base_url": baseURL,
		"api_key": "sk-upstream-secret", "models": []string{"embed-test"},
	})
	if w.Code != 201 {
		t.Fatalf("channel = %d %v", w.Code, out)
	}
	// $0.10/1M input（embeddings 无输出价）
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "embed-test", "input_usd_per_1m": "0.10", "output_usd_per_1m": "0",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
}

func TestEmbeddingsBillingFlow(t *testing.T) {
	srv, db := setupRelayServer(t)
	var gotPath, gotAuth string
	var gotBody map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, embeddingsFixture)
	}))
	defer up.Close()
	setupEmbeddingsChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "embed@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/embeddings", apiKey, map[string]any{
		"model": "embed-test",
		"input": []string{"the quick brown fox", "jumps over the lazy dog"},
	})
	if w.Code != 200 {
		t.Fatalf("embeddings = %d %v", w.Code, out)
	}
	if gotPath != "/embeddings" || gotAuth != "Bearer sk-upstream-secret" {
		t.Fatalf("upstream call: path=%s auth=%s", gotPath, gotAuth)
	}
	// 透传：input 原样到达上游、响应原样返回客户端
	if in, _ := gotBody["input"].([]any); len(in) != 2 {
		t.Fatalf("input not passed through: %v", gotBody["input"])
	}
	if out["object"] != "list" || len(out["data"].([]any)) != 1 {
		t.Fatalf("response not passed through: %v", out)
	}

	// 计费：8000/1M × $0.10 = $0.0008
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.9992")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after embeddings: posted=%s held=%s, want 9.9992/0", acc.PostedBalance, acc.HeldAmount)
	}

	// 调用记录：模态正确入账
	reqID := w.Header().Get("X-Request-Id")
	rec := waitInvocation(t, db, "request_id = ?", reqID)
	if rec.Status != invocation.StatusSuccess || rec.PromptTokens != 8000 ||
		!rec.CostUSD.Equal(decimal.RequireFromString("0.0008")) {
		t.Fatalf("invocation: status=%s in=%d cost=%s", rec.Status, rec.PromptTokens, rec.CostUSD)
	}
}

func TestEmbeddingsInsufficientBalance(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, embeddingsFixture)
	}))
	defer up.Close()
	setupEmbeddingsChannel(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "embed-poor@example.com", "") // 不授信

	w, _ := srv.do(t, "POST", "/v1/embeddings", apiKey, map[string]any{
		"model": "embed-test", "input": "hello",
	})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("no-balance embeddings = %d, want 402", w.Code)
	}
	var held int64
	db.Model(&ledger.Hold{}).Where("status = 0").Count(&held)
	if held != 0 {
		t.Fatalf("active holds = %d after 402", held)
	}
}

func TestEmbeddingsUnsupportedProvider(t *testing.T) {
	srv, _ := setupRelayServer(t)
	// anthropic 渠道服务该模型 → 明确 400 而不是把请求打到不存在的端点
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "claude-ch", "provider": "anthropic", "base_url": "https://api.anthropic.com/v1",
		"api_key": "sk-ant", "models": []string{"embed-on-claude"},
	})
	if w.Code != 201 {
		t.Fatalf("channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "embed-on-claude", "input_usd_per_1m": "1", "output_usd_per_1m": "0",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d", w.Code)
	}
	apiKey, orgID := seedUserWithCredit(t, srv, "embed-unsup@example.com", "10")
	w, resp := srv.do(t, "POST", "/v1/embeddings", apiKey, map[string]any{
		"model": "embed-on-claude", "input": "hello",
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(resp), "does not support embeddings") {
		t.Fatalf("unsupported provider = %d %v, want explicit 400", w.Code, resp)
	}
	_ = orgID
}

func TestEmbeddingsAzurePath(t *testing.T) {
	srv, _ := setupRelayServer(t)
	var gotURL string
	var gotAPIKeyHeader string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotAPIKeyHeader = r.Header.Get("api-key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, embeddingsFixture)
	}))
	defer up.Close()
	w, _ := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "azure-embed", "provider": "azure", "base_url": up.URL,
		"api_key": "az-key", "models": []string{"my-embed-deployment"},
	})
	if w.Code != 201 {
		t.Fatalf("channel = %d", w.Code)
	}
	w, _ = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "my-embed-deployment", "input_usd_per_1m": "0.1", "output_usd_per_1m": "0",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d", w.Code)
	}
	apiKey, _ := seedUserWithCredit(t, srv, "embed-az@example.com", "10")
	w, _ = srv.do(t, "POST", "/v1/embeddings", apiKey, map[string]any{
		"model": "my-embed-deployment", "input": "hello",
	})
	if w.Code != 200 {
		t.Fatalf("azure embeddings = %d", w.Code)
	}
	if !strings.Contains(gotURL, "/openai/deployments/my-embed-deployment/embeddings") ||
		!strings.Contains(gotURL, "api-version=") || gotAPIKeyHeader != "az-key" {
		t.Fatalf("azure url/auth: %s api-key=%s", gotURL, gotAPIKeyHeader)
	}
}
