package arkverification

import (
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
)

func TestFourKProfileHasCompleteMinimumEvidence(t *testing.T) {
	p, err := Load4K(ark.Model25)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != FourKVersion || len(p.Capability.Resolutions) != 4 || !p.Capability.DraftConversion || !p.Capability.ImageFrames || len(p.MinimumResolutions) != 4 || len(p.MinimumCells) != 648 || p.Bounds["4k/21:9"] != (Dimensions{4398, 1886}) || p.Bounds["4k/adaptive"] != (Dimensions{3326, 2494}) {
		t.Fatalf("incomplete 4K profile: %+v", p)
	}
	fourK := 0
	for _, c := range p.MinimumCells {
		if c.Resolution != "4k" {
			continue
		}
		fourK++
		if c.FPS != 24 {
			t.Fatal("4K minimum FPS was not independently verified")
		}
		if c.Ratio == "16:9" && c.OutputDurationMS == 5000 && c.MinimumTokens != 1749600 {
			t.Fatal("5-second minimum differs from captured table")
		}
	}
	if fourK != 162 {
		t.Fatal("4K minimum domain incomplete")
	}
	p.Capability.UpstreamModel = "doubao-seedance-2-5-260628"
	res := "4k"
	if _, err := ark.Normalize(ark.CreateRequest{Model: ark.Model25, Resolution: &res, Content: []ark.ContentItem{{Type: "text", Text: "test"}}}, p.Capability, ark.Bindings{}); err != nil {
		t.Fatal(err)
	}
	old, err := Load(ark.Model25)
	if err != nil || len(old.Capability.Resolutions) != 3 || len(old.MinimumResolutions) != 0 {
		t.Fatal("historical template profile changed", err)
	}
	if _, err := Load4K(ark.Model25Flash); err == nil {
		t.Fatal("Flash acquired 4K evidence")
	}
}
