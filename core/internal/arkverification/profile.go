// Package arkverification binds reviewed official documentation to the captured
// price/minimum tables. It performs no network, database or price publication.
package arkverification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
)

const Version = "ark-official-20261019-v1"
const VariantVersion = "ark-official-20261019-variants-v1"

// DraftVersion is opt-in so installed 2026-10-19 pricing templates and their
// evidence hashes continue to validate against the original profile.
const DraftVersion = "ark-official-20261030-draft-v1"

type Dimensions struct {
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}
type Source struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}
type MinimumCell struct {
	Resolution       string `json:"resolution"`
	Ratio            string `json:"ratio"`
	OutputDurationMS int64  `json:"output_duration_ms"`
	FPS              int64  `json:"fps"`
	MinimumTokens    int64  `json:"minimum_tokens"`
}
type Profile struct {
	Version                 string                `json:"version"`
	EvidenceSHA256          string                `json:"evidence_sha256"`
	SourceURL               string                `json:"source_url"`
	ObservedAt              time.Time             `json:"observed_at"`
	Capability              ark.Capability        `json:"capability"`
	Bounds                  map[string]Dimensions `json:"bounds"`
	MaxInputVideoDurationMS int64                 `json:"max_input_video_duration_ms"`
	MinimumVersion          string                `json:"minimum_version"`
	MinimumCells            []MinimumCell         `json:"minimum_cells"`
	Sources                 []Source              `json:"sources"`
}

// Source hashes identify the public Markdown bytes fetched on 2026-10-19.
// The minimum-table hashes retain their original 2026-10-17 capture dates in
// arkpricecatalog. This is documentation verification, not live output testing.
func documentationSources() []Source {
	return []Source{
		{"official-pricing-20261019", "https://ark.volcengine.com/region:cn-beijing/docs/model-pricing.md", "595f0d5d63b66266d17c6837481559f69ee5fea81edb1aac778450382be511c7"},
		{"official-model-list-20261019", "https://ark.volcengine.com/region:cn-beijing/docs/model-list.md", "f229d202a92612b067b16c1c5b0ce184d9a0a7ac70186f2b514fd9f4d56aaad2"},
		{"official-create-api-20261019", "https://ark.volcengine.com/region:cn-beijing/docs/create-video-generation-task-api.md", "f0a215473aefdcf24ac0d8ea0af70484d66a74854422b39469fd55f2c6099909"},
	}
}

// Load admits reviewed generation with explicit ratios only. In particular it
// does not claim a bound for 2.5 arbitrary-ratio editing/extension/first frames.
// Existing defaults and exact deployment identity are preserved by the server.
func Load(model string) (Profile, error) {
	var p Profile
	variant, isVariant := reviewedVariant(model)
	if model != ark.Model20 && model != ark.Model25 && !isVariant {
		return p, arkpricecatalog.ErrUnsupportedModel
	}
	ref, err := arkpricecatalog.ReferenceCandidates(model)
	if err != nil {
		return p, err
	}
	p.Version = Version
	p.SourceURL = "https://console.volcengine.com/ark/region:cn-beijing/docs/model-pricing"
	p.ObservedAt = time.Date(2026, 10, 19, 7, 54, 10, 0, time.UTC)
	p.MinimumVersion = "official-min-" + model + "-20261019-v1"
	p.Sources = documentationSources()
	if isVariant {
		// The official tutorial explicitly includes the exact Fast and Mini IDs
		// in the 2.0 family. Keep independent versions and provenance; never use
		// the standard model's already published minimum-rule identity.
		p.Version = VariantVersion
		p.ObservedAt = time.Date(2026, 10, 19, 10, 31, 47, 0, time.UTC)
		p.MinimumVersion = "official-min-" + model + "-20261019-v1"
		p.Sources = append(p.Sources,
			Source{"official-2-0-family-20261019", "https://ark.volcengine.com/region:cn-beijing/docs/seedance-2-0.md", "1cc95eb91d8bb7f8a69965e0815ea6fff8cde90458e4d4e60d9c8b5c99e0c39e"},
			Source{"official-query-api-20261019", "https://ark.volcengine.com/region:cn-beijing/docs/get-video-generation-task-api.md", "f7e6e434bb9cd12ba79d39b0cb2c80fbef8b56780e6efd6d95e98c4e1b9aabe8"})
		if err := verifyVariantMinimumSources(ref); err != nil {
			return Profile{}, err
		}
		if _, err := LoadTariff(model); err != nil {
			return Profile{}, err
		}
	}
	for _, source := range ref.Sources {
		p.Sources = append(p.Sources, Source{source.ID, source.SourceURL, source.RawSHA256})
	}
	sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].ID < p.Sources[j].ID })
	p.Capability = ark.Capability{
		PublicModel: model, Version: p.Version, Enabled: true, Verified: true,
		Resolutions: []string{"480p", "720p", "1080p"}, Ratios: []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9"},
		MinDuration: 4, MaxDuration: 15, DefaultDuration: 4, DefaultResolution: "480p", DefaultRatio: "1:1", DefaultSeed: -1,
		DefaultWatermark: true, DefaultExecutionExpiresAfter: 3600, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200,
		AutomaticDuration: true, FPS: 24, MaxContentItems: 16, MaxImages: 9, MaxVideos: 3, MaxAudios: 3,
		ImageInput: true, VideoInput: true, AudioInput: true, ImageFrames: true, GenerateAudio: true, ReturnLastFrame: true,
	}
	p.MaxInputVideoDurationMS = 15000
	rates := map[string][2]string{"480p": {"46", "28"}, "720p": {"46", "28"}, "1080p": {"51", "31"}, "4k": {"26", "16"}}
	if model == ark.Model20 {
		p.Capability.Resolutions = append(p.Capability.Resolutions, "4k")
	} else if model == ark.Model25 {
		p.Capability.MaxDuration = 30
		p.Capability.MaxContentItems = 51
		p.Capability.MaxImages = 30
		p.Capability.MaxVideos = 10
		p.Capability.MaxAudios = 10
		p.Capability.ImageFrames = false
		p.Capability.OmniReferenceTaskTypes = []string{"reference"}
		p.Capability.OutputFormats = []string{"mp4", "mov"}
		p.MaxInputVideoDurationMS = 30000
		rates = map[string][2]string{"480p": {"70", "42"}, "720p": {"70", "42"}, "1080p": {"77", "46"}}
	} else if isVariant {
		p.Capability.Resolutions = []string{"480p", "720p"}
		rates = map[string][2]string{"480p": {variant.noVideo, variant.video}, "720p": {variant.noVideo, variant.video}}
	}
	// A changed captured tariff requires a fresh review/version, never silently
	// carries an earlier verification badge forward.
	if len(ref.Prices) != len(rates)*2 {
		return Profile{}, fmt.Errorf("verification: incomplete standard prices")
	}
	for _, price := range ref.Prices {
		pair, ok := rates[price.Resolution]
		idx := 0
		if price.InputVideo {
			idx = 1
		}
		if !ok || price.Mode != "generate" || price.Draft || price.ServiceTier != "default" || price.SupplierCNYPer1M != pair[idx] {
			return Profile{}, fmt.Errorf("verification: official tariff changed")
		}
	}
	// Exact pairs from the official API's pixel table, not a 16:9 area assumed
	// for every ratio, and not independently maximized width/height.
	dimensions := map[string][]Dimensions{
		"480p":  {{864, 496}, {752, 560}, {640, 640}, {560, 752}, {496, 864}, {992, 432}},
		"720p":  {{1280, 720}, {1112, 834}, {960, 960}, {834, 1112}, {720, 1280}, {1470, 630}},
		"1080p": {{1920, 1080}, {1664, 1248}, {1440, 1440}, {1248, 1664}, {1080, 1920}, {2206, 946}},
		"4k":    {{3840, 2160}, {3326, 2494}, {2880, 2880}, {2494, 3326}, {2160, 3840}, {4398, 1886}},
	}
	if model == ark.Model25 {
		dimensions["480p"][0] = Dimensions{854, 480}
		dimensions["480p"][4] = Dimensions{480, 854}
	}
	p.Bounds = map[string]Dimensions{}
	for _, res := range p.Capability.Resolutions {
		for i, ratio := range p.Capability.Ratios {
			p.Bounds[res+"/"+ratio] = dimensions[res][i]
		}
	}
	seen := map[string]bool{}
	for _, cell := range ref.Minimum.Cells {
		if cell.FPS != nil && *cell.FPS != 24 {
			return Profile{}, fmt.Errorf("verification: FPS conflicts with official model list")
		}
		if cell.FPS == nil && model != ark.Model20 && !isVariant {
			return Profile{}, fmt.Errorf("verification: missing primary FPS")
		}
		// Supplement 2.0's absent column from the independently hashed official
		// model list. Preserve the original capture and its missing-FPS marker.
		key := fmt.Sprintf("%s/%s/%d", cell.Resolution, cell.Ratio, cell.OutputDurationMS)
		if _, ok := p.Bounds[cell.Resolution+"/"+cell.Ratio]; !ok || seen[key] || cell.MinimumTokens <= 0 || cell.OutputDurationMS < 4000 || cell.OutputDurationMS > int64(p.Capability.MaxDuration)*1000 || cell.OutputDurationMS%1000 != 0 {
			return Profile{}, fmt.Errorf("verification: invalid minimum domain")
		}
		seen[key] = true
		p.MinimumCells = append(p.MinimumCells, MinimumCell{cell.Resolution, cell.Ratio, cell.OutputDurationMS, 24, cell.MinimumTokens})
	}
	for _, res := range p.Capability.Resolutions {
		for _, ratio := range p.Capability.Ratios {
			for d := p.Capability.MinDuration; d <= p.Capability.MaxDuration; d++ {
				if !seen[fmt.Sprintf("%s/%s/%d", res, ratio, d*1000)] {
					return Profile{}, fmt.Errorf("verification: incomplete minimum domain")
				}
			}
		}
	}
	// The composite digest includes all verified values and every source digest;
	// it is explicitly distinct from the raw document hashes listed in Sources.
	raw, err := json.Marshal(p)
	if err != nil {
		return Profile{}, err
	}
	hash := sha256.Sum256(raw)
	p.EvidenceSHA256 = hex.EncodeToString(hash[:])
	return p, nil
}

// LoadDraft extends only the reviewed Seedance 2.5 profile. The newly captured
// official create API, pricing guide and tutorial establish that Draft is 480p,
// conversion is 1080p, and each step uses the ordinary resolution rate.
// This profile still does not authorize any account, channel, or price rule:
// synchronization and publication remain separate audited transactions.
func LoadDraft(model string) (Profile, error) {
	if model != ark.Model25 {
		return Profile{}, arkpricecatalog.ErrUnsupportedModel
	}
	p, err := Load(model)
	if err != nil {
		return Profile{}, err
	}
	p.Version = DraftVersion
	p.ObservedAt = time.Date(2026, 10, 30, 10, 38, 0, 0, time.UTC)
	p.MinimumVersion = "official-min-seedance-2.5-20261030-draft-v1"
	p.Capability.Version = p.Version
	p.Capability.Draft = true
	p.Capability.DraftConversion = true
	p.Sources = append(p.Sources,
		Source{"official-create-api-20261030-draft", "https://ark.volcengine.com/region:cn-beijing/docs/create-video-generation-task-api.md", "a779514c668fc18272275f07351034078e5aeec9da98cb1352b11a083751f324"},
		Source{"official-pricing-20261030-draft", "https://ark.volcengine.com/region:cn-beijing/docs/model-pricing.md", "c93a73de8f9db86686a4499065a401c08b04dcd3b972fe51b8ff3f2c43908231"},
		Source{"official-seedance-2-5-20261030-draft", "https://ark.volcengine.com/region:cn-beijing/docs/seedance-2-5.md", "c978148e9b3a6c954930b0a9d2a9b597efab67b011d764b07ed875d0cd0617ec"},
	)
	sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].ID < p.Sources[j].ID })
	p.EvidenceSHA256 = ""
	raw, err := json.Marshal(p)
	if err != nil {
		return Profile{}, err
	}
	hash := sha256.Sum256(raw)
	p.EvidenceSHA256 = hex.EncodeToString(hash[:])
	return p, nil
}

// A family-wide statement does not authorize an arbitrary table. Bind the two
// reviewed source views and their original bytes, with no interpolation from
// monetary examples or the 1080p/4k views that these models do not support.
func verifyVariantMinimumSources(ref arkpricecatalog.Candidates) error {
	want := map[string]string{
		"vew2CR2JYM": "94e2598e88f886ca082877d2e166e8917992f31adb68b8b3a3003d0fe95361bf",
		"vewVBehIlq": "9caba96a5ad810ea9673f5b26a114a5d8e35f8f7324fd240f2a9722136194e35",
	}
	seen := map[string]bool{}
	for _, source := range ref.Sources {
		if source.TableID != "tblmNCuMjADrXtDf" {
			continue
		}
		hash, ok := want[source.ViewID]
		if !ok || seen[source.ViewID] || source.RawSHA256 != hash || len(source.RawRows) != 48 {
			return fmt.Errorf("verification: reviewed variant minimum source changed")
		}
		seen[source.ViewID] = true
	}
	if len(seen) != 2 || len(ref.Minimum.Cells) != 144 {
		return fmt.Errorf("verification: incomplete variant minimum evidence")
	}
	return nil
}
