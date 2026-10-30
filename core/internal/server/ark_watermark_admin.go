package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/watermark"
	"gorm.io/gorm"
)

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
	c.JSON(http.StatusOK, gin.H{"enabled": enabled, "configured": configured})
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
