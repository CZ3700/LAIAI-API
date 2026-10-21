package ark

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func omniMedia(kind, role string) ContentItem {
	item := ContentItem{Type: kind + "_url", Role: role}
	ref := &MediaReference{URL: "https://media.example/reference"}
	switch kind {
	case "image":
		item.ImageURL = ref
	case "video":
		item.VideoURL = ref
	case "audio":
		item.AudioURL = ref
	}
	return item
}

// Simulated reference-only capability and transport: no paid provider request.
// Assert the actual submit body, including the no-media production failure case.
func TestSubmit25OmniTaskTypeMatchesReferenceContent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		media      []ContentItem
		frame      bool
		reference  bool
		inputVideo bool
	}{
		{name: "text only"},
		{name: "first frame", media: []ContentItem{omniMedia("image", "first_frame")}, frame: true},
		{name: "implicit first frame", media: []ContentItem{omniMedia("image", "")}, frame: true},
		{name: "first and last frame", media: []ContentItem{omniMedia("image", "first_frame"), omniMedia("image", "last_frame")}, frame: true},
		{name: "reference image", media: []ContentItem{omniMedia("image", "reference_image")}, reference: true},
		{name: "reference video", media: []ContentItem{omniMedia("video", "reference_video")}, reference: true, inputVideo: true},
		{name: "implicit reference video", media: []ContentItem{omniMedia("video", "")}, reference: true, inputVideo: true},
		{name: "reference audio", media: []ContentItem{omniMedia("audio", "reference_audio")}, reference: true},
		{name: "implicit reference audio", media: []ContentItem{omniMedia("audio", "")}, reference: true},
		{name: "mixed reference media", media: []ContentItem{omniMedia("image", "reference_image"), omniMedia("video", "reference_video"), omniMedia("audio", "reference_audio")}, reference: true, inputVideo: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := testCapability(Model25)
			cap.OmniReferenceTaskTypes = []string{"reference"}
			in := textRequest(Model25)
			in.Content = append(in.Content, tc.media...)
			if tc.frame {
				in.Ratio = ptr("adaptive")
			}
			normalized, err := Normalize(in, cap, Bindings{})
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if tc.reference {
				want = "reference"
			}
			if normalized.Parameters.OmniReferenceTaskType != want || normalized.HasInputVideo != tc.inputVideo {
				t.Fatalf("incorrect effective reference context: %+v", normalized)
			}
			calls := 0
			client := testClient(func(req *http.Request) (*http.Response, error) {
				calls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				raw, present := body["omni_reference_task_type"]
				if present != tc.reference {
					t.Fatalf("omni field present=%t, want=%t", present, tc.reference)
				}
				if present && string(raw) != `"reference"` {
					t.Fatalf("reference-only request escaped its enabled mode: %s", raw)
				}
				if req.Method != http.MethodPost || req.URL.Path != "/api/v3/contents/generations/tasks" {
					t.Fatal("unexpected submission route")
				}
				return response(http.StatusOK, `{"id":"cgt-reference-context"}`), nil
			})
			result := client.Submit(context.Background(), normalized)
			if result.Outcome != OutcomeAccepted || calls != 1 {
				t.Fatalf("submit result=%+v, calls=%d", result, calls)
			}
			if in.OmniReferenceTaskType != nil {
				t.Fatal("normalization mutated the request")
			}
		})
	}
}

func TestNormalize25RejectsExplicitOmniOutsideReferenceScenario(t *testing.T) {
	for _, tc := range []struct {
		name  string
		media []ContentItem
	}{
		{name: "text"},
		{name: "first frame", media: []ContentItem{omniMedia("image", "first_frame")}},
		{name: "implicit first frame", media: []ContentItem{omniMedia("image", "")}},
		{name: "first and last frame", media: []ContentItem{omniMedia("image", "first_frame"), omniMedia("image", "last_frame")}},
	} {
		for _, mode := range []string{"reference", "auto", "edit", "extend"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				in := textRequest(Model25)
				in.Content = append(in.Content, tc.media...)
				in.Ratio, in.Duration = ptr("adaptive"), ptr(-1)
				in.OmniReferenceTaskType = ptr(mode)
				out, err := Normalize(in, testCapability(Model25), Bindings{})
				apiErr, ok := err.(*Error)
				if !ok || apiErr.Code != "invalid_request" || apiErr.Field != "omni_reference_task_type" || apiErr.HTTPStatus != 422 || len(out.Body) != 0 {
					t.Fatalf("inapplicable task type emitted an upstream request: out=%+v err=%v", out, err)
				}
			})
		}
	}
}

func TestNormalize25ExplicitReferenceModesKeepEditingConstraints(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		videos   int
		image    bool
		ratio    string
		duration int
		valid    bool
	}{
		{name: "edit one video", mode: "edit", videos: 1, ratio: "adaptive", duration: -1, valid: true},
		{name: "edit needs automatic duration", mode: "edit", videos: 1, ratio: "adaptive", duration: 5},
		{name: "edit needs adaptive ratio", mode: "edit", videos: 1, ratio: "1:1", duration: -1},
		{name: "edit rejects multiple videos", mode: "edit", videos: 2, ratio: "adaptive", duration: -1},
		{name: "edit rejects reference image without video", mode: "edit", image: true, ratio: "adaptive", duration: -1},
		{name: "extend one video", mode: "extend", videos: 1, ratio: "adaptive", duration: 5, valid: true},
		{name: "extend needs adaptive ratio", mode: "extend", videos: 1, ratio: "1:1", duration: 5},
		{name: "extend rejects multiple videos", mode: "extend", videos: 2, ratio: "adaptive", duration: 5},
		{name: "extend rejects reference image without video", mode: "extend", image: true, ratio: "adaptive", duration: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := textRequest(Model25)
			in.OmniReferenceTaskType, in.Ratio, in.Duration = ptr(tc.mode), ptr(tc.ratio), ptr(tc.duration)
			for i := 0; i < tc.videos; i++ {
				in.Content = append(in.Content, omniMedia("video", "reference_video"))
			}
			if tc.image {
				in.Content = append(in.Content, omniMedia("image", "reference_image"))
			}
			out, err := Normalize(in, testCapability(Model25), Bindings{})
			if !tc.valid {
				apiErr, ok := err.(*Error)
				if !ok || apiErr.Code != "invalid_request" || apiErr.Field != "omni_reference_task_type" || len(out.Body) != 0 {
					t.Fatalf("editing constraint escaped: out=%+v err=%v", out, err)
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
			if string(body["omni_reference_task_type"]) != `"`+tc.mode+`"` || out.Parameters.OmniReferenceTaskType != tc.mode {
				t.Fatalf("explicit editing mode changed: %s", out.Body)
			}
		})
	}
}
