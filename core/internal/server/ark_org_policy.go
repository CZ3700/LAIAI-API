package server

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Matches the safe policy projection returned by asset-setup. Presence is part
// of the precondition so simultaneous first-time setup cannot overwrite a new
// policy even when the client previously saw no policy row.
type videoPolicyExpectation struct {
	Present bool `json:"present"`
	Async   bool `json:"async_execution_allowed"`
	History bool `json:"capture_history"`
	Days    int  `json:"retention_days"`
}

func (s *Server) publishVideoOrgPolicy(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	if s.video == nil {
		fail(c, 503, "video gateway unavailable")
		return
	}
	org, err := strconv.ParseUint(c.Param("org_id"), 10, 64)
	if err != nil || org == 0 {
		fail(c, 422, "invalid organization")
		return
	}
	var body struct {
		Async          bool                    `json:"async_execution_allowed"`
		History        bool                    `json:"capture_history"`
		Days           int                     `json:"retention_days"`
		ExpectedPolicy *videoPolicyExpectation `json:"expected_policy"`
	}
	if err = videoAdminDecode(c, &body); err != nil || body.Days < 1 || body.Days > 30 {
		fail(c, 422, "invalid retention policy")
		return
	}
	if p := body.ExpectedPolicy; p != nil && !p.Present && (p.Async || p.History || p.Days != 0) {
		fail(c, 422, "invalid expected policy")
		return
	}
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// A policy row may not exist yet. Serialize every writer on the stable
		// organization row before checking either presence or policy values.
		var organization struct {
			ID     uint64
			Status int8
		}
		if err := tx.Table("organizations").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id, status").Where("id = ?", org).Take(&organization).Error; err != nil {
			return err
		}
		if organization.Status != identity.StatusActive {
			return identity.ErrNotActiveMember
		}
		if expected := body.ExpectedPolicy; expected != nil {
			var existing video.Policy
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ?", org).Take(&existing).Error
			present := err == nil
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if expected.Present != present || present && (expected.Async != existing.AsyncExecutionAllowed || expected.History != existing.CaptureHistory || expected.Days != existing.RetentionDays) {
				return video.ErrConflict
			}
		}
		policy := video.Policy{OrgID: org, AsyncExecutionAllowed: body.Async, CaptureHistory: body.History, RetentionDays: body.Days, UpdatedAt: s.now().UTC()}
		if err := tx.Save(&policy).Error; err != nil {
			return err
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.policy.publish", Target: fmt.Sprint(org), Detail: "explicit_async_and_history_policy", CreatedAt: s.now().UnixMilli()}).Error
	})
	if errors.Is(err, video.ErrConflict) {
		c.JSON(409, gin.H{"error": "organization execution policy changed; refresh and confirm again", "code": "execution_policy_conflict"})
		return
	}
	s.videoAdminReply(c, err)
}
