package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/relay"
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
	Key               string               `json:"key"`
	Provider          string               `json:"provider"`
	ChannelID         uint64               `json:"channel_id"`
	Model             string               `json:"model"`
	UpstreamModel     string               `json:"upstream_model"`
	PricingKind       string               `json:"pricing_kind"`
	RecognitionSource string               `json:"recognition_source"`
	BillingModes      []string             `json:"billing_modes"`
	GenericPrice      *catalogGenericPrice `json:"generic_price"`
	Meta              *ModelMeta           `json:"meta"`
	UnsupportedReason string               `json:"unsupported_reason"`
	Pricing           pricingDescriptor    `json:"pricing"`
}

// adminPricingCatalog returns a read-only catalog projection of models, providers, and channels.
func (s *Server) adminPricingCatalog(c *gin.Context) {
	db := s.db.WithContext(c.Request.Context()).Begin()
	if db.Error != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}
	defer db.Rollback()

	var channels []relay.Channel
	if err := db.Select("id,name,provider,base_url,models,model_mapping,status,owner_org_id").Order("id").Find(&channels).Error; err != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}
	var prices []relay.ModelPrice
	if err := db.Find(&prices).Error; err != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}
	var metas []ModelMeta
	if err := db.Find(&metas).Error; err != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}

	priceMap := make(map[string]relay.ModelPrice, len(prices))
	for _, p := range prices {
		priceMap[p.Model] = p
	}
	metaMap := make(map[string]ModelMeta, len(metas))
	for _, m := range metas {
		metaMap[m.Model] = m
	}

	providers := make([]catalogProvider, 0, len(relay.ListProviders()))
	seenProviders := map[string]bool{}
	for _, p := range relay.ListProviders() {
		providers = append(providers, catalogProvider{p.Name, p.DisplayName})
		seenProviders[p.Name] = true
	}

	channelRows := make([]catalogChannel, 0, len(channels))
	models := []catalogModel{}
	for _, ch := range channels {
		if !seenProviders[ch.Provider] {
			providers = append(providers, catalogProvider{ch.Provider, ch.Provider})
			seenProviders[ch.Provider] = true
		}
		channelRows = append(channelRows, catalogChannel{ch.ID, ch.Name, ch.Provider, ch.Status})
		seen := map[string]bool{}
		for name := range strings.SplitSeq(ch.Models, ",") {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			row := catalogModel{
				Key:               fmt.Sprintf("%d:%s", ch.ID, name),
				Provider:          ch.Provider,
				ChannelID:         ch.ID,
				Model:             name,
				UpstreamModel:     ch.UpstreamModel(name),
				PricingKind:       "unknown",
				RecognitionSource: "unknown",
				BillingModes:      []string{},
				UnsupportedReason: "pricing_kind_unknown",
			}
			p, priced := priceMap[name]
			if priced {
				row.GenericPrice = genericPriceProjection(p)
			}
			if m, ok := metaMap[name]; ok {
				copyMeta := m
				row.Meta = &copyMeta
			}
			classifyCatalogModel(&row, ch, p, priced)
			row.Pricing = genericDescriptor(row, p, priced, ch)
			if ch.Status != 1 {
				row.Pricing.Status = "disabled"
			}
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
	if err := db.Commit().Error; err != nil {
		fail(c, 503, "pricing catalog unavailable")
		return
	}
	c.JSON(200, gin.H{"providers": providers, "channels": channelRows, "models": models, "price_scope": "public_model_global"})
}

func classifyCatalogModel(row *catalogModel, ch relay.Channel, p relay.ModelPrice, priced bool) {
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
