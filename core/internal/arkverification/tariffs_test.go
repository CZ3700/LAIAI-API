package arkverification

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
)

func TestVerifiedVariantTariffsRemainDocumentaryOnly(t *testing.T) {
	for _, tc := range []struct{ model, plain, video, hash string }{
		{"seedance-2.0-fast", "37", "22", "a1012d4429dfdb39e633d8f2840094d3b6fc706ae179461d1de3027487c64e62"},
		{"seedance-2.0-mini", "23", "14", "5ebbbc6a171376c7fd3f22157f8657a51b7f734a9c7db87a6eca8ba81835a552"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			got, e := LoadTariff(tc.model)
			if e != nil {
				t.Fatal(e)
			}
			if got.Status != "verified" || got.Model != tc.model || got.Unit != "CNY/1M_tokens" || len(got.Prices) != 4 || len(got.Sources) != 2 || got.Sources[0].SHA256 != tc.hash || got.Sources[1].ID != "official-pricing-20261019" || len(got.Sources[1].SHA256) != 64 {
				t.Fatal("incomplete independent evidence", got)
			}
			seen := map[string]bool{}
			for _, price := range got.Prices {
				want := tc.plain
				if price.InputVideo {
					want = tc.video
				}
				if price.SupplierCNYPer1M != want || (price.Resolution != "480p" && price.Resolution != "720p") {
					t.Fatal("wrong variant tariff", price)
				}
				key := price.Resolution + "/" + price.SupplierCNYPer1M
				if seen[key] {
					t.Fatal("duplicate condition")
				}
				seen[key] = true
			}
			raw, _ := json.Marshal(got)
			for _, forbidden := range []string{"minimum", "capability", "enabled", "retail", "USD"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatal("commercial or standard capability data included", forbidden)
				}
			}
			if _, e := Load(tc.model); e == nil {
				t.Fatal("variant capability was opened")
			}
			if _, e := arkpricecatalog.ReferenceCandidates(tc.model); e == nil {
				t.Fatal("variant entered standard pricing publication candidates")
			}
			got.Prices[0].SupplierCNYPer1M = "999"
			got.Sources[0].SHA256 = "changed"
			again, e := LoadTariff(tc.model)
			if e != nil || again.Prices[0].SupplierCNYPer1M == "999" || again.Sources[0].SHA256 != tc.hash {
				t.Fatal("mutable tariff alias")
			}
		})
	}
	for _, model := range []string{"seedance-2.0", "seedance-2.5-fast", "seedance-2.5-mini", "seedance-1.5-pro", "doubao-seedance-2-0-fast-260128"} {
		if _, e := LoadTariff(model); e == nil {
			t.Fatal("noncanonical/unsupported tariff accepted", model)
		}
	}
}

func TestVariantTariffRequiresEveryReviewedSourceRow(t *testing.T) {
	for _, mutation := range []string{"rate", "last_row_rate", "series", "resolution", "input_duration", "precision", "unit", "digest", "row_count", "missing_view", "missing_condition"} {
		t.Run(mutation, func(t *testing.T) {
			catalog, e := arkpricecatalog.Load()
			if e != nil {
				t.Fatal(e)
			}
			var table *arkpricecatalog.Table
			var capture *arkpricecatalog.Capture
			for i := range catalog.Tables {
				if catalog.Tables[i].ID == "seedance-2.0-prices" {
					table = &catalog.Tables[i]
				}
			}
			for i := range table.Captures {
				if table.Captures[i].ViewID == "vewAf9cSae" {
					capture = &table.Captures[i]
				}
			}
			cols := map[string]int{}
			for i, col := range capture.Columns {
				cols[col.Semantic] = i
			}
			row := table.RawRows[capture.FirstRow-1]
			switch mutation {
			case "rate":
				row[cols["supplier_token_price"]] = "38"
			case "last_row_rate":
				table.RawRows[capture.FirstRow+capture.ExpectedRows-2][cols["supplier_token_price"]] = "99"
			case "series":
				row[cols["series"]] = "2.0mini"
			case "resolution":
				row[cols["resolution"]] = "1080p"
			case "input_duration":
				row[cols["input_duration"]] = "-1"
			case "precision":
				capture.Columns[cols["supplier_token_price"]].Precision = "estimated"
			case "unit":
				capture.Columns[cols["supplier_token_price"]].Unit = "USD/1M_tokens"
			case "digest":
				capture.RawSHA256 = strings.Repeat("a", 64)
			case "row_count":
				capture.ExpectedRows = 25
			case "missing_view":
				capture.ViewID = "other-view"
			case "missing_condition":
				for _, r := range table.RawRows[capture.FirstRow-1 : capture.FirstRow-1+capture.ExpectedRows] {
					r[cols["resolution"]] = "480p"
				}
			}
			if _, e := variantTariff(catalog, "seedance-2.0-fast"); e == nil {
				t.Fatal("changed evidence still verified")
			}
		})
	}
}
