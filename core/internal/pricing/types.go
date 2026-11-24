// Package pricing 实现数据驱动的多模态计费规则引擎（对标 tkeapi billing_rules + 复式账本 decimal 精度）。
// 全部单价与金额计算严格使用 shopspring/decimal，禁止 float64 资金运算。
package pricing

import (
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	BillingTypeTokens   = "tokens"
	BillingTypeRequests = "requests"
	BillingTypeDuration = "duration"
)

var (
	oneMillion   = decimal.NewFromInt(1_000_000)
	oneThousand  = decimal.NewFromInt(1_000)
	tenThousand  = decimal.NewFromInt(10_000)
	oneHundred   = decimal.NewFromInt(100)
	pointOne     = decimal.RequireFromString("0.1")
	pointFive    = decimal.RequireFromString("0.5")
	onePointFive = decimal.RequireFromString("1.5")
	twoDec       = decimal.NewFromInt(2)
)

// BillingRule 对应数据库表 billing_rules。
type BillingRule struct {
	ID                      uint64          `gorm:"primaryKey;column:id" json:"id"`
	PID                     string          `gorm:"column:pid" json:"pid"`
	Name                    string          `gorm:"column:name" json:"name"`
	BillingType             string          `gorm:"column:billing_type" json:"billing_type"`
	BillingRule             string          `gorm:"column:billing_rule" json:"billing_rule"`
	PricingType             string          `gorm:"column:pricing_type" json:"pricing_type"`
	PromptRate              decimal.Decimal `gorm:"column:prompt_rate" json:"prompt_rate"`
	CompletionRate          decimal.Decimal `gorm:"column:completion_rate" json:"completion_rate"`
	CachedRate              decimal.Decimal `gorm:"column:cached_rate" json:"cached_rate"`
	ClaudeCacheCreationRate decimal.Decimal `gorm:"column:claude_cache_creation_rate" json:"claude_cache_creation_rate"`
	ClaudeCacheReadRate     decimal.Decimal `gorm:"column:claude_cache_read_rate" json:"claude_cache_read_rate"`
	FixedRate               decimal.Decimal `gorm:"column:fixed_rate" json:"fixed_rate"`
	DurationRate            decimal.Decimal `gorm:"column:duration_rate" json:"duration_rate"`
	PricingTiers            string          `gorm:"column:pricing_tiers" json:"pricing_tiers"`
	ExtendedConfig          string          `gorm:"column:extended_config" json:"extended_config"`
	IsActive                int8            `gorm:"column:is_active" json:"is_active"`
	IsSystem                int8            `gorm:"column:is_system" json:"is_system"`
	SortOrder               int             `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt               time.Time       `gorm:"column:created_at" json:"created_at"`
	UpdatedAt               time.Time       `gorm:"column:updated_at" json:"updated_at"`

	// AppliedMultiplier 请求开始时锁定的峰谷时段倍率（不落库）
	AppliedMultiplier decimal.Decimal `gorm:"-" json:"applied_multiplier"`
}

func (BillingRule) TableName() string { return "billing_rules" }

// ModelRuleBinding 对应数据库表 model_rule_bindings。
type ModelRuleBinding struct {
	Model                 string          `gorm:"primaryKey;column:model" json:"model"`
	DisplayName           string          `gorm:"column:display_name" json:"display_name"`
	ModelAlias            string          `gorm:"column:model_alias" json:"model_alias"`
	BillingRuleID         uint64          `gorm:"column:billing_rule_id" json:"billing_rule_id"`
	ForwardRuleIDs        string          `gorm:"column:forward_rule_ids" json:"forward_rule_ids"`
	PreDeduction          decimal.Decimal `gorm:"column:pre_deduction" json:"pre_deduction"`
	EnableLogContent      int8            `gorm:"column:enable_log_content" json:"enable_log_content"`
	SiteDiscount          decimal.Decimal `gorm:"column:site_discount" json:"site_discount"`
	SiteDiscountEnabled   int8            `gorm:"column:site_discount_enabled" json:"site_discount_enabled"`
	GlobalDiscount        decimal.Decimal `gorm:"column:global_discount" json:"global_discount"`
	GlobalDiscountEnabled int8            `gorm:"column:global_discount_enabled" json:"global_discount_enabled"`
	DiscountSchedule      string          `gorm:"column:discount_schedule" json:"discount_schedule"`
	IsListed              int8            `gorm:"column:is_listed" json:"is_listed"`
	IsActive              int8            `gorm:"column:is_active" json:"is_active"`
	SortOrder             int             `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt             time.Time       `gorm:"column:created_at" json:"created_at"`
	UpdatedAt             time.Time       `gorm:"column:updated_at" json:"updated_at"`
}

func (ModelRuleBinding) TableName() string { return "model_rule_bindings" }

// UsageTokens 上游响应与请求特征抽取后的统一 Token 计量结构。
type UsageTokens struct {
	Prompt            int64 `json:"prompt"`
	Completion        int64 `json:"completion"`
	Total             int64 `json:"total"`
	HasTotalTokens    bool  `json:"has_total_tokens"`
	Cached            int64 `json:"cached"`
	CacheWrite        int64 `json:"cache_write"`
	CacheCreation     int64 `json:"cache_creation"`
	CacheCreation1h   int64 `json:"cache_creation_1h"`
	AudioTokens       int64 `json:"audio_tokens"`
	AudioCachedTokens int64 `json:"audio_cached_tokens"`
	ImageTokens       int64 `json:"image_tokens"`
	OutputImageTokens int64 `json:"output_image_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`
	WebSearch         int64 `json:"web_search"`
}

// ExtractedFeatures 多模态/视频/缓存请求特征快照。
type ExtractedFeatures struct {
	HasVideo           bool             `json:"has_video"`
	HasAudio           bool             `json:"has_audio"`
	HasImageRef        bool             `json:"has_image_ref"`
	PromptExtend       bool             `json:"prompt_extend"`
	LayerDecomposition bool             `json:"layer_decomposition"`
	DurationSeconds    *decimal.Decimal `json:"duration_seconds,omitempty"`
	VideoRefSeconds    *decimal.Decimal `json:"video_ref_seconds,omitempty"`
	AudioRefSeconds    *decimal.Decimal `json:"audio_ref_seconds,omitempty"`
	Resolution         string           `json:"resolution,omitempty"`
	Size               string           `json:"size,omitempty"`
	Quality            string           `json:"quality,omitempty"`
	Sound              string           `json:"sound,omitempty"`
	ServiceTier        string           `json:"service_tier,omitempty"`
	Version            string           `json:"version,omitempty"`
	CacheTTL           string           `json:"cache_ttl,omitempty"`
	ImageCount         *int64           `json:"image_count,omitempty"`
	ImageRefCount      *int64           `json:"image_ref_count,omitempty"`
	TextCharacters     *int64           `json:"text_characters,omitempty"`
	FPS                *decimal.Decimal `json:"fps,omitempty"`
	WebSearch          *int64           `json:"web_search,omitempty"`
	TimeMultiplier     *decimal.Decimal `json:"time_multiplier,omitempty"`
}

// TotalVideoSeconds 返回视频计费总时长（无值时回退 5s，避免缺时长导致 0 计费）。
func (f ExtractedFeatures) TotalVideoSeconds() decimal.Decimal {
	if f.DurationSeconds != nil && f.DurationSeconds.IsPositive() {
		return *f.DurationSeconds
	}
	return decimal.NewFromInt(5)
}

// PricingTier Token 阶梯档位（支持 tiered / doubao_chat）。
type PricingTier struct {
	MaxPromptTokens     decimal.Decimal  `json:"max_prompt_tokens"`
	MaxCompletionTokens *decimal.Decimal `json:"max_completion_tokens,omitempty"`
	PromptRate          decimal.Decimal  `json:"prompt_rate"`
	CompletionRate      decimal.Decimal  `json:"completion_rate"`
	CachedRate          decimal.Decimal  `json:"cached_rate"`
	CacheWriteRate      decimal.Decimal  `json:"cache_write_rate"`
	AudioPromptRate     decimal.Decimal  `json:"audio_prompt_rate"`
	AudioCachedRate     decimal.Decimal  `json:"audio_cached_rate"`
	FastPromptRate      decimal.Decimal  `json:"fast_prompt_rate"`
	FastCompletionRate  decimal.Decimal  `json:"fast_completion_rate"`
	FastCachedRate      decimal.Decimal  `json:"fast_cached_rate"`
	FastAudioPromptRate decimal.Decimal  `json:"fast_audio_prompt_rate"`
	FastAudioCachedRate decimal.Decimal  `json:"fast_audio_cached_rate"`
}

// ResolutionTier 分辨率阶梯（image_resolution / image_resolution_io / video_resolution / video_seconds_io / video_seconds_ref）。
type ResolutionTier struct {
	Resolution         string           `json:"resolution"`
	Rate               decimal.Decimal  `json:"rate"`
	Enabled            *bool            `json:"enabled,omitempty"`
	ImageRefMultiplier *decimal.Decimal `json:"image_ref_multiplier,omitempty"`
	InputRate          decimal.Decimal  `json:"input_rate"`
	RateNoRef          *decimal.Decimal `json:"rate_no_ref,omitempty"`
	RateRef            *decimal.Decimal `json:"rate_ref,omitempty"`
	InputRateRef       *decimal.Decimal `json:"input_rate_ref,omitempty"`
}

func (t ResolutionTier) IsEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

// SizeTier 像素尺寸阶梯（image_size_pixel）。
type SizeTier struct {
	Size               string           `json:"size"`
	Rate               decimal.Decimal  `json:"rate"`
	Enabled            *bool            `json:"enabled,omitempty"`
	ImageRefMultiplier *decimal.Decimal `json:"image_ref_multiplier,omitempty"`
	QualityPricing     bool             `json:"quality_pricing"`
	RateLow            *decimal.Decimal `json:"rate_low,omitempty"`
	RateMedium         *decimal.Decimal `json:"rate_medium,omitempty"`
	RateHigh           *decimal.Decimal `json:"rate_high,omitempty"`
}

func (t SizeTier) IsEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

// VideoQualityTier 视频画质与帧率阶梯（video_quality）。
type VideoQualityTier struct {
	Resolution string          `json:"resolution"`
	FPSRange   string          `json:"fps_range"`
	Rate       decimal.Decimal `json:"rate"`
	Enabled    *bool           `json:"enabled,omitempty"`
}

func (t VideoQualityTier) IsEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

// VolcSeedreamTier 火山 Seedream Pro 输出像素万档阶梯。
type VolcSeedreamTier struct {
	MaxPixelsWan decimal.Decimal `json:"max_pixels_wan"`
	Rate         decimal.Decimal `json:"rate"`
	LayerRate    decimal.Decimal `json:"layer_rate"`
	Enabled      *bool           `json:"enabled,omitempty"`
}

func (t VolcSeedreamTier) IsEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

// RoundMoney 站点金额精度约定：非负并四舍五入至小数点后 6 位。
func RoundMoney(d decimal.Decimal) decimal.Decimal {
	if d.IsNegative() {
		return decimal.Zero
	}
	return d.Round(6)
}

// FormatCleanMoney 精简金额格式化（最多 6 位小数，去末尾 0）。
func FormatCleanMoney(d decimal.Decimal) string {
	s := d.Round(6).StringFixed(6)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func fmtDiscount(d decimal.Decimal) string {
	return d.StringFixed(2)
}

func fmtSec(d decimal.Decimal) string {
	return d.StringFixed(2)
}

func fmtRate(d decimal.Decimal) string {
	return FormatCleanMoney(d)
}

func int64Str(v int64) string {
	return strconv.FormatInt(v, 10)
}
