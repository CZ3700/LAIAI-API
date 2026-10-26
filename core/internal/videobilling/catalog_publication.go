package videobilling

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/shopspring/decimal"
)

var (
	ErrCatalogTariffInvalid     = errors.New("video_billing: invalid catalog tariff query")
	ErrCatalogTariffLimit       = errors.New("video_billing: catalog tariff limit exceeded")
	ErrCatalogTariffUnavailable = errors.New("video_billing: catalog tariffs unavailable")
)

type CatalogTariffReason string

const (
	CatalogTariffPricingMissing         CatalogTariffReason = "pricing_missing"
	CatalogTariffPricingConflict        CatalogTariffReason = "pricing_conflict"
	CatalogTariffPricingVersionMismatch CatalogTariffReason = "pricing_version_mismatch"
	CatalogTariffPricingInvalid         CatalogTariffReason = "pricing_invalid"
	CatalogTariffMinimumCoverageMissing CatalogTariffReason = "minimum_coverage_missing"
	CatalogTariffMinimumRuleInvalid     CatalogTariffReason = "minimum_rule_invalid"
)

type CatalogPublicationError struct{ Reasons []CatalogTariffReason }

func (*CatalogPublicationError) Error() string        { return ErrPricingUnavailable.Error() }
func (*CatalogPublicationError) Is(target error) bool { return target == ErrPricingUnavailable }

type CatalogTariffRef struct {
	ProviderScopeID       uint64
	Model, PricingVersion string
}
type PublicationBound struct {
	Resolution, Ratio string
	Width, Height     int64
}
type PublicationContext struct {
	CapabilityVersion               string
	Ratios                          []string
	MinDuration, MaxDuration        int
	FPS                             int64
	Frames                          bool
	MinFrames, MaxFrames, FrameStep int64
	Bounds                          []PublicationBound
	MaxInputVideoDurationMS         int64
	FXUSDPerCNY                     decimal.Decimal
	FXVersion                       string
	FXObservedAt                    time.Time
}

// CatalogTariffSnapshot is an observation, never a reservation or send permit.
// It deliberately exposes neither its source rules nor supplier/evidence facts.
type CatalogTariffSnapshot struct {
	ctx   context.Context
	at    time.Time
	refs  []CatalogTariffRef
	rules []PricingRule
	sets  []MinimumRuleSet
	cells []MinimumRuleCell
}
type CatalogPublishedPrice struct {
	RuleID                                             string
	RetailUSDPer1M                                     decimal.Decimal
	PricingVersion, FormulaVersion, MinimumRuleVersion string
}
type CatalogCellResult struct {
	Price   *CatalogPublishedPrice
	Reasons []CatalogTariffReason
}

func blockedCatalogCell(reason CatalogTariffReason) CatalogCellResult {
	return CatalogCellResult{Reasons: []CatalogTariffReason{reason}}
}

// Evaluate performs no database, network or decryption operation. The caller
// still owns the observation transaction and must commit before publishing it.
func (s CatalogTariffSnapshot) Evaluate(key PricingKey, p PublicationContext) (CatalogCellResult, error) {
	if s.ctx == nil || s.at.IsZero() || !catalogKeyValid(key) {
		return CatalogCellResult{}, ErrCatalogTariffInvalid
	}
	if err := s.ctx.Err(); err != nil {
		return CatalogCellResult{}, catalogTariffFailure(err)
	}
	var ref *CatalogTariffRef
	for i := range s.refs {
		if s.refs[i].ProviderScopeID == key.ProviderScopeID && s.refs[i].Model == key.Model {
			ref = &s.refs[i]
			break
		}
	}
	if ref == nil {
		return CatalogCellResult{}, ErrCatalogTariffInvalid
	}
	var rule *PricingRule
	for i := range s.rules {
		r := &s.rules[i]
		if r.PricingKey == key {
			if rule != nil {
				return blockedCatalogCell(CatalogTariffPricingConflict), nil
			}
			rule = r
		}
	}
	if rule == nil {
		return blockedCatalogCell(CatalogTariffPricingMissing), nil
	}
	out := evaluateCatalogRule(*rule, key, ref.PricingVersion, p, s.sets, s.cells)
	if err := s.ctx.Err(); err != nil {
		return CatalogCellResult{}, catalogTariffFailure(err)
	}
	return out, nil
}

func catalogKeyValid(k PricingKey) bool {
	return k.ProviderScopeID > 0 && ark.IsPublicModel(k.Model) && validID(k.Mode) && validID(k.Resolution) && validID(k.ServiceTier)
}

// Both discovery and the actual plan validate through this same pure function.
// Its probes verify representability of the admitted domain, not a client quote.
func evaluateCatalogRule(rule PricingRule, key PricingKey, version string, p PublicationContext, sets []MinimumRuleSet, cells []MinimumRuleCell) CatalogCellResult {
	if rule.PricingKey != key {
		return blockedCatalogCell(CatalogTariffPricingInvalid)
	}
	if rule.Version != version || !validID(version) {
		return blockedCatalogCell(CatalogTariffPricingVersionMismatch)
	}
	// Non-reference reservations deliberately ignore historical scalar floors.
	if !rule.InputVideo {
		rule.MinimumTokens = 0
	}
	if !validRule(rule) || !rule.Enabled || !validPublicationContext(p) {
		return blockedCatalogCell(CatalogTariffPricingInvalid)
	}
	floor, reason := catalogMinimumTokens(rule, sets, cells, MinimumContext{Ratios: p.Ratios, MinDuration: p.MinDuration, MaxDuration: p.MaxDuration}, p.FPS)
	if reason != "" {
		return blockedCatalogCell(reason)
	}
	rule.MinimumTokens = floor
	var maximumFrames int64
	if p.Frames && !rule.InputVideo {
		if p.MinFrames <= 0 || p.MaxFrames < p.MinFrames || p.FrameStep <= 0 {
			return blockedCatalogCell(CatalogTariffPricingInvalid)
		}
		difference := p.MaxFrames - p.MinFrames // both positive and ordered
		steps := difference / p.FrameStep
		if steps > (math.MaxInt64-p.MinFrames)/p.FrameStep {
			return blockedCatalogCell(CatalogTariffPricingInvalid)
		}
		maximumFrames = p.MinFrames + steps*p.FrameStep
	}
	for _, ratio := range p.Ratios {
		if ratio == "adaptive" {
			continue
		}
		var bound *PublicationBound
		for i := range p.Bounds {
			b := &p.Bounds[i]
			if b.Resolution == key.Resolution && b.Ratio == ratio {
				if bound != nil {
					return blockedCatalogCell(CatalogTariffPricingInvalid)
				}
				bound = b
			}
		}
		if bound == nil || bound.Width <= 0 || bound.Height <= 0 || bound.Width > 16384 || bound.Height > 16384 {
			return blockedCatalogCell(CatalogTariffPricingInvalid)
		}
		in := MeteringInput{Resolution: key.Resolution, Ratio: ratio, Mode: key.Mode, ServiceTier: key.ServiceTier, Draft: key.Draft,
			InputVideo: rule.InputVideo, OutputDurationMS: int64(p.MaxDuration) * 1000,
			Width: bound.Width, Height: bound.Height, FPSNumerator: p.FPS, FPSDenominator: 1, BoundsVerified: true,
			CapabilityVersion: p.CapabilityVersion, EffectiveRatio: decimal.NewFromInt(1), FXUSDPerCNY: p.FXUSDPerCNY, FXVersion: p.FXVersion, FXObservedAt: p.FXObservedAt}
		if rule.InputVideo {
			in.InputDurationMS = p.MaxInputVideoDurationMS
		}
		if _, err := Quote(rule, in); err != nil {
			return blockedCatalogCell(CatalogTariffPricingInvalid)
		}
		if maximumFrames > 0 {
			in.OutputDurationMS, in.OutputFrames = 0, maximumFrames
			if _, err := Quote(rule, in); err != nil {
				return blockedCatalogCell(CatalogTariffPricingInvalid)
			}
		}
	}
	minimumVersion := ""
	if rule.InputVideo {
		minimumVersion = rule.MinimumRuleVersion
	}
	return CatalogCellResult{Price: &CatalogPublishedPrice{RuleID: rule.RuleID, RetailUSDPer1M: rule.RetailUSDPer1M, PricingVersion: rule.Version, FormulaVersion: rule.FormulaVersion, MinimumRuleVersion: minimumVersion}, Reasons: []CatalogTariffReason{}}
}

func validPublicationContext(p PublicationContext) bool {
	if !validID(p.CapabilityVersion) || p.MinDuration < 4 || p.MaxDuration < p.MinDuration || p.MaxDuration > 30 || p.FPS < 1 || p.FPS > 120 || len(p.Ratios) == 0 || len(p.Ratios) > 7 || len(p.Bounds) > 28 || p.MaxInputVideoDurationMS < 0 {
		return false
	}
	if !p.FXUSDPerCNY.IsPositive() || !validRate(p.FXUSDPerCNY) || !validID(p.FXVersion) || p.FXObservedAt.IsZero() {
		return false
	}
	seen := map[string]bool{}
	actual := 0
	for _, r := range p.Ratios {
		if seen[r] || !oneOf(r, "adaptive", "16:9", "4:3", "1:1", "3:4", "9:16", "21:9") {
			return false
		}
		seen[r] = true
		if r != "adaptive" {
			actual++
		}
	}
	return actual > 0
}
