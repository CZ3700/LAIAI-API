package server

import (
	"fmt"
	"strings"
	"testing"
)

// 注册 + 登录，返回 session token
func registerAndLogin(t *testing.T, srv *gin999, email string) string {
	t.Helper()
	w, out := srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": email, "password": "longenough1"})
	if w.Code != 201 {
		t.Fatalf("register %s = %d %v", email, w.Code, out)
	}
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "longenough1"})
	return login["token"].(string)
}

func TestTeamLifecycle(t *testing.T) {
	srv, _ := setupRelayServer(t)
	ownerTok := registerAndLogin(t, srv, "owner@example.com")
	registerAndLogin(t, srv, "mate@example.com") // 成员需已注册

	// 建团队
	w, out := srv.do(t, "POST", "/api/orgs", ownerTok, map[string]string{"name": "Acme Team"})
	if w.Code != 201 {
		t.Fatalf("create org = %d %v", w.Code, out)
	}
	teamID := out["org_id"].(float64)

	// 我的 org 列表：个人 + 团队，角色正确
	w, out = srv.do(t, "GET", "/api/orgs", ownerTok, nil)
	if w.Code != 200 {
		t.Fatalf("list orgs = %d", w.Code)
	}
	orgs := out["orgs"].([]any)
	if len(orgs) != 2 {
		t.Fatalf("orgs = %d, want 2 (personal + team)", len(orgs))
	}

	// 切到团队 org
	w, out = srv.do(t, "POST", "/api/auth/switch", ownerTok, map[string]any{"org_id": teamID})
	if w.Code != 200 {
		t.Fatalf("switch = %d %v", w.Code, out)
	}
	teamTok := out["token"].(string)

	// 按邮箱加成员（alias 形态也能找到账号——canonical 查找）
	w, out = srv.do(t, "POST", "/api/org/members", teamTok, map[string]string{
		"email": "MATE+work@example.com", "role": "member",
	})
	if w.Code != 201 {
		t.Fatalf("add member = %d %v", w.Code, out)
	}
	mateID := out["user_id"].(float64)

	// 重复添加 → 409；不存在的邮箱 → 404；owner 角色不可经此 API 指派 → 400
	w, _ = srv.do(t, "POST", "/api/org/members", teamTok, map[string]string{"email": "mate@example.com", "role": "member"})
	if w.Code != 409 {
		t.Fatalf("duplicate add = %d, want 409", w.Code)
	}
	w, _ = srv.do(t, "POST", "/api/org/members", teamTok, map[string]string{"email": "ghost@example.com", "role": "member"})
	if w.Code != 404 {
		t.Fatalf("ghost add = %d, want 404", w.Code)
	}
	w, _ = srv.do(t, "POST", "/api/org/members", teamTok, map[string]string{"email": "mate@example.com", "role": "owner"})
	if w.Code != 400 {
		t.Fatalf("owner-role add = %d, want 400", w.Code)
	}

	// 成员列表 2 人
	w, out = srv.do(t, "GET", "/api/org/members", teamTok, nil)
	if w.Code != 200 || len(out["members"].([]any)) != 2 {
		t.Fatalf("members = %d %v", w.Code, out)
	}

	// member 角色不能管理成员（403）；但能看成员列表
	mateTok := registerAndLoginExisting(t, srv, "mate@example.com")
	_, sw := srv.do(t, "POST", "/api/auth/switch", mateTok, map[string]any{"org_id": teamID})
	mateTeamTok := sw["token"].(string)
	w, _ = srv.do(t, "POST", "/api/org/members", mateTeamTok, map[string]string{"email": "owner@example.com", "role": "admin"})
	if w.Code != 403 {
		t.Fatalf("member adding member = %d, want 403", w.Code)
	}
	w, _ = srv.do(t, "GET", "/api/org/members", mateTeamTok, nil)
	if w.Code != 200 {
		t.Fatalf("member viewing members = %d, want 200", w.Code)
	}

	// 非成员不能切入别人的 org
	strangerTok := registerAndLogin(t, srv, "stranger@example.com")
	w, _ = srv.do(t, "POST", "/api/auth/switch", strangerTok, map[string]any{"org_id": teamID})
	if w.Code != 403 {
		t.Fatalf("stranger switch = %d, want 403", w.Code)
	}

	// 末位 owner 保护：owner 移除自己 → 409
	var ownerID float64
	_, me := srv.do(t, "GET", "/api/me", ownerTok, nil)
	ownerID = me["user_id"].(float64)
	w, _ = srv.do(t, "DELETE", fmt.Sprintf("/api/org/members/%.0f", ownerID), teamTok, nil)
	if w.Code != 409 {
		t.Fatalf("remove last owner = %d, want 409", w.Code)
	}

	// 移除普通成员 → 成员的团队 token 对成员列表立即失效（角色实时查库）
	w, _ = srv.do(t, "DELETE", fmt.Sprintf("/api/org/members/%.0f", mateID), teamTok, nil)
	if w.Code != 200 {
		t.Fatalf("remove member = %d", w.Code)
	}
	w, _ = srv.do(t, "GET", "/api/org/members", mateTeamTok, nil)
	if w.Code != 403 {
		t.Fatalf("removed member access = %d, want 403 (revocation must be immediate)", w.Code)
	}
}

func registerAndLoginExisting(t *testing.T, srv *gin999, email string) string {
	t.Helper()
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "longenough1"})
	return login["token"].(string)
}

// 团队 org 的 Key 与计费隔离：团队 Key 扣团队余额，不动个人余额
func TestTeamKeysAndBillingIsolation(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)

	ownerTok := registerAndLogin(t, srv, "team-bill@example.com")
	_, created := srv.do(t, "POST", "/api/orgs", ownerTok, map[string]string{"name": "Bill Co"})
	teamID := created["org_id"].(float64)
	_, sw := srv.do(t, "POST", "/api/auth/switch", ownerTok, map[string]any{"org_id": teamID})
	teamTok := sw["token"].(string)

	// 团队 org 授信 + 团队 Key
	w, out := srv.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
		"org_id": teamID, "amount_usd": "10", "reason": "team grant", "idempotency_key": "team-grant-1",
	})
	if w.Code != 200 {
		t.Fatalf("grant = %d %v", w.Code, out)
	}
	_, keyOut := srv.do(t, "POST", "/api/keys", teamTok, map[string]any{"name": "team key"})
	teamKey := keyOut["key"].(string)

	w, _ = srv.do(t, "POST", "/v1/chat/completions", teamKey, map[string]any{
		"model": "test-model", "max_tokens": 100,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("team call = %d", w.Code)
	}
	// 团队账户被精确扣费；个人 org 无账户变动
	var posted string
	db.Raw("SELECT posted_balance FROM ledger_accounts WHERE org_id = ?", uint64(teamID)).Scan(&posted)
	if !strings.HasPrefix(posted, "9.995") {
		t.Fatalf("team balance = %s, want 9.995", posted)
	}
}

func TestModelsListEndpoint(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1, 1)
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL) // test-model 有活跃渠道
	// 定价但无渠道的模型不应出现
	srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "orphan-model", "input_usd_per_1m": "1", "output_usd_per_1m": "1",
	})
	apiKey, _ := seedUserWithCredit(t, srv, "models@example.com", "1")

	w, out := srv.do(t, "GET", "/v1/models", apiKey, nil)
	if w.Code != 200 || out["object"] != "list" {
		t.Fatalf("models = %d %v", w.Code, out)
	}
	data := out["data"].([]any)
	var ids []string
	for _, d := range data {
		ids = append(ids, d.(map[string]any)["id"].(string))
	}
	joined := strings.Join(ids, ",")
	if !strings.Contains(joined, "test-model") {
		t.Fatalf("test-model missing: %v", ids)
	}
	if strings.Contains(joined, "orphan-model") {
		t.Fatalf("orphan-model (no channel) must be hidden: %v", ids)
	}
	// 无 Key → 401
	w, _ = srv.do(t, "GET", "/v1/models", "", nil)
	if w.Code != 401 {
		t.Fatalf("unauthed models = %d", w.Code)
	}
}
