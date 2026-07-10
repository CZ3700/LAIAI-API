package server

// BYOK e2e（0044，设计 §4.4）：
//   - 路由：owner org 命中自有渠道（虚拟顶层）；其他 org 绝不打到别人的 BYOK（隔离红线）
//   - 计费：BYOK 命中按 byok_fee_percent 结算；他人/回落按全价
//   - 回落：BYOK 上游失败 → 平台渠道接棒 → 全价计费
//   - 管理 API：SSRF/provider/models 校验、脱敏列表、启停删

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/relay"
)

// byokMockUpstream：计数 + 校验 BYOK 凭证的 mock 上游（与平台 mockUpstreamJSON 区分开）。
func byokMockUpstream(t *testing.T, hits *atomic.Int64, wantKey string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantKey {
			t.Errorf("byok upstream got credential %q, want %q", got, "Bearer "+wantKey)
		}
		if status != 200 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"byok upstream down"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-b","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`)
	}))
}

// seedByokChannel：直插 BYOK 渠道（绕过 API 的 https/SSRF 校验——那是配置面测试的职责；
// 本地 mock 上游只有 http。凭证用同一 keyring 加密，转发路径逐字生产同款）。
func seedByokChannel(t *testing.T, srv *gin999, orgID float64, baseURL, apiKey string) uint64 {
	t.Helper()
	enc, err := srv.srv.keyring.Encrypt(apiKey)
	if err != nil {
		t.Fatal(err)
	}
	ch := &relay.Channel{
		Name: "byok-test", Provider: "openai", BaseURL: baseURL,
		CredentialEnc: enc, Models: "test-model", Weight: 100,
		OwnerOrgID: uint64(orgID), Status: 1,
	}
	if err := srv.srv.db.Create(ch).Error; err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

func TestByokRoutingIsolationAndFee(t *testing.T) {
	relay.ByokAllowPrivateForTest = true // mock 上游在 127.0.0.1（生产恒禁私网）
	t.Cleanup(func() { relay.ByokAllowPrivateForTest = false })
	srv, db := setupRelayServer(t)
	platUp := mockUpstreamJSON(t, 5, 7)
	setupChannelAndPrice(t, srv, platUp.URL) // 平台渠道 + test-model 价 $2/$6 每 1M
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('byok_fee_percent', '10')")

	keyA, orgA := seedUserWithCredit(t, srv, "byok-a@example.com", "10")
	keyB, orgB := seedUserWithCredit(t, srv, "byok-b@example.com", "10")

	var byokHits atomic.Int64
	byokUp := byokMockUpstream(t, &byokHits, "sk-byok-own", 200)
	seedByokChannel(t, srv, orgA, byokUp.URL, "sk-byok-own")

	call := func(k string) int {
		w, _ := srv.do(t, "POST", "/v1/chat/completions", k, map[string]any{
			"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
		return w.Code
	}

	// org A：命中自有渠道（虚拟顶层），按 10% 费率结算
	if c := call(keyA); c != 200 {
		t.Fatalf("orgA call = %d", c)
	}
	if byokHits.Load() != 1 {
		t.Fatalf("byok hits = %d, want 1 (owner routed to own channel)", byokHits.Load())
	}
	invA := waitInvocation(t, db, "org_id = ? AND status = ?", uint64(orgA), invocation.StatusSuccess)
	// 全价 = (5×2 + 7×6)/1e6 = 0.000052；BYOK 10% = 0.0000052
	if invA.CostUSD.String() != "0.0000052" {
		t.Fatalf("orgA byok cost = %s, want 0.0000052 (10%% of full)", invA.CostUSD)
	}

	// org B：同模型请求绝不碰 A 的 BYOK（隔离红线），走平台渠道全价
	if c := call(keyB); c != 200 {
		t.Fatalf("orgB call = %d", c)
	}
	if byokHits.Load() != 1 {
		t.Fatalf("byok hits = %d after orgB call, want still 1 (isolation red line)", byokHits.Load())
	}
	invB := waitInvocation(t, db, "org_id = ? AND status = ?", uint64(orgB), invocation.StatusSuccess)
	if invB.CostUSD.String() != "0.000052" {
		t.Fatalf("orgB cost = %s, want 0.000052 (full price)", invB.CostUSD)
	}
}

func TestByokFallbackToPlatformFullPrice(t *testing.T) {
	relay.ByokAllowPrivateForTest = true // mock 上游在 127.0.0.1（生产恒禁私网）
	t.Cleanup(func() { relay.ByokAllowPrivateForTest = false })
	srv, db := setupRelayServer(t)
	platUp := mockUpstreamJSON(t, 5, 7)
	setupChannelAndPrice(t, srv, platUp.URL)
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('byok_fee_percent', '10')")

	keyA, orgA := seedUserWithCredit(t, srv, "byok-fb@example.com", "10")
	var byokHits atomic.Int64
	byokUp := byokMockUpstream(t, &byokHits, "sk-byok-own", 503) // 自有上游恒 503（可重试类）
	seedByokChannel(t, srv, orgA, byokUp.URL, "sk-byok-own")

	w, _ := srv.do(t, "POST", "/v1/chat/completions", keyA, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("fallback call = %d, want 200 (platform takes over)", w.Code)
	}
	if byokHits.Load() < 1 {
		t.Fatal("byok upstream never tried (should be first)")
	}
	inv := waitInvocation(t, db, "org_id = ? AND status = ?", uint64(orgA), invocation.StatusSuccess)
	if inv.CostUSD.String() != "0.000052" {
		t.Fatalf("fallback cost = %s, want full 0.000052 (platform channel won)", inv.CostUSD)
	}
}

func TestByokManagementAPI(t *testing.T) {
	srv, _ := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "byok-mgr@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "byok-mgr@example.com", "password": "longenough1"})
	tok := login["token"].(string)

	// SSRF/校验拒绝面
	bad := []map[string]any{
		{"provider": "openai", "api_key": "sk-x", "base_url": "http://api.openai.com/v1", "models": []string{"m"}},      // 非 https
		{"provider": "openai", "api_key": "sk-x", "base_url": "https://169.254.169.254/v1", "models": []string{"m"}},    // metadata IP
		{"provider": "advanced_custom", "api_key": "sk-x", "base_url": "https://x.example/v1", "models": []string{"m"}}, // 白名单外
		{"provider": "openai", "api_key": "sk-x", "models": []string{}},                                                 // 无模型
		{"provider": "openai", "base_url": "https://x.example/v1", "models": []string{"m"}},                             // 无 key
	}
	for i, b := range bad {
		if w, _ := srv.do(t, "POST", "/api/org/byok-channels", tok, b); w.Code != http.StatusBadRequest {
			t.Fatalf("bad case %d = %d, want 400", i, w.Code)
		}
	}

	// 合法创建（默认 base_url 免填；创建期不拨号）
	w, out := srv.do(t, "POST", "/api/org/byok-channels", tok, map[string]any{
		"provider": "openai", "api_key": "sk-byok-mgr-secret", "models": []string{"gpt-x", "gpt-y"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %v", w.Code, out)
	}
	id := out["id"].(float64)
	if out["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("default base_url = %v", out["base_url"])
	}

	// 列表脱敏：绝不回显凭证
	_, list := srv.do(t, "GET", "/api/org/byok-channels", tok, nil)
	rows := list["channels"].([]any)
	if len(rows) != 1 {
		t.Fatalf("list = %d rows", len(rows))
	}
	if _, has := rows[0].(map[string]any)["credential_enc"]; has {
		t.Fatal("credential_enc leaked in list")
	}

	// 启停 + 删除
	if w, _ := srv.do(t, "PATCH", fmt.Sprintf("/api/org/byok-channels/%.0f", id), tok, map[string]any{"status": 0}); w.Code != 200 {
		t.Fatalf("disable = %d", w.Code)
	}
	if w, _ := srv.do(t, "DELETE", fmt.Sprintf("/api/org/byok-channels/%.0f", id), tok, nil); w.Code != 200 {
		t.Fatalf("delete = %d", w.Code)
	}
	_, list2 := srv.do(t, "GET", "/api/org/byok-channels", tok, nil)
	if len(list2["channels"].([]any)) != 0 {
		t.Fatal("channel not deleted")
	}
}