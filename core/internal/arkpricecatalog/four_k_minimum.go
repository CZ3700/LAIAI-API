package arkpricecatalog

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const FourKMinimumSourceID = "official-seedance-2.5-minimum-4k-20261116"
const FourKMinimumCaptureID = "official-seedance-2.5-minimum-4k-capture-20261116"
const fourKMinimumSHA256 = "33a8983370fd9e68a765d26e40e1d245bf6e4931c2b337328d892484fd521045"
const fourKMinimumCaptureSHA256 = "e91d8d4d893e1ed97b26a9e598ed35e7dbddafecd9899efa4759df96bfc5ad3c"

//go:embed data/seedance-2.5-minimum-4k-20261116.tsv
var fourKMinimumTSV []byte

//go:embed data/seedance-2.5-minimum-4k-capture-20261116.json
var fourKMinimumCapture []byte

// The 108 source rows are transcribed verbatim from six supplied screenshots,
// including merged ratio labels. Never interpolate from another model or from
// rounded prices. FPS is absent in the screenshots and stays absent here.
func fourKMinimumCandidates() ([]ReferenceMinimumCell, []CandidateSource, error) {
	h := sha256.Sum256(fourKMinimumTSV)
	captureHash := sha256.Sum256(fourKMinimumCapture)
	if hex.EncodeToString(h[:]) != fourKMinimumSHA256 || hex.EncodeToString(captureHash[:]) != fourKMinimumCaptureSHA256 {
		return nil, nil, fmt.Errorf("catalog: reviewed 4K minimum capture changed")
	}
	r := csv.NewReader(bytes.NewReader(fourKMinimumTSV))
	r.Comma, r.FieldsPerRecord = '\t', 6
	rows, err := r.ReadAll()
	if err != nil || len(rows) != 108 {
		return nil, nil, fmt.Errorf("catalog: incomplete 4K minimum rows")
	}
	sourceURL := "https://bytedance.larkoffice.com/wiki/FXaYwxzJ5i5Zdik32ipcWzt7nxd"
	sources := []CandidateSource{{
		ID: FourKMinimumSourceID, SourceURL: sourceURL, RawSHA256: fourKMinimumSHA256,
		CapturedAt: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), Resolution: "4k",
		Headers: []string{"时长-输出视频（秒）", "最低token用量", "分辨率", "画面比例", "长边（像素）", "短边（像素）"},
		Columns: []Column{{Semantic: "output_duration", Unit: "second", Precision: "exact"}, {Semantic: "minimum_tokens", Unit: "token", Precision: "estimated"}, {Semantic: "resolution"}, {Semantic: "ratio"}, {Semantic: "dimension_pixels", Unit: "pixel", Precision: "exact"}, {Semantic: "dimension_pixels", Unit: "pixel", Precision: "exact"}},
		RawRows: rows,
	}, {
		ID: FourKMinimumCaptureID, SourceURL: sourceURL, RawSHA256: fourKMinimumCaptureSHA256,
		CapturedAt: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
	}}
	cells := make([]ReferenceMinimumCell, 0, 162)
	seen := map[string]bool{}
	for i, row := range rows {
		duration, de := strconv.ParseInt(row[0], 10, 64)
		tokens, te := strconv.ParseInt(row[1], 10, 64)
		if de != nil || te != nil || duration < 4 || duration > 30 || tokens <= 0 || row[2] != "4k" {
			return nil, nil, fmt.Errorf("catalog: invalid 4K minimum row")
		}
		for _, ratio := range strings.Split(row[3], ",") {
			key := fmt.Sprintf("%s/%d", ratio, duration)
			if seen[key] || !strings.Contains("|16:9|9:16|4:3|3:4|1:1|21:9|", "|"+ratio+"|") {
				return nil, nil, fmt.Errorf("catalog: invalid 4K minimum ratio")
			}
			seen[key] = true
			cells = append(cells, ReferenceMinimumCell{Resolution: "4k", Ratio: ratio, OutputDurationMS: duration * 1000, MinimumTokens: tokens, SourceRows: []SourceRow{{SourceID: FourKMinimumSourceID, Row: i + 1}}})
		}
	}
	if len(cells) != 162 {
		return nil, nil, fmt.Errorf("catalog: incomplete 4K minimum domain")
	}
	return cells, sources, nil
}
