package arkverification

import (
	"fmt"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
)

func TestReviewedProfileCoverageAndNormalizer(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model25} {
		t.Run(model, func(t *testing.T) {
			p, err := Load(model)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := arkpricecatalog.ReferenceCandidates(model)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.MinimumCells) != len(ref.Minimum.Cells) || len(p.MinimumCells) != len(p.Capability.Resolutions)*6*(p.Capability.MaxDuration-3) {
				t.Fatal("minimum domain incomplete")
			}
			if len(p.EvidenceSHA256) != 64 || len(p.Sources) < 5 || p.Capability.Frames || p.Capability.Draft || p.Capability.DraftConversion {
				t.Fatal("unverified capability or missing provenance")
			}
			cap := p.Capability
			cap.UpstreamModel = "doubao-seedance-2-0-260128"
			if model == ark.Model25 {
				cap.UpstreamModel = "doubao-seedance-2-5-260628"
			}
			mixed := ark.CreateRequest{Model: model, Content: []ark.ContentItem{{Type: "text", Text: "verification"}}}
			for i := 0; i < cap.MaxImages; i++ {
				mixed.Content = append(mixed.Content, ark.ContentItem{Type: "image_url", Role: "reference_image", ImageURL: &ark.MediaReference{URL: "https://example.com/image.png"}})
			}
			for i := 0; i < cap.MaxVideos; i++ {
				mixed.Content = append(mixed.Content, ark.ContentItem{Type: "video_url", Role: "reference_video", VideoURL: &ark.MediaReference{URL: "https://example.com/video.mp4"}})
			}
			for i := 0; i < cap.MaxAudios; i++ {
				mixed.Content = append(mixed.Content, ark.ContentItem{Type: "audio_url", Role: "reference_audio", AudioURL: &ark.MediaReference{URL: "https://example.com/audio.mp3"}})
			}
			if _, err := ark.Normalize(mixed, cap, ark.Bindings{}); err != nil {
				t.Fatalf("documented mixed-media counts rejected: %v", err)
			}
			for _, res := range cap.Resolutions {
				for _, ratio := range cap.Ratios {
					for _, duration := range []int{cap.MinDuration, cap.MaxDuration, -1} {
						r := ark.CreateRequest{Model: model, Resolution: &res, Ratio: &ratio, Duration: &duration, Content: []ark.ContentItem{{Type: "text", Text: "verification"}, {Type: "video_url", Role: "reference_video", VideoURL: &ark.MediaReference{URL: "https://example.com/reference.mp4"}}}}
						if _, err := ark.Normalize(r, cap, ark.Bindings{}); err != nil {
							t.Fatalf("%s/%s/%d: %v", res, ratio, duration, err)
						}
					}
				}
			}
			for i, m := range p.MinimumCells {
				if m.FPS != 24 || m.MinimumTokens != ref.Minimum.Cells[i].MinimumTokens {
					t.Fatal("source minimum replaced rather than supplemented")
				}
			}
			if model == ark.Model20 && ref.Minimum.Cells[0].FPS != nil {
				t.Fatal("original missing FPS was rewritten")
			}
			// Reloading cannot retain mutable caller data or a changed digest.
			hash := p.EvidenceSHA256
			p.MinimumCells[0].MinimumTokens = 1
			p.Bounds["480p/1:1"] = Dimensions{1, 1}
			next, err := Load(model)
			if err != nil || next.EvidenceSHA256 != hash || next.MinimumCells[0].MinimumTokens == 1 {
				t.Fatal("profile aliased mutable caller state")
			}
		})
	}
}

func TestReviewedPixelBoundsAndUnsupportedModels(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model25} {
		p, err := Load(model)
		if err != nil {
			t.Fatal(err)
		}
		for res, want := range map[string]int64{"480p": 428544, "720p": 927408, "1080p": 2086876, "4k": 8295044} {
			if model == ark.Model25 && res == "4k" {
				continue
			}
			var max int64
			for _, ratio := range p.Capability.Ratios {
				d := p.Bounds[res+"/"+ratio]
				if d.Width*d.Height > max {
					max = d.Width * d.Height
				}
			}
			if max != want {
				t.Fatal(fmt.Sprintf("%s/%s unsafe area %d", model, res, max))
			}
		}
		if _, ok := p.Bounds["1080p/adaptive"]; ok {
			t.Fatal("arbitrary ratio must not inherit six-enum bound")
		}
	}
	for _, model := range []string{"seedance-1.0", "seedance-2.0-fast", "seedance-2.0-mini", "doubao-seedance-2-0-260128"} {
		if _, err := Load(model); err == nil {
			t.Fatalf("standard evidence reused for %s", model)
		}
	}
}
