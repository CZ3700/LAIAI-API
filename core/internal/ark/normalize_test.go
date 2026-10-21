package ark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// Verified flags below are simulated test configuration, not claims about a
// production account or newly captured official fixtures.
func testCapability(model string) Capability {
	max := 15
	upstream := "doubao-seedance-2-0-260128"
	if model == Model25 {
		max = 30
		upstream = "doubao-seedance-2-5-260628"
	}
	return Capability{PublicModel: model, UpstreamModel: upstream, Version: "test-v1", Enabled: true, Verified: true,
		Resolutions: []string{"480p", "720p", "1080p", "4k"}, Ratios: []string{"16:9", "1:1", "9:16", "adaptive"},
		MinDuration: 4, MaxDuration: max, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1,
		DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24,
		AutomaticDuration: true, Frames: true, MinFrames: 97, MaxFrames: 721, FrameStep: 4,
		MaxContentItems: 51, MaxImages: 50, MaxVideos: 3, MaxAudios: 3, ImageInput: true, VideoInput: true, AudioInput: true, ImageFrames: true,
		GenerateAudio: true, ReturnLastFrame: true, Draft: true, DraftConversion: true,
		OmniReferenceTaskTypes: []string{"auto", "reference", "edit", "extend"}, OutputFormats: []string{"mp4", "mov"}}
}
func textRequest(model string) CreateRequest {
	return CreateRequest{Model: model, Content: []ContentItem{{Type: "text", Text: "A paper boat."}}}
}

func TestDecodeClosedJSON(t *testing.T) {
	for _, body := range []string{
		`{"model":"seedance-2.0","Model":"other","content":[{"type":"text","text":"x"}]}`,
		`{"model":"seedance-2.0","model":"seedance-2.5","content":[{"type":"text","text":"x"}]}`,
		`{"model":"seedance-2.0","content":[{"type":"text","text":"a","text":"b"}]}`,
		`{"model":"seedance-2.0","content":[{"type":"image_url","image_url":{"url":"https://a.example/x","URL":"https://b.example/y"}}]}`,
		`{"model":"seedance-2.0","content":[{"type":"image_url","text":"","image_url":{"url":"https://a.example/x"}}]}`,
		`{"model":"seedance-2.0","content":[{"type":"text","text":"x"}],"duration":null}`,
		`{"model":"seedance-2.0","content":[{"type":"text","text":"x"}],"duration":5.1}`,
		`{"model":"seedance-2.0","content":[{"type":"text","text":"x"}],"duration":"5"}`,
		`{"model":"seedance-2.0","content":[{"type":"text","text":"x"}],"provider_params":{}}`,
		`{"model":"seedance-2.0","content":[{"type":"text","text":"x"}],"callback_url":"https://callback.example"}`,
		`{"model":"seedance-2.0","content":[{"type":"text","text":"x"}]} {}`,
		`null`, `[]`, `{"model":"seedance-2.0","content":null}`,
	} {
		if _, err := Decode(strings.NewReader(body), 0); err == nil {
			t.Errorf("accepted malformed input: %s", body)
		}
	}
	if _, err := Decode(strings.NewReader(strings.Repeat("x", 21)), 20); err == nil || err.(*Error).HTTPStatus != 413 {
		t.Fatalf("body bound: %v", err)
	}
	if _, err := Decode(strings.NewReader(`{"model":"seedance-2.0","content":[{"type":"text","text":"x"}]}`), 0); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeCapabilityAndCanonicalBody(t *testing.T) {
	in := textRequest(Model20)
	in.MaxChargeUSD = ptr("0.25000000")
	in.ExpectedPricingVersion = ptr("price-v1")
	out, err := Normalize(in, testCapability(Model20), Bindings{})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "doubao-seedance-2-0-260128" || body["duration"] != float64(5) || body["resolution"] != "720p" || body["seed"] != float64(-1) {
		t.Fatalf("canonical body: %s", out.Body)
	}
	for _, k := range []string{"max_charge_usd", "expected_pricing_version", "provider_params"} {
		if _, ok := body[k]; ok {
			t.Fatalf("platform field escaped: %s", k)
		}
	}
	if out.Platform.MaxChargeUSD == nil || *out.Platform.MaxChargeUSD != "0.25000000" || out.CapabilityVersion != "test-v1" {
		t.Fatal("lost platform context")
	}
	cap := testCapability(Model20)
	cap.Verified = false
	if _, err := Normalize(in, cap, Bindings{}); err == nil {
		t.Fatal("unverified model accepted")
	}
}

func TestNormalizeDraftRequestContract(t *testing.T) {
	for _, tc := range []struct {
		name       string
		model      string
		capDraft   bool
		request    *bool
		wantDraft  bool
		wantReject bool
	}{
		{name: "2.0 omitted unsupported field", model: Model20},
		{name: "2.0 explicit false stays local", model: Model20, request: ptr(false)},
		{name: "2.0 true rejected", model: Model20, request: ptr(true), wantReject: true},
		{name: "2.5 disabled capability omits field", model: Model25, request: ptr(false)},
		{name: "2.5 verified capability default false", model: Model25, capDraft: true},
		{name: "2.5 verified capability explicit false", model: Model25, capDraft: true, request: ptr(false)},
		{name: "2.5 verified capability true", model: Model25, capDraft: true, request: ptr(true), wantDraft: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := textRequest(tc.model)
			in.Draft = tc.request
			if tc.wantDraft {
				in.Resolution = ptr("480p")
			}
			cap := testCapability(tc.model)
			cap.Draft = tc.capDraft
			out, err := Normalize(in, cap, Bindings{})
			if tc.wantReject {
				apiErr, ok := err.(*Error)
				if !ok || apiErr.Code != "unsupported_capability" || apiErr.Field != "draft" || len(out.Body) != 0 {
					t.Fatalf("unsupported draft must fail before emitting a body: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(out.Body, &body); err != nil {
				t.Fatal(err)
			}
			raw, present := body["draft"]
			if present != tc.capDraft {
				t.Fatalf("draft presence = %t for capability %t: %s", present, tc.capDraft, out.Body)
			}
			if present {
				var actual bool
				if err := json.Unmarshal(raw, &actual); err != nil || actual != tc.wantDraft {
					t.Fatalf("draft value = %s, want %t", raw, tc.wantDraft)
				}
			}
			if out.Parameters.Draft != tc.wantDraft {
				t.Fatalf("effective draft = %t, want %t", out.Parameters.Draft, tc.wantDraft)
			}
		})
	}
}

func TestNormalizeReferenceOnlyTaskType(t *testing.T) {
	for _, tc := range []struct {
		name       string
		model      string
		allowed    []string
		request    *string
		want       string
		wantReject bool
	}{
		{name: "reference-only default is explicit", model: Model25, allowed: []string{"reference"}, want: "reference"},
		{name: "explicit reference retained", model: Model25, allowed: []string{"reference"}, request: ptr("reference"), want: "reference"},
		{name: "explicit auto rejected", model: Model25, allowed: []string{"reference"}, request: ptr("auto"), wantReject: true},
		{name: "explicit edit rejected", model: Model25, allowed: []string{"reference"}, request: ptr("edit"), wantReject: true},
		{name: "explicit extend rejected", model: Model25, allowed: []string{"reference"}, request: ptr("extend"), wantReject: true},
		{name: "legacy empty capabilities omit field", model: Model25},
		{name: "all capabilities preserve implicit auto", model: Model25, allowed: []string{"auto", "reference", "edit", "extend"}},
		{name: "auto capability preserves default", model: Model25, allowed: []string{"reference", "auto"}},
		{name: "edit capability preserves default", model: Model25, allowed: []string{"reference", "edit"}},
		{name: "extend capability preserves default", model: Model25, allowed: []string{"reference", "extend"}},
		{name: "existing explicit auto retained", model: Model25, allowed: []string{"auto", "reference", "edit", "extend"}, request: ptr("auto"), want: "auto"},
		{name: "2.0 does not receive 2.5 field", model: Model20, allowed: []string{"reference"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := textRequest(tc.model)
			// The prompt could otherwise cause upstream's implicit auto mode to
			// choose an editing task outside the configured capability.
			in.Content[0].Text = "Edit the reference video and remove the boat."
			in.Content = append(in.Content, ContentItem{Type: "video_url", Role: "reference_video", VideoURL: &MediaReference{URL: "https://media.example/reference.mp4"}})
			in.OmniReferenceTaskType = tc.request
			cap := testCapability(tc.model)
			cap.OmniReferenceTaskTypes = tc.allowed
			out, err := Normalize(in, cap, Bindings{})
			if tc.wantReject {
				apiErr, ok := err.(*Error)
				if !ok || apiErr.Code != "unsupported_capability" || apiErr.Field != "omni_reference_task_type" || len(out.Body) != 0 {
					t.Fatalf("disabled task type must be rejected before emitting JSON: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.Parameters.OmniReferenceTaskType != tc.want {
				t.Fatalf("effective task type = %q, want %q", out.Parameters.OmniReferenceTaskType, tc.want)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(out.Body, &body); err != nil {
				t.Fatal(err)
			}
			raw, present := body["omni_reference_task_type"]
			if present != (tc.want != "") {
				t.Fatalf("outgoing task type presence = %t, want %t", present, tc.want != "")
			}
			if present {
				var actual string
				if err := json.Unmarshal(raw, &actual); err != nil || actual != tc.want {
					t.Fatalf("outgoing task type = %s, want %q", raw, tc.want)
				}
			}
			if in.OmniReferenceTaskType != tc.request {
				t.Fatal("normalization mutated the caller's task type")
			}
		})
	}
}

func TestSubmitOmitsDefaultServiceTierAndRetainsBillingContext(t *testing.T) {
	for _, model := range []string{Model20, Model25} {
		for _, explicit := range []bool{false, true} {
			name := model + "/omitted"
			if explicit {
				name = model + "/explicit-default"
			}
			t.Run(name, func(t *testing.T) {
				in := textRequest(model)
				if explicit {
					in.ServiceTier = ptr("default")
				}
				normalized, err := Normalize(in, testCapability(model), Bindings{})
				if err != nil {
					t.Fatal(err)
				}
				if normalized.Parameters.ServiceTier != "default" {
					t.Fatal("default service tier was lost from the billing context")
				}
				requests := make(chan map[string]json.RawMessage, 1)
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.Method != http.MethodPost || req.URL.Path != "/api/v3/contents/generations/tasks" {
						t.Error("unexpected submission method or path")
					}
					var body map[string]json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Errorf("invalid outgoing JSON: %v", err)
					}
					requests <- body
					w.Header().Set("Content-Type", "application/json")
					if _, present := body["service_tier"]; present {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"code":"InvalidParameter","message":"service_tier must be omitted"}}`))
						return
					}
					_, _ = w.Write([]byte(`{"id":"cgt-local-contract"}`))
				}))
				defer upstream.Close()
				localURL, err := url.Parse(upstream.URL)
				if err != nil {
					t.Fatal(err)
				}
				transport := upstream.Client().Transport
				client := testClient(func(req *http.Request) (*http.Response, error) {
					// Exercise the built HTTP request over loopback only; never contact Ark.
					local := req.Clone(req.Context())
					local.URL.Scheme, local.URL.Host = localURL.Scheme, localURL.Host
					return transport.RoundTrip(local)
				})
				result := client.Submit(context.Background(), normalized)
				if result.Outcome != OutcomeAccepted || result.TaskID != "cgt-local-contract" {
					t.Fatalf("local contract rejected submission: outcome=%s status=%d error=%v", result.Outcome, result.HTTPStatus, result.Error)
				}
				if _, present := (<-requests)["service_tier"]; present {
					t.Fatal("service_tier escaped into the outgoing HTTP request")
				}
				in.ServiceTier = ptr("flex")
				if _, err := Normalize(in, testCapability(model), Bindings{}); err == nil {
					t.Fatal("nondefault pricing tier was accepted")
				}
			})
		}
	}
}

func TestNormalizeRejectsUnverifiedAndInvalidParameters(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*CreateRequest, *Capability)
	}{
		{"frames-duration", func(r *CreateRequest, c *Capability) { r.Frames = ptr(121); r.Duration = ptr(5) }},
		{"duration-short", func(r *CreateRequest, c *Capability) { r.Duration = ptr(3) }},
		{"20-duration-long", func(r *CreateRequest, c *Capability) { r.Duration = ptr(16) }},
		{"invalid-frame-step", func(r *CreateRequest, c *Capability) { r.Frames = ptr(122) }},
		{"frames-disabled", func(r *CreateRequest, c *Capability) { r.Frames = ptr(121); c.Frames = false }},
		{"adaptive-disabled", func(r *CreateRequest, c *Capability) { r.Duration = ptr(-1); c.AutomaticDuration = false }},
		{"seed-overflow", func(r *CreateRequest, c *Capability) { r.Seed = ptr(int64(2147483648)) }},
		{"flex", func(r *CreateRequest, c *Capability) { r.ServiceTier = ptr("flex") }},
		{"audio-disabled", func(r *CreateRequest, c *Capability) { r.GenerateAudio = ptr(true); c.GenerateAudio = false }},
		{"last-frame-disabled", func(r *CreateRequest, c *Capability) { r.ReturnLastFrame = ptr(true); c.ReturnLastFrame = false }},
		{"draft-disabled", func(r *CreateRequest, c *Capability) { r.Draft = ptr(true); c.Draft = false }},
		{"20-format", func(r *CreateRequest, c *Capability) { r.OutputFormat = ptr("mov") }},
		{"20-edit", func(r *CreateRequest, c *Capability) { r.OmniReferenceTaskType = ptr("edit") }},
		{"bad-money", func(r *CreateRequest, c *Capability) { r.MaxChargeUSD = ptr("0.000000001") }},
		{"expiry-unverified", func(r *CreateRequest, c *Capability) { r.ExecutionExpiresAfter = ptr(1) }},
		{"empty-ratio", func(r *CreateRequest, c *Capability) { r.Ratio = ptr("") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := textRequest(Model20)
			c := testCapability(Model20)
			tc.alter(&r, &c)
			if _, err := Normalize(r, c, Bindings{}); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	r := textRequest(Model25)
	r.Duration = ptr(30)
	r.OutputFormat = ptr("mov")
	if _, err := Normalize(r, testCapability(Model25), Bindings{}); err != nil {
		t.Fatal(err)
	}
	r.Resolution = ptr("4k")
	if _, err := Normalize(r, testCapability(Model25), Bindings{}); err == nil {
		t.Fatal("2.5 4k accepted")
	}
}

func TestDirectiveVariantsAndHarmlessURL(t *testing.T) {
	for _, text := range []string{"--duration 30", "--DUR=30", "－－ｒｓ 4k", "—-frames 721", "--\u200bduration 30", "-- resolution 4k"} {
		r := textRequest(Model20)
		r.Content[0].Text = text
		if _, err := Normalize(r, testCapability(Model20), Bindings{}); err == nil {
			t.Errorf("directive accepted: %q", text)
		}
	}
	r := textRequest(Model20)
	r.Content = append(r.Content, ContentItem{Type: "image_url", ImageURL: &MediaReference{URL: "https://media.example/a--resolution.jpg"}, Role: "reference_image"})
	if _, err := Normalize(r, testCapability(Model20), Bindings{}); err != nil {
		t.Fatal(err)
	}
}

func TestMediaBindingsAndNoInputMutation(t *testing.T) {
	r := textRequest(Model25)
	r.Content = append(r.Content, ContentItem{Type: "video_url", VideoURL: &MediaReference{URL: "asset://asset_test"}, Role: "reference_video"})
	bindings := Bindings{Assets: map[string]AssetBinding{"asset_test": {MediaType: "video", UpstreamURI: "asset://upstream-test"}}}
	out, err := Normalize(r, testCapability(Model25), bindings)
	if err != nil {
		t.Fatal(err)
	}
	if !out.HasInputVideo || len(out.AssetIDs) != 1 || !strings.Contains(string(out.Body), "asset://upstream-test") {
		t.Fatalf("lost mapping: %+v", out)
	}
	if r.Content[1].VideoURL.URL != "asset://asset_test" {
		t.Fatal("mutated caller reference")
	}
	for _, u := range []string{"asset://official-id", "https://user:secret@media.example/a", "http://media.example/a", "data:video/mp4;base64,abc"} {
		r.Content[1].VideoURL.URL = u
		if _, err := Normalize(r, testCapability(Model25), bindings); err == nil {
			t.Errorf("invalid media accepted: %q", u)
		}
	}
}

func TestDraftConversionDoesNotReinjectInheritedParameters(t *testing.T) {
	r := CreateRequest{Model: Model25, Content: []ContentItem{{Type: "draft_task", DraftTask: &DraftReference{ID: "task_parent"}}}, Resolution: ptr("1080p"), OutputFormat: ptr("mov")}
	b := Bindings{Drafts: map[string]DraftBinding{"task_parent": {UpstreamTaskID: "cgt-parent", PublicModel: Model25, CapabilityVersion: "test-v1", HasInputVideo: true, Parameters: EffectiveParameters{Duration: 12, Ratio: "adaptive", Seed: 42, GenerateAudio: true, OmniReferenceTaskType: "edit", FramesPerSecond: 24}}}}
	for _, draft := range []*bool{nil, ptr(false)} {
		r.Draft = draft
		out, err := Normalize(r, testCapability(Model25), b)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out.Body, &payload); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"duration", "frames", "ratio", "seed", "generate_audio", "omni_reference_task_type", "draft"} {
			if _, ok := payload[key]; ok {
				t.Errorf("inherited field in JSON: %s", key)
			}
		}
		if out.Parameters.Draft || out.Parameters.Duration != 12 || !out.HasInputVideo || out.SourceDraftID != "task_parent" {
			t.Fatal("lost inherited billing context or marked converted video as draft")
		}
	}
	r.Draft = ptr(true)
	if _, err := Normalize(r, testCapability(Model25), b); err == nil {
		t.Fatal("draft conversion accepts draft mode")
	}
	r.Draft = nil
	r.Duration = ptr(12)
	if _, err := Normalize(r, testCapability(Model25), b); err == nil {
		t.Fatal("draft accepts inherited duration")
	}
}

func TestImageFrameAndEditCombinations(t *testing.T) {
	r := textRequest(Model25)
	r.Content = append(r.Content, ContentItem{Type: "image_url", Role: "last_frame", ImageURL: &MediaReference{URL: "https://media.example/last.jpg"}})
	if _, err := Normalize(r, testCapability(Model25), Bindings{}); err == nil {
		t.Fatal("last frame without first accepted")
	}
	r = textRequest(Model25)
	r.OmniReferenceTaskType = ptr("edit")
	r.Ratio = ptr("adaptive")
	r.Duration = ptr(-1)
	if _, err := Normalize(r, testCapability(Model25), Bindings{}); err == nil {
		t.Fatal("edit without video accepted")
	}
	r.Content = append(r.Content, ContentItem{Type: "video_url", VideoURL: &MediaReference{URL: "https://media.example/reference.mp4"}})
	if _, err := Normalize(r, testCapability(Model25), Bindings{}); err != nil {
		t.Fatal(err)
	}
}

func TestFrameRatiosFollowModelCapabilities(t *testing.T) {
	ratios := []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive"}
	for _, model := range []string{Model20, Model25} {
		for _, ratio := range ratios {
			for _, withLast := range []bool{false, true} {
				name := model + "/" + ratio + "/first"
				if withLast {
					name += "-last"
				}
				t.Run(name, func(t *testing.T) {
					cap := testCapability(model)
					cap.Ratios = ratios
					for _, duration := range []int{cap.MinDuration, cap.MaxDuration} {
						in := textRequest(model)
						in.Ratio, in.Duration = ptr(ratio), ptr(duration)
						in.Content = append(in.Content, ContentItem{Type: "image_url", Role: "first_frame", ImageURL: &MediaReference{URL: "https://media.example/first.jpg"}})
						if withLast {
							in.Content = append(in.Content, ContentItem{Type: "image_url", Role: "last_frame", ImageURL: &MediaReference{URL: "https://media.example/last.jpg"}})
						}
						out, err := Normalize(in, cap, Bindings{})
						if model == Model25 && ratio != "adaptive" {
							apiErr, ok := err.(*Error)
							if !ok || apiErr.Code != "invalid_request" || apiErr.Field != "content" || len(out.Body) != 0 {
								t.Fatalf("2.5 explicit frame ratio must be rejected: %v", err)
							}
							continue
						}
						if err != nil {
							t.Fatalf("frame duration %d rejected: %v", duration, err)
						}
						var body struct {
							Ratio    string        `json:"ratio"`
							Duration int           `json:"duration"`
							Content  []ContentItem `json:"content"`
						}
						if err := json.Unmarshal(out.Body, &body); err != nil || body.Ratio != ratio || body.Duration != duration || body.Content[1].Role != "first_frame" || withLast && body.Content[2].Role != "last_frame" {
							t.Fatalf("frame parameters changed in outbound JSON: %s", out.Body)
						}
						cap.ImageFrames = false
						if _, err := Normalize(in, cap, Bindings{}); err == nil {
							t.Fatal("disabled frame capability accepted")
						}
						cap.ImageFrames = true
						in.Content = append(in.Content, ContentItem{Type: "video_url", Role: "reference_video", VideoURL: &MediaReference{URL: "https://media.example/reference.mp4"}})
						if _, err := Normalize(in, cap, Bindings{}); err == nil {
							t.Fatal("frame mode accepted mixed reference media")
						}
					}
				})
			}
		}
	}
}

func TestImplicitFirstFrameCannotBypassCapability(t *testing.T) {
	for _, tc := range []struct {
		name            string
		model           string
		ratio           string
		frames          bool
		secondImageRole *string
		wantReject      bool
	}{
		{name: "2.0 omitted image role is first frame", model: Model20, ratio: "16:9", frames: true},
		{name: "2.0 disabled implicit frame", model: Model20, ratio: "16:9", wantReject: true},
		{name: "2.5 reference-only cannot admit implicit frame", model: Model25, ratio: "1:1", wantReject: true},
		{name: "2.5 disabled adaptive implicit frame", model: Model25, ratio: "adaptive", wantReject: true},
		{name: "2.5 implicit frame still requires adaptive", model: Model25, ratio: "16:9", frames: true, wantReject: true},
		{name: "2.5 enabled adaptive implicit frame", model: Model25, ratio: "adaptive", frames: true},
		{name: "two omitted image roles are not first and last", model: Model20, ratio: "16:9", frames: true, secondImageRole: ptr(""), wantReject: true},
		{name: "implicit frame cannot mix reference image", model: Model20, ratio: "16:9", frames: true, secondImageRole: ptr("reference_image"), wantReject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := testCapability(tc.model)
			cap.ImageFrames = tc.frames
			if tc.model == Model25 && !tc.frames {
				cap.OmniReferenceTaskTypes = []string{"reference"}
			}
			in := textRequest(tc.model)
			in.Ratio = ptr(tc.ratio)
			in.Content = append(in.Content, ContentItem{Type: "image_url", ImageURL: &MediaReference{URL: "https://media.example/first.jpg"}})
			if tc.secondImageRole != nil {
				in.Content = append(in.Content, ContentItem{Type: "image_url", Role: *tc.secondImageRole, ImageURL: &MediaReference{URL: "https://media.example/second.jpg"}})
			}
			out, err := Normalize(in, cap, Bindings{})
			if tc.wantReject {
				apiErr, ok := err.(*Error)
				if !ok || apiErr.Code != "invalid_request" || apiErr.Field != "content" || len(out.Body) != 0 {
					t.Fatalf("implicit frame bypassed capability or combination checks: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Content []map[string]json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(out.Body, &body); err != nil || len(body.Content) != len(in.Content) {
				t.Fatalf("invalid outgoing content: %s", out.Body)
			}
			if _, exists := body.Content[1]["role"]; exists || in.Content[1].Role != "" {
				t.Fatal("implicit first frame was rewritten in the input or outgoing JSON")
			}
		})
	}
}
