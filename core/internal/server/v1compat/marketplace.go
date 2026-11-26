package v1compat

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ClassificationItem 描述模型厂商或模型分类筛选项（对应 /api/v1/model-providers 与 /api/v1/model-types）。
type ClassificationItem struct {
	ID              uint64 `json:"id"`
	Name            string `json:"name"`
	NameEn          string `json:"name_en,omitempty"`
	Logo            string `json:"logo,omitempty"`
	SortOrder       int    `json:"sort_order"`
	IsActive        int8   `json:"is_active"`
	IsSystem        int8   `json:"is_system"`
	Remark          string `json:"remark,omitempty"`
	DefaultFeatures string `json:"default_features,omitempty"`
	ModelCount      int64  `json:"model_count"`
}

// MarketplaceModel 是 ModelMarketplace.tsx 消费的模型卡片 DTO。
// 严格执行渠道隔离红线：绝不包含 channel_id / channel_name / base_url / provider 采购价信息。
type MarketplaceModel struct {
	ID                    uint64             `json:"id"`
	Mid                   string             `json:"mid"`
	Name                  string             `json:"name"`
	ModelID               string             `json:"model_id"`
	OriginalID            string             `json:"original_id,omitempty"`
	ProviderID            uint64             `json:"provider_id"`
	ProviderName          string             `json:"provider_name"`
	ProviderNameEn        string             `json:"provider_name_en,omitempty"`
	ProviderLogo          string             `json:"provider_logo,omitempty"`
	TypeID                uint64             `json:"type_id"`
	TypeName              string             `json:"type_name"`
	TypeNameEn            string             `json:"type_name_en,omitempty"`
	TypeLogo              string             `json:"type_logo,omitempty"`
	Logo                  string             `json:"logo,omitempty"`
	SortOrder             int                `json:"sort_order"`
	Description           string             `json:"description"`
	DescriptionEn         string             `json:"description_en,omitempty"`
	ModelDescription      string             `json:"model_description,omitempty"`
	FeatureAttributes     string             `json:"feature_attributes,omitempty"`
	FeatureKeys           []string           `json:"feature_keys,omitempty"`
	SiteDiscount          decimal.Decimal    `json:"site_discount"`
	SiteDiscountEnabled   int8               `json:"site_discount_enabled"`
	GlobalDiscount        decimal.Decimal    `json:"global_discount"`
	GlobalDiscountEnabled int8               `json:"global_discount_enabled"`
	DiscountSchedule      string             `json:"discount_schedule,omitempty"`
	Billing               any                `json:"billing"`
	BillingInvalid        bool               `json:"billing_invalid"`
	CreatedAt             string             `json:"created_at"`
	VariantCount          int                `json:"variant_count,omitempty"`
	Variants              []MarketplaceModel `json:"variants,omitempty"`
}

type providerProfile struct {
	ID       uint64
	Name     string
	NameEn   string
	Logo     string
	Aliases  []string
	SortRank int
}

var standardProviderProfiles = []providerProfile{
	{ID: 1, Name: "字节火山", NameEn: "Volcengine", Logo: "doubao", Aliases: []string{"volcengine", "modelark", "ark", "doubao", "seedance", "seedream", "seed-tts", "bytedance", "字节", "火山"}, SortRank: 100},
	{ID: 2, Name: "OpenAI", NameEn: "OpenAI", Logo: "openai", Aliases: []string{"openai", "gpt-", "o1", "o3", "o4", "sora", "dall-e", "whisper", "tts-", "text-embedding"}, SortRank: 95},
	{ID: 3, Name: "Anthropic", NameEn: "Anthropic", Logo: "claude", Aliases: []string{"anthropic", "claude"}, SortRank: 90},
	{ID: 4, Name: "Google", NameEn: "Google", Logo: "gemini", Aliases: []string{"google", "gemini", "veo", "imagen", "gemma"}, SortRank: 85},
	{ID: 5, Name: "DeepSeek", NameEn: "DeepSeek", Logo: "deepseek", Aliases: []string{"deepseek"}, SortRank: 80},
	{ID: 6, Name: "阿里百炼", NameEn: "Qwen", Logo: "qwen", Aliases: []string{"ali", "dashscope", "qwen", "wan", "通义", "阿里"}, SortRank: 75},
	{ID: 7, Name: "快手可灵", NameEn: "Kling", Logo: "kling", Aliases: []string{"kling", "可灵"}, SortRank: 70},
	{ID: 8, Name: "MiniMax", NameEn: "MiniMax", Logo: "minimax", Aliases: []string{"minimax", "hailuo", "abab"}, SortRank: 65},
	{ID: 9, Name: "Vidu", NameEn: "Vidu", Logo: "vidu", Aliases: []string{"vidu"}, SortRank: 60},
	{ID: 10, Name: "月之暗面", NameEn: "Moonshot", Logo: "moonshot", Aliases: []string{"moonshot", "kimi"}, SortRank: 55},
	{ID: 11, Name: "xAI", NameEn: "xAI", Logo: "grok", Aliases: []string{"xai", "grok"}, SortRank: 50},
	{ID: 99, Name: "其他厂商", NameEn: "Other", Logo: "default-model", Aliases: nil, SortRank: 1},
}

type typeProfile struct {
	ID              uint64
	Name            string
	NameEn          string
	Logo            string
	Aliases         []string
	SortRank        int
	DefaultFeatures string
}

var standardTypeProfiles = []typeProfile{
	{
		ID: 1, Name: "聊天", NameEn: "Chat", Logo: "", Aliases: []string{"chat", "llm", "text", "reasoning", "聊天", "对话"}, SortRank: 100,
		DefaultFeatures: `["理解图片","理解音频","理解视频","理解文件","输入-文字输入","输入-语音输入","输入-视频输入","输出-文字输出"]`,
	},
	{
		ID: 2, Name: "视频", NameEn: "Video", Logo: "", Aliases: []string{"video", "视频"}, SortRank: 90,
		DefaultFeatures: `["全能参考生视频","文生视频","图生视频","首尾帧生视频","编辑视频","延长视频","对口型"]`,
	},
	{
		ID: 3, Name: "图片", NameEn: "Image", Logo: "", Aliases: []string{"image", "img", "picture", "图片", "图像"}, SortRank: 80,
		DefaultFeatures: `["文生图","图文生图","多图文生图","蒙版","坐标框","放大图片"]`,
	},
	{
		ID: 4, Name: "音频", NameEn: "Audio", Logo: "", Aliases: []string{"audio", "speech", "tts", "stt", "music", "音频", "语音"}, SortRank: 70,
		DefaultFeatures: `["语音合成","音频生成"]`,
	},
	{
		ID: 5, Name: "向量", NameEn: "Embedding", Logo: "", Aliases: []string{"embed", "vector", "向量", "嵌入"}, SortRank: 60,
		DefaultFeatures: `["文本向量","多模态向量"]`,
	},
	{
		ID: 6, Name: "排序", NameEn: "Rerank", Logo: "", Aliases: []string{"rerank", "排序", "重排"}, SortRank: 50,
		DefaultFeatures: `["文本重排","多模态重排"]`,
	},
	{
		ID: 7, Name: "封装", NameEn: "Encapsulation", Logo: "", Aliases: []string{"encapsulation", "mediakit", "tool", "封装", "画质增强", "字幕擦除"}, SortRank: 40,
		DefaultFeatures: `["视频画质增强","视频字幕擦除","图像画质增强","图像背景移除"]`,
	},
}

// StandardProviders 返回标准厂商列表（供管理端筛选与模型编辑器使用）。
func StandardProviders() []ClassificationItem {
	out := make([]ClassificationItem, 0, len(standardProviderProfiles))
	for _, p := range standardProviderProfiles {
		isSys := int8(1)
		out = append(out, ClassificationItem{
			ID:        p.ID,
			Name:      p.Name,
			NameEn:    p.NameEn,
			Logo:      p.Logo,
			SortOrder: p.SortRank,
			IsActive:  1,
			IsSystem:  isSys,
		})
	}
	return out
}

// StandardModelTypes 返回标准模型模态分类列表（含 tkeapi 二级功能默认属性）。
func StandardModelTypes() []ClassificationItem {
	out := make([]ClassificationItem, 0, len(standardTypeProfiles))
	for _, t := range standardTypeProfiles {
		out = append(out, ClassificationItem{
			ID:              t.ID,
			Name:            t.Name,
			NameEn:          t.NameEn,
			Logo:            t.Logo,
			SortOrder:       t.SortRank,
			IsActive:        1,
			IsSystem:        1,
			DefaultFeatures: t.DefaultFeatures,
		})
	}
	return out
}

// ParseFeatureAttributes 解析模型或类型保存的 JSON 二级功能标签列表。
func ParseFeatureAttributes(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return []string{}
	}
	var list []string
	if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if t := strings.TrimSpace(item); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// InferModelProviderAndType 根据模型名称与上游线索推断官方服务商、模型类型与默认二级功能属性。
func InferModelProviderAndType(rawVendor, rawCategory, model string) (providerID uint64, providerName string, typeID uint64, typeName string, defaultFeatures string) {
	prov := resolveProvider(rawVendor, model)
	tp := resolveType(rawCategory, "", "", model)
	return prov.ID, prov.Name, tp.ID, tp.Name, tp.DefaultFeatures
}

func resolveProvider(rawHint, model string) providerProfile {
	combined := strings.ToLower(strings.TrimSpace(rawHint) + " " + strings.TrimSpace(model))
	for _, p := range standardProviderProfiles {
		for _, alias := range p.Aliases {
			if strings.Contains(combined, alias) {
				return p
			}
		}
	}
	return standardProviderProfiles[len(standardProviderProfiles)-1]
}

func resolveType(rawCategory, billingType, billingRule, model string) typeProfile {
	combined := strings.ToLower(strings.TrimSpace(rawCategory) + " " + strings.TrimSpace(billingRule) + " " + strings.TrimSpace(model))
	if strings.Contains(combined, "enhance-video") ||
		strings.Contains(combined, "erase-video-subtitle") ||
		strings.Contains(combined, "volc_image_enhance") ||
		strings.Contains(combined, "volc_image_remove_bg") ||
		strings.Contains(combined, "enhance-image") ||
		strings.Contains(combined, "remove-image-background") ||
		strings.Contains(combined, "封装") {
		return standardTypeProfiles[6]
	}
	if billingType == pricing.BillingTypeDuration ||
		strings.Contains(combined, "video") ||
		strings.Contains(combined, "seedance") ||
		strings.Contains(combined, "kling") ||
		strings.Contains(combined, "vidu") ||
		strings.Contains(combined, "sora") ||
		strings.Contains(combined, "veo") ||
		strings.Contains(combined, "hailuo") {
		return standardTypeProfiles[1]
	}
	if strings.Contains(combined, "image") ||
		strings.Contains(combined, "seedream") ||
		strings.Contains(combined, "dall-e") ||
		strings.Contains(combined, "gpt_billing") ||
		strings.Contains(combined, "per_image") ||
		strings.Contains(combined, "midjourney") ||
		strings.Contains(combined, "flux") ||
		strings.Contains(combined, "imagen") ||
		strings.Contains(combined, "hunyuan-image") ||
		strings.Contains(combined, "wanx") {
		return standardTypeProfiles[2]
	}
	if strings.Contains(combined, "audio") ||
		strings.Contains(combined, "speech") ||
		strings.Contains(combined, "tts") ||
		strings.Contains(combined, "whisper") ||
		strings.Contains(combined, "characters") ||
		strings.Contains(combined, "suno") {
		return standardTypeProfiles[3]
	}
	if strings.Contains(combined, "embed") || strings.Contains(combined, "bge-") {
		return standardTypeProfiles[4]
	}
	if strings.Contains(combined, "rerank") {
		return standardTypeProfiles[5]
	}
	return standardTypeProfiles[0]
}

type modelMetaRow struct {
	Model         string `gorm:"column:model;primaryKey"`
	Vendor        string `gorm:"column:vendor"`
	Category      string `gorm:"column:category"`
	Tags          string `gorm:"column:tags"`
	Description   string `gorm:"column:description"`
	ContextLength int    `gorm:"column:context_length"`
	Icon          string `gorm:"column:icon"`
}

func (modelMetaRow) TableName() string { return "model_meta" }

func billingDTOFromRule(rule *pricing.BillingRule) map[string]any {
	if rule == nil {
		return nil
	}
	return map[string]any{
		"billing_type":               rule.BillingType,
		"billing_rule":               rule.BillingRule,
		"pricing_type":               rule.PricingType,
		"prompt_rate":                rule.PromptRate,
		"completion_rate":            rule.CompletionRate,
		"cached_rate":                rule.CachedRate,
		"claude_cache_creation_rate": rule.ClaudeCacheCreationRate,
		"claude_cache_read_rate":     rule.ClaudeCacheReadRate,
		"fixed_rate":                 rule.FixedRate,
		"duration_rate":              rule.DurationRate,
		"pricing_tiers":              cmp.Or(strings.TrimSpace(rule.PricingTiers), "[]"),
		"extended_config":            cmp.Or(strings.TrimSpace(rule.ExtendedConfig), "{}"),
	}
}

func billingDTOFromPrice(p *relay.ModelPrice) map[string]any {
	if p == nil {
		return nil
	}
	if p.PerCallUSD.IsPositive() {
		return map[string]any{
			"billing_type":  pricing.BillingTypeRequests,
			"billing_rule":  "fixed",
			"pricing_type":  "fixed",
			"fixed_rate":    p.PerCallUSD,
			"pricing_tiers": "[]",
		}
	}
	if p.ImageUSD.IsPositive() {
		return map[string]any{
			"billing_type":  pricing.BillingTypeRequests,
			"billing_rule":  "per_image",
			"pricing_type":  "fixed",
			"fixed_rate":    p.ImageUSD,
			"pricing_tiers": "[]",
		}
	}
	if p.TTSPer1KChars.IsPositive() {
		return map[string]any{
			"billing_type":  pricing.BillingTypeRequests,
			"billing_rule":  "characters",
			"pricing_type":  "fixed",
			"fixed_rate":    p.TTSPer1KChars,
			"pricing_tiers": "[]",
		}
	}
	return map[string]any{
		"billing_type":    pricing.BillingTypeTokens,
		"billing_rule":    "standard",
		"pricing_type":    "standard",
		"prompt_rate":     p.InputUSDPer1M,
		"completion_rate": p.OutputUSDPer1M,
		"cached_rate":     p.CachedInputUSDPer1M,
		"pricing_tiers":   "[]",
	}
}

// BuildPublicMarketplace 实时聚合已上架模型、billing_rules / model_prices 与元数据，
// 生成符合前端 ModelMarketplace.tsx 规范且严格隐藏内部渠道隐私字段的公开清单。
func BuildPublicMarketplace(ctx context.Context, db *gorm.DB) (map[string]any, error) {
	if db == nil {
		return map[string]any{
			"enabled":         true,
			"models":          []MarketplaceModel{},
			"grouped_models":  []MarketplaceModel{},
			"providers":       []ClassificationItem{},
			"types":           []ClassificationItem{},
			"trending_config": defaultTrendingConfig(),
		}, nil
	}

	var bindings []pricing.ModelRuleBinding
	if err := db.WithContext(ctx).Order("sort_order DESC, model ASC").Find(&bindings).Error; err != nil && !IsMissingTable(err) {
		return nil, fmt.Errorf("load model_rule_bindings: %w", err)
	}

	var rules []pricing.BillingRule
	if err := db.WithContext(ctx).Where("is_active = 1").Find(&rules).Error; err != nil && !IsMissingTable(err) {
		return nil, fmt.Errorf("load billing_rules: %w", err)
	}
	ruleByID := make(map[uint64]pricing.BillingRule, len(rules))
	for _, r := range rules {
		ruleByID[r.ID] = r
	}

	var prices []relay.ModelPrice
	if err := db.WithContext(ctx).Order("model ASC").Find(&prices).Error; err != nil && !IsMissingTable(err) {
		return nil, fmt.Errorf("load model_prices: %w", err)
	}
	priceByModel := make(map[string]relay.ModelPrice, len(prices))
	for _, p := range prices {
		priceByModel[p.Model] = p
	}

	var metas []modelMetaRow
	if err := db.WithContext(ctx).Find(&metas).Error; err != nil && !IsMissingTable(err) {
		return nil, fmt.Errorf("load model_meta: %w", err)
	}
	metaByModel := make(map[string]modelMetaRow, len(metas))
	for _, m := range metas {
		metaByModel[m.Model] = m
	}

	// Only check whether at least one public channel serves a model; never leak channel name/URL/cost.
	var channels []relay.Channel
	if err := db.WithContext(ctx).
		Select("id, provider, models, status, owner_org_id").
		Where("status = 1 AND owner_org_id = 0").
		Find(&channels).Error; err != nil && !IsMissingTable(err) {
		return nil, fmt.Errorf("load channels: %w", err)
	}
	servedProviderHint := make(map[string]string)
	for _, ch := range channels {
		for _, m := range strings.Split(ch.Models, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			if _, exists := servedProviderHint[m]; !exists {
				servedProviderHint[m] = ch.Provider
			}
		}
	}

	bindingByModel := make(map[string]pricing.ModelRuleBinding, len(bindings))
	for _, b := range bindings {
		bindingByModel[b.Model] = b
	}

	seenModels := make(map[string]struct{}, len(bindings)+len(prices))
	orderedNames := make([]string, 0, len(bindings)+len(prices))
	for _, b := range bindings {
		if b.IsActive == 1 && b.IsListed == 1 {
			seenModels[b.Model] = struct{}{}
			orderedNames = append(orderedNames, b.Model)
		} else {
			seenModels[b.Model] = struct{}{}
		}
	}
	for _, p := range prices {
		if _, ok := seenModels[p.Model]; ok {
			continue
		}
		// Unbound legacy model_prices are visible only when backed by an active public channel.
		if _, served := servedProviderHint[p.Model]; served {
			seenModels[p.Model] = struct{}{}
			orderedNames = append(orderedNames, p.Model)
		}
	}

	outModels := make([]MarketplaceModel, 0, len(orderedNames))
	usedProviders := make(map[uint64]ClassificationItem)
	usedTypes := make(map[uint64]ClassificationItem)

	nowStr := time.Now().UTC().Format(time.RFC3339)
	for idx, modelName := range orderedNames {
		b, hasBinding := bindingByModel[modelName]
		meta := metaByModel[modelName]

		var billingObj map[string]any
		billingInvalid := false
		billingType, billingRuleName := "", ""
		if hasBinding && b.BillingRuleID > 0 {
			if rule, ok := ruleByID[b.BillingRuleID]; ok {
				billingObj = billingDTOFromRule(&rule)
				billingType = rule.BillingType
				billingRuleName = rule.BillingRule
			} else {
				billingInvalid = true
			}
		} else if p, hasPrice := priceByModel[modelName]; hasPrice {
			billingObj = billingDTOFromPrice(&p)
			billingType, _ = billingObj["billing_type"].(string)
			billingRuleName, _ = billingObj["billing_rule"].(string)
		} else {
			billingInvalid = true
		}

		provHint := cmp.Or(meta.Vendor, servedProviderHint[modelName])
		prov := resolveProvider(provHint, modelName)
		tp := resolveType(meta.Category, billingType, billingRuleName, modelName)

		usedProviders[prov.ID] = ClassificationItem{
			ID:        prov.ID,
			Name:      prov.Name,
			NameEn:    prov.NameEn,
			Logo:      prov.Logo,
			SortOrder: prov.SortRank,
			IsActive:  1,
		}
		usedTypes[tp.ID] = ClassificationItem{
			ID:        tp.ID,
			Name:      tp.Name,
			NameEn:    tp.NameEn,
			Logo:      tp.Logo,
			SortOrder: tp.SortRank,
			IsActive:  1,
		}

		displayName := modelName
		sortOrder := 0
		siteDisc := decimal.NewFromInt(1)
		siteDiscEn := int8(0)
		globalDisc := decimal.NewFromInt(1)
		globalDiscEn := int8(0)
		discSched := ""
		createdAt := nowStr
		if hasBinding {
			displayName = cmp.Or(strings.TrimSpace(b.DisplayName), modelName)
			sortOrder = b.SortOrder
			if b.SiteDiscount.IsPositive() {
				siteDisc = b.SiteDiscount
			}
			siteDiscEn = b.SiteDiscountEnabled
			if b.GlobalDiscount.IsPositive() {
				globalDisc = b.GlobalDiscount
			}
			globalDiscEn = b.GlobalDiscountEnabled
			discSched = b.DiscountSchedule
			if !b.CreatedAt.IsZero() {
				createdAt = b.CreatedAt.UTC().Format(time.RFC3339)
			}
		}

		logo := cmp.Or(strings.TrimSpace(meta.Icon), prov.Logo)
		outModels = append(outModels, MarketplaceModel{
			ID:                    uint64(idx + 1),
			Mid:                   "m-" + strconv.Itoa(idx+1),
			Name:                  displayName,
			ModelID:               modelName,
			OriginalID:            modelName,
			ProviderID:            prov.ID,
			ProviderName:          prov.Name,
			ProviderNameEn:        prov.NameEn,
			ProviderLogo:          prov.Logo,
			TypeID:                tp.ID,
			TypeName:              tp.Name,
			TypeNameEn:            tp.NameEn,
			TypeLogo:              tp.Logo,
			Logo:                  logo,
			SortOrder:             sortOrder,
			Description:           meta.Description,
			ModelDescription:      meta.Description,
			SiteDiscount:          siteDisc,
			SiteDiscountEnabled:   siteDiscEn,
			GlobalDiscount:        globalDisc,
			GlobalDiscountEnabled: globalDiscEn,
			DiscountSchedule:      discSched,
			Billing:               billingObj,
			BillingInvalid:        billingInvalid,
			CreatedAt:             createdAt,
		})
	}

	slices.SortFunc(outModels, func(a, b MarketplaceModel) int {
		if c := cmp.Compare(b.SortOrder, a.SortOrder); c != 0 {
			return c
		}
		return cmp.Compare(a.ModelID, b.ModelID)
	})

	providersList := make([]ClassificationItem, 0, len(usedProviders))
	for _, p := range usedProviders {
		providersList = append(providersList, p)
	}
	slices.SortFunc(providersList, func(a, b ClassificationItem) int {
		if c := cmp.Compare(b.SortOrder, a.SortOrder); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})

	typesList := make([]ClassificationItem, 0, len(usedTypes))
	for _, t := range usedTypes {
		typesList = append(typesList, t)
	}
	slices.SortFunc(typesList, func(a, b ClassificationItem) int {
		if c := cmp.Compare(b.SortOrder, a.SortOrder); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})

	grouped := make([]MarketplaceModel, 0, len(outModels))
	for _, m := range outModels {
		item := m
		item.VariantCount = 1
		item.Variants = []MarketplaceModel{m}
		grouped = append(grouped, item)
	}

	return map[string]any{
		"enabled":         true,
		"models":          outModels,
		"grouped_models":  grouped,
		"providers":       providersList,
		"types":           typesList,
		"total":           len(outModels),
		"group_total":     len(grouped),
		"trending_config": defaultTrendingConfig(),
		"icon_versions":   map[string]string{},
	}, nil
}

func defaultTrendingConfig() map[string]any {
	return map[string]any{
		"enabled": false,
		"hero_slides": []map[string]any{
			{
				"id":           "hero-seedance",
				"category":     "视频旗舰 · 火山原生直连",
				"title":        "Seedance 2.0 旗舰视频生成模型",
				"description":  "原生 /api/v3/contents/generations/tasks 接入，按分辨率×时长×有声×参考图实时透明计价，失败全额自动退还预扣。",
				"try_model_id": "",
				"docs_url":     "/docs/quickstart/doc0003",
			},
			{
				"id":           "hero-multimodal",
				"category":     "全栈多协议网关",
				"title":        "OpenAI · Claude · Gemini 统一密钥直连",
				"description":  "一套 Key 通配 /v1/chat/completions、/v1/messages 与 /v1beta/models，支持分时峰谷倍率与 Prompt Cache 计费。",
				"try_model_id": "",
				"docs_url":     "/docs/quickstart/doc0002",
			},
		},
		"quick_tags": []string{"seedance", "claude", "gpt", "gemini", "deepseek"},
		"sections":   []any{},
	}
}

// ParseRuleIDList 解析 model_rule_bindings.forward_rule_ids JSON 数组。
func ParseRuleIDList(raw string) []uint64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []uint64{}
	}
	var ids []uint64
	if err := json.Unmarshal([]byte(trimmed), &ids); err != nil || ids == nil {
		return []uint64{}
	}
	return ids
}
