package arkpricecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func catalogFixture(t *testing.T) (Catalog, fstest.MapFS) {
	t.Helper()
	when := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	raw := []byte("2.5\t42\t86468\t3.63\r\n")
	hash := sha256.Sum256(raw)
	c := Catalog{SchemaVersion: 1, Version: "fixture-v1", Title: "Fixture only"}
	view := "vew1"
	for _, id := range []string{"first", "second", "third", "fourth", "fifth", "sixth"} {
		c.Tables = append(c.Tables, Table{ID: id, Title: id, Series: []string{"2.5"}, Status: "missing", TableID: "tbl1", SourceURL: "https://bytedance.larkoffice.com/wiki/fixture?table=tbl1"})
	}
	c.Tables[0] = Table{ID: "first", Title: "Fixture only", Series: []string{"2.5"}, Status: "captured", SourceURL: "https://bytedance.larkoffice.com/wiki/fixture?table=tbl1&view=vew1", TableID: "tbl1", ViewID: &view, CapturedAt: &when, RawFile: "fixture.tsv", RawSHA256: hex.EncodeToString(hash[:]), ExpectedRows: 1,
		ReferenceVideoScope: "requires_reference_video", Headers: []string{"模型", "CNY / 百万 Token", "最低 Token", "示例金额"}, Columns: []Column{{Semantic: "series", Precision: "not_applicable"}, {Semantic: "supplier_token_price", Unit: "CNY/1M_tokens", Precision: "exact"}, {Semantic: "minimum_tokens", Unit: "token", Precision: "estimated"}, {Semantic: "reference_video_amount", Unit: "CNY/video", Precision: "rounded"}}}
	return c, fstest.MapFS{"data/fixture.tsv": &fstest.MapFile{Data: raw}}
}

func manifest(t *testing.T, c Catalog, files fstest.MapFS) {
	t.Helper()
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	files["data/catalog.json"] = &fstest.MapFile{Data: raw}
}

func TestCatalogPreservesEvidenceAndUnitsWithoutDerivation(t *testing.T) {
	c, files := catalogFixture(t)
	manifest(t, c, files)
	got, e := load(files)
	if e != nil {
		t.Fatal(e)
	}
	if got.Complete || got.CapturedTables != 1 || len(got.ManifestSHA256) != 64 || got.Usage != "reference_only_no_price_or_capability_changes" {
		t.Fatalf("incorrect capture state: %+v", got)
	}
	row := got.Tables[0].RawRows[0]
	if strings.Join(row, "|") != "2.5|42|86468|3.63" || got.Tables[0].Columns[3].Precision != "rounded" || got.Tables[1].Status != "missing" || len(got.Tables[1].RawRows) != 0 {
		t.Fatal("source values or missing state changed")
	}
	// Caller changes cannot affect the next request.
	got.Tables[0].RawRows[0][1] = "999"
	again, e := load(files)
	if e != nil || again.Tables[0].RawRows[0][1] != "42" {
		t.Fatal("shared catalog mutation")
	}
}

func TestCatalogRejectsCorruptOrAmbiguousEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Catalog, fstest.MapFS)
	}{
		{"digest", func(_ *Catalog, f fstest.MapFS) { f["data/fixture.tsv"].Data = []byte("2.5\t42\t86468\t9.99\n") }},
		{"row_count", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].ExpectedRows = 2 }},
		{"header_count", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].Headers = c.Tables[0].Headers[:3] }},
		{"source_host", func(c *Catalog, _ fstest.MapFS) {
			c.Tables[0].SourceURL = "https://bytedance.larkoffice.com.attacker.invalid/wiki/fixture?table=tbl1&view=vew1"
		}},
		{"source_table", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].TableID = "another" }},
		{"path", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].RawFile = "../fixture.tsv" }},
		{"rounded_minimum", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].Columns[2].Precision = "rounded" }},
		{"estimated_minimum_mislabeled_exact", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].Columns[2].Precision = "exact" }},
		{"money_as_tokens", func(c *Catalog, _ fstest.MapFS) {
			c.Tables[0].Columns[3] = Column{Semantic: "minimum_tokens", Unit: "token", Precision: "estimated"}
		}},
		{"wrong_currency", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].Columns[1].Unit = "USD/1M_tokens" }},
		{"missing_with_data", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].Status = "missing" }},
		{"duplicate_table", func(c *Catalog, _ fstest.MapFS) { c.Tables[1].ID = c.Tables[0].ID }},
		{"forged_complete", func(c *Catalog, _ fstest.MapFS) { c.Complete = true }},
		{"minimum_duration_is_not_a_request", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].ReferenceVideoScope = "per_row_input_duration" }},
		{"series_must_match_raw_cell", func(c *Catalog, _ fstest.MapFS) { c.Tables[0].Series = []string{"2.0"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, f := catalogFixture(t)
			tc.change(&c, f)
			manifest(t, c, f)
			if _, e := load(f); e == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}

func TestEmbeddedCatalogIntegrity(t *testing.T) {
	c, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if len(c.Tables) != 6 || !c.Complete || c.CapturedTables != 6 {
		t.Fatal("version v1 must contain all six captured official tables")
	}
	wantCounts := map[string]int{"seedance-2.5-prices": 48, "seedance-2.5-minimum": 339, "seedance-2.0-prices": 104, "seedance-2.0-minimum": 192, "seedance-1.5-prices": 12, "seedance-1.0-prices": 24}
	total, views := 0, 0
	for _, table := range c.Tables {
		if len(table.RawRows) != table.ExpectedRows || len(table.RawRows) != wantCounts[table.ID] {
			t.Fatal("truncated embedded table")
		}
		total += len(table.RawRows)
		if len(table.Captures) == 0 {
			views++
		} else {
			views += len(table.Captures)
		}
	}
	if total != 719 || views != 12 {
		t.Fatal("source inventory no longer matches the reviewed capture version")
	}
}

func TestCatalogMultipleViewsRemainTraceableAndComplete(t *testing.T) {
	c, files := catalogFixture(t)
	table := &c.Tables[0]
	first := Capture{Title: "base", ViewID: *table.ViewID, SourceURL: table.SourceURL, CapturedAt: *table.CapturedAt, RawFile: table.RawFile, RawSHA256: table.RawSHA256, ExpectedRows: 1}
	second := first
	second.Title, second.ViewID, second.SourceURL, second.RawFile = "fast", "vew2", "https://bytedance.larkoffice.com/wiki/fixture?table=tbl1&view=vew2", "fast.tsv"
	first.Resolution, second.Resolution = "480p", "720p"
	files["data/fast.tsv"] = &fstest.MapFile{Data: append([]byte(nil), files["data/fixture.tsv"].Data...)}
	table.SourceURL, table.ViewID, table.CapturedAt, table.RawFile, table.RawSHA256 = "https://bytedance.larkoffice.com/wiki/fixture?table=tbl1", nil, nil, "", ""
	table.ExpectedRows, table.ExpectedViews, table.Captures = 2, []string{"vew1", "vew2"}, []Capture{first, second}
	manifest(t, c, files)
	got, e := load(files)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Tables[0].RawRows) != 2 || got.Tables[0].Captures[1].FirstRow != 2 || got.Tables[0].RawSHA256 != "" || got.Tables[0].CapturedAt == nil || got.Tables[0].Captures[1].Resolution != "720p" || len(got.Tables[0].RawRows[1]) != 4 {
		t.Fatal("source view provenance lost or a fake aggregate source created")
	}
	table.Captures = table.Captures[:1]
	manifest(t, c, files)
	if _, e = load(files); e == nil {
		t.Fatal("partial views accepted as complete table")
	}
	table.Captures = []Capture{first, first}
	manifest(t, c, files)
	if _, e = load(files); e == nil {
		t.Fatal("duplicate view accepted as complete table")
	}
}

func TestEmbeddedMinimumViewsPreserveOriginalSchemas(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var table *Table
	for i := range c.Tables {
		if c.Tables[i].ID == "seedance-2.0-minimum" {
			table = &c.Tables[i]
		}
	}
	if table == nil || !table.HeterogeneousViews || len(table.Captures) != 4 || len(table.RawRows) != 192 || table.ReferenceVideoScope != "requires_reference_video" {
		t.Fatal("minimum view provenance or scope lost")
	}
	for _, capture := range table.Captures {
		wantColumns := 6
		if capture.Resolution == "4k" {
			wantColumns = 7
			if capture.Headers[3] != "分辨率" || capture.Columns[3].Semantic != "resolution" {
				t.Fatal("4k resolution column shifted")
			}
		}
		if len(capture.Headers) != wantColumns || len(capture.Columns) != wantColumns || capture.ExpectedRows != 48 || capture.Columns[2].Semantic != "minimum_tokens" || capture.Columns[2].Precision != "estimated" {
			t.Fatalf("view schema or estimated meaning lost: %s", capture.Title)
		}
		for _, row := range table.RawRows[capture.FirstRow-1 : capture.FirstRow-1+capture.ExpectedRows] {
			if len(row) != wantColumns || capture.Resolution == "4k" && row[3] != "4k" {
				t.Fatal("original row padded or columns reordered")
			}
		}
	}
}
