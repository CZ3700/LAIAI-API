package server

import (
	"strings"
	"testing"
	"time"
)

// 用户自助 CSV 导出（Latency/Usage 页「Export CSV」）：返回本 org 调用明细，org 隔离强制。
func TestUsageExportCSV(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "export@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := me["org_id"].(float64)
	insertInvocation(t, db, orgID, "rexp1", "success", "openai", 0.02, time.Now())

	w, _ := srv.do(t, "GET", "/api/usage/export?days=30", tok, nil)
	if w.Code != 200 {
		t.Fatalf("export = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content-type = %q, want text/csv", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"request_id", "duration_ms", "rexp1", "gpt-4o"} {
		if !strings.Contains(body, want) {
			t.Fatalf("csv missing %q:\n%s", want, body)
		}
	}

	// 另一 org 看不到本 org 的记录（红线 5 隔离）
	tok2 := registerAndLogin(t, srv, "export2@example.com")
	w2, _ := srv.do(t, "GET", "/api/usage/export?days=30", tok2, nil)
	if strings.Contains(w2.Body.String(), "rexp1") {
		t.Fatalf("org isolation breach: other org sees rexp1")
	}
}
