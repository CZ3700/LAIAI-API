// Package arkpricecatalog exposes immutable official reference tables. It has no
// database or billing dependency and cannot publish prices or enable models.
package arkpricecatalog

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed data/*
var data embed.FS

type Column struct {
	Semantic      string   `json:"semantic"`
	Unit          string   `json:"unit"`
	Precision     string   `json:"precision"`
	MissingValues []string `json:"missing_values,omitempty"`
}

// Capture preserves one source view independently. Multi-view tables are only
// concatenated for display; each source file and its original row order survive.
type Capture struct {
	Title        string    `json:"title"`
	SourceURL    string    `json:"source_url"`
	ViewID       string    `json:"view_id"`
	CapturedAt   time.Time `json:"captured_at"`
	RawFile      string    `json:"raw_file"`
	RawSHA256    string    `json:"raw_sha256"`
	ExpectedRows int       `json:"expected_rows"`
	FirstRow     int       `json:"first_row"`            // one-based display offset, derived by loader
	Resolution   string    `json:"resolution,omitempty"` // source view context, never a fabricated raw cell
	Headers      []string  `json:"headers,omitempty"`
	Columns      []Column  `json:"columns,omitempty"`
}

type Table struct {
	ID                  string     `json:"id"`
	Title               string     `json:"title"`
	Series              []string   `json:"series"`
	Status              string     `json:"status"`
	SourceURL           string     `json:"source_url"`
	TableID             string     `json:"table_id"`
	ViewID              *string    `json:"view_id"`
	CapturedAt          *time.Time `json:"captured_at"`
	RawFile             string     `json:"raw_file"`
	RawSHA256           string     `json:"raw_sha256"`
	ExpectedRows        int        `json:"expected_rows"`
	Headers             []string   `json:"headers"`
	Columns             []Column   `json:"columns"`
	Notes               []string   `json:"notes"`
	RawRows             [][]string `json:"raw_rows"`
	ExpectedViews       []string   `json:"expected_views,omitempty"`
	Captures            []Capture  `json:"captures,omitempty"`
	ReferenceVideoScope string     `json:"reference_video_scope"`
	HeterogeneousViews  bool       `json:"heterogeneous_views"` // derived, raw rows retain each view's schema
}

type Catalog struct {
	SchemaVersion  int     `json:"schema_version"`
	Version        string  `json:"version"`
	Title          string  `json:"title"`
	Tables         []Table `json:"tables"`
	ManifestSHA256 string  `json:"manifest_sha256"`
	CapturedTables int     `json:"captured_tables"`
	Complete       bool    `json:"complete"`
	Usage          string  `json:"usage"`
}

// Load verifies the embedded bytes on every read and returns fresh owned slices.
// No caller can mutate a shared catalog or substitute an external URL/file.
func Load() (Catalog, error) { return load(data) }

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
var number = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
var integer = regexp.MustCompile(`^[1-9][0-9]*$`)

func load(files fs.FS) (Catalog, error) {
	var out Catalog
	raw, err := fs.ReadFile(files, "data/catalog.json")
	if err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&out); err != nil {
		return Catalog{}, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Catalog{}, fmt.Errorf("catalog: trailing document")
	}
	if out.SchemaVersion != 1 || !identifier.MatchString(out.Version) || out.Title == "" || len(out.Tables) != 6 || out.ManifestSHA256 != "" || out.CapturedTables != 0 || out.Complete || out.Usage != "" {
		return Catalog{}, fmt.Errorf("catalog: invalid manifest")
	}
	seen := map[string]bool{}
	for i := range out.Tables {
		t := &out.Tables[i]
		if !identifier.MatchString(t.ID) || seen[t.ID] || t.Title == "" || len(t.Series) == 0 || len(t.RawRows) != 0 || t.HeterogeneousViews {
			return Catalog{}, fmt.Errorf("catalog: invalid table %d", i)
		}
		seen[t.ID] = true
		for _, series := range t.Series {
			if !identifier.MatchString(series) {
				return Catalog{}, fmt.Errorf("catalog: invalid series")
			}
		}
		u, e := url.Parse(t.SourceURL)
		if e != nil || u.Scheme != "https" || u.Host != "bytedance.larkoffice.com" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/wiki/") {
			return Catalog{}, fmt.Errorf("catalog: invalid official source")
		}
		t.RawRows = [][]string{}
		if !identifier.MatchString(t.TableID) || u.Query().Get("table") != t.TableID || t.ViewID != nil && (!identifier.MatchString(*t.ViewID) || u.Query().Get("view") != *t.ViewID) || t.ViewID == nil && u.Query().Get("view") != "" {
			return Catalog{}, fmt.Errorf("catalog: source table/view mismatch")
		}
		if t.Status == "missing" {
			if t.CapturedAt != nil || t.RawFile != "" || t.RawSHA256 != "" || t.ExpectedRows != 0 || len(t.Headers) != 0 || len(t.Columns) != 0 || len(t.Captures) != 0 {
				return Catalog{}, fmt.Errorf("catalog: missing table contains captured data")
			}
			continue
		}
		if t.Status != "captured" || t.ExpectedRows < 1 || t.ExpectedRows > 10000 || len(t.Headers) == 0 || len(t.Headers) > 100 || len(t.Headers) != len(t.Columns) {
			return Catalog{}, fmt.Errorf("catalog: incomplete capture metadata for %s", t.ID)
		}
		for col, h := range t.Headers {
			if h == "" || !validColumn(t.Columns[col]) {
				return Catalog{}, fmt.Errorf("catalog: invalid column %s/%d", t.ID, col)
			}
		}
		if t.ReferenceVideoScope != "per_row_input_duration" && t.ReferenceVideoScope != "requires_reference_video" && t.ReferenceVideoScope != "unspecified" {
			return Catalog{}, fmt.Errorf("catalog: invalid reference video scope")
		}
		hasInputDuration := false
		for _, col := range t.Columns {
			hasInputDuration = hasInputDuration || col.Semantic == "input_duration"
			if col.Semantic == "minimum_tokens" && t.ReferenceVideoScope != "requires_reference_video" {
				return Catalog{}, fmt.Errorf("catalog: minimum reference scope must be explicit")
			}
		}
		if t.ReferenceVideoScope == "per_row_input_duration" && !hasInputDuration {
			return Catalog{}, fmt.Errorf("catalog: reference video duration column missing")
		}
		if len(t.Captures) == 0 {
			if t.CapturedAt == nil || t.ViewID == nil || len(t.ExpectedViews) != 0 {
				return Catalog{}, fmt.Errorf("catalog: single view evidence missing")
			}
			rows, e := readCapture(files, t, Capture{Title: t.Title, SourceURL: t.SourceURL, ViewID: *t.ViewID, CapturedAt: *t.CapturedAt, RawFile: t.RawFile, RawSHA256: t.RawSHA256, ExpectedRows: t.ExpectedRows})
			if e != nil {
				return Catalog{}, e
			}
			t.RawRows = rows
		} else {
			if len(t.Captures) < 2 || len(t.Captures) > 20 || len(t.ExpectedViews) != len(t.Captures) || t.ViewID != nil || t.CapturedAt != nil || t.RawFile != "" || t.RawSHA256 != "" {
				return Catalog{}, fmt.Errorf("catalog: ambiguous multi-view evidence")
			}
			views, filesSeen := map[string]bool{}, map[string]bool{}
			for _, view := range t.ExpectedViews {
				if !identifier.MatchString(view) || views[view] {
					return Catalog{}, fmt.Errorf("catalog: invalid expected view")
				}
				views[view] = true
			}
			var latest time.Time
			for ci := range t.Captures {
				capture := &t.Captures[ci]
				if !views[capture.ViewID] || filesSeen[capture.RawFile] || capture.FirstRow != 0 {
					return Catalog{}, fmt.Errorf("catalog: duplicated or unexpected source view")
				}
				delete(views, capture.ViewID)
				filesSeen[capture.RawFile] = true
				rows, e := readCapture(files, t, *capture)
				if e != nil {
					return Catalog{}, e
				}
				capture.FirstRow = len(t.RawRows) + 1
				if len(capture.Headers) == 0 {
					capture.Headers = append([]string(nil), t.Headers...)
					capture.Columns = append([]Column(nil), t.Columns...)
				} else if !reflect.DeepEqual(capture.Headers, t.Headers) || !reflect.DeepEqual(capture.Columns, t.Columns) {
					t.HeterogeneousViews = true
				}
				t.RawRows = append(t.RawRows, rows...)
				if capture.CapturedAt.After(latest) {
					latest = capture.CapturedAt
				}
			}
			if len(views) != 0 || len(t.RawRows) != t.ExpectedRows {
				return Catalog{}, fmt.Errorf("catalog: incomplete source views")
			}
			t.CapturedAt = &latest
		}
		out.CapturedTables++
	}
	hash := sha256.Sum256(raw)
	out.ManifestSHA256 = hex.EncodeToString(hash[:])
	out.Complete = out.CapturedTables == len(out.Tables)
	out.Usage = "reference_only_no_price_or_capability_changes"
	return out, nil
}

func readCapture(files fs.FS, t *Table, c Capture) ([][]string, error) {
	u, e := url.Parse(c.SourceURL)
	if e != nil || u.Scheme != "https" || u.Host != "bytedance.larkoffice.com" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/wiki/") || u.Query().Get("table") != t.TableID || u.Query().Get("view") != c.ViewID || !identifier.MatchString(c.ViewID) || c.Title == "" || c.CapturedAt.IsZero() || c.CapturedAt.Location() != time.UTC || c.ExpectedRows < 1 || c.ExpectedRows > 10000 || path.Base(c.RawFile) != c.RawFile || !strings.HasSuffix(c.RawFile, ".tsv") || c.Resolution != "" && !identifier.MatchString(c.Resolution) {
		return nil, fmt.Errorf("catalog: incomplete view metadata")
	}
	headers, columns := t.Headers, t.Columns
	if len(c.Headers) != 0 || len(c.Columns) != 0 {
		headers, columns = c.Headers, c.Columns
	}
	if len(headers) == 0 || len(headers) > 100 || len(headers) != len(columns) {
		return nil, fmt.Errorf("catalog: invalid view columns")
	}
	hasInputDuration := false
	for ci, header := range headers {
		if header == "" || !validColumn(columns[ci]) || columns[ci].Semantic == "minimum_tokens" && t.ReferenceVideoScope != "requires_reference_video" {
			return nil, fmt.Errorf("catalog: invalid view column semantics")
		}
		hasInputDuration = hasInputDuration || columns[ci].Semantic == "input_duration"
	}
	if t.ReferenceVideoScope == "per_row_input_duration" && !hasInputDuration {
		return nil, fmt.Errorf("catalog: view input duration missing")
	}
	raw, e := fs.ReadFile(files, "data/"+c.RawFile)
	if e != nil {
		return nil, e
	}
	if len(raw) > 5<<20 || !utf8.Valid(raw) {
		return nil, fmt.Errorf("catalog: invalid source encoding or size")
	}
	hash := sha256.Sum256(raw)
	if c.RawSHA256 != hex.EncodeToString(hash[:]) {
		return nil, fmt.Errorf("catalog: digest mismatch for %s", c.RawFile)
	}
	r := csv.NewReader(bytes.NewReader(raw))
	r.Comma = '\t'
	r.FieldsPerRecord = len(headers)
	rows, e := r.ReadAll()
	if e != nil || len(rows) != c.ExpectedRows {
		return nil, fmt.Errorf("catalog: row/column count mismatch for %s", c.RawFile)
	}
	for ri, row := range rows {
		for ci, value := range row {
			if !validValue(columns[ci], value) {
				return nil, fmt.Errorf("catalog: invalid value at %s/%d/%d", c.RawFile, ri+1, ci+1)
			}
			if columns[ci].Semantic == "series" {
				found := false
				for _, series := range t.Series {
					found = found || series == value
				}
				if !found {
					return nil, fmt.Errorf("catalog: raw series missing from declared filter options")
				}
			}
		}
	}
	return rows, nil
}

func validColumn(c Column) bool {
	if c.Precision != "exact" && c.Precision != "estimated" && c.Precision != "rounded" && c.Precision != "not_applicable" {
		return false
	}
	switch c.Semantic {
	case "minimum_tokens":
		return c.Unit == "token" && c.Precision == "estimated"
	case "estimated_tokens":
		return c.Unit == "token" && c.Precision == "estimated"
	case "supplier_token_price":
		return c.Unit == "CNY/1M_tokens" && c.Precision == "exact"
	case "reference_video_amount", "minimum_amount":
		return c.Unit == "CNY/video" && c.Precision != "not_applicable"
	case "reference_second_price":
		return c.Unit == "CNY/second" && c.Precision != "not_applicable"
	case "input_duration", "output_duration":
		return c.Unit == "second" && c.Precision == "exact"
	case "fps":
		return c.Unit == "fps" && c.Precision == "exact"
	case "dimension_pixels":
		return c.Unit == "pixel" && c.Precision == "exact"
	case "dimensions":
		return c.Unit == "pixel" && c.Precision == "not_applicable"
	case "series", "model_id", "resolution", "ratio", "context":
		return c.Unit == "" && c.Precision == "not_applicable"
	}
	return false
}

func validValue(c Column, value string) bool {
	for _, missing := range c.MissingValues {
		if value == missing {
			return true
		}
	}
	switch c.Semantic {
	case "minimum_tokens", "estimated_tokens", "fps", "dimension_pixels":
		return integer.MatchString(value) && len(value) <= 18
	case "supplier_token_price", "reference_video_amount", "minimum_amount", "reference_second_price", "input_duration", "output_duration":
		return number.MatchString(value) && len(value) <= 40
	default:
		return len(value) <= 4096
	}
}
