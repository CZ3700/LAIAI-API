package server

import (
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
)

// P0-1：封禁用户的旧 session 令牌即时失效（账户级 + org-scoped 全 403），不等 TTL。
func TestBannedUserOldTokenRejected(t *testing.T) {
	srv, db := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "ban@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "ban@gmail.com", "password": "longenough1"})
	token := login["token"].(string)

	if w, _ := srv.do(t, "GET", "/api/keys", token, nil); w.Code != 200 {
		t.Fatalf("pre-ban /api/keys = %d, want 200", w.Code)
	}
	db.Exec(`UPDATE users SET status = 0 WHERE email_canonical = ?`, "ban@gmail.com")

	if w, _ := srv.do(t, "GET", "/api/keys", token, nil); w.Code != 403 {
		t.Fatalf("post-ban /api/keys = %d, want 403", w.Code)
	}
	if w, _ := srv.do(t, "GET", "/api/me", token, nil); w.Code != 403 {
		t.Fatalf("post-ban /api/me = %d, want 403 (banned user fully locked)", w.Code)
	}
}

// P0-1：组织被禁用后，旧令牌失去该 org 的数据访问（org-scoped 403），但账户级 /me 仍可用（用户本身 active）。
func TestOrgDisabledRevokesDataAccess(t *testing.T) {
	srv, db := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "od@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "od@gmail.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := uint64(login["org_id"].(float64))

	db.Exec(`UPDATE organizations SET status = 0 WHERE id = ?`, orgID)

	if w, _ := srv.do(t, "GET", "/api/balance", token, nil); w.Code != 403 {
		t.Fatalf("disabled-org /api/balance = %d, want 403", w.Code)
	}
	if w, _ := srv.do(t, "GET", "/api/me", token, nil); w.Code != 200 {
		t.Fatalf("disabled-org /api/me = %d, want 200 (user still active, can switch org)", w.Code)
	}
}

// P0-1：会话用户被移出 org 后，旧令牌失去该 org 数据访问。
func TestRevokedMembershipCannotUseOldToken(t *testing.T) {
	srv, db := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "rm@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "rm@gmail.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := uint64(login["org_id"].(float64))

	db.Exec(`DELETE FROM org_members WHERE org_id = ?`, orgID)

	if w, _ := srv.do(t, "GET", "/api/usage", token, nil); w.Code != 403 {
		t.Fatalf("removed-member /api/usage = %d, want 403", w.Code)
	}
}

// P0-1：per-member API Key 在用户被移除 / 组织禁用后，VerifyKey 立即拒绝（relay/task 全路径）。
func TestPerMemberKeyRejectedAfterMembershipRevoked(t *testing.T) {
	srv, db := setupRelayServer(t)
	apiKey, orgID := seedUserWithCredit(t, srv, "pmk@gmail.com", "10")

	if _, err := identity.VerifyKey(db, apiKey, time.Now()); err != nil {
		t.Fatalf("pre-revoke VerifyKey err = %v, want nil", err)
	}
	// 移除该 org 的成员关系（per-member key 绑定的成员不再存在于 org）
	db.Exec(`DELETE FROM org_members WHERE org_id = ?`, orgID)
	if _, err := identity.VerifyKey(db, apiKey, time.Now()); err != identity.ErrNotActiveMember {
		t.Fatalf("post-revoke VerifyKey err = %v, want ErrNotActiveMember", err)
	}

	// 另起一个 key，改测组织禁用路径
	apiKey2, orgID2 := seedUserWithCredit(t, srv, "pmk2@gmail.com", "10")
	db.Exec(`UPDATE organizations SET status = 0 WHERE id = ?`, orgID2)
	if _, err := identity.VerifyKey(db, apiKey2, time.Now()); err != identity.ErrNotActiveMember {
		t.Fatalf("disabled-org VerifyKey err = %v, want ErrNotActiveMember", err)
	}
}
