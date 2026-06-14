package server

import (
	"fmt"
	"testing"
)

// TestEntitlements 证明 P0-2 功能调配的 4 处强制门确实生效，且 admin API 的
// 整行覆盖 / 校验 / RBAC 语义正确。强制点是真实代码路径，不是镜像断言。
func TestEntitlements(t *testing.T) {
	srv, _ := setupRelayServer(t)

	// owner 用户 A：拿 session token + orgID + 一把 API Key。
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "ent@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "ent@example.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := login["org_id"].(float64)
	_, keyOut := srv.do(t, "POST", "/api/keys", token, map[string]any{"name": "k1"})
	apiKey := keyOut["key"].(string)

	entURL := fmt.Sprintf("/api/admin/orgs/%.0f/entitlements", orgID)
	// 整行覆盖：每次必须传全量字段（与前端表单一致）。
	setEnt := func(images, video bool, maxMembers, maxKeys int) int {
		w, _ := srv.doAdmin(t, "PUT", entURL, map[string]any{
			"feature_images": images, "feature_video": video,
			"max_members": maxMembers, "max_api_keys": maxKeys,
		})
		return w.Code
	}
	findOrg := func() map[string]any {
		_, list := srv.doAdmin(t, "GET", "/api/admin/orgs", nil)
		for _, it := range list["orgs"].([]any) {
			if m := it.(map[string]any); m["id"] == orgID {
				return m
			}
		}
		t.Fatalf("org %.0f not in admin list", orgID)
		return nil
	}

	// 1. 默认（无配置行）→ admin 列表回平台默认：全功能开、不限量。
	if o := findOrg(); o["feature_images"] != true || o["feature_video"] != true ||
		o["max_members"] != float64(0) || o["max_api_keys"] != float64(0) {
		t.Fatalf("default entitlements = %v", o)
	}

	// 2. 图像功能门：关 → relay 图像 403；开 → 不再是 403（放行到定价/渠道层）。
	if c := setEnt(false, true, 0, 0); c != 200 {
		t.Fatalf("set images-off = %d", c)
	}
	if w, _ := srv.do(t, "POST", "/v1/images/generations", apiKey,
		map[string]any{"model": "any", "prompt": "x"}); w.Code != 403 {
		t.Fatalf("images-off relay = %d, want 403", w.Code)
	}
	if c := setEnt(true, true, 0, 0); c != 200 {
		t.Fatalf("set images-on = %d", c)
	}
	if w, _ := srv.do(t, "POST", "/v1/images/generations", apiKey,
		map[string]any{"model": "any", "prompt": "x"}); w.Code == 403 {
		t.Fatalf("images-on relay still 403 (gate stuck closed)")
	}

	// 3. 视频功能门：关 → task 视频 403。
	if c := setEnt(true, false, 0, 0); c != 200 {
		t.Fatalf("set video-off = %d", c)
	}
	if w, _ := srv.do(t, "POST", "/v1/video/generations", apiKey,
		map[string]any{"model": "any", "content": []any{"x"}}); w.Code != 403 {
		t.Fatalf("video-off task = %d, want 403", w.Code)
	}

	// 4. API Key 上限：org 已有 1 把，封顶 1 → 再签发 403；放开 → 201。
	if c := setEnt(true, true, 0, 1); c != 200 {
		t.Fatalf("set maxkeys=1 = %d", c)
	}
	if w, _ := srv.do(t, "POST", "/api/keys", token, map[string]any{"name": "k2"}); w.Code != 403 {
		t.Fatalf("key over limit = %d, want 403", w.Code)
	}
	if c := setEnt(true, true, 0, 0); c != 200 {
		t.Fatalf("unset maxkeys = %d", c)
	}
	if w, _ := srv.do(t, "POST", "/api/keys", token, map[string]any{"name": "k2"}); w.Code != 201 {
		t.Fatalf("key after unlimit = %d, want 201", w.Code)
	}

	// 5. 成员上限：org owner 占 1，封顶 1 → 加成员 403；放开到 2 → 201。
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "member@example.com", "password": "longenough1"})
	if c := setEnt(true, true, 1, 0); c != 200 {
		t.Fatalf("set maxmembers=1 = %d", c)
	}
	if w, _ := srv.do(t, "POST", "/api/org/members", token,
		map[string]any{"email": "member@example.com", "role": "member"}); w.Code != 403 {
		t.Fatalf("member over limit = %d, want 403", w.Code)
	}
	if c := setEnt(true, true, 2, 0); c != 200 {
		t.Fatalf("set maxmembers=2 = %d", c)
	}
	if w, _ := srv.do(t, "POST", "/api/org/members", token,
		map[string]any{"email": "member@example.com", "role": "member"}); w.Code != 201 {
		t.Fatalf("member after raise = %d, want 201", w.Code)
	}

	// 6. 校验：负数上限 → 400。
	if w, _ := srv.doAdmin(t, "PUT", entURL, map[string]any{"max_members": -1}); w.Code != 400 {
		t.Fatalf("negative limit = %d, want 400", w.Code)
	}
	// 7. 不存在的 org → 404。
	if w, _ := srv.doAdmin(t, "PUT", "/api/admin/orgs/999999/entitlements",
		map[string]any{"feature_images": true}); w.Code != 404 {
		t.Fatalf("missing org = %d, want 404", w.Code)
	}
	// 8. RBAC：普通用户 session（role=user）改 entitlements → 403。
	if w, _ := srv.do(t, "PUT", entURL, token, map[string]any{"feature_images": false}); w.Code != 403 {
		t.Fatalf("non-admin entitlements = %d, want 403", w.Code)
	}

	// 9. 审计落账：最近一条 org_entitlements_set 存在。
	_, audit := srv.doAdmin(t, "GET", "/api/admin/audit-logs", nil)
	found := false
	for _, it := range audit["items"].([]any) {
		if it.(map[string]any)["action"] == "org_entitlements_set" {
			found = true
			break
		}
	}
	if !found {
		t.Error("no org_entitlements_set audit entry")
	}
}
