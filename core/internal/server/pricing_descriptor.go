package server

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
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
	if tx == nil || tx.Statement == nil {
		return nil, video.ErrCatalogInvalid
	}
	ctx := tx.Statement.Context
	type preparedConfig struct {
		config      video.ModelConfig
		publication video.PublicationCapability
	}
	prepared := []preparedConfig{}
	refs := []videobilling.CatalogTariffRef{}
	scopeIDs := []uint64{}
	seenScope := map[uint64]bool{}
	for _, cfg := range configs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := emptyPricing("video")
		d.Unit, d.Status, d.Endpoints = "USD/1M_video_tokens", "blocked", []string{"/v1/video/generations"}
		projection := videoPricingProjection{Config: cfg, Pricing: d}
		if !cfg.Enabled {
			projection.Pricing.Status = "disabled"
		}
		out[cfg.PublicModel] = projection
		if !video.IsOfficialPublicModel(cfg.PublicModel) || !cfg.Enabled || cfg.VerifiedAt == nil || cfg.ProviderScopeID == 0 || cfg.PricingVersion == "" || len(cfg.PricingVersion) > 64 {
			continue
		}
		publication, err := video.ParsePublicationCapability(cfg)
		if errors.Is(err, video.ErrCatalogLimit) {
			return nil, err
		}
		if err != nil {
			continue
		}
		var cap ark.Capability
		if json.Unmarshal([]byte(cfg.CapabilityJSON), &cap) != nil || !cap.Enabled || !cap.Verified {
			continue
		}
		projection.Capability = cap
		out[cfg.PublicModel] = projection
		prepared = append(prepared, preparedConfig{cfg, publication})
		refs = append(refs, videobilling.CatalogTariffRef{ProviderScopeID: cfg.ProviderScopeID, Model: cfg.PublicModel, PricingVersion: cfg.PricingVersion})
		if !seenScope[cfg.ProviderScopeID] {
			seenScope[cfg.ProviderScopeID] = true
			scopeIDs = append(scopeIDs, cfg.ProviderScopeID)
		}
	}
	if len(prepared) == 0 {
		return out, nil
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ProviderScopeID != refs[j].ProviderScopeID {
			return refs[i].ProviderScopeID < refs[j].ProviderScopeID
		}
		return refs[i].Model < refs[j].Model
	})
	sort.Slice(scopeIDs, func(i, j int) bool { return scopeIDs[i] < scopeIDs[j] })
	var scopes []video.ProviderScope
	if err := tx.Where("id IN ?", scopeIDs).Find(&scopes).Error; err != nil {
		return nil, err
	}
	tariffs, err := videobilling.ReadCatalogTariffsTx(ctx, tx, refs, now)
	if err != nil {
		return nil, err
	}
	for _, current := range prepared {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cfg, publication := current.config, current.publication
		projection := out[cfg.PublicModel]
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
		if channel == nil || !video.PublicationChannelUsable(relay.VideoCatalogChannel{
			ID: channel.ID, PlatformOwned: channel.OwnerOrgID == 0, Enabled: channel.Status == 1,
			OfficialArkEndpoint: strings.TrimRight(channel.BaseURL, "/") == "https://ark.cn-beijing.volces.com/api/v3", Provider: channel.Provider,
		}) {
			continue
		}
		projection.Provider = channel.Provider
		billingContext, err := publication.BillingContext(*scope)
		if err != nil {
			out[cfg.PublicModel] = projection
			continue
		}
		cells := publication.Cells()
		allMissing := len(cells) > 0
		for _, cell := range cells {
			key := videobilling.PricingKey{ProviderScopeID: scope.ID, Model: cfg.PublicModel, Mode: cell.Mode, Resolution: cell.Resolution, InputVideo: cell.InputVideo, ServiceTier: cell.ServiceTier, Draft: cell.Draft}
			result, err := tariffs.Evaluate(key, billingContext)
			if err != nil {
				return nil, err
			}
			if len(result.Reasons) != 1 || result.Reasons[0] != videobilling.CatalogTariffPricingMissing {
				allMissing = false
			}
			if result.Price == nil {
				continue
			}
			projection.Pricing.Combinations = append(projection.Pricing.Combinations, pricingCombination{cell.Key(), cell.Mode, cell.Resolution, cell.InputVideo, cell.ServiceTier, cell.Draft, result.Price.RetailUSDPer1M.String()})
		}
		sort.Slice(projection.Pricing.Combinations, func(i, j int) bool {
			return projection.Pricing.Combinations[i].Key < projection.Pricing.Combinations[j].Key
		})
		if len(projection.Pricing.Combinations) > 0 {
			projection.Pricing.Status = "published"
			if len(projection.Pricing.Combinations) < len(cells) {
				projection.Pricing.Status = "partial"
			}
		} else if allMissing {
			projection.Pricing.Status = "missing"
		}
		out[cfg.PublicModel] = projection
	}
	return out, nil
}
