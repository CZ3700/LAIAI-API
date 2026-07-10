package server

// Notify 统一层的 org 订阅配置 API（设计 §4.9）。
//   GET  /api/org/notify-settings —— 成员可见（webhook secret 只回显是否已配，绝不明文）。
//   PUT  /api/org/notify-settings —— 仅 owner/admin；webhook_url 强制 https + SSRF 校验，
//        secret 信封加密落库；events 白名单过滤。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/notify"
	"github.com/laiai/laiai-next/core/internal/relay"
)

// ProcessNotifyOutbox：出站通知 outbox 投递一批（leader-only 维护循环调用，设计 §4.9）。
func (s *Server) ProcessNotifyOutbox(ctx context.Context, batch int) (sent, failed int) {
	if s.notify == nil {
		return 0, 0
	}
	return s.notify.ProcessOnce(ctx, batch)
}

// PurgeNotifyOutbox：终态 outbox 行滚动保留清理（leader-only 每小时维护循环调用，对抗 review：
// 防终态行永久堆积）。保留天数走 ext_settings.notify_outbox_retention_days（默认 30，与调用日志同档）。
func (s *Server) PurgeNotifyOutbox(ctx context.Context) (int64, error) {
	d := s.retentionDaysSetting("notify_outbox_retention_days", 30)
	if d <= 0 {
		return 0, nil
	}
	cutoff := s.now().UTC().AddDate(0, 0, -d)
	return notify.PurgeOldOutbox(s.db.WithContext(ctx), cutoff, 1000)
}

func (s *Server) getNotifySettings(c *gin.Context) {
	claims := mustClaims(c)
	st := &notify.Settings{}
	if err := s.db.Where("org_id = ?", claims.OrgID).First(st).Error; err != nil {
		// 未配置：返回空默认（前端渲染空表单）
		c.JSON(http.StatusOK, gin.H{
			"email_enabled": false, "webhook_url": "", "webhook_configured": false, "events": []string{},
		})
		return
	}
	var events []string
	_ = json.Unmarshal([]byte(st.Events), &events)
	if events == nil {
		events = []string{}
	}
	c.JSON(http.StatusOK, gin.H{
		"email_enabled":      st.EmailEnabled,
		"webhook_url":        st.WebhookURL,
		"webhook_configured": st.WebhookSecretEnc != "", // 只回显"是否已配密钥"，绝不明文（红线 7）
		"events":             events,
	})
}

func (s *Server) putNotifySettings(c *gin.Context) {
	claims := mustClaims(c)
	var m identity.OrgMember
	if err := s.db.Where("org_id = ? AND user_id = ?", claims.OrgID, claims.UserID).First(&m).Error; err != nil ||
		(m.Role != "owner" && m.Role != "admin") {
		fail(c, http.StatusForbidden, "owner or admin role required")
		return
	}
	var req struct {
		EmailEnabled  bool     `json:"email_enabled"`
		WebhookURL    string   `json:"webhook_url"`
		WebhookSecret string   `json:"webhook_secret"` // 明文入参，落库前信封加密；空=保留原密钥
		Events        []string `json:"events"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	// events 白名单过滤（去未知/去重）
	seen := map[string]bool{}
	var events []string
	for _, e := range req.Events {
		if notify.KnownEvents[e] && !seen[e] {
			seen[e] = true
			events = append(events, e)
		}
	}
	if events == nil {
		events = []string{}
	}
	eventsJSON, _ := json.Marshal(events)

	// 载入原行（保留未变更的加密密钥）
	st := &notify.Settings{OrgID: claims.OrgID}
	_ = s.db.Where("org_id = ?", claims.OrgID).First(st).Error

	// webhook URL：非空则**强制 https**（对抗 review：复用的 advanced_custom 校验器放行 http，
	// 但出站 webhook 红线要求 https——事件信封+签名绝不明文 HTTP 传）+ SSRF 校验（绝不放行私网）。
	if req.WebhookURL != "" {
		u, err := url.Parse(req.WebhookURL)
		if err != nil || !strings.EqualFold(u.Scheme, "https") {
			fail(c, http.StatusBadRequest, "webhook_url must be a public https URL")
			return
		}
		if err := relay.ValidateAdvancedUpstreamURL(req.WebhookURL, false); err != nil {
			fail(c, http.StatusBadRequest, "webhook_url must be a public https URL")
			return
		}
	}

	secretEnc := st.WebhookSecretEnc
	if req.WebhookSecret != "" {
		if s.keyring == nil {
			fail(c, http.StatusServiceUnavailable, "encryption unavailable")
			return
		}
		enc, err := s.keyring.Encrypt(req.WebhookSecret)
		if err != nil {
			fail(c, http.StatusInternalServerError, "encrypt failed")
			return
		}
		secretEnc = enc
	}
	if req.WebhookURL == "" {
		secretEnc = "" // 清空 URL 同时清密钥（避免孤儿密钥）
	} else if secretEnc == "" {
		// 配 webhook 但无签名密钥（新旧都空）→ 拒绝（对抗 review：否则每条 webhook 静默 dead-letter，
		// 用户以为已订阅却一条收不到）。webhook 必须签名（HMAC），无 secret 结构性不可用。
		fail(c, http.StatusBadRequest, "webhook_secret is required when a webhook_url is set")
		return
	}

	row := &notify.Settings{
		OrgID: claims.OrgID, EmailEnabled: req.EmailEnabled,
		WebhookURL: req.WebhookURL, WebhookSecretEnc: secretEnc, Events: string(eventsJSON),
	}
	// upsert（主键 org_id）：save 全列覆盖
	if err := s.db.Save(row).Error; err != nil {
		fail(c, http.StatusInternalServerError, "save failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"email_enabled": row.EmailEnabled, "webhook_url": row.WebhookURL,
		"webhook_configured": secretEnc != "", "events": events,
	})
}
