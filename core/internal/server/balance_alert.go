package server

// 余额低预警（0042，上线冲刺批次 3）：org 自设阈值，可用余额（posted-held）低于阈值时
// 给 owner 发邮件。扫描在每小时维护循环（leader-only），绝不进数据面热路径；24h 防重发。
// 预警是尽力而为的通知（best-effort）——mailer 未配/发送失败只记日志，不影响任何资金语义。

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// getBalanceAlert：GET /api/org/balance-alert —— 当前 org 的预警阈值（成员可见）。
func (s *Server) getBalanceAlert(c *gin.Context) {
	claims := mustClaims(c)
	var org identity.Organization
	if err := s.db.Where("id = ?", claims.OrgID).First(&org).Error; err != nil {
		fail(c, http.StatusInternalServerError, "load failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"low_balance_alert_usd": org.LowBalanceAlertUSD})
}

// putBalanceAlert：PUT /api/org/balance-alert {low_balance_alert_usd} —— 仅 owner/admin 可设。
// 阈值改动即清 alerted_at（新阈值立即生效，不被旧的 24h 冷却压住）。
func (s *Server) putBalanceAlert(c *gin.Context) {
	claims := mustClaims(c)
	var m identity.OrgMember
	if err := s.db.Where("org_id = ? AND user_id = ?", claims.OrgID, claims.UserID).First(&m).Error; err != nil ||
		(m.Role != "owner" && m.Role != "admin") {
		fail(c, http.StatusForbidden, "owner or admin role required")
		return
	}
	var req struct {
		Threshold string `json:"low_balance_alert_usd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	v, err := decimal.NewFromString(req.Threshold)
	if err != nil || v.IsNegative() {
		fail(c, http.StatusBadRequest, "low_balance_alert_usd must be a non-negative decimal string")
		return
	}
	if err := s.db.Model(&identity.Organization{}).Where("id = ?", claims.OrgID).
		Updates(map[string]any{"low_balance_alert_usd": v, "low_balance_alerted_at": nil}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "save failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"low_balance_alert_usd": v})
}

// SweepLowBalanceAlerts：扫描已设阈值且出了 24h 冷却期的 org，可用余额低于阈值即给全部
// owner 发邮件并盖 alerted_at 章。返回 (检查数, 发送数)。leader-only 每小时调用（main.go）。
func (s *Server) SweepLowBalanceAlerts(ctx context.Context, batch int) (checked, sent int) {
	if s.mailer == nil || !s.mailer.Active() {
		return 0, 0 // 无邮件通道时整段跳过（配好 SMTP 后自动生效）
	}
	now := s.now()
	var orgs []identity.Organization
	if err := s.db.WithContext(ctx).
		Where("low_balance_alert_usd > 0 AND status = 1 AND (low_balance_alerted_at IS NULL OR low_balance_alerted_at < ?)",
			now.Add(-24*time.Hour)).
		Limit(batch).Find(&orgs).Error; err != nil {
		s.log.Warn("low-balance sweep query failed", "err", err)
		return 0, 0
	}
	site := s.getSetting("site_name")
	if site == "" {
		site = "LAIAI"
	}
	for i := range orgs {
		org := &orgs[i]
		checked++
		avail, err := ledger.OrgAvailable(s.db, org.ID, ledger.AccountUserCredit)
		if err != nil || avail.GreaterThanOrEqual(org.LowBalanceAlertUSD) {
			continue
		}
		// 收件人 = 全部 owner 的邮箱（org 自己的管理者，无跨界 PII）
		var emails []string
		if err := s.db.Raw(`SELECT u.email FROM org_members m JOIN users u ON u.id = m.user_id
			WHERE m.org_id = ? AND m.role = 'owner' AND u.status = 1`, org.ID).Scan(&emails).Error; err != nil || len(emails) == 0 {
			continue
		}
		subject := fmt.Sprintf("[%s] Low balance alert: $%s remaining", site, avail.StringFixed(2))
		body := fmt.Sprintf(
			"Your organization %q has $%s available balance, below your alert threshold of $%s.\n\n"+
				"Top up to avoid request failures. This alert repeats at most once every 24 hours.\n",
			org.Name, avail.StringFixed(2), org.LowBalanceAlertUSD.StringFixed(2))
		delivered := false
		for _, to := range emails {
			if err := s.mailer.Send(to, subject, body); err != nil {
				s.log.Warn("low-balance alert send failed", "org", org.ID, "err", err)
			} else {
				delivered = true
			}
		}
		if delivered {
			sent++
			// 只盖章不重试语义：至少送达一位 owner 即算本轮完成（24h 后自然重扫）
			if err := s.db.Model(&identity.Organization{}).Where("id = ?", org.ID).
				Update("low_balance_alerted_at", now).Error; err != nil {
				s.log.Warn("low-balance alerted_at stamp failed", "org", org.ID, "err", err)
			}
		}
	}
	return checked, sent
}
