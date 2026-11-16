package ark

import (
	"encoding/json"
	"testing"
)

func TestSeedance25FourKDirectAndDraftConversion(t *testing.T) {
	cap := testCapability(Model25)
	in := textRequest(Model25)
	in.Resolution, in.OutputFormat = ptr("4k"), ptr("mov")
	out, err := Normalize(in, cap, Bindings{})
	if err != nil || out.Parameters.Resolution != "4k" {
		t.Fatalf("direct 4K rejected: %v", err)
	}
	in.Content = []ContentItem{{Type: "draft_task", DraftTask: &DraftReference{ID: "task_parent"}}}
	b := Bindings{Drafts: map[string]DraftBinding{"task_parent": {UpstreamTaskID: "cgt-parent", PublicModel: Model25, CapabilityVersion: "test-v1", HasInputVideo: true, Parameters: EffectiveParameters{Duration: 5, Ratio: "16:9", FramesPerSecond: 24, Seed: 42, GenerateAudio: true}}}}
	out, err = Normalize(in, cap, b)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["resolution"] != "4k" || body["output_format"] != "mov" || out.Parameters.Duration != 5 || !out.HasInputVideo || out.Parameters.Resolution != "4k" {
		t.Fatalf("target resolution or inherited billing context lost: %s %+v", out.Body, out.Parameters)
	}
	for _, key := range []string{"duration", "frames", "ratio", "seed", "generate_audio", "omni_reference_task_type"} {
		if _, ok := body[key]; ok {
			t.Fatalf("conversion resent inherited field %s", key)
		}
	}
	in.Duration = ptr(5)
	if _, err := Normalize(in, cap, b); err == nil {
		t.Fatal("4K conversion accepted inherited duration")
	}
	in = textRequest(Model25)
	in.Draft = ptr(true)
	out, err = Normalize(in, cap, Bindings{})
	if err != nil || out.Parameters.Resolution != "480p" {
		t.Fatalf("Draft default did not select 480p: %v", err)
	}
	in.Resolution = ptr("4k")
	if _, err := Normalize(in, cap, Bindings{}); err == nil {
		t.Fatal("4K Draft preview accepted")
	}
	cap.PublicModel, cap.UpstreamModel = Model25Flash, "doubao-seedance-2-5-flash-261115"
	in = textRequest(Model25Flash)
	in.Resolution = ptr("4k")
	if _, err := Normalize(in, cap, Bindings{}); err == nil {
		t.Fatal("standard 2.5 upgrade granted Flash 4K")
	}
}
