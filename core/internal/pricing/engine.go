package pricing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ComputeCostRaw 未舍入的基础计费计算（未乘峰谷时段倍率）。
// 优先查询可扩展计算器注册表，未注册时走标准 tokens / requests / duration 引擎。
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
	if customFn, ok := lookupCustomCalculator(rule.BillingType, rule.BillingRule); ok {
		cost, detail = customFn(rule, usage, discount, feat)
	} else {
		switch rule.BillingType {
		case BillingTypeRequests:
			cost, detail = computeRequestsCost(rule, usage, discount, feat)
		case BillingTypeDuration:
			cost, detail = computeDurationCost(rule, usage, discount, feat)
		default:
			cost, detail = computeTokensCost(rule, usage, discount, feat)
		}
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

// ResolveModelRuleContext 按请求上下文读取模型绑定与关联启用的计费规则。
// 严格区分“未绑定/未启用”（返回 nil, nil, nil）与真实数据库故障（返回包装后的 error，防静默缓存错误状态）。
func ResolveModelRuleContext(ctx context.Context, db *gorm.DB, model string) (*ModelRuleBinding, *BillingRule, error) {
	model = strings.TrimSpace(model)
	if db == nil || model == "" {
		return nil, nil, nil
	}
	var binding ModelRuleBinding
	if err := db.WithContext(ctx).Where("model = ? AND is_active = 1", model).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("query model_rule_bindings %q: %w", model, err)
	}
	if binding.BillingRuleID == 0 {
		return &binding, nil, nil
	}
	var rule BillingRule
	if err := db.WithContext(ctx).Where("id = ? AND is_active = 1", binding.BillingRuleID).First(&rule).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &binding, nil, nil
		}
		return nil, nil, fmt.Errorf("query billing_rules id=%d: %w", binding.BillingRuleID, err)
	}
	return &binding, &rule, nil
}

// ResolveModelRule 兼容无显式 context 的调用入口。
func ResolveModelRule(db *gorm.DB, model string) (*ModelRuleBinding, *BillingRule, error) {
	return ResolveModelRuleContext(context.Background(), db, model)
}
