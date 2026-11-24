// Package forward 实现数据驱动的协议转发规则解析、入口评分匹配与模板缓存（对标 tkeapi relay/forward）。
package forward

import (
	"encoding/json"
	"time"
)

// ChannelCategory 对应数据库表 channel_categories。
type ChannelCategory struct {
	ID          uint64    `gorm:"primaryKey;column:id" json:"id"`
	Name        string    `gorm:"column:name" json:"name"`
	Description string    `gorm:"column:description" json:"description"`
	SortOrder   int       `gorm:"column:sort_order" json:"sort_order"`
	IsActive    int8      `gorm:"column:is_active" json:"is_active"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (ChannelCategory) TableName() string { return "channel_categories" }

// ForwardRule 对应数据库表 forward_rules。
type ForwardRule struct {
	ID          uint64    `gorm:"primaryKey;column:id" json:"id"`
	EID         string    `gorm:"column:eid" json:"eid"`
	Name        string    `gorm:"column:name" json:"name"`
	RuleType    string    `gorm:"column:rule_type" json:"rule_type"`
	Category    string    `gorm:"column:category" json:"category"`
	ConfigJSON  string    `gorm:"column:config_json" json:"config_json"`
	Description string    `gorm:"column:description" json:"description"`
	Remark      string    `gorm:"column:remark" json:"remark"`
	IsActive    int8      `gorm:"column:is_active" json:"is_active"`
	IsSystem    int8      `gorm:"column:is_system" json:"is_system"`
	SortOrder   int       `gorm:"column:sort_order" json:"sort_order"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (ForwardRule) TableName() string { return "forward_rules" }

// PathRewritePair 描述一条入口→上游路径重写规则。
type PathRewritePair struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// ForwardConfig 对应 forward_rules.config_json 结构。
type ForwardConfig struct {
	TargetType             string            `json:"target_type"`
	AuthType               string            `json:"auth_type"`
	PathRewrite            *PathRewritePair  `json:"path_rewrite,omitempty"`
	PathRewrites           []PathRewritePair `json:"path_rewrites,omitempty"`
	PathPassthrough        []string          `json:"path_passthrough,omitempty"`
	PathAccept             []string          `json:"path_accept,omitempty"`
	PollPath               string            `json:"poll_path,omitempty"`
	AssetConvert           bool              `json:"asset_convert,omitempty"`
	AssetConvertNS         string            `json:"asset_convert_ns,omitempty"`
	UpstreamAssetConvert   bool              `json:"upstream_asset_convert,omitempty"`
	UpstreamAssetBindingID int64             `json:"upstream_asset_binding_id,omitempty"`
	Moderation             bool              `json:"moderation,omitempty"`
	ContentToPrompt        bool              `json:"content_to_prompt,omitempty"`
	FastResponse           bool              `json:"fast_response,omitempty"`
	AllowedResolutions     []string          `json:"allowed_resolutions,omitempty"`
	AllowedQualities       []string          `json:"allowed_qualities,omitempty"`
	MaxRefImages           *int              `json:"max_ref_images,omitempty"`
	MinPixels              *float64          `json:"min_pixels,omitempty"`
	MaxPixels              *float64          `json:"max_pixels,omitempty"`
	AutoDowngrade          bool              `json:"auto_downgrade,omitempty"`
	Step                   *int              `json:"step,omitempty"`
	MaxAspectRatio         *int              `json:"max_aspect_ratio,omitempty"`
}

// ResolvedForward 经过请求路径匹配与模板解析后的转发指令。
type ResolvedForward struct {
	RuleID                 uint64                     `json:"rule_id"`
	EID                    string                     `json:"eid"`
	RuleName               string                     `json:"rule_name"`
	Model                  string                     `json:"model"`
	TargetType             string                     `json:"target_type"`
	UpstreamPath           string                     `json:"upstream_path"`
	AuthType               string                     `json:"auth_type"`
	PollPath               string                     `json:"poll_path,omitempty"`
	IsPassthrough          bool                       `json:"is_passthrough"`
	AssetConvert           bool                       `json:"asset_convert"`
	AssetConvertNS         string                     `json:"asset_convert_ns"`
	UpstreamAssetConvert   bool                       `json:"upstream_asset_convert"`
	UpstreamAssetBindingID int64                      `json:"upstream_asset_binding_id,omitempty"`
	AssetModeration        bool                       `json:"asset_moderation"`
	ContentToPrompt        bool                       `json:"content_to_prompt"`
	FastResponse           bool                       `json:"fast_response"`
	AllowedResolutions     []string                   `json:"allowed_resolutions,omitempty"`
	AllowedQualities       []string                   `json:"allowed_qualities,omitempty"`
	MaxRefImages           *int                       `json:"max_ref_images,omitempty"`
	MinPixels              *float64                   `json:"min_pixels,omitempty"`
	MaxPixels              *float64                   `json:"max_pixels,omitempty"`
	AutoDowngrade          bool                       `json:"auto_downgrade,omitempty"`
	Step                   *int                       `json:"step,omitempty"`
	MaxAspectRatio         *int                       `json:"max_aspect_ratio,omitempty"`
	Raw                    map[string]json.RawMessage `json:"-"`
}
