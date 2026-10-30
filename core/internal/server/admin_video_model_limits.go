package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
)

// 视频模型级受理限额（火山官方式 per-model 配额，0136）：官方 Seedance 模型逐个设置
// 并发上限与 create RPM，仅 superadmin。写入 video_model_configs 的两列，受理侧
// （video.Service）实时生效：并发闸取事务内权威值，RPM 走 5s 热读缓存。

type videoModelLimitView struct {
	Model              string     `json:"model"`
	Published          bool       `json:"published"`
	Enabled            bool       `json:"enabled"`
	ChannelID          uint64     `json:"channel_id"`
	ChannelName        string     `json:"channel_name"`
	MaxConcurrentTasks int        `json:"max_concurrent_tasks"`
	CreateRPM          int        `json:"create_rpm"`
	UpdatedAt          *time.Time `json:"updated_at"`
}

type videoModelLimitsReq struct {
	MaxConcurrentTasks *int `json:"max_concurrent_tasks"`
	CreateRPM          *int `json:"create_rpm"`
}

// officialVideoModels：展示顺序固定（新→旧），与 ark.IsPublicModel 的判定集一致。
func officialVideoModels() []string {
	return []string{ark.Model25, ark.Model20, ark.Model20Fast, ark.Model20Mini}
}

// adminListModelLimits：GET /api/admin/video/model-limits —— 官方模型限额一览
// （含未发布的官方模型，published=false 提示先发布再设限）。
func (s *Server) adminListModelLimits(c *gin.Context) {
	var rows []struct {
		PublicModel        string     `gorm:"column:public_model"`
		ChannelID          uint64     `gorm:"column:channel_id"`
		Enabled            bool       `gorm:"column:enabled"`
		MaxConcurrentTasks int        `gorm:"column:max_concurrent_tasks"`
		CreateRPM          int        `gorm:"column:create_rpm"`
		UpdatedAt          *time.Time `gorm:"column:updated_at"`
	}
	if err := s.db.Table("video_model_configs").
		Select("public_model, channel_id, enabled, max_concurrent_tasks, create_rpm, updated_at").
		Where("public_model IN ?", officialVideoModels()).
		Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "load model limits failed")
		return
	}
	byModel := map[string]int{}
	for i, r := range rows {
		byModel[r.PublicModel] = i
	}
	channelNames := map[uint64]string{}
	ids := map[uint64]bool{}
	for _, r := range rows {
		ids[r.ChannelID] = true
	}
	if len(ids) > 0 {
		var chs []struct {
			ID   uint64 `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		keys := make([]uint64, 0, len(ids))
		for id := range ids {
			keys = append(keys, id)
		}
		if err := s.db.Table("channels").Select("id, name").Where("id IN ?", keys).Find(&chs).Error; err == nil {
			for _, ch := range chs {
				channelNames[ch.ID] = ch.Name
			}
		}
	}
	out := make([]videoModelLimitView, 0, len(officialVideoModels()))
	for _, m := range officialVideoModels() {
		v := videoModelLimitView{Model: m}
		if i, ok := byModel[m]; ok {
			r := rows[i]
			v.Published, v.Enabled, v.ChannelID, v.ChannelName = true, r.Enabled, r.ChannelID, channelNames[r.ChannelID]
			v.MaxConcurrentTasks, v.CreateRPM, v.UpdatedAt = r.MaxConcurrentTasks, r.CreateRPM, r.UpdatedAt
		}
		out = append(out, v)
	}
	c.JSON(http.StatusOK, gin.H{"models": out})
}

// adminSetModelLimits：PUT /api/admin/video/model-limits/:model —— 更新单模型限额。
// 只允许官方公共模型、且已发布（配置行存在）；0=不限制。并发闸下一笔受理即生效，
// RPM 闸经 5s 缓存生效。
func (s *Server) adminSetModelLimits(c *gin.Context) {
	model := c.Param("model")
	if !ark.IsPublicModel(model) {
		fail(c, http.StatusBadRequest, "not an official public model")
		return
	}
	var req videoModelLimitsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MaxConcurrentTasks == nil || req.CreateRPM == nil {
		fail(c, http.StatusBadRequest, "max_concurrent_tasks and create_rpm are required")
		return
	}
	conc, rpm := *req.MaxConcurrentTasks, *req.CreateRPM
	if conc < 0 || conc > 100000 || rpm < 0 || rpm > 1000000 {
		fail(c, http.StatusBadRequest, "limits must be within 0..100000 (concurrency) and 0..1000000 (rpm)")
		return
	}
	updated := time.Now().UTC().Truncate(time.Millisecond)
	res := s.db.Table("video_model_configs").
		Where("public_model = ?", model).
		Updates(map[string]any{"max_concurrent_tasks": conc, "create_rpm": rpm, "updated_at": updated})
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "update model limits failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "model is not published; publish it before setting limits")
		return
	}
	s.writeAudit(c, "video_model_limits", model,
		"max_concurrent_tasks="+strconv.Itoa(conc)+" create_rpm="+strconv.Itoa(rpm))
	c.JSON(http.StatusOK, gin.H{"model": model, "max_concurrent_tasks": conc, "create_rpm": rpm, "updated_at": updated})
}
