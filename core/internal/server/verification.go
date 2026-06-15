package server

import (
	"crypto/rand"
	"math/big"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

// EmailVerification：验证码记录（0014）。
type EmailVerification struct {
	EmailCanonical string `gorm:"primaryKey;column:email_canonical"`
	Code           string `gorm:"column:code"`
	ExpiresAt      int64  `gorm:"column:expires_at"`
	CreatedAt      int64  `gorm:"column:created_at"`
}

func (EmailVerification) TableName() string { return "email_verifications" }

const verificationTTLSeconds = 600 // 10 分钟

// gen6 生成 6 位数字验证码（crypto/rand）。
func gen6() string {
	const digits = "0123456789"
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(10))
		b[i] = digits[n.Int64()]
	}
	return string(b)
}

// sendVerification：POST /api/auth/send-verification {email}
// 生成验证码、落库、发邮件（未配 SMTP 则打日志）。受 authRateLimit 限流。
// 不泄露邮箱是否已注册（防枚举）：无论是否已注册都返回成功。
func (s *Server) sendVerification(c *gin.Context) {
	if s.mailer == nil {
		fail(c, http.StatusServiceUnavailable, "email is not configured")
		return
	}
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "email required")
		return
	}
	canonical, err := identity.CanonicalizeEmail(req.Email)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid email")
		return
	}
	// 邮箱准入策略（gap P2-E）：不向被禁/非白名单域名发码，省得后续注册再被拒。403=策略拒绝
	if e := s.checkEmailDomainPolicy(req.Email); e != nil {
		fail(c, http.StatusForbidden, e.Error())
		return
	}
	code := gen6()
	now := s.now().Unix()
	rec := &EmailVerification{
		EmailCanonical: canonical,
		Code:           code,
		ExpiresAt:      now + verificationTTLSeconds,
		CreatedAt:      now,
	}
	if err := s.db.Save(rec).Error; err != nil {
		fail(c, http.StatusInternalServerError, "could not store verification")
		return
	}
	// 发送（LogMailer 打日志；SMTP 真发）。失败只记日志不阻断（用户可重发）。
	site := s.getSetting("site_name")
	_ = s.mailer.Send(req.Email, site+" verification code",
		"Your "+site+" verification code is: "+code+"\n\nIt expires in 10 minutes.")
	c.JSON(http.StatusOK, gin.H{"sent": true})
}

// verifyEmailCode 校验码是否有效（未过期且匹配）。注册流程内部调用。
func (s *Server) verifyEmailCode(canonical, code string) bool {
	if code == "" {
		return false
	}
	var rec EmailVerification
	if err := s.db.Where("email_canonical = ?", canonical).First(&rec).Error; err != nil {
		return false
	}
	if rec.Code != code || s.now().Unix() > rec.ExpiresAt {
		return false
	}
	// 用后即焚，防重放
	s.db.Where("email_canonical = ?", canonical).Delete(&EmailVerification{})
	return true
}
