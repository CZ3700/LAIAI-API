package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 镜像 migrations/0001_identity.sql（SQLite 方言）——与 identity 包测试同源
const testSchema = `
CREATE TABLE organizations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
    kind INTEGER NOT NULL DEFAULT 0, white_label INTEGER NOT NULL DEFAULT 0,
    group_name TEXT NOT NULL DEFAULT 'default', aff_code TEXT, inviter_org_id INTEGER NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 1,
    low_balance_alert_usd NUMERIC NOT NULL DEFAULT 0, low_balance_alerted_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE email_verifications (
    email_canonical TEXT PRIMARY KEY, code TEXT NOT NULL,
    expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE notify_settings (
    org_id INTEGER PRIMARY KEY,
    email_enabled INTEGER NOT NULL DEFAULT 0,
    webhook_url TEXT NOT NULL DEFAULT '', webhook_secret_enc TEXT NOT NULL DEFAULT '',
    events TEXT NOT NULL DEFAULT '', updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE notify_outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, event TEXT NOT NULL, payload TEXT NOT NULL,
    channel TEXT NOT NULL, target TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at DATETIME NOT NULL, sent_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE ext_settings (
    name TEXT PRIMARY KEY, value TEXT NOT NULL
);
CREATE TABLE audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL, ip TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL
);
CREATE TABLE group_ratios (
    name TEXT PRIMARY KEY, ratio NUMERIC NOT NULL DEFAULT 1
);
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL, email_canonical TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
    totp_secret_enc TEXT NOT NULL DEFAULT '', totp_enabled INTEGER NOT NULL DEFAULT 0, status INTEGER NOT NULL DEFAULT 1,
    role TEXT NOT NULL DEFAULT 'user', admin_remark TEXT NOT NULL DEFAULT '',
    email_verified INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE org_members (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
    role TEXT NOT NULL DEFAULT 'member',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, user_id)
);
CREATE TABLE org_invitations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, inviter_user_id INTEGER NOT NULL, invitee_user_id INTEGER NOT NULL,
    role TEXT NOT NULL DEFAULT 'member', created_at INTEGER NOT NULL,
    UNIQUE (org_id, invitee_user_id)
);
CREATE TABLE api_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, user_id INTEGER,
    name TEXT NOT NULL DEFAULT '', prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE, scopes TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 1, expires_at DATETIME,
    quota_limit NUMERIC NOT NULL DEFAULT 0, used_amount NUMERIC NOT NULL DEFAULT 0,
    model_limits TEXT NOT NULL DEFAULT '', allow_ips TEXT NOT NULL DEFAULT '',
    rpm_limit INTEGER NOT NULL DEFAULT 0, route_policy TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE org_entitlements (
    org_id INTEGER PRIMARY KEY,
    feature_images INTEGER NOT NULL DEFAULT 1, feature_video INTEGER NOT NULL DEFAULT 1,
    max_members INTEGER NOT NULL DEFAULT 0, max_api_keys INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE twofa_backup_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL, code_hash TEXT NOT NULL,
    used_at INTEGER, created_at INTEGER NOT NULL,
    UNIQUE (user_id, code_hash)
);
`

var testSecret = []byte("test-secret-0123456789abcdef-0123456789abcdef")

func setupServer(t *testing.T) (*gin999, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(testSchema).Error; err != nil {
		t.Fatalf("schema: %v", err)
	}
	// 测试基线：默认关邮箱验证（多数测试不传验证码直接注册）；验证相关测试各自 INSERT OR REPLACE 开。
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('email_verification_enabled', 'false')")
	engine, srv := NewWithServer(db, Options{AuthSecret: testSecret, AuthRateBurst: 100000, Mailer: &captureMailer{}})
	return &gin999{engine: engine, srv: srv}, db
}

// captureMailer：测试用，记录最后一封邮件正文（从中抠验证码）。
type captureMailer struct{ lastBody string }

func (m *captureMailer) Active() bool { return true }
func (m *captureMailer) Send(to, subject, body string) error {
	m.lastBody = body
	return nil
}

// 轻薄的请求辅助
type gin999 struct {
	engine http.Handler
	srv    *Server // 后台 job（对账等）测试用句柄
}

func (g *gin999) do(t *testing.T, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	g.engine.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// doRaw：GET 一个非 JSON（二进制/流）响应，返回原始 recorder（body 不解析）。产物代理端点测试用。
func (g *gin999) doRaw(t *testing.T, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	g.engine.ServeHTTP(w, req)
	return w
}

func TestHealthz(t *testing.T) {
	srv, _ := setupServer(t)
	w, out := srv.do(t, "GET", "/healthz", "", nil)
	if w.Code != 200 || out["status"] != "ok" {
		t.Fatalf("healthz = %d %v", w.Code, out)
	}
}

func TestFullAuthAndKeyFlow(t *testing.T) {
	srv, _ := setupServer(t)

	// 注册
	w, out := srv.do(t, "POST", "/api/auth/register", "", map[string]string{
		"email": "Grace@Gmail.com", "password": "longenough1",
	})
	if w.Code != 201 {
		t.Fatalf("register = %d %v", w.Code, out)
	}

	// 薅羊毛变体 → 409
	w, _ = srv.do(t, "POST", "/api/auth/register", "", map[string]string{
		"email": "g.race+bonus@googlemail.com", "password": "longenough1",
	})
	if w.Code != 409 {
		t.Fatalf("farming variant = %d, want 409", w.Code)
	}

	// 错密码 → 401（与不存在用户同错误）
	w, _ = srv.do(t, "POST", "/api/auth/login", "", map[string]string{
		"email": "grace@gmail.com", "password": "wrongwrong1",
	})
	if w.Code != 401 {
		t.Fatalf("wrong password = %d", w.Code)
	}
	w, _ = srv.do(t, "POST", "/api/auth/login", "", map[string]string{
		"email": "nobody@gmail.com", "password": "wrongwrong1",
	})
	if w.Code != 401 {
		t.Fatalf("unknown user = %d (must equal wrong-password code)", w.Code)
	}

	// 登录（alias 形态也能登录到同一账户）
	w, out = srv.do(t, "POST", "/api/auth/login", "", map[string]string{
		"email": "G.race+login@gmail.com", "password": "longenough1",
	})
	if w.Code != 200 {
		t.Fatalf("login = %d %v", w.Code, out)
	}
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatal("no token")
	}

	// 未带 token 访问受保护端点 → 401
	w, _ = srv.do(t, "GET", "/api/me", "", nil)
	if w.Code != 401 {
		t.Fatalf("unauthed /me = %d", w.Code)
	}

	// /me
	w, out = srv.do(t, "GET", "/api/me", token, nil)
	if w.Code != 200 || out["email"] != "Grace@Gmail.com" {
		t.Fatalf("/me = %d %v", w.Code, out)
	}

	// 签发 Key：明文只此一次
	w, out = srv.do(t, "POST", "/api/keys", token, map[string]any{"name": "prod"})
	if w.Code != 201 {
		t.Fatalf("create key = %d %v", w.Code, out)
	}
	plaintext, _ := out["key"].(string)
	keyID := out["id"]
	if len(plaintext) < 40 {
		t.Fatalf("key plaintext = %q", plaintext)
	}

	// 列表不回明文
	w, out = srv.do(t, "GET", "/api/keys", token, nil)
	if w.Code != 200 {
		t.Fatalf("list = %d", w.Code)
	}
	keysJSON, _ := json.Marshal(out)
	if bytes.Contains(keysJSON, []byte(plaintext)) {
		t.Fatal("plaintext key leaked in list response")
	}

	// 吊销
	w, _ = srv.do(t, "DELETE", fmt.Sprintf("/api/keys/%v", keyID), token, nil)
	if w.Code != 200 {
		t.Fatalf("revoke = %d", w.Code)
	}
}

func TestKeyOrgIsolation(t *testing.T) {
	srv, _ := setupServer(t)
	// 两个用户各自注册登录
	mkToken := func(email string) string {
		srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": email, "password": "longenough1"})
		_, out := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "longenough1"})
		return out["token"].(string)
	}
	tokenA := mkToken("a@example.com")
	tokenB := mkToken("b@example.com")

	_, out := srv.do(t, "POST", "/api/keys", tokenA, map[string]any{"name": "a-key"})
	keyID := out["id"]

	// B 不能看到也不能吊销 A 的 key（org 隔离红线）
	_, outB := srv.do(t, "GET", "/api/keys", tokenB, nil)
	if keys, _ := outB["keys"].([]any); len(keys) != 0 {
		t.Fatalf("org B sees org A keys: %v", keys)
	}
	w, _ := srv.do(t, "DELETE", fmt.Sprintf("/api/keys/%v", keyID), tokenB, nil)
	if w.Code != 404 {
		t.Fatalf("cross-org revoke = %d, want 404", w.Code)
	}
}

// 账户设置：改显示名 + 改密码（旧密码校验）。
func TestProfileAndPassword(t *testing.T) {
	srv, _ := setupServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "acct@example.com", "password": "origpass1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "acct@example.com", "password": "origpass1"})
	token := login["token"].(string)

	// 改显示名
	w, _ := srv.do(t, "PATCH", "/api/me", token, map[string]string{"display_name": "Alice"})
	if w.Code != 200 {
		t.Fatalf("patch me = %d", w.Code)
	}
	_, me := srv.do(t, "GET", "/api/me", token, nil)
	if me["display_name"] != "Alice" {
		t.Fatalf("display_name = %v", me["display_name"])
	}

	// 改密码：旧密码错 → 401
	w, _ = srv.do(t, "POST", "/api/me/password", token, map[string]string{"old_password": "wrongpass", "new_password": "newpass12"})
	if w.Code != 401 {
		t.Fatalf("wrong old password = %d, want 401", w.Code)
	}
	// 改密码：新密码太短 → 400
	w, _ = srv.do(t, "POST", "/api/me/password", token, map[string]string{"old_password": "origpass1", "new_password": "short"})
	if w.Code != 400 {
		t.Fatalf("short new password = %d, want 400", w.Code)
	}
	// 改密码：成功
	w, _ = srv.do(t, "POST", "/api/me/password", token, map[string]string{"old_password": "origpass1", "new_password": "newpass12"})
	if w.Code != 200 {
		t.Fatalf("change password = %d", w.Code)
	}
	// 旧密码登录失败，新密码登录成功
	w, _ = srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "acct@example.com", "password": "origpass1"})
	if w.Code == 200 {
		t.Fatal("old password should no longer work")
	}
	w, _ = srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "acct@example.com", "password": "newpass12"})
	if w.Code != 200 {
		t.Fatalf("new password login = %d", w.Code)
	}
}

// 密钥硬删除：活跃 key 不可删（需先吊销），吊销后可删。
func TestDeleteKey(t *testing.T) {
	srv, _ := setupServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "del@example.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "del@example.com", "password": "longenough1"})
	token := login["token"].(string)
	_, k := srv.do(t, "POST", "/api/keys", token, map[string]any{"name": "tmp"})
	_ = k
	_, list := srv.do(t, "GET", "/api/keys", token, nil)
	id := list["keys"].([]any)[0].(map[string]any)["id"]

	// 活跃 → 删除被拒
	w, _ := srv.do(t, "DELETE", fmt.Sprintf("/api/keys/%v/purge", id), token, nil)
	if w.Code != 400 {
		t.Fatalf("delete active key = %d, want 400", w.Code)
	}
	// 吊销后可删
	srv.do(t, "DELETE", fmt.Sprintf("/api/keys/%v", id), token, nil)
	w, _ = srv.do(t, "DELETE", fmt.Sprintf("/api/keys/%v/purge", id), token, nil)
	if w.Code != 200 {
		t.Fatalf("delete revoked key = %d, want 200", w.Code)
	}
	_, list = srv.do(t, "GET", "/api/keys", token, nil)
	if len(list["keys"].([]any)) != 0 {
		t.Fatal("key list should be empty after purge")
	}
}

// 邮箱验证：开启后注册必须带有效验证码（防薅羊毛）。
func TestEmailVerification(t *testing.T) {
	srv, db := setupServer(t)
	// 开启邮箱验证
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "admin@example.com", "password": "longenough1"})
	// 借 admin token 开关——server_test 的 setupServer 无 admin token，改用 DB 直接写设置
	db.Exec("INSERT OR REPLACE INTO ext_settings (name, value) VALUES ('email_verification_enabled', 'true')")

	// 不带验证码注册 → 400
	w, _ := srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "new@example.com", "password": "longenough1"})
	if w.Code != 400 {
		t.Fatalf("register without code = %d, want 400", w.Code)
	}
	// 发验证码
	w, _ = srv.do(t, "POST", "/api/auth/send-verification", "", map[string]string{"email": "new@example.com"})
	if w.Code != 200 {
		t.Fatalf("send-verification = %d", w.Code)
	}
	// 从 DB 取码（生产从邮件，测试从库）
	var rec struct{ Code string }
	db.Raw("SELECT code FROM email_verifications WHERE email_canonical = ?", "new@example.com").Scan(&rec)
	if len(rec.Code) != 6 {
		t.Fatalf("code = %q, want 6 digits", rec.Code)
	}
	// 错码 → 400
	w, _ = srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "new@example.com", "password": "longenough1", "verification_code": "000000"})
	if w.Code != 400 {
		t.Fatalf("wrong code register = %d, want 400", w.Code)
	}
	// 正确码 → 201
	w, _ = srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "new@example.com", "password": "longenough1", "verification_code": rec.Code})
	if w.Code != 201 {
		t.Fatalf("correct code register = %d, want 201", w.Code)
	}
	// 码用后即焚：再用同码 → 行已删
	var n int64
	db.Raw("SELECT COUNT(*) FROM email_verifications WHERE email_canonical = ?", "new@example.com").Scan(&n)
	if n != 0 {
		t.Fatal("verification code should be consumed after use")
	}
}
