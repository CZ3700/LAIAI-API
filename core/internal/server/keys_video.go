package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errVideoConsent     = errors.New("video_execution_consent_required")
	errVideoDisabled    = errors.New("video_execution_disabled")
	errVideoOwner       = errors.New("video_execution_owner_required")
	errVideoUnavailable = errors.New("video_execution_unavailable")
)

func videoWriteScope(scopes string) bool {
	for _, scope := range strings.FieldsFunc(scopes, func(r rune) bool { return r == ' ' || r == ',' }) {
		if scope == "video:write" || scope == "video:*" || scope == "*" {
			return true
		}
	}
	return false
}

// This describes video execution, not the separately inherited asset policy.
func (s *Server) keyVideoAccess(c *gin.Context) {
	claims := mustClaims(c)
	state, canEnable := "unavailable", false
	if s.video != nil {
		ent, err := identity.LoadEntitlements(s.db, claims.OrgID)
		if err != nil {
			fail(c, 503, "video access unavailable")
			return
		}
		var policy video.Policy
		err = s.db.Where("org_id = ?", claims.OrgID).Take(&policy).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 503, "video access unavailable")
			return
		}
		if !ent.FeatureVideo || err == nil && !policy.AsyncExecutionAllowed {
			state = "disabled"
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			role, e := identity.RoleIn(s.db, claims.OrgID, claims.UserID)
			if e != nil {
				fail(c, 403, "active organization membership required")
				return
			}
			state, canEnable = "not_configured", role == identity.RoleOwner
		} else if policy.RetentionDays >= 1 && policy.RetentionDays <= 30 {
			state = "enabled"
		}
	}
	c.JSON(200, gin.H{"state": state, "can_enable": canEnable})
}

// Caller holds the organization lock. Never overwrite an administrator's policy.
func (s *Server) ensureKeyVideoAccess(tx *gorm.DB, org, user uint64, consent bool, ent identity.Entitlements) error {
	if s.video == nil {
		return errVideoUnavailable
	}
	if !ent.FeatureVideo {
		return errVideoDisabled
	}
	var policy video.Policy
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ?", org).Take(&policy).Error
	if err == nil {
		if !policy.AsyncExecutionAllowed {
			return errVideoDisabled
		}
		if policy.RetentionDays < 1 || policy.RetentionDays > 30 {
			return errVideoUnavailable
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var member identity.OrgMember
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND user_id = ?", org, user).Take(&member).Error; err != nil {
		return errVideoOwner
	}
	if member.Role != identity.RoleOwner {
		return errVideoOwner
	}
	if !consent {
		return errVideoConsent
	}
	policy = video.Policy{OrgID: org, AsyncExecutionAllowed: true, CaptureHistory: false, RetentionDays: 1, UpdatedAt: s.now().UTC()}
	if err := tx.Create(&policy).Error; err != nil {
		return err
	}
	return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", user), Action: "video.policy.enable_on_key_create", Target: fmt.Sprint(org), Detail: "explicit_async_consent_history_disabled", CreatedAt: s.now().UnixMilli()}).Error
}

func replyKeyVideoError(c *gin.Context, err error) bool {
	code, message := "", ""
	switch {
	case errors.Is(err, errVideoConsent):
		code, message = errVideoConsent.Error(), "Confirm organization video execution when creating this key."
	case errors.Is(err, errVideoOwner):
		code, message = errVideoOwner.Error(), "Ask the organization owner to enable video execution."
	case errors.Is(err, errVideoDisabled):
		code, message = errVideoDisabled.Error(), "Video execution is disabled. Contact an administrator."
	case errors.Is(err, errVideoUnavailable):
		code, message = errVideoUnavailable.Error(), "Video execution is unavailable. Contact an administrator."
	default:
		return false
	}
	status := 403
	if code == errVideoUnavailable.Error() {
		status = 503
	}
	c.JSON(status, gin.H{"code": code, "error": message})
	return true
}
