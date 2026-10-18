package arkpricecatalog

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReferenceCandidatesPricesMinimumAndProvenance(t *testing.T) {
	for _, tc := range []struct {
		model                        string
		prices, minimums, sourceRows int
	}{
		{"seedance-2.0", 8, 288, 244}, {"seedance-2.5", 6, 486, 387},
	} {
		t.Run(tc.model, func(t *testing.T) {
			c, e := ReferenceCandidates(tc.model)
			if e != nil {
				t.Fatal(e)
			}
			if len(c.Prices) != tc.prices || len(c.Minimum.Cells) != tc.minimums {
				t.Fatalf("prices=%d minimums=%d", len(c.Prices), len(c.Minimum.Cells))
			}
			if c.Usage != "reference_candidates_only" || c.Minimum.Precision != "estimated" || c.Minimum.Purpose != "hold_estimation_only" || c.Minimum.Status != "requires_operator_verification" {
				t.Fatal("unsafe candidate semantics")
			}
			sources := map[string]CandidateSource{}
			rows := 0
			for _, s := range c.Sources {
				sources[s.ID] = s
				rows += len(s.RawRows)
				if s.RawSHA256 == "" || s.CapturedAt.IsZero() || len(s.Headers) != len(s.Columns) {
					t.Fatal("missing provenance")
				}
			}
			if rows != tc.sourceRows {
				t.Fatalf("source rows=%d", rows)
			}
			for _, p := range c.Prices {
				if p.Mode != "generate" || p.ServiceTier != "default" || p.Draft || p.Unit != "CNY/1M_tokens" || p.Precision != "exact" {
					t.Fatal("wrong price semantics")
				}
				want := "46"
				if p.InputVideo {
					want = "28"
				}
				if p.Resolution == "1080p" {
					want = "51"
					if p.InputVideo {
						want = "31"
					}
				}
				if p.Resolution == "4k" {
					want = "26"
					if p.InputVideo {
						want = "16"
					}
				}
				if tc.model == "seedance-2.5" {
					want = "70"
					if p.InputVideo {
						want = "42"
					}
					if p.Resolution == "1080p" {
						want = "77"
						if p.InputVideo {
							want = "46"
						}
					}
				}
				if p.SupplierCNYPer1M != want {
					t.Fatalf("%s: %s != %s", p.Key, p.SupplierCNYPer1M, want)
				}
				for _, ref := range p.SourceRows {
					s, ok := sources[ref.SourceID]
					if !ok || ref.Row < 1 || ref.Row > len(s.RawRows) {
						t.Fatal("untraceable price")
					}
				}
			}
			dups := 0
			for _, cell := range c.Minimum.Cells {
				if cell.MinimumTokens <= 0 || cell.OutputDurationMS%1000 != 0 {
					t.Fatal("invalid minimum")
				}
				if tc.model == "seedance-2.0" && cell.FPS != nil {
					t.Fatal("invented missing FPS")
				}
				if tc.model == "seedance-2.5" && (cell.FPS == nil || *cell.FPS != 24) {
					t.Fatal("primary FPS lost")
				}
				if len(cell.SourceRows) > 1 {
					dups++
				}
				for _, ref := range cell.SourceRows {
					s, ok := sources[ref.SourceID]
					if !ok || ref.Row < 1 || ref.Row > len(s.RawRows) {
						t.Fatal("untraceable minimum")
					}
				}
			}
			if tc.model == "seedance-2.5" && dups != 15 {
				t.Fatalf("duplicate evidence not retained: %d", dups)
			}
			if tc.model == "seedance-2.0" && !reflect.DeepEqual(c.Minimum.Issues, []string{"estimated_reference_requires_verification", "fps_not_in_source"}) {
				t.Fatal(c.Minimum.Issues)
			}
			c2, e := ReferenceCandidates(tc.model)
			if e != nil {
				t.Fatal(e)
			}
			a, _ := json.Marshal(c)
			b, _ := json.Marshal(c2)
			if string(a) != string(b) {
				t.Fatal("nondeterministic candidates")
			}
			c.Sources[0].RawRows[0][0] = "caller mutation"
			c3, _ := ReferenceCandidates(tc.model)
			if c3.Sources[0].RawRows[0][0] == "caller mutation" {
				t.Fatal("shared source mutation")
			}
		})
	}
}

func TestReferenceCandidatesRejectUnsupportedAndConflictingEvidence(t *testing.T) {
	for _, model := range []string{"seedance-1.0", "seedance-1.5", "seedance-2.0-fast", "seedance-2.0-mini", "doubao-seedance-2-0-260128", "unknown"} {
		if _, e := ReferenceCandidates(model); !errors.Is(e, ErrUnsupportedModel) {
			t.Fatalf("accepted %s", model)
		}
	}
	for _, kind := range []string{"price", "minimum", "price_unit", "series"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := Load()
			for ti := range c.Tables {
				table := &c.Tables[ti]
				if kind == "minimum" && table.ID == "seedance-2.5-minimum" {
					row := append([]string(nil), table.RawRows[0]...)
					row[3] = "1"
					table.RawRows = append(table.RawRows, row)
				}
				if table.ID == "seedance-2.5-prices" {
					switch kind {
					case "price":
						table.RawRows[1][1] = "1"
					case "price_unit":
						table.Columns[1].Unit = "CNY/video"
					case "series":
						table.RawRows[0][0] = "2.0fast"
					}
				}
			}
			_, e := referenceCandidates(c, "seedance-2.5")
			if e == nil {
				t.Fatal("accepted conflicting evidence")
			}
			if kind == "minimum" && !strings.Contains(e.Error(), "conflicting minimum") {
				t.Fatal(e)
			}
		})
	}
}
