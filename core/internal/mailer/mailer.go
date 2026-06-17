// Package mailer：邮件发送抽象。未配置 SMTP 时退化为日志（开发态/框架先行），
// 配置后走标准 SMTP（STARTTLS + PLAIN auth）。验证码等内容永不返回前端，
// 开发态从服务器日志获取。
package mailer

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type Mailer interface {
	Send(to, subject, body string) error
	// Active 返回是否真实发送（false=仅日志，前端据此决定是否走邮箱验证流）。
	Active() bool
}

// Config：SMTP 配置（env 或 admin 后台 ext_settings 热配；Host 空=不启用真实发送）。
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	SSL      bool // true = 隐式 TLS（端口 465，连接即 TLS）；false = 明文连接 + STARTTLS（端口 587）
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

func (m *smtpMailer) Send(to, subject, body string) error { return SendSMTP(m.cfg, to, subject, body) }

// SendSMTP 用 cfg 发一封邮件。供静态 smtpMailer 与 server 层「settings 热配 mailer」共用。
// cfg.SSL=true → 隐式 TLS（端口 465，tls.Dial）；否则明文 Dial + 若服务器支持则 STARTTLS（端口 587）。
// 端口默认：SSL 时 465，否则 587。
func SendSMTP(cfg Config, to, subject, body string) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return fmt.Errorf("smtp: host not configured")
	}
	if cfg.From == "" {
		cfg.From = cfg.Username
	}
	// 防邮件头注入：to/subject/from 都会原样进邮件头，含 CR/LF 可注入额外头（Bcc/伪造 From）。
	// 共享原语在此 fail-closed，覆盖 stdlib Rcpt 不校验的 subject。
	if strings.ContainsAny(to+subject+cfg.From, "\r\n") {
		return fmt.Errorf("smtp: refusing to send — CR/LF in recipient, subject, or from")
	}
	port := cfg.Port
	if port == 0 {
		if cfg.SSL {
			port = 465
		} else {
			port = 587
		}
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port)) // JoinHostPort 正确括住 IPv6 字面量（vet 安全）
	msg := buildMessage(cfg.From, to, subject, body)
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)

	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 15 * time.Second} // 拨号超时：防黑洞 host 挂死 goroutine（对抗评审）
	var c *smtp.Client
	var err error
	if cfg.SSL {
		// 隐式 TLS：连接即 TLS（端口 465 SMTPS）。
		conn, derr := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
		if derr != nil {
			return fmt.Errorf("smtp tls dial: %w", derr)
		}
		if c, err = smtp.NewClient(conn, cfg.Host); err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp client: %w", err)
		}
	} else {
		conn, derr := dialer.Dial("tcp", addr)
		if derr != nil {
			return fmt.Errorf("smtp dial: %w", derr)
		}
		if c, err = smtp.NewClient(conn, cfg.Host); err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp client: %w", err)
		}
		// 🚨 强制 STARTTLS：服务器不通告则拒发——防 STARTTLS-stripping MITM 把凭证/正文降级到明文。
		// 需明文 SMTP（如本机 dev relay）应改用 SSL 端口或专用 relay；本路径不接受明文投递。
		if ok, _ := c.Extension("STARTTLS"); !ok {
			_ = c.Close()
			return fmt.Errorf("smtp: server does not advertise STARTTLS (use SSL for implicit TLS on 465)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	defer c.Close()
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := c.Mail(cfg.From); err != nil {
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
