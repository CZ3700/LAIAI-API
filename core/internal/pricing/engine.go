package pricing

import (
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ComputeCostRaw 未舍入的基础计费计算（未乘峰谷时段倍率）。
func ComputeCostRaw(
	rule *BillingRule,
	usage UsageTokens,
	discount decimal.Decimal,
	feat ExtractedFeatures,
) (decimal.Decimal, string) {
	if rule == nil {
		total := usage.Prompt + usage.Completion
		cost := decimal.NewFromInt(total).Div(oneMillion).Mul(discount)
		return cost, fmt.Sprintf("无规则默认计费: %d总Tokens * 1/1M * %s倍率", total, fmtDiscount(discount))
	}

	var cost decimal.Decimal
	var detail string
	switch rule.BillingType {
	case BillingTypeRequests:
		cost, detail = computeRequestsCost(rule, usage, discount, feat)
	case BillingTypeDuration:
		cost, detail = computeDurationCost(rule, usage, discount, feat)
	default:
		cost, detail = computeTokensCost(rule, usage, discount, feat)
	}
	if cost.IsNegative() {
		cost = decimal.Zero
	}
	return cost, detail
}

// ComputeCost 统一计费入口：自动叠加峰谷时段倍率并按 6 位小数四舍五入。
func ComputeCost(
	rule *BillingRule,
	usage UsageTokens,
	discount decimal.Decimal,
	feat ExtractedFeatures,
) (decimal.Decimal, string) {
	cost, detail := ComputeCostRaw(rule, usage, discount, feat)
	if rule != nil {
		tm := decimal.NewFromInt(1)
		if feat.TimeMultiplier != nil {
			tm = *feat.TimeMultiplier
		} else if rule.AppliedMultiplier.IsPositive() {
			tm = rule.AppliedMultiplier
		}
		if !tm.Equal(decimal.NewFromInt(1)) && tm.Sub(decimal.NewFromInt(1)).Abs().GreaterThan(decimal.RequireFromString("0.00001")) {
			cost = cost.Mul(tm)
			detail = fmt.Sprintf("%s * %s倍(时段倍率)", detail, fmtDiscount(tm))
		}
	}
	return RoundMoney(cost), detail
}

// ResolveModelRule 读取模型绑定与关联启用的计费规则（未绑定或规则未启用时返回 nil）。
func ResolveModelRule(db *gorm.DB, model string) (*ModelRuleBinding, *BillingRule, error) {
	if db == nil {
		return nil, nil, nil
	}
	var binding ModelRuleBinding
	if err := db.Where("model = ? AND is_active = 1", model).First(&binding).Error; err != nil {
		return nil, nil, nil
	}
	if binding.BillingRuleID == 0 {
		return &binding, nil, nil
	}
	var rule BillingRule
	if err := db.Where("id = ? AND is_active = 1", binding.BillingRuleID).First(&rule).Error; err != nil {
		return &binding, nil, nil
	}
	return &binding, &rule, nil
}
