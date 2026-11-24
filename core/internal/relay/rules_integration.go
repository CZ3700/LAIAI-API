package relay

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/shopspring/decimal"
)

type resolvedBillingRule struct {
	Binding *pricing.ModelRuleBinding
	Rule    *pricing.BillingRule
}

var cstZone = time.FixedZone("CST", 8*3600)

func isMissingRuleTableErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "does not exist")
}

// cachedBillingRule 读取 model_rule_bindings + billing_rules（45s TTL；未绑定或测试库缺表返回 nil）。
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
			// 无绑定时 ResolveModelRule 返回 gorm.ErrRecordNotFound → 视为未绑定，走 legacy model_prices
			return &resolvedBillingRule{}, nil
		}
		return &resolvedBillingRule{Binding: b, Rule: rule}, nil
	})
	if err != nil || res == nil {
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

// joinUpstreamURL 智能拼接渠道 base_url 与规则 upstream_path，避免 /api/v3 或 /v1 段重复叠加。
func joinUpstreamURL(baseURL, upstreamPath string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	up := strings.TrimSpace(upstreamPath)
	if up == "" {
		return base
	}
	if !strings.HasPrefix(up, "/") {
		up = "/" + up
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		basePath := strings.TrimRight(parsed.Path, "/")
		if basePath != "" && (up == basePath || strings.HasPrefix(up, basePath+"/")) {
			return parsed.Scheme + "://" + parsed.Host + up
		}
	}
	return base + up
}

func applyForwardAuth(req *http.Request, authType, credential string) {
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "x-api-key":
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", credential)
	case "x-goog-api-key":
		req.Header.Del("Authorization")
		req.Header.Set("x-goog-api-key", credential)
	case "query_key":
		req.Header.Del("Authorization")
		q := req.URL.Query()
		q.Set("key", credential)
		req.URL.RawQuery = q.Encode()
	case "key":
		req.Header.Set("Authorization", "Key "+credential)
	default:
		req.Header.Set("Authorization", "Bearer "+credential)
	}
}

func adapterForTarget(targetType string, ch *Channel) adapter {
	switch strings.ToLower(strings.TrimSpace(targetType)) {
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
	// 复制渠道临时视图（绝不污染共享快照），注入规则定义的 upstream_path（适用于 OpenAI 家族与兼容路由）
	chCopy := *ch
	if fwd.UpstreamPath != "" {
		chCopy.UpstreamPath = fwd.UpstreamPath
	}
	upReq, err := ad.buildRequest(ctx, &chCopy, credential, req, stream)
	if err != nil {
		return nil, ad, err
	}
	// 对非 OpenAI 原生 adapter 或跨版本路径重写，直接按智能拼接替换 Path 与 AuthType
	if fwd.UpstreamPath != "" {
		tt := strings.ToLower(strings.TrimSpace(fwd.TargetType))
		if tt == "openai" || tt == "volcengine_chat" || tt == "" {
			if targetURL := joinUpstreamURL(ch.BaseURL, fwd.UpstreamPath); targetURL != "" {
				if parsed, perr := url.Parse(targetURL); perr == nil {
					upReq.URL = parsed
				}
			}
		}
	}
	if strings.TrimSpace(fwd.AuthType) != "" {
		applyForwardAuth(upReq, fwd.AuthType, credential)
	}
	// 当 fwd.UpstreamPath 与 baseURL 已构成完整原生请求且 Body 为 JSON 且需要确保流式 include_usage 时，
	// ad.buildRequest 已完成 Canonical→Target 转换。
	return upReq, ad, nil
}

// bindingSiteDiscount 计算模型绑定的站点折扣×全局折扣（未启用各自分别视作 1）。
func bindingSiteDiscount(binding *pricing.ModelRuleBinding) decimal.Decimal {
	d := decimal.NewFromInt(1)
	if binding == nil {
		return d
	}
	if binding.SiteDiscountEnabled == 1 && binding.SiteDiscount.IsPositive() {
		d = d.Mul(binding.SiteDiscount)
	}
	if binding.GlobalDiscountEnabled == 1 && binding.GlobalDiscount.IsPositive() {
		d = d.Mul(binding.GlobalDiscount)
	}
	return d
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
	disc := groupRatio.Mul(bindingSiteDiscount(binding))
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
	disc := groupRatio.Mul(bindingSiteDiscount(binding)).Mul(byok)
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
	actual, detail := pricing.ComputeCost(rule, ut, disc, feat)
	return actual, detail
}

// suppressUnusedImport 保留 bytes 导入备用（如未来 multipart 包装）。
var _ = bytes.MinRead
var _ = (*gin.Context)(nil)
