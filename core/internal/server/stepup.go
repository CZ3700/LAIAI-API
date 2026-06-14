package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/totp"
)

// 敏感操作二次验证（step-up，gap #4，对标 NewAPI secure_verification）。
// laiai-next 是无状态 HMAC 会话，无服务端 session，故用短期 step-up 令牌：
// 用密码或当前 TOTP 再认证 → 发 5 分钟令牌（绑定 uid）→ 敏感端点要求 X-Secure-Verify 头。
// **默认关闭**：仅当运营开启 require_stepup_for_keys 时才对受保护端点强制（零摩擦默认）。

const stepUpTTL = 300 // 5 分钟

func (s *Server) makeStepUpToken(uid uint64) string {
	exp := s.now().Unix() + stepUpTTL
	payload := fmt.Sprintf("%d.%d", uid, exp)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("stepup:" + payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifyStepUpToken(token string, uid uint64) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("stepup:" + payload))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[2])) {
		return false
	}
	tokUID, err1 := strconv.ParseUint(parts[0], 10, 64)
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || tokUID != uid || s.now().Unix() > exp {
		return false
	}
	return true
}

// meVerify：POST /api/me/verify {password?, totp_code?} —— 再认证换 step-up 令牌。
// 接受密码或（已启用 2FA 时的）当前 TOTP；任一通过即发令牌。
func (s *Server) meVerify(c *gin.Context) {
	claims := mustClaims(c)
	var req struct {
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	_ = c.ShouldBindJSON(&req)
	user := &identity.User{}
	if err := s.db.First(user, claims.UserID).Error; err != nil {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	ok := false
	if req.TOTPCode != "" && user.TOTPEnabled != 0 && s.keyring != nil {
		if secret, derr := s.keyring.Decrypt(user.TOTPSecretEnc); derr == nil && totp.Validate(secret, req.TOTPCode, s.now()) {
			ok = true
		}
	}
	if !ok && req.Password != "" {
		if identity.VerifyPassword(req.Password, user.PasswordHash) == nil {
			ok = true
		}
	}
	if !ok {
		fail(c, http.StatusUnauthorized, "verification failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"token": s.makeStepUpToken(user.ID), "expires_at": s.now().Unix() + stepUpTTL})
}

// requireStepUpForKeys：受 require_stepup_for_keys 开关控制的二次验证门。
// 关闭（默认）= 直接放行；开启 = 必须带有效 X-Secure-Verify 令牌，否则 403 VERIFICATION_REQUIRED。
func (s *Server) requireStepUpForKeys() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.getSetting("require_stepup_for_keys") != "true" {
			c.Next()
			return
		}
		claims := mustClaims(c)
		token := c.GetHeader("X-Secure-Verify")
		if token == "" || !s.verifyStepUpToken(token, claims.UserID) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "secure verification required", "code": "VERIFICATION_REQUIRED",
			})
			return
		}
		c.Next()
	}
}
