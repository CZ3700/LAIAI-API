package video

import (
	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/videobilling"
)

// Explicit customer projection: never serialize the internal snapshot, which
// also contains supplier costs, FX, provider scope and evidence fingerprints.
func publicMetering(m videobilling.MeteringInput, minimumVersion string) gin.H {
	source := m.InputDurationSource
	if source == "" {
		source = "unknown"
	}
	return gin.H{
		"mode": m.Mode, "resolution": m.Resolution, "ratio": m.Ratio,
		"service_tier": m.ServiceTier, "draft": m.Draft,
		"input_video": m.InputVideo, "input_video_count": m.InputVideoCount,
		"input_duration_ms": m.InputDurationMS, "input_duration_source": source,
		"output_duration_ms": m.OutputDurationMS, "output_frames": m.OutputFrames,
		"width": m.Width, "height": m.Height,
		"fps_numerator": m.FPSNumerator, "fps_denominator": m.FPSDenominator,
		"minimum_tokens": m.MinimumTokens, "minimum_rule_version": minimumVersion,
		"capability_version":    m.CapabilityVersion,
		"input_token_semantics": "not_projected_separately_completion_tokens_authoritative",
		"usage_semantics":       "reservation_upper_bound",
	}
}

func publicPrice(rule videobilling.PricingRule, amount string) gin.H {
	inputMode := "without_input_video"
	if rule.InputVideo {
		inputMode = "with_input_video"
	}
	return gin.H{
		"price_rule_id": rule.RuleID,
		"label":         rule.Resolution + "/" + inputMode + "/" + rule.ServiceTier,
		"mode":          rule.Mode, "resolution": rule.Resolution,
		"input_video": rule.InputVideo, "service_tier": rule.ServiceTier, "draft": rule.Draft,
		"unit": "million_video_tokens", "amount": amount, "currency": "USD",
		"minimum_rule_version": rule.MinimumRuleVersion,
	}
}
