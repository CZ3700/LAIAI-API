package arkverification

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/shopspring/decimal"
)

type TariffPrice struct {
	Resolution       string `json:"resolution"`
	InputVideo       bool   `json:"input_video"`
	SupplierCNYPer1M string `json:"supplier_cny_per_1m"`
}

// Tariff is documentary price evidence only. It has no deployment identity,
// minimum usage table, capability, commercial USD price or enabled flag.
type Tariff struct {
	Status     string        `json:"status"`
	Model      string        `json:"model"`
	VerifiedAt time.Time     `json:"verified_at"`
	Unit       string        `json:"unit"`
	Prices     []TariffPrice `json:"prices"`
	Sources    []Source      `json:"sources"`
}

type reviewedVariantTariff struct {
	series, view, hash string
	noVideo, video     string
}

var tariffDuration = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func reviewedVariant(model string) (reviewedVariantTariff, bool) {
	switch model {
	case "seedance-2.0-fast":
		return reviewedVariantTariff{"2.0fast", "vewAf9cSae", "a1012d4429dfdb39e633d8f2840094d3b6fc706ae179461d1de3027487c64e62", "37", "22"}, true
	case "seedance-2.0-mini":
		return reviewedVariantTariff{"2.0mini", "vewFrH7ax8", "5ebbbc6a171376c7fd3f22157f8657a51b7f734a9c7db87a6eca8ba81835a552", "23", "14"}, true
	default:
		return reviewedVariantTariff{}, false
	}
}

// LoadTariff verifies the reviewed variant's own 26-row capture. It deliberately
// does not call Load or ReferenceCandidates, whose minimum/capability profiles
// belong only to the standard models.
func LoadTariff(model string) (Tariff, error) {
	if _, ok := reviewedVariant(model); !ok {
		return Tariff{}, arkpricecatalog.ErrUnsupportedModel
	}
	catalog, err := arkpricecatalog.Load()
	if err != nil {
		return Tariff{}, err
	}
	return variantTariff(catalog, model)
}

func variantTariff(catalog arkpricecatalog.Catalog, model string) (Tariff, error) {
	reviewed, ok := reviewedVariant(model)
	if !ok {
		return Tariff{}, arkpricecatalog.ErrUnsupportedModel
	}
	var table *arkpricecatalog.Table
	var capture *arkpricecatalog.Capture
	for ti := range catalog.Tables {
		t := &catalog.Tables[ti]
		if t.ID != "seedance-2.0-prices" {
			continue
		}
		if table != nil || t.TableID != "tblns3WjGMNbR8sL" || t.Status != "captured" || t.ReferenceVideoScope != "per_row_input_duration" {
			return Tariff{}, fmt.Errorf("tariff: invalid variant table")
		}
		table = t
		for ci := range t.Captures {
			c := &t.Captures[ci]
			if c.ViewID != reviewed.view {
				continue
			}
			if capture != nil {
				return Tariff{}, fmt.Errorf("tariff: duplicate variant capture")
			}
			capture = c
		}
	}
	if table == nil || capture == nil || capture.ExpectedRows != 26 || capture.RawSHA256 != reviewed.hash || capture.FirstRow < 1 || capture.FirstRow-1+capture.ExpectedRows > len(table.RawRows) {
		return Tariff{}, fmt.Errorf("tariff: reviewed variant evidence unavailable")
	}
	cols := map[string]int{}
	for i, column := range capture.Columns {
		switch column.Semantic {
		case "series", "supplier_token_price", "input_duration", "resolution":
			if _, exists := cols[column.Semantic]; exists {
				return Tariff{}, fmt.Errorf("tariff: ambiguous source columns")
			}
			cols[column.Semantic] = i
		}
	}
	if len(cols) != 4 || capture.Columns[cols["supplier_token_price"]].Unit != "CNY/1M_tokens" || capture.Columns[cols["supplier_token_price"]].Precision != "exact" {
		return Tariff{}, fmt.Errorf("tariff: price unit or precision not verified")
	}
	prices := map[string]TariffPrice{}
	for _, row := range table.RawRows[capture.FirstRow-1 : capture.FirstRow-1+capture.ExpectedRows] {
		if len(row) != len(capture.Columns) || row[cols["series"]] != reviewed.series || (row[cols["resolution"]] != "480p" && row[cols["resolution"]] != "720p") || !tariffDuration.MatchString(row[cols["input_duration"]]) {
			return Tariff{}, fmt.Errorf("tariff: variant price conditions changed")
		}
		duration, err := decimal.NewFromString(row[cols["input_duration"]])
		if err != nil {
			return Tariff{}, err
		}
		input := duration.IsPositive()
		want := reviewed.noVideo
		if input {
			want = reviewed.video
		}
		if row[cols["supplier_token_price"]] != want {
			return Tariff{}, fmt.Errorf("tariff: variant price changed; review required")
		}
		resolution := row[cols["resolution"]]
		prices[fmt.Sprintf("%s/%t", resolution, input)] = TariffPrice{resolution, input, want}
	}
	if len(prices) != 4 {
		return Tariff{}, fmt.Errorf("tariff: incomplete variant price coverage")
	}
	out := Tariff{Status: "verified", Model: model, VerifiedAt: time.Date(2026, 10, 19, 7, 54, 10, 0, time.UTC), Unit: "CNY/1M_tokens", Prices: []TariffPrice{}, Sources: []Source{{table.TableID + "/" + capture.ViewID, capture.SourceURL, capture.RawSHA256}}}
	for _, source := range documentationSources() {
		if source.ID == "official-pricing-20261019" {
			out.Sources = append(out.Sources, source)
		}
	}
	if len(out.Sources) != 2 {
		return Tariff{}, fmt.Errorf("tariff: official documentation verification unavailable")
	}
	for _, price := range prices {
		out.Prices = append(out.Prices, price)
	}
	sort.Slice(out.Prices, func(i, j int) bool {
		a, b := out.Prices[i], out.Prices[j]
		if a.Resolution != b.Resolution {
			return a.Resolution < b.Resolution
		}
		return !a.InputVideo && b.InputVideo
	})
	return out, nil
}
