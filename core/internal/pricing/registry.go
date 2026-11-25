package pricing

import (
	"slices"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
)

// RuleCalculator 自定义/扩展计费策略函数签名。
// 新增厂商计费算法（如新视频模型）可通过 RegisterCalculator 注入，无需修改核心分发引擎。
type RuleCalculator func(
	rule *BillingRule,
	usage UsageTokens,
	discount decimal.Decimal,
	feat ExtractedFeatures,
) (decimal.Decimal, string)

type registryKey struct {
	billingType string
	ruleName    string
}

var (
	regMu             sync.RWMutex
	customCalculators = make(map[registryKey]RuleCalculator)

	builtinRulesByType = map[string][]string{
		BillingTypeTokens: {
			"standard", "tiered", "seedance2.0", "seedance1.5pro", "seedance1.0",
			"doubao_chat", "kimi_chat", "multimodal", "gpt_billing", "claude", "glm_5_3",
		},
		BillingTypeRequests: {
			"fixed", "standard", "per_image", "image_resolution", "image_resolution_io",
			"image_size_pixel", "vidu_image", "characters", "volc_seedream_pro",
		},
		BillingTypeDuration: {
			"fixed", "standard", "video_resolution", "minimax_h3", "video_seconds_io",
			"video_seconds_ref", "fal_ref_video", "video_quality", "kling_video",
			"kling4_video", "vidu_video", "volc_enhance_cascade",
		},
	}
)

// RegisterCalculator 注册自定义计费规则计算器（并发安全，支持后续扩展新多模态厂商）。
func RegisterCalculator(billingType, ruleName string, fn RuleCalculator) {
	bt := strings.ToLower(strings.TrimSpace(billingType))
	rn := strings.ToLower(strings.TrimSpace(ruleName))
	if bt == "" || rn == "" || fn == nil {
		return
	}
	regMu.Lock()
	customCalculators[registryKey{billingType: bt, ruleName: rn}] = fn
	regMu.Unlock()
}

func lookupCustomCalculator(billingType, ruleName string) (RuleCalculator, bool) {
	regMu.RLock()
	fn, ok := customCalculators[registryKey{
		billingType: strings.ToLower(strings.TrimSpace(billingType)),
		ruleName:    strings.ToLower(strings.TrimSpace(ruleName)),
	}]
	regMu.RUnlock()
	return fn, ok
}

// IsKnownBillingRule 判断指定 billing_type 下的 billing_rule 标识是否已内置或已通过扩展注册。
func IsKnownBillingRule(billingType, ruleName string) bool {
	bt := strings.ToLower(strings.TrimSpace(billingType))
	rn := strings.ToLower(strings.TrimSpace(ruleName))
	if rn == "" {
		return true
	}
	if _, ok := lookupCustomCalculator(bt, rn); ok {
		return true
	}
	return slices.Contains(builtinRulesByType[bt], rn)
}

// SupportedRules 返回指定 billingType 下全部可用规则名拷贝（防御性克隆）。
func SupportedRules(billingType string) []string {
	bt := strings.ToLower(strings.TrimSpace(billingType))
	out := slices.Clone(builtinRulesByType[bt])
	regMu.RLock()
	for k := range customCalculators {
		if k.billingType == bt && !slices.Contains(out, k.ruleName) {
			out = append(out, k.ruleName)
		}
	}
	regMu.RUnlock()
	slices.Sort(out)
	return out
}
