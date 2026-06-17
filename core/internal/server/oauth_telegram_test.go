package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signTelegram：测试侧扮演合法 Telegram 客户端，按规范产出 hash（与 verifyTelegramAuth 互验）。
func signTelegram(fields map[string]string, botToken string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+fields[k])
	}
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestTelegramLogin(t *testing.T) {
	srv, db := setupRelayServer(t)

	// 未配置 → 400
	if w, _ := srv.do(t, "POST", "/api/auth/telegram", "", map[string]any{"id": 1, "hash": "x", "auth_date": 1}); w.Code != 400 {
		t.Fatalf("unconfigured telegram = %d, want 400", w.Code)
	}

	botToken := "123456:ABC-DEF1234ghIkl"
	db.Exec("INSERT INTO ext_settings (name,value) VALUES ('telegram_bot_token',?),('telegram_bot_username','mybot')", botToken)

	authDate := time.Now().Unix()
	fields := map[string]string{
		"id": "555", "auth_date": strconv.FormatInt(authDate, 10),
		"first_name": "Cz", "username": "cz",
	}
	good := map[string]any{
		"id": 555, "first_name": "Cz", "username": "cz", "auth_date": authDate,
		"hash": signTelegram(fields, botToken),
	}
	// 合法签名 → 200 + session
	w, out := srv.do(t, "POST", "/api/auth/telegram", "", good)
	if w.Code != 200 || out["token"] == nil {
		t.Fatalf("valid telegram login = %d %v", w.Code, out)
	}
	// 合成身份建号
	var cnt int64
	db.Raw("SELECT COUNT(*) FROM users WHERE email_canonical = ?", "tg555@telegram.local").Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("synthetic user count = %d, want 1", cnt)
	}
	// 再次登录复用同一用户（不重复建号）
	srv.do(t, "POST", "/api/auth/telegram", "", good)
	db.Raw("SELECT COUNT(*) FROM users WHERE email_canonical = ?", "tg555@telegram.local").Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("after re-login user count = %d, want 1 (reuse)", cnt)
	}

	// 篡改 hash → 401
	bad := map[string]any{"id": 555, "first_name": "Cz", "username": "cz", "auth_date": authDate, "hash": "deadbeef"}
	if w, _ := srv.do(t, "POST", "/api/auth/telegram", "", bad); w.Code != 401 {
		t.Fatalf("tampered telegram = %d, want 401", w.Code)
	}
	// 篡改字段（id 改了但 hash 没变）→ 401（HMAC 覆盖 id）
	forged := map[string]any{"id": 999, "first_name": "Cz", "username": "cz", "auth_date": authDate, "hash": good["hash"]}
	if w, _ := srv.do(t, "POST", "/api/auth/telegram", "", forged); w.Code != 401 {
		t.Fatalf("forged id telegram = %d, want 401", w.Code)
	}
	// 过期授权（auth_date 2 小时前，但签名有效）→ 401
	oldDate := time.Now().Unix() - 7200
	oldFields := map[string]string{"id": "777", "auth_date": strconv.FormatInt(oldDate, 10), "first_name": "Old"}
	expired := map[string]any{"id": 777, "first_name": "Old", "auth_date": oldDate, "hash": signTelegram(oldFields, botToken)}
	if w, _ := srv.do(t, "POST", "/api/auth/telegram", "", expired); w.Code != 401 {
		t.Fatalf("expired telegram = %d, want 401", w.Code)
	}

	// 无任何姓名字段（仅 id+auth_date）→ 仍应建号成功（display_name 为空）
	noNameFields := map[string]string{"id": "888", "auth_date": strconv.FormatInt(authDate, 10)}
	noName := map[string]any{"id": 888, "auth_date": authDate, "hash": signTelegram(noNameFields, botToken)}
	if w, out := srv.do(t, "POST", "/api/auth/telegram", "", noName); w.Code != 200 || out["token"] == nil {
		t.Fatalf("telegram no-name login = %d %v, want 200", w.Code, out)
	}
}

// 合成域 @telegram.local 内部保留：公开注册不得占用（防 Telegram 账号预劫持）。
// 大小写/别名经 canonical 归一后同样命中。
func TestReservedSyntheticEmailRejected(t *testing.T) {
	srv, _ := setupRelayServer(t)
	for _, email := range []string{
		"tg555@telegram.local",
		"TG555@Telegram.Local",   // 大小写 → canonical 命中
		"tg555+x@telegram.local", // +alias → canonical 命中
	} {
		w, out := srv.do(t, "POST", "/api/auth/register", "", map[string]any{
			"email": email, "password": "supersecret123",
		})
		if w.Code != 400 {
			t.Fatalf("register %q = %d %v, want 400 (reserved domain)", email, w.Code, out)
		}
	}
	// 普通域不受影响
	if w, _ := srv.do(t, "POST", "/api/auth/register", "", map[string]any{
		"email": "real@example.com", "password": "supersecret123",
	}); w.Code != 201 {
		t.Fatalf("register normal email = %d, want 201", w.Code)
	}
}

// Discord / OIDC 配置后出现在 OAuth 提供方列表。
func TestOAuthProvidersDiscordOIDC(t *testing.T) {
	srv, db := setupRelayServer(t)
	// 默认空列表
	_, out := srv.do(t, "GET", "/api/auth/oauth", "", nil)
	if ps, _ := out["providers"].([]any); len(ps) != 0 {
		t.Fatalf("default providers = %v, want empty", ps)
	}
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES
		('discord_client_id','dcid'),('discord_client_secret','dcsec'),
		('oidc_client_id','oid'),('oidc_auth_url','https://idp/auth'),
		('oidc_token_url','https://idp/token'),('oidc_userinfo_url','https://idp/userinfo'),
		('telegram_bot_token','t'),('telegram_bot_username','mybot')`)
	_, out = srv.do(t, "GET", "/api/auth/oauth", "", nil)
	got := map[string]bool{}
	for _, p := range out["providers"].([]any) {
		got[p.(string)] = true
	}
	if !got["discord"] || !got["oidc"] {
		t.Fatalf("providers = %v, want discord+oidc", out["providers"])
	}
	if out["telegram_bot"] != "mybot" {
		t.Fatalf("telegram_bot = %v, want mybot", out["telegram_bot"])
	}
}

// 新 OAuth 设置键可保存（证明 oauth.go init() 已登记进 settingDefaults，否则被未知键拒绝）。
func TestOAuthSettingsSavable(t *testing.T) {
	srv, _ := setupRelayServer(t)
	w, out := srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]any{
		"discord_client_id":     "x",
		"oidc_client_id":        "y",
		"oidc_auth_url":         "https://idp/auth",
		"telegram_bot_token":    "t",
		"telegram_bot_username": "b",
	})
	if w.Code != 200 {
		t.Fatalf("save oauth settings = %d %v, want 200 (keys must be registered)", w.Code, out)
	}
	// 负向对照：未注册的键必须被拒（证明上面 200 是“键已登记”而非“来者不拒”）。
	w2, _ := srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]any{"definitely_not_a_real_key": "z"})
	if w2.Code == 200 {
		t.Fatalf("unknown settings key accepted (want rejected) = %d", w2.Code)
	}
}

// OIDC email_verified：必须显式为真才放行（防未验证邮箱接管账号）。缺省/false/非真 → 拒。
func TestOIDCEmailVerified(t *testing.T) {
	srv, _ := setupRelayServer(t)
	cases := []struct {
		body      string
		wantEmail string
		wantErr   bool
	}{
		{`{"email":"a@x.com","email_verified":true}`, "a@x.com", false},
		{`{"email":"a@x.com","email_verified":"true"}`, "a@x.com", false},
		{`{"email":"a@x.com","email_verified":"1"}`, "a@x.com", false},
		{`{"email":"a@x.com","email_verified":1}`, "a@x.com", false}, // JSON 数字 1 → float64
		{`{"email":"a@x.com","email_verified":0}`, "", true},         // JSON 数字 0 → 拒
		{`{"email":"a@x.com","email_verified":false}`, "", true},
		{`{"email":"a@x.com","email_verified":"false"}`, "", true},
		{`{"email":"a@x.com"}`, "", true},     // 缺省 → 拒（安全优先）
		{`{"email_verified":true}`, "", true}, // 无 email
	}
	for i, cse := range cases {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(cse.body))
		}))
		p := &oauthProvider{userURL: mock.URL}
		email, err := srv.srv.oauthFetchEmail(context.Background(), p, "oidc", "tok")
		mock.Close()
		if cse.wantErr {
			if err == nil {
				t.Fatalf("case %d (%s): want error, got email %q", i, cse.body, email)
			}
			continue
		}
		if err != nil || email != cse.wantEmail {
			t.Fatalf("case %d (%s): got %q err %v, want %q", i, cse.body, email, err, cse.wantEmail)
		}
	}
}
