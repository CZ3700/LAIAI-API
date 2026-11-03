package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/watermark"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type watermarkUserView struct {
	UserID      uint64    `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Status      int8      `json:"status"`
	GrantedAt   time.Time `json:"granted_at"`
}

// The watermark pipeline's runtime toggle. The worker and registration both
// read this DB flag, so flipping it takes effect without restarts; the
// pipeline itself only exists when media storage and ffmpeg are wired.
func (s *Server) getWatermarkSetting(c *gin.Context) {
	configured := s.video != nil && s.video.Watermark != nil
	enabled := false
	var row watermark.Settings
	if err := s.db.Where("id = 1").Take(&row).Error; err == nil {
		enabled = row.Enabled
	} else if err != gorm.ErrRecordNotFound {
		fail(c, http.StatusInternalServerError, "watermark settings unavailable")
		return
	}
	var users []watermarkUserView
	if err := s.db.WithContext(c.Request.Context()).Table("video_watermark_users AS w").
		Select("w.user_id, u.email, u.display_name, u.status, w.granted_at").
		Joins("JOIN users AS u ON u.id = w.user_id").Order("w.granted_at DESC, w.user_id DESC").Scan(&users).Error; err != nil {
		fail(c, http.StatusInternalServerError, "watermark users unavailable")
		return
	}
	if users == nil {
		users = []watermarkUserView{}
	}
	c.JSON(http.StatusOK, gin.H{"enabled": enabled, "configured": configured, "users": users})
}

func (s *Server) putWatermarkUser(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	userID, err := strconv.ParseUint(c.Param("user_id"), 10, 64)
	if err != nil || userID == 0 {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if videoAdminDecode(c, &in) != nil || in.Enabled == nil {
		fail(c, http.StatusUnprocessableEntity, "enabled boolean required")
		return
	}
	if *in.Enabled {
		var user identity.User
		if err := s.db.WithContext(c.Request.Context()).Where("id = ? AND status = ?", userID, identity.StatusActive).Take(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				fail(c, http.StatusNotFound, "active user not found")
			} else {
				fail(c, http.StatusInternalServerError, "user lookup failed")
			}
			return
		}
	}
	now := s.now().UTC()
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if *in.Enabled {
			grant := watermark.UserGrant{UserID: userID, GrantedBy: actor.ID, GrantedAt: now}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoNothing: true}).Create(&grant).Error; err != nil {
				return err
			}
		} else if err := tx.Where("user_id = ?", userID).Delete(&watermark.UserGrant{}).Error; err != nil {
			return err
		}
		detail := "user_disabled"
		if *in.Enabled {
			detail = "user_enabled"
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.watermark.user", Target: fmt.Sprintf("user#%d", userID), Detail: detail, CreatedAt: s.now().UnixMilli()}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "watermark user update failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"user_id": userID, "enabled": *in.Enabled})
}

func (s *Server) putWatermarkSetting(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	if s.video == nil || s.video.Watermark == nil {
		fail(c, http.StatusServiceUnavailable, "watermark pipeline requires media storage and ffmpeg configuration")
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if videoAdminDecode(c, &in) != nil || in.Enabled == nil {
		fail(c, http.StatusUnprocessableEntity, "enabled boolean required")
		return
	}
	now := s.now().UTC()
	detail := "watermark_disabled"
	if *in.Enabled {
		detail = "watermark_enabled"
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		write := tx.Model(&watermark.Settings{}).Where("id = 1").
			Updates(map[string]any{"enabled": *in.Enabled, "updated_by": actor.ID, "updated_at": now})
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return fmt.Errorf("watermark settings row missing")
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.watermark.toggle", Target: "video_watermark_settings", Detail: detail, CreatedAt: s.now().UnixMilli()}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "watermark toggle failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": *in.Enabled, "updated_at": now.Format(time.RFC3339)})
}
