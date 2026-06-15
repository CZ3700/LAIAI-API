package server

import (
	"fmt"
	"testing"
)

// BUG-H3：团队成员管理的 actor-vs-target 等级守卫。admin 不能移除 owner/平级 admin、
// 不能授 admin（横向提权）；owner 可管理任何人；成员可自我离队。
func TestTeamRoleHierarchyGuard(t *testing.T) {
	srv, _ := setupRelayServer(t)
	ownerTok := registerAndLogin(t, srv, "h3owner@example.com")
	registerAndLogin(t, srv, "h3alice@example.com")
	registerAndLogin(t, srv, "h3carol@example.com")
	registerAndLogin(t, srv, "h3bob@example.com")
	registerAndLogin(t, srv, "h3eve@example.com")

	_, out := srv.do(t, "POST", "/api/orgs", ownerTok, map[string]string{"name": "H3 Team"})
	teamID := out["org_id"].(float64)
	_, sw := srv.do(t, "POST", "/api/auth/switch", ownerTok, map[string]any{"org_id": teamID})
	ownerTeam := sw["token"].(string)
	_, me := srv.do(t, "GET", "/api/me", ownerTok, nil)
	ownerID := me["user_id"].(float64)

	add := func(tok, email, role string) (int, float64) {
		w, o := srv.do(t, "POST", "/api/org/members", tok, map[string]string{"email": email, "role": role})
		id := 0.0
		if v, ok := o["user_id"].(float64); ok {
			id = v
		}
		return w.Code, id
	}
	// owner 加 alice=admin, carol=admin, bob=member
	if c, _ := add(ownerTeam, "h3alice@example.com", "admin"); c != 201 {
		t.Fatalf("owner add admin alice = %d", c)
	}
	_, carolID := add(ownerTeam, "h3carol@example.com", "admin")
	_, bobID := add(ownerTeam, "h3bob@example.com", "member")

	aliceTok := registerAndLoginExisting(t, srv, "h3alice@example.com")
	_, asw := srv.do(t, "POST", "/api/auth/switch", aliceTok, map[string]any{"org_id": teamID})
	aliceTeam := asw["token"].(string)
	_, am := srv.do(t, "GET", "/api/me", aliceTok, nil)
	aliceID := am["user_id"].(float64)

	// 1. admin 不能授 admin（横向提权）→ 403
	if c, _ := add(aliceTeam, "h3eve@example.com", "admin"); c != 403 {
		t.Fatalf("admin granting admin = %d, want 403", c)
	}
	// 2. admin 可加 member → 201
	if c, _ := add(aliceTeam, "h3eve@example.com", "member"); c != 201 {
		t.Fatalf("admin adding member = %d, want 201", c)
	}
	del := func(tok string, id float64) int {
		w, _ := srv.do(t, "DELETE", fmt.Sprintf("/api/org/members/%.0f", id), tok, nil)
		return w.Code
	}
	// 3. admin 不能移除 owner → 403
	if c := del(aliceTeam, ownerID); c != 403 {
		t.Fatalf("admin removing owner = %d, want 403", c)
	}
	// 4. admin 不能移除平级 admin → 403
	if c := del(aliceTeam, carolID); c != 403 {
		t.Fatalf("admin removing peer admin = %d, want 403", c)
	}
	// 5. admin 可移除 member → 200
	if c := del(aliceTeam, bobID); c != 200 {
		t.Fatalf("admin removing member = %d, want 200", c)
	}
	// 6. admin 可自我离队 → 200
	if c := del(aliceTeam, aliceID); c != 200 {
		t.Fatalf("admin self-leave = %d, want 200", c)
	}
	// 7. owner 可移除 admin → 200
	if c := del(ownerTeam, carolID); c != 200 {
		t.Fatalf("owner removing admin = %d, want 200", c)
	}
}
