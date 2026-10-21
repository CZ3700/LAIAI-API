package ark

import "strings"

// IsPublicModel identifies the canonical gateway contract. Deployment IDs are
// configuration, never additional request aliases.
func IsPublicModel(model string) bool {
	return model == Model20 || model == Model25 || Is20Variant(model)
}

func Is20Variant(model string) bool { return model == Model20Fast || model == Model20Mini }

// ValidUpstreamModel preserves the standard models' six-digit deployment
// grammar. Each variant is pinned to the deployment independently reviewed in
// its official evidence profile; a future release requires another review.
func ValidUpstreamModel(model, upstream string) bool {
	switch model {
	case Model20Fast:
		return upstream == "doubao-seedance-2-0-fast-260128"
	case Model20Mini:
		return upstream == "doubao-seedance-2-0-mini-260615"
	case Model20, Model25:
		prefix := "doubao-seedance-2-0-"
		if model == Model25 {
			prefix = "doubao-seedance-2-5-"
		}
		if !strings.HasPrefix(upstream, prefix) || len(upstream) != len(prefix)+6 {
			return false
		}
		for _, c := range strings.TrimPrefix(upstream, prefix) {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// A variant cannot acquire unsupported parameters from a malformed or copied
// capability record, even if that record carries enabled/verified flags.
func valid20VariantCapability(cap Capability) bool {
	if !ValidUpstreamModel(cap.PublicModel, cap.UpstreamModel) || cap.FPS != 24 ||
		cap.MaxContentItems > 16 || cap.MaxImages > 9 || cap.MaxVideos > 3 || cap.MaxAudios > 3 ||
		cap.Frames || cap.Draft || cap.DraftConversion || cap.DefaultDraft ||
		len(cap.OmniReferenceTaskTypes) != 0 || len(cap.OutputFormats) != 0 {
		return false
	}
	for _, resolution := range cap.Resolutions {
		if resolution != "480p" && resolution != "720p" {
			return false
		}
	}
	if len(cap.Ratios) == 0 || len(cap.Ratios) > 6 {
		return false
	}
	seenRatios := map[string]bool{}
	for _, ratio := range cap.Ratios {
		if !contains([]string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9"}, ratio) || seenRatios[ratio] {
			return false
		}
		seenRatios[ratio] = true
	}
	return true
}
