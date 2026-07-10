package server

// Notify 统一层 e2e（设计 §4.9）：订阅 API 权限/校验 + webhook secret 不明文回显 + SSRF 拒私网。

import (
	"testing"

	"github.com/laiai/laiai-next/core/internal/notify"
)

func TestNotifySettingsAPI(t *testing.T) {
	srv, db := setupRelayServer(t)
	_, orgID := seedUserWithCredit(t, srv, "notify@example.com", "")
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "notify@example.com", "password": "longenough1"})
	token := login["token"].(string)

	// 初始：空默认
	w, out := srv.do(t, "GET", "/api/org/notify-settings", token, nil)
	if w.Code != 200 || out["webhook_configured"] != false {
		t.Fatalf("initial = %d %v", w.Code, out)
	}

	// 配置 webhook + 事件（owner 有权）
	w, out = srv.do(t, "PUT", "/api/org/notify-settings", token, map[string]any{
		"email_enabled":  true,
		"webhook_url":    "https://hooks.example.com/laiai",
		"webhook_secret": "whsec_supersecret",
		"events":         []string{"task.completed", "task.failed", "bogus.event"},
	})
	if w.Code != 200 {
		t.Fatalf("put = %d %v", w.Code, out)
	}
	// 未知事件被白名单过滤
	events, _ := out["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %v, want 2 (bogus 过滤)", out["events"])
	}
	if out["webhook_configured"] != true {
		t.Fatal("webhook_configured should be true after secret set")
	}

	// GET 绝不回显明文 secret（红线 7）
	w, out = srv.do(t, "GET", "/api/org/notify-settings", token, nil)
	if _, leaked := out["webhook_secret"]; leaked {
		t.Fatal("RED LINE 7: webhook_secret must never be returned")
	}
	if out["webhook_configured"] != true || out["webhook_url"] != "https://hooks.example.com/laiai" {
		t.Fatalf("get after config = %v", out)
	}

	// 落库 secret 是密文（信封加密），非明文
	st := &notify.Settings{}
	db.Where("org_id = ?", uint64(orgID)).First(st)
	if st.WebhookSecretEnc == "" || st.WebhookSecretEnc == "whsec_supersecret" {
		t.Fatalf("secret not envelope-encrypted: %q", st.WebhookSecretEnc)
	}

	// SSRF：私网/非 https webhook 被拒
	w, _ = srv.do(t, "PUT", "/api/org/notify-settings", token, map[string]any{
		"webhook_url": "http://169.254.169.254/latest/meta-data", "webhook_secret": "s",
		"events": []string{"task.completed"},
	})
	if w.Code != 400 {
		t.Fatalf("metadata URL = %d, want 400 (SSRF block)", w.Code)
	}
	w, _ = srv.do(t, "PUT", "/api/org/notify-settings", token, map[string]any{
		"webhook_url": "https://192.168.1.1/hook", "webhook_secret": "s",
		"events": []string{"task.completed"},
	})
	if w.Code != 400 {
		t.Fatalf("private IP URL = %d, want 400 (SSRF block)", w.Code)
	}
	// http（公网）被拒——出站 webhook 强制 https（对抗 review 修）
	w, _ = srv.do(t, "PUT", "/api/org/notify-settings", token, map[string]any{
		"webhook_url": "http://collector.example.com/hook", "webhook_secret": "s",
		"events": []string{"task.completed"},
	})
	if w.Code != 400 {
		t.Fatalf("public http URL = %d, want 400 (https-only)", w.Code)
	}
	// 配 webhook 但无 secret 被拒（对抗 review 修：防静默 dead-letter）——先清掉已配的 secret
	w, _ = srv.do(t, "PUT", "/api/org/notify-settings", token, map[string]any{
		"webhook_url": "", "events": []string{"task.completed"}, // 清 url 会连带清 secret
	})
	if w.Code != 200 {
		t.Fatalf("clear webhook = %d", w.Code)
	}
	w, _ = srv.do(t, "PUT", "/api/org/notify-settings", token, map[string]any{
		"webhook_url": "https://hooks.example.com/x", // 无 secret
		"events":      []string{"task.completed"},
	})
	if w.Code != 400 {
		t.Fatalf("webhook without secret = %d, want 400", w.Code)
	}
}

func TestNotifySettingsRequiresManager(t *testing.T) {
	srv, db := setupRelayServer(t)
	// owner org
	_, orgID := seedUserWithCredit(t, srv, "nowner@example.com", "")
	// 第二个用户，直接播种为该 org 的 member（免去完整邀请流），switch 到该 org 拿受限 token
	memberTok := registerAndLogin(t, srv, "nmember@example.com")
	_, me := srv.do(t, "GET", "/api/me", memberTok, nil)
	memberUID := uint64(me["user_id"].(float64))
	if err := db.Exec(`INSERT INTO org_members (org_id, user_id, role, created_at) VALUES (?, ?, 'member', 0)`,
		uint64(orgID), memberUID).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	_, sw := srv.do(t, "POST", "/api/auth/switch", memberTok, map[string]any{"org_id": orgID})
	scoped, ok := sw["token"].(string)
	if !ok {
		t.Fatalf("switch failed: %v", sw)
	}

	// member 无权改通知设置
	w, _ := srv.do(t, "PUT", "/api/org/notify-settings", scoped, map[string]any{
		"email_enabled": true, "events": []string{"task.completed"},
	})
	if w.Code != 403 {
		t.Fatalf("member put = %d, want 403", w.Code)
	}
}
