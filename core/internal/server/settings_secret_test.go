package server

import (
	"strings"
	"testing"
)

// P1-3：secret 设置落库即密文（不存明文），内部读解密回明文，admin GET 只见掩码。
func TestSettingsSecretEncryptedAtRestAndDecryptedInternally(t *testing.T) {
	srv, db := setupRelayServer(t)
	const plain = "sk_live_realsecret_abcdef123456"

	if rec, _ := srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{
		"stripe_secret_key": plain, "site_name": "MyGW",
	}); rec.Code != 200 {
		t.Fatalf("PUT settings = %d", rec.Code)
	}

	// 落库值必须是密文（带前缀），绝不是明文
	var raw string
	db.Raw(`SELECT value FROM ext_settings WHERE name = 'stripe_secret_key'`).Scan(&raw)
	if raw == plain || raw == "" {
		t.Fatalf("stored value = %q, want ciphertext (not plaintext, not empty)", raw)
	}
	if !strings.HasPrefix(raw, settingEncPrefix) {
		t.Fatalf("stored value %q missing enc prefix", raw)
	}
	if strings.Contains(raw, "realsecret") {
		t.Fatalf("stored value leaks plaintext: %q", raw)
	}
	// 内部读（引擎消费口径）解密回明文
	if got := srv.srv.getSetting("stripe_secret_key"); got != plain {
		t.Fatalf("getSetting = %q, want decrypted plaintext", got)
	}
	// 非 secret 键不受影响
	if got := srv.srv.getSetting("site_name"); got != "MyGW" {
		t.Fatalf("site_name = %q, want MyGW", got)
	}
}

// P1-3：GET /api/admin/settings 对已配置 secret 只回掩码，未配置回空，绝不回显明文/密文。
func TestSettingsGetMasksSecrets(t *testing.T) {
	srv, _ := setupRelayServer(t)
	const plain = "whsec_supersecret_value_987654"
	srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"stripe_webhook_secret": plain})

	_, out := srv.doAdmin(t, "GET", "/api/admin/settings", nil)
	settings, ok := out["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings shape = %T", out["settings"])
	}
	if got := settings["stripe_webhook_secret"]; got != secretMask {
		t.Fatalf("masked secret = %v, want %q (no plaintext echo)", got, secretMask)
	}
	// 未配置的 secret 回空
	if got := settings["github_client_secret"]; got != "" {
		t.Fatalf("unset secret = %v, want empty", got)
	}
	// 非 secret 键正常回显
	if settings["site_name"] == "" {
		t.Fatal("site_name should be non-empty")
	}
}

// P1-3 write-only：PUT 收到掩码/空＝保留原 secret，不被清空；传新明文则更新。
func TestSettingsSecretWriteOnlyPreserve(t *testing.T) {
	srv, _ := setupRelayServer(t)
	const orig = "sk_live_original_value_111111"
	srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"stripe_secret_key": orig})

	// 回传掩码 → 保留
	srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"stripe_secret_key": secretMask})
	if got := srv.srv.getSetting("stripe_secret_key"); got != orig {
		t.Fatalf("after mask PUT = %q, want preserved %q", got, orig)
	}
	// 回传空 → 保留
	srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"stripe_secret_key": ""})
	if got := srv.srv.getSetting("stripe_secret_key"); got != orig {
		t.Fatalf("after empty PUT = %q, want preserved %q", got, orig)
	}
	// 传新明文 → 更新
	const updated = "sk_live_rotated_value_222222"
	srv.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"stripe_secret_key": updated})
	if got := srv.srv.getSetting("stripe_secret_key"); got != updated {
		t.Fatalf("after rotate PUT = %q, want %q", got, updated)
	}
}

// P1-3 兼容：历史明文（无加密前缀）仍可被内部读原样返回（不破坏既有部署）。
func TestSettingsLegacyPlaintextStillReadable(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec(`INSERT INTO ext_settings (name, value) VALUES ('epay_key', 'legacy_plain_key')`)
	if got := srv.srv.getSetting("epay_key"); got != "legacy_plain_key" {
		t.Fatalf("legacy plaintext getSetting = %q, want legacy_plain_key", got)
	}
}
