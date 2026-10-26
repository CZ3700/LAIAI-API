package video

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
)

type PublicationCell struct {
	Mode, Resolution, ServiceTier string
	InputVideo, Draft             bool
}

func (c PublicationCell) Key() string {
	return fmt.Sprintf("%s/%s/%t/%s/%t", c.Mode, c.Resolution, c.InputVideo, c.ServiceTier, c.Draft)
}

type PublicationCapability struct {
	valid                   bool
	cap                     ark.Capability
	bounds                  map[string]Dimensions
	maxInputVideoDurationMS int64
}

func IsOfficialPublicModel(model string) bool { return ark.IsPublicModel(model) }

func PublicationChannelUsable(channel relay.VideoCatalogChannel) bool {
	return channel.ID != 0 && channel.PlatformOwned && channel.Enabled && channel.OfficialArkEndpoint && (channel.Provider == "volcengine" || channel.Provider == "modelark")
}

func publicationContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func publicationEnum(values, allowed []string) bool {
	if len(values) == 0 || len(values) > len(allowed) {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] || !publicationContains(allowed, value) {
			return false
		}
		seen[value] = true
	}
	return true
}

// ValidatePublicationCapability is shared with administration; it performs no
// IO and uses the existing normalization rules for legal default requests.
func ValidatePublicationCapability(cfg ModelConfig, cap ark.Capability, bounds map[string]Dimensions) error {
	maxDuration := 15
	if cfg.PublicModel == ark.Model25 {
		maxDuration = 30
	}
	if !IsOfficialPublicModel(cfg.PublicModel) || cfg.PublicModel != cap.PublicModel || cfg.Version == "" || cfg.PricingVersion == "" || len(cfg.Version) > 64 || cap.Version == "" || len(cap.Version) > 64 || len(cfg.PricingVersion) > 64 || !ark.ValidUpstreamModel(cfg.PublicModel, cap.UpstreamModel) || cap.MinDuration < 4 || cap.MaxDuration > maxDuration || cap.MaxDuration < cap.MinDuration || cap.FPS <= 0 || cap.FPS > 120 || !publicationContains(cap.Resolutions, cap.DefaultResolution) || !publicationContains(cap.Ratios, cap.DefaultRatio) || cfg.MaxInputVideoDurationMS < 0 {
		return ErrInvalid
	}
	if !publicationEnum(cap.Resolutions, []string{"480p", "720p", "1080p", "4k"}) || !publicationEnum(cap.Ratios, []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive"}) || len(cap.OmniReferenceTaskTypes) > 4 || len(cap.OutputFormats) > 2 || len(bounds) > 28 {
		return ErrInvalid
	}
	if cap.Frames && (cap.MinFrames <= 0 || cap.MaxFrames < cap.MinFrames || cap.FrameStep <= 0) {
		return ErrInvalid
	}
	if ark.Is20Variant(cfg.PublicModel) {
		profile, err := arkverification.Load(cfg.PublicModel)
		if err != nil || cfg.MaxInputVideoDurationMS > profile.MaxInputVideoDurationMS || cap.VideoInput && cfg.MaxInputVideoDurationMS != profile.MaxInputVideoDurationMS {
			return ErrInvalid
		}
		for _, resolution := range cap.Resolutions {
			for _, ratio := range cap.Ratios {
				d, ok := profile.Bounds[resolution+"/"+ratio]
				got, exists := bounds[resolution+"/"+ratio]
				if !ok || !exists || got.Width != d.Width || got.Height != d.Height {
					return ErrInvalid
				}
			}
		}
	}
	for _, resolution := range cap.Resolutions {
		if cfg.PublicModel == ark.Model25 && resolution == "4k" {
			return ErrInvalid
		}
		for _, ratio := range cap.Ratios {
			d, ok := bounds[resolution+"/"+ratio]
			if !ok || d.Width <= 0 || d.Height <= 0 || d.Width > 16384 || d.Height > 16384 {
				return ErrInvalid
			}
		}
	}
	seen := map[string]bool{}
	for _, status := range strings.Split(cfg.NoChargeStatuses, ",") {
		if status == "" {
			continue
		}
		if seen[status] || (status != "failed" && status != "cancelled" && status != "expired") {
			return ErrInvalid
		}
		seen[status] = true
	}
	_, err := ark.Normalize(ark.CreateRequest{Model: cfg.PublicModel, Content: []ark.ContentItem{{Type: "text", Text: "capability validation"}}}, cap, ark.Bindings{})
	return err
}

func ParsePublicationCapability(cfg ModelConfig) (PublicationCapability, error) {
	var out PublicationCapability
	if len(cfg.CapabilityJSON) > 65536 || len(cfg.BoundsJSON) > 65536 {
		return out, ErrCatalogLimit
	}
	if json.Unmarshal([]byte(cfg.CapabilityJSON), &out.cap) != nil || json.Unmarshal([]byte(cfg.BoundsJSON), &out.bounds) != nil {
		return PublicationCapability{}, ErrInvalid
	}
	// Disabled/unverified are separate publication facts. Validate the remaining
	// structure with a local copy so their diagnostic capabilities remain visible.
	probe := out.cap
	probe.Enabled, probe.Verified = true, true
	if err := ValidatePublicationCapability(cfg, probe, out.bounds); err != nil {
		return PublicationCapability{}, ErrInvalid
	}
	out.valid, out.maxInputVideoDurationMS = true, cfg.MaxInputVideoDurationMS
	return out, nil
}

func PublicationCellSupported(cell PublicationCell, cap ark.Capability) bool {
	if !cap.Enabled || !cap.Verified || !publicationContains(cap.Resolutions, cell.Resolution) || cell.ServiceTier != "default" || cell.InputVideo && !cap.VideoInput {
		return false
	}
	if cell.Draft {
		return cap.Draft && cap.PublicModel == ark.Model25 && cell.Resolution == "480p" && cell.Mode == "generate"
	}
	switch cell.Mode {
	case "generate":
		return true
	case "edit", "extend":
		return cell.InputVideo && publicationContains(cap.OmniReferenceTaskTypes, cell.Mode)
	case "draft_conversion":
		return cap.DraftConversion && cell.Resolution == "1080p"
	default:
		return false
	}
}

func (p PublicationCapability) Supports(cell PublicationCell) bool {
	if !p.valid {
		return false
	}
	cap := p.cap
	cap.Enabled, cap.Verified = true, true
	return PublicationCellSupported(cell, cap)
}

func (p PublicationCapability) Cells() []PublicationCell {
	out := []PublicationCell{}
	if !p.valid {
		return out
	}
	for _, mode := range []string{"generate", "edit", "extend", "draft_conversion"} {
		for _, resolution := range p.cap.Resolutions {
			for _, input := range []bool{false, true} {
				for _, draft := range []bool{false, true} {
					cell := PublicationCell{Mode: mode, Resolution: resolution, ServiceTier: "default", InputVideo: input, Draft: draft}
					if p.Supports(cell) {
						out = append(out, cell)
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func (p PublicationCapability) BillingContext(scope ProviderScope) (videobilling.PublicationContext, error) {
	var out videobilling.PublicationContext
	if !p.valid {
		return out, ErrInvalid
	}
	fx, err := decimal.NewFromString(scope.FXUSDPerCNY)
	if err != nil {
		return out, ErrInvalid
	}
	out = videobilling.PublicationContext{CapabilityVersion: p.cap.Version, Ratios: append([]string{}, p.cap.Ratios...), MinDuration: p.cap.MinDuration, MaxDuration: p.cap.MaxDuration, FPS: int64(p.cap.FPS), Frames: p.cap.Frames, MinFrames: int64(p.cap.MinFrames), MaxFrames: int64(p.cap.MaxFrames), FrameStep: int64(p.cap.FrameStep), MaxInputVideoDurationMS: p.maxInputVideoDurationMS, FXUSDPerCNY: fx, FXVersion: scope.FXVersion, FXObservedAt: scope.FXObservedAt, Bounds: []videobilling.PublicationBound{}}
	for _, resolution := range p.cap.Resolutions {
		for _, ratio := range p.cap.Ratios {
			d := p.bounds[resolution+"/"+ratio]
			out.Bounds = append(out.Bounds, videobilling.PublicationBound{Resolution: resolution, Ratio: ratio, Width: d.Width, Height: d.Height})
		}
	}
	return out, nil
}
