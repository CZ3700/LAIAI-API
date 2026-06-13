// Package mailer：邮件发送抽象。未配置 SMTP 时退化为日志（开发态/框架先行），
// 配置后走标准 SMTP（STARTTLS + PLAIN auth）。验证码等内容永不返回前端，
// 开发态从服务器日志获取。
package mailer

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
)

type Mailer interface {
	Send(to, subject, body string) error
	// Active 返回是否真实发送（false=仅日志，前端据此决定是否走邮箱验证流）。
	Active() bool
}

// Config：SMTP 配置（从 env 读，全空=不启用真实发送）。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// New 按配置选择实现：Host 为空 → LogMailer；否则 SMTPMailer。
func New(cfg Config, log *slog.Logger) Mailer {
	if strings.TrimSpace(cfg.Host) == "" {
		return &logMailer{log: log}
	}
	if cfg.From == "" {
		cfg.From = cfg.Username
	}
	return &smtpMailer{cfg: cfg, log: log}
}

// logMailer：开发态/未配置 SMTP，把邮件打到服务器日志，不真实发送。
type logMailer struct{ log *slog.Logger }

func (m *logMailer) Active() bool { return false }
func (m *logMailer) Send(to, subject, body string) error {
	m.log.Info("EMAIL (not sent — SMTP not configured; check this log for the code)",
		"to", to, "subject", subject, "body", body)
	return nil
}

// smtpMailer：标准 SMTP（STARTTLS + PLAIN）。
type smtpMailer struct {
	cfg Config
	log *slog.Logger
}

func (m *smtpMailer) Active() bool { return true }

func (m *smtpMailer) Send(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)
	msg := buildMessage(m.cfg.From, to, subject, body)
	auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)

	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.cfg.Host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := c.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func buildMessage(from, to, subject, body string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.String()
}
