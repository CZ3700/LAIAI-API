package server

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// doAdminRaw：以 superadmin(X-Admin-Token) 发请求，返回原始 recorder（CSV 等非 JSON 响应用）。
func (g *gin999) doAdminRaw(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Admin-Token", testAdminToken)
	w := httptest.NewRecorder()
	g.engine.ServeHTTP(w, req)
	return w
}

// 运营备注：设置 → 出现在用户清单；超长拒绝；不外露给用户自身 /api/me。
func TestAdminSetUserRemark(t *testing.T) {
	srv, _ := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "rmk@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "rmk@gmail.com", "password": "longenough1"})
	uid := int(login["org_id"].(float64)) // 个人 org id 与 user id 在测试里都从 1 起；用 list 找真实 uid

	// 找到该用户 id
	_, list := srv.doAdmin(t, "GET", "/api/admin/users", nil)
	var userID float64
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		if m["email"] == "rmk@gmail.com" {
			userID = m["id"].(float64)
		}
	}
	if userID == 0 {
		t.Fatalf("user not found in list (org_id was %d)", uid)
	}
	path := "/api/admin/users/" + strconv.Itoa(int(userID)) + "/remark"

	// 设置备注 → 200
	if w, out := srv.doAdmin(t, "POST", path, map[string]any{"remark": "VIP 大客户，慎封"}); w.Code != 200 {
		t.Fatalf("set remark = %d %v", w.Code, out)
	}
	// 清单回显备注
	_, list = srv.doAdmin(t, "GET", "/api/admin/users", nil)
	found := false
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		if m["email"] == "rmk@gmail.com" && m["admin_remark"] == "VIP 大客户，慎封" {
			found = true
		}
	}
	if !found {
		t.Fatal("remark not reflected in user list")
	}
	// 超长（>500 runes）→ 400
	long := strings.Repeat("字", 501)
	if w, _ := srv.doAdmin(t, "POST", path, map[string]any{"remark": long}); w.Code != 400 {
		t.Fatalf("overlong remark = %d, want 400", w.Code)
	}
	// 不外露：用户自己的 /api/me 不含备注
	_, me := srv.do(t, "GET", "/api/me", login["token"].(string), nil)
	if _, leaked := me["admin_remark"]; leaked {
		t.Fatal("admin_remark leaked via /api/me (snake)")
	}
	if _, leaked := me["AdminRemark"]; leaked {
		t.Fatal("admin_remark leaked via /api/me (struct field name)")
	}
}

// 按用户聚合用量 + CSV 导出：同口径，按费用降序。
func TestAdminUsageByUser(t *testing.T) {
	srv, db := setupRelayServer(t)
	// 直插调用记录：user 7 烧 $3（2 次，1 成功），user 9 烧 $1（1 次成功）
	db.Exec(`INSERT INTO invocations (org_id,user_id,api_key_id,request_id,model,status,prompt_tokens,completion_tokens,cost_usd,created_at)
		VALUES (7,7,1,'r1','gpt','success',100,50,'2.0',CURRENT_TIMESTAMP),
		       (7,7,1,'r2','gpt','upstream_error',10,0,'1.0',CURRENT_TIMESTAMP),
		       (9,9,1,'r3','gpt','success',200,80,'1.0',CURRENT_TIMESTAMP)`)

	_, out := srv.doAdmin(t, "GET", "/api/admin/usage/by-user?days=7", nil)
	items := out["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("usage-by-user rows = %d, want 2", len(items))
	}
	// 按 cost 降序：user 7 ($3) 在前
	top := items[0].(map[string]any)
	if int(top["user_id"].(float64)) != 7 || top["cost_usd"] != "3" {
		t.Fatalf("top row = %v, want user 7 cost 3", top)
	}
	if int(top["calls"].(float64)) != 2 || int(top["succeeded"].(float64)) != 1 {
		t.Fatalf("user 7 calls/succeeded = %v/%v, want 2/1", top["calls"], top["succeeded"])
	}

	// CSV 导出：header + 2 行，content-type csv
	w := srv.doAdminRaw(t, "GET", "/api/admin/usage/by-user/export?days=7")
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("export content-type = %q, want text/csv", ct)
	}
	body := w.Body.String()
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 3 { // header + 2 users
		t.Fatalf("csv lines = %d, want 3\n%s", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], "user_id,email,calls") {
		t.Fatalf("csv header = %q", lines[0])
	}
}

// 空结果：items 必须是 []（非 null），否则前端 .length 崩；CSV 仅表头。
func TestAdminUsageByUserEmpty(t *testing.T) {
	srv, _ := setupRelayServer(t)
	_, out := srv.doAdmin(t, "GET", "/api/admin/usage/by-user?days=7", nil)
	items, ok := out["items"].([]any)
	if !ok {
		t.Fatalf("items not a JSON array (likely null): %v", out["items"])
	}
	if len(items) != 0 {
		t.Fatalf("empty usage items = %d, want 0", len(items))
	}
	w := srv.doAdminRaw(t, "GET", "/api/admin/usage/by-user/export?days=7")
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "user_id,email") {
		t.Fatalf("empty export = %q, want header only", w.Body.String())
	}
}
