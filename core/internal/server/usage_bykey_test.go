package server

import (
	"testing"
	"time"
)

// 仪表盘「密钥排行榜」数据源：GET /api/usage/by-key 按 api_key_id 聚合，org 隔离强制。
func TestUsageByKey(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "bykey@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := me["org_id"].(float64)
	insertInvocation(t, db, orgID, "bk1", "success", "openai", 0.02, time.Now())
	insertInvocation(t, db, orgID, "bk2", "success", "openai", 0.03, time.Now())

	w, out := srv.do(t, "GET", "/api/usage/by-key?days=30", tok, nil)
	if w.Code != 200 {
		t.Fatalf("by-key = %d %v", w.Code, out)
	}
	items, ok := out["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("by-key items = %v", out["items"])
	}
	first := items[0].(map[string]any)
	if first["calls"].(float64) != 2 {
		t.Fatalf("calls = %v, want 2", first["calls"])
	}
	// insertInvocation 每条 prompt=100 / completion=50 → 两条合计 prompt 200 / completion 100
	if first["prompt_tokens"].(float64) != 200 || first["completion_tokens"].(float64) != 100 {
		t.Fatalf("tokens = %v/%v, want 200/100", first["prompt_tokens"], first["completion_tokens"])
	}

	// org 隔离（红线 5）：另一 org 看不到本 org 的聚合
	tok2 := registerAndLogin(t, srv, "bykey2@example.com")
	_, out2 := srv.do(t, "GET", "/api/usage/by-key?days=30", tok2, nil)
	if items2, _ := out2["items"].([]any); len(items2) != 0 {
		t.Fatalf("org isolation breach: %v", out2["items"])
	}
}
