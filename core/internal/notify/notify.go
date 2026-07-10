// Package notify：统一通知层 v1（设计 §4.9，2026-07-11）——事件→通道路由 + outbox at-least-once。
//
// 形态：Emit(事件) = 查 org 订阅 → 按启用通道写 notify_outbox 行（best-effort，绝不影响调用方
// 资金/业务语义）；ProcessOnce = leader 定时扫 pending 到期行发送，指数退避 1m/5m/30m/2h/12h，
// attempts≥6 → dead（admin 观测）。at-least-once：接收方按 payload 里的 id 幂等去重（文档化契约）。
//
// 红线 7：payload 元数据-only（task_id/model/status/cost/时间——绝无 prompt/产物内容/key）；
// webhook secret 信封加密落库、绝不明文回显；投递日志只记 outbox id + 状态。
// SSRF：出站 webhook 恒走注入的 guarded client（与 BYOK 同纪律），URL 创建期已强制 https。
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"gorm.io/gorm"
)

// 事件枚举（对外 API 契约，只增不改）。
const (
	EventTaskCompleted = "task.completed"
	EventTaskFailed    = "task.failed"
	EventBalanceLow    = "balance.low"
)

// KnownEvents：写入侧白名单（settings API 校验用）。
var KnownEvents = map[string]bool{EventTaskCompleted: true, EventTaskFailed: true, EventBalanceLow: true}

// 通道与 outbox 状态。
const (
	ChannelEmail   = "email"
	ChannelWebhook = "webhook"

	StatusPending = "pending"
	StatusSent    = "sent"
	StatusDead    = "dead"

	maxAttempts = 6
)

// backoffSteps：第 n 次失败后的重试间隔（指数退避，设计 §4.9）。
var backoffSteps = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour}

// Settings（0046 notify_settings）：每 org 一行的订阅配置。
type Settings struct {
	OrgID            uint64    `gorm:"primaryKey;column:org_id"`
	EmailEnabled     bool      `gorm:"column:email_enabled"`
	WebhookURL       string    `gorm:"column:webhook_url"`
	WebhookSecretEnc string    `gorm:"column:webhook_secret_enc"`
	Events           string    `gorm:"column:events"` // JSON 数组
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (Settings) TableName() string { return "notify_settings" }

// EventEnabled：events JSON 数组是否含该事件。
func (s *Settings) EventEnabled(event string) bool {
	var evs []string
	if json.Unmarshal([]byte(s.Events), &evs) != nil {
		return false
	}
	for _, e := range evs {
		if e == event {
			return true
		}
	}
	return false
}

// Outbox（0046 notify_outbox）：at-least-once 投递行。
type Outbox struct {
	ID            uint64     `gorm:"primaryKey"`
	OrgID         uint64     `gorm:"column:org_id"`
	Event         string     `gorm:"column:event"`
	Payload       string     `gorm:"column:payload"`
	Channel       string     `gorm:"column:channel"`
	Target        string     `gorm:"column:target"`
	Status        string     `gorm:"column:status"`
	Attempts      int        `gorm:"column:attempts"`
	NextAttemptAt time.Time  `gorm:"column:next_attempt_at"`
	SentAt        *time.Time `gorm:"column:sent_at"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime:milli"`
}

func (Outbox) TableName() string { return "notify_outbox" }

// Dispatcher：事件分发 + outbox 投递。所有依赖显式注入（可测性）。
type Dispatcher struct {
	DB      *gorm.DB
	Keyring *crypto.Keyring
	Log     *slog.Logger
	// Mail：邮件发送（server 注入 settingsMailer.Send）；nil = 邮件通道整体不可用。
	Mail func(to, subject, body string) error
	// MailActive：邮件通道当前是否可用（SMTP 已配）。nil 视为 false。
	MailActive func() bool
	// HTTP：出站 webhook 客户端——必须是 guarded client（SSRF 纪律，调用方注入）。
	HTTP *http.Client
	Now  func() time.Time
}

// Emit：向 org 发出一个事件。查订阅 → 按启用通道写 outbox（best-effort：任何失败只记日志，
// 绝不影响调用方业务/资金语义）。payload 必须已是元数据-only（红线 7，调用方纪律）。
func (d *Dispatcher) Emit(orgID uint64, event string, payload map[string]any) {
	if d == nil || d.DB == nil {
		return
	}
	st := &Settings{}
	if err := d.DB.Where("org_id = ?", orgID).First(st).Error; err != nil {
		return // 未配置订阅 = 静默（绝大多数 org）
	}
	if !st.EventEnabled(event) {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		d.Log.Warn("notify payload marshal failed", "event", event, "err", err)
		return
	}
	now := d.Now()
	var rows []Outbox
	if st.EmailEnabled && d.MailActive != nil && d.MailActive() {
		if emails := d.ownerEmails(orgID); len(emails) > 0 {
			rows = append(rows, Outbox{OrgID: orgID, Event: event, Payload: string(body),
				Channel: ChannelEmail, Target: strings.Join(emails, ","), Status: StatusPending, NextAttemptAt: now})
		}
	}
	if st.WebhookURL != "" {
		rows = append(rows, Outbox{OrgID: orgID, Event: event, Payload: string(body),
			Channel: ChannelWebhook, Target: st.WebhookURL, Status: StatusPending, NextAttemptAt: now})
	}
	if len(rows) == 0 {
		return
	}
	if err := d.DB.Create(&rows).Error; err != nil {
		d.Log.Warn("notify outbox insert failed", "event", event, "err", err)
	}
}

// ownerEmails：org 全部 owner 的邮箱（与 balance_alert 同口径，org 自己的管理者、无跨界 PII）。
func (d *Dispatcher) ownerEmails(orgID uint64) []string {
	var emails []string
	if err := d.DB.Raw(`SELECT u.email FROM org_members m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = ? AND m.role = 'owner' AND u.status = 1`, orgID).Scan(&emails).Error; err != nil {
		return nil
	}
	return emails
}

// ProcessOnce：扫一批到期的 pending 行并投递（leader-only 定时调用）。返回 (成功数, 失败数)。
func (d *Dispatcher) ProcessOnce(ctx context.Context, batch int) (sent, failed int) {
	if d == nil || d.DB == nil {
		return 0, 0
	}
	if batch <= 0 {
		batch = 100
	}
	now := d.Now()
	var rows []Outbox
	if err := d.DB.WithContext(ctx).
		Where("status = ? AND next_attempt_at <= ?", StatusPending, now).
		Order("id").Limit(batch).Find(&rows).Error; err != nil {
		d.Log.Warn("notify outbox scan failed", "err", err)
		return 0, 0
	}
	for i := range rows {
		row := &rows[i]
		var err error
		switch row.Channel {
		case ChannelEmail:
			err = d.sendEmail(row)
		case ChannelWebhook:
			err = d.sendWebhook(ctx, row)
		default:
			err = fmt.Errorf("unknown channel %q", row.Channel)
		}
		if err == nil {
			t := d.Now()
			d.DB.Model(row).Updates(map[string]any{"status": StatusSent, "sent_at": t})
			sent++
			continue
		}
		failed++
		row.Attempts++
		upd := map[string]any{"attempts": row.Attempts}
		if row.Attempts >= maxAttempts {
			upd["status"] = StatusDead
			d.Log.Warn("notify delivery dead-lettered", "outbox", row.ID, "event", row.Event, "channel", row.Channel)
		} else {
			step := backoffSteps[min(row.Attempts-1, len(backoffSteps)-1)]
			upd["next_attempt_at"] = d.Now().Add(step)
		}
		d.DB.Model(row).Updates(upd)
		// err 不整段落日志：webhook 传输错误可能回显目标 URL 细节，只记摘要（红线 7 纪律）
		d.Log.Warn("notify delivery failed", "outbox", row.ID, "event", row.Event, "channel", row.Channel, "attempts", row.Attempts)
	}
	return sent, failed
}

// sendEmail：事件模板极简（英文，与既有系统邮件一致）。
func (d *Dispatcher) sendEmail(row *Outbox) error {
	if d.Mail == nil {
		return fmt.Errorf("mailer unavailable")
	}
	subject, body := emailTemplate(row)
	var lastErr error
	delivered := false
	for _, to := range strings.Split(row.Target, ",") {
		to = strings.TrimSpace(to)
		if to == "" {
			continue
		}
		if err := d.Mail(to, subject, body); err != nil {
			lastErr = err
		} else {
			delivered = true
		}
	}
	if delivered {
		return nil // 至少送达一位（与 balance_alert 同语义）
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no recipients")
	}
	return lastErr
}

func emailTemplate(row *Outbox) (subject, body string) {
	var p map[string]any
	_ = json.Unmarshal([]byte(row.Payload), &p)
	get := func(k string) string { s, _ := p[k].(string); return s }
	switch row.Event {
	case EventTaskCompleted:
		subject = fmt.Sprintf("[LAIAI] Task completed: %s", get("task_id"))
		body = fmt.Sprintf("Your task %s (model %s) has completed.\nCost: $%s\n", get("task_id"), get("model"), get("cost_usd"))
	case EventTaskFailed:
		subject = fmt.Sprintf("[LAIAI] Task failed: %s", get("task_id"))
		body = fmt.Sprintf("Your task %s (model %s) has failed. The hold has been fully released.\n", get("task_id"), get("model"))
	case EventBalanceLow:
		subject = fmt.Sprintf("[LAIAI] Low balance alert: $%s remaining", get("available_usd"))
		body = fmt.Sprintf("Your organization has $%s available balance, below your alert threshold of $%s.\n\n"+
			"Top up to avoid request failures. This alert repeats at most once every 24 hours.\n",
			get("available_usd"), get("threshold_usd"))
	default:
		subject = fmt.Sprintf("[LAIAI] Notification: %s", row.Event)
		body = row.Payload + "\n"
	}
	return subject, body
}

// HandlesEvent：该 org 是否由 Notify 层接管此事件（有订阅行 + 事件启用 + 至少一个可用通道）。
// balance_alert sweep 用它做新旧路径互斥（有订阅走 Notify，无订阅走 legacy 直发邮件——绝不双发）。
func (d *Dispatcher) HandlesEvent(orgID uint64, event string) bool {
	if d == nil || d.DB == nil {
		return false
	}
	st := &Settings{}
	if err := d.DB.Where("org_id = ?", orgID).First(st).Error; err != nil {
		return false
	}
	if !st.EventEnabled(event) {
		return false
	}
	emailOK := st.EmailEnabled && d.MailActive != nil && d.MailActive()
	return emailOK || st.WebhookURL != ""
}

// sendWebhook：POST {"id","event","created_at","data"}，签名头
// X-LAIAI-Signature: t=<unix>,v1=hex(hmac-sha256(secret, "<t>.<body>"))（复用支付回调 HMAC 口径）。
func (d *Dispatcher) sendWebhook(ctx context.Context, row *Outbox) error {
	if d.HTTP == nil {
		return fmt.Errorf("webhook client unavailable")
	}
	secret, err := d.webhookSecret(row.OrgID)
	if err != nil {
		return err
	}
	var data json.RawMessage = []byte(row.Payload)
	envelope, err := json.Marshal(map[string]any{
		"id": row.ID, "event": row.Event, "created_at": row.CreatedAt.UTC().Format(time.RFC3339), "data": data,
	})
	if err != nil {
		return err
	}
	ts := fmt.Sprintf("%d", d.Now().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(envelope)
	sig := hex.EncodeToString(mac.Sum(nil))

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, row.Target, bytes.NewReader(envelope))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LAIAI-Signature", "t="+ts+",v1="+sig)
	req.Header.Set("User-Agent", "laiai-notify/1")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("webhook unreachable") // 不透传 *url.Error（可能含 URL 细节，红线 7 纪律）
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("webhook status %d", resp.StatusCode)
}

// webhookSecret：解出该 org 的签名密钥（信封加密落库）。
func (d *Dispatcher) webhookSecret(orgID uint64) (string, error) {
	st := &Settings{}
	if err := d.DB.Where("org_id = ?", orgID).First(st).Error; err != nil {
		return "", err
	}
	if st.WebhookSecretEnc == "" {
		return "", fmt.Errorf("webhook secret not configured")
	}
	if d.Keyring == nil {
		return "", fmt.Errorf("keyring unavailable")
	}
	return d.Keyring.Decrypt(st.WebhookSecretEnc)
}
