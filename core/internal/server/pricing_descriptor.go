package server

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type pricingRate struct {
	Key    string `json:"key"`
	Amount string `json:"amount"`
	Unit   string `json:"unit"`
}
type pricingCombination struct {
	Key         string `json:"key"`
	Mode        string `json:"mode"`
	Resolution  string `json:"resolution"`
	InputVideo  bool   `json:"input_video"`
	ServiceTier string `json:"service_tier"`
	Draft       bool   `json:"draft"`
	Amount      string `json:"amount"`
}

// This DTO is safe for public responses. Do not add admin-only fields here.
type pricingDescriptor struct {
	BillingKind  string               `json:"billing_kind"`
	Currency     string               `json:"currency"`
	Unit         string               `json:"unit"`
	Status       string               `json:"status"`
	Rates        []pricingRate        `json:"rates"`
	Combinations []pricingCombination `json:"combinations"`
	Endpoints    []string             `json:"endpoints"`
}

func emptyPricing(kind string) pricingDescriptor {
	return pricingDescriptor{BillingKind: kind, Currency: "USD", Unit: "unknown", Status: "missing", Rates: []pricingRate{}, Combinations: []pricingCombination{}, Endpoints: []string{}}
}

func tokenGuideEndpoint(row catalogModel, ch relay.Channel) string {
	// Prices cannot distinguish chat from embeddings/rerank. Only explicit
	// operator metadata may select a guide, checked against transport support.
	// Metadata never participates in classification, routing or price selection.
	if row.Meta == nil {
		return ""
	}
	var mode relay.RelayMode
	var endpoint string
	switch row.Meta.Category {
	case "chat", "text":
		mode = relay.ModeChat
		endpoint = "/v1/chat/completions"
	case "embedding", "embeddings":
		mode = relay.ModeEmbeddings
		endpoint = "/v1/embeddings"
	case "rerank":
		mode = relay.ModeRerank
		endpoint = "/v1/rerank"
	default:
		return ""
	}
	if !relay.SupportsCatalogMode(ch, mode) {
		return ""
	}
	return endpoint
}
func genericDescriptor(row catalogModel, p relay.ModelPrice, priced bool, ch relay.Channel) pricingDescriptor {
	d := emptyPricing(row.PricingKind)
	add := func(k string, n decimal.Decimal, u string) { d.Rates = append(d.Rates, pricingRate{k, n.String(), u}) }
	if row.PricingKind == "video" {
		d.Unit = "USD/1M_video_tokens"
		d.Status = "blocked"
		d.Endpoints = []string{"/v1/video/generations"}
		return d
	}
	if !priced {
		return d
	}
	profile, knownProvider := relay.ProfileFor(ch.Provider)
	if ch.Provider == "midjourney" {
		d.Endpoints = []string{"/mj/submit/:action"}
		if p.PerCallUSD.IsPositive() {
			add("per_call_usd", p.PerCallUSD, "USD/call")
		}
	} else if knownProvider && profile.Adapter == "replicate" {
		d.Endpoints = []string{"/v1/images/generations"}
		if p.ImageUSD.IsPositive() {
			add("image_usd", p.ImageUSD, "USD/image")
		}
	} else if row.PricingKind == "generic_video" {
		d.Endpoints = []string{"/v1/video/generations"}
		if p.PerCallUSD.IsPositive() {
			add("per_call_usd", p.PerCallUSD, "USD/call")
		} else if p.OutputUSDPer1M.IsPositive() {
			add("output_usd_per_1m", p.OutputUSDPer1M, "USD/1M_tokens")
		}
	} else {
		if p.PerCallUSD.IsPositive() {
			add("per_call_usd", p.PerCallUSD, "USD/call")
		} else if p.InputUSDPer1M.IsPositive() || p.OutputUSDPer1M.IsPositive() || p.CachedInputUSDPer1M.IsPositive() {
			add("input_usd_per_1m", p.InputUSDPer1M, "USD/1M_tokens")
			add("output_usd_per_1m", p.OutputUSDPer1M, "USD/1M_tokens")
			if p.CachedInputUSDPer1M.IsPositive() {
				add("cached_input_usd_per_1m", p.CachedInputUSDPer1M, "USD/1M_tokens")
			}
			if endpoint := tokenGuideEndpoint(row, ch); endpoint != "" {
				d.Endpoints = append(d.Endpoints, endpoint)
			}
		}
		if p.ImageUSD.IsPositive() {
			add("image_usd", p.ImageUSD, "USD/image")
			d.Endpoints = append(d.Endpoints, "/v1/images/generations")
		}
		if p.TTSPer1KChars.IsPositive() {
			add("tts_usd_per_1k_chars", p.TTSPer1KChars, "USD/1K_characters")
			d.Endpoints = append(d.Endpoints, "/v1/audio/speech")
		}
		if p.TranscriptionPerCall.IsPositive() {
			add("transcription_usd_per_call", p.TranscriptionPerCall, "USD/call")
			d.Endpoints = append(d.Endpoints, "/v1/audio/transcriptions")
		}
		if p.AudioInputUSDPer1M.IsPositive() {
			add("audio_input_usd_per_1m", p.AudioInputUSDPer1M, "USD/1M_audio_tokens")
		}
		if p.AudioOutputUSDPer1M.IsPositive() {
			add("audio_output_usd_per_1m", p.AudioOutputUSDPer1M, "USD/1M_audio_tokens")
		}
		if p.AudioInputUSDPer1M.IsPositive() || p.AudioOutputUSDPer1M.IsPositive() {
			d.Endpoints = append(d.Endpoints, "/v1/realtime")
		}
		if p.PerCallUSD.IsPositive() {
			if endpoint := tokenGuideEndpoint(row, ch); endpoint != "" {
				d.Endpoints = append(d.Endpoints, endpoint)
			}
		}
	}
	if len(d.Rates) > 0 {
		d.Status = "published"
		d.Unit = d.Rates[0].Unit
		for _, r := range d.Rates {
			if r.Unit != d.Unit {
				d.Unit = "mixed"
				break
			}
		}
	}
	if ch.Status != 1 {
		d.Status = "disabled"
	}
	return d
}

type videoPricingProjection struct {
	Config     video.ModelConfig
	Capability ark.Capability
	Provider   string
	Pricing    pricingDescriptor
}

func loadVideoPricingProjection(tx *gorm.DB, configs []video.ModelConfig, channels []relay.Channel, now time.Time) (map[string]videoPricingProjection, error) {
	out := map[string]videoPricingProjection{}
	if len(configs) == 0 {
		return out, nil
	}
	var scopes []video.ProviderScope
	var rules []videobilling.PricingRule
	var sets []videobilling.MinimumRuleSet
	var cells []videobilling.MinimumRuleCell
	models := []string{}
	scopeIDs := []uint64{}
	for _, cfg := range configs {
		models = append(models, cfg.PublicModel)
		scopeIDs = append(scopeIDs, cfg.ProviderScopeID)
	}
	if e := tx.Where("id IN ?", scopeIDs).Find(&scopes).Error; e != nil {
		return nil, e
	}
	// Include every active version to detect overlaps, but never scan expired
	// historical tariffs or unrelated scopes for each public catalog request.
	if e := tx.Where("model IN ? AND provider_scope_id IN ? AND enabled = ? AND effective_from <= ? AND (effective_until IS NULL OR effective_until > ?)", models, scopeIDs, true, now, now).Find(&rules).Error; e != nil {
		return nil, e
	}
	versions := []string{}
	for _, p := range rules {
		if p.InputVideo && p.MinimumRuleVersion != "" {
			versions = append(versions, p.MinimumRuleVersion)
		}
	}
	if len(versions) > 0 {
		if e := tx.Where("model IN ? AND version IN ?", models, versions).Find(&sets).Error; e != nil {
			return nil, e
		}
		ids := []uint64{}
		for _, set := range sets {
			ids = append(ids, set.ID)
		}
		if len(ids) > 0 {
			if e := tx.Where("rule_set_id IN ?", ids).Find(&cells).Error; e != nil {
				return nil, e
			}
		}
	}
	for _, cfg := range configs {
		d := emptyPricing("video")
		d.Unit = "USD/1M_video_tokens"
		d.Status = "blocked"
		d.Endpoints = []string{"/v1/video/generations"}
		projection := videoPricingProjection{Config: cfg, Pricing: d}
		out[cfg.PublicModel] = projection
		if cfg.PublicModel != ark.Model20 && cfg.PublicModel != ark.Model25 {
			continue
		}
		if !cfg.Enabled {
			projection.Pricing.Status = "disabled"
			out[cfg.PublicModel] = projection
			continue
		}
		var cap ark.Capability
		var bounds map[string]video.Dimensions
		if cfg.Version == "" || cfg.VerifiedAt == nil || json.Unmarshal([]byte(cfg.CapabilityJSON), &cap) != nil || json.Unmarshal([]byte(cfg.BoundsJSON), &bounds) != nil || validateVideoCapability(cfg, cap, bounds) != nil {
			continue
		}
		projection.Capability = cap
		var scope *video.ProviderScope
		for i := range scopes {
			if scopes[i].ID == cfg.ProviderScopeID {
				scope = &scopes[i]
				break
			}
		}
		if scope == nil || !scope.Enabled || scope.Region != "cn-beijing" || scope.AccountRef == "" || scope.ProjectName == "" {
			continue
		}
		var channel *relay.Channel
		for i := range channels {
			if channels[i].ID == cfg.ChannelID {
				channel = &channels[i]
				break
			}
		}
		if channel == nil || channel.Status != 1 || channel.OwnerOrgID != 0 || (channel.Provider != "volcengine" && channel.Provider != "modelark") || channel.BaseURL != "https://ark.cn-beijing.volces.com/api/v3" {
			continue
		}
		projection.Provider = channel.Provider
		active := map[string][]videobilling.PricingRule{}
		for _, p := range rules {
			if p.Model == cfg.PublicModel && p.ProviderScopeID == cfg.ProviderScopeID && p.Enabled && !p.EffectiveFrom.After(now) && (p.EffectiveUntil == nil || p.EffectiveUntil.After(now)) {
				k := priceCellKey(cellFromRule(p))
				active[k] = append(active[k], p)
			}
		}
		for k, matching := range active {
			if len(matching) != 1 {
				continue
			}
			p := matching[0]
			c := cellFromRule(p)
			if p.Version != cfg.PricingVersion || !priceCellSupported(c, cap) {
				continue
			}
			minimum, e := videobilling.CatalogMinimumTokens(p, sets, cells, videobilling.MinimumContext{Ratios: cap.Ratios, MinDuration: cap.MinDuration, MaxDuration: cap.MaxDuration}, int64(cap.FPS))
			if e != nil {
				continue
			}
			p.MinimumTokens = minimum
			fx, e := decimal.NewFromString(scope.FXUSDPerCNY)
			if e != nil {
				continue
			}
			valid := true
			domain := 0
			for _, ratio := range cap.Ratios {
				if ratio == "adaptive" {
					continue
				}
				dim := bounds[c.Resolution+"/"+ratio]
				domain++
				meter := videobilling.MeteringInput{InputVideo: c.InputVideo, OutputDurationMS: int64(cap.MaxDuration) * 1000, Width: dim.Width, Height: dim.Height, FPSNumerator: int64(cap.FPS), FPSDenominator: 1, BoundsVerified: true, CapabilityVersion: cap.Version, EffectiveRatio: decimal.NewFromInt(1), FXUSDPerCNY: fx, FXVersion: scope.FXVersion, FXObservedAt: scope.FXObservedAt}
				if c.InputVideo {
					meter.InputDurationMS = cfg.MaxInputVideoDurationMS
				}
				if _, e := videobilling.Quote(p, meter); e != nil {
					valid = false
					break
				}
			}
			if !valid || domain == 0 {
				continue
			}
			projection.Pricing.Combinations = append(projection.Pricing.Combinations, pricingCombination{k, p.Mode, p.Resolution, p.InputVideo, p.ServiceTier, p.Draft, p.RetailUSDPer1M.String()})
		}
		sort.Slice(projection.Pricing.Combinations, func(i, j int) bool {
			return projection.Pricing.Combinations[i].Key < projection.Pricing.Combinations[j].Key
		})
		if len(projection.Pricing.Combinations) > 0 {
			projection.Pricing.Status = "published"
			expected := 0
			for _, c := range priceMatrix(cap, nil) {
				if c.Available {
					expected++
				}
			}
			if len(projection.Pricing.Combinations) < expected {
				projection.Pricing.Status = "partial"
			}
		} else if len(active) == 0 {
			projection.Pricing.Status = "missing"
		}
		out[cfg.PublicModel] = projection
	}
	return out, nil
}
