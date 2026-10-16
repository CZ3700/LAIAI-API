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
					t.Fatalf("local contract rejected submission: outcome=%s status=%d", result.Outcome, result.HTTPStatus)
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
