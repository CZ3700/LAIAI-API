package server

import (
	"encoding/json"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/video"
)

func TestVariantConfigurationRejectsUnboundedOrMismatchedProfiles(t *testing.T) {
	for _, model := range []string{ark.Model20Fast, ark.Model20Mini} {
		for _, scenario := range []string{"valid", "other-deployment", "future-deployment", "adaptive", "smaller-pixels", "smaller-input-bound", "1080p", "2.5-fields"} {
			t.Run(model+"/"+scenario, func(t *testing.T) {
				p, err := arkverification.Load(model)
				if err != nil {
					t.Fatal(err)
				}
				cap := p.Capability
				cap.UpstreamModel = "doubao-seedance-2-0-fast-260128"
				if model == ark.Model20Mini {
					cap.UpstreamModel = "doubao-seedance-2-0-mini-260615"
				}
				bounds := map[string]video.Dimensions{}
				for key, d := range p.Bounds {
					bounds[key] = video.Dimensions{Width: d.Width, Height: d.Height}
				}
				cfg := video.ModelConfig{PublicModel: model, Version: "variant-test", PricingVersion: "variant-test", MaxInputVideoDurationMS: p.MaxInputVideoDurationMS}
				switch scenario {
				case "other-deployment":
					cap.UpstreamModel = "doubao-seedance-2-0-260128"
				case "future-deployment":
					cap.UpstreamModel = "doubao-seedance-2-0-fast-270101"
				case "adaptive":
					cap.Ratios = []string{"adaptive"}
					cap.DefaultRatio = "adaptive"
					bounds["480p/adaptive"] = video.Dimensions{Width: 640, Height: 640}
					bounds["720p/adaptive"] = video.Dimensions{Width: 960, Height: 960}
				case "smaller-pixels":
					bounds["480p/1:1"] = video.Dimensions{Width: 1, Height: 1}
				case "smaller-input-bound":
					cfg.MaxInputVideoDurationMS = 4000
				case "1080p":
					cap.Resolutions = append(cap.Resolutions, "1080p")
				case "2.5-fields":
					cap.OmniReferenceTaskTypes = []string{"edit"}
				}
				raw, _ := json.Marshal(cap)
				cfg.CapabilityJSON = string(raw)
				err = validateVideoCapability(cfg, cap, bounds)
				if (err == nil) != (scenario == "valid") {
					t.Fatalf("configuration validation = %v", err)
				}
			})
		}
	}
}
