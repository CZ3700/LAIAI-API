package server

// 生产边缘 IP 信任链回归（指令 P1-2）：gin 默认信任一切 X-Forwarded-For——直连 core 的
// 请求可伪造客户端 IP 绕过 keyGate IP 白名单/限流/审计口径。收窄后必须满足：
//   1. 不可信来源（公网直连）带伪造 XFF → 一律按 RemoteAddr 判定，白名单绕不过；
//   2. 可信反代（私网默认段）设置的 XFF → 正常还原真实客户端 IP（Caddy 拓扑不回退）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doVideoSubmitFrom(t *testing.T, srv *gin999, apiKey, remoteAddr, xff string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": "x"}},
	})
	req := httptest.NewRequest("POST", "/v1/video/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// 伪造 XFF 不得绕过 IP 白名单：请求源 192.0.2.1（公网，不可信）声称自己是白名单 IP → 仍 403。
func TestForgedXFFDoesNotBypassIPWhitelist(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	token, orgID := seedUserSession(t, srv, "xff-forge@example.com")
	grantCredit(t, srv, orgID, "10", "seed-xff-forge")
	apiKey := issueControlledKey(t, srv, token, map[string]any{"allow_ips": "1.2.3.4"})

	w, out := doVideoSubmitFrom(t, srv, apiKey, "192.0.2.1:5555", "1.2.3.4")
	if w.Code != http.StatusForbidden {
		t.Fatalf("forged XFF submit = %d %v, want 403 (XFF from untrusted source must be ignored)", w.Code, out)
	}
	if ark.submits.Load() != 0 {
		t.Fatal("forged request must not reach upstream")
	}
}

// 可信反代设置的 XFF 正常生效：源 10.0.0.5（私网默认可信段，即 Caddy 拓扑）带真实客户端 IP → 放行。
func TestTrustedProxyXFFHonored(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	token, orgID := seedUserSession(t, srv, "xff-trusted@example.com")
	grantCredit(t, srv, orgID, "10", "seed-xff-trusted")
	apiKey := issueControlledKey(t, srv, token, map[string]any{"allow_ips": "203.0.113.9"})

	w, out := doVideoSubmitFrom(t, srv, apiKey, "10.0.0.5:9999", "203.0.113.9")
	if w.Code != http.StatusAccepted {
		t.Fatalf("trusted-proxy submit = %d %v, want 202 (XFF from trusted proxy must be honored)", w.Code, out)
	}
}
