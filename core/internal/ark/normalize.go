package ark

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	platformAsset = regexp.MustCompile(`^asset_[A-Za-z0-9_-]+$`)
	platformTask  = regexp.MustCompile(`^task_[A-Za-z0-9_-]+$`)
	upstreamID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)
	money         = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,8})?$`)
	directive     = regexp.MustCompile(`--\s*(dur(?:ation)?|rs|resolution|frames?|fps|ratio|ar|seed|generate[_-]?audio|draft|service[_-]?tier|execution[_-]?expires[_-]?after)\b`)
)

// Normalize validates the platform request against explicitly verified
// capabilities, resolves trusted bindings and emits canonical official JSON.
// It never guesses a price, fetches media, or authorizes a resource reference.
func Normalize(in CreateRequest, cap Capability, bindings Bindings) (NormalizedRequest, error) {
	var out NormalizedRequest
	bad := func(field, msg string) (NormalizedRequest, error) {
		return out, failure("invalid_request", msg, field, 422)
	}
	unsupported := func(field string) (NormalizedRequest, error) {
		return out, failure("unsupported_capability", "capability is not enabled and verified", field, 422)
	}
	if (in.Model != Model20 && in.Model != Model25) || cap.PublicModel != in.Model {
		return unsupported("model")
	}
	if !cap.Enabled || !cap.Verified || cap.Version == "" || !upstreamID.MatchString(cap.UpstreamModel) {
		return unsupported("model")
	}
	maxDuration := 15
	if in.Model == Model25 {
		maxDuration = 30
	}
	if cap.MinDuration < 4 || cap.MaxDuration < cap.MinDuration || cap.MaxDuration > maxDuration || cap.FPS <= 0 || cap.MaxContentItems < 1 || cap.MaxContentItems > 51 {
		return unsupported("model")
	}
	if len(in.Content) == 0 || len(in.Content) > cap.MaxContentItems {
		return bad("content", "content item count is outside the verified limit")
	}
	if in.Duration != nil && in.Frames != nil {
		return bad("frames", "frames and duration are mutually exclusive")
	}
	if in.MaxChargeUSD != nil && (len(*in.MaxChargeUSD) > 29 || !money.MatchString(*in.MaxChargeUSD)) {
		return bad("max_charge_usd", "amount must be a nonnegative decimal string with at most 8 decimals")
	}
	if in.ExpectedPricingVersion != nil && (len(*in.ExpectedPricingVersion) == 0 || len(*in.ExpectedPricingVersion) > 128) {
		return bad("expected_pricing_version", "invalid pricing version")
	}
	p := EffectiveParameters{Resolution: cap.DefaultResolution, Ratio: cap.DefaultRatio, Duration: cap.DefaultDuration,
		FramesPerSecond: cap.FPS, Seed: cap.DefaultSeed, GenerateAudio: cap.DefaultGenerateAudio, Watermark: cap.DefaultWatermark,
		ReturnLastFrame: cap.DefaultReturnLastFrame, Draft: cap.DefaultDraft, ServiceTier: "default", ExecutionExpiresAfter: cap.DefaultExecutionExpiresAfter}
	if in.Resolution != nil {
		p.Resolution = *in.Resolution
	}
	if in.Ratio != nil {
		p.Ratio = *in.Ratio
	}
	if in.Duration != nil {
		p.Duration = *in.Duration
	}
	if in.Frames != nil {
		p.Frames = *in.Frames
		p.Duration = 0
	}
	if in.Seed != nil {
		p.Seed = *in.Seed
	}
	if in.GenerateAudio != nil {
		p.GenerateAudio = *in.GenerateAudio
	}
	if in.Watermark != nil {
		p.Watermark = *in.Watermark
	}
	if in.ReturnLastFrame != nil {
		p.ReturnLastFrame = *in.ReturnLastFrame
	}
	if in.Draft != nil {
		p.Draft = *in.Draft
	}
	if in.ServiceTier != nil {
		p.ServiceTier = *in.ServiceTier
	}
	if in.ExecutionExpiresAfter != nil {
		p.ExecutionExpiresAfter = *in.ExecutionExpiresAfter
	}
	if in.OmniReferenceTaskType != nil {
		p.OmniReferenceTaskType = *in.OmniReferenceTaskType
	}
	if in.OutputFormat != nil {
		p.OutputFormat = *in.OutputFormat
	}
	if !contains([]string{"480p", "720p", "1080p", "4k"}, p.Resolution) || !contains(cap.Resolutions, p.Resolution) || (in.Model == Model25 && p.Resolution == "4k") {
		return unsupported("resolution")
	}
	if !contains([]string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive"}, p.Ratio) || !contains(cap.Ratios, p.Ratio) {
		return unsupported("ratio")
	}
	if p.Seed < -1 || p.Seed > 2147483647 {
		return bad("seed", "seed is outside the supported range")
	}
	if p.ServiceTier != "default" {
		return unsupported("service_tier")
	}
	if p.GenerateAudio && !cap.GenerateAudio {
		return unsupported("generate_audio")
	}
	if p.ReturnLastFrame && !cap.ReturnLastFrame {
		return unsupported("return_last_frame")
	}
	if p.Draft && !cap.Draft {
		return unsupported("draft")
	}
	if p.ExecutionExpiresAfter <= 0 || p.ExecutionExpiresAfter < cap.MinExecutionExpiresAfter || p.ExecutionExpiresAfter > cap.MaxExecutionExpiresAfter {
		return unsupported("execution_expires_after")
	}
	if in.Frames != nil {
		if !cap.Frames || cap.MinFrames <= 0 || cap.FrameStep <= 0 || p.Frames < cap.MinFrames || p.Frames > cap.MaxFrames || (p.Frames-cap.MinFrames)%cap.FrameStep != 0 {
			return unsupported("frames")
		}
	} else if p.Duration == -1 {
		if !cap.AutomaticDuration {
			return unsupported("duration")
		}
	} else if p.Duration < cap.MinDuration || p.Duration > cap.MaxDuration {
		return bad("duration", "duration is outside the verified range")
	}
	if in.OmniReferenceTaskType != nil && (in.Model != Model25 || !contains([]string{"auto", "reference", "edit", "extend"}, p.OmniReferenceTaskType) || !contains(cap.OmniReferenceTaskTypes, p.OmniReferenceTaskType)) {
		return unsupported("omni_reference_task_type")
	}
	if in.OutputFormat != nil && (in.Model != Model25 || !contains([]string{"mp4", "mov"}, p.OutputFormat) || !contains(cap.OutputFormats, p.OutputFormat)) {
		return unsupported("output_format")
	}
	var content []ContentItem
	images, videos, audios, first, last, drafts := 0, 0, 0, 0, 0, 0
	for _, item := range in.Content {
		n, shapeOK := contentShape(item)
		if !shapeOK {
			return bad("content", "content type, fields, or role are inconsistent")
		}
		if item.Type == "text" {
			if strings.TrimSpace(item.Text) == "" || len([]rune(item.Text)) > 20000 || hasDirective(item.Text) {
				return bad("content", "text is empty, too long, or contains parameter directives")
			}
		}
		if item.Type == "draft_task" {
			drafts++
			if !cap.DraftConversion || in.Model != Model25 || !platformTask.MatchString(item.DraftTask.ID) {
				return unsupported("content")
			}
			b, ok := bindings.Drafts[item.DraftTask.ID]
			if !ok || b.PublicModel != in.Model || b.CapabilityVersion == "" || !upstreamID.MatchString(b.UpstreamTaskID) {
				return bad("content", "draft reference has not been verified")
			}
			out.SourceDraftID = item.DraftTask.ID
			n.DraftTask = &DraftReference{ID: b.UpstreamTaskID}
			out.HasInputVideo = b.HasInputVideo
			// Inherited billing context is intentionally separate from outbound JSON.
			p.Duration, p.Frames, p.FramesPerSecond = b.Parameters.Duration, b.Parameters.Frames, b.Parameters.FramesPerSecond
			p.Ratio, p.Seed, p.GenerateAudio, p.OmniReferenceTaskType = b.Parameters.Ratio, b.Parameters.Seed, b.Parameters.GenerateAudio, b.Parameters.OmniReferenceTaskType
		}
		var ref *MediaReference
		mediaType := ""
		switch item.Type {
		case "image_url":
			images++
			ref = n.ImageURL
			mediaType = "image"
			if !cap.ImageInput {
				return unsupported("content")
			}
			if item.Role == "first_frame" {
				first++
			}
			if item.Role == "last_frame" {
				last++
			}
		case "video_url":
			videos++
			ref = n.VideoURL
			mediaType = "video"
			out.HasInputVideo = true
			if !cap.VideoInput {
				return unsupported("content")
			}
		case "audio_url":
			audios++
			ref = n.AudioURL
			mediaType = "audio"
			if !cap.AudioInput {
				return unsupported("content")
			}
		}
		if ref != nil {
			if strings.HasPrefix(ref.URL, "asset://") {
				id := strings.TrimPrefix(ref.URL, "asset://")
				b, ok := bindings.Assets[id]
				if !platformAsset.MatchString(id) || !ok || b.MediaType != mediaType || !validUpstreamAsset(b.UpstreamURI) {
					return bad("content", "asset reference has not been verified")
				}
				ref.URL = b.UpstreamURI
				out.AssetIDs = append(out.AssetIDs, id)
			} else if !validMediaURL(ref.URL) {
				return bad("content", "media URL must use HTTPS without user credentials")
			}
		}
		content = append(content, n)
	}
	if images > cap.MaxImages || videos > cap.MaxVideos || audios > cap.MaxAudios {
		return bad("content", "media count exceeds the verified limit")
	}
	if first > 1 || last > 1 || (last > 0 && first == 0) || ((first+last) > 0 && !cap.ImageFrames) {
		return bad("content", "invalid frame reference combination")
	}
	if (first+last) > 0 && (images != first+last || videos > 0 || audios > 0 || p.Ratio != "adaptive") {
		return bad("content", "frame mode requires adaptive ratio and no reference media")
	}
	if drafts == 0 && p.OmniReferenceTaskType == "edit" && (videos != 1 || p.Ratio != "adaptive" || p.Duration != -1) {
		return bad("omni_reference_task_type", "edit mode requires one video, adaptive ratio and duration -1")
	}
	if drafts == 0 && p.OmniReferenceTaskType == "extend" && (videos != 1 || p.Ratio != "adaptive") {
		return bad("omni_reference_task_type", "extend mode requires one video and adaptive ratio")
	}
	if drafts > 0 && (drafts != 1 || len(content) != 1 || in.Resolution == nil || *in.Resolution != "1080p" || in.Duration != nil || in.Frames != nil || in.Ratio != nil || in.Seed != nil || in.GenerateAudio != nil || in.OmniReferenceTaskType != nil || p.Draft) {
		return bad("content", "draft conversion requires only its parent and resolution 1080p; inherited fields cannot be supplied")
	}
	// Keep the default service tier in billing context, not the optional upstream request field.
	payload := map[string]any{"model": cap.UpstreamModel, "content": content, "resolution": p.Resolution, "watermark": p.Watermark, "return_last_frame": p.ReturnLastFrame, "execution_expires_after": p.ExecutionExpiresAfter}
	if drafts == 0 {
		payload["ratio"] = p.Ratio
		payload["seed"] = p.Seed
		payload["generate_audio"] = p.GenerateAudio
		if cap.Draft {
			payload["draft"] = p.Draft
		}
		if in.Frames != nil {
			payload["frames"] = p.Frames
		} else {
			payload["duration"] = p.Duration
		}
		if p.OmniReferenceTaskType != "" {
			payload["omni_reference_task_type"] = p.OmniReferenceTaskType
		}
	}
	if p.OutputFormat != "" {
		payload["output_format"] = p.OutputFormat
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return bad("body", "request serialization failed")
	}
	out.PublicModel = in.Model
	out.UpstreamModel = cap.UpstreamModel
	out.CapabilityVersion = cap.Version
	out.Body = body
	out.Parameters = p
	out.Platform = PlatformOptions{MaxChargeUSD: in.MaxChargeUSD, ExpectedPricingVersion: in.ExpectedPricingVersion}
	return out, nil
}

func contentShape(in ContentItem) (ContentItem, bool) {
	n := in
	count := 0
	if in.ImageURL != nil {
		v := *in.ImageURL
		n.ImageURL = &v
		count++
	}
	if in.VideoURL != nil {
		v := *in.VideoURL
		n.VideoURL = &v
		count++
	}
	if in.AudioURL != nil {
		v := *in.AudioURL
		n.AudioURL = &v
		count++
	}
	if in.DraftTask != nil {
		v := *in.DraftTask
		n.DraftTask = &v
		count++
	}
	if in.Text != "" {
		count++
	}
	if count != 1 {
		return n, false
	}
	switch in.Type {
	case "text":
		return n, in.Text != "" && in.Role == ""
	case "image_url":
		return n, in.ImageURL != nil && contains([]string{"", "first_frame", "last_frame", "reference_image"}, in.Role)
	case "video_url":
		return n, in.VideoURL != nil && contains([]string{"", "reference_video"}, in.Role)
	case "audio_url":
		return n, in.AudioURL != nil && contains([]string{"", "reference_audio"}, in.Role)
	case "draft_task":
		return n, in.DraftTask != nil && in.Role == ""
	}
	return n, false
}

func hasDirective(s string) bool {
	s = norm.NFKC.String(s)
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if r == '\u2010' || r == '\u2011' || r == '\u2012' || r == '\u2013' || r == '\u2014' || r == '\u2212' {
			return '-'
		}
		return unicode.ToLower(r)
	}, s)
	return directive.MatchString(s)
}
func validMediaURL(s string) bool {
	if len(s) > 4096 || strings.IndexFunc(s, unicode.IsSpace) >= 0 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
func validUpstreamAsset(s string) bool {
	return strings.HasPrefix(s, "asset://") && upstreamID.MatchString(strings.TrimPrefix(s, "asset://"))
}
