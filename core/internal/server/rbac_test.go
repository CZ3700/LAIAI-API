package server

import (
	"fmt"
	"strings"
	"testing"
)

// admin 与 superadmin 权限分级（对标 NewAPI）：运营级 vs 资金/系统/定价级 + 等级守卫。
func TestRBACTierSplit(t *testing.T) {
	srv, db := setupRelayServer(t)
	mk := func(email string) (string, uint64) {
		srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": email, "password": "longenough1"})
		_, l := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "longenough1"})
		_, me := srv.do(t, "GET", "/api/me", l["token"].(string), nil)
		return l["token"].(string), uint64(me["user_id"].(float64))
	}
	setRole := func(uid uint64, role string) { db.Exec("UPDATE users SET role = ? WHERE id = ?", role, uid) }
	manage := func(uid uint64) string { return fmt.Sprintf("/api/admin/users/%d/manage", uid) }

	adminTok, adminUID := mk("tier-admin@ex.com")
	suTok, suUID := mk("tier-su@ex.com")
	_, victimUID := mk("tier-victim@ex.com")
	setRole(adminUID, "admin")
	setRole(suUID, "superadmin")

	// admin 级（运营）：admin 可访问
	for _, p := range []string{"/api/admin/channels", "/api/admin/model-prices", "/api/admin/overview"} {
		if w, _ := srv.do(t, "GET", p, adminTok, nil); w.Code != 200 {
			t.Fatalf("admin GET %s = %d, want 200", p, w.Code)
		}
	}
	// superadmin 级：admin 一律 403（资金/系统/定价）
	for _, p := range []string{"/api/admin/settings", "/api/admin/group-ratios", "/api/admin/payment-channels", "/api/admin/payment-config"} {
		if w, _ := srv.do(t, "GET", p, adminTok, nil); w.Code != 403 {
			t.Fatalf("admin GET %s = %d, want 403 (superadmin-only)", p, w.Code)
		}
	}
	// 授信（动钱）：admin 403
	if w, _ := srv.do(t, "POST", "/api/admin/credits", adminTok,
		map[string]any{"org_id": 1, "amount_usd": "1", "reason": "x", "idempotency_key": "k1"}); w.Code != 403 {
		t.Fatalf("admin credits = %d, want 403", w.Code)
	}
	// superadmin 可访问 superadmin 级
	if w, _ := srv.do(t, "GET", "/api/admin/settings", suTok, nil); w.Code != 200 {
		t.Fatalf("superadmin settings = %d, want 200", w.Code)
	}

	// 等级守卫：admin 能封普通用户，封不动同级 admin / 更高 superadmin
	if w, _ := srv.do(t, "POST", manage(victimUID), adminTok, map[string]any{"action": "ban"}); w.Code != 200 {
		t.Fatalf("admin ban common user = %d, want 200", w.Code)
	}
	if w, _ := srv.do(t, "POST", manage(suUID), adminTok, map[string]any{"action": "ban"}); w.Code != 403 {
		t.Fatalf("admin ban superadmin = %d, want 403", w.Code)
	}
	_, admin2UID := mk("tier-admin2@ex.com")
	setRole(admin2UID, "admin")
	if w, _ := srv.do(t, "POST", manage(admin2UID), adminTok, map[string]any{"action": "ban"}); w.Code != 403 {
		t.Fatalf("admin ban peer admin = %d, want 403", w.Code)
	}
}

func TestRBAC(t *testing.T) {
	srv, db := setupRelayServer(t)

	mkUser := func(email string) (token string, uid uint64) {
		srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": email, "password": "longenough1"})
		_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "longenough1"})
		token = login["token"].(string)
		_, me := srv.do(t, "GET", "/api/me", token, nil)
		uid = uint64(me["user_id"].(float64))
		if me["role"] != "user" {
			t.Fatalf("new user role = %v, want user", me["role"])
		}
		return
	}
	setRole := func(uid uint64, role string) {
		if err := db.Exec("UPDATE users SET role = ? WHERE id = ?", role, uid).Error; err != nil {
			t.Fatal(err)
		}
	}
	roleURL := func(uid uint64) string { return fmt.Sprintf("/api/admin/users/%d/role", uid) }

	adminTok, adminUID := mkUser("admin@gmail.com")
	plainTok, plainUID := mkUser("plain@gmail.com")

	// 1. role=user（默认）→ 有身份但权限不足 → 403
	if w, _ := srv.do(t, "GET", "/api/admin/channels", plainTok, nil); w.Code != 403 {
		t.Fatalf("user-role admin access = %d, want 403", w.Code)
	}
	// 1b. 完全无身份（无 token 无 session）→ 401
	if w, _ := srv.do(t, "GET", "/api/admin/channels", "", nil); w.Code != 401 {
		t.Fatalf("no-identity admin access = %d, want 401", w.Code)
	}

	// 2. 升为 admin → 可访问后台
	setRole(adminUID, "admin")
	if w, _ := srv.do(t, "GET", "/api/admin/channels", adminTok, nil); w.Code != 200 {
		t.Fatalf("admin-role channels = %d, want 200", w.Code)
	}

	// 3. admin 不能授角色（仅 superadmin）→ 403
	if w, _ := srv.do(t, "POST", roleURL(plainUID), adminTok, map[string]any{"role": "admin"}); w.Code != 403 {
		t.Fatalf("admin set-role = %d, want 403", w.Code)
	}

	// 4. root-token（视同 superadmin）授角色成功
	if w, _ := srv.doAdmin(t, "POST", roleURL(plainUID), map[string]any{"role": "superadmin"}); w.Code != 200 {
		t.Fatalf("root set-role = %d, want 200", w.Code)
	}

	// 5. plain 现在是 superadmin → 可授角色（实名 actor）
	if w, _ := srv.do(t, "POST", roleURL(adminUID), plainTok, map[string]any{"role": "support"}); w.Code != 200 {
		t.Fatalf("superadmin set-role = %d, want 200", w.Code)
	}

	// 6. 自我保护：superadmin 不能改自己的角色 → 400
	if w, _ := srv.do(t, "POST", roleURL(plainUID), plainTok, map[string]any{"role": "user"}); w.Code != 400 {
		t.Fatalf("self role change = %d, want 400", w.Code)
	}

	// 7. 非法角色 → 400
	if w, _ := srv.do(t, "POST", roleURL(adminUID), plainTok, map[string]any{"role": "godmode"}); w.Code != 400 {
		t.Fatalf("invalid role = %d, want 400", w.Code)
	}

	// 8. support 角色（rank=1 < admin=2）→ 后台拒绝 403（曾被授成 admin，第5步降到 support）
	if w, _ := srv.do(t, "GET", "/api/admin/channels", adminTok, nil); w.Code != 403 {
		t.Fatalf("support-role admin access = %d, want 403", w.Code)
	}

	// 9. 审计记到人：最近一条 user_role_set 的 actor 含具体 user#，而非泛化 "admin"
	_, audit := srv.doAdmin(t, "GET", "/api/admin/audit-logs", nil)
	actor := ""
	for _, it := range audit["items"].([]any) {
		if m := it.(map[string]any); m["action"] == "user_role_set" {
			actor, _ = m["actor"].(string)
			break
		}
	}
	if !strings.Contains(actor, "user#") {
		t.Errorf("audit actor = %q, want identity-based (contains user#)", actor)
	}
}
