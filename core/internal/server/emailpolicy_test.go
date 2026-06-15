package server

import (
	"strconv"
	"testing"
)

// 邮箱准入策略：白名单/黑名单/别名限制，含 gmail 归一与默认放行。
func TestCheckEmailDomainPolicy(t *testing.T) {
	srv, db := setupRelayServer(t)
	set := func(k, v string) { db.Exec(`INSERT OR REPLACE INTO ext_settings (name,value) VALUES (?,?)`, k, v) }

	// 默认：不限制 → 放行
	if err := srv.srv.checkEmailDomainPolicy("a@x.com"); err != nil {
		t.Fatalf("default allow = %v, want nil", err)
	}

	// 白名单：仅 company.com / gmail.com
	set("email_domain_whitelist", "company.com, gmail.com")
	if err := srv.srv.checkEmailDomainPolicy("u@company.com"); err != nil {
		t.Fatalf("whitelisted = %v, want nil", err)
	}
	if err := srv.srv.checkEmailDomainPolicy("u@evil.com"); err != errEmailDomainNotAllowed {
		t.Fatalf("non-whitelisted = %v, want errEmailDomainNotAllowed", err)
	}
	// gmail 别名/googlemail 归一后命中白名单 gmail.com
	if err := srv.srv.checkEmailDomainPolicy("u.s.e.r+tag@googlemail.com"); err != nil {
		t.Fatalf("googlemail canonical to gmail = %v, want nil (whitelisted)", err)
	}
	set("email_domain_whitelist", "")

	// 黑名单：挡 10minutemail.com
	set("email_domain_blocklist", "10minutemail.com")
	if err := srv.srv.checkEmailDomainPolicy("a@10minutemail.com"); err != errEmailDomainBlocked {
		t.Fatalf("blocked domain = %v, want errEmailDomainBlocked", err)
	}
	if err := srv.srv.checkEmailDomainPolicy("a@x.com"); err != nil {
		t.Fatalf("non-blocked = %v, want nil", err)
	}
	set("email_domain_blocklist", "")

	// 误填只含分隔符/空格的白名单（", ,"）必须按「不限制」处理，绝不锁死所有注册
	set("email_domain_whitelist", ", ,")
	if err := srv.srv.checkEmailDomainPolicy("anyone@anywhere.com"); err != nil {
		t.Fatalf("separator-only whitelist should NOT lock out all, got %v", err)
	}
	set("email_domain_whitelist", "")

	// 别名限制：拒带 + 的本地名
	set("email_alias_block", "true")
	if err := srv.srv.checkEmailDomainPolicy("a+promo@x.com"); err != errEmailAliasBlocked {
		t.Fatalf("alias = %v, want errEmailAliasBlocked", err)
	}
	if err := srv.srv.checkEmailDomainPolicy("a@x.com"); err != nil {
		t.Fatalf("no-alias = %v, want nil", err)
	}
}

// 端到端：注册端点按白名单拒绝非白名单域名。
func TestRegisterEmailDomainPolicy(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('email_domain_whitelist','company.com')`)

	// 非白名单 → 403（策略拒绝，与 oauth/telegram 同口径）
	if w, _ := srv.do(t, "POST", "/api/auth/register", "", map[string]string{
		"email": "x@gmail.com", "password": "longenough1",
	}); w.Code != 403 {
		t.Fatalf("non-whitelisted register = %d, want 403", w.Code)
	}
	// 白名单 → 201
	if w, _ := srv.do(t, "POST", "/api/auth/register", "", map[string]string{
		"email": "x@company.com", "password": "longenough1",
	}); w.Code != 201 {
		t.Fatalf("whitelisted register = %d, want 201", w.Code)
	}
}

// Telegram 登录在配了域名白名单时被一并关闭（合成域 telegram.local 不在白名单）。
func TestTelegramBlockedByWhitelist(t *testing.T) {
	srv, db := setupRelayServer(t)
	botToken := "123456:ABC-DEF1234ghIkl"
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('telegram_bot_token',?),('email_domain_whitelist','company.com')`, botToken)
	authDate := srv.srv.now().Unix()
	fields := map[string]string{"id": "555", "auth_date": strconv.FormatInt(authDate, 10), "first_name": "Cz"}
	good := map[string]any{"id": 555, "first_name": "Cz", "auth_date": authDate, "hash": signTelegram(fields, botToken)}
	// 合法签名但白名单生效 → 403
	if w, _ := srv.do(t, "POST", "/api/auth/telegram", "", good); w.Code != 403 {
		t.Fatalf("telegram under whitelist = %d, want 403", w.Code)
	}
}
