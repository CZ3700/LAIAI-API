package routinggroup

import "strings"

// LegacyModelAllowed preserves the legacy CSV predicate. It neither validates
// model names nor authenticates a subject or grants an action/channel permit.
func LegacyModelAllowed(modelLimits, model string) bool {
	if strings.TrimSpace(modelLimits) == "" {
		return true
	}
	for _, entry := range strings.Split(modelLimits, ",") {
		if strings.TrimSpace(entry) == model {
			return true
		}
	}
	return false
}
