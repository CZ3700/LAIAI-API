package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/relay"
)

// 模型元数据（gap P2-D，0026）：厂商/分类/标签/描述/上下文长度/图标。纯展示，不参与计费/路由。

type ModelMeta struct {
	Model         string `gorm:"column:model;primaryKey" json:"model"`
	Vendor        string `gorm:"column:vendor" json:"vendor"`
	Category      string `gorm:"column:category" json:"category"`
	Tags          string `gorm:"column:tags" json:"tags"`
	Description   string `gorm:"column:description" json:"description"`
	ContextLength int    `gorm:"column:context_length" json:"context_length"`
	Icon          string `gorm:"column:icon" json:"icon"`
	UpdatedAt     int64  `gorm:"column:updated_at" json:"updated_at"`
}

func (ModelMeta) TableName() string { return "model_meta" }

// loadModelMetaMap：一次性读出全部元数据，给 catalog/pricing 关联（避免 N+1）。
func (s *Server) loadModelMetaMap() map[string]ModelMeta {
	var metas []ModelMeta
	s.db.Find(&metas)
	m := make(map[string]ModelMeta, len(metas))
	for _, meta := range metas {
		m[meta.Model] = meta
	}
	return m
}

// adminUpsertModelMeta：POST /api/admin/model-meta —— 设/改某模型的展示元数据（upsert by model）。
// 长度上限对齐 schema；context_length 非负。空 model 拒。
func (s *Server) adminUpsertModelMeta(c *gin.Context) {
	var req struct {
		Model         string `json:"model" binding:"required"`
		Vendor        string `json:"vendor"`
		Category      string `json:"category"`
		Tags          string `json:"tags"`
		Description   string `json:"description"`
		ContextLength int    `json:"context_length"`
		Icon          string `json:"icon"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "model is required")
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		fail(c, http.StatusBadRequest, "model is required")
		return
	}
	if len([]rune(req.Model)) > 128 { // PK VARCHAR(128)：超长前置拒，避免 DB 层 500
		fail(c, http.StatusBadRequest, "model name too long (max 128 chars)")
		return
	}
	// 各字段 rune 上限对齐 schema（MySQL VARCHAR(N) 计的是字符数，非字节，故 rune 计数即对）
	if len([]rune(req.Vendor)) > 64 || len([]rune(req.Category)) > 32 ||
		len([]rune(req.Tags)) > 255 || len([]rune(req.Description)) > 500 || len([]rune(req.Icon)) > 128 {
		fail(c, http.StatusBadRequest, "a metadata field exceeds its length limit")
		return
	}
	if req.ContextLength < 0 {
		fail(c, http.StatusBadRequest, "context_length must be >= 0")
		return
	}
	meta := ModelMeta{
		Model: req.Model, Vendor: req.Vendor, Category: req.Category, Tags: req.Tags,
		Description: req.Description, ContextLength: req.ContextLength, Icon: req.Icon,
		UpdatedAt: s.now().UnixMilli(),
	}
	// upsert：主键冲突则全列覆盖（同 adminUpsertPrice 口径）
	if err := s.db.Save(&meta).Error; err != nil {
		fail(c, http.StatusInternalServerError, "save failed")
		return
	}
	s.writeAudit(c, "model_meta_upsert", req.Model, "set model metadata")
	c.JSON(http.StatusOK, gin.H{"model": req.Model})
}

// adminFetchChannelModels：GET /api/admin/channels/:id/fetch-models —— 自动拉模型 + 缺失检测（gap P2-D）。
// 解密渠道凭证 → 拉上游 /v1/models → 与「渠道已配模型」「平台已定价模型」做差集：
//
//	missing_in_config = 上游有、渠道 Models 未列（漏上货）
//	extra_in_config   = 渠道列了、上游已无（可能已下线，建议清理）
//	unpriced          = 上游有、model_prices 无（漏配价）
//
// 只读探测，不改任何库（admin 据此再手动上货/定价，避免自动改动误伤）。
func (s *Server) adminFetchChannelModels(c *gin.Context) {
	if s.relay == nil {
		fail(c, http.StatusServiceUnavailable, "relay not configured")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	var ch relay.Channel
	if e := s.db.First(&ch, id).Error; e != nil {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	res := s.relay.ProbeExistingChannel(c.Request.Context(), &ch)
	if !res.OK {
		msg := res.Message
		if msg == "" {
			msg = "upstream model list fetch failed"
		}
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": msg, "tried": res.Tried})
		return
	}

	configured := map[string]bool{}
	for _, m := range strings.Split(ch.Models, ",") {
		if t := strings.TrimSpace(m); t != "" {
			configured[t] = true
		}
	}
	var prices []relay.ModelPrice
	s.db.WithContext(c.Request.Context()).Find(&prices)
	priced := make(map[string]bool, len(prices))
	for _, p := range prices {
		priced[p.Model] = true
	}

	upstreamSet := map[string]bool{}
	missingInConfig, unpriced := []string{}, []string{}
	for _, m := range res.Models {
		upstreamSet[m] = true
		if !configured[m] {
			missingInConfig = append(missingInConfig, m)
		}
		if !priced[m] {
			unpriced = append(unpriced, m)
		}
	}
	extraInConfig := []string{}
	for m := range configured {
		if !upstreamSet[m] {
			extraInConfig = append(extraInConfig, m)
		}
	}
	sort.Strings(missingInConfig)
	sort.Strings(unpriced)
	sort.Strings(extraInConfig)

	c.JSON(http.StatusOK, gin.H{
		"ok":                true,
		"detected_base_url": res.DetectedBaseURL,
		"upstream":          res.Models,
		"upstream_count":    len(res.Models),
		"missing_in_config": missingInConfig, // 上游有、渠道未配
		"extra_in_config":   extraInConfig,   // 渠道配了、上游已无
		"unpriced":          unpriced,        // 上游有、平台未定价
	})
}
