package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
)

type catalogGenericPrice struct {
	Model                   string `json:"model"`
	InputUSDPer1M           string `json:"input_usd_per_1m"`
	OutputUSDPer1M          string `json:"output_usd_per_1m"`
	CachedInputUSDPer1M     string `json:"cached_input_usd_per_1m"`
	ImageUSD                string `json:"image_usd"`
	PerCallUSD              string `json:"per_call_usd"`
	TTSUSDPer1KChars        string `json:"tts_usd_per_1k_chars"`
	TranscriptionUSDPerCall string `json:"transcription_usd_per_call"`
	AudioInputUSDPer1M      string `json:"audio_input_usd_per_1m"`
	AudioOutputUSDPer1M     string `json:"audio_output_usd_per_1m"`
}

func genericPriceProjection(p relay.ModelPrice) *catalogGenericPrice {
	return &catalogGenericPrice{p.Model, p.InputUSDPer1M.String(), p.OutputUSDPer1M.String(), p.CachedInputUSDPer1M.String(), p.ImageUSD.String(), p.PerCallUSD.String(), p.TTSPer1KChars.String(), p.TranscriptionPerCall.String(), p.AudioInputUSDPer1M.String(), p.AudioOutputUSDPer1M.String()}
}

type catalogVideoConfiguration struct {
	ChannelID         uint64 `json:"channel_id"`
	ProviderScopeID   uint64 `json:"provider_scope_id"`
	ConfigVersion     string `json:"config_version"`
	PricingVersion    string `json:"pricing_version"`
	CapabilityVersion string `json:"capability_version"`
	Enabled           bool   `json:"enabled"`
}

func videoConfigProjection(cfg video.ModelConfig, cap ark.Capability) *catalogVideoConfiguration {
	return &catalogVideoConfiguration{cfg.ChannelID, cfg.ProviderScopeID, cfg.Version, cfg.PricingVersion, cap.Version, cfg.Enabled}
}

type catalogProvider struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}
type catalogChannel struct {
	ID       uint64 `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Status   int8   `json:"status"`
}
type catalogModel struct {
	Key                string                     `json:"key"`
	Provider           string                     `json:"provider"`
	ChannelID          uint64                     `json:"channel_id"`
	Model              string                     `json:"model"`
	UpstreamModel      string                     `json:"upstream_model"`
	PricingKind        string                     `json:"pricing_kind"`
	RecognitionSource  string                     `json:"recognition_source"`
	BillingModes       []string                   `json:"billing_modes"`
	GenericPrice       *catalogGenericPrice       `json:"generic_price"`
	Meta               *ModelMeta                 `json:"meta"`
	VideoModel         *string                    `json:"video_model"`
	VideoConfiguration *catalogVideoConfiguration `json:"video_configuration"`
	UnsupportedReason  string                     `json:"unsupported_reason"`
}

// A known family is an explicit identifier grammar, not a modality keyword guess.
// It prevents legacy Seedance variants from entering a generic text price form.
var seedanceStandardID = regexp.MustCompile(`^(?:doubao-)?seedance-(2[.-][05])(?:-[0-9]{6})?$`)
var seedanceVariantID = regexp.MustCompile(`^(?:doubao-)?seedance-2[.-][05]-(?:fast|mini)(?:-[0-9]{6})?$`)
var seedanceLegacyID = regexp.MustCompile(`^(?:doubao-)?seedance-1[.-][05](?:-(?:pro(?:-fast)?|lite|fast))?(?:-[0-9]{6})?$`)

func seedanceIdentity(model string) (canonical, reason string) {
	if seedanceLegacyID.MatchString(model) {
		return "", "seedance_1x_excluded"
	}
	if seedanceVariantID.MatchString(model) {
		return "", "seedance_variant_not_supported"
	}
	if m := seedanceStandardID.FindStringSubmatch(model); m != nil {
		return "seedance-" + strings.ReplaceAll(m[1], "-", "."), ""
	}
	return "", ""
}

// This is a read projection of existing model-global prices, never a default
// tariff or a new channel-specific price. Metadata remains display-only.
func (s *Server) adminPricingCatalog(c *gin.Context) {
	db := s.db.WithContext(c.Request.Context())
	videoDetails := false
	if value, ok := c.Get("actor_id"); ok {
		if id, ok := value.(uint64); ok && id != 0 {
			var actor identity.User
			e := db.Select("id,role,status").Where("id = ?", id).Take(&actor).Error
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				fail(c, 503, "pricing catalog unavailable")
				return
			}
			videoDetails = e == nil && actor.Status == identity.StatusActive && actor.Role == identity.PlatRoleSuperadmin
		}
	}
	channels := []relay.Channel{}
	prices := []relay.ModelPrice{}
	metas := []ModelMeta{}
	configs := []video.ModelConfig{}
	// Explicit projection avoids even reading encrypted channel credentials,
	// request overrides and other unrelated channel settings into this endpoint.
	if e := db.Select("id,name,provider,base_url,models,model_mapping,status,owner_org_id").Order("id").Find(&channels).Error; e != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}
	if e := db.Find(&prices).Error; e != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}
	if e := db.Find(&metas).Error; e != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}
	if e := db.Select("public_model,channel_id,provider_scope_id,version,pricing_version,capability_json,enabled").Order("public_model").Find(&configs).Error; e != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}
	priceMap := map[string]relay.ModelPrice{}
	for _, p := range prices {
		priceMap[p.Model] = p
	}
	metaMap := map[string]ModelMeta{}
	for _, m := range metas {
		metaMap[m.Model] = m
	}
	providers := []catalogProvider{}
	seenProviders := map[string]bool{}
	// Use the same registry as channel setup, including providers that do not
	// have a channel yet. Only actual channels below contribute model rows.
	for _, p := range relay.ListProviders() {
		providers = append(providers, catalogProvider{p.Name, p.DisplayName})
		seenProviders[p.Name] = true
	}
	channelRows := []catalogChannel{}
	models := []catalogModel{}
	for _, ch := range channels {
		if !seenProviders[ch.Provider] {
			// Existing API-created custom providers remain selectable without
			// rewriting their identity or adding them to channel setup's registry.
			providers = append(providers, catalogProvider{ch.Provider, ch.Provider})
			seenProviders[ch.Provider] = true
		}
		channelRows = append(channelRows, catalogChannel{ch.ID, ch.Name, ch.Provider, ch.Status})
		seen := map[string]bool{}
		for _, name := range strings.Split(ch.Models, ",") {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			row := catalogModel{Key: fmt.Sprintf("%d:%s", ch.ID, name), Provider: ch.Provider, ChannelID: ch.ID, Model: name, UpstreamModel: ch.UpstreamModel(name), PricingKind: "unknown", RecognitionSource: "unknown", BillingModes: []string{}, UnsupportedReason: "pricing_kind_unknown"}
			p, priced := priceMap[name]
			if priced {
				row.GenericPrice = genericPriceProjection(p)
			}
			if m, ok := metaMap[name]; ok {
				copy := m
				row.Meta = &copy
			}
			classifyCatalogModel(&row, ch, p, priced, configs, videoDetails)
			models = append(models, row)
		}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	sort.Slice(models, func(i, j int) bool {
		if models[i].ChannelID != models[j].ChannelID {
			return models[i].ChannelID < models[j].ChannelID
		}
		return models[i].Model < models[j].Model
	})
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"providers": providers, "channels": channelRows, "models": models, "permissions": gin.H{"video_details": videoDetails}, "price_scope": "public_model_global"})
}

func classifyCatalogModel(row *catalogModel, ch relay.Channel, p relay.ModelPrice, priced bool, configs []video.ModelConfig, details bool) {
	canonical, reason := seedanceIdentity(row.UpstreamModel)
	publicCanonical, publicReason := seedanceIdentity(row.Model)
	if publicReason != "" {
		reason = publicReason
	}
	if canonical == "" && reason == "" {
		canonical, reason = publicCanonical, publicReason
	}
	if reason != "" {
		row.PricingKind = "video"
		row.RecognitionSource = "provider_contract"
		row.UnsupportedReason = reason
		return
	}
	if publicCanonical != "" && canonical != "" && publicCanonical != canonical {
		row.PricingKind = "video"
		row.RecognitionSource = "provider_contract"
		row.UnsupportedReason = "official_video_configuration_ambiguous"
		return
	}
	var matches []video.ModelConfig
	var caps []ark.Capability
	matchingOtherChannel := false
	for _, cfg := range configs {
		if cfg.PublicModel != ark.Model20 && cfg.PublicModel != ark.Model25 {
			continue
		}
		var cap ark.Capability
		if json.Unmarshal([]byte(cfg.CapabilityJSON), &cap) != nil || cap.PublicModel != cfg.PublicModel {
			continue
		}
		upstreamCanonical, upstreamReason := seedanceIdentity(cap.UpstreamModel)
		if upstreamCanonical != cfg.PublicModel || upstreamReason != "" {
			continue
		}
		matchesName := row.UpstreamModel == cap.UpstreamModel || row.Model == cfg.PublicModel && row.UpstreamModel == row.Model
		if !matchesName {
			continue
		}
		if cfg.ChannelID != ch.ID || ch.OwnerOrgID != 0 || (ch.Provider != "volcengine" && ch.Provider != "modelark") || ch.BaseURL != "https://ark.cn-beijing.volces.com/api/v3" {
			matchingOtherChannel = true
			continue
		}
		matches = append(matches, cfg)
		caps = append(caps, cap)
	}
	if len(matches) > 0 {
		row.PricingKind = "video"
		row.RecognitionSource = "video_config"
		row.BillingModes = []string{"official_video_completion_tokens"}
		row.UnsupportedReason = ""
		if len(matches) != 1 {
			row.UnsupportedReason = "official_video_configuration_ambiguous"
			return
		}
		name := matches[0].PublicModel
		row.VideoModel = &name
		if details {
			row.VideoConfiguration = videoConfigProjection(matches[0], caps[0])
		}
		return
	}
	if canonical != "" || matchingOtherChannel {
		row.PricingKind = "video"
		row.RecognitionSource = "provider_contract"
		row.UnsupportedReason = "official_video_configuration_missing"
		if matchingOtherChannel {
			row.UnsupportedReason = "official_video_channel_mismatch"
		}
		// Keep the supported standard series discoverable, but only the bound
		// configuration may be edited; a different channel cannot inherit it.
		if canonical != "" {
			row.VideoModel = &canonical
		}
		return
	}
	profile, known := relay.ProfileFor(ch.Provider)
	if known && profile.TaskOnly {
		row.RecognitionSource = "provider_contract"
		row.UnsupportedReason = ""
		row.PricingKind = "generic_video"
		row.BillingModes = []string{"task_output_units"}
		if ch.Provider == "midjourney" {
			row.PricingKind = "per_call"
			row.BillingModes = []string{"per_call"}
		} else if priced && p.PerCallUSD.IsPositive() {
			row.BillingModes = []string{"per_call"}
		}
		return
	}
	if known && profile.Adapter == "replicate" {
		row.PricingKind = "image"
		row.RecognitionSource = "provider_contract"
		row.UnsupportedReason = ""
		row.BillingModes = []string{"image"}
		return
	}
	if !priced {
		return
	}
	row.RecognitionSource = "existing_price"
	row.UnsupportedReason = ""
	families := map[string]bool{}
	if p.ImageUSD.IsPositive() {
		row.BillingModes = append(row.BillingModes, "image")
		families["image"] = true
	}
	if p.PerCallUSD.IsPositive() {
		row.BillingModes = append(row.BillingModes, "per_call")
		families["per_call"] = true
	} else if p.InputUSDPer1M.IsPositive() || p.OutputUSDPer1M.IsPositive() || p.CachedInputUSDPer1M.IsPositive() {
		row.BillingModes = append(row.BillingModes, "token")
		families["token"] = true
	}
	if p.TTSPer1KChars.IsPositive() {
		row.BillingModes = append(row.BillingModes, "tts_characters")
		families["audio"] = true
	}
	if p.TranscriptionPerCall.IsPositive() {
		row.BillingModes = append(row.BillingModes, "transcription_per_call")
		families["audio"] = true
	}
	if p.AudioInputUSDPer1M.IsPositive() || p.AudioOutputUSDPer1M.IsPositive() {
		row.BillingModes = append(row.BillingModes, "audio_token")
		families["audio"] = true
	}
	if len(families) == 0 {
		row.PricingKind = "unknown"
		row.UnsupportedReason = "pricing_kind_unknown"
		return
	}
	row.PricingKind = "mixed"
	if len(families) == 1 {
		for kind := range families {
			row.PricingKind = kind
		}
	}
}
