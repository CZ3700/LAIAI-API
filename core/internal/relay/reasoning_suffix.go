package relay

import "strings"

const (
	reasoningBudgetLow    int64 = 2048
	reasoningBudgetMedium int64 = 8192
	reasoningBudgetHigh   int64 = 16384
)

// splitReasoningSuffix splits OpenAI-style reasoning effort suffixes (-low/-medium/-high) from a model name.
func splitReasoningSuffix(model string) (base, effort string, ok bool) {
	lower := strings.ToLower(model)
	for _, suf := range []string{"-high", "-medium", "-low"} {
		if strings.HasSuffix(lower, suf) && len(model) > len(suf) {
			return model[:len(model)-len(suf)], suf[1:], true
		}
	}
	return model, "", false
}

func reasoningEffortBudget(effort string) int64 {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "high":
		return reasoningBudgetHigh
	case "medium":
		return reasoningBudgetMedium
	case "low":
		return reasoningBudgetLow
	default:
		return 0
	}
}

func applyReasoningEffort(req map[string]any, effort string) {
	if req == nil || effort == "" {
		return
	}
	if _, has := req["reasoning_effort"]; !has {
		req["reasoning_effort"] = effort
	}
}
