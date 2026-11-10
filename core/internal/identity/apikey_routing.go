package identity

import (
	"errors"
	"strings"
)

const (
	ModelAccessLegacy       = "legacy"
	ModelAccessInheritGroup = "inherit_group"
)

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
	case ModelAccessInheritGroup, "subset":
		if group == nil || *group == 0 {
			return "", ErrRoutingGroup
		}
		if m == ModelAccessInheritGroup && strings.TrimSpace(modelLimits) != "" || m == "subset" && strings.TrimSpace(modelLimits) == "" {
			return "", ErrModelAccessLimits
		}
		if m == "subset" {
			return "", ErrGroupRoutingDisabled
		}
		return m, nil
	default:
		return "", ErrModelAccessMode
	}
}

func (k *APIKey) validLegacyRouting() bool {
	return k != nil && k.ModelAccessMode == ModelAccessLegacy && k.RoutingGroupID == nil && k.PolicyVersion > 0
}

func (k *APIKey) validGroupedRouting() bool {
	return k != nil && k.ModelAccessMode == ModelAccessInheritGroup && k.RoutingGroupID != nil && *k.RoutingGroupID > 0 && strings.TrimSpace(k.ModelLimits) == "" && k.PolicyVersion > 0
}
