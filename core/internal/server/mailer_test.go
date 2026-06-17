package server

import "testing"

// settingsMailer：后台 ext_settings 的 SMTP 配置优先；未配回退 env mailer；配置解析（端口/SSL/from 默认）正确。
func TestSettingsMailerFromSettings(t *testing.T) {
	g, db := setupRelayServer(t) // 有 keyring；env mailer 为 nil

	// 未配 SMTP + env=nil → inactive
	m := &settingsMailer{s: g.srv, env: nil}
	if m.Active() {
		t.Fatal("no smtp settings + nil env → must be inactive")
	}
	if _, ok := m.smtpFromSettings(); ok {
		t.Fatal("smtpFromSettings ok=true with no smtp_host")
	}

	// 后台配置 SMTP（host 非空）→ active + 配置解析正确
	db.Exec("INSERT OR REPLACE INTO ext_settings (name,value) VALUES ('smtp_host','smtppro.zoho.com')")
	db.Exec("INSERT OR REPLACE INTO ext_settings (name,value) VALUES ('smtp_port','465')")
	db.Exec("INSERT OR REPLACE INTO ext_settings (name,value) VALUES ('smtp_ssl','true')")
	db.Exec("INSERT OR REPLACE INTO ext_settings (name,value) VALUES ('smtp_username','info@laiai.ai')")
	if !m.Active() {
		t.Fatal("smtp_host set → must be active")
	}
	cfg, ok := m.smtpFromSettings()
	if !ok || cfg.Host != "smtppro.zoho.com" || cfg.Port != 465 || !cfg.SSL || cfg.From != "info@laiai.ai" {
		t.Fatalf("cfg = %+v ok=%v, want zoho/465/ssl/from=username", cfg, ok)
	}

	// from 显式覆盖 username
	db.Exec("INSERT OR REPLACE INTO ext_settings (name,value) VALUES ('smtp_from','noreply@laiai.ai')")
	if cfg, _ := m.smtpFromSettings(); cfg.From != "noreply@laiai.ai" {
		t.Fatalf("from = %q, want explicit noreply@laiai.ai", cfg.From)
	}

	// port 留空 → 0（SendSMTP 内按 SSL 取默认）
	db.Exec("UPDATE ext_settings SET value='' WHERE name='smtp_port'")
	if cfg, _ := m.smtpFromSettings(); cfg.Port != 0 {
		t.Fatalf("empty port → %d, want 0 (SendSMTP defaults)", cfg.Port)
	}
}

// smtp_password 是 secret：admin GET 掩码回显，绝不下发明文。
func TestSMTPPasswordIsSecret(t *testing.T) {
	if !secretSettingKeys["smtp_password"] {
		t.Fatal("smtp_password must be a secret setting key (encrypted at rest + masked)")
	}
}
