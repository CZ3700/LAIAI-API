package video

import (
	"encoding/json"

	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
)

// RequestHoldBreakdown explains a historical reservation, not another charge.
// Existing snapshots did not persist the policy's original risk multiplier.
// The observed reserved/base ratio is deliberately a separate, derived field.
type RequestHoldBreakdown struct {
	FormulaTokens        int64   `json:"formula_tokens"`
	MinimumTokens        int64   `json:"minimum_tokens"`
	BaseTokens           int64   `json:"base_tokens"`
	ReservedTokens       int64   `json:"reserved_tokens"`
	BaseAmountUSD        string  `json:"base_amount_usd"`
	ReservationFactor    string  `json:"reservation_factor"`
	FactorSource         string  `json:"factor_source"`
	RiskMultiplier       *string `json:"risk_multiplier"`
	RiskMultiplierSource string  `json:"risk_multiplier_source"`
}

func requestHoldBreakdown(s videobilling.Snapshot) *RequestHoldBreakdown {
	// A new formula version needs an explicit replay implementation. Missing
	// metadata must not borrow present-day prices, capabilities or defaults.
	if videoruntime.ValidateBillingSchema(s.BillingSchemaVersion) != nil || s.FormulaVersion != "ark-video-token-v1" || s.EstimatedTokens <= 0 {
		return nil
	}
	var saved struct {
		Resolution, Ratio, Mode, ServiceTier, CapabilityVersion *string
		Draft, InputVideo, BoundsVerified                       *bool
		MinimumTokens                                           *int64
		InputDurationMS, OutputDurationMS, OutputFrames         *int64
		Width, Height, FPSNumerator, FPSDenominator             *int64
	}
	if json.Unmarshal([]byte(s.MeteringJSON), &saved) != nil ||
		saved.Resolution == nil || saved.Ratio == nil || saved.Mode == nil || saved.ServiceTier == nil || saved.CapabilityVersion == nil ||
		saved.Draft == nil || saved.InputVideo == nil || saved.BoundsVerified == nil || !*saved.BoundsVerified ||
		saved.MinimumTokens == nil || *saved.MinimumTokens < 0 ||
		saved.InputDurationMS == nil || saved.OutputDurationMS == nil || saved.OutputFrames == nil ||
		saved.Width == nil || saved.Height == nil || saved.FPSNumerator == nil || saved.FPSDenominator == nil {
		return nil
	}
	if *saved.Ratio == "" || *saved.CapabilityVersion != s.CapabilityVersion || (!*saved.InputVideo && *saved.MinimumTokens != 0) {
		return nil
	}
	meter := videobilling.MeteringInput{
		Resolution: *saved.Resolution, Ratio: *saved.Ratio, Mode: *saved.Mode, ServiceTier: *saved.ServiceTier, Draft: *saved.Draft,
		InputVideo: *saved.InputVideo, InputDurationMS: *saved.InputDurationMS, OutputDurationMS: *saved.OutputDurationMS, OutputFrames: *saved.OutputFrames,
		Width: *saved.Width, Height: *saved.Height, FPSNumerator: *saved.FPSNumerator, FPSDenominator: *saved.FPSDenominator,
		BoundsVerified: *saved.BoundsVerified, CapabilityVersion: *saved.CapabilityVersion,
		EffectiveRatio: s.EffectiveRatio, FXUSDPerCNY: s.FXUSDPerCNY, FXVersion: s.FXVersion, FXObservedAt: s.FXObservedAt,
	}
	rule := videobilling.PricingRule{
		RuleID: s.RuleID, Version: s.RuleVersion,
		PricingKey:     videobilling.PricingKey{ProviderScopeID: s.ProviderScopeID, Model: s.Model, Mode: meter.Mode, Resolution: meter.Resolution, InputVideo: meter.InputVideo, ServiceTier: meter.ServiceTier, Draft: meter.Draft},
		RetailUSDPer1M: s.RetailUSDPer1M, SupplierCNYPer1M: s.SupplierCNYPer1M,
		MinimumRuleVersion: s.MinimumRuleVersion, FormulaVersion: s.FormulaVersion,
		RiskMultiplier: decimal.NewFromInt(1),
	}
	// Reuse pricing validation and its exact rational/frame/ceiling arithmetic.
	// risk=1 here isolates the base calculation; it is NOT claimed as the
	// original policy, and these local values never reach a write operation.
	formula, err := videobilling.Quote(rule, meter)
	if err != nil {
		return nil
	}
	rule.MinimumTokens = *saved.MinimumTokens
	base, err := videobilling.Quote(rule, meter)
	if err != nil || s.EstimatedTokens < base.EstimatedTokens {
		return nil
	}
	// Validate the reservation against immutable monetary facts before exposing
	// a decomposition. Never infer token counts by dividing rounded USD amounts.
	expectedHold := s.RetailUSDPer1M.Mul(decimal.NewFromInt(s.EstimatedTokens)).Shift(-6).Mul(s.EffectiveRatio).RoundCeil(8)
	if !s.HoldUSD.Equal(expectedHold) {
		return nil
	}
	return &RequestHoldBreakdown{
		FormulaTokens: formula.EstimatedTokens, MinimumTokens: *saved.MinimumTokens,
		BaseTokens: base.EstimatedTokens, ReservedTokens: s.EstimatedTokens,
		BaseAmountUSD:     base.HoldUSD.StringFixed(8),
		ReservationFactor: decimal.NewFromInt(s.EstimatedTokens).DivRound(decimal.NewFromInt(base.EstimatedTokens), 12).String(),
		FactorSource:      "derived_from_snapshot", RiskMultiplierSource: "not_snapshotted",
	}
}
