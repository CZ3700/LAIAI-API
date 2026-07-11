package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/payment"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/totp"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// relaypkgTimeout 设置流式 per-chunk 超时并返回旧值（测试用）
func relaypkgTimeout(d time.Duration) time.Duration {
	old := relay.StreamChunkTimeout
	relay.StreamChunkTimeout = d
	return old
}

// 镜像 migrations/0002_ledger.sql + 0003_relay.sql（SQLite 方言）
const relayTestSchema = `
CREATE TABLE ledger_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, type TEXT NOT NULL, shard INTEGER NOT NULL DEFAULT 0,
    posted_balance NUMERIC NOT NULL DEFAULT 0, held_amount NUMERIC NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, type, shard)
);
CREATE TABLE ledger_account_shards (
    org_id INTEGER NOT NULL, type TEXT NOT NULL, shard_count INTEGER NOT NULL DEFAULT 1,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (org_id, type)
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
    weight INTEGER NOT NULL DEFAULT 1, priority INTEGER NOT NULL DEFAULT 0,
    max_concurrent_tasks INTEGER NOT NULL DEFAULT 3, strict_response INTEGER NOT NULL DEFAULT 1, model_mapping TEXT NOT NULL DEFAULT '',
    proxy TEXT NOT NULL DEFAULT '',
    param_override TEXT NOT NULL DEFAULT '', header_override TEXT NOT NULL DEFAULT '',
    credential_mode TEXT NOT NULL DEFAULT 'random',
    converter TEXT NOT NULL DEFAULT '', upstream_path TEXT NOT NULL DEFAULT '',
    model_sync_enabled INTEGER NOT NULL DEFAULT 0, last_sync_at DATETIME,
    detected_models TEXT NOT NULL DEFAULT '', ignored_models TEXT NOT NULL DEFAULT '',
    owner_org_id INTEGER NOT NULL DEFAULT 0,
    suggest_disable_reason TEXT NOT NULL DEFAULT '', suggest_disabled_at DATETIME,
    status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE channel_credentials (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id INTEGER NOT NULL, credential_enc TEXT NOT NULL,
    fingerprint TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 1,
    disabled_reason TEXT NOT NULL DEFAULT '', disabled_at DATETIME, last_used_at DATETIME,
    mode_weight INTEGER NOT NULL DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id TEXT NOT NULL UNIQUE,
    org_id INTEGER NOT NULL, user_id INTEGER, api_key_id INTEGER NOT NULL,
    model TEXT NOT NULL, channel_id INTEGER NOT NULL, provider TEXT NOT NULL,
    status TEXT NOT NULL, fail_reason TEXT NOT NULL DEFAULT '',
    upstream_task_id TEXT NOT NULL DEFAULT '', result_url TEXT NOT NULL DEFAULT '',
    result_ref TEXT NOT NULL DEFAULT '',
    hold_id INTEGER NOT NULL, credential_id INTEGER NOT NULL DEFAULT 0,
    est_tokens INTEGER NOT NULL DEFAULT 0,
    actual_tokens INTEGER NOT NULL DEFAULT 0, cost_usd NUMERIC NOT NULL DEFAULT 0,
    price_out_usd_per_1m NUMERIC NOT NULL,
    hold_amount_usd NUMERIC NOT NULL DEFAULT 0, group_ratio NUMERIC NOT NULL DEFAULT 1,
    submitted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME, finished_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE topup_orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    order_no TEXT NOT NULL UNIQUE, org_id INTEGER NOT NULL, user_id INTEGER,
    provider TEXT NOT NULL, amount_usd NUMERIC NOT NULL, pay_amount NUMERIC NOT NULL,
    pay_currency TEXT NOT NULL DEFAULT 'USD', status INTEGER NOT NULL DEFAULT 0,
    trade_no TEXT NOT NULL DEFAULT '', provider_ref TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL, paid_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE payment_channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL DEFAULT '',
    protocol TEXT NOT NULL, config_enc TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1, priority INTEGER NOT NULL DEFAULT 0,
    pay_methods TEXT NOT NULL DEFAULT '', currency TEXT NOT NULL DEFAULT 'USD',
    last_test_at INTEGER NOT NULL DEFAULT 0, last_test_ok INTEGER NOT NULL DEFAULT 0,
    last_test_msg TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE model_prices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model TEXT NOT NULL UNIQUE,
    input_usd_per_1m NUMERIC NOT NULL, output_usd_per_1m NUMERIC NOT NULL,
    image_usd NUMERIC NOT NULL DEFAULT 0,
    cached_input_usd_per_1m NUMERIC NOT NULL DEFAULT 0,
    per_call_usd NUMERIC NOT NULL DEFAULT 0,
    tts_usd_per_1k_chars NUMERIC NOT NULL DEFAULT 0,
    transcription_usd_per_call NUMERIC NOT NULL DEFAULT 0,
    audio_input_usd_per_1m NUMERIC NOT NULL DEFAULT 0,
    audio_output_usd_per_1m NUMERIC NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE model_group_ratios (
    group_name TEXT NOT NULL, model TEXT NOT NULL, ratio NUMERIC NOT NULL DEFAULT 1,
    PRIMARY KEY (group_name, model)
);
CREATE TABLE model_meta (
    model TEXT PRIMARY KEY, vendor TEXT NOT NULL DEFAULT '', category TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
    context_length INTEGER NOT NULL DEFAULT 0, icon TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE invocations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, user_id INTEGER, api_key_id INTEGER NOT NULL,
    request_id TEXT NOT NULL UNIQUE, model TEXT NOT NULL,
    modality TEXT NOT NULL DEFAULT '',
    channel_id INTEGER NOT NULL DEFAULT 0, provider TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL, status_code INTEGER NOT NULL DEFAULT 0,
    is_stream INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd NUMERIC NOT NULL DEFAULT 0, usage_source TEXT NOT NULL DEFAULT '',
    frt_ms INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
    upstream_request_id TEXT NOT NULL DEFAULT '', error_brief TEXT NOT NULL DEFAULT '',
    payload_object_key TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT '', user_agent TEXT NOT NULL DEFAULT '', referer TEXT NOT NULL DEFAULT '',
    finish_reason TEXT NOT NULL DEFAULT '', protocol TEXT NOT NULL DEFAULT '',
    cached_tokens INTEGER NOT NULL DEFAULT 0, reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    upstream_error_code TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
	// 单连接：:memory: 下每条连接是独立库，后台 goroutine（禁用/release）必须与主连接共享同一库
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	for _, schema := range []string{testSchema, relayTestSchema} {
		if err := db.Exec(schema).Error; err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	// 测试基线：默认关邮箱验证（多数 relay/e2e 测试直接注册无验证码）；验证相关测试各自 OR REPLACE 开。
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('email_verification_enabled', 'false')")
	rec := invocation.NewRecorder(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Cleanup(rec.Close)
	engine, srv := NewWithServer(db, Options{
		AuthSecret:    testSecret,
		AdminToken:    []byte(testAdminToken),
		Keyring:       testKeyring(t),
		Recorder:      rec,
		AuthRateBurst: 100000, // 测试不限流（功能流密集发起 auth 调用）
	})
	return &gin999{engine: engine, srv: srv}, db
}

// waitInvocation 轮询等待异步落库的调用记录（flush 间隔 250ms）
func waitInvocation(t *testing.T, db *gorm.DB, where string, args ...any) *invocation.Invocation {
	t.Helper()
	for i := 0; i < 100; i++ {
		inv := &invocation.Invocation{}
		if err := db.Where(where, args...).First(inv).Error; err == nil {
			return inv
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("invocation not recorded: %s %v", where, args)
	return nil
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

	// —— Invocation Store 断言（异步落库，元数据与 journal 同口径）——
	reqID := w.Header().Get("X-Request-Id")
	if reqID == "" {
		t.Fatal("X-Request-Id header missing")
	}
	rec := waitInvocation(t, db, "request_id = ?", reqID)
	if rec.Status != invocation.StatusSuccess || rec.StatusCode != 200 {
		t.Fatalf("invocation status = %s/%d", rec.Status, rec.StatusCode)
	}
	if rec.PromptTokens != 1000 || rec.CompletionTokens != 500 {
		t.Fatalf("invocation tokens = %d/%d", rec.PromptTokens, rec.CompletionTokens)
	}
	if !rec.CostUSD.Equal(decimal.RequireFromString("0.005")) {
		t.Fatalf("invocation cost = %s, want 0.005 (journal 口径)", rec.CostUSD)
	}
	if rec.UsageSource != invocation.UsageSourceUpstream || rec.Attempts != 1 ||
		rec.ChannelID == 0 || rec.Provider != "openai" || rec.OrgID != uint64(orgID) {
		t.Fatalf("invocation meta: source=%s attempts=%d channel=%d provider=%s org=%d",
			rec.UsageSource, rec.Attempts, rec.ChannelID, rec.Provider, rec.OrgID)
	}
}

func TestUsageAPIOrgIsolation(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 10, 10)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKeyA, _ := seedUserWithCredit(t, srv, "usage-a@example.com", "10")

	// B 只注册登录，不调用
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "usage-b@example.com", "password": "longenough1"})
	_, loginB := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "usage-b@example.com", "password": "longenough1"})
	tokenB := loginB["token"].(string)
	_, loginA := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "usage-a@example.com", "password": "longenough1"})
	tokenA := loginA["token"].(string)

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKeyA, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("call = %d", w.Code)
	}
	waitInvocation(t, db, "status = ?", invocation.StatusSuccess)

	// A 能看到自己的记录
	w, outA := srv.do(t, "GET", "/api/usage", tokenA, nil)
	if w.Code != 200 || outA["total"].(float64) < 1 {
		t.Fatalf("org A usage = %d %v", w.Code, outA)
	}
	items := outA["items"].([]any)
	first := items[0].(map[string]any)
	if first["model"] != "test-model" || first["request_id"] == "" {
		t.Fatalf("usage item shape: %v", first)
	}
	if _, leaked := first["upstream_request_id"]; leaked {
		t.Fatal("upstream_request_id leaked to user API (internal field)")
	}
	// B 看不到任何记录（org 隔离红线）
	w, outB := srv.do(t, "GET", "/api/usage", tokenB, nil)
	if w.Code != 200 || outB["total"].(float64) != 0 {
		t.Fatalf("org B usage = %v, want empty", outB)
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

// 双渠道工具：ch1 优先级高（先被选中），ch2 兜底
func setupTwoChannels(t *testing.T, srv *gin999, url1, url2 string) {
	t.Helper()
	for i, cfg := range []struct {
		name, url string
		priority  int
	}{{"primary", url1, 10}, {"fallback", url2, 0}} {
		w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
			"name": cfg.name, "provider": "openai", "base_url": cfg.url,
			"api_key": "sk-upstream-secret", "models": []string{"test-model"}, "priority": cfg.priority,
		})
		if w.Code != 201 {
			t.Fatalf("create channel %d = %d %v", i, w.Code, out)
		}
	}
	w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("upsert price = %d %v", w.Code, out)
	}
}

// 重试核心：主渠道 500 → 自动切兜底渠道成功，计费精确，资金无残渣
func TestRelayFailoverRetry(t *testing.T) {
	srv, db := setupRelayServer(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"internal"}}`, http.StatusInternalServerError)
	}))
	defer bad.Close()
	good := mockUpstreamJSON(t, 1000, 500)
	defer good.Close()
	setupTwoChannels(t, srv, bad.URL, good.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "failover@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 100,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 || out["id"] != "cmpl-1" {
		t.Fatalf("failover = %d %v, want success from fallback", w.Code, out)
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.995")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after failover: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
	}
	// 500 不应禁用主渠道（仅重试，知识库 §2.1）
	var s int8
	db.Raw("SELECT status FROM channels WHERE name='primary'").Scan(&s)
	if s != 1 {
		t.Fatalf("primary channel status = %d after 500, want still enabled", s)
	}
}

// 不可重试错误（400）必须直接透传，不烧第二个渠道
func TestRelayNonRetryablePassthrough(t *testing.T) {
	srv, db := setupRelayServer(t)
	var fallbackHits int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"invalid_request_error","message":"bad request"}}`, http.StatusBadRequest)
	}))
	defer bad.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Write([]byte(`{}`))
	}))
	defer fallback.Close()
	setupTwoChannels(t, srv, bad.URL, fallback.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "noretry@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 400 {
		t.Fatalf("400 passthrough = %d", w.Code)
	}
	if fallbackHits != 0 {
		t.Fatalf("fallback hit %d times on non-retryable error", fallbackHits)
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("10")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("funds after 400: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
	}
}

// 流式 per-chunk 超时：上游 stall 不会挂死 relay，资金按已收数据结算
func TestRelayStreamStallTimeout(t *testing.T) {
	old := relaypkgTimeout(50 * time.Millisecond)
	defer relaypkgTimeout(old)

	srv, db := setupRelayServer(t)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		f.Flush()
		<-release // stall：不再发任何数据
	}))
	defer up.Close()
	defer close(release)
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "stall@example.com", "10")

	done := make(chan struct{})
	go func() {
		defer close(done)
		w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
			"model": "test-model", "stream": true, "max_tokens": 50,
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
		if !strings.Contains(w.Body.String(), "partial") {
			t.Errorf("partial data not streamed before stall: %q", w.Body.String())
		}
	}()
	select {
	case <-done: // per-chunk 超时生效，handler 返回
	case <-time.After(5 * time.Second):
		t.Fatal("relay hung on stalled upstream — per-chunk timeout not working")
	}
	// 资金已结算（估算兜底），无活跃 hold
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held = %s after stall settle", acc.HeldAmount)
	}
}

func TestRelayChannelAutoDisable(t *testing.T) {
	srv, db := setupRelayServer(t)
	// 上游返回 401 invalid_api_key —— 应触发渠道自动禁用（知识库 §2.2）
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":"invalid_api_key","message":"bad key"}}`)
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	// 渠道自动禁用默认关闭（用户要求改手动）；本测试显式开启以验证开启后的行为。
	db.Exec("INSERT OR REPLACE INTO ext_settings (name, value) VALUES ('channel_auto_disable','true')")
	apiKey, orgID := seedUserWithCredit(t, srv, "disable@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("upstream 401 passthrough = %d, want 401", w.Code)
	}
	// 异步禁用，轮询断言渠道 status→2（不 sleep 固定值）
	disabled := false
	for i := 0; i < 100; i++ {
		var s int8
		db.Raw("SELECT status FROM channels LIMIT 1").Scan(&s)
		if s == 2 {
			disabled = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !disabled {
		t.Fatal("channel was not auto-disabled after invalid_api_key")
	}
	// 资金未受损：401 已 release，余额完整
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("10")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("funds after disable: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
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
	// 未配置 admin token 且无角色用户：无任何有效后台身份 → 401 拒绝（RBAC 0020 后，
	// 后台也可由 role 用户进入，故不再用 404 假装端点不存在；统一 401「需要后台权限」）。
	db2, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	_ = db2.Exec(testSchema).Error
	engine2 := New(db2, Options{AuthSecret: testSecret})
	req2 := httptest.NewRequest("GET", "/api/admin/channels", nil)
	req2.Header.Set("X-Admin-Token", testAdminToken)
	w2 := httptest.NewRecorder()
	engine2.ServeHTTP(w2, req2)
	if w2.Code != 401 {
		t.Fatalf("no admin identity = %d, want 401", w2.Code)
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

// 上游响应校验（部署冒烟暴露）：上游 200 但返回 HTML（中转站首页）→ 判上游异常，不计费。
func TestRelayRejectsNonLLMResponse(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><html><body>relay station homepage</body></html>")
	}))
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "htmlresp@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("HTML response = %d, want 502", w.Code)
	}
	// 资金完整退还，不被误计费
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("10")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after HTML reject: posted=%s held=%s, want 10/0", acc.PostedBalance, acc.HeldAmount)
	}
	// 调用记录为 upstream_error，不是 success
	rec := waitInvocation(t, db, "org_id = ?", uint64(orgID))
	if rec.Status != invocation.StatusUpstreamError {
		t.Fatalf("invocation status = %s, want upstream_error", rec.Status)
	}
}

// 编辑渠道：改 base_url + 关 strict_response。
func TestAdminUpdateChannel(t *testing.T) {
	srv, db := setupRelayServer(t)
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "edit-me", "provider": "openai", "base_url": "https://old.example.com",
		"api_key": "sk-old", "models": []string{"m1"},
	})
	if w.Code != 201 {
		t.Fatalf("create = %d %v", w.Code, out)
	}
	id := out["id"]

	// 改 base_url、模型、关 strict_response；不传 api_key（保留原凭证）
	w, _ = srv.doAdmin(t, "PUT", fmt.Sprintf("/api/admin/channels/%v", id), map[string]any{
		"base_url": "https://new.example.com/v1", "models": []string{"m1", "m2"}, "strict_response": false,
	})
	if w.Code != 200 {
		t.Fatalf("update = %d", w.Code)
	}
	var ch struct {
		BaseURL        string
		Models         string
		StrictResponse int8
		CredentialEnc  string
	}
	db.Raw("SELECT base_url, models, strict_response, credential_enc FROM channels WHERE id = ?", id).Scan(&ch)
	if ch.BaseURL != "https://new.example.com/v1" || ch.Models != "m1,m2" || ch.StrictResponse != 0 {
		t.Fatalf("update not applied: %+v", ch)
	}
	if ch.CredentialEnc == "" {
		t.Fatal("credential wiped when api_key omitted")
	}
	// 不存在的 id → 404
	w, _ = srv.doAdmin(t, "PUT", "/api/admin/channels/9999", map[string]any{"weight": 5})
	if w.Code != 404 {
		t.Fatalf("update missing = %d, want 404", w.Code)
	}
}

// 模型映射（0009）：上游收到映射后的名字，计费/记录用对外名。
func TestRelayModelMapping(t *testing.T) {
	srv, db := setupRelayServer(t)
	var upstreamSawModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		upstreamSawModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	}))
	defer up.Close()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "mapped", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-x", "models": []string{"public-name"},
		"model_mapping": map[string]string{"public-name": "internal-deploy-7"},
	})
	if w.Code != 201 {
		t.Fatalf("create = %d %v", w.Code, out)
	}
	srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "public-name", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	apiKey, orgID := seedUserWithCredit(t, srv, "mapping@example.com", "10")

	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "public-name", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d %s", w.Code, w.Body.String())
	}
	if upstreamSawModel != "internal-deploy-7" {
		t.Fatalf("upstream saw model %q, want internal-deploy-7", upstreamSawModel)
	}
	// 调用记录用对外名
	rec := waitInvocation(t, db, "org_id = ?", uint64(orgID))
	if rec.Model != "public-name" {
		t.Fatalf("invocation model = %q, want public-name", rec.Model)
	}
}

// 渠道一键测试：健康上游 ok=true 带延迟；HTML 上游 ok=false 给出 base_url 提示。
func TestAdminTestChannel(t *testing.T) {
	srv, _ := setupRelayServer(t)
	good := mockUpstreamJSON(t, 3, 2)
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>home</html>")
	}))
	defer bad.Close()

	_, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "good", "provider": "openai", "base_url": good.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
	})
	goodID := out["id"]
	_, out = srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "bad", "provider": "openai", "base_url": bad.URL,
		"api_key": "sk-x", "models": []string{"test-model"},
	})
	badID := out["id"]

	w, res := srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/channels/%v/test", goodID), nil)
	if w.Code != 200 || res["ok"] != true {
		t.Fatalf("good channel test = %d %v", w.Code, res)
	}
	w, res = srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/channels/%v/test", badID), nil)
	if w.Code != 200 || res["ok"] != false {
		t.Fatalf("bad channel test = %d %v", w.Code, res)
	}
	if msg, _ := res["message"].(string); !strings.Contains(msg, "base_url") {
		t.Fatalf("bad test message %q should hint base_url", res["message"])
	}
}

// 充值对账（M4 金融级双保险）：pending 订单 callback 漏发，reconcile 主动 Query 补回入账。
func TestReconcileTopups(t *testing.T) {
	srv, db := setupRelayServer(t)
	mock := &mockPayEngine{}
	engineForTest = func(p string) payment.Engine {
		if p == "stripe" {
			return mock
		}
		return nil
	}
	defer func() { engineForTest = nil }()

	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "rec@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "rec@example.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := login["org_id"].(float64)
	_, out := srv.do(t, "POST", "/api/payment/create", token, map[string]any{"provider": "stripe", "amount_usd": "50"})
	orderNo := out["order_no"].(string)

	// 网关侧其实已支付，但 callback 漏发（reconcile 要兜住）
	mock.queryFound, mock.queryPaid, mock.tradeNo, mock.amount = true, true, "pi_recon", decimal.RequireFromString("50")

	// 太新的单（minAge=1h，cutoff 在过去）→ 不对账，给足正常支付窗口
	if checked, credited, err := srv.srv.ReconcileTopups(context.Background(), time.Hour, 100); err != nil || checked != 0 || credited != 0 {
		t.Fatalf("fresh order must be skipped: checked=%d credited=%d err=%v", checked, credited, err)
	}

	// backdate 到 10 分钟前 → 进入对账窗口
	db.Exec("UPDATE topup_orders SET created_at = ? WHERE order_no = ?", time.Now().Add(-10*time.Minute).Unix(), orderNo)
	checked, credited, err := srv.srv.ReconcileTopups(context.Background(), 2*time.Minute, 100)
	if err != nil || checked != 1 || credited != 1 {
		t.Fatalf("reconcile must credit orphan: checked=%d credited=%d err=%v", checked, credited, err)
	}
	acc := &ledger.Account{}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("50")) {
		t.Fatalf("balance after reconcile = %s, want 50", acc.PostedBalance)
	}
	var status int8
	db.Raw("SELECT status FROM topup_orders WHERE order_no = ?", orderNo).Scan(&status)
	if status != topupPaid {
		t.Fatalf("order status = %d, want paid", status)
	}

	// 幂等：订单已 paid，不再被扫到，余额不翻倍
	if checked, credited, _ = srv.srv.ReconcileTopups(context.Background(), 2*time.Minute, 100); checked != 0 || credited != 0 {
		t.Fatalf("paid order must not be re-checked: checked=%d credited=%d", checked, credited)
	}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("50")) {
		t.Fatalf("balance after 2nd reconcile = %s, want 50 (idempotent)", acc.PostedBalance)
	}
}

// 额度三档调账（add/subtract/override）：全走 ledger、扣减有可用余额守卫、override 订正到目标、幂等。
func TestCreditAdjust(t *testing.T) {
	srv, db := setupRelayServer(t)
	_, orgID := seedUserWithCredit(t, srv, "adj@example.com", "100") // add 100
	bal := func() decimal.Decimal {
		acc := &ledger.Account{}
		db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
		return acc.PostedBalance
	}
	adjust := func(mode, amount, key string) int {
		w, _ := srv.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
			"org_id": orgID, "mode": mode, "amount_usd": amount, "reason": "t", "idempotency_key": key,
		})
		return w.Code
	}
	want := func(v string, ctx string) {
		if !bal().Equal(decimal.RequireFromString(v)) {
			t.Fatalf("%s: balance = %s, want %s", ctx, bal(), v)
		}
	}

	if c := adjust("subtract", "30", "s1"); c != 200 {
		t.Fatalf("subtract = %d", c)
	}
	want("70", "after subtract 30")

	if c := adjust("override", "50", "o1"); c != 200 { // 70 → 50（扣 20）
		t.Fatalf("override = %d", c)
	}
	want("50", "after override→50")

	if c := adjust("override", "50", "o2"); c != 200 { // 已是 50 → noop
		t.Fatalf("override noop = %d", c)
	}
	want("50", "after override noop")

	if c := adjust("subtract", "1000", "s2"); c != 409 { // 超过可用 → 拒绝
		t.Fatalf("over-subtract = %d, want 409", c)
	}
	want("50", "after rejected over-subtract (unchanged)")

	adjust("subtract", "10", "dup") // 50 → 40
	adjust("subtract", "10", "dup") // 同键重放 → 不重复
	want("40", "after idempotent dup subtract")

	// 向后兼容：不传 mode 仍是授信
	if w, _ := srv.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
		"org_id": orgID, "amount_usd": "5", "reason": "t", "idempotency_key": "add1",
	}); w.Code != 200 {
		t.Fatalf("add (no mode) = %d", w.Code)
	}
	want("45", "after add 5 (no mode)")
}

// x402 充值档（M4 §5.2 第一档）：建单(返 requirements) → /x402/settle(X-PAYMENT) →
// facilitator verify/settle → 统一资金路径入账。默认禁用，配置齐全才生效。
func TestX402TopupSettle(t *testing.T) {
	srv, db := setupRelayServer(t)
	fac := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/verify":
			_, _ = w.Write([]byte(`{"isValid":true}`))
		case "/settle":
			_, _ = w.Write([]byte(`{"success":true,"transaction":"0xTOPUPTX"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer fac.Close()
	// 启用 x402（总开关 + 三项配置齐全；P0-4 起须显式 x402_enabled=true）
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('x402_enabled','true'),('x402_facilitator_url',?),('x402_pay_to','0xMerchant'),('x402_asset','0xUSDC')`, fac.URL)

	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "x402@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "x402@example.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := login["org_id"].(float64)

	// 建单：返回 requirements（无跳转 url）
	w, out := srv.do(t, "POST", "/api/payment/create", token, map[string]any{"provider": "x402", "amount_usd": "25"})
	if w.Code != 200 || out["requirements"] == nil || out["pay_url"] != "" {
		t.Fatalf("x402 create = %d %v", w.Code, out)
	}
	orderNo := out["order_no"].(string)

	// settle：带 X-PAYMENT 头（base64 paymentPayload）
	xpay := base64.StdEncoding.EncodeToString([]byte(`{"x402Version":1,"scheme":"exact","payload":{}}`))
	settle := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/payment/x402/settle/"+orderNo, nil)
		req.Header.Set("X-PAYMENT", xpay)
		rec := httptest.NewRecorder()
		srv.engine.ServeHTTP(rec, req)
		return rec
	}
	if rec := settle(); rec.Code != 200 {
		t.Fatalf("x402 settle = %d %s", rec.Code, rec.Body.String())
	}
	acc := &ledger.Account{}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("25")) {
		t.Fatalf("balance after x402 = %s, want 25", acc.PostedBalance)
	}
	// 幂等：重复 settle 不重复入账
	if rec := settle(); rec.Code != 200 {
		t.Fatalf("x402 re-settle = %d", rec.Code)
	}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("25")) {
		t.Fatalf("balance after re-settle = %s, want 25 (idempotent)", acc.PostedBalance)
	}
}

// 公开状态页（M3）：无需登录；无渠道=relay down；有活跃渠道=operational；不泄露细节。
func TestPublicStatus(t *testing.T) {
	srv, _ := setupRelayServer(t)

	// 无渠道：relay 无容量 → down，整体 down（HTTP 503，便于 uptime 探针告警）
	w, out := srv.do(t, "GET", "/api/status", "", nil)
	if w.Code != http.StatusServiceUnavailable || out["status"] != "down" {
		t.Fatalf("no-channel status = %d %v, want 503/down", w.Code, out)
	}
	comps, _ := out["components"].([]any)
	if len(comps) != 3 {
		t.Fatalf("components = %v, want 3 (api/database/relay)", comps)
	}
	// 不泄露：响应里不得出现渠道名/厂商/org/成功率数字等运营细节
	body := w.Body.String()
	for _, leak := range []string{"success_rate", "channel", "provider", "org", "calls"} {
		if strings.Contains(body, leak) {
			t.Fatalf("public status leaked %q: %s", leak, body)
		}
	}

	// 有活跃渠道 + 无近期失败 → operational（HTTP 200）
	up := mockUpstreamJSON(t, 1, 1)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	w, out = srv.do(t, "GET", "/api/status", "", nil)
	if w.Code != 200 || out["status"] != "operational" {
		t.Fatalf("healthy status = %d %v, want 200/operational", w.Code, out)
	}
}

// 模型级限流（gap #2）：每 (org, model) 60s 窗口 RPM 上限；per-org 隔离；开关从 settings 读。
func TestModelRateLimit(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 5, 5)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey1, _ := seedUserWithCredit(t, srv, "rl1@example.com", "10")
	apiKey2, _ := seedUserWithCredit(t, srv, "rl2@example.com", "10")
	db.Exec("INSERT INTO ext_settings (name,value) VALUES ('model_rate_limit_enabled','true'),('model_rate_limit_rpm','2')")

	call := func(apiKey string) int {
		w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
			"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
		return w.Code
	}
	// org1：前 2 次放行，第 3 次同窗口触顶 → 429
	if call(apiKey1) != 200 || call(apiKey1) != 200 {
		t.Fatal("first two requests should pass")
	}
	if c := call(apiKey1); c != http.StatusTooManyRequests {
		t.Fatalf("3rd request = %d, want 429", c)
	}
	// org2：独立计数，第 1 次照常放行（per-org 隔离）
	if c := call(apiKey2); c != 200 {
		t.Fatalf("org2 first request = %d, want 200 (per-org isolation)", c)
	}
}

// 渠道健康看板（M2）：从 invocations 派生每渠道成功率/时延/最近活跃；含零流量渠道。
func TestChannelHealth(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL) // 渠道 1（mock），test-model 已定价
	apiKey, orgID := seedUserWithCredit(t, srv, "health@example.com", "10")

	// 一个闲置渠道（无流量）——看板必须也列出它
	srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "idle", "provider": "anthropic", "base_url": "https://idle.example.com",
		"api_key": "sk-x", "models": []string{"unused-model"},
	})

	// 两次成功调用 → 渠道 1 calls=2 succeeded=2 success_rate=1
	for i := 0; i < 2; i++ {
		w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
			"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
		if w.Code != 200 {
			t.Fatalf("relay call %d = %d", i, w.Code)
		}
	}
	// 等两条 invocation 异步落库
	for i := 0; i < 100; i++ {
		var n int64
		db.Model(&invocation.Invocation{}).Where("org_id = ?", uint64(orgID)).Count(&n)
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	w, out := srv.doAdmin(t, "GET", "/api/admin/channel-health", nil)
	if w.Code != 200 {
		t.Fatalf("channel-health = %d %v", w.Code, out)
	}
	chs, _ := out["channels"].([]any)
	if len(chs) != 2 {
		t.Fatalf("health channels = %d, want 2 (incl idle)", len(chs))
	}
	var mock, idle map[string]any
	for _, it := range chs {
		m := it.(map[string]any)
		switch m["name"] {
		case "mock":
			mock = m
		case "idle":
			idle = m
		}
	}
	if mock == nil || idle == nil {
		t.Fatalf("missing channel rows: %v", chs)
	}
	// 活跃渠道：真实聚合
	if mock["calls"] != float64(2) || mock["succeeded"] != float64(2) ||
		mock["failed"] != float64(0) || mock["success_rate"] != float64(1) {
		t.Fatalf("mock health = %v", mock)
	}
	if mock["last_active"] == "" || mock["provider"] != "openai" || mock["status"] != float64(1) {
		t.Fatalf("mock meta = %v", mock)
	}
	// 闲置渠道：零流量、默认满分、不报错
	if idle["calls"] != float64(0) || idle["success_rate"] != float64(1) || idle["last_active"] != "" {
		t.Fatalf("idle health = %v", idle)
	}
}

// admin orgs：余额/成员数/owner email 聚合视图。
func TestAdminListOrgs(t *testing.T) {
	srv, _ := setupRelayServer(t)
	_, orgID := seedUserWithCredit(t, srv, "orgview@example.com", "7.5")

	w, out := srv.doAdmin(t, "GET", "/api/admin/orgs", nil)
	if w.Code != 200 {
		t.Fatalf("orgs = %d %v", w.Code, out)
	}
	orgs, _ := out["orgs"].([]any)
	if len(orgs) != 1 {
		t.Fatalf("orgs len = %d, want 1", len(orgs))
	}
	o := orgs[0].(map[string]any)
	if o["id"] != float64(orgID) || o["owner_email"] != "orgview@example.com" ||
		o["balance"] != "7.5" || o["members"] != float64(1) {
		t.Fatalf("org row = %v", o)
	}
}

// 用量日聚合：1 次成功调用 → 当日 calls=1 succeeded=1 token/费用一致。
func TestUsageSummary(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "summary@example.com", "10")
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{
		"email": "summary@example.com", "password": "longenough1"})
	token := login["token"].(string)

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d", w.Code)
	}
	waitInvocation(t, db, "org_id = ?", uint64(orgID)) // 等异步落库

	w, out := srv.do(t, "GET", "/api/usage/summary?days=7", token, nil)
	if w.Code != 200 {
		t.Fatalf("summary = %d %v", w.Code, out)
	}
	items, _ := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("summary items = %d, want 1", len(items))
	}
	day := items[0].(map[string]any)
	if day["calls"] != float64(1) || day["succeeded"] != float64(1) ||
		day["prompt_tokens"] != float64(1000) || day["completion_tokens"] != float64(500) {
		t.Fatalf("summary day = %v", day)
	}
}

// 余额端点：授信前 0/0，授信后精确，调用后反映扣费。
func TestBalanceEndpoint(t *testing.T) {
	srv, _ := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "bal@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "bal@example.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := login["org_id"].(float64)

	// 授信前：0/0（无账户行是正常态，不报错）
	w, out := srv.do(t, "GET", "/api/balance", token, nil)
	if w.Code != 200 || out["balance"] != "0" {
		t.Fatalf("pre-grant balance = %d %v", w.Code, out)
	}
	srv.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
		"org_id": orgID, "amount_usd": "3.5", "reason": "t", "idempotency_key": "bal-1",
	})
	w, out = srv.do(t, "GET", "/api/balance", token, nil)
	if w.Code != 200 || out["balance"] != "3.5" || out["held"] != "0" {
		t.Fatalf("post-grant balance = %d %v", w.Code, out)
	}
	// 未登录 → 401
	w, _ = srv.do(t, "GET", "/api/balance", "", nil)
	if w.Code != 401 {
		t.Fatalf("unauthed balance = %d, want 401", w.Code)
	}
}

// 公开价目：无需登录；只列"已定价且有活跃渠道"的模型（与 /v1/models 同规则）。
func TestPublicPricing(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1, 1)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL) // test-model 已定价已有渠道
	// 孤儿定价（无渠道服务）不应出现
	srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "orphan-model", "input_usd_per_1m": "1", "output_usd_per_1m": "1",
	})

	w, out := srv.do(t, "GET", "/api/pricing", "", nil) // 无 token
	if w.Code != 200 {
		t.Fatalf("pricing = %d %v", w.Code, out)
	}
	models, _ := out["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("pricing models = %d, want 1 (orphan hidden)", len(models))
	}
	m := models[0].(map[string]any)
	if m["model"] != "test-model" || m["provider"] != "openai" ||
		m["input_usd_per_1m"] != "2" || m["output_usd_per_1m"] != "6" {
		t.Fatalf("pricing row = %v", m)
	}
}

// admin 概览：渠道分布 + 调用统计 + 模型 top + 渠道健康。
func TestAdminOverview(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "overview@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d", w.Code)
	}
	waitInvocation(t, db, "org_id = ?", uint64(orgID))

	w, out := srv.doAdmin(t, "GET", "/api/admin/overview", nil)
	if w.Code != 200 {
		t.Fatalf("overview = %d %v", w.Code, out)
	}
	ch := out["channels"].(map[string]any)
	if ch["total"] != float64(1) || ch["active"] != float64(1) {
		t.Fatalf("channels = %v", ch)
	}
	if out["calls_today"] != float64(1) || out["calls_window"] != float64(1) {
		t.Fatalf("calls = today %v window %v", out["calls_today"], out["calls_window"])
	}
	if out["success_rate"] != float64(1) {
		t.Fatalf("success_rate = %v, want 1", out["success_rate"])
	}
	top := out["top_models"].([]any)
	if len(top) != 1 || top[0].(map[string]any)["model"] != "test-model" ||
		top[0].(map[string]any)["calls"] != float64(1) {
		t.Fatalf("top_models = %v", top)
	}
	health := out["channel_health"].([]any)
	if len(health) != 1 || health[0].(map[string]any)["calls"] != float64(1) {
		t.Fatalf("channel_health = %v", health)
	}
}

// 渠道列表带累计调用数 + 模型数。
func TestAdminListChannelsEnriched(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1, 1)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "chlist@example.com", "10")
	srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	waitInvocation(t, db, "org_id = ?", uint64(orgID))

	w, out := srv.doAdmin(t, "GET", "/api/admin/channels", nil)
	if w.Code != 200 {
		t.Fatalf("list = %d", w.Code)
	}
	chs := out["channels"].([]any)
	c0 := chs[0].(map[string]any)
	if c0["calls"] != float64(1) || c0["model_count"] != float64(1) {
		t.Fatalf("enriched channel = %v", c0)
	}
}

// admin 全局调用日志：跨 org 可见 + 内部字段回显 + status 筛选。
func TestAdminListInvocations(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 100, 50)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "logs@example.com", "10")
	srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	waitInvocation(t, db, "org_id = ?", uint64(orgID))

	// 全局可见 + 内部字段（channel_id/provider/org_id）回显
	w, out := srv.doAdmin(t, "GET", "/api/admin/invocations", nil)
	if w.Code != 200 {
		t.Fatalf("invocations = %d %v", w.Code, out)
	}
	items := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	row := items[0].(map[string]any)
	if row["org_id"] != orgID || row["provider"] != "openai" || row["channel_id"] != float64(1) {
		t.Fatalf("admin row missing internal fields: %v", row)
	}
	// P1-4 不超承诺：元数据-only 存储，不回显 payload 指针（恒为空、指向不存在的对象）
	if _, ok := row["payload_object_key"]; ok {
		t.Fatalf("admin row must not expose payload_object_key (metadata-only store): %v", row)
	}
	// status 筛选：success 命中，failed 落空
	_, out = srv.doAdmin(t, "GET", "/api/admin/invocations?status=success", nil)
	if len(out["items"].([]any)) != 1 {
		t.Fatal("status=success should match")
	}
	_, out = srv.doAdmin(t, "GET", "/api/admin/invocations?status=upstream_error", nil)
	if len(out["items"].([]any)) != 0 {
		t.Fatal("status=upstream_error should be empty")
	}
	// channel 筛选：错的 channel 落空
	_, out = srv.doAdmin(t, "GET", "/api/admin/invocations?channel_id=999", nil)
	if len(out["items"].([]any)) != 0 {
		t.Fatal("channel_id=999 should be empty")
	}
}

// 用量按模型聚合：模型分布 + token + 成本 + 成功数。
func TestUsageByModel(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "bymodel@example.com", "10")
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "bymodel@example.com", "password": "longenough1"})
	token := login["token"].(string)
	srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	waitInvocation(t, db, "org_id = ?", uint64(orgID))

	w, out := srv.do(t, "GET", "/api/usage/by-model?days=7", token, nil)
	if w.Code != 200 {
		t.Fatalf("by-model = %d %v", w.Code, out)
	}
	items := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	m := items[0].(map[string]any)
	if m["model"] != "test-model" || m["calls"] != float64(1) || m["succeeded"] != float64(1) ||
		m["prompt_tokens"] != float64(1000) || m["completion_tokens"] != float64(500) {
		t.Fatalf("by-model row = %v", m)
	}
}

// 渠道探测：从主域名试出含 /v1 的 base_url + 拉模型列表。
func TestAdminProbeChannel(t *testing.T) {
	srv, _ := setupRelayServer(t)
	// mock 上游：只有 /v1/models 返回模型，裸路径 /models 404
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" && r.Header.Get("Authorization") == "Bearer sk-probe" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Server", "test-gateway")
			fmt.Fprint(w, `{"data":[{"id":"gpt-5.2"},{"id":"claude-4"},{"id":"glm-5"}]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer up.Close()

	// 只给主域名（无 /v1），探测应补全为 {url}/v1
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels/probe", map[string]any{
		"api_key": "sk-probe", "base_url": up.URL,
	})
	if w.Code != 200 {
		t.Fatalf("probe = %d %v", w.Code, out)
	}
	if out["ok"] != true {
		t.Fatalf("probe not ok: %v", out)
	}
	if out["detected_base_url"] != up.URL+"/v1" {
		t.Fatalf("detected = %v, want %s/v1", out["detected_base_url"], up.URL)
	}
	models := out["models"].([]any)
	if len(models) != 3 || models[0] != "gpt-5.2" {
		t.Fatalf("models = %v", models)
	}
	if out["gateway"] != "test-gateway" {
		t.Fatalf("gateway = %v", out["gateway"])
	}
	// 错 key → 探测失败
	w, out = srv.doAdmin(t, "POST", "/api/admin/channels/probe", map[string]any{
		"api_key": "sk-wrong", "base_url": up.URL,
	})
	if out["ok"] != false {
		t.Fatalf("wrong key should fail: %v", out)
	}
}

// 端到端缓存计费：上游返回 cached_tokens，命中部分按缓存价折扣结算。
func TestRelayCachedBilling(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 1000 prompt(800 cached) + 500 completion
		fmt.Fprint(w, `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1000,"completion_tokens":500,"prompt_tokens_details":{"cached_tokens":800}}}`)
	}))
	defer up.Close()
	// 渠道
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "c", "provider": "openai", "base_url": up.URL, "api_key": "sk-x", "models": []string{"m"},
	})
	if w.Code != 201 {
		t.Fatalf("channel = %d %v", w.Code, out)
	}
	// 定价：input $10/1M, output $30/1M, cached $1/1M
	srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "m", "input_usd_per_1m": "10", "output_usd_per_1m": "30", "cached_input_usd_per_1m": "1",
	})
	apiKey, orgID := seedUserWithCredit(t, srv, "cache@example.com", "10")

	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "m", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d", w.Code)
	}
	rec := waitInvocation(t, db, "org_id = ?", uint64(orgID))
	// 期望 cost = 200×10/1M + 800×1/1M + 500×30/1M = 0.0178（而非全价 0.025）
	if !rec.CostUSD.Equal(decimal.RequireFromString("0.0178")) {
		t.Fatalf("cost = %s, want 0.0178 (cached discount applied)", rec.CostUSD)
	}
}

// 分组倍率：VIP 组 0.5× 时计费减半，hold/capture 一致。
func TestRelayGroupRatio(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL) // input $2/1M, output $6/1M
	apiKey, orgID := seedUserWithCredit(t, srv, "vip@example.com", "10")

	// 建 vip 分组 0.5×，把 org 划入 vip
	if w, out := srv.doAdmin(t, "POST", "/api/admin/group-ratios", map[string]any{"name": "vip", "ratio": "0.5"}); w.Code != 200 {
		t.Fatalf("upsert ratio = %d %v", w.Code, out)
	}
	if w, _ := srv.doAdmin(t, "PUT", fmt.Sprintf("/api/admin/orgs/%v/group", uint64(orgID)), map[string]any{"group": "vip"}); w.Code != 200 {
		t.Fatal("set org group failed")
	}

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d", w.Code)
	}
	rec := waitInvocation(t, db, "org_id = ?", uint64(orgID))
	// 基础 = 1000×2/1M + 500×6/1M = 0.002 + 0.003 = 0.005；×0.5 = 0.0025
	if !rec.CostUSD.Equal(decimal.RequireFromString("0.0025")) {
		t.Fatalf("cost = %s, want 0.0025 (vip 0.5x)", rec.CostUSD)
	}
}

// 分组倍率 CRUD：default 不可删。
func TestAdminGroupRatioCRUD(t *testing.T) {
	srv, _ := setupRelayServer(t)
	srv.doAdmin(t, "POST", "/api/admin/group-ratios", map[string]any{"name": "svip", "ratio": "0.3"})
	w, out := srv.doAdmin(t, "GET", "/api/admin/group-ratios", nil)
	if w.Code != 200 {
		t.Fatalf("list = %d", w.Code)
	}
	groups := out["groups"].([]any)
	found := false
	for _, g := range groups {
		if g.(map[string]any)["name"] == "svip" {
			found = true
		}
	}
	if !found {
		t.Fatal("svip not in list")
	}
	// default 不可删
	if w, _ := srv.doAdmin(t, "DELETE", "/api/admin/group-ratios/default", nil); w.Code != 400 {
		t.Fatalf("delete default = %d, want 400", w.Code)
	}
	// svip 可删
	if w, _ := srv.doAdmin(t, "DELETE", "/api/admin/group-ratios/svip", nil); w.Code != 200 {
		t.Fatalf("delete svip = %d, want 200", w.Code)
	}
}

// Key 精细管控：模型白名单拒绝 + quota 上限拒绝 + used_amount 累加。
func TestKeyControls(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL) // test-model, input $2/1M out $6/1M

	// 注册 + 登录 + 授信
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "kc@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "kc@example.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := login["org_id"].(float64)
	srv.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
		"org_id": orgID, "amount_usd": "10", "reason": "t", "idempotency_key": "kc",
	})

	// 建一个只允许 other-model、quota $0.004 的 key
	_, keyOut := srv.do(t, "POST", "/api/keys", token, map[string]any{
		"name": "limited", "model_limits": []string{"other-model"}, "quota_limit": "0.004",
	})
	apiKey := keyOut["key"].(string)

	// 调 test-model（不在白名单）→ 403
	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 403 {
		t.Fatalf("non-whitelisted model = %d, want 403", w.Code)
	}

	// 换一个允许 test-model、quota $0.004 的 key（一次调用花 0.005 > 0.004）
	_, keyOut = srv.do(t, "POST", "/api/keys", token, map[string]any{
		"name": "q", "model_limits": []string{"test-model"}, "quota_limit": "0.004",
	})
	apiKey2 := keyOut["key"].(string)
	keyID2 := keyOut["id"]
	// 第一次成功（调用前 used=0 < limit）
	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey2, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("first call = %d, want 200", w.Code)
	}
	// 等 used_amount 累加（0.005 > 0.004）
	for i := 0; i < 100; i++ {
		var k struct{ UsedAmount string }
		db.Raw("SELECT used_amount FROM api_keys WHERE id = ?", keyID2).Scan(&k)
		if k.UsedAmount != "" && k.UsedAmount != "0" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 第二次：used(0.005) >= limit(0.004) → 402
	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey2, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 402 {
		t.Fatalf("over-quota call = %d, want 402", w.Code)
	}
}

// 运营设置：注册开关关闭后注册被拒；公开 config 反映设置。
func TestSettingsAndRegisterGate(t *testing.T) {
	srv, _ := setupRelayServer(t)
	// 默认可注册
	w, _ := srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "s1@example.com", "password": "longenough1"})
	if w.Code != 201 {
		t.Fatalf("default register = %d, want 201", w.Code)
	}
	// 关闭注册
	if w, _ := srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"register_enabled": "false", "announcement": "down for maintenance"}); w.Code != 200 {
		t.Fatal("set settings failed")
	}
	// 注册被拒
	w, _ = srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "s2@example.com", "password": "longenough1"})
	if w.Code != 403 {
		t.Fatalf("register when disabled = %d, want 403", w.Code)
	}
	// 公开 config 反映
	_, cfg := srv.do(t, "GET", "/api/config", "", nil)
	if cfg["register_enabled"] != false || cfg["announcement"] != "down for maintenance" {
		t.Fatalf("config = %v", cfg)
	}
	// 未知键被拒
	if w, _ := srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"bogus_key": "x"}); w.Code != 400 {
		t.Fatalf("unknown setting = %d, want 400", w.Code)
	}
}

// 用户管理 + 审计：封禁用户 + 操作留痕。
func TestUserManageAndAudit(t *testing.T) {
	srv, _ := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "victim@example.com", "password": "longenough1"})
	_, list := srv.doAdmin(t, "GET", "/api/admin/users", nil)
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("users = %d, want 1", len(items))
	}
	uid := items[0].(map[string]any)["id"]

	// 封禁
	if w, _ := srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/users/%v/manage", uid), map[string]any{"action": "ban"}); w.Code != 200 {
		t.Fatal("ban failed")
	}
	// 被封用户无法登录
	w, _ := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "victim@example.com", "password": "longenough1"})
	if w.Code == 200 {
		t.Fatal("banned user should not log in")
	}
	// 解封后可登录
	srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/users/%v/manage", uid), map[string]any{"action": "unban"})
	w, _ = srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "victim@example.com", "password": "longenough1"})
	if w.Code != 200 {
		t.Fatalf("unbanned login = %d, want 200", w.Code)
	}
	// 审计留痕（ban + unban）
	_, audit := srv.doAdmin(t, "GET", "/api/admin/audit-logs", nil)
	if audit["total"].(float64) < 2 {
		t.Fatalf("audit total = %v, want >=2", audit["total"])
	}
}

// mockPayEngine：测试用支付引擎，回调与查询结果均可控。
type mockPayEngine struct {
	orderNo, tradeNo      string
	amount                decimal.Decimal
	success               bool
	queryFound, queryPaid bool  // QueryOrder（对账/过期确认）受控返回
	queryErr              error // 非 nil = 模拟网关查询故障
}

func (m *mockPayEngine) Name() string { return "mock" }
func (m *mockPayEngine) CreatePayment(_ context.Context, req *payment.CreateRequest) (*payment.CreateResult, error) {
	return &payment.CreateResult{PayURL: "https://pay.test/" + req.OrderNo, ProviderRef: req.OrderNo}, nil
}
func (m *mockPayEngine) VerifyCallback(_ []byte, _, _ map[string]string) (*payment.CallbackResult, error) {
	return &payment.CallbackResult{OrderNo: m.orderNo, TradeNo: m.tradeNo, Amount: m.amount, Currency: "USD", Success: m.success}, nil
}
func (m *mockPayEngine) AckBody() string { return "ok" }
func (m *mockPayEngine) QueryOrder(_ context.Context, _ payment.QueryRequest) (*payment.QueryResult, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return &payment.QueryResult{Found: m.queryFound, Paid: m.queryPaid, TradeNo: m.tradeNo, Amount: m.amount, Currency: "USD"}, nil
}

// 支付入金红线：成功入账一次 + 幂等不重复 + 终态保护 + 金额过低拒收。
func TestPaymentCreditFlow(t *testing.T) {
	srv, db := setupRelayServer(t)
	mock := &mockPayEngine{}
	engineForTest = func(p string) payment.Engine {
		if p == "stripe" {
			return mock
		}
		return nil
	}
	defer func() { engineForTest = nil }()

	apiKey, orgID := seedUserWithCredit(t, srv, "pay@example.com", "")
	_ = apiKey
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "pay@example.com", "password": "longenough1"})
	token := login["token"].(string)

	// 建订单 $50
	w, out := srv.do(t, "POST", "/api/payment/create", token, map[string]any{"provider": "stripe", "amount_usd": "50"})
	if w.Code != 200 {
		t.Fatalf("create topup = %d %v", w.Code, out)
	}
	orderNo := out["order_no"].(string)

	// 回调成功入金
	mock.orderNo, mock.tradeNo, mock.amount, mock.success = orderNo, "pi_1", decimal.RequireFromString("50"), true
	w, _ = srv.do(t, "POST", "/api/payment/callback/stripe", "", nil)
	if w.Code != 200 {
		t.Fatalf("callback = %d", w.Code)
	}
	// 余额到账 $50
	acc := &ledger.Account{}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("50")) {
		t.Fatalf("balance after topup = %s, want 50", acc.PostedBalance)
	}
	// 幂等：重复回调不重复入账
	srv.do(t, "POST", "/api/payment/callback/stripe", "", nil)
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("50")) {
		t.Fatalf("balance after duplicate callback = %s, want 50 (idempotent)", acc.PostedBalance)
	}

	// 新订单 + 金额过低回调 → 拒收（不入金）
	w, out = srv.do(t, "POST", "/api/payment/create", token, map[string]any{"provider": "stripe", "amount_usd": "100"})
	order2 := out["order_no"].(string)
	mock.orderNo, mock.amount, mock.success = order2, decimal.RequireFromString("1"), true // 实付远低于应付
	w, _ = srv.do(t, "POST", "/api/payment/callback/stripe", "", nil)
	if w.Code == 200 {
		t.Fatal("low-amount callback should be rejected")
	}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("50")) {
		t.Fatalf("balance after low-amount = %s, want 50 (rejected)", acc.PostedBalance)
	}
}

// 注：TestAffiliateRebate 已删除——返佣/affiliate 功能已移除（本站无此功能，旧平台迁移残留）。
// payAffiliateRebate 现为永久 no-op，相应测试随之删除。

// 二次验证 step-up（gap #4）：默认关闭零摩擦；开启后创建 Key 需 X-Secure-Verify；令牌绑 uid。
func TestStepUpVerification(t *testing.T) {
	srv, db := setupRelayServer(t)
	reg := func(email string) string {
		srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": email, "password": "longenough1"})
		_, l := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "longenough1"})
		return l["token"].(string)
	}
	tokA := reg("step-a@example.com")
	tokB := reg("step-b@example.com")

	createKey := func(sess, stepup string) int {
		req := httptest.NewRequest("POST", "/api/keys", bytes.NewBufferString(`{"name":"k"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+sess)
		if stepup != "" {
			req.Header.Set("X-Secure-Verify", stepup)
		}
		w := httptest.NewRecorder()
		srv.engine.ServeHTTP(w, req)
		return w.Code
	}

	// 默认关闭：创建 Key 无需 step-up
	if c := createKey(tokA, ""); c != 201 {
		t.Fatalf("default create key = %d, want 201 (stepup off)", c)
	}

	// 开启开关
	db.Exec("INSERT INTO ext_settings (name,value) VALUES ('require_stepup_for_keys','true')")

	// 无 step-up → 403 VERIFICATION_REQUIRED
	w, out := srv.do(t, "POST", "/api/keys", tokA, map[string]any{"name": "k"})
	if w.Code != 403 || out["code"] != "VERIFICATION_REQUIRED" {
		t.Fatalf("create without stepup = %d %v, want 403 VERIFICATION_REQUIRED", w.Code, out)
	}

	// 错密码 verify → 401
	if w, _ := srv.do(t, "POST", "/api/me/verify", tokA, map[string]any{"password": "wrongpass1"}); w.Code != 401 {
		t.Fatalf("verify wrong password = %d, want 401", w.Code)
	}
	// 对密码 verify → 拿令牌
	_, vr := srv.do(t, "POST", "/api/me/verify", tokA, map[string]any{"password": "longenough1"})
	stepTok, _ := vr["token"].(string)
	if stepTok == "" {
		t.Fatalf("verify did not return token: %v", vr)
	}
	// 带令牌创建 Key → 201
	if c := createKey(tokA, stepTok); c != 201 {
		t.Fatalf("create with stepup = %d, want 201", c)
	}
	// 令牌绑 uid：A 的令牌给 B 用 → 403
	if c := createKey(tokB, stepTok); c != 403 {
		t.Fatalf("cross-user stepup token = %d, want 403", c)
	}
	// 伪造令牌 → 403
	if c := createKey(tokA, "1.9999999999.deadbeef"); c != 403 {
		t.Fatalf("forged stepup token = %d, want 403", c)
	}

	// 过期令牌 → 403（把服务端时钟前推 6 分钟使刚发的令牌过期）
	_, vx := srv.do(t, "POST", "/api/me/verify", tokA, map[string]any{"password": "longenough1"})
	expTok := vx["token"].(string)
	realNow := srv.srv.now
	srv.srv.now = func() time.Time { return realNow().Add(6 * time.Minute) }
	if c := createKey(tokA, expTok); c != 403 {
		t.Fatalf("expired stepup token = %d, want 403", c)
	}
	srv.srv.now = realNow

	// TOTP 路径：给 A 开 2FA，用 TOTP 再认证换令牌 → 创建 Key
	_, setup := srv.do(t, "POST", "/api/me/2fa/setup", tokA, nil)
	secret := setup["secret"].(string)
	if w, _ := srv.do(t, "POST", "/api/me/2fa/enable", tokA, map[string]string{"code": genTOTP(secret)}); w.Code != 200 {
		t.Fatalf("enable 2fa = %d", w.Code)
	}
	_, vt := srv.do(t, "POST", "/api/me/verify", tokA, map[string]any{"totp_code": genTOTP(secret)})
	if vt["token"] == nil {
		t.Fatalf("TOTP verify did not return token: %v", vt)
	}
	if c := createKey(tokA, vt["token"].(string)); c != 201 {
		t.Fatalf("create with TOTP stepup = %d, want 201", c)
	}
}

// 2FA 全流程：setup → enable → 登录二阶段验证。
func TestTwoFactorFlow(t *testing.T) {
	srv, _ := setupRelayServer(t) // 有 keyring
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "2fa@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "2fa@example.com", "password": "longenough1"})
	token := login["token"].(string)

	// 未启用 2FA 时重置备用码 → 400（边界）
	if w, _ := srv.do(t, "POST", "/api/me/2fa/backup-codes", token, map[string]string{"code": "123456"}); w.Code != 400 {
		t.Fatalf("regen before enable = %d, want 400", w.Code)
	}

	// setup 拿 secret
	_, setup := srv.do(t, "POST", "/api/me/2fa/setup", token, nil)
	secret := setup["secret"].(string)
	if len(secret) < 16 {
		t.Fatalf("secret = %q", secret)
	}
	// 错码 enable 失败
	w, _ := srv.do(t, "POST", "/api/me/2fa/enable", token, map[string]string{"code": "000000"})
	if w.Code == 200 {
		t.Fatal("wrong code should not enable")
	}
	// 正确码 enable
	code := totp.Validate // 用包生成当前码
	_ = code
	curCode := genTOTP(secret)
	w, en := srv.do(t, "POST", "/api/me/2fa/enable", token, map[string]string{"code": curCode})
	if w.Code != 200 {
		t.Fatalf("enable = %d", w.Code)
	}
	// enable 返回 10 个备用码（明文仅此一次）
	rawCodes, _ := en["backup_codes"].([]any)
	if len(rawCodes) != 10 {
		t.Fatalf("backup_codes = %d, want 10", len(rawCodes))
	}
	backup := rawCodes[0].(string)

	// 重新登录 → 要求 2FA（不直接发 session）
	_, l2 := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "2fa@example.com", "password": "longenough1"})
	if l2["requires_2fa"] != true {
		t.Fatalf("login should require 2fa: %v", l2)
	}
	twofaToken := l2["token"].(string)
	if l2["token"] == nil {
		t.Fatal("no 2fa token")
	}
	// 二阶段验证 → 真 session
	w, l3 := srv.do(t, "POST", "/api/auth/2fa", "", map[string]any{"token": twofaToken, "code": genTOTP(secret)})
	if w.Code != 200 || l3["token"] == nil {
		t.Fatalf("2fa verify = %d %v", w.Code, l3)
	}

	// 用备用码登录（设备丢失场景）
	loginStage := func() string {
		_, l := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "2fa@example.com", "password": "longenough1"})
		return l["token"].(string)
	}
	w, lb := srv.do(t, "POST", "/api/auth/2fa", "", map[string]any{"token": loginStage(), "code": backup})
	if w.Code != 200 || lb["token"] == nil {
		t.Fatalf("backup-code login = %d %v", w.Code, lb)
	}
	// 同一备用码重用 → 拒绝（用过即焚）
	if w, _ := srv.do(t, "POST", "/api/auth/2fa", "", map[string]any{"token": loginStage(), "code": backup}); w.Code == 200 {
		t.Fatal("reused backup code must be rejected")
	}
	// /api/me 报告剩余 9（用掉 1 个）
	if _, meR := srv.do(t, "GET", "/api/me", token, nil); meR["backup_codes_remaining"] != float64(9) {
		t.Fatalf("backup_codes_remaining = %v, want 9", meR["backup_codes_remaining"])
	}
	// 另一个原始备用码仍可用（隔离性，非全量失效）
	if w, _ := srv.do(t, "POST", "/api/auth/2fa", "", map[string]any{"token": loginStage(), "code": rawCodes[2].(string)}); w.Code != 200 {
		t.Fatal("a different original backup code should still work")
	}

	// 重置备用码（验 TOTP）：旧码全失效
	w, rg := srv.do(t, "POST", "/api/me/2fa/backup-codes", token, map[string]string{"code": genTOTP(secret)})
	if w.Code != 200 {
		t.Fatalf("regen backup = %d", w.Code)
	}
	newCodes, _ := rg["backup_codes"].([]any)
	if len(newCodes) != 10 {
		t.Fatalf("regen returned %d codes, want 10", len(newCodes))
	}
	// 旧的另一个备用码（rawCodes[1]）重置后失效
	if w, _ := srv.do(t, "POST", "/api/auth/2fa", "", map[string]any{"token": loginStage(), "code": rawCodes[1].(string)}); w.Code == 200 {
		t.Fatal("old backup code after regen must be rejected")
	}
	// 新码可用
	if w, _ := srv.do(t, "POST", "/api/auth/2fa", "", map[string]any{"token": loginStage(), "code": newCodes[0].(string)}); w.Code != 200 {
		t.Fatal("new backup code should work")
	}
}

// genTOTP：测试用，生成 secret 的当前有效码（暴力试 0-999999 不现实，改用 totp 包）。
func genTOTP(secret string) string {
	return totp.CurrentCode(secret)
}

// OAuth：未配置时无提供方；state 签名验证防 CSRF。
func TestOAuthProvidersAndState(t *testing.T) {
	srv, _ := setupRelayServer(t)
	// 未配置 → 空列表
	_, out := srv.do(t, "GET", "/api/auth/oauth", "", nil)
	if out["providers"] != nil {
		if ps, ok := out["providers"].([]any); ok && len(ps) != 0 {
			t.Fatalf("providers should be empty when unconfigured: %v", ps)
		}
	}
	// 未配置 start → 400
	w, _ := srv.do(t, "GET", "/api/auth/oauth/github/start", "", nil)
	if w.Code != 400 {
		t.Fatalf("unconfigured start = %d, want 400", w.Code)
	}
}
