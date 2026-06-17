package server

import (
	"strconv"

	"github.com/laiai/laiai-next/core/internal/mailer"
)

// settingsMailer：邮件发送优先用 ext_settings 里的 SMTP 配置（admin 后台热配，改了即生效、无需重启或改 env），
// 缺失则回退到构造期注入的 mailer（env 来源，向后兼容）。
// secret（smtp_password）经 keyring 信封加密落库、getSetting 内部解密——明文绝不回显前端（adminGetSettings 掩码）。
type settingsMailer struct {
	s   *Server
	env mailer.Mailer // 回退：env 构造的 mailer（可能为 nil）
}

// smtpFromSettings 从 ext_settings 读 SMTP 配置；smtp_host 为空 → ok=false（未在后台配置）。
func (m *settingsMailer) smtpFromSettings() (mailer.Config, bool) {
	host := m.s.getSetting("smtp_host")
	if host == "" {
		return mailer.Config{}, false
	}
	port := 0 // 0 → SendSMTP 按 SSL 取 465/587 默认
	if p := m.s.getSetting("smtp_port"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			port = n
		}
	}
	user := m.s.getSetting("smtp_username")
	from := m.s.getSetting("smtp_from")
	if from == "" {
		from = user
	}
	return mailer.Config{
		Host:     host,
		Port:     port,
		Username: user,
		Password: m.s.getSetting("smtp_password"), // keyring 解密后的明文
		From:     from,
		SSL:      m.s.getBoolSetting("smtp_ssl"),
	}, true
}

// Active：后台配了 SMTP（smtp_host 非空）→ true；否则回退 env mailer（nil → false）。
// verification 流据此决定是否 fail-closed（验证开启却无真实投递 → 503）。
func (m *settingsMailer) Active() bool {
	if _, ok := m.smtpFromSettings(); ok {
		return true
	}
	return m.env != nil && m.env.Active()
}

// Send：优先后台 SMTP；否则回退 env mailer；env 亦为 nil → no-op（语义同 logMailer；调用方以 Active() 门控）。
func (m *settingsMailer) Send(to, subject, body string) error {
	if cfg, ok := m.smtpFromSettings(); ok {
		return mailer.SendSMTP(cfg, to, subject, body)
	}
	if m.env != nil {
		return m.env.Send(to, subject, body)
	}
	return nil
}
