package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
)

const sessionTTL = 24 * time.Hour

type registerReq struct {
	Email       string `json:"email" binding:"required"`
	Password    string `json:"password" binding:"required"`
	DisplayName string `json:"display_name"`
}

// register：去重由 users.email_canonical UNIQUE 兜底（schema 层红线）。
// 旧平台的 Redis SETNX 前置锁不再需要——那是 NewAPI 不写 email、无法建 UNIQUE 时代的补丁；
// 自有 schema 下并发注册竞态由数据库唯一约束干净解决。
func (s *Server) register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "email and password are required")
		return
	}
	var user *identity.User
	var org *identity.Organization
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		user, org, err = identity.Register(tx, identity.RegisterInput{
			Email: req.Email, Password: req.Password, DisplayName: req.DisplayName,
		})
		return err
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
	// 默认登入个人 org（owner 的第一个 org）；org 切换 M1 Console 做
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
	c.JSON(http.StatusOK, gin.H{
		"user_id":      user.ID,
		"email":        user.Email,
		"display_name": user.DisplayName,
		"org_id":       claims.OrgID,
	})
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
