package arkpricecatalog

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"time"
)

const FourKSourceID = "official-seedance-2.5-4k-20261116"
const FourKSourceURL = "https://bytedance.larkoffice.com/wiki/DXIqwz9aOiVWkCkFoxEcohosnfd"

// Reviewed facts from the user-supplied official PDF. The original PDF digest
// is retained without publishing its full text in this public repository.
//
//go:embed data/seedance-2.5-4k-20261116.txt
var fourKDocument []byte

func FourKSourceHash() string {
	h := sha256.Sum256(fourKDocument)
	return hex.EncodeToString(h[:])
}

// LatestReferenceCandidates adds the separately captured 2.5 4K tariff.
// ReferenceCandidates stays frozen for historical template/profile validation.
func LatestReferenceCandidates(model string) (Candidates, error) {
	ref, err := ReferenceCandidates(model)
	if err != nil || model != "seedance-2.5" {
		return ref, err
	}
	ref.CatalogVersion += "+seedance25-4k-20261116"
	minimum, sources, err := fourKMinimumCandidates()
	if err != nil {
		return Candidates{}, err
	}
	ref.Minimum.Cells = append(ref.Minimum.Cells, minimum...)
	ref.Sources = append(ref.Sources, sources...)
	digest := sha256.Sum256([]byte(ref.ManifestSHA256 + "/" + FourKSourceHash() + "/" + fourKMinimumSHA256 + "/" + fourKMinimumCaptureSHA256))
	ref.ManifestSHA256 = hex.EncodeToString(digest[:])
	ref.Sources = append(ref.Sources, CandidateSource{
		ID: FourKSourceID, SourceURL: FourKSourceURL, RawSHA256: FourKSourceHash(),
		CapturedAt: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
		Headers:    []string{"生成类型", "输入不含视频（元/百万 token）", "输入包含视频（元/百万 token）"},
		Columns:    []Column{{Semantic: "generation_type"}, {Semantic: "supplier_token_price", Unit: "CNY/1M_tokens", Precision: "exact"}, {Semantic: "supplier_token_price", Unit: "CNY/1M_tokens", Precision: "exact"}},
		RawRows:    [][]string{{"480p Draft", "70", "42"}, {"4k generation / Draft conversion", "31", "19"}},
	})
	for _, input := range []bool{false, true} {
		rate := "31"
		if input {
			rate = "19"
		}
		ref.Prices = append(ref.Prices, ReferencePrice{
			Key: fmt.Sprintf("generate/4k/%t/default/false", input), Mode: "generate", Resolution: "4k", InputVideo: input,
			ServiceTier: "default", SupplierCNYPer1M: rate, Unit: "CNY/1M_tokens", Precision: "exact",
			SourceRows: []SourceRow{{SourceID: FourKSourceID, Row: 2}},
		})
	}
	ref.Minimum.Issues = append(ref.Minimum.Issues, "4k_primary_fps_supplemented_by_verified_model_profile")
	return ref, nil
}
