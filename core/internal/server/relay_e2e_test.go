package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 镜像 migrations/0002_ledger.sql + 0003_relay.sql（SQLite 方言）
const relayTestSchema = `
CREATE TABLE ledger_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, type TEXT NOT NULL,
    posted_balance NUMERIC NOT NULL DEFAULT 0, held_amount NUMERIC NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, type)
);
CREATE TABLE ledger_journal (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key TEXT NOT NULL UNIQUE, account_id INTEGER NOT NULL,
    kind TEXT NOT NULL, delta NUMERIC NOT NULL, balance_after NUMERIC NOT NULL,
    hold_id INTEGER, ref_type TEXT NOT NULL DEFAULT '', ref_id TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '', created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE ledger_holds (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key TEXT NOT NULL UNIQUE, account_id INTEGER NOT NULL,
    amount NUMERIC NOT NULL, captured_amount NUMERIC NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 0, ref_type TEXT NOT NULL DEFAULT '',
    ref_id TEXT NOT NULL DEFAULT '', expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL, provider TEXT NOT NULL, base_url TEXT NOT NULL,
    credential_enc TEXT NOT NULL, models TEXT NOT NULL,
    weight INTEGER NOT NULL DEFAULT 1, status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE model_prices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model TEXT NOT NULL UNIQUE,
    input_usd_per_1m NUMERIC NOT NULL, output_usd_per_1m NUMERIC NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

var testAdminToken = "admin-token-0123456789abcdef-0123456789"

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 100)
	}
	kr, err := crypto.NewKeyring("t1:"+base64.StdEncoding.EncodeToString(raw), "t1")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func setupRelayServer(t *testing.T) (*gin999, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, schema := range []string{testSchema, relayTestSchema} {
		if err := db.Exec(schema).Error; err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	engine := New(db, Options{
		AuthSecret: testSecret,
		AdminToken: []byte(testAdminToken),
		Keyring:    testKeyring(t),
	})
	return &gin999{engine: engine}, db
}

func (g *gin999) doAdmin(t *testing.T, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Token", testAdminToken)
	w := httptest.NewRecorder()
	g.engine.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// 注册用户 + 签发 API Key + 授信余额，返回 (apiKey, orgID)
func seedUserWithCredit(t *testing.T, srv *gin999, email, credit string) (string, float64) {
	t.Helper()
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": email, "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "longenough1"})
	token := login["token"].(string)
	orgID := login["org_id"].(float64)
	_, keyOut := srv.do(t, "POST", "/api/keys", token, map[string]any{"name": "relay-test"})
	apiKey := keyOut["key"].(string)
	if credit != "" {
		w, out := srv.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
			"org_id": orgID, "amount_usd": credit, "reason": "test grant",
			"idempotency_key": "seed-" + email,
		})
		if w.Code != 200 {
			t.Fatalf("grant = %d %v", w.Code, out)
		}
	}
	return apiKey, orgID
}

func mockUpstreamJSON(t *testing.T, promptTok, completionTok int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-upstream-secret" {
			t.Errorf("upstream got wrong credential: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"cmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
			promptTok, completionTok)
	}))
}

func setupChannelAndPrice(t *testing.T, srv *gin999, baseURL string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "mock", "provider": "openai", "base_url": baseURL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	// 价格：$2/1M in, $6/1M out
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("upsert price = %d %v", w.Code, out)
	}
}

func TestRelayBillingFlow(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500) // 真实 usage：1000 in + 500 out
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "relay@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 100,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d %v", w.Code, out)
	}
	if out["id"] != "cmpl-1" {
		t.Fatalf("upstream body not passed through: %v", out)
	}

	// 计费断言：cost = 1000/1M*2 + 500/1M*6 = 0.002 + 0.003 = 0.005
	acc := &ledger.Account{}
	if err := db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc).Error; err != nil {
		t.Fatal(err)
	}
	wantBalance := decimal.RequireFromString("9.995")
	if !acc.PostedBalance.Equal(wantBalance) {
		t.Fatalf("balance = %s, want %s", acc.PostedBalance, wantBalance)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held = %s, want 0 (hold not settled)", acc.HeldAmount)
	}
	// journal 结算总额准确（capture + 可能的 overage debit 合计 = 真实成本）
	var entries []ledger.JournalEntry
	if err := db.Where("kind IN ?", []string{ledger.KindCapture, ledger.KindDebit}).Find(&entries).Error; err != nil || len(entries) == 0 {
		t.Fatalf("no settle entries: %v", err)
	}
	settled := decimal.Zero
	for _, e := range entries {
		settled = settled.Add(e.Delta)
	}
	if !settled.Equal(decimal.RequireFromString("-0.005")) {
		t.Fatalf("settled total = %s, want -0.005", settled)
	}
}

func TestRelayInsufficientBalance(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 10, 10)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "poor@example.com", "") // 不授信

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("no-balance call = %d %v, want 402", w.Code, out)
	}
	// 不留任何冻结残渣
	var held int64
	db.Model(&ledger.Hold{}).Where("status = 0").Count(&held)
	if held != 0 {
		t.Fatalf("active holds = %d after 402", held)
	}
	_ = orgID
}

func TestRelayUpstreamErrorReleasesHold(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"upstream exploded"}}`, http.StatusInternalServerError)
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "err@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("upstream error passthrough = %d, want 500", w.Code)
	}
	// 资金完整回滚：余额不动、无活跃 hold、无 capture
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("10")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after upstream error: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
	}
	var captures int64
	db.Model(&ledger.JournalEntry{}).Where("kind = ?", ledger.KindCapture).Count(&captures)
	if captures != 0 {
		t.Fatalf("captures = %d after failed call", captures)
	}
}

func TestRelayStreamingBilling(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 断言 include_usage 被注入
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if so, ok := body["stream_options"].(map[string]any); !ok || so["include_usage"] != true {
			t.Errorf("stream_options.include_usage not injected: %v", body["stream_options"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2000,\"completion_tokens\":1000}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "stream@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "stream": true, "max_tokens": 2000,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("stream relay = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("SSE not passed through: %q", w.Body.String())
	}
	// cost = 2000/1M*2 + 1000/1M*6 = 0.004 + 0.006 = 0.01
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.99")) {
		t.Fatalf("balance after stream = %s, want 9.99", acc.PostedBalance)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held after stream = %s", acc.HeldAmount)
	}
}

func TestAdminTokenGate(t *testing.T) {
	srv, _ := setupRelayServer(t)
	// 错 token
	req := httptest.NewRequest("GET", "/api/admin/channels", nil)
	req.Header.Set("X-Admin-Token", "wrong-token")
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("wrong admin token = %d", w.Code)
	}
	// 未配置 admin token 的实例：整组 404
	db2, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	_ = db2.Exec(testSchema).Error
	engine2 := New(db2, Options{AuthSecret: testSecret})
	req2 := httptest.NewRequest("GET", "/api/admin/channels", nil)
	req2.Header.Set("X-Admin-Token", testAdminToken)
	w2 := httptest.NewRecorder()
	engine2.ServeHTTP(w2, req2)
	if w2.Code != 404 {
		t.Fatalf("disabled admin api = %d, want 404", w2.Code)
	}
}

func TestChannelCredentialNeverExposed(t *testing.T) {
	srv, _ := setupRelayServer(t)
	srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "c1", "provider": "openai", "base_url": "https://example.com/v1",
		"api_key": "sk-upstream-secret", "models": []string{"m"},
	})
	w, _ := srv.doAdmin(t, "GET", "/api/admin/channels", nil)
	if strings.Contains(w.Body.String(), "sk-upstream-secret") ||
		strings.Contains(w.Body.String(), "enc:v1:") {
		t.Fatalf("credential (or ciphertext) exposed in list: %s", w.Body.String())
	}
}

func TestGrantIdempotency(t *testing.T) {
	srv, db := setupRelayServer(t)
	_, orgID := seedUserWithCredit(t, srv, "idem@example.com", "")
	for i := 0; i < 3; i++ {
		w, out := srv.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
			"org_id": orgID, "amount_usd": "5", "reason": "r", "idempotency_key": "same-key",
		})
		if w.Code != 200 {
			t.Fatalf("grant round %d = %d %v", i, w.Code, out)
		}
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ?", uint64(orgID)).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("balance = %s after 3 replays, want 5", acc.PostedBalance)
	}
}
