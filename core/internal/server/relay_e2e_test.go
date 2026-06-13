package server

import (
	"bytes"
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
	"github.com/laiai/laiai-next/core/internal/relay"
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
    weight INTEGER NOT NULL DEFAULT 1, priority INTEGER NOT NULL DEFAULT 0,
    max_concurrent_tasks INTEGER NOT NULL DEFAULT 3, strict_response INTEGER NOT NULL DEFAULT 1, model_mapping TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id TEXT NOT NULL UNIQUE,
    org_id INTEGER NOT NULL, user_id INTEGER, api_key_id INTEGER NOT NULL,
    model TEXT NOT NULL, channel_id INTEGER NOT NULL, provider TEXT NOT NULL,
    status TEXT NOT NULL, fail_reason TEXT NOT NULL DEFAULT '',
    upstream_task_id TEXT NOT NULL DEFAULT '', result_url TEXT NOT NULL DEFAULT '',
    hold_id INTEGER NOT NULL, est_tokens INTEGER NOT NULL DEFAULT 0,
    actual_tokens INTEGER NOT NULL DEFAULT 0, cost_usd NUMERIC NOT NULL DEFAULT 0,
    price_out_usd_per_1m NUMERIC NOT NULL,
    submitted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME, finished_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE model_prices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model TEXT NOT NULL UNIQUE,
    input_usd_per_1m NUMERIC NOT NULL, output_usd_per_1m NUMERIC NOT NULL,
    image_usd NUMERIC NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE invocations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, user_id INTEGER, api_key_id INTEGER NOT NULL,
    request_id TEXT NOT NULL UNIQUE, model TEXT NOT NULL,
    channel_id INTEGER NOT NULL DEFAULT 0, provider TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL, status_code INTEGER NOT NULL DEFAULT 0,
    is_stream INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd NUMERIC NOT NULL DEFAULT 0, usage_source TEXT NOT NULL DEFAULT '',
    frt_ms INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
    upstream_request_id TEXT NOT NULL DEFAULT '', error_brief TEXT NOT NULL DEFAULT '',
    payload_object_key TEXT NOT NULL DEFAULT '',
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
	rec := invocation.NewRecorder(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Cleanup(rec.Close)
	engine := New(db, Options{
		AuthSecret:    testSecret,
		AdminToken:    []byte(testAdminToken),
		Keyring:       testKeyring(t),
		Recorder:      rec,
		AuthRateBurst: 100000, // 测试不限流（功能流密集发起 auth 调用）
	})
	return &gin999{engine: engine}, db
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
