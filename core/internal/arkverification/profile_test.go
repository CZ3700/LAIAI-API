package arkverification

import (
	"fmt"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
)

func TestReviewedProfileCoverageAndNormalizer(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model25, ark.Model20Fast, ark.Model20Mini} {
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
			} else if model == ark.Model20Fast {
				cap.UpstreamModel = "doubao-seedance-2-0-fast-260128"
			} else if model == ark.Model20Mini {
				cap.UpstreamModel = "doubao-seedance-2-0-mini-260615"
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
	for _, model := range []string{"seedance-1.0", "seedance-2.5-fast", "seedance-2.5-mini", "doubao-seedance-2-0-260128", "doubao-seedance-2-0-fast-260128", "doubao-seedance-2-0-mini-260615"} {
		if _, err := Load(model); err == nil {
			t.Fatalf("standard evidence reused for %s", model)
		}
	}
}

func TestVariantProfilesHaveIndependentIdentityAndReviewedMinimumEvidence(t *testing.T) {
	standard, err := Load(ark.Model20)
	if err != nil {
		t.Fatal(err)
	}
	versions, hashes := map[string]bool{standard.MinimumVersion: true}, map[string]bool{standard.EvidenceSHA256: true}
	for _, model := range []string{ark.Model20Fast, ark.Model20Mini} {
		p, err := Load(model)
		if err != nil {
			t.Fatal(err)
		}
		if p.Version != VariantVersion || p.Capability.Version != VariantVersion || p.Capability.PublicModel != model || p.MinimumVersion != "official-min-"+model+"-20261019-v1" || versions[p.MinimumVersion] || hashes[p.EvidenceSHA256] {
			t.Fatal("variant reused a standard/other variant profile identity")
		}
		versions[p.MinimumVersion], hashes[p.EvidenceSHA256] = true, true
		if len(p.MinimumCells) != 144 || len(p.Bounds) != 12 || len(p.Capability.Resolutions) != 2 || p.MaxInputVideoDurationMS != 15000 || p.Capability.MaxDuration != 15 || p.Capability.MinDuration != 4 || p.Capability.FPS != 24 {
			t.Fatal("variant capability/minimum domain incorrect")
		}
		for _, forbidden := range []string{"1080p/16:9", "4k/16:9", "720p/adaptive"} {
			if _, ok := p.Bounds[forbidden]; ok {
				t.Fatal("unsupported bounds admitted")
			}
		}
		if p.Bounds["480p/16:9"] != (Dimensions{864, 496}) || p.Bounds["720p/4:3"] != (Dimensions{1112, 834}) || p.Bounds["720p/21:9"] != (Dimensions{1470, 630}) {
			t.Fatal("official ratio-specific pixels lost")
		}
		for key, want := range map[string]int64{"480p/16:9/4000": 70308, "480p/1:1/4000": 67200, "720p/16:9/4000": 151200, "720p/4:3/4000": 152153, "720p/21:9/15000": 542637} {
			found := false
			for _, cell := range p.MinimumCells {
				if fmt.Sprintf("%s/%s/%d", cell.Resolution, cell.Ratio, cell.OutputDurationMS) == key {
					found = cell.MinimumTokens == want && cell.FPS == 24
				}
			}
			if !found {
				t.Fatal("literal official minimum changed", key)
			}
		}
		sources := map[string]string{}
		for _, s := range p.Sources {
			sources[s.ID] = s.SHA256
		}
		if sources["official-2-0-family-20261019"] != "1cc95eb91d8bb7f8a69965e0815ea6fff8cde90458e4d4e60d9c8b5c99e0c39e" || sources["official-query-api-20261019"] != "f7e6e434bb9cd12ba79d39b0cb2c80fbef8b56780e6efd6d95e98c4e1b9aabe8" {
			t.Fatal("independent family membership/settlement evidence missing")
		}
	}
}

func TestVariantMinimumEvidenceFailsClosed(t *testing.T) {
	for _, mutation := range []string{"hash", "view", "table", "row_count", "cell_count", "duplicate_view"} {
		t.Run(mutation, func(t *testing.T) {
			ref, err := arkpricecatalog.ReferenceCandidates(ark.Model20Fast)
			if err != nil {
				t.Fatal(err)
			}
			for i := range ref.Sources {
				if ref.Sources[i].ViewID != "vew2CR2JYM" {
					continue
				}
				switch mutation {
				case "hash":
					ref.Sources[i].RawSHA256 = "changed"
				case "view":
					ref.Sources[i].ViewID = "vewQ2CK6dj"
				case "table":
					ref.Sources[i].TableID = "tblheIYDXXwYM43b"
				case "row_count":
					ref.Sources[i].RawRows = ref.Sources[i].RawRows[:47]
				case "cell_count":
					ref.Minimum.Cells = ref.Minimum.Cells[:143]
				case "duplicate_view":
					ref.Sources = append(ref.Sources, ref.Sources[i])
				}
				break
			}
			if verifyVariantMinimumSources(ref) == nil {
				t.Fatal("changed or incomplete minimum evidence accepted")
			}
		})
	}
}
