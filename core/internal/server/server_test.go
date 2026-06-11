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
    status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL, email_canonical TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
    totp_secret_enc TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE org_members (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
    role TEXT NOT NULL DEFAULT 'member',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, user_id)
);
CREATE TABLE api_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, user_id INTEGER,
    name TEXT NOT NULL DEFAULT '', prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE, scopes TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 1, expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
	return &gin999{engine: New(db, Options{AuthSecret: testSecret})}, db
}

// 轻薄的请求辅助
type gin999 struct{ engine http.Handler }

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
