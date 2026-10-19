package arkpricecatalog

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var ErrUnsupportedModel = errors.New("catalog: unsupported candidate model")

type SourceRow struct {
	SourceID string `json:"source_id"`
	Row      int    `json:"row"`
}

// CandidateSource keeps the original view schema and row bytes' digest. Expanded
// ratios and deduplicated cells point back to one-based rows in this view.
type CandidateSource struct {
	ID         string     `json:"id"`
	TableID    string     `json:"table_id"`
	ViewID     string     `json:"view_id"`
	SourceURL  string     `json:"source_url"`
	CapturedAt time.Time  `json:"captured_at"`
	RawSHA256  string     `json:"raw_sha256"`
	Resolution string     `json:"resolution,omitempty"`
	Headers    []string   `json:"headers"`
	Columns    []Column   `json:"columns"`
	RawRows    [][]string `json:"raw_rows"`
}

type ReferencePrice struct {
	Key              string      `json:"key"`
	Mode             string      `json:"mode"`
	Resolution       string      `json:"resolution"`
	InputVideo       bool        `json:"input_video"`
	ServiceTier      string      `json:"service_tier"`
	Draft            bool        `json:"draft"`
	SupplierCNYPer1M string      `json:"supplier_cny_per_1m"`
	Unit             string      `json:"unit"`
	Precision        string      `json:"precision"`
	SourceRows       []SourceRow `json:"source_rows"`
}

type ReferenceMinimumCell struct {
	Resolution       string      `json:"resolution"`
	Ratio            string      `json:"ratio"`
	OutputDurationMS int64       `json:"output_duration_ms"`
	FPS              *int        `json:"fps"`
	MinimumTokens    int64       `json:"minimum_tokens"`
	SourceRows       []SourceRow `json:"source_rows"`
}

type ReferenceMinimum struct {
	Precision string                 `json:"precision"`
	Purpose   string                 `json:"purpose"`
	Status    string                 `json:"status"`
	Cells     []ReferenceMinimumCell `json:"cells"`
	Issues    []string               `json:"issues"`
}

type Candidates struct {
	Model          string            `json:"model"`
	CatalogVersion string            `json:"catalog_version"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	Usage          string            `json:"usage"`
	Prices         []ReferencePrice  `json:"prices"`
	Minimum        ReferenceMinimum  `json:"minimum"`
	Sources        []CandidateSource `json:"sources"`
}

// ReferenceCandidates never imports minimum rules or writes a price. Missing
// evidence (including FPS) stays missing; estimated floors are not actual usage.
func ReferenceCandidates(model string) (Candidates, error) {
	if _, ok := candidateIdentity(model); !ok {
		return Candidates{}, ErrUnsupportedModel
	}
	c, err := Load()
	if err != nil {
		return Candidates{}, err
	}
	return referenceCandidates(c, model)
}

type candidateModelIdentity struct {
	priceTable, minimumTable, priceView, series string
	variant                                     bool
}

// The variant minimum source is the explicitly documented 2.0 family table,
// not a standard-model profile or the 26-row price examples. The verification
// package independently binds the official family definition and deployment ID.
func candidateIdentity(model string) (candidateModelIdentity, bool) {
	switch model {
	case "seedance-2.0":
		return candidateModelIdentity{"seedance-2.0-prices", "seedance-2.0-minimum", "vew7f33fzS", "2.0", false}, true
	case "seedance-2.5":
		return candidateModelIdentity{"seedance-2.5-prices", "seedance-2.5-minimum", "", "2.5", false}, true
	case "seedance-2.0-fast":
		return candidateModelIdentity{"seedance-2.0-prices", "seedance-2.0-minimum", "vewAf9cSae", "2.0fast", true}, true
	case "seedance-2.0-mini":
		return candidateModelIdentity{"seedance-2.0-prices", "seedance-2.0-minimum", "vewFrH7ax8", "2.0mini", true}, true
	default:
		return candidateModelIdentity{}, false
	}
}

func referenceCandidates(c Catalog, model string) (Candidates, error) {
	identity, ok := candidateIdentity(model)
	if !ok {
		return Candidates{}, ErrUnsupportedModel
	}
	out := Candidates{Model: model, CatalogVersion: c.Version, ManifestSHA256: c.ManifestSHA256, Usage: "reference_candidates_only", Prices: []ReferencePrice{}, Sources: []CandidateSource{}, Minimum: ReferenceMinimum{Precision: "estimated", Purpose: "hold_estimation_only", Status: "requires_operator_verification", Cells: []ReferenceMinimumCell{}, Issues: []string{"estimated_reference_requires_verification"}}}
	prices := map[string]int{}
	minimums := map[string]int{}
	resolutions := map[string]bool{"480p": true, "720p": true, "1080p": true}
	if identity.variant {
		delete(resolutions, "1080p")
	}
	if model == "seedance-2.0" {
		resolutions["4k"] = true
	}
	missingFPS := false
	for _, t := range c.Tables {
		isPrice := t.ID == identity.priceTable
		if !isPrice && t.ID != identity.minimumTable {
			continue
		}
		if t.Status != "captured" {
			return Candidates{}, fmt.Errorf("catalog: missing candidate source")
		}
		captures := t.Captures
		if len(captures) == 0 {
			if t.ViewID == nil || t.CapturedAt == nil {
				return Candidates{}, fmt.Errorf("catalog: missing source metadata")
			}
			captures = []Capture{{ViewID: *t.ViewID, SourceURL: t.SourceURL, CapturedAt: *t.CapturedAt, RawSHA256: t.RawSHA256, FirstRow: 1, ExpectedRows: len(t.RawRows), Headers: t.Headers, Columns: t.Columns}}
		}
		for _, capture := range captures {
			// Exact view identities prevent substituting another variant's rate.
			if isPrice && identity.priceView != "" && capture.ViewID != identity.priceView {
				continue
			}
			if !isPrice && identity.variant && capture.ViewID != "vew2CR2JYM" && capture.ViewID != "vewVBehIlq" {
				continue
			}
			start, end := capture.FirstRow-1, capture.FirstRow-1+capture.ExpectedRows
			if start < 0 || end > len(t.RawRows) || end <= start {
				return Candidates{}, fmt.Errorf("catalog: invalid source range")
			}
			source := CandidateSource{ID: t.TableID + "/" + capture.ViewID, TableID: t.TableID, ViewID: capture.ViewID, SourceURL: capture.SourceURL, CapturedAt: capture.CapturedAt, RawSHA256: capture.RawSHA256, Resolution: capture.Resolution, Headers: capture.Headers, Columns: capture.Columns, RawRows: t.RawRows[start:end]}
			cols := map[string]int{}
			for i, col := range source.Columns {
				// The original minimum sheet also includes linked/derived FPS(1)
				// and repeated pixel columns. Preserve them in evidence, but only
				// the explicitly named primary FPS field defines a candidate.
				if col.Semantic == "fps" && source.Headers[i] != "帧率" {
					continue
				}
				switch col.Semantic {
				case "series", "supplier_token_price", "input_duration", "output_duration", "minimum_tokens", "resolution", "ratio", "fps":
				default:
					continue
				}
				if _, exists := cols[col.Semantic]; exists {
					return Candidates{}, fmt.Errorf("catalog: ambiguous semantic column")
				}
				cols[col.Semantic] = i
			}
			get := func(row []string, semantic string) string {
				if i, ok := cols[semantic]; ok && i < len(row) {
					return row[i]
				}
				return ""
			}
			out.Sources = append(out.Sources, source)
			for ri, row := range source.RawRows {
				if len(row) != len(source.Columns) {
					return Candidates{}, fmt.Errorf("catalog: source schema mismatch")
				}
				ref := SourceRow{SourceID: source.ID, Row: ri + 1}
				res := get(row, "resolution")
				if res == "" {
					res = source.Resolution
				}
				if !resolutions[res] || source.Resolution != "" && source.Resolution != res {
					return Candidates{}, fmt.Errorf("catalog: invalid resolution")
				}
				if isPrice {
					if get(row, "series") != identity.series {
						return Candidates{}, fmt.Errorf("catalog: candidate series mismatch")
					}
					idx, ok := cols["supplier_token_price"]
					if !ok || source.Columns[idx].Unit != "CNY/1M_tokens" || source.Columns[idx].Precision != "exact" {
						return Candidates{}, fmt.Errorf("catalog: invalid price unit")
					}
					rate, e := positiveDecimal(get(row, "supplier_token_price"))
					if e != nil {
						return Candidates{}, e
					}
					duration := get(row, "input_duration")
					if !number.MatchString(duration) {
						return Candidates{}, fmt.Errorf("catalog: invalid input duration")
					}
					d, e := decimal.NewFromString(duration)
					if e != nil || d.IsNegative() {
						return Candidates{}, fmt.Errorf("catalog: invalid input duration")
					}
					input := d.IsPositive()
					key := fmt.Sprintf("generate/%s/%t/default/false", res, input)
					if i, ok := prices[key]; ok {
						if out.Prices[i].SupplierCNYPer1M != rate.String() {
							return Candidates{}, fmt.Errorf("catalog: conflicting price cell %s", key)
						}
						out.Prices[i].SourceRows = append(out.Prices[i].SourceRows, ref)
					} else {
						prices[key] = len(out.Prices)
						out.Prices = append(out.Prices, ReferencePrice{Key: key, Mode: "generate", Resolution: res, InputVideo: input, ServiceTier: "default", SupplierCNYPer1M: rate.String(), Unit: "CNY/1M_tokens", Precision: "exact", SourceRows: []SourceRow{ref}})
					}
					continue
				}
				idx, ok := cols["minimum_tokens"]
				if !ok || source.Columns[idx].Precision != "estimated" || source.Columns[idx].Unit != "token" {
					return Candidates{}, fmt.Errorf("catalog: invalid minimum unit")
				}
				tokens, e := positiveInteger(get(row, "minimum_tokens"))
				if e != nil {
					return Candidates{}, e
				}
				d, e := positiveDecimal(get(row, "output_duration"))
				if e != nil {
					return Candidates{}, e
				}
				ms := d.Mul(decimal.NewFromInt(1000))
				if !ms.Equal(ms.Truncate(0)) || ms.GreaterThan(decimal.NewFromInt(600000)) {
					return Candidates{}, fmt.Errorf("catalog: invalid output duration")
				}
				var fps *int
				if value := get(row, "fps"); value != "" {
					f, e := positiveInteger(value)
					if e != nil || f > 240 {
						return Candidates{}, fmt.Errorf("catalog: invalid FPS")
					}
					v := int(f)
					fps = &v
				} else {
					missingFPS = true
				}
				seenRatios := map[string]bool{}
				for _, ratio := range strings.Split(get(row, "ratio"), ",") {
					if !validCandidateRatio(ratio) || seenRatios[ratio] {
						return Candidates{}, fmt.Errorf("catalog: invalid ratio")
					}
					seenRatios[ratio] = true
					key := fmt.Sprintf("%s/%s/%d", res, ratio, ms.IntPart())
					if i, ok := minimums[key]; ok {
						old := &out.Minimum.Cells[i]
						if old.MinimumTokens != tokens || (old.FPS == nil) != (fps == nil) || fps != nil && *old.FPS != *fps {
							return Candidates{}, fmt.Errorf("catalog: conflicting minimum cell %s", key)
						}
						old.SourceRows = append(old.SourceRows, ref)
					} else {
						minimums[key] = len(out.Minimum.Cells)
						out.Minimum.Cells = append(out.Minimum.Cells, ReferenceMinimumCell{Resolution: res, Ratio: ratio, OutputDurationMS: ms.IntPart(), FPS: fps, MinimumTokens: tokens, SourceRows: []SourceRow{ref}})
					}
				}
			}
		}
	}
	if len(out.Prices) != len(resolutions)*2 || len(out.Minimum.Cells) == 0 {
		return Candidates{}, fmt.Errorf("catalog: incomplete candidate coverage")
	}
	if missingFPS {
		out.Minimum.Issues = append(out.Minimum.Issues, "fps_not_in_source")
	}
	sort.Slice(out.Prices, func(i, j int) bool { return out.Prices[i].Key < out.Prices[j].Key })
	sort.Slice(out.Minimum.Cells, func(i, j int) bool {
		a, b := out.Minimum.Cells[i], out.Minimum.Cells[j]
		if a.Resolution != b.Resolution {
			return a.Resolution < b.Resolution
		}
		if a.Ratio != b.Ratio {
			return a.Ratio < b.Ratio
		}
		return a.OutputDurationMS < b.OutputDurationMS
	})
	return out, nil
}

func positiveDecimal(s string) (decimal.Decimal, error) {
	if !number.MatchString(s) {
		return decimal.Zero, fmt.Errorf("catalog: invalid positive decimal")
	}
	d, e := decimal.NewFromString(s)
	if e != nil || !d.IsPositive() {
		return decimal.Zero, fmt.Errorf("catalog: invalid positive decimal")
	}
	return d, nil
}
func positiveInteger(s string) (int64, error) {
	if !integer.MatchString(s) {
		return 0, fmt.Errorf("catalog: invalid positive integer")
	}
	v, e := strconv.ParseInt(s, 10, 64)
	if e != nil {
		return 0, e
	}
	return v, nil
}
func validCandidateRatio(s string) bool {
	switch s {
	case "16:9", "9:16", "4:3", "3:4", "1:1", "21:9":
		return true
	}
	return false
}
