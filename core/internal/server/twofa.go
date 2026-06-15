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
	"gorm.io/gorm"
)

// 2FA（TOTP，0017）。secret 用 keyring 信封加密存 users.totp_secret_enc。

// twofaSetup：POST /api/me/2fa/setup —— 生成密钥（暂存未启用），返回 otpauth URI 给前端转二维码。
func (s *Server) twofaSetup(c *gin.Context) {
	if s.keyring == nil {
		fail(c, http.StatusServiceUnavailable, "2FA unavailable (no data key configured)")
		return
	}
	claims := mustClaims(c)
	user := &identity.User{}
	if err := s.db.First(user, claims.UserID).Error; err != nil {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	secret := totp.GenerateSecret()
	enc, err := s.keyring.Encrypt(secret)
	if err != nil {
		fail(c, http.StatusInternalServerError, "encrypt failed")
		return
	}
	// 存密钥但不启用（enable 验证码后才启用）
	if err := s.db.Model(user).Updates(map[string]any{"totp_secret_enc": enc, "totp_enabled": 0}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "save failed")
		return
	}
	uri := totp.ProvisioningURI(secret, user.Email, s.getSetting("site_name"))
	c.JSON(http.StatusOK, gin.H{"secret": secret, "otpauth_uri": uri})
}

// twofaEnable：POST /api/me/2fa/enable {code} —— 验证码正确则启用。
func (s *Server) twofaEnable(c *gin.Context) {
	claims := mustClaims(c)
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "code required")
		return
	}
	user := &identity.User{}
	if err := s.db.First(user, claims.UserID).Error; err != nil || user.TOTPSecretEnc == "" {
		fail(c, http.StatusBadRequest, "run setup first")
		return
	}
	secret, err := s.keyring.Decrypt(user.TOTPSecretEnc)
	if err != nil || !totp.Validate(secret, req.Code, s.now()) {
		fail(c, http.StatusBadRequest, "invalid code")
		return
	}
	// 启用 + 生成备用码（同事务），明文仅此一次返回
	var codes []string
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(user).Update("totp_enabled", 1).Error; e != nil {
			return e
		}
		var genErr error
		codes, genErr = identity.GenerateBackupCodes(tx, user.ID, s.now().Unix())
		return genErr
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "enable failed")
		return
	}
	s.writeAudit(c, "2fa_enable", strconv.FormatUint(user.ID, 10), "2FA enabled")
	c.Header("Cache-Control", "no-store") // 备用码明文不进缓存
	c.JSON(http.StatusOK, gin.H{"enabled": true, "backup_codes": codes})
}

// twofaRegenBackup：POST /api/me/2fa/backup-codes {code} —— 验 TOTP 后重置备用码（旧码全失效）。
func (s *Server) twofaRegenBackup(c *gin.Context) {
	claims := mustClaims(c)
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "code required")
		return
	}
	user := &identity.User{}
	if err := s.db.First(user, claims.UserID).Error; err != nil || user.TOTPEnabled == 0 {
		fail(c, http.StatusBadRequest, "2FA not enabled")
		return
	}
	secret, err := s.keyring.Decrypt(user.TOTPSecretEnc)
	if err != nil || !totp.Validate(secret, req.Code, s.now()) {
		fail(c, http.StatusBadRequest, "invalid code")
		return
	}
	var codes []string
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var genErr error
		codes, genErr = identity.GenerateBackupCodes(tx, user.ID, s.now().Unix())
		return genErr
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "regenerate failed")
		return
	}
	s.writeAudit(c, "2fa_backup_regen", strconv.FormatUint(user.ID, 10), "2FA backup codes regenerated")
	c.Header("Cache-Control", "no-store") // 备用码明文不进缓存
	c.JSON(http.StatusOK, gin.H{"backup_codes": codes})
}

// twofaDisable：POST /api/me/2fa/disable {code} —— 验证码正确则关闭并清除密钥。
func (s *Server) twofaDisable(c *gin.Context) {
	claims := mustClaims(c)
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "code required")
		return
	}
	user := &identity.User{}
	if err := s.db.First(user, claims.UserID).Error; err != nil || user.TOTPEnabled == 0 {
		fail(c, http.StatusBadRequest, "2FA not enabled")
		return
	}
	secret, err := s.keyring.Decrypt(user.TOTPSecretEnc)
	if err != nil || !totp.Validate(secret, req.Code, s.now()) {
		fail(c, http.StatusBadRequest, "invalid code")
		return
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(user).Updates(map[string]any{"totp_enabled": 0, "totp_secret_enc": ""}).Error; e != nil {
			return e
		}
		return identity.DeleteBackupCodes(tx, user.ID) // 关闭即清空备用码
	}); err != nil {
		fail(c, http.StatusInternalServerError, "disable failed")
		return
	}
	s.writeAudit(c, "2fa_disable", strconv.FormatUint(user.ID, 10), "2FA disabled")
	c.JSON(http.StatusOK, gin.H{"enabled": false})
}

// twofaVerifyLogin：POST /api/auth/2fa {token, code} —— 登录二阶段，验 2FA 后发真 session。
func (s *Server) twofaVerifyLogin(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "token and code required")
		return
	}
	uid, ok := s.verify2FAToken(req.Token)
	if !ok {
		fail(c, http.StatusUnauthorized, "invalid or expired 2fa session")
		return
	}
	user := &identity.User{}
	if err := s.db.First(user, uid).Error; err != nil || user.TOTPEnabled == 0 {
		fail(c, http.StatusUnauthorized, "2fa not active")
		return
	}
	// 与一阶段 login / OAuth / Telegram 登录口径一致：签发会话前复核账号在职（fail-fast，
	// 防被封用户在 5 分钟中间票窗口内兑换出 session；下游 requireActiveUser 也会拦，此处提前一致化）
	if user.Status != identity.StatusActive {
		fail(c, http.StatusForbidden, "account is not active")
		return
	}
	secret, err := s.keyring.Decrypt(user.TOTPSecretEnc)
	if err != nil {
		fail(c, http.StatusUnauthorized, "invalid code")
		return
	}
	if !totp.Validate(secret, req.Code, s.now()) {
		// TOTP 不过，尝试备用码（原子消费，用过即焚）
		consumed, cerr := identity.ConsumeBackupCode(s.db, uid, req.Code, s.now().Unix())
		if cerr != nil {
			fail(c, http.StatusInternalServerError, "verify failed")
			return
		}
		if !consumed {
			fail(c, http.StatusUnauthorized, "invalid code")
			return
		}
		s.writeAudit(c, "2fa_backup_used", strconv.FormatUint(uid, 10), "logged in with a backup code")
	}
	s.issueSessionFor(c, user)
}

// —— 2FA 中间凭证（登录密码通过、待验 TOTP 的短期票）——
// 用 session secret 签 "uid.exp.hmac"，5 分钟有效。无状态，不落库。

const twofaTokenTTL = 300 // 5 分钟

func (s *Server) make2FAToken(uid uint64) string {
	exp := s.now().Unix() + twofaTokenTTL
	payload := fmt.Sprintf("%d.%d", uid, exp)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("2fa:" + payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) verify2FAToken(token string) (uint64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("2fa:" + payload))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[2])) {
		return 0, false
	}
	uid, _ := strconv.ParseUint(parts[0], 10, 64)
	exp, _ := strconv.ParseInt(parts[1], 10, 64)
	if s.now().Unix() > exp {
		return 0, false
	}
	return uid, true
}
