package ark

import (
	"encoding/json"
	"testing"
)

func variantTestCapability(model string) Capability {
	c := testCapability(model)
	c.UpstreamModel = "doubao-seedance-2-0-fast-260128"
	if model == Model20Mini {
		c.UpstreamModel = "doubao-seedance-2-0-mini-260615"
	}
	c.Resolutions = []string{"480p", "720p"}
	c.Ratios = []string{"16:9", "1:1", "9:16"}
	c.MaxContentItems, c.MaxImages = 16, 9
	c.Frames, c.Draft, c.DraftConversion = false, false, false
	c.OmniReferenceTaskTypes, c.OutputFormats = nil, nil
	return c
}

// Regression for the reported Mini request: 420p is a typo, not a permission failure.
func TestMiniReportedResolution(t *testing.T) {
	in := CreateRequest{}
	if err := json.Unmarshal([]byte(`{"model":"seedance-2.0-mini","content":[{"type":"text","text":"A paper boat on a quiet lake."}],"duration":4,"resolution":"420p","ratio":"16:9","generate_audio":true}`), &in); err != nil {
		t.Fatal(err)
	}
	cap := variantTestCapability(Model20Mini)
	_, err := Normalize(in, cap, Bindings{})
	if e, ok := err.(*Error); !ok || e.Code != "unsupported_capability" || e.Field != "resolution" {
		t.Fatalf("unexpected error: %v", err)
	}
	resolution := "480p"
	in.Resolution = &resolution
	normalized, err := Normalize(in, cap, Bindings{})
	if err != nil || normalized.Parameters.Resolution != "480p" || !normalized.Parameters.GenerateAudio {
		t.Fatalf("corrected request: %v", err)
	}
}

func TestVariantIdentityPinsReviewedDeployments(t *testing.T) {
	for _, tc := range []struct{ model, upstream string }{
		{Model20, "doubao-seedance-2-0-260128"},
		{Model25, "doubao-seedance-2-5-260628"},
		{Model20Fast, "doubao-seedance-2-0-fast-260128"},
		{Model20Mini, "doubao-seedance-2-0-mini-260615"},
	} {
		if !IsPublicModel(tc.model) || !ValidUpstreamModel(tc.model, tc.upstream) {
			t.Fatalf("reviewed identity rejected: %s", tc.model)
		}
		if IsPublicModel(tc.upstream) || ValidUpstreamModel(tc.model, tc.upstream+"0") {
			t.Fatal("deployment spelling admitted as public identity or malformed version")
		}
	}
	for _, model := range []string{Model20Fast, Model20Mini} {
		for _, upstream := range []string{"doubao-seedance-2-0-260128", "doubao-seedance-2-5-260628", "doubao-seedance-2-0-fast-270101", "doubao-seedance-2-0-mini-270101"} {
			if ValidUpstreamModel(model, upstream) {
				t.Fatal("unreviewed variant deployment accepted", model, upstream)
			}
		}
	}
	if ValidUpstreamModel(Model20Fast, "doubao-seedance-2-0-mini-260615") || ValidUpstreamModel(Model20Mini, "doubao-seedance-2-0-fast-260128") || IsPublicModel("seedance-2.5-fast") {
		t.Fatal("variant model identity confused")
	}
}

func TestVariantNormalizedRequestsKeepIndependentIdentity(t *testing.T) {
	for _, model := range []string{Model20Fast, Model20Mini} {
		for _, resolution := range []string{"480p", "720p"} {
			for _, videoInput := range []bool{false, true} {
				t.Run(model+"/"+resolution+"/"+map[bool]string{false: "text", true: "reference"}[videoInput], func(t *testing.T) {
					cap := variantTestCapability(model)
					in := textRequest(model)
					in.Resolution, in.Duration = ptr(resolution), ptr(4)
					in.GenerateAudio, in.ReturnLastFrame = ptr(true), ptr(true)
					in.MaxChargeUSD, in.ExpectedPricingVersion = ptr("1"), ptr("variant-price-version")
					if videoInput {
						in.Content = append(in.Content, ContentItem{Type: "video_url", VideoURL: &MediaReference{URL: "https://media.example/fixture.mp4"}, Role: "reference_video"})
					}
					out, err := Normalize(in, cap, Bindings{})
					if err != nil {
						t.Fatal(err)
					}
					var raw map[string]json.RawMessage
					if err := json.Unmarshal(out.Body, &raw); err != nil {
						t.Fatal(err)
					}
					var upstream string
					_ = json.Unmarshal(raw["model"], &upstream)
					if upstream != cap.UpstreamModel || out.PublicModel != model || out.HasInputVideo != videoInput || out.Parameters.Resolution != resolution || out.Parameters.FramesPerSecond != 24 {
						t.Fatal("request lost variant identity, media or metering context")
					}
					if _, ok := raw["max_charge_usd"]; ok {
						t.Fatal("platform maximum leaked upstream")
					}
					if _, ok := raw["expected_pricing_version"]; ok {
						t.Fatal("platform pricing version leaked upstream")
					}
				})
			}
		}
	}
}

func TestVariantRejectsUnverifiedCapabilitiesAndRequests(t *testing.T) {
	for _, model := range []string{Model20Fast, Model20Mini} {
		for _, tc := range []struct {
			name   string
			change func(*Capability, *CreateRequest)
		}{
			{"future deployment", func(c *Capability, _ *CreateRequest) { c.UpstreamModel += "0" }},
			{"wrong standard deployment", func(c *Capability, _ *CreateRequest) { c.UpstreamModel = "doubao-seedance-2-0-260128" }},
			{"1080p configuration", func(c *Capability, _ *CreateRequest) { c.Resolutions = append(c.Resolutions, "1080p") }},
			{"adaptive ratio configuration", func(c *Capability, _ *CreateRequest) { c.Ratios = append(c.Ratios, "adaptive") }},
			{"unknown ratio configuration", func(c *Capability, _ *CreateRequest) { c.Ratios = append(c.Ratios, "5:4") }},
			{"duplicate ratio configuration", func(c *Capability, _ *CreateRequest) { c.Ratios = append(c.Ratios, c.Ratios[0]) }},
			{"empty ratio configuration", func(c *Capability, _ *CreateRequest) { c.Ratios = nil }},
			{"fps", func(c *Capability, _ *CreateRequest) { c.FPS = 30 }},
			{"frames configuration", func(c *Capability, _ *CreateRequest) { c.Frames = true }},
			{"draft configuration", func(c *Capability, _ *CreateRequest) { c.Draft = true }},
			{"conversion configuration", func(c *Capability, _ *CreateRequest) { c.DraftConversion = true }},
			{"edit configuration", func(c *Capability, _ *CreateRequest) { c.OmniReferenceTaskTypes = []string{"edit"} }},
			{"format configuration", func(c *Capability, _ *CreateRequest) { c.OutputFormats = []string{"mov"} }},
			{"image limits", func(c *Capability, _ *CreateRequest) { c.MaxImages = 30 }},
			{"video limits", func(c *Capability, _ *CreateRequest) { c.MaxVideos = 10 }},
			{"audio limits", func(c *Capability, _ *CreateRequest) { c.MaxAudios = 10 }},
			{"content limits", func(c *Capability, _ *CreateRequest) { c.MaxContentItems = 51 }},
			{"duration configuration", func(c *Capability, _ *CreateRequest) { c.MaxDuration = 30 }},
			{"not enabled", func(c *Capability, _ *CreateRequest) { c.Enabled = false }},
			{"not verified", func(c *Capability, _ *CreateRequest) { c.Verified = false }},
			{"1080p request", func(_ *Capability, r *CreateRequest) { r.Resolution = ptr("1080p") }},
			{"4k request", func(_ *Capability, r *CreateRequest) { r.Resolution = ptr("4k") }},
			{"adaptive ratio request", func(_ *Capability, r *CreateRequest) { r.Ratio = ptr("adaptive") }},
			{"short duration", func(_ *Capability, r *CreateRequest) { r.Duration = ptr(3) }},
			{"long duration", func(_ *Capability, r *CreateRequest) { r.Duration = ptr(16) }},
			{"frames request", func(_ *Capability, r *CreateRequest) { r.Frames = ptr(121) }},
			{"draft request", func(_ *Capability, r *CreateRequest) { r.Draft = ptr(true) }},
			{"edit request", func(_ *Capability, r *CreateRequest) { r.OmniReferenceTaskType = ptr("edit") }},
			{"format request", func(_ *Capability, r *CreateRequest) { r.OutputFormat = ptr("mov") }},
			{"deployment as public model", func(c *Capability, r *CreateRequest) { r.Model = c.UpstreamModel; c.PublicModel = r.Model }},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				cap, in := variantTestCapability(model), textRequest(model)
				tc.change(&cap, &in)
				if out, err := Normalize(in, cap, Bindings{}); err == nil || len(out.Body) != 0 {
					t.Fatal("unverified variant capability admitted")
				}
			})
		}
	}
}
