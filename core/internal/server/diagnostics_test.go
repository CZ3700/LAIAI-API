package server

// Diagnostics handler 验证层（§4.8）：auth 门 + 输入校验 + SSRF 配置期拒（私网 base_url 在拨号前 400）。
// 核心探测/红线 7 日志断言在 relay 包单测（那里可注入 mock 上游 + 缓冲 logger）。

import (
	"net/http"
	"testing"
)

func TestDiagnosticsHandlerValidation(t *testing.T) {
	srv, _ := setupRelayServer(t)
	token, _ := seedUserSession(t, srv, "diag@example.com")

	// 未登录 → 401
	if w, _ := srv.do(t, "POST", "/api/diagnostics/check", "", map[string]any{"provider": "openai", "api_key": "sk-x"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth = %d, want 401", w.Code)
	}
	// provider/key 必填 → 400
	if w, _ := srv.do(t, "POST", "/api/diagnostics/check", token, map[string]any{"provider": "openai"}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing key = %d, want 400", w.Code)
	}
	if w, _ := srv.do(t, "POST", "/api/diagnostics/check", token, map[string]any{"api_key": "sk-x"}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing provider = %d, want 400", w.Code)
	}
	// custom base_url 非 https → 400（拨号前）
	if w, _ := srv.do(t, "POST", "/api/diagnostics/check", token, map[string]any{"provider": "openai", "api_key": "sk-x", "base_url": "http://api.openai.com/v1"}); w.Code != http.StatusBadRequest {
		t.Fatalf("http base_url = %d, want 400", w.Code)
	}
	// SSRF：私网/metadata base_url → 400（拨号前拒，绝不探测内网）
	for _, bad := range []string{"https://169.254.169.254/v1", "https://10.0.0.1/v1", "https://127.0.0.1/v1"} {
		if w, _ := srv.do(t, "POST", "/api/diagnostics/check", token, map[string]any{"provider": "openai", "api_key": "sk-x", "base_url": bad}); w.Code != http.StatusBadRequest {
			t.Fatalf("private base_url %q = %d, want 400", bad, w.Code)
		}
	}
	// 对抗 review 硬化：非标端口 → 400（灭任意端口扫描）；标准 443 放行
	if w, _ := srv.do(t, "POST", "/api/diagnostics/check", token, map[string]any{"provider": "openai", "api_key": "sk-x", "base_url": "https://api.example.com:8443/v1"}); w.Code != http.StatusBadRequest {
		t.Fatalf("non-standard port = %d, want 400", w.Code)
	}
	// 非白名单 provider → 200 + status error（CheckKey 立即拒，不拨号，无网络）
	w, out := srv.do(t, "POST", "/api/diagnostics/check", token, map[string]any{"provider": "advanced_custom", "api_key": "sk-x"})
	if w.Code != http.StatusOK || out["status"] != "error" {
		t.Fatalf("non-whitelist provider = %d %v, want 200 error", w.Code, out)
	}
	// 响应绝不含被测 key（红线 7 的 handler 侧兜底断言）
	if body := w.Body.String(); contains(body, "sk-x") {
		t.Fatalf("RED LINE 7: key echoed in response: %s", body)
	}
}

// per-user 诊断配额（对抗 review）：突发耗尽后 429（以 userID 为键，抗 IP 轮换）。
func TestDiagnosticsPerUserQuota(t *testing.T) {
	srv, _ := setupRelayServer(t)
	token, _ := seedUserSession(t, srv, "diagquota@example.com")
	// 非白名单 provider 通过校验+配额检查后由 CheckKey 立即返回（不拨号，无网络）——用它快速耗配额。
	body := map[string]any{"provider": "advanced_custom", "api_key": "sk-x"}
	got429 := false
	for i := 0; i < 25; i++ { // 配额突发 20，第 21 次起应 429
		w, _ := srv.do(t, "POST", "/api/diagnostics/check", token, body)
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("per-user diagnostics quota never triggered 429 within 25 calls")
	}
}

// admin 丰富指纹端点（§4.8 v1.1）：门控（非 admin 401）+ 输入校验 + 红线 7（key 不回显）。
func TestAdminDiagnosticsGating(t *testing.T) {
	srv, _ := setupRelayServer(t)
	token, _ := seedUserSession(t, srv, "diagadmin@example.com")

	// 普通用户（会话令牌，非 admin）→ 401/403（admin 门）
	if w, _ := srv.do(t, "POST", "/api/admin/diagnostics/check", token, map[string]any{"provider": "openai", "api_key": "sk-x"}); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("non-admin = %d, want 401/403", w.Code)
	}
	// admin：非白名单 provider → 200 status error（不拨号）；响应绝不含 key（红线 7）
	w, out := srv.doAdmin(t, "POST", "/api/admin/diagnostics/check", map[string]any{"provider": "advanced_custom", "api_key": "sk-secret-xyz"})
	if w.Code != http.StatusOK || out["status"] != "error" {
		t.Fatalf("admin non-whitelist = %d %v, want 200 error", w.Code, out)
	}
	if contains(w.Body.String(), "sk-secret-xyz") {
		t.Fatalf("RED LINE 7: key echoed in admin diagnostics response")
	}
	// admin：metadata base_url → 400（IMDS 反射 oracle 恒拦）
	if w, _ := srv.doAdmin(t, "POST", "/api/admin/diagnostics/check", map[string]any{"provider": "openai", "api_key": "sk-x", "base_url": "https://169.254.169.254/v1"}); w.Code != http.StatusBadRequest {
		t.Fatalf("metadata base_url = %d, want 400", w.Code)
	}
	// admin：缺 provider/key → 400
	if w, _ := srv.doAdmin(t, "POST", "/api/admin/diagnostics/check", map[string]any{"provider": "openai"}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing key = %d, want 400", w.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
