package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
)

// Organizations/Teams 端点（设计 §4.2）。
// 成员管理的角色门（owner/admin）按当前 session 的 org 实时查库——
// 角色不进 token claims，撤权即时生效，无需等 token 过期。

// GET /api/orgs：我的全部 org（含角色）
func (s *Server) listOrgs(c *gin.Context) {
	claims := mustClaims(c)
	orgs, err := identity.OrgsOf(s.db, claims.UserID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "org list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"orgs": orgs, "current_org_id": claims.OrgID})
}

type createOrgReq struct {
	Name string `json:"name" binding:"required"`
}

// POST /api/orgs：建团队 org（创建者为 owner）
func (s *Server) createOrg(c *gin.Context) {
	claims := mustClaims(c)
	var req createOrgReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	var org *identity.Organization
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		org, err = identity.CreateTeamOrg(tx, claims.UserID, req.Name)
		return err
	})
	if err != nil {
		if errors.Is(err, identity.ErrInvalidInput) {
			fail(c, http.StatusBadRequest, "invalid organization name")
			return
		}
		fail(c, http.StatusInternalServerError, "org create failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"org_id": org.ID, "name": org.Name})
}

type switchOrgReq struct {
	OrgID uint64 `json:"org_id" binding:"required"`
}

// POST /api/auth/switch：切换当前 org（校验成员资格后签发新 token）
func (s *Server) switchOrg(c *gin.Context) {
	claims := mustClaims(c)
	var req switchOrgReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "org_id is required")
		return
	}
	if _, err := identity.RoleIn(s.db, req.OrgID, claims.UserID); err != nil {
		fail(c, http.StatusForbidden, "not a member of this organization")
		return
	}
	token, newClaims, err := identity.IssueSessionToken(s.secret, claims.UserID, req.OrgID, sessionTTL, s.now())
	if err != nil {
		fail(c, http.StatusInternalServerError, "token issue failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "expires_at": newClaims.Exp, "org_id": req.OrgID})
}

// GET /api/org/members：当前 org 成员（任何成员可见）
func (s *Server) listMembers(c *gin.Context) {
	claims := mustClaims(c)
	if _, err := identity.RoleIn(s.db, claims.OrgID, claims.UserID); err != nil {
		fail(c, http.StatusForbidden, "not a member of this organization")
		return
	}
	var members []struct {
		UserID uint64 `json:"user_id"`
		Email  string `json:"email"`
		Role   string `json:"role"`
	}
	err := s.db.Table("org_members").
		Select("org_members.user_id, users.email, org_members.role").
		Joins("JOIN users ON users.id = org_members.user_id").
		Where("org_members.org_id = ?", claims.OrgID).
		Order("org_members.id").
		Scan(&members).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "member list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": members})
}

type addMemberReq struct {
	Email string `json:"email" binding:"required"`
	Role  string `json:"role" binding:"required"`
}

// POST /api/org/members：按邮箱「邀请」成员（owner/admin）。
// 不直接拉人入伙——落一条 pending 邀请，对方在 /api/invitations 里 accept 后才成为成员
// （未经同意拉人 = 让其访问团队共享余额，红线）。
func (s *Server) inviteMember(c *gin.Context) {
	claims := mustClaims(c)
	actorRole, ok := s.requireManager(c, claims.OrgID, claims.UserID)
	if !ok {
		return
	}
	var req addMemberReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "email and role are required")
		return
	}
	var inv *identity.OrgInvitation
	var invitee *identity.User
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ent, err := identity.LoadEntitlements(tx, claims.OrgID)
		if err != nil {
			return err
		}
		inv, invitee, err = identity.InviteMember(tx, claims.OrgID, claims.UserID, req.Email, req.Role, ent.MaxMembers, actorRole, s.now().UnixMilli())
		return err
	})
	switch {
	case errors.Is(err, identity.ErrMemberLimit):
		fail(c, http.StatusForbidden, "member limit reached for this organization")
	case errors.Is(err, identity.ErrUserNotFound):
		fail(c, http.StatusNotFound, "no account with this email")
	case errors.Is(err, identity.ErrAlreadyMember):
		fail(c, http.StatusConflict, "already a member")
	case errors.Is(err, identity.ErrInvitePending):
		fail(c, http.StatusConflict, "an invitation for this user is already pending")
	case errors.Is(err, identity.ErrForbiddenRole):
		fail(c, http.StatusBadRequest, "role must be admin, member or billing")
	case errors.Is(err, identity.ErrCannotManageTarget):
		fail(c, http.StatusForbidden, "only an owner can grant the admin role")
	case err != nil:
		fail(c, http.StatusInternalServerError, "invite member failed")
	default:
		s.writeAudit(c, "org_invite_create", invitee.Email, "invited "+invitee.Email+" as "+req.Role)
		c.JSON(http.StatusCreated, gin.H{"invitation_id": inv.ID, "user_id": invitee.ID, "email": invitee.Email, "role": req.Role, "status": "pending"})
	}
}

// GET /api/org/invitations：本团队待确认邀请（owner/admin）。
func (s *Server) listOrgInvitations(c *gin.Context) {
	claims := mustClaims(c)
	if _, ok := s.requireManager(c, claims.OrgID, claims.UserID); !ok {
		return
	}
	invites, err := identity.InvitationsForOrg(s.db, claims.OrgID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "invitation list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"invitations": invites})
}

// DELETE /api/org/invitations/:id：管理者撤销本团队待确认邀请（owner/admin）。
func (s *Server) revokeOrgInvitation(c *gin.Context) {
	claims := mustClaims(c)
	if _, ok := s.requireManager(c, claims.OrgID, claims.UserID); !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid invitation id")
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return identity.RevokeInvitation(tx, id, claims.OrgID)
	})
	switch {
	case errors.Is(err, identity.ErrInviteNotFound):
		fail(c, http.StatusNotFound, "invitation not found")
	case err != nil:
		fail(c, http.StatusInternalServerError, "revoke invitation failed")
	default:
		s.writeAudit(c, "org_invite_revoke", strconv.FormatUint(id, 10), "revoked pending invitation")
		c.JSON(http.StatusOK, gin.H{"revoked": id})
	}
}

// GET /api/invitations：我收到的、待我确认的团队邀请（任何登录用户）。
func (s *Server) listMyInvitations(c *gin.Context) {
	claims := mustClaims(c)
	invites, err := identity.InvitationsForUser(s.db, claims.UserID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "invitation list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"invitations": invites})
}

// POST /api/invitations/:id/accept：接受邀请 → 成为团队成员（仅本人）。
func (s *Server) acceptInvitation(c *gin.Context) {
	claims := mustClaims(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid invitation id")
		return
	}
	var inv *identity.OrgInvitation
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var e error
		inv, e = identity.AcceptInvitation(tx, id, claims.UserID)
		return e
	})
	switch {
	case errors.Is(err, identity.ErrInviteNotFound):
		fail(c, http.StatusNotFound, "invitation not found")
	case errors.Is(err, identity.ErrMemberLimit):
		fail(c, http.StatusForbidden, "member limit reached for this organization")
	case errors.Is(err, identity.ErrNotActiveMember):
		fail(c, http.StatusForbidden, "this team is no longer active")
	case errors.Is(err, identity.ErrAlreadyMember):
		// 幂等：已是成员视作成功
		c.JSON(http.StatusOK, gin.H{"accepted": id, "already_member": true})
	case err != nil:
		fail(c, http.StatusInternalServerError, "accept invitation failed")
	default:
		s.writeAudit(c, "org_invite_accept", strconv.FormatUint(inv.OrgID, 10), "joined team via invitation")
		c.JSON(http.StatusOK, gin.H{"accepted": id, "org_id": inv.OrgID, "role": inv.Role})
	}
}

// POST /api/invitations/:id/decline：拒绝邀请（仅本人）。
func (s *Server) declineInvitation(c *gin.Context) {
	claims := mustClaims(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid invitation id")
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return identity.DeclineInvitation(tx, id, claims.UserID)
	})
	switch {
	case errors.Is(err, identity.ErrInviteNotFound):
		fail(c, http.StatusNotFound, "invitation not found")
	case err != nil:
		fail(c, http.StatusInternalServerError, "decline invitation failed")
	default:
		c.JSON(http.StatusOK, gin.H{"declined": id})
	}
}

// DELETE /api/org/members/:user_id：移除成员（owner/admin；末位 owner 保护）
func (s *Server) removeMember(c *gin.Context) {
	claims := mustClaims(c)
	actorRole, ok := s.requireManager(c, claims.OrgID, claims.UserID)
	if !ok {
		return
	}
	uid, err := strconv.ParseUint(c.Param("user_id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return identity.RemoveMember(tx, claims.OrgID, actorRole, claims.UserID, uid)
	})
	switch {
	case errors.Is(err, identity.ErrLastOwner):
		fail(c, http.StatusConflict, "cannot remove the only owner")
	case errors.Is(err, identity.ErrNotMember):
		fail(c, http.StatusNotFound, "not a member")
	case errors.Is(err, identity.ErrCannotManageTarget):
		fail(c, http.StatusForbidden, "cannot remove an owner or a peer admin")
	case err != nil:
		fail(c, http.StatusInternalServerError, "remove member failed")
	default:
		c.JSON(http.StatusOK, gin.H{"removed": uid})
	}
}

func (s *Server) requireManager(c *gin.Context, orgID, userID uint64) (string, bool) {
	role, err := identity.RoleIn(s.db, orgID, userID)
	if err != nil || !identity.CanManageMembers(role) {
		fail(c, http.StatusForbidden, "requires owner or admin role")
		return "", false
	}
	return role, true
}
