package video

import (
	"encoding/json"

	"github.com/laiai/laiai-next/core/internal/videobilling"
)

// RequestMetering is a public, numerical projection of creation-time estimates.
// Never marshal MeteringInput directly: it also contains internal FX and hashes.
type RequestMetering struct {
	Source              string `json:"source"`
	HasInputVideo       bool   `json:"has_input_video"`
	InputVideoCount     *int64 `json:"input_video_count"`
	InputDurationMS     int64  `json:"input_duration_ms"`
	InputDurationSource string `json:"input_duration_source"`
	OutputDurationMS    int64  `json:"output_duration_ms"`
	OutputFrames        int64  `json:"output_frames"`
	Width               int64  `json:"width"`
	Height              int64  `json:"height"`
	FPSNumerator        int64  `json:"fps_numerator"`
	FPSDenominator      int64  `json:"fps_denominator"`
	Resolution          string `json:"resolution"`
	Ratio               string `json:"ratio"`
	Mode                string `json:"mode"`
	ServiceTier         string `json:"service_tier"`
	Draft               bool   `json:"draft"`
	CapabilityVersion   string `json:"capability_version"`
	MinimumRuleVersion  string `json:"minimum_rule_version"`
	MinimumTokens       *int64 `json:"minimum_tokens"`
}

func requestMetering(snap videobilling.Snapshot, parametersJSON string) *RequestMetering {
	// Optional provenance fields are absent in older snapshots; unknown is
	// explicit instead of guessing that an upper bound was a measured duration.
	var meter struct {
		InputVideo                                                                                   bool
		InputVideoCount                                                                              *int64
		InputDurationSource                                                                          string
		InputDurationMS, OutputDurationMS, OutputFrames, Width, Height, FPSNumerator, FPSDenominator int64
		ParentDraftTaskID                                                                            string
		MinimumTokens                                                                                int64
	}
	if json.Unmarshal([]byte(snap.MeteringJSON), &meter) != nil {
		return nil
	}
	v := &RequestMetering{Source: "creation_estimate", HasInputVideo: meter.InputVideo, InputVideoCount: meter.InputVideoCount, InputDurationMS: meter.InputDurationMS, OutputDurationMS: meter.OutputDurationMS, OutputFrames: meter.OutputFrames, Width: meter.Width, Height: meter.Height, FPSNumerator: meter.FPSNumerator, FPSDenominator: meter.FPSDenominator, InputDurationSource: "unknown", CapabilityVersion: snap.CapabilityVersion, MinimumRuleVersion: snap.MinimumRuleVersion, Mode: "unknown"}
	if !meter.InputVideo {
		zero := int64(0)
		v.InputDurationSource, v.InputVideoCount = "none", &zero
	} else {
		switch meter.InputDurationSource {
		case "verified_media", "configured_upper_bound", "draft_inherited":
			v.InputDurationSource = meter.InputDurationSource
		}
	}
	if v.InputVideoCount != nil && *v.InputVideoCount < 0 {
		v.InputVideoCount = nil
	}
	if meter.MinimumTokens > 0 {
		v.MinimumTokens = &meter.MinimumTokens
	}
	var parameters struct {
		Resolution, Ratio, ServiceTier, OmniReferenceTaskType string
		Draft                                                 bool
	}
	if json.Unmarshal([]byte(parametersJSON), &parameters) == nil {
		v.Mode = "generate"
		v.Resolution, v.Ratio, v.ServiceTier, v.Draft = parameters.Resolution, parameters.Ratio, parameters.ServiceTier, parameters.Draft
		if parameters.OmniReferenceTaskType == "edit" || parameters.OmniReferenceTaskType == "extend" {
			v.Mode = parameters.OmniReferenceTaskType
		}
	}
	if meter.ParentDraftTaskID != "" {
		v.Mode = "draft_conversion"
	}
	return v
}
