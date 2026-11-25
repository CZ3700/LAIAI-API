package relay

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/shopspring/decimal"
)

type resolvedBillingRule struct {
	Binding *pricing.ModelRuleBinding
	Rule    *pricing.BillingRule
}

var (
	cstZone = time.FixedZone("CST", 8*3600)

	targetAdaptersMu     sync.RWMutex
	customTargetAdapters = make(map[string]func(*Channel) adapter)
)

// RegisterTargetAdapter 允许扩展包按 forward_rules.target_type 注册自定义同步协议适配器。
func RegisterTargetAdapter(targetType string, factory func(*Channel) adapter) {
	tt := strings.ToLower(strings.TrimSpace(targetType))
	if tt == "" || factory == nil {
		return
	}
	targetAdaptersMu.Lock()
	customTargetAdapters[tt] = factory
	targetAdaptersMu.Unlock()
}

func isMissingRuleTableErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "does not exist")
}

// cachedBillingRule 读取 model_rule_bindings + billing_rules（45s TTL）。
// 未绑定返回 (nil, nil) 并缓存空结构；真实 DB 故障返回 err 以免把瞬时故障缓存为“无规则”。
func (r *Relay) cachedBillingRule(model string) (*pricing.ModelRuleBinding, *pricing.BillingRule) {
	if r.billingRules == nil {
		return nil, nil
	}
	res, err := r.billingRules.getOrLoad(model, func() (*resolvedBillingRule, error) {
		b, rule, err := pricing.ResolveModelRule(r.DB, model)
		if err != nil {
			if isMissingRuleTableErr(err) {
				return &resolvedBillingRule{}, nil
			}
			return nil, err
		}
		return &resolvedBillingRule{Binding: b, Rule: rule}, nil
	})
	if err != nil {
		r.Log.Warn("billing rule lookup failed, falling back to model_prices", "model", model, "err", err)
		return nil, nil
	}
	if res == nil {
		return nil, nil
	}
	return res.Binding, res.Rule
}

// resolveForwardRule 查 model_rule_bindings 关联的 forward_rules 并按入站路径评分匹配。
func (r *Relay) resolveForwardRule(ctx context.Context, model, category, reqPath string) *forward.ResolvedForward {
	if r.Forward == nil || model == "" {
		return nil
	}
	fwd, err := r.Forward.ResolveForModel(ctx, r.DB, model, category, reqPath)
	if err != nil {
		if !isMissingRuleTableErr(err) {
			r.Log.Warn("forward rule resolve failed", "model", model, "err", err)
		}
		return nil
	}
	return fwd
}

func inferInboundCategory(reqPath string) string {
	p := strings.ToLower(reqPath)
	switch {
	case strings.Contains(p, "/images"):
		return "图片"
	case strings.Contains(p, "/video"), strings.Contains(p, "/generations/tasks"):
		return "视频"
	case strings.Contains(p, "/audio"):
		return "音频"
	case strings.Contains(p, "/embeddings"):
		return "嵌入"
	default:
		return "聊天"
	}
}

func adapterForTarget(targetType string, ch *Channel) adapter {
	tt := strings.ToLower(strings.TrimSpace(targetType))
	targetAdaptersMu.RLock()
	factory, ok := customTargetAdapters[tt]
	targetAdaptersMu.RUnlock()
	if ok {
		return factory(ch)
	}

	switch tt {
	case "anthropic":
		return anthropicAdapter{}
	case "gemini":
		return geminiAdapter{}
	case "openai-responses", "responses":
		return openaiResponsesAdapter{}
	case "volcengine", "volcengine_chat":
		return volcengineChatAdapter{}
	case "openai":
		return openaiAdapter{}
	default:
		return adapterForChannel(ch)
	}
}

// buildForwardAwareRequest 在命中 forward_rules 时按 target_type / upstream_path / auth_type 构建上游请求。
func buildForwardAwareRequest(
	ctx context.Context,
	ch *Channel,
	fwd *forward.ResolvedForward,
	credential string,
	req map[string]any,
	stream bool,
) (*http.Request, adapter, error) {
	if fwd == nil {
		ad := adapterForChannel(ch)
		upReq, err := ad.buildRequest(ctx, ch, credential, req, stream)
		return upReq, ad, err
	}

	ad := adapterForTarget(fwd.TargetType, ch)
	// 浅拷贝渠道视图（绝不原地修改共享渠道快照）
	chCopy := *ch
	if fwd.UpstreamPath != "" {
		chCopy.UpstreamPath = fwd.UpstreamPath
	}
	upReq, err := ad.buildRequest(ctx, &chCopy, credential, req, stream)
	if err != nil {
		return nil, ad, err
	}
	if fwd.UpstreamPath != "" {
		tt := strings.ToLower(strings.TrimSpace(fwd.TargetType))
		if tt == "openai" || tt == "volcengine_chat" || tt == "" {
			parsedURL, uerr := forward.JoinUpstreamURL(ch.BaseURL, fwd.UpstreamPath)
			if uerr != nil {
				return nil, ad, uerr
			}
			upReq.URL = parsedURL
		}
	}
	if strings.TrimSpace(fwd.AuthType) != "" {
		forward.ApplyAuth(upReq, fwd.AuthType, credential)
	}
	return upReq, ad, nil
}

// computeBillingRuleHold 基于 billing_rules + model_rule_bindings 计算请求级预冻结金额。
func computeBillingRuleHold(
	binding *pricing.ModelRuleBinding,
	rule *pricing.BillingRule,
	holdIn int64,
	holdOutput int64,
	groupRatio decimal.Decimal,
	timeMult decimal.Decimal,
	serviceTier string,
) decimal.Decimal {
	disc := groupRatio.Mul(binding.EffectiveDiscount())
	feat := pricing.ExtractedFeatures{
		ServiceTier:    serviceTier,
		TimeMultiplier: &timeMult,
	}
	var holdAmount decimal.Decimal
	if rule != nil {
		holdAmount, _ = pricing.ComputeCost(rule, pricing.UsageTokens{
			Prompt:     holdIn,
			Completion: holdOutput,
		}, disc, feat)
	}
	if binding != nil && binding.PreDeduction.IsPositive() {
		pre := binding.PreDeduction.Mul(groupRatio).Round(6)
		if pre.GreaterThan(holdAmount) {
			holdAmount = pre
		}
	}
	return holdAmount
}

// computeBillingRuleActual 基于上游真实 usage + 锁定峰谷倍率 + billing_rules 引擎结算实际费用。
func computeBillingRuleActual(
	binding *pricing.ModelRuleBinding,
	rule *pricing.BillingRule,
	u usage,
	cw5m, cw1h, aIn int64,
	groupRatio, byok, timeMult decimal.Decimal,
	serviceTier string,
) (decimal.Decimal, string) {
	disc := groupRatio.Mul(binding.EffectiveDiscount()).Mul(byok)
	ut := pricing.UsageTokens{
		Prompt:          u.PromptTokens,
		Completion:      u.CompletionTokens,
		Total:           u.PromptTokens + u.CompletionTokens,
		HasTotalTokens:  true,
		Cached:          u.Cached(),
		CacheCreation:   cw5m + cw1h,
		CacheCreation1h: cw1h,
		AudioTokens:     aIn,
		ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
	}
	feat := pricing.ExtractedFeatures{
		ServiceTier:    serviceTier,
		TimeMultiplier: &timeMult,
	}
	return pricing.ComputeCost(rule, ut, disc, feat)
}
