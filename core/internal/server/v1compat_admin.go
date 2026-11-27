package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/server/v1compat"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	v1KeyModelProviders    = "v1_model_providers_json"
	v1KeyModelAPIProviders = "v1_model_api_providers_json"
	v1KeyModelTypes        = "v1_model_types_json"
	v1KeyModelExt          = "v1_model_ext_json"
)

type v1ModelExt struct {
	ProviderID        uint64 `json:"provider_id,omitempty"`
	ProviderSet       bool   `json:"provider_set,omitempty"`
	APIProviderID     uint64 `json:"api_provider_id,omitempty"`
	APIProviderSet    bool   `json:"api_provider_set,omitempty"`
	TypeID            uint64 `json:"type_id,omitempty"`
	TypeSet           bool   `json:"type_set,omitempty"`
	OriginalID        string `json:"original_id,omitempty"`
	Mid               string `json:"mid,omitempty"`
	Remark            string `json:"remark,omitempty"`
	FeatureAttributes string `json:"feature_attributes,omitempty"`
	GroupRatios       string `json:"group_ratios,omitempty"`
}

func modelNumericID(model string) uint64 {
	id := uint64(crc32.ChecksumIEEE([]byte(model)) & 0x7fffffff)
	if id == 0 {
		return 1
	}
	return id
}

func derefUint64(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

func sortClassifications(items []v1compat.ClassificationItem) []v1compat.ClassificationItem {
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b v1compat.ClassificationItem) int {
		if c := cmp.Compare(b.SortOrder, a.SortOrder); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return sorted
}

func activeClassifications(items []v1compat.ClassificationItem) ([]v1compat.ClassificationItem, map[uint64]bool) {
	sorted := sortClassifications(items)
	active := make([]v1compat.ClassificationItem, 0, len(sorted))
	validIDs := make(map[uint64]bool, len(sorted))
	for _, it := range sorted {
		if it.IsActive == 1 {
			active = append(active, it)
			validIDs[it.ID] = true
		}
	}
	return active, validIDs
}

func isLibraryModelSource(source string) bool {
	s := strings.TrimSpace(source)
	return strings.EqualFold(s, "library") || strings.EqualFold(s, "system")
}

type classificationFilterQuery struct {
	ProviderID    *uint64
	APIProviderID *uint64
	TypeID        *uint64
	Source        string
	Keyword       string
	IsActive      *int8
	PageSize      int
}

func parseOptionalClassID(c *gin.Context, key string) *uint64 {
	raw, exists := c.GetQuery(key)
	if !exists {
		return nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if trimmed == "0" || trimmed == "-1" {
		zero := uint64(0)
		return &zero
	}
	id, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		invalid := ^uint64(0)
		return &invalid
	}
	return &id
}

func parseClassificationQuery(c *gin.Context) classificationFilterQuery {
	q := classificationFilterQuery{
		ProviderID:    parseOptionalClassID(c, "provider_id"),
		APIProviderID: parseOptionalClassID(c, "api_provider_id"),
		TypeID:        parseOptionalClassID(c, "type_id"),
		Source:        strings.TrimSpace(c.Query("source")),
		Keyword:       strings.TrimSpace(c.Query("keyword")),
	}
	if activeRaw := strings.TrimSpace(c.Query("is_active")); activeRaw != "" {
		switch strings.ToLower(activeRaw) {
		case "1", "true", "active":
			v := int8(1)
			q.IsActive = &v
		case "0", "false", "disabled":
			v := int8(0)
			q.IsActive = &v
		}
	}
	if psRaw := strings.TrimSpace(c.Query("page_size")); psRaw != "" {
		if ps, err := strconv.Atoi(psRaw); err == nil && ps > 0 {
			q.PageSize = min(ps, 10000)
		}
	}
	return q
}

func matchesClassFilter(val uint64, filter *uint64) bool {
	return filter == nil || val == *filter
}

func resolveModelClassificationIDs(
	ext v1ModelExt,
	meta ModelMeta,
	modelName string,
	validProv, validAPIProv, validType map[uint64]bool,
) (provID, apiProvID, typeID uint64, inferredTypeName string) {
	inferredProvID, _, inferredTypeID, inferredTypeName, _ := v1compat.InferModelProviderAndType(meta.Vendor, meta.Category, modelName)
	switch {
	case ext.ProviderSet:
		provID = ext.ProviderID
	case ext.ProviderID > 0:
		provID = ext.ProviderID
	default:
		provID = inferredProvID
	}
	if provID > 0 && validProv != nil && !validProv[provID] {
		provID = 0
	}

	switch {
	case ext.APIProviderSet:
		apiProvID = ext.APIProviderID
	case ext.APIProviderID > 0:
		apiProvID = ext.APIProviderID
	default:
		apiProvID = provID
	}
	if apiProvID > 0 && validAPIProv != nil && !validAPIProv[apiProvID] {
		apiProvID = 0
	}

	switch {
	case ext.TypeSet:
		typeID = ext.TypeID
	case ext.TypeID > 0:
		typeID = ext.TypeID
	default:
		typeID = inferredTypeID
	}
	if typeID > 0 && validType != nil && !validType[typeID] {
		typeID = 0
	}
	return provID, apiProvID, typeID, inferredTypeName
}

func (s *Server) loadClassifications(settingKey string, defaults []v1compat.ClassificationItem) []v1compat.ClassificationItem {
	raw := s.getSetting(settingKey)
	if strings.TrimSpace(raw) == "" {
		return slices.Clone(defaults)
	}
	var list []v1compat.ClassificationItem
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) == 0 {
		return slices.Clone(defaults)
	}
	defByID := make(map[uint64]v1compat.ClassificationItem, len(defaults))
	defByName := make(map[string]v1compat.ClassificationItem, len(defaults))
	seenID := make(map[uint64]bool, len(list))
	seenName := make(map[string]bool, len(list))
	for _, d := range defaults {
		defByID[d.ID] = d
		defByName[d.Name] = d
	}
	for i := range list {
		seenID[list[i].ID] = true
		seenName[list[i].Name] = true
		def, ok := defByID[list[i].ID]
		if !ok {
			def, ok = defByName[list[i].Name]
		}
		if ok {
			if def.IsSystem == 1 && def.DefaultFeatures != "" {
				list[i].DefaultFeatures = def.DefaultFeatures
				list[i].IsSystem = 1
			} else if list[i].DefaultFeatures == "" && def.DefaultFeatures != "" {
				list[i].DefaultFeatures = def.DefaultFeatures
			}
			if list[i].IsSystem == 0 && def.IsSystem != 0 {
				list[i].IsSystem = def.IsSystem
			}
			if list[i].NameEn == "" && def.NameEn != "" {
				list[i].NameEn = def.NameEn
			}
			if list[i].Logo == "" && def.Logo != "" {
				list[i].Logo = def.Logo
			}
		}
	}
	for _, d := range defaults {
		if d.IsSystem == 1 && !seenID[d.ID] && !seenName[d.Name] {
			list = append(list, d)
		}
	}
	return list
}

type modelClassificationCountInput struct {
	Model         string
	Name          string
	ProviderID    uint64
	APIProviderID uint64
	TypeID        uint64
	IsListed      bool
}

func (s *Server) loadAllModelClassificationInputs(c *gin.Context) []modelClassificationCountInput {
	ctx := c.Request.Context()
	rdb := s.readOnly().WithContext(ctx)
	var bindings []pricing.ModelRuleBinding
	_ = rdb.Select("model, display_name, is_listed").Find(&bindings).Error
	var prices []relay.ModelPrice
	_ = rdb.Select("model").Find(&prices).Error
	var metas []ModelMeta
	_ = rdb.Select("model, vendor, category").Find(&metas).Error
	metaMap := make(map[string]ModelMeta, len(metas))
	for _, m := range metas {
		metaMap[m.Model] = m
	}
	_, validProv := activeClassifications(s.listModelProvidersData())
	_, validAPIProv := activeClassifications(s.listModelAPIProvidersData())
	_, validType := activeClassifications(s.listModelTypesData())

	seen := make(map[string]struct{}, len(bindings)+len(prices))
	extMap := s.loadV1ModelExtMap()
	out := make([]modelClassificationCountInput, 0, len(bindings)+len(prices))
	addModel := func(modelName, displayName string, isListed bool) {
		if _, ok := seen[modelName]; ok {
			return
		}
		seen[modelName] = struct{}{}
		ext := extMap[modelName]
		meta := metaMap[modelName]
		pid, apid, tid, _ := resolveModelClassificationIDs(ext, meta, modelName, validProv, validAPIProv, validType)
		out = append(out, modelClassificationCountInput{
			Model:         modelName,
			Name:          cmp.Or(displayName, modelName),
			ProviderID:    pid,
			APIProviderID: apid,
			TypeID:        tid,
			IsListed:      isListed,
		})
	}
	for _, b := range bindings {
		addModel(b.Model, b.DisplayName, b.IsListed == 1)
	}
	for _, p := range prices {
		addModel(p.Model, p.Model, true)
	}
	return out
}

func (s *Server) listModelProvidersData() []v1compat.ClassificationItem {
	return s.loadClassifications(v1KeyModelProviders, v1compat.StandardProviders())
}

func (s *Server) listModelAPIProvidersData() []v1compat.ClassificationItem {
	return s.loadClassifications(v1KeyModelAPIProviders, v1compat.StandardProviders())
}

func (s *Server) listModelTypesData() []v1compat.ClassificationItem {
	return s.loadClassifications(v1KeyModelTypes, v1compat.StandardModelTypes())
}

func (s *Server) v1ModelProviders(c *gin.Context) {
	items := sortClassifications(s.listModelProvidersData())
	models := s.loadAllModelClassificationInputs(c)
	counts := map[uint64]int64{}
	for _, m := range models {
		if m.IsListed {
			counts[m.ProviderID]++
		}
	}
	for i := range items {
		items[i].ModelCount = counts[items[i].ID]
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) v1ModelAPIProviders(c *gin.Context) {
	items := sortClassifications(s.listModelAPIProvidersData())
	models := s.loadAllModelClassificationInputs(c)
	counts := map[uint64]int64{}
	for _, m := range models {
		if m.IsListed {
			counts[m.APIProviderID]++
		}
	}
	for i := range items {
		items[i].ModelCount = counts[items[i].ID]
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) v1ModelTypes(c *gin.Context) {
	items := sortClassifications(s.listModelTypesData())
	models := s.loadAllModelClassificationInputs(c)
	counts := map[uint64]int64{}
	for _, m := range models {
		if m.IsListed {
			counts[m.TypeID]++
		}
	}
	for i := range items {
		items[i].ModelCount = counts[items[i].ID]
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) handleSaveClassification(c *gin.Context, settingKey string, defaults []v1compat.ClassificationItem) {
	var req struct {
		Name            string  `json:"name" binding:"required"`
		NameEn          string  `json:"name_en"`
		Logo            string  `json:"logo"`
		SortOrder       int     `json:"sort_order"`
		IsActive        *int8   `json:"is_active"`
		Remark          *string `json:"remark"`
		DefaultFeatures *string `json:"default_features"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	list := s.loadClassifications(settingKey, defaults)
	active := int8(1)
	if req.IsActive != nil && *req.IsActive == 0 {
		active = 0
	}
	if idRaw := strings.TrimSpace(c.Param("id")); idRaw != "" {
		if id, err := strconv.ParseUint(idRaw, 10, 64); err == nil {
			for i := range list {
				if list[i].ID == id {
					if list[i].IsSystem == 0 {
						list[i].Name = strings.TrimSpace(req.Name)
					}
					list[i].NameEn = strings.TrimSpace(req.NameEn)
					list[i].Logo = strings.TrimSpace(req.Logo)
					list[i].SortOrder = req.SortOrder
					list[i].IsActive = active
					if req.Remark != nil {
						list[i].Remark = strings.TrimSpace(*req.Remark)
					}
					if req.DefaultFeatures != nil && list[i].IsSystem == 0 {
						list[i].DefaultFeatures = strings.TrimSpace(*req.DefaultFeatures)
					}
					b, _ := json.Marshal(list)
					_ = s.upsertRawSetting(settingKey, string(b))
					c.JSON(http.StatusOK, list[i])
					return
				}
			}
		}
	}
	var maxID uint64
	for _, it := range list {
		if it.ID > maxID {
			maxID = it.ID
		}
	}
	item := v1compat.ClassificationItem{
		ID:        maxID + 1,
		Name:      strings.TrimSpace(req.Name),
		NameEn:    strings.TrimSpace(req.NameEn),
		Logo:      strings.TrimSpace(req.Logo),
		SortOrder: req.SortOrder,
		IsActive:  active,
	}
	if req.Remark != nil {
		item.Remark = strings.TrimSpace(*req.Remark)
	}
	if req.DefaultFeatures != nil {
		item.DefaultFeatures = strings.TrimSpace(*req.DefaultFeatures)
	}
	list = append(list, item)
	b, _ := json.Marshal(list)
	_ = s.upsertRawSetting(settingKey, string(b))
	c.JSON(http.StatusCreated, item)
}

func (s *Server) handleDeleteClassification(c *gin.Context, settingKey string, defaults []v1compat.ClassificationItem) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	list := s.loadClassifications(settingKey, defaults)
	for _, it := range list {
		if it.ID == id && it.IsSystem == 1 {
			fail(c, http.StatusBadRequest, "系统预制分类不可删除")
			return
		}
	}
	next := slices.DeleteFunc(list, func(it v1compat.ClassificationItem) bool { return it.ID == id })
	b, _ := json.Marshal(next)
	_ = s.upsertRawSetting(settingKey, string(b))

	extMap := s.loadV1ModelExtMap()
	extChanged := false
	for modelName, ext := range extMap {
		rowUpdated := false
		switch settingKey {
		case v1KeyModelProviders:
			if ext.ProviderID == id {
				ext.ProviderID = 0
				ext.ProviderSet = true
				rowUpdated = true
			}
		case v1KeyModelAPIProviders:
			if ext.APIProviderID == id {
				ext.APIProviderID = 0
				ext.APIProviderSet = true
				rowUpdated = true
			}
		case v1KeyModelTypes:
			if ext.TypeID == id {
				ext.TypeID = 0
				ext.TypeSet = true
				rowUpdated = true
			}
		}
		if rowUpdated {
			extMap[modelName] = ext
			extChanged = true
		}
	}
	if extChanged {
		if extBytes, err := json.Marshal(extMap); err == nil {
			_ = s.upsertRawSetting(v1KeyModelExt, string(extBytes))
		}
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1SaveModelProvider(c *gin.Context) {
	s.handleSaveClassification(c, v1KeyModelProviders, v1compat.StandardProviders())
}
func (s *Server) v1DeleteModelProvider(c *gin.Context) {
	s.handleDeleteClassification(c, v1KeyModelProviders, v1compat.StandardProviders())
}
func (s *Server) v1SaveModelAPIProvider(c *gin.Context) {
	s.handleSaveClassification(c, v1KeyModelAPIProviders, v1compat.StandardProviders())
}
func (s *Server) v1DeleteModelAPIProvider(c *gin.Context) {
	s.handleDeleteClassification(c, v1KeyModelAPIProviders, v1compat.StandardProviders())
}
func (s *Server) v1SaveModelType(c *gin.Context) {
	s.handleSaveClassification(c, v1KeyModelTypes, v1compat.StandardModelTypes())
}
func (s *Server) v1DeleteModelType(c *gin.Context) {
	s.handleDeleteClassification(c, v1KeyModelTypes, v1compat.StandardModelTypes())
}

func (s *Server) v1ClassificationsStats(c *gin.Context) {
	q := parseClassificationQuery(c)
	providers, _ := activeClassifications(s.listModelProvidersData())
	apiProviders, _ := activeClassifications(s.listModelAPIProvidersData())
	types, _ := activeClassifications(s.listModelTypesData())
	models := s.loadAllModelClassificationInputs(c)

	var customCount, libraryCount int64
	provCounts := map[uint64]int64{}
	apiProvCounts := map[uint64]int64{}
	typeCounts := map[uint64]int64{}
	var unProv, unAPIProv, unType int64
	wantListed := !isLibraryModelSource(q.Source)

	for _, m := range models {
		matchProv := matchesClassFilter(m.ProviderID, q.ProviderID)
		matchAPIProv := matchesClassFilter(m.APIProviderID, q.APIProviderID)
		matchType := matchesClassFilter(m.TypeID, q.TypeID)

		if matchProv && matchAPIProv && matchType {
			if m.IsListed {
				customCount++
			} else {
				libraryCount++
			}
		}

		if m.IsListed != wantListed {
			continue
		}

		// 交叉统计：每个维度排除自身条件，仅保留另外两个维度的已选筛选
		if matchAPIProv && matchType {
			if m.ProviderID > 0 {
				provCounts[m.ProviderID]++
			} else {
				unProv++
			}
		}
		if matchProv && matchType {
			if m.APIProviderID > 0 {
				apiProvCounts[m.APIProviderID]++
			} else {
				unAPIProv++
			}
		}
		if matchProv && matchAPIProv {
			if m.TypeID > 0 {
				typeCounts[m.TypeID]++
			} else {
				unType++
			}
		}
	}

	toStats := func(items []v1compat.ClassificationItem, counts map[uint64]int64) []gin.H {
		out := make([]gin.H, 0, len(items))
		for _, it := range items {
			out = append(out, gin.H{
				"id":        it.ID,
				"name":      it.Name,
				"name_en":   it.NameEn,
				"logo":      it.Logo,
				"is_system": it.IsSystem,
				"count":     counts[it.ID],
			})
		}
		return out
	}
	c.JSON(http.StatusOK, gin.H{
		"providers":                  toStats(providers, provCounts),
		"api_providers":              toStats(apiProviders, apiProvCounts),
		"types":                      toStats(types, typeCounts),
		"unclassified_providers":     unProv,
		"unclassified_api_providers": unAPIProv,
		"unclassified_types":         unType,
		"source_counts": gin.H{
			"custom":  customCount,
			"library": libraryCount,
		},
	})
}

func (s *Server) loadV1ModelExtMap() map[string]v1ModelExt {
	raw := s.getSetting(v1KeyModelExt)
	out := map[string]v1ModelExt{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

func (s *Server) saveV1ModelExt(model string, ext v1ModelExt) {
	m := s.loadV1ModelExtMap()
	m[model] = ext
	if b, err := json.Marshal(m); err == nil {
		_ = s.upsertRawSetting(v1KeyModelExt, string(b))
	}
}

func (s *Server) resolveModelNameByParam(c *gin.Context) string {
	param := strings.TrimSpace(c.Param("id"))
	if param == "" {
		return ""
	}
	if numID, err := strconv.ParseUint(param, 10, 64); err == nil && numID > 0 {
		var bindings []pricing.ModelRuleBinding
		_ = s.readOnly().WithContext(c.Request.Context()).Select("model").Find(&bindings).Error
		for _, b := range bindings {
			if modelNumericID(b.Model) == numID {
				return b.Model
			}
		}
		var prices []relay.ModelPrice
		_ = s.readOnly().WithContext(c.Request.Context()).Select("model").Find(&prices).Error
		for _, p := range prices {
			if modelNumericID(p.Model) == numID {
				return p.Model
			}
		}
	}
	return param
}

func (s *Server) v1ListChannelCategories(c *gin.Context) {
	rows := []forward.ChannelCategory{}
	if err := s.readOnly().WithContext(c.Request.Context()).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list channel categories failed")
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) v1ListBillingRules(c *gin.Context) {
	ctx := c.Request.Context()
	_ = v1compat.EnsurePresetRules(ctx, s.db)
	rows := []pricing.BillingRule{}
	if err := s.readOnly().WithContext(ctx).Order("sort_order DESC, id DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list billing rules failed")
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) v1RestoreDefaultBillingRule(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var existing pricing.BillingRule
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		fail(c, http.StatusNotFound, "billing rule not found")
		return
	}
	preset, found := v1compat.PresetBillingRuleByPIDOrName(existing.PID, existing.Name)
	if !found {
		fail(c, http.StatusBadRequest, "no system default exists for this billing rule")
		return
	}
	updates := map[string]any{
		"billing_type":               preset.BillingType,
		"billing_rule":               preset.BillingRule,
		"pricing_type":               preset.PricingType,
		"prompt_rate":                preset.PromptRate,
		"completion_rate":            preset.CompletionRate,
		"cached_rate":                preset.CachedRate,
		"claude_cache_creation_rate": preset.ClaudeCacheCreationRate,
		"claude_cache_read_rate":     preset.ClaudeCacheReadRate,
		"fixed_rate":                 preset.FixedRate,
		"duration_rate":              preset.DurationRate,
		"pricing_tiers":              preset.PricingTiers,
		"extended_config":            preset.ExtendedConfig,
	}
	if err := s.db.WithContext(ctx).Model(&pricing.BillingRule{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "restore billing rule failed")
		return
	}
	_ = s.db.WithContext(ctx).First(&existing, id).Error
	s.invalidateCache(relay.KindBillingRule, "")
	c.JSON(http.StatusOK, gin.H{"rule": existing, "message": "已恢复系统默认配置"})
}

func (s *Server) v1ListForwardRules(c *gin.Context) {
	ctx := c.Request.Context()
	_ = v1compat.EnsurePresetRules(ctx, s.db)
	rows := []forward.ForwardRule{}
	if err := s.readOnly().WithContext(ctx).Order("sort_order DESC, id DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list forward rules failed")
		return
	}
	out := make([]forward.ForwardRule, 0, len(rows))
	for _, r := range rows {
		if !v1compat.ShouldHidePluginGatedForwardRule(r) {
			out = append(out, r)
		}
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) v1AdminListModels(c *gin.Context) {
	ctx := c.Request.Context()
	rdb := s.readOnly().WithContext(ctx)
	q := parseClassificationQuery(c)

	var bindings []pricing.ModelRuleBinding
	if err := rdb.Order("sort_order DESC, model ASC").Find(&bindings).Error; err != nil && !v1compat.IsMissingTable(err) {
		fail(c, http.StatusInternalServerError, "list model bindings failed")
		return
	}
	var prices []relay.ModelPrice
	_ = rdb.Order("model ASC").Find(&prices).Error
	var metas []ModelMeta
	_ = rdb.Find(&metas).Error
	var billingRules []pricing.BillingRule
	_ = rdb.Find(&billingRules).Error
	var forwardRules []forward.ForwardRule
	_ = rdb.Find(&forwardRules).Error

	priceMap := make(map[string]relay.ModelPrice, len(prices))
	for _, p := range prices {
		priceMap[p.Model] = p
	}
	metaMap := make(map[string]ModelMeta, len(metas))
	for _, m := range metas {
		metaMap[m.Model] = m
	}
	brMap := make(map[uint64]pricing.BillingRule, len(billingRules))
	for _, br := range billingRules {
		brMap[br.ID] = br
	}
	frMap := make(map[uint64]forward.ForwardRule, len(forwardRules))
	for _, fr := range forwardRules {
		frMap[fr.ID] = fr
	}
	bindingMap := make(map[string]pricing.ModelRuleBinding, len(bindings))
	for _, b := range bindings {
		bindingMap[b.Model] = b
	}

	seen := make(map[string]struct{}, len(bindings)+len(prices))
	names := make([]string, 0, len(bindings)+len(prices))
	for _, b := range bindings {
		seen[b.Model] = struct{}{}
		names = append(names, b.Model)
	}
	for _, p := range prices {
		if _, ok := seen[p.Model]; !ok {
			seen[p.Model] = struct{}{}
			names = append(names, p.Model)
		}
	}

	extMap := s.loadV1ModelExtMap()
	_, validProv := activeClassifications(s.listModelProvidersData())
	_, validAPIProv := activeClassifications(s.listModelAPIProvidersData())
	typesList, validType := activeClassifications(s.listModelTypesData())
	typeNameByID := make(map[uint64]string, len(typesList))
	for _, tp := range typesList {
		typeNameByID[tp.ID] = tp.Name
	}

	wantListed := !isLibraryModelSource(q.Source)
	kwLower := strings.ToLower(q.Keyword)
	out := make([]gin.H, 0, len(names))
	for _, modelName := range names {
		b, hasB := bindingMap[modelName]
		p := priceMap[modelName]
		meta := metaMap[modelName]

		displayName := modelName
		isListed := true
		isActive := true
		sortOrder := 0
		preDed := decimal.Zero
		siteDisc := decimal.NewFromInt(1)
		siteDiscEn := false
		if hasB {
			displayName = cmp.Or(strings.TrimSpace(b.DisplayName), modelName)
			isListed = b.IsListed == 1
			isActive = b.IsActive == 1
			sortOrder = b.SortOrder
			preDed = b.PreDeduction
			if b.SiteDiscount.IsPositive() {
				siteDisc = b.SiteDiscount
			}
			siteDiscEn = b.SiteDiscountEnabled == 1
		}

		if isListed != wantListed {
			continue
		}
		if q.IsActive != nil && b2i8(isActive) != *q.IsActive {
			continue
		}

		ext := extMap[modelName]
		provID, apiProvID, typeID, inferredTypeName := resolveModelClassificationIDs(ext, meta, modelName, validProv, validAPIProv, validType)
		if !matchesClassFilter(provID, q.ProviderID) || !matchesClassFilter(apiProvID, q.APIProviderID) || !matchesClassFilter(typeID, q.TypeID) {
			continue
		}

		numID := modelNumericID(modelName)
		mid := cmp.Or(ext.Mid, "m-"+strconv.FormatUint(numID, 10))
		origID := cmp.Or(ext.OriginalID, modelName)
		if kwLower != "" {
			if !strings.Contains(strings.ToLower(modelName), kwLower) &&
				!strings.Contains(strings.ToLower(displayName), kwLower) &&
				!strings.Contains(strings.ToLower(b.ModelAlias), kwLower) &&
				!strings.Contains(strings.ToLower(mid), kwLower) &&
				!strings.Contains(strings.ToLower(ext.Remark), kwLower) &&
				!strings.Contains(strings.ToLower(origID), kwLower) {
				continue
			}
		}

		fwdIDs := []uint64{}
		fwdNames := []string{}
		if hasB {
			fwdIDs = v1compat.ParseRuleIDList(b.ForwardRuleIDs)
			for _, fid := range fwdIDs {
				if fr, ok := frMap[fid]; ok {
					fwdNames = append(fwdNames, fmt.Sprintf("%s (%s)", fr.Name, fr.EID))
				}
			}
		}
		var brName, brPID string
		if hasB && b.BillingRuleID > 0 {
			if br, ok := brMap[b.BillingRuleID]; ok {
				brName = br.Name
				brPID = br.PID
			}
		}

		typeName := cmp.Or(typeNameByID[typeID], inferredTypeName, "聊天")
		featAttrs := cmp.Or(ext.FeatureAttributes, "[]")
		fwdJSON := b.ForwardRuleIDs
		if strings.TrimSpace(fwdJSON) == "" {
			fwdJSON = "[]"
		}

		var globalDisc decimal.Decimal = decimal.NewFromInt(1)
		var globalDiscEn bool
		var discountSchedule string
		if hasB {
			if b.GlobalDiscount.IsPositive() {
				globalDisc = b.GlobalDiscount
			}
			globalDiscEn = b.GlobalDiscountEnabled == 1
			discountSchedule = b.DiscountSchedule
		}
		var brObj any
		if hasB && b.BillingRuleID > 0 {
			if br, ok := brMap[b.BillingRuleID]; ok {
				brObj = br
			}
		}

		row := gin.H{
			"id":                      numID,
			"mid":                     mid,
			"model":                   modelName,
			"model_id":                modelName,
			"original_id":             origID,
			"model_id_alias":          b.ModelAlias,
			"name":                    displayName,
			"model_alias":             b.ModelAlias,
			"provider_id":             provID,
			"api_provider_id":         apiProvID,
			"type_id":                 typeID,
			"type_name":               typeName,
			"vendor":                  meta.Vendor,
			"category":                cmp.Or(meta.Category, typeName),
			"description":             meta.Description,
			"remark":                  ext.Remark,
			"feature_attributes":      featAttrs,
			"feature_keys":            v1compat.ParseFeatureAttributes(featAttrs),
			"group_ratios":            cmp.Or(ext.GroupRatios, "{}"),
			"context_length":          meta.ContextLength,
			"logo":                    meta.Icon,
			"icon":                    meta.Icon,
			"billing_rule_id":         b.BillingRuleID,
			"billing_rule_name":       brName,
			"billing_rule_pid":        brPID,
			"billing_rule":            brObj,
			"forward_rule_ids":        fwdJSON,
			"forward_rule_id_list":    fwdIDs,
			"forward_rule_names":      fwdNames,
			"pre_deduction":           preDed,
			"site_discount":           siteDisc,
			"site_discount_enabled":   map[bool]int{true: 1, false: 0}[siteDiscEn],
			"global_discount":         globalDisc,
			"global_discount_enabled": map[bool]int{true: 1, false: 0}[globalDiscEn],
			"discount_schedule":       discountSchedule,
			"enable_log_content":      b.EnableLogContent,
			"input_usd_per_1m":        p.InputUSDPer1M,
			"output_usd_per_1m":       p.OutputUSDPer1M,
			"image_usd":               p.ImageUSD,
			"per_call_usd":            p.PerCallUSD,
			"is_listed":               map[bool]int{true: 1, false: 0}[isListed],
			"is_active":               map[bool]int{true: 1, false: 0}[isActive],
			"sort_order":              sortOrder,
			"created_at":              b.CreatedAt,
		}
		if !isListed {
			row["library_kind"] = "unlisted"
		}
		out = append(out, row)
	}
	total := len(out)
	if q.PageSize > 0 && len(out) > q.PageSize {
		out = out[:q.PageSize]
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": total})
}

func (s *Server) v1AdminUpsertModel(c *gin.Context) {
	ctx := c.Request.Context()
	rawBody, readErr := io.ReadAll(c.Request.Body)
	if readErr != nil || len(bytes.TrimSpace(rawBody)) == 0 {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &rawFields); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	var req struct {
		Model                 string           `json:"model"`
		ModelID               string           `json:"model_id"`
		OriginalID            *string          `json:"original_id"`
		Name                  string           `json:"name"`
		ModelAlias            string           `json:"model_alias"`
		ModelIDAlias          string           `json:"model_id_alias"`
		Vendor                string           `json:"vendor"`
		Category              string           `json:"category"`
		Description           string           `json:"description"`
		Remark                *string          `json:"remark"`
		FeatureAttributes     *string          `json:"feature_attributes"`
		GroupRatios           *string          `json:"group_ratios"`
		ContextLength         int              `json:"context_length"`
		Icon                  string           `json:"icon"`
		Logo                  string           `json:"logo"`
		ProviderID            *uint64          `json:"provider_id"`
		APIProviderID         *uint64          `json:"api_provider_id"`
		TypeID                *uint64          `json:"type_id"`
		BillingRuleID         uint64           `json:"billing_rule_id"`
		ForwardRuleIDsRaw     json.RawMessage  `json:"forward_rule_ids"`
		PreDeduction          *decimal.Decimal `json:"pre_deduction"`
		SiteDiscount          *decimal.Decimal `json:"site_discount"`
		SiteDiscountEnabled   *flexBool        `json:"site_discount_enabled"`
		GlobalDiscount        *decimal.Decimal `json:"global_discount"`
		GlobalDiscountEnabled *flexBool        `json:"global_discount_enabled"`
		DiscountSchedule      *string          `json:"discount_schedule"`
		EnableLogContent      *flexBool        `json:"enable_log_content"`
		InputUSDPer1M         *decimal.Decimal `json:"input_usd_per_1m"`
		OutputUSDPer1M        *decimal.Decimal `json:"output_usd_per_1m"`
		ImageUSD              *decimal.Decimal `json:"image_usd"`
		PerCallUSD            *decimal.Decimal `json:"per_call_usd"`
		IsListed              *flexBool        `json:"is_listed"`
		IsActive              *flexBool        `json:"is_active"`
		SortOrder             int              `json:"sort_order"`
	}
	if err := json.Unmarshal(rawBody, &req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	resolvedParam := s.resolveModelNameByParam(c)
	model := cmp.Or(resolvedParam, strings.TrimSpace(req.Model), strings.TrimSpace(req.ModelID))
	if model == "" {
		fail(c, http.StatusBadRequest, "model is required")
		return
	}

	var parsedFwds []uint64
	if raw := strings.TrimSpace(string(req.ForwardRuleIDsRaw)); raw != "" && raw != "null" {
		if strings.HasPrefix(raw, `"`) {
			var str string
			if json.Unmarshal(req.ForwardRuleIDsRaw, &str) == nil {
				parsedFwds = v1compat.ParseRuleIDList(str)
			}
		} else {
			_ = json.Unmarshal(req.ForwardRuleIDsRaw, &parsedFwds)
		}
	}

	db := s.db.WithContext(ctx)
	if req.BillingRuleID > 0 {
		var cnt int64
		if err := db.Table("billing_rules").Where("id = ?", req.BillingRuleID).Count(&cnt).Error; err != nil || cnt == 0 {
			fail(c, http.StatusBadRequest, "referenced billing_rule_id not found")
			return
		}
	}
	uniqueFwds := deduplicateUint64s(parsedFwds)
	if len(uniqueFwds) > 0 {
		var cnt int64
		if err := db.Table("forward_rules").Where("id IN ?", uniqueFwds).Count(&cnt).Error; err != nil || cnt != int64(len(uniqueFwds)) {
			fail(c, http.StatusBadRequest, "referenced forward_rule_ids contain unknown id")
			return
		}
	}

	var existingB pricing.ModelRuleBinding
	_ = db.Where("model = ?", model).First(&existingB).Error
	var existingMeta ModelMeta
	_ = db.Where("model = ?", model).First(&existingMeta).Error

	_, hasVendorField := rawFields["vendor"]
	_, hasCategoryField := rawFields["category"]
	_, hasDescField := rawFields["description"]
	_, hasContextLenField := rawFields["context_length"]
	_, hasLogoField := rawFields["logo"]
	_, hasIconField := rawFields["icon"]
	_, hasSortOrderField := rawFields["sort_order"]

	err := db.Transaction(func(tx *gorm.DB) error {
		dVal := func(p *decimal.Decimal) decimal.Decimal {
			if p == nil || p.IsNegative() {
				return decimal.Zero
			}
			return *p
		}
		if req.InputUSDPer1M != nil || req.OutputUSDPer1M != nil || req.ImageUSD != nil || req.PerCallUSD != nil || req.BillingRuleID > 0 {
			var existingPrice relay.ModelPrice
			_ = tx.Where("model = ?", model).First(&existingPrice).Error
			p := relay.ModelPrice{
				Model:          model,
				InputUSDPer1M:  existingPrice.InputUSDPer1M,
				OutputUSDPer1M: existingPrice.OutputUSDPer1M,
				ImageUSD:       existingPrice.ImageUSD,
				PerCallUSD:     existingPrice.PerCallUSD,
			}
			if req.InputUSDPer1M != nil {
				p.InputUSDPer1M = dVal(req.InputUSDPer1M)
			}
			if req.OutputUSDPer1M != nil {
				p.OutputUSDPer1M = dVal(req.OutputUSDPer1M)
			}
			if req.ImageUSD != nil {
				p.ImageUSD = dVal(req.ImageUSD)
			}
			if req.PerCallUSD != nil {
				p.PerCallUSD = dVal(req.PerCallUSD)
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&p).Error; err != nil {
				return err
			}
		}
		vendor := existingMeta.Vendor
		if hasVendorField {
			vendor = strings.TrimSpace(req.Vendor)
		}
		category := existingMeta.Category
		if hasCategoryField {
			category = strings.TrimSpace(req.Category)
		}
		description := existingMeta.Description
		if hasDescField {
			description = strings.TrimSpace(req.Description)
		}
		contextLength := existingMeta.ContextLength
		if hasContextLenField {
			contextLength = max(0, req.ContextLength)
		}
		icon := existingMeta.Icon
		if hasLogoField || hasIconField {
			icon = cmp.Or(strings.TrimSpace(req.Logo), strings.TrimSpace(req.Icon))
		}
		meta := ModelMeta{
			Model:         model,
			Vendor:        vendor,
			Category:      category,
			Description:   description,
			ContextLength: contextLength,
			Icon:          icon,
			UpdatedAt:     s.now().UnixMilli(),
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&meta).Error; err != nil {
			return err
		}

		fwdStr := cmp.Or(existingB.ForwardRuleIDs, "[]")
		if req.ForwardRuleIDsRaw != nil {
			fwdBytes, _ := json.Marshal(uniqueFwds)
			fwdStr = string(fwdBytes)
		}
		preDed := existingB.PreDeduction
		if req.PreDeduction != nil {
			preDed = dVal(req.PreDeduction)
		}
		siteDisc := cmp.Or(existingB.SiteDiscount, decimal.NewFromInt(1))
		if req.SiteDiscount != nil && req.SiteDiscount.IsPositive() {
			siteDisc = *req.SiteDiscount
		}
		isListed := int8(1)
		if existingB.Model != "" {
			isListed = existingB.IsListed
		}
		if req.IsListed != nil {
			isListed = b2i8(bool(*req.IsListed))
		}
		isActive := int8(1)
		if existingB.Model != "" {
			isActive = existingB.IsActive
		}
		if req.IsActive != nil {
			isActive = b2i8(bool(*req.IsActive))
		}
		siteEn := existingB.SiteDiscountEnabled
		if req.SiteDiscountEnabled != nil {
			siteEn = b2i8(bool(*req.SiteDiscountEnabled))
		}
		logContent := existingB.EnableLogContent
		if req.EnableLogContent != nil {
			logContent = b2i8(bool(*req.EnableLogContent))
		}
		alias := cmp.Or(strings.TrimSpace(req.ModelIDAlias), strings.TrimSpace(req.ModelAlias), existingB.ModelAlias)
		bRuleID := req.BillingRuleID
		if bRuleID == 0 && req.Model == "" && req.ModelID == "" && existingB.BillingRuleID > 0 {
			bRuleID = existingB.BillingRuleID
		}
		globalDisc := cmp.Or(existingB.GlobalDiscount, decimal.NewFromInt(1))
		if req.GlobalDiscount != nil && req.GlobalDiscount.IsPositive() {
			globalDisc = *req.GlobalDiscount
		}
		globalEn := existingB.GlobalDiscountEnabled
		if req.GlobalDiscountEnabled != nil {
			globalEn = b2i8(bool(*req.GlobalDiscountEnabled))
		}
		discSched := existingB.DiscountSchedule
		if req.DiscountSchedule != nil {
			discSched = *req.DiscountSchedule
		}
		sortOrder := existingB.SortOrder
		if hasSortOrderField {
			sortOrder = req.SortOrder
		}
		binding := pricing.ModelRuleBinding{
			Model:                 model,
			DisplayName:           cmp.Or(strings.TrimSpace(req.Name), existingB.DisplayName, model),
			ModelAlias:            alias,
			BillingRuleID:         bRuleID,
			ForwardRuleIDs:        fwdStr,
			PreDeduction:          preDed,
			EnableLogContent:      logContent,
			SiteDiscount:          siteDisc,
			SiteDiscountEnabled:   siteEn,
			GlobalDiscount:        globalDisc,
			GlobalDiscountEnabled: globalEn,
			DiscountSchedule:      discSched,
			IsListed:              isListed,
			IsActive:              isActive,
			SortOrder:             sortOrder,
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&binding).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "save model failed")
		return
	}

	extMap := s.loadV1ModelExtMap()
	ext := extMap[model]
	if _, ok := rawFields["provider_id"]; ok {
		ext.ProviderSet = true
		ext.ProviderID = derefUint64(req.ProviderID)
	}
	if _, ok := rawFields["api_provider_id"]; ok {
		ext.APIProviderSet = true
		ext.APIProviderID = derefUint64(req.APIProviderID)
	}
	if _, ok := rawFields["type_id"]; ok {
		ext.TypeSet = true
		ext.TypeID = derefUint64(req.TypeID)
	}
	if req.OriginalID != nil {
		ext.OriginalID = strings.TrimSpace(*req.OriginalID)
	}
	if req.Remark != nil {
		ext.Remark = strings.TrimSpace(*req.Remark)
	}
	if req.FeatureAttributes != nil {
		ext.FeatureAttributes = cmp.Or(strings.TrimSpace(*req.FeatureAttributes), "[]")
	}
	if req.GroupRatios != nil {
		ext.GroupRatios = cmp.Or(strings.TrimSpace(*req.GroupRatios), "{}")
	}
	s.saveV1ModelExt(model, ext)

	s.writeAudit(c, "v1_model_upsert", model, "saved model metadata & rule binding")
	s.invalidateCache(relay.KindPrice, model)
	s.invalidateCache(relay.KindForwardRule, model)
	s.invalidateCache(relay.KindBillingRule, model)
	c.JSON(http.StatusOK, gin.H{"id": modelNumericID(model), "model": model})
}

func (s *Server) v1AdminDeleteModel(c *gin.Context) {
	model := s.resolveModelNameByParam(c)
	if model == "" {
		fail(c, http.StatusBadRequest, "model required")
		return
	}
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model = ?", model).Delete(&pricing.ModelRuleBinding{}).Error; err != nil && !v1compat.IsMissingTable(err) {
			return err
		}
		_ = tx.Where("model = ?", model).Delete(&ModelMeta{}).Error
		return tx.Where("model = ?", model).Delete(&relay.ModelPrice{}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete model failed")
		return
	}
	extMap := s.loadV1ModelExtMap()
	if _, ok := extMap[model]; ok {
		delete(extMap, model)
		if b, err := json.Marshal(extMap); err == nil {
			_ = s.upsertRawSetting(v1KeyModelExt, string(b))
		}
	}
	s.writeAudit(c, "v1_model_delete", model, "deleted model binding and price")
	s.invalidateCache(relay.KindPrice, model)
	s.invalidateCache(relay.KindForwardRule, model)
	s.invalidateCache(relay.KindBillingRule, model)
	c.JSON(http.StatusOK, gin.H{"deleted": model})
}

func (s *Server) v1ModelSetListed(c *gin.Context, listed int8) {
	model := s.resolveModelNameByParam(c)
	if model == "" {
		fail(c, http.StatusBadRequest, "model not found")
		return
	}
	_ = s.db.WithContext(c.Request.Context()).Model(&pricing.ModelRuleBinding{}).Where("model = ?", model).Update("is_listed", listed).Error
	c.JSON(http.StatusOK, gin.H{"model": model, "is_listed": listed})
}

func (s *Server) v1ModelCopy(c *gin.Context) {
	srcModel := s.resolveModelNameByParam(c)
	if srcModel == "" {
		fail(c, http.StatusBadRequest, "source model not found")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = c.ShouldBindJSON(&req)
	newModel := cmp.Or(strings.TrimSpace(req.Name), srcModel+"-copy")
	ctx := c.Request.Context()
	var src pricing.ModelRuleBinding
	if err := s.db.WithContext(ctx).Where("model = ?", srcModel).First(&src).Error; err != nil {
		fail(c, http.StatusNotFound, "source model not found")
		return
	}
	src.Model = newModel
	src.DisplayName = cmp.Or(strings.TrimSpace(req.Name), src.DisplayName+" (Copy)")
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&src).Error; err != nil {
			return err
		}
		var srcMeta ModelMeta
		if tx.Where("model = ?", srcModel).First(&srcMeta).Error == nil {
			srcMeta.Model = newModel
			srcMeta.UpdatedAt = s.now().UnixMilli()
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&srcMeta).Error; err != nil {
				return err
			}
		}
		var srcPrice relay.ModelPrice
		if tx.Where("model = ?", srcModel).First(&srcPrice).Error == nil {
			srcPrice.Model = newModel
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&srcPrice).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fail(c, http.StatusConflict, "target model already exists")
		return
	}
	extMap := s.loadV1ModelExtMap()
	if srcExt, ok := extMap[srcModel]; ok {
		srcExt.Mid = ""
		s.saveV1ModelExt(newModel, srcExt)
	}
	c.JSON(http.StatusCreated, gin.H{"id": modelNumericID(newModel), "model": newModel})
}

func (s *Server) v1AdminListAnnouncements(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": s.loadAnnouncements(c.Request.Context())})
}

func (s *Server) v1AdminCreateAnnouncement(c *gin.Context) {
	var req struct {
		Title     string `json:"title" binding:"required"`
		Content   string `json:"content" binding:"required"`
		IsPinned  int8   `json:"is_pinned"`
		IsActive  *int8  `json:"is_active"`
		IsPopup   int8   `json:"is_popup"`
		SortOrder int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "title and content are required")
		return
	}
	ctx := c.Request.Context()
	items := s.loadAnnouncements(ctx)
	var maxID uint64
	for _, it := range items {
		if it.ID > maxID {
			maxID = it.ID
		}
	}
	active := int8(1)
	if req.IsActive != nil && *req.IsActive == 0 {
		active = 0
	}
	now := s.now().UTC().Format(time.RFC3339)
	entry := v1Announcement{
		ID:        maxID + 1,
		Title:     strings.TrimSpace(req.Title),
		Content:   req.Content,
		IsPinned:  req.IsPinned,
		IsActive:  active,
		IsPopup:   req.IsPopup,
		SortOrder: req.SortOrder,
		CreatedAt: now,
		UpdatedAt: now,
	}
	items = append([]v1Announcement{entry}, items...)
	if err := s.saveAnnouncements(ctx, items); err != nil {
		fail(c, http.StatusInternalServerError, "save announcement failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"announcement": entry})
}

func (s *Server) v1AdminUpdateAnnouncement(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Title     *string `json:"title"`
		Content   *string `json:"content"`
		IsPinned  *int8   `json:"is_pinned"`
		IsActive  *int8   `json:"is_active"`
		IsPopup   *int8   `json:"is_popup"`
		SortOrder *int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	items := s.loadAnnouncements(ctx)
	found := false
	for i := range items {
		if items[i].ID == id {
			found = true
			if req.Title != nil {
				items[i].Title = strings.TrimSpace(*req.Title)
			}
			if req.Content != nil {
				items[i].Content = *req.Content
			}
			if req.IsPinned != nil {
				items[i].IsPinned = *req.IsPinned
			}
			if req.IsActive != nil {
				items[i].IsActive = *req.IsActive
			}
			if req.IsPopup != nil {
				items[i].IsPopup = *req.IsPopup
			}
			if req.SortOrder != nil {
				items[i].SortOrder = *req.SortOrder
			}
			items[i].UpdatedAt = s.now().UTC().Format(time.RFC3339)
			break
		}
	}
	if !found {
		fail(c, http.StatusNotFound, "announcement not found")
		return
	}
	if err := s.saveAnnouncements(ctx, items); err != nil {
		fail(c, http.StatusInternalServerError, "save announcement failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": id})
}

func (s *Server) v1AdminDeleteAnnouncement(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	items := s.loadAnnouncements(ctx)
	next := make([]v1Announcement, 0, len(items))
	removed := false
	for _, it := range items {
		if it.ID == id {
			removed = true
			continue
		}
		next = append(next, it)
	}
	if !removed {
		fail(c, http.StatusNotFound, "announcement not found")
		return
	}
	if err := s.saveAnnouncements(ctx, next); err != nil {
		fail(c, http.StatusInternalServerError, "delete announcement failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// ── Admin Docs API ──────────────────────────

func (s *Server) v1AdminListDocs(c *gin.Context) {
	tree, err := v1compat.ListDocTree(c.Request.Context(), s.db, "", false)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list docs failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"tree": tree})
}

func (s *Server) v1AdminGetDoc(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	doc, err := v1compat.GetDocDetail(c.Request.Context(), s.db, id, c.Query("lang"), c.Query("lang") == "", false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "document not found")
			return
		}
		fail(c, http.StatusInternalServerError, "get doc failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"doc": doc})
}

func (s *Server) v1AdminCreateDoc(c *gin.Context) {
	var req v1compat.DocMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	doc, err := v1compat.CreateDoc(c.Request.Context(), s.db, req)
	if errors.Is(err, v1compat.ErrDocParentNotDir) || errors.Is(err, v1compat.ErrDocCycle) {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "create doc failed")
		return
	}
	s.writeAudit(c, "doc_create", strconv.FormatUint(doc.ID, 10), "created doc "+doc.Title)
	c.JSON(http.StatusCreated, gin.H{"doc": doc})
}

func (s *Server) v1AdminUpdateDoc(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req v1compat.DocMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	doc, err := v1compat.UpdateDoc(c.Request.Context(), s.db, id, req)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "document not found")
		return
	}
	if errors.Is(err, v1compat.ErrDocParentNotDir) || errors.Is(err, v1compat.ErrDocCycle) {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "update doc failed")
		return
	}
	s.writeAudit(c, "doc_update", strconv.FormatUint(id, 10), "updated doc "+doc.Title)
	c.JSON(http.StatusOK, gin.H{"doc": doc})
}

func (s *Server) v1AdminDeleteDoc(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	err := v1compat.DeleteDoc(c.Request.Context(), s.db, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "document not found")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete doc failed")
		return
	}
	s.writeAudit(c, "doc_delete", strconv.FormatUint(id, 10), "deleted doc")
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1AdminSetDocExpanded(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		DefaultExpanded int8 `json:"default_expanded"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	err := v1compat.SetDocDefaultExpanded(c.Request.Context(), s.db, id, req.DefaultExpanded != 0)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusBadRequest, "directory not found")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "update default expanded failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "default_expanded": req.DefaultExpanded != 0})
}

func (s *Server) v1AdminImportDefaultDocs(c *gin.Context) {
	if err := v1compat.EnsurePresetDocs(c.Request.Context(), s.db, true); err != nil {
		fail(c, http.StatusInternalServerError, "import default docs failed")
		return
	}
	s.writeAudit(c, "doc_import_default", "all", "restored preset docs")
	c.JSON(http.StatusOK, gin.H{"message": "已重置系统预设文档"})
}

func (s *Server) v1AdminClearAllDocs(c *gin.Context) {
	if err := v1compat.ClearAllDocs(c.Request.Context(), s.db); err != nil {
		fail(c, http.StatusInternalServerError, "clear docs failed")
		return
	}
	s.writeAudit(c, "doc_clear_all", "all", "cleared all docs")
	c.JSON(http.StatusOK, gin.H{"message": "cleared"})
}

func (s *Server) v1AdminTranslateDoc(c *gin.Context) {
	var req struct {
		Text   string `json:"text"`
		Title  string `json:"title"`
		ToLang string `json:"to_lang"`
		Target string `json:"target_lang"`
		Model  string `json:"model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid translate request")
		return
	}
	sourceText := cmp.Or(strings.TrimSpace(req.Text), strings.TrimSpace(req.Title))
	if sourceText == "" {
		fail(c, http.StatusBadRequest, "text is required")
		return
	}
	toLang := cmp.Or(strings.TrimSpace(req.ToLang), strings.TrimSpace(req.Target), "en")
	ctx := c.Request.Context()

	var channels []relay.Channel
	_ = s.readOnly().WithContext(ctx).Where("status = 1 AND owner_org_id = 0").Order("priority DESC, id DESC").Find(&channels).Error
	var chosenCh *relay.Channel
	var chosenModel, cred string
	for i := range channels {
		ch := &channels[i]
		if ch.BaseURL == "" {
			continue
		}
		plain := s.decryptConfigSecret(ch.CredentialEnc)
		if plain == "" || plain == "placeholder-key" {
			continue
		}
		if req.Model != "" && ch.ServesModel(req.Model) {
			chosenCh = ch
			chosenModel = ch.UpstreamModel(req.Model)
			cred = plain
			break
		}
		if chosenCh == nil {
			for m := range strings.SplitSeq(ch.Models, ",") {
				if trimmed := strings.TrimSpace(m); trimmed != "" {
					chosenCh = ch
					chosenModel = ch.UpstreamModel(trimmed)
					cred = plain
					break
				}
			}
		}
	}
	if chosenCh == nil {
		fail(c, http.StatusBadRequest, "请先配置至少一个可用的上游对话渠道与模型后再使用 AI 翻译")
		return
	}

	targetURL := strings.TrimRight(chosenCh.BaseURL, "/") + "/v1/chat/completions"
	if strings.HasSuffix(strings.TrimRight(chosenCh.BaseURL, "/"), "/v1") {
		targetURL = strings.TrimRight(chosenCh.BaseURL, "/") + "/chat/completions"
	}
	bodyMap := map[string]any{
		"model": chosenModel,
		"messages": []map[string]string{
			{"role": "system", "content": "Translate the provided Markdown documentation cleanly into target language " + toLang + ". Preserve Markdown formatting, code blocks, URLs, and {{baseUrl}} variables verbatim. Output only the translation."},
			{"role": "user", "content": sourceText},
		},
		"temperature": 0.2,
	}
	b, _ := json.Marshal(bodyMap)
	upCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	upReq, err := http.NewRequestWithContext(upCtx, http.MethodPost, targetURL, bytes.NewReader(b))
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+cred)
	resp, err := s.upstreamHTTPClient().Do(upReq)
	if err != nil {
		fail(c, http.StatusBadGateway, "AI 翻译请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fail(c, http.StatusBadGateway, fmt.Sprintf("AI 翻译上游返回 HTTP %d", resp.StatusCode))
		return
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil || len(parsed.Choices) == 0 {
		fail(c, http.StatusBadGateway, "AI 翻译响应解析失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"translated": strings.TrimSpace(parsed.Choices[0].Message.Content)})
}
