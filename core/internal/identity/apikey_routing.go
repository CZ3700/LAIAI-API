package identity

import (
	"errors"
	"strings"
)

const ModelAccessLegacy = "legacy"

var (
	ErrModelAccessMode      = errors.New("invalid_model_access_mode")
	ErrRoutingGroup         = errors.New("invalid_routing_group")
	ErrModelAccessLimits    = errors.New("invalid_model_limits")
	ErrGroupRoutingDisabled = errors.New("group_routing_not_enabled")
)

// ValidateKeyRouting distinguishes omitted mode (nil) from explicitly empty.
// Only request omission gets a default. Persisted records never use this helper.
// This must run before entitlement writes, key generation, or DB mutation.
func ValidateKeyRouting(mode *string, group *uint64, modelLimits string) (string, error) {
	m := ModelAccessLegacy
	if mode != nil {
		m = *mode
	}
	switch m {
	case ModelAccessLegacy:
		if group != nil {
			return "", ErrRoutingGroup
		}
		return m, nil
	case "inherit_group", "subset":
		if group == nil || *group == 0 {
			return "", ErrRoutingGroup
		}
		if m == "inherit_group" && strings.TrimSpace(modelLimits) != "" || m == "subset" && strings.TrimSpace(modelLimits) == "" {
			return "", ErrModelAccessLimits
		}
		return "", ErrGroupRoutingDisabled
	default:
		return "", ErrModelAccessMode
	}
}

func (k *APIKey) validLegacyRouting() bool {
	return k != nil && k.ModelAccessMode == ModelAccessLegacy && k.RoutingGroupID == nil && k.PolicyVersion > 0
}
