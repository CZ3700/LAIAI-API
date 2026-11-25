package server

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm/clause"
)

const (
	v1SettingChannelConfigs   = "v1_channel_configs_json"
	v1SettingChannelProviders = "v1_channel_config_providers_json"
	v1SettingChannelMeta      = "v1_channel_meta_json"
)

type v1ChannelConfig struct {
	ID                        uint64   `json:"id"`
	Name                      string   `json:"name"`
	ProviderType              string   `json:"provider_type"`
	BaseURL                   string   `json:"base_url"`
	APIKeyEnc                 string   `json:"-"`
	APIKeyMask                string   `json:"api_key"`
	Remark                    string   `json:"remark"`
	YID                       string   `json:"yid"`
	CategoryID                *uint64  `json:"category_id,omitempty"`
	CategoryIDs               []uint64 `json:"category_ids,omitempty"`
	Status                    int8     `json:"status"`
	SortOrder                 int      `json:"sort_order"`
	Rate                      float64  `json:"rate"`
	UpstreamCurrency          string   `json:"upstream_currency,omitempty"`
	CurrencyRate              float64  `json:"currency_rate,omitempty"`
	QuotaLimit                float64  `json:"quota_limit"`
	DailyQuotaLimit           float64  `json:"daily_quota_limit"`
	WeeklyQuotaLimit          float64  `json:"weekly_quota_limit"`
	MonthlyQuotaLimit         float64  `json:"monthly_quota_limit"`
	DailyResetHour            int      `json:"daily_reset_hour"`
	DailyResetMinute          int      `json:"daily_reset_minute"`
	DailyResetCooldownMinutes int      `json:"daily_reset_cooldown_minutes"`
	CreatedAt                 string   `json:"created_at"`
	UpdatedAt                 string   `json:"updated_at"`
}

type v1ChannelMeta struct {
	PresetID          *uint64         `json:"preset_id,omitempty"`
	GroupAID          string          `json:"group_aid,omitempty"`
	SortOrder         int             `json:"sort_order,omitempty"`
	UserGroups        []string        `json:"user_groups,omitempty"`
	ExcludeUserGroups []string        `json:"exclude_user_groups,omitempty"`
	Config            json.RawMessage `json:"config,omitempty"`
}

func (s *Server) v1ListChannels(c *gin.Context) {
	ctx := c.Request.Context()
	var channels []relay.Channel
	if err := s.readOnly().WithContext(ctx).Where("owner_org_id = 0").Order("priority DESC, id DESC").Find(&channels).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list channels failed")
		return
	}
	metaMap := s.loadV1ChannelMetaMap()
	out := make([]gin.H, 0, len(channels))
	for _, ch := range channels {
		out = append(out, s.formatV1TkeChannel(ch, metaMap[ch.ID]))
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (s *Server) formatV1TkeChannel(ch relay.Channel, meta v1ChannelMeta) gin.H {
	models := []string{}
	for item := range strings.SplitSeq(ch.Models, ",") {
		if m := strings.TrimSpace(item); m != "" {
			models = append(models, m)
		}
	}
	mapping := map[string]string{}
	if strings.TrimSpace(ch.ModelMapping) != "" {
		_ = json.Unmarshal([]byte(ch.ModelMapping), &mapping)
	}
	rateF, _ := ch.UpstreamCostRatio.Float64()
	if rateF <= 0 {
		rateF = 1
	}
	aid := cmp.Or(meta.GroupAID, "A"+strconv.FormatUint(ch.ID, 10))
	var cfgVal any = map[string]any{}
	if len(meta.Config) > 0 {
		_ = json.Unmarshal(meta.Config, &cfgVal)
	}
	var catID *uint64
	if ch.CategoryID > 0 {
		v := ch.CategoryID
		catID = &v
	}
	return gin.H{
		"id":                  ch.ID,
		"name":                ch.Name,
		"provider_type":       ch.Provider,
		"base_url":            ch.BaseURL,
		"models":              models,
		"model_mapping":       mapping,
		"user_groups":         slices.Clone(meta.UserGroups),
		"exclude_user_groups": slices.Clone(meta.ExcludeUserGroups),
		"group_aid":           aid,
		"preset_id":           meta.PresetID,
		"category_id":         catID,
		"sort_order":          meta.SortOrder,
		"priority":            ch.Priority,
		"weight":              ch.Weight,
		"status":              ch.Status,
		"quota_limit":         -1,
		"quota_used":          0,
		"daily_quota_limit":   -1,
		"weekly_quota_limit":  -1,
		"monthly_quota_limit": -1,
		"rate":                rateF,
		"config":              cfgVal,
		"created_at":          time.Now().UTC().Format(time.RFC3339),
	}
}

func (s *Server) v1CreateChannel(c *gin.Context) {
	if s.keyring == nil {
		fail(c, http.StatusServiceUnavailable, "LAIAI_DATA_KEYS not configured")
		return
	}
	var req struct {
		Name              string            `json:"name" binding:"required"`
		ProviderType      string            `json:"provider_type"`
		BaseURL           string            `json:"base_url"`
		APIKey            string            `json:"api_key"`
		Models            []string          `json:"models"`
		ModelMapping      map[string]string `json:"model_mapping"`
		Priority          int               `json:"priority"`
		Weight            int               `json:"weight"`
		Rate              float64           `json:"rate"`
		CategoryID        *uint64           `json:"category_id"`
		PresetID          *uint64           `json:"preset_id"`
		SortOrder         int               `json:"sort_order"`
		UserGroups        []string          `json:"user_groups"`
		ExcludeUserGroups []string          `json:"exclude_user_groups"`
		Config            json.RawMessage   `json:"config"`
		Status            *int8             `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid channel request")
		return
	}

	provider := mapTkeProviderToRelay(cmp.Or(req.ProviderType, "openai"))
	baseURL := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)

	var credEnc string
	if req.PresetID != nil && *req.PresetID > 0 {
		if cfg, ok := s.findV1ChannelConfig(*req.PresetID); ok {
			if baseURL == "" {
				baseURL = cfg.BaseURL
			}
			if apiKey == "" {
				credEnc = cfg.APIKeyEnc
			}
			if req.ProviderType == "" && cfg.ProviderType != "" {
				provider = mapTkeProviderToRelay(cfg.ProviderType)
			}
		}
	}
	if baseURL == "" {
		baseURL = cmp.Or(relay.DefaultBaseURLFor(provider), "https://api.openai.com")
	}
	if credEnc == "" {
		if apiKey == "" {
			apiKey = "placeholder-key"
		}
		enc, err := s.keyring.Encrypt(apiKey)
		if err != nil {
			fail(c, http.StatusInternalServerError, "encrypt credential failed")
			return
		}
		credEnc = enc
	}

	mappingJSON := ""
	if len(req.ModelMapping) > 0 {
		if b, err := json.Marshal(req.ModelMapping); err == nil {
			mappingJSON = string(b)
		}
	}
	var catID uint64
	if req.CategoryID != nil {
		catID = *req.CategoryID
	}
	rateDec := decimal.NewFromInt(1)
	if req.Rate > 0 {
		rateDec = decimal.NewFromFloat(req.Rate)
	}
	status := int8(1)
	if req.Status != nil {
		status = *req.Status
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}

	ch := relay.Channel{
		Name:               strings.TrimSpace(req.Name),
		Provider:           provider,
		BaseURL:            baseURL,
		CredentialEnc:      credEnc,
		Models:             joinTrim(req.Models),
		ModelMapping:       mappingJSON,
		Priority:           req.Priority,
		Weight:             weight,
		MaxConcurrentTasks: 3,
		CategoryID:         catID,
		UpstreamCostRatio:  rateDec,
		Status:             status,
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&ch).Error; err != nil {
		fail(c, http.StatusInternalServerError, "create channel failed")
		return
	}

	meta := v1ChannelMeta{
		PresetID:          req.PresetID,
		GroupAID:          "A" + strconv.FormatUint(ch.ID, 10),
		SortOrder:         req.SortOrder,
		UserGroups:        req.UserGroups,
		ExcludeUserGroups: req.ExcludeUserGroups,
		Config:            req.Config,
	}
	s.saveV1ChannelMeta(ch.ID, meta)
	s.invalidateCache(relay.KindChannel, "")
	s.writeAudit(c, "v1_channel_create", strconv.FormatUint(ch.ID, 10), "created channel "+ch.Name)
	c.JSON(http.StatusCreated, gin.H{"data": s.formatV1TkeChannel(ch, meta)})
}

func (s *Server) v1UpdateChannel(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var ch relay.Channel
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&ch).Error; err != nil {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	var req struct {
		Name              *string            `json:"name"`
		ProviderType      *string            `json:"provider_type"`
		BaseURL           *string            `json:"base_url"`
		APIKey            *string            `json:"api_key"`
		Models            *[]string          `json:"models"`
		ModelMapping      *map[string]string `json:"model_mapping"`
		Priority          *int               `json:"priority"`
		Weight            *int               `json:"weight"`
		Rate              *float64           `json:"rate"`
		CategoryID        *uint64            `json:"category_id"`
		PresetID          *uint64            `json:"preset_id"`
		SortOrder         *int               `json:"sort_order"`
		UserGroups        *[]string          `json:"user_groups"`
		ExcludeUserGroups *[]string          `json:"exclude_user_groups"`
		Config            json.RawMessage    `json:"config"`
		Status            *int8              `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]any{}
	if req.Name != nil {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.ProviderType != nil {
		updates["provider"] = mapTkeProviderToRelay(*req.ProviderType)
	}
	if req.BaseURL != nil && strings.TrimSpace(*req.BaseURL) != "" {
		updates["base_url"] = strings.TrimSpace(*req.BaseURL)
	}
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" && !strings.HasPrefix(*req.APIKey, "••••") {
		if s.keyring == nil {
			fail(c, http.StatusServiceUnavailable, "keyring unavailable")
			return
		}
		enc, err := s.keyring.Encrypt(strings.TrimSpace(*req.APIKey))
		if err != nil {
			fail(c, http.StatusInternalServerError, "encrypt key failed")
			return
		}
		updates["credential_enc"] = enc
	}
	if req.Models != nil {
		updates["models"] = joinTrim(*req.Models)
	}
	if req.ModelMapping != nil {
		if b, err := json.Marshal(*req.ModelMapping); err == nil {
			updates["model_mapping"] = string(b)
		}
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.Weight != nil && *req.Weight > 0 {
		updates["weight"] = *req.Weight
	}
	if req.Rate != nil && *req.Rate > 0 {
		updates["upstream_cost_ratio"] = decimal.NewFromFloat(*req.Rate)
	}
	if req.CategoryID != nil {
		updates["category_id"] = *req.CategoryID
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if len(updates) > 0 {
		if err := s.db.WithContext(c.Request.Context()).Model(&relay.Channel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			fail(c, http.StatusInternalServerError, "update channel failed")
			return
		}
	}

	metaMap := s.loadV1ChannelMetaMap()
	meta := metaMap[id]
	if req.PresetID != nil {
		meta.PresetID = req.PresetID
	}
	if req.SortOrder != nil {
		meta.SortOrder = *req.SortOrder
	}
	if req.UserGroups != nil {
		meta.UserGroups = *req.UserGroups
	}
	if req.ExcludeUserGroups != nil {
		meta.ExcludeUserGroups = *req.ExcludeUserGroups
	}
	if len(req.Config) > 0 {
		meta.Config = req.Config
	}
	s.saveV1ChannelMeta(id, meta)

	_ = s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&ch).Error
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusOK, gin.H{"data": s.formatV1TkeChannel(ch, meta)})
}

func (s *Server) v1DeleteChannel(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).Delete(&relay.Channel{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "delete channel failed")
		return
	}
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1ChannelMeltdownStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"melted": false, "cooldown_remaining_sec": 0, "consecutive_failures": 0})
}

func (s *Server) v1ChannelMeltdownReset(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"reset": true, "melted": false})
}

func (s *Server) v1ChannelQuotaReset(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"reset": true})
}

func mapTkeProviderToRelay(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "兼容", "custom", "high_availability_group":
		return "openai"
	case "官方":
		return "openai"
	case "火山引擎":
		return "doubao"
	case "阿里云":
		return "dashscope"
	default:
		return strings.ToLower(strings.TrimSpace(p))
	}
}

func (s *Server) loadV1ChannelConfigs() []v1ChannelConfig {
	raw := s.getSetting(v1SettingChannelConfigs)
	if strings.TrimSpace(raw) == "" {
		return []v1ChannelConfig{}
	}
	var list []v1ChannelConfig
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []v1ChannelConfig{}
	}
	return list
}

func (s *Server) saveV1ChannelConfigs(list []v1ChannelConfig) error {
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.upsertRawSetting(v1SettingChannelConfigs, string(b))
}

func (s *Server) findV1ChannelConfig(id uint64) (v1ChannelConfig, bool) {
	for _, cfg := range s.loadV1ChannelConfigs() {
		if cfg.ID == id {
			return cfg, true
		}
	}
	return v1ChannelConfig{}, false
}

func (s *Server) v1ListChannelConfigs(c *gin.Context) {
	list := s.loadV1ChannelConfigs()
	for i := range list {
		list[i].APIKeyEnc = ""
		if list[i].APIKeyMask == "" {
			list[i].APIKeyMask = "••••••••"
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": list})
}

func (s *Server) v1CreateChannelConfig(c *gin.Context) {
	var req struct {
		Name                      string   `json:"name" binding:"required"`
		ProviderType              string   `json:"provider_type"`
		BaseURL                   string   `json:"base_url"`
		APIKey                    string   `json:"api_key"`
		Remark                    string   `json:"remark"`
		YID                       string   `json:"yid"`
		CategoryID                *uint64  `json:"category_id"`
		CategoryIDs               []uint64 `json:"category_ids"`
		Status                    *int8    `json:"status"`
		SortOrder                 int      `json:"sort_order"`
		Rate                      float64  `json:"rate"`
		UpstreamCurrency          string   `json:"upstream_currency"`
		CurrencyRate              float64  `json:"currency_rate"`
		QuotaLimit                float64  `json:"quota_limit"`
		DailyQuotaLimit           float64  `json:"daily_quota_limit"`
		WeeklyQuotaLimit          float64  `json:"weekly_quota_limit"`
		MonthlyQuotaLimit         float64  `json:"monthly_quota_limit"`
		DailyResetHour            int      `json:"daily_reset_hour"`
		DailyResetMinute          int      `json:"daily_reset_minute"`
		DailyResetCooldownMinutes int      `json:"daily_reset_cooldown_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	list := s.loadV1ChannelConfigs()
	var maxID uint64
	for _, it := range list {
		if it.ID > maxID {
			maxID = it.ID
		}
	}
	enc := ""
	if k := strings.TrimSpace(req.APIKey); k != "" && s.keyring != nil {
		if ciph, err := s.keyring.Encrypt(k); err == nil {
			enc = ciph
		}
	}
	now := s.now().UTC().Format(time.RFC3339)
	status := int8(1)
	if req.Status != nil {
		status = *req.Status
	}
	rate := req.Rate
	if rate <= 0 {
		rate = 1
	}
	id := maxID + 1
	item := v1ChannelConfig{
		ID:                        id,
		Name:                      strings.TrimSpace(req.Name),
		ProviderType:              cmp.Or(strings.TrimSpace(req.ProviderType), "兼容"),
		BaseURL:                   strings.TrimSpace(req.BaseURL),
		APIKeyEnc:                 enc,
		APIKeyMask:                "••••••••",
		Remark:                    req.Remark,
		YID:                       cmp.Or(strings.TrimSpace(req.YID), "Y"+strconv.FormatUint(1000+id, 10)),
		CategoryID:                req.CategoryID,
		CategoryIDs:               req.CategoryIDs,
		Status:                    status,
		SortOrder:                 req.SortOrder,
		Rate:                      rate,
		UpstreamCurrency:          req.UpstreamCurrency,
		CurrencyRate:              req.CurrencyRate,
		QuotaLimit:                cmp.Or(req.QuotaLimit, -1),
		DailyQuotaLimit:           cmp.Or(req.DailyQuotaLimit, -1),
		WeeklyQuotaLimit:          cmp.Or(req.WeeklyQuotaLimit, -1),
		MonthlyQuotaLimit:         cmp.Or(req.MonthlyQuotaLimit, -1),
		DailyResetHour:            req.DailyResetHour,
		DailyResetMinute:          req.DailyResetMinute,
		DailyResetCooldownMinutes: req.DailyResetCooldownMinutes,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	list = append([]v1ChannelConfig{item}, list...)
	if err := s.saveV1ChannelConfigs(list); err != nil {
		fail(c, http.StatusInternalServerError, "save channel config failed")
		return
	}
	item.APIKeyEnc = ""
	c.JSON(http.StatusCreated, gin.H{"data": item})
}

func (s *Server) v1UpdateChannelConfig(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req map[string]any
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	list := s.loadV1ChannelConfigs()
	for i := range list {
		if list[i].ID != id {
			continue
		}
		if v, ok := req["name"].(string); ok && strings.TrimSpace(v) != "" {
			list[i].Name = strings.TrimSpace(v)
		}
		if v, ok := req["provider_type"].(string); ok {
			list[i].ProviderType = v
		}
		if v, ok := req["base_url"].(string); ok {
			list[i].BaseURL = strings.TrimSpace(v)
		}
		if v, ok := req["api_key"].(string); ok && strings.TrimSpace(v) != "" && !strings.HasPrefix(v, "••••") && s.keyring != nil {
			if ciph, err := s.keyring.Encrypt(strings.TrimSpace(v)); err == nil {
				list[i].APIKeyEnc = ciph
			}
		}
		if v, ok := req["remark"].(string); ok {
			list[i].Remark = v
		}
		if v, ok := req["rate"].(float64); ok && v > 0 {
			list[i].Rate = v
		}
		if v, ok := req["status"].(float64); ok {
			list[i].Status = int8(v)
		}
		if v, ok := req["sort_order"].(float64); ok {
			list[i].SortOrder = int(v)
		}
		list[i].UpdatedAt = s.now().UTC().Format(time.RFC3339)
		_ = s.saveV1ChannelConfigs(list)
		out := list[i]
		out.APIKeyEnc = ""
		c.JSON(http.StatusOK, gin.H{"data": out})
		return
	}
	fail(c, http.StatusNotFound, "channel config not found")
}

func (s *Server) v1DeleteChannelConfig(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	list := s.loadV1ChannelConfigs()
	filtered := slices.DeleteFunc(list, func(it v1ChannelConfig) bool { return it.ID == id })
	if len(filtered) == len(list) {
		fail(c, http.StatusNotFound, "channel config not found")
		return
	}
	_ = s.saveV1ChannelConfigs(filtered)
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1CopyChannelConfig(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = c.ShouldBindJSON(&req)
	list := s.loadV1ChannelConfigs()
	var maxID uint64
	var src *v1ChannelConfig
	for i := range list {
		if list[i].ID > maxID {
			maxID = list[i].ID
		}
		if list[i].ID == id {
			src = &list[i]
		}
	}
	if src == nil {
		fail(c, http.StatusNotFound, "channel config not found")
		return
	}
	copyItem := *src
	copyItem.ID = maxID + 1
	copyItem.Name = cmp.Or(strings.TrimSpace(req.Name), src.Name+" (副本)")
	copyItem.YID = "Y" + strconv.FormatUint(1000+copyItem.ID, 10)
	list = append([]v1ChannelConfig{copyItem}, list...)
	_ = s.saveV1ChannelConfigs(list)
	copyItem.APIKeyEnc = ""
	c.JSON(http.StatusCreated, gin.H{"data": copyItem})
}

func (s *Server) v1ChannelConfigProviders(c *gin.Context) {
	raw := s.getSetting(v1SettingChannelProviders)
	if strings.TrimSpace(raw) == "" {
		c.JSON(http.StatusOK, []gin.H{
			{"id": 1, "name": "兼容", "sort_order": 1, "is_active": 1},
			{"id": 2, "name": "官方", "sort_order": 2, "is_active": 1},
			{"id": 3, "name": "火山引擎", "sort_order": 3, "is_active": 1},
			{"id": 4, "name": "阿里云", "sort_order": 4, "is_active": 1},
			{"id": 5, "name": "newapi", "sort_order": 5, "is_active": 1},
		})
		return
	}
	c.Data(http.StatusOK, "application/json", []byte(raw))
}

func (s *Server) v1SaveChannelConfigProvider(c *gin.Context) {
	var list []forward.ChannelCategory
	if raw := s.getSetting(v1SettingChannelProviders); strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &list)
	}
	var req struct {
		Name      string `json:"name" binding:"required"`
		SortOrder int    `json:"sort_order"`
		IsActive  int8   `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	idParam := strings.TrimSpace(c.Param("id"))
	if idParam != "" {
		id, err := strconv.ParseUint(idParam, 10, 64)
		if err == nil {
			for i := range list {
				if list[i].ID == id {
					list[i].Name = strings.TrimSpace(req.Name)
					list[i].SortOrder = req.SortOrder
					list[i].IsActive = req.IsActive
					b, _ := json.Marshal(list)
					_ = s.upsertRawSetting(v1SettingChannelProviders, string(b))
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
	item := forward.ChannelCategory{ID: maxID + 1, Name: strings.TrimSpace(req.Name), SortOrder: req.SortOrder, IsActive: req.IsActive}
	list = append(list, item)
	b, _ := json.Marshal(list)
	_ = s.upsertRawSetting(v1SettingChannelProviders, string(b))
	c.JSON(http.StatusCreated, item)
}

func (s *Server) v1ListUpstreams(c *gin.Context) {
	var rows []relay.Channel
	_ = s.readOnly().WithContext(c.Request.Context()).Where("owner_org_id = 0").Order("id DESC").Find(&rows).Error
	out := make([]gin.H, 0, len(rows))
	for _, ch := range rows {
		out = append(out, gin.H{"id": ch.ID, "name": ch.Name, "type": ch.Provider, "base_url": ch.BaseURL, "status": ch.Status})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) v1ListUserLevels(c *gin.Context) {
	var rows []GroupRatio
	_ = s.readOnly().WithContext(c.Request.Context()).Order("name").Find(&rows).Error
	out := make([]gin.H, 0, len(rows))
	for i, r := range rows {
		disc, _ := r.Ratio.Float64()
		out = append(out, gin.H{
			"id":                     i + 1,
			"name":                   r.Name,
			"group_key":              r.Name,
			"discount":               disc,
			"is_default":             map[bool]int{true: 1, false: 0}[r.Name == "default"],
			"allow_view_log_details": 1,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (s *Server) v1UpdateUserLevel(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) loadV1ChannelMetaMap() map[uint64]v1ChannelMeta {
	raw := s.getSetting(v1SettingChannelMeta)
	if strings.TrimSpace(raw) == "" {
		return map[uint64]v1ChannelMeta{}
	}
	out := map[uint64]v1ChannelMeta{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func (s *Server) saveV1ChannelMeta(id uint64, meta v1ChannelMeta) {
	m := s.loadV1ChannelMetaMap()
	m[id] = meta
	if b, err := json.Marshal(m); err == nil {
		_ = s.upsertRawSetting(v1SettingChannelMeta, string(b))
	}
}

func (s *Server) upsertRawSetting(name, val string) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&Setting{Name: name, Value: val}).Error
}
