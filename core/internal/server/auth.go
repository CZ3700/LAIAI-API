package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const sessionTTL = 24 * time.Hour

type registerReq struct {
	Email            string `json:"email" binding:"required"`
	Password         string `json:"password" binding:"required"`
	DisplayName      string `json:"display_name"`
	VerificationCode string `json:"verification_code"` // email_verification_enabled 时必填
	InviterCode      string `json:"inviter_code"`      // 邀请码（0016，可选）
}

// register：去重由 users.email_canonical UNIQUE 兜底（schema 层红线）。
// 旧平台的 Redis SETNX 前置锁不再需要——那是 NewAPI 不写 email、无法建 UNIQUE 时代的补丁；
// 自有 schema 下并发注册竞态由数据库唯一约束干净解决。
func (s *Server) register(c *gin.Context) {
	if !s.getBoolSetting("register_enabled") { // 运营开关（0013）
		fail(c, http.StatusForbidden, "registration is currently disabled")
		return
	}
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "email and password are required")
		return
	}
	// 邮箱验证（0014）：开启时必须先通过验证码（堵同邮箱薅羊毛）
	if s.getBoolSetting("email_verification_enabled") {
		canonical, err := identity.CanonicalizeEmail(req.Email)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid email")
			return
		}
		if !s.verifyEmailCode(canonical, req.VerificationCode) {
			fail(c, http.StatusBadRequest, "invalid or expired verification code")
			return
		}
	}
	// 注册赠送额度（运营可配，热更新）：>0 时在同一事务内入账，保证「建号即到账」原子
	welcome := decimal.Zero
	if g := s.getSetting("new_user_grant_usd"); g != "" {
		if v, e := decimal.NewFromString(g); e == nil && v.IsPositive() {
			welcome = v
		}
	}
	var user *identity.User
	var org *identity.Organization
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		user, org, err = identity.Register(tx, identity.RegisterInput{
			Email: req.Email, Password: req.Password, DisplayName: req.DisplayName,
			InviterCode: req.InviterCode,
		})
		if err != nil {
			return err
		}
		if welcome.IsPositive() {
			acc, e := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
			if e != nil {
				return e
			}
			// 幂等键含 org：同一新组织永不重复赠送
			if _, _, e := ledger.Post(tx, ledger.PostInput{
				IdempotencyKey: "welcome_grant:" + strconv.FormatUint(org.ID, 10),
				AccountID:      acc.ID,
				Kind:           ledger.KindGrant,
				Delta:          welcome,
				RefType:        "welcome_grant",
				Reason:         "new user signup bonus",
			}); e != nil {
				return e
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, identity.ErrEmailTaken):
		fail(c, http.StatusConflict, "email already registered")
	case errors.Is(err, identity.ErrInvalidInput):
		fail(c, http.StatusBadRequest, "invalid email or password (min 8 chars)")
	case err != nil:
		fail(c, http.StatusInternalServerError, "registration failed")
	default:
		c.JSON(http.StatusCreated, gin.H{"user_id": user.ID, "org_id": org.ID})
	}
}

type loginReq struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// login：用户不存在与密码错误返回同一错误（不暴露注册情况的 oracle）。
func (s *Server) login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "email and password are required")
		return
	}
	user, err := identity.FindUserByEmail(s.db, req.Email)
	if err != nil || user.Status != identity.StatusActive {
		fail(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := identity.VerifyPassword(req.Password, user.PasswordHash); err != nil {
		fail(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	// 2FA（0017）：密码通过后，若启用则返回中间票，要求二阶段验 TOTP
	if user.TOTPEnabled != 0 {
		c.JSON(http.StatusOK, gin.H{"requires_2fa": true, "token": s.make2FAToken(user.ID)})
		return
	}
	s.issueSessionFor(c, user)
}

// issueSessionFor：为用户签发会话（登入其 owner 的第一个 org）。2FA 通过后复用。
func (s *Server) issueSessionFor(c *gin.Context, user *identity.User) {
	m := &identity.OrgMember{}
	if err := s.db.Where("user_id = ? AND role = ?", user.ID, identity.RoleOwner).
		Order("id").First(m).Error; err != nil {
		fail(c, http.StatusInternalServerError, "no organization")
		return
	}
	token, claims, err := identity.IssueSessionToken(s.secret, user.ID, m.OrgID, sessionTTL, s.now())
	if err != nil {
		fail(c, http.StatusInternalServerError, "token issue failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "expires_at": claims.Exp, "org_id": m.OrgID})
}

func (s *Server) me(c *gin.Context) {
	claims := mustClaims(c)
	user := &identity.User{}
	if err := s.db.First(user, claims.UserID).Error; err != nil {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	role := user.Role
	if role == "" {
		role = identity.PlatRoleUser
	}
	out := gin.H{
		"user_id":      user.ID,
		"email":        user.Email,
		"display_name": user.DisplayName,
		"org_id":       claims.OrgID,
		"totp_enabled": user.TOTPEnabled != 0,
		"role":         role, // RBAC（0020）：前端据此决定是否放行后台、显示管理员入口
	}
	if user.TOTPEnabled != 0 { // 剩余备用码数（低余量提醒，gap #3）
		if n, err := identity.CountUnusedBackupCodes(s.db, user.ID); err == nil {
			out["backup_codes_remaining"] = n
		}
	}
	c.JSON(http.StatusOK, out)
}

type updateProfileReq struct {
	DisplayName string `json:"display_name"`
}

// updateProfile：PATCH /api/me —— 改显示名。
func (s *Server) updateProfile(c *gin.Context) {
	claims := mustClaims(c)
	var req updateProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request")
		return
	}
	if err := s.db.Model(&identity.User{}).Where("id = ?", claims.UserID).
		Update("display_name", req.DisplayName).Error; err != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"display_name": req.DisplayName})
}

type changePasswordReq struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// changePassword：POST /api/me/password —— 验证旧密码后改密。
// 不使会话失效（无 token blocklist；session 短 TTL + 改密非高频，M2 上 Redis 后再撤销）。
func (s *Server) changePassword(c *gin.Context) {
	claims := mustClaims(c)
	var req changePasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "old_password and new_password required")
		return
	}
	user := &identity.User{}
	if err := s.db.First(user, claims.UserID).Error; err != nil {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	if err := identity.VerifyPassword(req.OldPassword, user.PasswordHash); err != nil {
		fail(c, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	hash, err := identity.HashPassword(req.NewPassword)
	if err != nil {
		fail(c, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	if err := s.db.Model(&identity.User{}).Where("id = ?", claims.UserID).
		Update("password_hash", hash).Error; err != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// authRequired：Bearer session token 校验中间件。
func (s *Server) authRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			fail(c, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := identity.VerifySessionToken(s.secret, strings.TrimPrefix(h, "Bearer "), s.now())
		if err != nil {
			fail(c, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		c.Set("claims", claims)
		c.Next()
	}
}

func mustClaims(c *gin.Context) *identity.SessionClaims {
	return c.MustGet("claims").(*identity.SessionClaims)
}
