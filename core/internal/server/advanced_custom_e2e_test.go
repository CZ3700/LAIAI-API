package server

// Advanced Custom Channel e2e（指令 §3.3）：
// 不写代码接自定义上游（自定义路径 + 自定义认证头）；converter 选格式转换器；
// SSRF 拦内网/metadata；非法 converter 拒。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// 无码自定义上游：converter=none + 自定义 upstream_path + header_override 自定义认证头
// （{{credential}} 占位），OpenAI 兼容响应正常计费。
func TestAdvancedCustomNoCodeUpstream(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('advanced_custom_allow_private', 'true')") // httptest 走 127.0.0.1
	var gotPath, gotAuth atomic.Value
	gotPath.Store("")
	gotAuth.Store("")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		gotAuth.Store(r.Header.Get("X-Custom-Auth"))
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	}))
	defer up.Close()

	// advanced_custom 渠道：自定义端点 /weird/endpoint + 自定义认证头（默认 Authorization 删掉）
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "adv", "provider": "advanced_custom", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
		"converter":       "none",
		"upstream_path":   "/weird/endpoint",
		"header_override": `{"X-Custom-Auth":"{{credential}}","Authorization":null}`,
	})
	if w.Code != 201 {
		t.Fatalf("create advanced channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, orgID := seedUserWithCredit(t, srv, "adv@example.com", "10")

	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chat via advanced = %d %s", w.Code, w.Body.String())
	}
	if gotPath.Load() != "/weird/endpoint" {
		t.Fatalf("custom path not honored: %v", gotPath.Load())
	}
	if gotAuth.Load() != "sk-upstream-secret" {
		t.Fatalf("custom auth header/credential placeholder not applied: %v", gotAuth.Load())
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.995")) {
		t.Fatalf("billing = %s, want 9.995", acc.PostedBalance)
	}
}

// converter=openai_chat_to_gemini_generate_content：chat 请求转 Gemini 原生 generateContent。
func TestAdvancedCustomGeminiConverter(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('advanced_custom_allow_private', 'true')") // httptest 走 127.0.0.1
	var gotPath atomic.Value
	gotPath.Store("")
	var gotBody atomic.Value
	gotBody.Store(map[string]any{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		gotBody.Store(b)
		w.Header().Set("Content-Type", "application/json")
		// Gemini 原生响应形态
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"hi from gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3},"modelVersion":"gemini-x"}`)
	}))
	defer up.Close()

	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "adv-gem", "provider": "advanced_custom", "base_url": up.URL,
		"api_key": "sk-gem", "models": []string{"test-model"},
		"converter": "openai_chat_to_gemini_generate_content",
	})
	if w.Code != 201 {
		t.Fatalf("create = %d %v", w.Code, out)
	}
	srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	apiKey, _ := seedUserWithCredit(t, srv, "adv-gem@example.com", "10")

	w, out = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chat via gemini converter = %d %v", w.Code, out)
	}
	// 上游收到 Gemini 原生路径 + contents 结构；客户端拿回 chat.completion
	if !strings.Contains(gotPath.Load().(string), ":generateContent") {
		t.Fatalf("not gemini native path: %v", gotPath.Load())
	}
	if _, ok := gotBody.Load().(map[string]any)["contents"]; !ok {
		t.Fatalf("upstream body not converted to gemini contents: %v", gotBody.Load())
	}
	if out["object"] != "chat.completion" {
		t.Fatalf("client did not get chat.completion: %v", out)
	}
	msg := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "hi from gemini" {
		t.Fatalf("converted content = %v", msg["content"])
	}
}

// SSRF：advanced_custom 渠道 base_url 指向内网/metadata → 创建即 400。
func TestAdvancedCustomSSRFBlocked(t *testing.T) {
	srv, _ := setupRelayServer(t)
	for _, badURL := range []string{
		"http://127.0.0.1:8080/v1",
		"http://169.254.169.254/latest/meta-data",
		"http://10.1.2.3:9000/v1",
		"http://192.168.1.1/v1",
		"http://[::1]/v1",
		"http://100.64.0.1/v1",                 // CGNAT
		"https://user:pass@api.example.com/v1", // 嵌凭证
		"ftp://api.example.com/v1",             // 非 http
	} {
		w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
			"name": "ssrf", "provider": "advanced_custom", "base_url": badURL,
			"api_key": "sk-x", "models": []string{"m"}, "converter": "none",
		})
		if w.Code != 400 {
			t.Fatalf("SSRF url %q: create = %d %v, want 400", badURL, w.Code, out)
		}
	}
	// 普通 provider（非 advanced）不受 SSRF 约束——自建 ollama 指 localhost 合法
	w, _ := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ollama", "provider": "ollama", "base_url": "http://127.0.0.1:11434/v1",
		"api_key": "x", "models": []string{"m"},
	})
	if w.Code != 201 {
		t.Fatalf("self-hosted provider must not be SSRF-blocked: %d", w.Code)
	}
}

// 对抗 review：channel probe 端点的 SSRF——advanced_custom probe 到内网被拦；
// 任何 provider 打字面 metadata IP 被拦（IMDS 反射 oracle）。
func TestAdvancedCustomProbeSSRF(t *testing.T) {
	srv, _ := setupRelayServer(t)
	// advanced_custom probe 到内网 → 400（配置期校验）
	for _, badURL := range []string{"http://127.0.0.1:8080", "http://169.254.169.254", "http://10.1.2.3:9000"} {
		w, out := srv.doAdmin(t, "POST", "/api/admin/channels/probe", map[string]any{
			"api_key": "x", "base_url": badURL, "provider": "advanced_custom",
		})
		if w.Code != 400 {
			t.Fatalf("advanced probe %q = %d %v, want 400", badURL, w.Code, out)
		}
	}
	// 任何 provider 打字面 metadata IP → 400（恒拦 IMDS 反射 oracle）
	for _, prov := range []string{"openai", "ollama", ""} {
		w, out := srv.doAdmin(t, "POST", "/api/admin/channels/probe", map[string]any{
			"api_key": "x", "base_url": "http://169.254.169.254/latest", "provider": prov,
		})
		if w.Code != 400 {
			t.Fatalf("metadata probe provider=%q = %d %v, want 400", prov, w.Code, out)
		}
	}
	// 阿里云/Oracle metadata 变体
	for _, u := range []string{"http://100.100.100.200/x", "http://192.0.0.192/x"} {
		if w, _ := srv.doAdmin(t, "POST", "/api/admin/channels/probe", map[string]any{
			"api_key": "x", "base_url": u, "provider": "openai",
		}); w.Code != 400 {
			t.Fatalf("metadata variant %q not blocked: %d", u, w.Code)
		}
	}
}

// 对抗 review：upstream_path 配非 openai converter（会被静默丢弃）→ fail-loud 400。
func TestAdvancedCustomUpstreamPathConverterConflict(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('advanced_custom_allow_private', 'true')")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "conflict", "provider": "advanced_custom", "base_url": up.URL,
		"api_key": "x", "models": []string{"m"},
		"converter": "openai_chat_to_gemini_generate_content", "upstream_path": "/custom",
	})
	if w.Code != 400 {
		t.Fatalf("upstream_path + gemini converter = %d %v, want 400", w.Code, out)
	}
	// converter=none + upstream_path 合法
	if w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ok", "provider": "advanced_custom", "base_url": up.URL,
		"api_key": "x", "models": []string{"m"}, "converter": "none", "upstream_path": "/custom",
	}); w.Code != 201 {
		t.Fatalf("none + upstream_path = %d %v, want 201", w.Code, out)
	}
}

// 非法 converter → 400。
func TestAdvancedCustomInvalidConverter(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "badconv", "provider": "advanced_custom", "base_url": up.URL,
		"api_key": "x", "models": []string{"m"}, "converter": "nonsense_converter",
	})
	if w.Code != 400 {
		t.Fatalf("invalid converter = %d %v, want 400", w.Code, out)
	}
	// 入站方向 converter（非渠道 converter）也拒
	if w, _ := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "badconv2", "provider": "advanced_custom", "base_url": up.URL,
		"api_key": "x", "models": []string{"m"}, "converter": "anthropic_messages_to_openai_chat",
	}); w.Code != 400 {
		t.Fatalf("inbound-direction converter must be rejected as channel converter: %d", w.Code)
	}
}

// SSRF 拨号守卫：即使配置期放行（域名），拨号解析到内网 IP 也拦（DNS-rebinding 语义）。
// 用 base_url 指向一个解析到 127.0.0.1 的地址走拨号守卫路径。
func TestAdvancedCustomSSRFDialGuard(t *testing.T) {
	srv, _ := setupRelayServer(t)
	// allow-private=false（默认）：advanced_custom 渠道拨号到 127.0.0.1 被 Control 拦。
	// 用一个真实 localhost httptest 上游（其 URL 是 127.0.0.1），但因是 advanced_custom，
	// 配置期字面 IP 就拦——所以这里改用 allow-private 放过配置期、验证拨号仍成功（正常 localhost），
	// 再关掉验证拨号被拦。简化：直接验证 allow-private 开时 localhost advanced 渠道可用。
	db := srv.srv.db
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('advanced_custom_allow_private', 'true')")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	defer up.Close()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "adv-local", "provider": "advanced_custom", "base_url": up.URL,
		"api_key": "sk-x", "models": []string{"test-model"}, "converter": "none",
	})
	if w.Code != 201 {
		t.Fatalf("allow-private advanced create = %d %v", w.Code, out)
	}
	srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	apiKey, _ := seedUserWithCredit(t, srv, "adv-local@example.com", "10")
	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("allow-private localhost advanced = %d %s", w.Code, w.Body.String())
	}
	// 关掉 allow-private → 拨号守卫拦 localhost（clientFor 每请求按 allow-private 选客户端，
	// 无需快照失效——ssrfClientFor 读的是实时 ext_settings）。
	db.Exec("UPDATE ext_settings SET value='false' WHERE name='advanced_custom_allow_private'")
	w, _ = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 50,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code == 200 {
		t.Fatalf("dial guard must block localhost when allow-private off: %d", w.Code)
	}
}
