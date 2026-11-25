package server

import (
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
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
