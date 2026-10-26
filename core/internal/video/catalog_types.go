package video

import (
	"errors"
	"time"
)

var (
	ErrCatalogInvalid     = errors.New("video catalog: invalid request")
	ErrCatalogDenied      = errors.New("video catalog: access denied")
	ErrCatalogLimit       = errors.New("video catalog: observation limit exceeded")
	ErrCatalogUnavailable = errors.New("video catalog: unavailable")
)

type CatalogSubjectRef struct{ OrgID, KeyID uint64 }

// CatalogObservation is an observation, never a reusable send permission.
type CatalogObservation struct {
	CheckedAt time.Time      `json:"-"`
	Models    []CatalogModel `json:"data"`
	subject   officialActionSubject
}

// AllowsModel reuses the same authenticated RR subject for the generic host
// projection. A zero-value observation denies every model.
func (o CatalogObservation) AllowsModel(model string) bool {
	return o.subject.allowsModel(model)
}

type CatalogModel struct {
	ID                 string              `json:"id"`
	Provider           string              `json:"-"`
	Enabled            bool                `json:"enabled"`
	Verified           bool                `json:"verified"`
	CapabilityVersion  string              `json:"capability_version"`
	PricingVersion     string              `json:"pricing_version"`
	BillingRuleVersion string              `json:"billing_rule_version"`
	Capabilities       CatalogCapabilities `json:"capabilities"`
	Prices             []CatalogPrice      `json:"prices"`
	Publication        CatalogPublication  `json:"publication"`
	Actions            CatalogActions      `json:"actions"`
}

type CatalogCapabilities struct {
	Resolutions        []string `json:"resolutions"`
	MinDurationSeconds int      `json:"min_duration_seconds"`
	MaxDurationSeconds int      `json:"max_duration_seconds"`
	Ratios             []string `json:"ratios"`
	AutomaticDuration  bool     `json:"automatic_duration"`
	Frames             bool     `json:"frames"`
	InputVideo         bool     `json:"input_video"`
	InputAudio         bool     `json:"input_audio"`
	Draft              bool     `json:"draft"`
	PortraitAssets     bool     `json:"portrait_assets"`
}

type CatalogPrice struct {
	PriceRuleID        string `json:"price_rule_id"`
	Label              string `json:"label"`
	Mode               string `json:"mode"`
	Resolution         string `json:"resolution"`
	InputVideo         bool   `json:"input_video"`
	ServiceTier        string `json:"service_tier"`
	Draft              bool   `json:"draft"`
	Unit               string `json:"unit"`
	Amount             string `json:"amount"`
	Currency           string `json:"currency"`
	MinimumRuleVersion string `json:"minimum_rule_version"`
}

type CatalogPublication struct {
	Status       string               `json:"status"`
	ReasonCodes  []string             `json:"reason_codes"`
	Combinations []CatalogCombination `json:"combinations"`
}

type CatalogCombination struct {
	Key                string   `json:"key"`
	Mode               string   `json:"mode"`
	Resolution         string   `json:"resolution"`
	InputVideo         bool     `json:"input_video"`
	ServiceTier        string   `json:"service_tier"`
	Draft              bool     `json:"draft"`
	Status             string   `json:"status"`
	ReasonCodes        []string `json:"reason_codes"`
	PricingVersion     *string  `json:"pricing_version"`
	BillingRuleVersion *string  `json:"billing_rule_version"`
	MinimumRuleVersion *string  `json:"minimum_rule_version"`
}

type CatalogAction struct {
	Eligible    bool     `json:"eligible"`
	ReasonCodes []string `json:"reason_codes"`
}
type CatalogActions struct {
	ModelsRead CatalogAction `json:"models_read"`
	Estimate   CatalogAction `json:"estimate"`
	Create     CatalogAction `json:"create"`
}

func (m CatalogModel) HasEligibleAction() bool {
	return m.Actions.ModelsRead.Eligible || m.Actions.Estimate.Eligible || m.Actions.Create.Eligible
}
