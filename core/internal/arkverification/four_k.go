package arkverification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
)

const FourKVersion = "ark-official-20261116-4k-v1"

// Load4K is opt-in and leaves historical profiles and pinned templates intact.
// The PDF establishes capability/rates; six separately captured screenshots
// supply the complete 4K minimum domain, using this model's reviewed 24 FPS.
func Load4K(model string) (Profile, error) {
	p, err := LoadDraft(model)
	if err != nil {
		return Profile{}, err
	}
	p.Version, p.Capability.Version = FourKVersion, FourKVersion
	p.SourceURL = arkpricecatalog.FourKSourceURL
	p.ObservedAt = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	p.MinimumVersion = "official-min-seedance-2.5-20261116-4k-v1"
	p.Capability.Resolutions = append(p.Capability.Resolutions, "4k")
	p.MinimumResolutions = append([]string(nil), p.Capability.Resolutions...)
	p.Capability.DefaultResolution = "720p"
	dimensions := []Dimensions{{3840, 2160}, {3326, 2494}, {2880, 2880}, {2494, 3326}, {2160, 3840}, {4398, 1886}}
	var largest Dimensions
	for i, ratio := range []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9"} {
		d := dimensions[i]
		p.Bounds["4k/"+ratio] = d
		if d.Width*d.Height > largest.Width*largest.Height {
			largest = d
		}
	}
	p.Bounds["4k/adaptive"] = largest
	p.Sources = append(p.Sources, Source{arkpricecatalog.FourKSourceID, arkpricecatalog.FourKSourceURL, arkpricecatalog.FourKSourceHash()})
	ref, err := arkpricecatalog.LatestReferenceCandidates(model)
	if err != nil {
		return Profile{}, err
	}
	for _, cell := range ref.Minimum.Cells {
		if cell.Resolution == "4k" {
			if _, ok := p.Bounds["4k/"+cell.Ratio]; !ok || cell.FPS != nil && *cell.FPS != p.Capability.FPS {
				return Profile{}, fmt.Errorf("verification: conflicting 4K minimum domain")
			}
			p.MinimumCells = append(p.MinimumCells, MinimumCell{cell.Resolution, cell.Ratio, cell.OutputDurationMS, int64(p.Capability.FPS), cell.MinimumTokens})
		}
	}
	if len(p.MinimumCells) != 648 {
		return Profile{}, fmt.Errorf("verification: incomplete 4K minimum domain")
	}
	for _, source := range ref.Sources {
		if source.ID == arkpricecatalog.FourKMinimumSourceID || source.ID == arkpricecatalog.FourKMinimumCaptureID {
			p.Sources = append(p.Sources, Source{source.ID, source.SourceURL, source.RawSHA256})
		}
	}
	sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].ID < p.Sources[j].ID })
	p.EvidenceSHA256 = ""
	raw, err := json.Marshal(p)
	if err != nil {
		return Profile{}, err
	}
	h := sha256.Sum256(raw)
	p.EvidenceSHA256 = hex.EncodeToString(h[:])
	return p, nil
}
