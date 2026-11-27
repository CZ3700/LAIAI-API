package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	v1SettingChannelProviders = "v1_channel_config_providers_json"
	v1SettingChannelMeta      = "v1_channel_meta_json"
)

type v1BoundChannelSummary struct {
	ID                uint64 `json:"id"`
	Name              string `json:"name"`
	GroupAID          string `json:"group_aid,omitempty"`
	IsHA              bool   `json:"is_ha"`
	Status            int8   `json:"status"`
	EffectivePriority int    `json:"effective_priority"`
	EffectiveWeight   int    `json:"effective_weight"`
}

type v1ChannelConfig struct {
	ID                          uint64                  `json:"id"`
	Name                        string                  `json:"name"`
	ProviderType                string                  `json:"provider_type"`
	BaseURL                     string                  `json:"base_url"`
	APIKeyEnc                   string                  `json:"api_key_enc,omitempty"`
	APIKeyMask                  string                  `json:"api_key"`
	HasAPIKey                   bool                    `json:"has_api_key"`
	Remark                      string                  `json:"remark"`
	YID                         string                  `json:"yid"`
	CategoryID                  *uint64                 `json:"category_id,omitempty"`
	CategoryIDs                 []uint64                `json:"category_ids,omitempty"`
	Status                      int8                    `json:"status"`
	SortOrder                   int                     `json:"sort_order"`
	Priority                    int                     `json:"priority"`
	Weight                      int                     `json:"weight"`
	Rate                        float64                 `json:"rate"`
	UpstreamSystem              string                  `json:"upstream_system,omitempty"`
	UpstreamGroup               string                  `json:"upstream_group,omitempty"`
	UpstreamSyncIntervalMinutes int                     `json:"upstream_sync_interval_minutes"`
	UpstreamSyncRateAdd         float64                 `json:"upstream_sync_rate_add"`
	UpstreamCurrency            string                  `json:"upstream_currency,omitempty"`
	SiteCurrency                string                  `json:"site_currency,omitempty"`
	CurrencyRate                float64                 `json:"currency_rate,omitempty"`
	UpstreamCurrencyRate        float64                 `json:"upstream_currency_rate,omitempty"`
	UpstreamUserID              string                  `json:"upstream_user_id,omitempty"`
	UpstreamAccessToken         string                  `json:"upstream_access_token,omitempty"`
	HasUpstreamAccessToken      bool                    `json:"has_upstream_access_token"`
	UpstreamSyncedAt            *string                 `json:"upstream_synced_at,omitempty"`
	VolcAK                      string                  `json:"volc_ak,omitempty"`
	VolcSK                      string                  `json:"volc_sk,omitempty"`
	HasVolcCredentials          bool                    `json:"has_volc_credentials"`
	UpstreamBalance             *float64                `json:"upstream_balance,omitempty"`
	UpstreamBalanceUpdatedAt    *string                 `json:"upstream_balance_updated_at,omitempty"`
	BoundChannelCount           int                     `json:"bound_channel_count"`
	BoundChannels               []v1BoundChannelSummary `json:"bound_channels"`
	QuotaLimit                  float64                 `json:"quota_limit"`
	DailyQuotaLimit             float64                 `json:"daily_quota_limit"`
	WeeklyQuotaLimit            float64                 `json:"weekly_quota_limit"`
	MonthlyQuotaLimit           float64                 `json:"monthly_quota_limit"`
	DailyResetHour              int                     `json:"daily_reset_hour"`
	DailyResetMinute            int                     `json:"daily_reset_minute"`
	DailyResetCooldownMinutes   int                     `json:"daily_reset_cooldown_minutes"`
	CreatedAt                   string                  `json:"created_at"`
	UpdatedAt                   string                  `json:"updated_at"`
}

type v1ChannelMeta struct {
	RawProviderType   string          `json:"raw_provider_type,omitempty"`
	PresetID          *uint64         `json:"preset_id,omitempty"`
	ModelMids         []string        `json:"model_mids,omitempty"`
	GroupAID          string          `json:"group_aid,omitempty"`
	SortOrder         int             `json:"sort_order,omitempty"`
	UserGroups        []string        `json:"user_groups,omitempty"`
	ExcludeUserGroups []string        `json:"exclude_user_groups,omitempty"`
	Config            json.RawMessage `json:"config,omitempty"`
}

func (s *Server) resolveCanonicalChannelModels(ctx context.Context, raw []string) ([]string, []string) {
	midToModel := map[string]string{}
	extMap := s.loadV1ModelExtMap()
	for modelName, ext := range extMap {
		if ext.Mid != "" {
			midToModel[ext.Mid] = modelName
		}
		midToModel["m-"+strconv.FormatUint(modelNumericID(modelName), 10)] = modelName
	}
	var bindings []pricing.ModelRuleBinding
	if err := s.readOnly().WithContext(ctx).Select("model").Find(&bindings).Error; err == nil {
		for _, b := range bindings {
			midToModel["m-"+strconv.FormatUint(modelNumericID(b.Model), 10)] = b.Model
		}
	}
	var prices []relay.ModelPrice
	if err := s.readOnly().WithContext(ctx).Select("model").Find(&prices).Error; err == nil {
		for _, p := range prices {
			midToModel["m-"+strconv.FormatUint(modelNumericID(p.Model), 10)] = p.Model
		}
	}

	canonical := make([]string, 0, len(raw))
	rawList := make([]string, 0, len(raw))
	seenCanon := make(map[string]struct{}, len(raw))
	seenRaw := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seenRaw[trimmed]; !ok {
			seenRaw[trimmed] = struct{}{}
			rawList = append(rawList, trimmed)
		}
		resolved := trimmed
		if mapped, ok := midToModel[trimmed]; ok && mapped != "" {
			resolved = mapped
		}
		if _, ok := seenCanon[resolved]; !ok {
			seenCanon[resolved] = struct{}{}
			canonical = append(canonical, resolved)
		}
	}
	return canonical, rawList
}

func (s *Server) resolveV1SubChannels(presetID *uint64, rawConfig json.RawMessage) (*v1ChannelConfig, []v1ChannelConfig) {
	all := s.loadV1ChannelConfigs()
	byID := make(map[uint64]v1ChannelConfig, len(all))
	for _, item := range all {
		byID[item.ID] = item
	}

	var parsed struct {
		SubChannels []uint64 `json:"sub_channels"`
	}
	if len(rawConfig) > 0 {
		_ = json.Unmarshal(rawConfig, &parsed)
	}
	subs := make([]v1ChannelConfig, 0, len(parsed.SubChannels))
	seen := make(map[uint64]struct{}, len(parsed.SubChannels))
	for _, sid := range parsed.SubChannels {
		if _, ok := seen[sid]; ok {
			continue
		}
		if cfg, ok := byID[sid]; ok {
			seen[sid] = struct{}{}
			subs = append(subs, cfg)
		}
	}

	var primary *v1ChannelConfig
	if presetID != nil && *presetID > 0 {
		if cfg, ok := byID[*presetID]; ok {
			c := cfg
			primary = &c
		}
	}
	if primary == nil && len(subs) > 0 {
		c := subs[0]
		primary = &c
	}
	return primary, subs
}

func (s *Server) syncChannelSubCredentials(ctx context.Context, channelID uint64, subs []v1ChannelConfig) {
	if channelID == 0 || len(subs) == 0 {
		return
	}
	rows := make([]relay.ChannelCredential, 0, len(subs))
	for _, sub := range subs {
		if strings.TrimSpace(sub.APIKeyEnc) == "" {
			continue
		}
		fp := "cfg-" + strconv.FormatUint(sub.ID, 10)
		if s.keyring != nil {
			if plain, err := s.keyring.Decrypt(sub.APIKeyEnc); err == nil && plain != "" {
				fp = relay.CredentialFingerprint(plain)
			}
		}
		st := int8(1)
		if sub.Status == 0 {
			st = 0
		}
		rows = append(rows, relay.ChannelCredential{
			ChannelID:     channelID,
			CredentialEnc: sub.APIKeyEnc,
			Fingerprint:   fp,
			Status:        st,
			ModeWeight:    1,
		})
	}
	if len(rows) == 0 {
		return
	}
	_ = s.db.WithContext(ctx).Where("channel_id = ?", channelID).Delete(&relay.ChannelCredential{}).Error
	_ = s.db.WithContext(ctx).Create(&rows).Error
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
	if len(meta.ModelMids) > 0 {
		models = slices.Clone(meta.ModelMids)
	} else {
		for item := range strings.SplitSeq(ch.Models, ",") {
			if m := strings.TrimSpace(item); m != "" {
				models = append(models, m)
			}
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
		"provider_type":       cmp.Or(meta.RawProviderType, ch.Provider),
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
		"thinking_to_content": ch.ThinkingToContent == 1,
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
		ThinkingToContent bool              `json:"thinking_to_content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid channel request")
		return
	}

	provider := mapTkeProviderToRelay(cmp.Or(req.ProviderType, "openai"))
	baseURL := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)
	primary, subs := s.resolveV1SubChannels(req.PresetID, req.Config)

	var credEnc string
	if primary != nil {
		if baseURL == "" {
			baseURL = primary.BaseURL
		}
		if apiKey == "" {
			credEnc = primary.APIKeyEnc
		}
		if req.ProviderType == "" || req.ProviderType == "high_availability_group" || req.ProviderType == "custom" {
			if primary.ProviderType != "" {
				provider = mapTkeProviderToRelay(primary.ProviderType)
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
	} else if primary != nil && primary.Rate > 0 {
		rateDec = decimal.NewFromFloat(primary.Rate)
	}
	status := int8(1)
	if req.Status != nil {
		status = *req.Status
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}

	canonicalModels, rawModelMids := s.resolveCanonicalChannelModels(c.Request.Context(), req.Models)
	var thinkingToContent int8
	if req.ThinkingToContent {
		thinkingToContent = 1
	}
	ch := relay.Channel{
		Name:               strings.TrimSpace(req.Name),
		Provider:           provider,
		BaseURL:            baseURL,
		CredentialEnc:      credEnc,
		Models:             joinTrim(canonicalModels),
		ModelMapping:       mappingJSON,
		Priority:           req.Priority,
		Weight:             weight,
		MaxConcurrentTasks: 3,
		ThinkingToContent:  thinkingToContent,
		CategoryID:         catID,
		UpstreamCostRatio:  rateDec,
		Status:             status,
	}
	if err := s.db.WithContext(c.Request.Context()).Create(&ch).Error; err != nil {
		fail(c, http.StatusInternalServerError, "create channel failed")
		return
	}
	s.syncChannelSubCredentials(c.Request.Context(), ch.ID, subs)

	meta := v1ChannelMeta{
		RawProviderType:   strings.TrimSpace(req.ProviderType),
		PresetID:          req.PresetID,
		ModelMids:         rawModelMids,
		GroupAID:          "A" + strconv.FormatUint(ch.ID, 10),
		SortOrder:         req.SortOrder,
		UserGroups:        req.UserGroups,
		ExcludeUserGroups: req.ExcludeUserGroups,
		Config:            req.Config,
	}
	if err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		return updateV1ChannelMeta(tx, ch.ID, func(current *v1ChannelMeta) { *current = meta })
	}); err != nil {
		s.Log().Error("save channel metadata", "channel", ch.ID, "err", err)
		fail(c, http.StatusInternalServerError, "save channel metadata failed")
		return
	}
	s.invalidateSettingsCache()
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
		ThinkingToContent *bool              `json:"thinking_to_content"`
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
	var updatedModelMids []string
	if req.Models != nil {
		canonicalModels, rawMids := s.resolveCanonicalChannelModels(c.Request.Context(), *req.Models)
		updates["models"] = joinTrim(canonicalModels)
		updatedModelMids = rawMids
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
	if req.ThinkingToContent != nil {
		updates["thinking_to_content"] = b2i8(*req.ThinkingToContent)
	}
	if len(updates) > 0 {
		if err := s.db.WithContext(c.Request.Context()).Model(&relay.Channel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			fail(c, http.StatusInternalServerError, "update channel failed")
			return
		}
	}

	var meta v1ChannelMeta
	if err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		return updateV1ChannelMeta(tx, id, func(current *v1ChannelMeta) {
			if req.Models != nil {
				current.ModelMids = updatedModelMids
			}
			if req.ProviderType != nil {
				current.RawProviderType = strings.TrimSpace(*req.ProviderType)
			}
			if req.PresetID != nil {
				current.PresetID = req.PresetID
			}
			if req.SortOrder != nil {
				current.SortOrder = *req.SortOrder
			}
			if req.UserGroups != nil {
				current.UserGroups = *req.UserGroups
			}
			if req.ExcludeUserGroups != nil {
				current.ExcludeUserGroups = *req.ExcludeUserGroups
			}
			if len(req.Config) > 0 {
				current.Config = req.Config
			}
			meta = *current
		})
	}); err != nil {
		s.Log().Error("update channel metadata", "channel", id, "err", err)
		fail(c, http.StatusInternalServerError, "update channel metadata failed")
		return
	}
	s.invalidateSettingsCache()
	primary, subs := s.resolveV1SubChannels(meta.PresetID, meta.Config)
	if len(subs) > 0 {
		s.syncChannelSubCredentials(c.Request.Context(), id, subs)
	}
	if primary != nil {
		subUp := map[string]any{}
		if primary.BaseURL != "" && (req.BaseURL == nil || strings.TrimSpace(*req.BaseURL) == "") {
			subUp["base_url"] = primary.BaseURL
		}
		if primary.APIKeyEnc != "" && (req.APIKey == nil || strings.TrimSpace(*req.APIKey) == "") {
			subUp["credential_enc"] = primary.APIKeyEnc
		}
		if primary.ProviderType != "" && (meta.RawProviderType == "" || meta.RawProviderType == "high_availability_group") {
			subUp["provider"] = mapTkeProviderToRelay(primary.ProviderType)
		}
		if len(subUp) > 0 {
			_ = s.db.WithContext(c.Request.Context()).Model(&relay.Channel{}).Where("id = ?", id).Updates(subUp).Error
		}
	}

	_ = s.db.WithContext(c.Request.Context()).Where("id = ?", id).First(&ch).Error
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusOK, gin.H{"data": s.formatV1TkeChannel(ch, meta)})
}

func (s *Server) v1DeleteChannel(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	if err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", id).Delete(&relay.ChannelCredential{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&relay.Channel{}, id).Error; err != nil {
			return err
		}
		return updateSettingJSON(tx, v1SettingChannelMeta, map[uint64]v1ChannelMeta{}, func(m *map[uint64]v1ChannelMeta) error {
			delete(*m, id)
			return nil
		})
	}); err != nil {
		s.Log().Error("delete channel", "channel", id, "err", err)
		fail(c, http.StatusInternalServerError, "delete channel failed")
		return
	}
	s.invalidateSettingsCache()
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

type channelConfigRow struct {
	ID                          uint64     `gorm:"primaryKey;autoIncrement"`
	Name                        string     `gorm:"column:name"`
	ProviderType                string     `gorm:"column:provider_type"`
	BaseURL                     string     `gorm:"column:base_url"`
	APIKeyEnc                   string     `gorm:"column:api_key_enc"`
	APIKeyMask                  string     `gorm:"column:api_key_mask"`
	Remark                      string     `gorm:"column:remark"`
	YID                         string     `gorm:"column:yid"`
	CategoryID                  *uint64    `gorm:"column:category_id"`
	CategoryIDsJSON             string     `gorm:"column:category_ids_json"`
	Status                      int8       `gorm:"column:status"`
	SortOrder                   int        `gorm:"column:sort_order"`
	Priority                    int        `gorm:"column:priority"`
	Weight                      int        `gorm:"column:weight"`
	Rate                        float64    `gorm:"column:rate"`
	UpstreamSystem              string     `gorm:"column:upstream_system"`
	UpstreamGroup               string     `gorm:"column:upstream_group"`
	UpstreamSyncIntervalMinutes int        `gorm:"column:upstream_sync_interval_minutes"`
	UpstreamSyncRateAdd         float64    `gorm:"column:upstream_sync_rate_add"`
	UpstreamCurrency            string     `gorm:"column:upstream_currency"`
	SiteCurrency                string     `gorm:"column:site_currency"`
	CurrencyRate                float64    `gorm:"column:currency_rate"`
	UpstreamCurrencyRate        float64    `gorm:"column:upstream_currency_rate"`
	UpstreamUserID              string     `gorm:"column:upstream_user_id"`
	UpstreamAccessTokenEnc      string     `gorm:"column:upstream_access_token_enc"`
	UpstreamSyncedAt            *time.Time `gorm:"column:upstream_synced_at"`
	VolcAK                      string     `gorm:"column:volc_ak"`
	VolcSKEnc                   string     `gorm:"column:volc_sk_enc"`
	UpstreamBalance             *float64   `gorm:"column:upstream_balance"`
	UpstreamBalanceUpdatedAt    *time.Time `gorm:"column:upstream_balance_updated_at"`
	QuotaLimit                  float64    `gorm:"column:quota_limit"`
	DailyQuotaLimit             float64    `gorm:"column:daily_quota_limit"`
	WeeklyQuotaLimit            float64    `gorm:"column:weekly_quota_limit"`
	MonthlyQuotaLimit           float64    `gorm:"column:monthly_quota_limit"`
	DailyResetHour              int        `gorm:"column:daily_reset_hour"`
	DailyResetMinute            int        `gorm:"column:daily_reset_minute"`
	DailyResetCooldownMinutes   int        `gorm:"column:daily_reset_cooldown_minutes"`
	CreatedAt                   time.Time  `gorm:"column:created_at"`
	UpdatedAt                   time.Time  `gorm:"column:updated_at"`
}

func (channelConfigRow) TableName() string { return "channel_configs" }

func (r channelConfigRow) toV1() v1ChannelConfig {
	var catIDs []uint64
	if strings.TrimSpace(r.CategoryIDsJSON) != "" {
		_ = json.Unmarshal([]byte(r.CategoryIDsJSON), &catIDs)
	}
	if catIDs == nil {
		catIDs = []uint64{}
	}
	hasAPIKey := strings.TrimSpace(r.APIKeyEnc) != ""
	mask := ""
	if hasAPIKey {
		mask = cmp.Or(r.APIKeyMask, "••••••••")
	}
	hasAccessTok := strings.TrimSpace(r.UpstreamAccessTokenEnc) != ""
	accessTokMask := ""
	if hasAccessTok {
		accessTokMask = "••••••••"
	}
	hasVolc := strings.TrimSpace(r.VolcAK) != "" && strings.TrimSpace(r.VolcSKEnc) != ""
	volcSKMask := ""
	if strings.TrimSpace(r.VolcSKEnc) != "" {
		volcSKMask = "••••••••"
	}
	var syncedAtStr *string
	if r.UpstreamSyncedAt != nil {
		s := r.UpstreamSyncedAt.UTC().Format(time.RFC3339)
		syncedAtStr = &s
	}
	var balUpdatedStr *string
	if r.UpstreamBalanceUpdatedAt != nil {
		s := r.UpstreamBalanceUpdatedAt.UTC().Format(time.RFC3339)
		balUpdatedStr = &s
	}
	upCurrRate := r.UpstreamCurrencyRate
	if upCurrRate <= 0 {
		upCurrRate = r.CurrencyRate
	}
	if upCurrRate <= 0 {
		upCurrRate = defaultUpstreamCurrencyRate
	}
	w := r.Weight
	if w <= 0 {
		w = 1
	}
	return v1ChannelConfig{
		ID:                          r.ID,
		Name:                        r.Name,
		ProviderType:                r.ProviderType,
		BaseURL:                     r.BaseURL,
		APIKeyEnc:                   r.APIKeyEnc,
		APIKeyMask:                  mask,
		HasAPIKey:                   hasAPIKey,
		Remark:                      r.Remark,
		YID:                         cmp.Or(r.YID, "Y"+strconv.FormatUint(1000+r.ID, 10)),
		CategoryID:                  r.CategoryID,
		CategoryIDs:                 catIDs,
		Status:                      r.Status,
		SortOrder:                   r.SortOrder,
		Priority:                    r.Priority,
		Weight:                      w,
		Rate:                        r.Rate,
		UpstreamSystem:              r.UpstreamSystem,
		UpstreamGroup:               r.UpstreamGroup,
		UpstreamSyncIntervalMinutes: r.UpstreamSyncIntervalMinutes,
		UpstreamSyncRateAdd:         r.UpstreamSyncRateAdd,
		UpstreamCurrency:            r.UpstreamCurrency,
		SiteCurrency:                cmp.Or(r.SiteCurrency, "USD"),
		CurrencyRate:                upCurrRate,
		UpstreamCurrencyRate:        upCurrRate,
		UpstreamUserID:              r.UpstreamUserID,
		UpstreamAccessToken:         accessTokMask,
		HasUpstreamAccessToken:      hasAccessTok,
		UpstreamSyncedAt:            syncedAtStr,
		VolcAK:                      r.VolcAK,
		VolcSK:                      volcSKMask,
		HasVolcCredentials:          hasVolc,
		UpstreamBalance:             r.UpstreamBalance,
		UpstreamBalanceUpdatedAt:    balUpdatedStr,
		BoundChannels:               []v1BoundChannelSummary{},
		QuotaLimit:                  r.QuotaLimit,
		DailyQuotaLimit:             r.DailyQuotaLimit,
		WeeklyQuotaLimit:            r.WeeklyQuotaLimit,
		MonthlyQuotaLimit:           r.MonthlyQuotaLimit,
		DailyResetHour:              r.DailyResetHour,
		DailyResetMinute:            r.DailyResetMinute,
		DailyResetCooldownMinutes:   r.DailyResetCooldownMinutes,
		CreatedAt:                   r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:                   r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) loadV1ChannelConfigs() []v1ChannelConfig {
	var rows []channelConfigRow
	if err := s.db.Order("sort_order ASC, id DESC").Find(&rows).Error; err != nil {
		return []v1ChannelConfig{}
	}
	out := make([]v1ChannelConfig, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toV1())
	}
	return out
}

func (s *Server) findV1ChannelConfig(id uint64) (v1ChannelConfig, bool) {
	if id == 0 {
		return v1ChannelConfig{}, false
	}
	var row channelConfigRow
	if err := s.db.First(&row, id).Error; err != nil {
		return v1ChannelConfig{}, false
	}
	return row.toV1(), true
}

func (s *Server) v1ListChannelConfigs(c *gin.Context) {
	list := s.loadV1ChannelConfigs()
	boundMap := s.collectBoundChannelsByConfigID(c.Request.Context())
	for i := range list {
		list[i].APIKeyEnc = ""
		if b := boundMap[list[i].ID]; len(b) > 0 {
			list[i].BoundChannels = b
			list[i].BoundChannelCount = len(b)
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": list})
}

func (s *Server) v1CreateChannelConfig(c *gin.Context) {
	var req struct {
		Name                        string   `json:"name" binding:"required"`
		ProviderType                string   `json:"provider_type"`
		BaseURL                     string   `json:"base_url"`
		APIKey                      string   `json:"api_key"`
		Remark                      string   `json:"remark"`
		YID                         string   `json:"yid"`
		CategoryID                  *uint64  `json:"category_id"`
		CategoryIDs                 []uint64 `json:"category_ids"`
		Status                      *int8    `json:"status"`
		SortOrder                   int      `json:"sort_order"`
		Priority                    int      `json:"priority"`
		Weight                      int      `json:"weight"`
		Rate                        float64  `json:"rate"`
		UpstreamSystem              string   `json:"upstream_system"`
		UpstreamGroup               string   `json:"upstream_group"`
		UpstreamSyncIntervalMinutes int      `json:"upstream_sync_interval_minutes"`
		UpstreamSyncRateAdd         float64  `json:"upstream_sync_rate_add"`
		UpstreamCurrency            string   `json:"upstream_currency"`
		SiteCurrency                string   `json:"site_currency"`
		CurrencyRate                float64  `json:"currency_rate"`
		UpstreamCurrencyRate        float64  `json:"upstream_currency_rate"`
		UpstreamUserID              string   `json:"upstream_user_id"`
		UpstreamAccessToken         string   `json:"upstream_access_token"`
		VolcAK                      string   `json:"volc_ak"`
		VolcSK                      string   `json:"volc_sk"`
		QuotaLimit                  float64  `json:"quota_limit"`
		DailyQuotaLimit             float64  `json:"daily_quota_limit"`
		WeeklyQuotaLimit            float64  `json:"weekly_quota_limit"`
		MonthlyQuotaLimit           float64  `json:"monthly_quota_limit"`
		DailyResetHour              int      `json:"daily_reset_hour"`
		DailyResetMinute            int      `json:"daily_reset_minute"`
		DailyResetCooldownMinutes   int      `json:"daily_reset_cooldown_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	encryptSecret := func(raw string) string {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || isMaskedSecret(trimmed) {
			return ""
		}
		if s.keyring != nil {
			if ciph, err := s.keyring.Encrypt(trimmed); err == nil {
				return ciph
			}
		}
		return trimmed
	}
	enc := encryptSecret(req.APIKey)
	accessTokEnc := encryptSecret(req.UpstreamAccessToken)
	volcSKEnc := encryptSecret(req.VolcSK)

	catIDsJSON := "[]"
	if len(req.CategoryIDs) > 0 {
		if b, err := json.Marshal(req.CategoryIDs); err == nil {
			catIDsJSON = string(b)
		}
	}
	now := s.now().UTC()
	status := int8(1)
	if req.Status != nil {
		status = *req.Status
	}
	rate := req.Rate
	if rate <= 0 {
		rate = 1
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}
	upCurrRate := req.UpstreamCurrencyRate
	if upCurrRate <= 0 {
		upCurrRate = req.CurrencyRate
	}
	if upCurrRate <= 0 {
		upCurrRate = defaultUpstreamCurrencyRate
	}
	row := channelConfigRow{
		Name:                        strings.TrimSpace(req.Name),
		ProviderType:                cmp.Or(strings.TrimSpace(req.ProviderType), "兼容"),
		BaseURL:                     strings.TrimSpace(req.BaseURL),
		APIKeyEnc:                   enc,
		APIKeyMask:                  "••••••••",
		Remark:                      req.Remark,
		YID:                         strings.TrimSpace(req.YID),
		CategoryID:                  req.CategoryID,
		CategoryIDsJSON:             catIDsJSON,
		Status:                      status,
		SortOrder:                   req.SortOrder,
		Priority:                    req.Priority,
		Weight:                      weight,
		Rate:                        rate,
		UpstreamSystem:              strings.TrimSpace(req.UpstreamSystem),
		UpstreamGroup:               strings.TrimSpace(req.UpstreamGroup),
		UpstreamSyncIntervalMinutes: max(0, req.UpstreamSyncIntervalMinutes),
		UpstreamSyncRateAdd:         max(0, req.UpstreamSyncRateAdd),
		UpstreamCurrency:            strings.TrimSpace(req.UpstreamCurrency),
		SiteCurrency:                cmp.Or(strings.TrimSpace(req.SiteCurrency), "USD"),
		CurrencyRate:                upCurrRate,
		UpstreamCurrencyRate:        upCurrRate,
		UpstreamUserID:              strings.TrimSpace(req.UpstreamUserID),
		UpstreamAccessTokenEnc:      accessTokEnc,
		VolcAK:                      strings.TrimSpace(req.VolcAK),
		VolcSKEnc:                   volcSKEnc,
		QuotaLimit:                  cmp.Or(req.QuotaLimit, -1),
		DailyQuotaLimit:             cmp.Or(req.DailyQuotaLimit, -1),
		WeeklyQuotaLimit:            cmp.Or(req.WeeklyQuotaLimit, -1),
		MonthlyQuotaLimit:           cmp.Or(req.MonthlyQuotaLimit, -1),
		DailyResetHour:              req.DailyResetHour,
		DailyResetMinute:            req.DailyResetMinute,
		DailyResetCooldownMinutes:   req.DailyResetCooldownMinutes,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}
	ctx := c.Request.Context()
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		fail(c, http.StatusInternalServerError, "save channel config failed")
		return
	}
	if row.YID == "" {
		row.YID = "Y" + strconv.FormatUint(1000+row.ID, 10)
		_ = s.db.WithContext(ctx).Model(&row).Update("yid", row.YID).Error
	}
	if strings.EqualFold(row.UpstreamSystem, "newapi") && row.UpstreamGroup != "" {
		if syncedRow, _, err := s.syncSingleChannelConfigRate(ctx, row.ID, "save"); err == nil && syncedRow != nil {
			row = *syncedRow
		}
	}
	item := row.toV1()
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
	ctx := c.Request.Context()
	var row channelConfigRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "channel config not found")
			return
		}
		fail(c, http.StatusInternalServerError, "load channel config failed")
		return
	}
	updates := map[string]any{"updated_at": s.now().UTC()}
	if v, ok := req["name"].(string); ok && strings.TrimSpace(v) != "" {
		updates["name"] = strings.TrimSpace(v)
	}
	if v, ok := req["provider_type"].(string); ok {
		updates["provider_type"] = strings.TrimSpace(v)
	}
	if v, ok := req["base_url"].(string); ok {
		updates["base_url"] = strings.TrimSpace(v)
	}
	if v, ok := req["api_key"].(string); ok {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			updates["api_key_enc"] = ""
		} else if !isMaskedSecret(trimmed) && s.keyring != nil {
			if ciph, err := s.keyring.Encrypt(trimmed); err == nil {
				updates["api_key_enc"] = ciph
			}
		}
	}
	if v, ok := req["remark"].(string); ok {
		updates["remark"] = v
	}
	if v, ok := req["rate"]; ok {
		if f, ok := parseFlexibleNumber(v); ok && f > 0 {
			updates["rate"] = f
		}
	}
	if v, ok := req["status"]; ok {
		if f, ok := parseFlexibleNumber(v); ok {
			updates["status"] = int8(f)
		}
	}
	if v, ok := req["sort_order"]; ok {
		if f, ok := parseFlexibleNumber(v); ok {
			updates["sort_order"] = int(f)
		}
	}
	if v, ok := req["priority"]; ok {
		if f, ok := parseFlexibleNumber(v); ok {
			updates["priority"] = int(f)
		}
	}
	if v, ok := req["weight"]; ok {
		if f, ok := parseFlexibleNumber(v); ok && f > 0 {
			updates["weight"] = int(f)
		}
	}
	if rawCat, exists := req["category_id"]; exists {
		if rawCat == nil {
			updates["category_id"] = nil
		} else if f, ok := parseFlexibleNumber(rawCat); ok && f > 0 {
			catVal := uint64(f)
			updates["category_id"] = &catVal
		}
	}
	if rawCats, exists := req["category_ids"]; exists {
		if b, err := json.Marshal(rawCats); err == nil {
			updates["category_ids_json"] = string(b)
		}
	}
	if v, ok := req["upstream_system"].(string); ok {
		updates["upstream_system"] = strings.TrimSpace(v)
	}
	if v, ok := req["upstream_group"].(string); ok {
		updates["upstream_group"] = strings.TrimSpace(v)
	}
	if v, ok := req["upstream_sync_interval_minutes"]; ok {
		if f, ok := parseFlexibleNumber(v); ok {
			updates["upstream_sync_interval_minutes"] = max(0, int(f))
		}
	}
	if v, ok := req["upstream_sync_rate_add"]; ok {
		if f, ok := parseFlexibleNumber(v); ok {
			updates["upstream_sync_rate_add"] = max(0, f)
		}
	}
	if v, ok := req["upstream_currency"].(string); ok {
		updates["upstream_currency"] = strings.TrimSpace(v)
	}
	if v, ok := req["site_currency"].(string); ok {
		updates["site_currency"] = strings.TrimSpace(v)
	}
	if v, ok := req["currency_rate"]; ok {
		if f, ok := parseFlexibleNumber(v); ok && f > 0 {
			updates["currency_rate"] = f
		}
	}
	if v, ok := req["upstream_currency_rate"]; ok {
		if f, ok := parseFlexibleNumber(v); ok && f > 0 {
			updates["upstream_currency_rate"] = f
			updates["currency_rate"] = f
		}
	}
	if v, ok := req["upstream_user_id"].(string); ok {
		updates["upstream_user_id"] = strings.TrimSpace(v)
	}
	if v, ok := req["upstream_access_token"].(string); ok {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			updates["upstream_access_token_enc"] = ""
		} else if !isMaskedSecret(trimmed) && s.keyring != nil {
			if ciph, err := s.keyring.Encrypt(trimmed); err == nil {
				updates["upstream_access_token_enc"] = ciph
			}
		}
	}
	if v, ok := req["volc_ak"].(string); ok {
		updates["volc_ak"] = strings.TrimSpace(v)
	}
	if v, ok := req["volc_sk"].(string); ok {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			updates["volc_sk_enc"] = ""
		} else if !isMaskedSecret(trimmed) && s.keyring != nil {
			if ciph, err := s.keyring.Encrypt(trimmed); err == nil {
				updates["volc_sk_enc"] = ciph
			}
		}
	}
	if err := s.db.WithContext(ctx).Model(&row).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "update channel config failed")
		return
	}
	_ = s.db.WithContext(ctx).First(&row, id).Error
	if strings.EqualFold(row.UpstreamSystem, "newapi") && row.UpstreamGroup != "" {
		if syncedRow, _, err := s.syncSingleChannelConfigRate(ctx, row.ID, "save"); err == nil && syncedRow != nil {
			row = *syncedRow
		}
	}
	out := row.toV1()
	out.APIKeyEnc = ""
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (s *Server) v1DeleteChannelConfig(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	res := s.db.WithContext(ctx).Delete(&channelConfigRow{}, id)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "delete channel config failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "channel config not found")
		return
	}
	_ = s.db.WithContext(ctx).Where("config_id = ?", id).Delete(&channelConfigSyncLogRow{}).Error
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
	ctx := c.Request.Context()
	var src channelConfigRow
	if err := s.db.WithContext(ctx).First(&src, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "channel config not found")
			return
		}
		fail(c, http.StatusInternalServerError, "load channel config failed")
		return
	}
	now := s.now().UTC()
	copyRow := src
	copyRow.ID = 0
	copyRow.Name = cmp.Or(strings.TrimSpace(req.Name), src.Name+" (副本)")
	copyRow.YID = ""
	copyRow.CreatedAt = now
	copyRow.UpdatedAt = now
	if err := s.db.WithContext(ctx).Create(&copyRow).Error; err != nil {
		fail(c, http.StatusInternalServerError, "copy channel config failed")
		return
	}
	copyRow.YID = "Y" + strconv.FormatUint(1000+copyRow.ID, 10)
	_ = s.db.WithContext(ctx).Model(&copyRow).Update("yid", copyRow.YID).Error
	out := copyRow.toV1()
	out.APIKeyEnc = ""
	c.JSON(http.StatusCreated, gin.H{"data": out})
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
	var req struct {
		Name      string `json:"name" binding:"required"`
		SortOrder int    `json:"sort_order"`
		IsActive  int8   `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	id, _ := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	item := forward.ChannelCategory{Name: strings.TrimSpace(req.Name), SortOrder: req.SortOrder, IsActive: req.IsActive}
	status := http.StatusCreated
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		return updateSettingJSON(tx, v1SettingChannelProviders, []forward.ChannelCategory{}, func(list *[]forward.ChannelCategory) error {
			var maxID uint64
			for i, existing := range *list {
				if id != 0 && existing.ID == id {
					item.ID = id
					(*list)[i] = item
					status = http.StatusOK
					return nil
				}
				maxID = max(maxID, existing.ID)
			}
			item.ID = maxID + 1
			*list = append(*list, item)
			return nil
		})
	})
	if err != nil {
		s.Log().Error("save channel provider", "err", err)
		fail(c, http.StatusInternalServerError, "save channel provider failed")
		return
	}
	s.invalidateSettingsCache()
	c.JSON(status, item)
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

func updateV1ChannelMeta(tx *gorm.DB, id uint64, update func(*v1ChannelMeta)) error {
	return updateSettingJSON(tx, v1SettingChannelMeta, map[uint64]v1ChannelMeta{}, func(m *map[uint64]v1ChannelMeta) error {
		if *m == nil {
			*m = make(map[uint64]v1ChannelMeta)
		}
		meta := (*m)[id]
		update(&meta)
		(*m)[id] = meta
		return nil
	})
}

func (s *Server) upsertRawSetting(name, val string) error {
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&Setting{Name: name, Value: val}).Error
	if err == nil {
		s.invalidateSettingsCache()
	}
	return err
}

func (s *Server) v1ChannelConfigQuotaReset(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	s.writeAudit(c, "v1_channel_config_quota_reset", strconv.FormatUint(id, 10), "reset channel config quota")
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id})
}

func (s *Server) v1SimulateChannelBilling(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rdb := s.readOnly().WithContext(ctx)

	var ch relay.Channel
	if err := rdb.First(&ch, id).Error; err != nil {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}

	var req struct {
		Model              string   `json:"model"`
		SubChannelID       *uint64  `json:"sub_channel_id"`
		UserLevelID        *uint64  `json:"user_level_id"`
		UserID             string   `json:"user_id"`
		CustomDiscount     *float64 `json:"custom_discount"`
		CustomUpstreamRate *float64 `json:"custom_upstream_rate"`
		SimulatedTime      string   `json:"simulated_time"`
		MockUserBalance    *float64 `json:"mock_user_balance"`
		Usage              struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			CachedTokens     int64 `json:"cached_tokens"`
			CacheWriteTokens int64 `json:"cache_write_tokens"`
			AudioTokens      int64 `json:"audio_tokens"`
		} `json:"usage"`
		Features struct {
			WebSearch       int64    `json:"web_search"`
			Resolution      string   `json:"resolution"`
			DurationSeconds *float64 `json:"duration_seconds"`
			ImageCount      *int64   `json:"image_count"`
			Size            string   `json:"size"`
			TextCharacters  *int64   `json:"text_characters"`
		} `json:"features"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid simulate request")
		return
	}

	reqModel := strings.TrimSpace(req.Model)
	if reqModel != "" {
		if resolved, _ := s.resolveCanonicalChannelModels(ctx, []string{reqModel}); len(resolved) > 0 {
			reqModel = resolved[0]
		}
	}
	if reqModel == "" {
		for item := range strings.SplitSeq(ch.Models, ",") {
			if m := strings.TrimSpace(item); m != "" {
				reqModel = m
				break
			}
		}
	}
	if reqModel == "" {
		fail(c, http.StatusBadRequest, "model is required for billing simulation")
		return
	}

	metaMap := s.loadV1ChannelMetaMap()
	meta := metaMap[ch.ID]
	primary, subs := s.resolveV1SubChannels(meta.PresetID, meta.Config)

	subOptions := make([]gin.H, 0, len(subs))
	for _, sub := range subs {
		r := sub.Rate
		if r <= 0 {
			r = 1
		}
		subOptions = append(subOptions, gin.H{
			"id":            sub.ID,
			"name":          sub.Name,
			"rate":          r,
			"status":        sub.Status,
			"provider_type": sub.ProviderType,
		})
	}

	upstreamName := "自定义上游"
	upstreamRateDec := ch.UpstreamCostRatio
	if !upstreamRateDec.IsPositive() {
		upstreamRateDec = decimal.NewFromInt(1)
	}
	if req.CustomUpstreamRate != nil && *req.CustomUpstreamRate >= 0 {
		upstreamName = "自定义上游 (手动指定倍率)"
		upstreamRateDec = decimal.NewFromFloat(*req.CustomUpstreamRate)
	} else if req.SubChannelID != nil && *req.SubChannelID > 0 {
		for _, sub := range subs {
			if sub.ID == *req.SubChannelID {
				upstreamName = "HA子渠道: " + sub.Name
				if sub.Rate > 0 {
					upstreamRateDec = decimal.NewFromFloat(sub.Rate)
				}
				break
			}
		}
	} else if len(subs) > 0 {
		upstreamName = "HA子渠道(默认首项): " + subs[0].Name
		if subs[0].Rate > 0 {
			upstreamRateDec = decimal.NewFromFloat(subs[0].Rate)
		}
	} else if primary != nil {
		upstreamName = "上游预设: " + primary.Name
		if primary.Rate > 0 {
			upstreamRateDec = decimal.NewFromFloat(primary.Rate)
		}
	}

	resolvedModel := ch.UpstreamModel(reqModel)
	mappingHit := resolvedModel != reqModel
	mappingSource := "未启用模型映射"
	if mappingHit {
		mappingSource = "精确匹配"
	}

	var binding pricing.ModelRuleBinding
	hasBinding := rdb.Where("model = ?", reqModel).First(&binding).Error == nil
	if !hasBinding && resolvedModel != reqModel {
		hasBinding = rdb.Where("model = ?", resolvedModel).First(&binding).Error == nil
	}

	var rule *pricing.BillingRule
	if hasBinding && binding.BillingRuleID > 0 {
		var r pricing.BillingRule
		if rdb.Where("id = ?", binding.BillingRuleID).First(&r).Error == nil {
			rule = &r
		}
	}

	levelDisc := decimal.NewFromInt(1)
	if req.UserLevelID != nil && *req.UserLevelID > 0 {
		var ratios []GroupRatio
		_ = rdb.Order("name").Find(&ratios).Error
		idx := int(*req.UserLevelID) - 1
		if idx >= 0 && idx < len(ratios) && ratios[idx].Ratio.IsPositive() {
			levelDisc = ratios[idx].Ratio
		}
	}
	if req.CustomDiscount != nil && *req.CustomDiscount >= 0 {
		levelDisc = decimal.NewFromFloat(*req.CustomDiscount)
	}

	modelDisc := decimal.NewFromInt(1)
	if hasBinding {
		modelDisc = binding.EffectiveDiscount()
	}
	effectiveDisc := levelDisc.Mul(modelDisc)
	floorClamped := false
	floorRate := decimal.Zero
	if hasBinding && binding.SiteDiscountEnabled == 1 && binding.SiteDiscount.IsPositive() {
		floorRate = binding.SiteDiscount
		if effectiveDisc.LessThan(floorRate) {
			effectiveDisc = floorRate
			floorClamped = true
		}
	}

	cstLoc := time.FixedZone("CST", 8*3600)
	simTime := s.now().In(cstLoc)
	if rawT := strings.TrimSpace(req.SimulatedTime); rawT != "" {
		layout := "15:04:05"
		if len(rawT) == 5 {
			layout = "15:04"
		}
		if parsed, err := time.ParseInLocation(layout, rawT, cstLoc); err == nil {
			simTime = time.Date(simTime.Year(), simTime.Month(), simTime.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, cstLoc)
		}
	}
	timeMult := pricing.ResolveTimeMultiplierAt(rule, simTime, cstLoc)

	ut := pricing.UsageTokens{
		Prompt:        req.Usage.PromptTokens,
		Completion:    req.Usage.CompletionTokens,
		Total:         req.Usage.PromptTokens + req.Usage.CompletionTokens,
		Cached:        req.Usage.CachedTokens,
		CacheWrite:    req.Usage.CacheWriteTokens,
		CacheCreation: req.Usage.CacheWriteTokens,
		AudioTokens:   req.Usage.AudioTokens,
		WebSearch:     req.Features.WebSearch,
	}
	cascadeMul := 1.0
	cascadeNote := ""
	if s.forwardResolver != nil {
		if fwd, ferr := s.forwardResolver.ResolveForModel(ctx, s.db, reqModel, "视频", "/v1/video/generations"); ferr == nil && fwd != nil && fwd.IsCascade {
			cascadeMul = fwd.LookupResMul(req.Features.Resolution)
			ut.Completion = int64(float64(ut.Completion) * cascadeMul)
			ut.Total = ut.Prompt + ut.Completion
			cascadeNote = fmt.Sprintf("级联超分 %s 倍率 %.2fx (rule=%s)", cmp.Or(req.Features.Resolution, "720p"), cascadeMul, fwd.EID)
		}
	}
	featNoTime := pricing.ExtractedFeatures{
		Resolution: req.Features.Resolution,
		Size:       req.Features.Size,
		ImageCount: req.Features.ImageCount,
		WebSearch:  func() *int64 { v := req.Features.WebSearch; return &v }(),
	}
	if req.Features.DurationSeconds != nil {
		d := decimal.NewFromFloat(*req.Features.DurationSeconds)
		featNoTime.DurationSeconds = &d
		featNoTime.HasVideo = true
	}
	if req.Features.TextCharacters != nil {
		featNoTime.TextCharacters = req.Features.TextCharacters
	}
	featWithTime := featNoTime
	featWithTime.TimeMultiplier = &timeMult

	baseCostDec, _ := pricing.ComputeCostRaw(rule, ut, decimal.NewFromInt(1), featNoTime)
	baseCostDec = pricing.RoundMoney(baseCostDec)
	actualCostDec, billDetail := pricing.ComputeCost(rule, ut, effectiveDisc, featWithTime)

	preDeductDec := actualCostDec
	if hasBinding && binding.PreDeduction.IsPositive() && binding.PreDeduction.GreaterThan(preDeductDec) {
		preDeductDec = pricing.RoundMoney(binding.PreDeduction)
	}
	refundDec := decimal.Zero
	addDeductDec := decimal.Zero
	if preDeductDec.GreaterThan(actualCostDec) {
		refundDec = pricing.RoundMoney(preDeductDec.Sub(actualCostDec))
	} else if actualCostDec.GreaterThan(preDeductDec) {
		addDeductDec = pricing.RoundMoney(actualCostDec.Sub(preDeductDec))
	}

	upstreamCostDec := pricing.RoundMoney(baseCostDec.Mul(upstreamRateDec))
	grossProfitDec := actualCostDec.Sub(upstreamCostDec)
	marginPctDec := decimal.Zero
	if actualCostDec.IsPositive() {
		marginPctDec = grossProfitDec.Div(actualCostDec).Mul(decimal.NewFromInt(100))
	} else if upstreamCostDec.IsPositive() {
		marginPctDec = decimal.NewFromInt(-100)
	}

	baseF, _ := baseCostDec.Float64()
	preF, _ := preDeductDec.Float64()
	actF, _ := actualCostDec.Float64()
	refundF, _ := refundDec.Float64()
	addF, _ := addDeductDec.Float64()
	upRateF, _ := upstreamRateDec.Float64()
	upCostF, _ := upstreamCostDec.Float64()
	profitF, _ := grossProfitDec.Float64()
	marginF, _ := marginPctDec.Float64()
	discF, _ := effectiveDisc.Float64()
	timeMultF, _ := timeMult.Float64()
	chRateF, _ := ch.UpstreamCostRatio.Float64()

	var warningMsg string
	if ch.Status == 0 {
		warningMsg = "当前渠道处于禁用状态，线上调用将被拦截；此处仅演算理论计费。"
	} else if req.MockUserBalance != nil && decimal.NewFromFloat(*req.MockUserBalance).LessThan(preDeductDec) {
		warningMsg = fmt.Sprintf("模拟钱包余额 ($%.4f) 低于预扣冻结 ($%.6f)，线上将被余额不足拦截。", *req.MockUserBalance, preF)
	}

	ruleInfo := gin.H{
		"rule_name":           "系统默认兜底规则",
		"pricing_type":        "standard",
		"billing_type":        "tokens",
		"is_default_fallback": true,
		"rates":               gin.H{},
	}
	if rule != nil {
		pF, _ := rule.PromptRate.Float64()
		cF, _ := rule.CompletionRate.Float64()
		ruleInfo = gin.H{
			"rule_id":             rule.ID,
			"rule_name":           rule.Name,
			"pricing_type":        rule.PricingType,
			"billing_type":        rule.BillingType,
			"is_default_fallback": false,
			"rates": gin.H{
				"prompt_rate":     pF,
				"completion_rate": cF,
			},
		}
	}

	isLoss := grossProfitDec.IsNegative()
	floorStatus := "pass"
	floorDesc := fmt.Sprintf("有效折扣率 %.4fx 未低于保底限价。", discF)
	if floorClamped {
		floorStatus = "warn"
		floorDesc = fmt.Sprintf("触发模型站点保底折扣限价，强制夹紧至 %sx。", floorRate.String())
	}
	marginStatus := "pass"
	if isLoss {
		marginStatus = "danger"
	}

	trace := []gin.H{
		{
			"step":        1,
			"stage":       "routing",
			"title":       "模型路由与名称映射",
			"formula":     fmt.Sprintf("%s -> %s", reqModel, resolvedModel),
			"description": mappingSource,
			"status":      "normal",
		},
		{
			"step":        2,
			"stage":       "pre_deduct",
			"title":       "请求预冻结检查",
			"formula":     fmt.Sprintf("预扣冻结: %.6f USD", preF),
			"description": "进入上游转发前创建账本 Hold 冻结资金。",
			"value":       preF,
			"status":      "normal",
		},
		{
			"step":        3,
			"stage":       "base_cost",
			"title":       "基础计费规则原价",
			"formula":     fmt.Sprintf("基础原价 = %.6f USD", baseF),
			"description": fmt.Sprintf("依据计费规则 %s 按 1.0x 原价核算。", ruleInfo["rule_name"]),
			"value":       baseF,
			"status":      "pass",
		},
		{
			"step":        4,
			"stage":       "discount",
			"title":       "分组与模型折扣折算",
			"formula":     fmt.Sprintf("有效折扣率 = %.4fx", discF),
			"description": "合并分组/等级倍率与模型绑定折扣。",
			"value":       discF,
			"status":      "normal",
		},
		{
			"step":        5,
			"stage":       "channel_rate_and_floor",
			"title":       "折扣保底防倒贴校验",
			"formula":     fmt.Sprintf("最终折扣倍率 = %.4fx", discF),
			"description": floorDesc,
			"value":       discF,
			"status":      floorStatus,
		},
		{
			"step":        6,
			"stage":       "time_multiplier",
			"title":       "峰谷时段倍率核算",
			"formula":     fmt.Sprintf("时段倍率 = %.4fx", timeMultF),
			"description": fmt.Sprintf("按东八区时刻 %s 匹配时段乘数。", simTime.Format("15:04:05")),
			"value":       timeMultF,
			"status":      "normal",
		},
		{
			"step":        7,
			"stage":       "settlement",
			"title":       "终态账本结算退补",
			"formula":     fmt.Sprintf("实扣 %.6f USD (退还 %.6f / 补扣 %.6f)", actF, refundF, addF),
			"description": billDetail,
			"value":       actF,
			"status":      "pass",
		},
		{
			"step":        8,
			"stage":       "cost_and_margin",
			"title":       "上游采购成本与平台毛利",
			"formula":     fmt.Sprintf("毛利 %+.6f USD (%.2f%%)", profitF, marginF),
			"description": fmt.Sprintf("%s · 采购倍率 %.4fx", upstreamName, upRateF),
			"value":       profitF,
			"status":      marginStatus,
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"channel_info": gin.H{
			"channel_id":    ch.ID,
			"channel_name":  ch.Name,
			"channel_rate":  chRateF,
			"provider_type": cmp.Or(meta.RawProviderType, ch.Provider),
			"group_aid":     cmp.Or(meta.GroupAID, "A"+strconv.FormatUint(ch.ID, 10)),
			"status":        ch.Status,
			"is_disabled":   ch.Status == 0,
		},
		"available_sub_channels": subOptions,
		"routing": gin.H{
			"requested_model":   reqModel,
			"resolved_model":    resolvedModel,
			"model_mapping_hit": mappingHit,
			"mapping_source":    mappingSource,
			"cascade_res_mul":   cascadeMul,
			"cascade_note":      cascadeNote,
		},
		"billing_rule":   ruleInfo,
		"pipeline_trace": trace,
		"settlement": gin.H{
			"pre_deducted":        preF,
			"actual_cost":         actF,
			"refund_amount":       refundF,
			"additional_deduct":   addF,
			"billing_detail_text": billDetail,
		},
		"cost_and_margin": gin.H{
			"upstream_name":  upstreamName,
			"upstream_rate":  upRateF,
			"upstream_cost":  upCostF,
			"gross_profit":   profitF,
			"margin_percent": marginF,
			"is_loss_making": isLoss,
		},
		"warning": warningMsg,
	})
}
