package videobilling

import (
	"math"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var maxMoney = decimal.RequireFromString("999999999999.99999999")

func validMoney(d decimal.Decimal) bool { return !d.IsNegative() && !d.GreaterThan(maxMoney) }
func validRate(d decimal.Decimal) bool  { return validMoney(d) && d.Equal(d.Truncate(12)) }
func validID(s string) bool             { return len(s) > 0 && len(s) <= 64 }

// requireTx prevents accidentally committing a half reservation via a global DB.
func requireTx(tx *gorm.DB) error {
	if tx == nil || tx.Statement == nil {
		return ErrTransactionRequired
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrTransactionRequired
	}
	return nil
}

func validRule(r PricingRule) bool {
	if ark.Is20Variant(r.Model) && (r.Mode != "generate" || !oneOf(r.Resolution, "480p", "720p") || r.ServiceTier != "default" || r.Draft) {
		return false
	}
	return validID(r.RuleID) && validID(r.Version) && r.ProviderScopeID > 0 &&
		validID(r.Model) && r.Mode != "" && r.Resolution != "" && r.ServiceTier != "" &&
		r.RetailUSDPer1M.IsPositive() && validRate(r.RetailUSDPer1M) &&
		r.SupplierCNYPer1M.IsPositive() && validRate(r.SupplierCNYPer1M) &&
		r.MinimumTokens >= 0 && (!r.InputVideo || validID(r.MinimumRuleVersion)) &&
		r.RiskMultiplier.GreaterThanOrEqual(decimal.NewFromInt(1)) && validRate(r.RiskMultiplier) && validID(r.FormulaVersion)
}

// Quote uses rational frame rates and decimal arithmetic; no artificial token
// cap. Bounds that cannot be safely represented are rejected, never truncated.
func Quote(rule PricingRule, in MeteringInput) (QuoteResult, error) {
	if ark.Is20Variant(rule.Model) && (in.OutputFrames != 0 || in.OutputDurationMS < 4000 || in.OutputDurationMS > 15000 || in.OutputDurationMS%1000 != 0 || in.InputDurationMS > 15000 || in.FPSNumerator != 24 || in.FPSDenominator != 1) {
		return QuoteResult{}, ErrInvalid
	}
	if !validRule(rule) || !in.BoundsVerified || !validID(in.CapabilityVersion) ||
		in.InputVideo != rule.InputVideo || in.InputDurationMS < 0 ||
		(!in.InputVideo && in.InputDurationMS != 0) || (in.InputVideo && in.InputDurationMS <= 0) ||
		in.OutputDurationMS < 0 || in.OutputFrames < 0 ||
		(in.OutputDurationMS == 0) == (in.OutputFrames == 0) ||
		in.Width <= 0 || in.Height <= 0 || in.FPSNumerator <= 0 || in.FPSDenominator <= 0 ||
		!in.EffectiveRatio.IsPositive() || !validRate(in.EffectiveRatio) ||
		!in.FXUSDPerCNY.IsPositive() || !validRate(in.FXUSDPerCNY) || !validID(in.FXVersion) || in.FXObservedAt.IsZero() {
		return QuoteResult{}, ErrInvalid
	}
	// Keep a common denominator until the final ceiling. decimal.DivRound avoids
	// default division precision rounding a small positive remainder to zero.
	framesN := decimal.NewFromInt(in.InputDurationMS).Add(decimal.NewFromInt(in.OutputDurationMS)).Mul(decimal.NewFromInt(in.FPSNumerator))
	den := decimal.NewFromInt(in.FPSDenominator).Mul(decimal.NewFromInt(1000))
	if in.OutputFrames > 0 {
		framesN = framesN.Add(decimal.NewFromInt(in.OutputFrames).Mul(den))
	}
	numerator := framesN.Mul(decimal.NewFromInt(in.Width)).Mul(decimal.NewFromInt(in.Height))
	den = den.Mul(decimal.NewFromInt(1024))
	base := ceilRatio(numerator, den)
	if base.LessThan(decimal.NewFromInt(rule.MinimumTokens)) {
		base = decimal.NewFromInt(rule.MinimumTokens)
	}
	est := base.Mul(rule.RiskMultiplier).Ceil()
	if !est.IsPositive() || est.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return QuoteResult{}, ErrInvalid
	}
	hold := rule.RetailUSDPer1M.Mul(est).Shift(-6).Mul(in.EffectiveRatio).RoundCeil(8)
	cny := rule.SupplierCNYPer1M.Mul(est).Shift(-6).RoundCeil(8)
	usd := cny.Mul(in.FXUSDPerCNY).RoundCeil(8)
	if !hold.IsPositive() || !validMoney(hold) || !validMoney(cny) || !validMoney(usd) {
		return QuoteResult{}, ErrInvalid
	}
	return QuoteResult{Rule: rule, Metering: in, EstimatedTokens: est.IntPart(), HoldUSD: hold, SupplierReservedCNY: cny, SupplierReservedUSD: usd}, nil
}

func ceilRatio(n, d decimal.Decimal) decimal.Decimal {
	q, rem := n.QuoRem(d, 0)
	if rem.IsPositive() {
		q = q.Add(decimal.NewFromInt(1))
	}
	return q
}

// ResolvePricingTx refuses overlapping active rules instead of guessing a price.
func ResolvePricingTx(tx *gorm.DB, key PricingKey, at time.Time) (PricingRule, error) {
	if err := requireTx(tx); err != nil {
		return PricingRule{}, err
	}
	if at.IsZero() {
		return PricingRule{}, ErrInvalid
	}
	var rows []PricingRule
	err := tx.Where("provider_scope_id = ? AND model = ? AND mode = ? AND resolution = ? AND input_video = ? AND service_tier = ? AND draft = ? AND enabled = ? AND effective_from <= ? AND (effective_until IS NULL OR effective_until > ?)",
		key.ProviderScopeID, key.Model, key.Mode, key.Resolution, key.InputVideo, key.ServiceTier, key.Draft, true, at.UTC(), at.UTC()).Limit(2).Find(&rows).Error
	if err != nil {
		return PricingRule{}, err
	}
	if len(rows) != 1 || !validRule(rows[0]) {
		return PricingRule{}, ErrPricingUnavailable
	}
	return rows[0], nil
}
