package videobilling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogPublicationCursorStandardJSONDifferential(t *testing.T) {
	// The standard encoder supplies syntax and escaped strings, while explicit
	// Go integer values supply the independent precision oracle (never float).
	want := []MinimumRuleCell{
		{ID: math.MaxUint64, RuleSetID: uint64(math.MaxInt64) + 1, Resolution: "\"\\/\b\f\n\r\t", Ratio: "😀", OutputDurationMS: math.MinInt64, FPS: math.MaxInt64, MinimumTokens: 9007199254740993},
		{ID: 0, RuleSetID: 9007199254740993, Resolution: strings.Repeat("\x00", 16), Ratio: "<>&\u2028\u2029", OutputDurationMS: 0, FPS: -1, MinimumTokens: math.MinInt64},
		{ID: 1, RuleSetID: 2, Resolution: strings.Repeat("😀", 16), Ratio: "", OutputDurationMS: 1, FPS: 2, MinimumTokens: math.MaxInt64},
	}
	encoded := make([]string, len(want))
	for i, cell := range want {
		raw, err := json.Marshal([]any{cell.ID, cell.RuleSetID, cell.Resolution, cell.Ratio, cell.OutputDurationMS, cell.FPS, cell.MinimumTokens})
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = string(raw)
	}
	for _, whitespace := range []string{"", " ", "\r\n\t "} {
		t.Run(fmt.Sprintf("whitespace_%d", len(whitespace)), func(t *testing.T) {
			tuples := make([]string, len(encoded))
			for i, text := range encoded {
				var fields []json.RawMessage
				if err := json.Unmarshal([]byte(text), &fields); err != nil || len(fields) != 7 {
					t.Fatal("independent encoder oracle failed", err)
				}
				values := make([]string, len(fields))
				for j := range fields {
					values[j] = string(fields[j])
				}
				tuples[i] = "[" + whitespace + strings.Join(values, whitespace+","+whitespace) + whitespace + "]"
			}
			body := whitespace + "[" + whitespace + strings.Join(tuples, whitespace+","+whitespace) + whitespace + "]" + whitespace
			if !json.Valid([]byte(body)) {
				t.Fatal("differential corpus is not standard JSON")
			}
			a := publicationAggregate(int64(len(want)), body)
			got, err := decodeCatalogMinimumAggregate(context.Background(), &a)
			if err != nil || !reflect.DeepEqual(got, want) || a.DecodedCount != len(want) {
				t.Fatal("cursor differed from exact oracle", err)
			}
			// Returned cells must own strings after borrowed input spans are gone.
			for i := range a.CellsJSON {
				a.CellsJSON[i] = 'x'
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("returned strings alias mutable aggregate bytes")
			}
		})
	}
	// Explicit legal spellings not necessarily chosen by the standard encoder.
	for name, pair := range map[string][2]string{
		"slash": {`"\/"`, "/"}, "uppercase_surrogate": {`"\uD83D\uDE00"`, "😀"},
		"escaped_quote": {`"\""`, "\""}, "escaped_backslash": {`"\\"`, "\\"},
		"nul": {`"\u0000"`, "\x00"}, "del": {"\"\x7f\"", "\x7f"},
	} {
		t.Run(name, func(t *testing.T) {
			var standard string
			if err := json.Unmarshal([]byte(pair[0]), &standard); err != nil || standard != pair[1] {
				t.Fatal("standard string oracle failed", err)
			}
			a := publicationAggregate(1, `[[1,2,`+pair[0]+`,"1:1",-0,-0,-0]]`)
			got, err := decodeCatalogMinimumAggregate(context.Background(), &a)
			if err != nil || len(got) != 1 || got[0].Resolution != standard || got[0].OutputDurationMS != 0 || got[0].FPS != 0 || got[0].MinimumTokens != 0 {
				t.Fatal("legal escape or signed negative zero rejected", err)
			}
		})
	}
}

func TestCatalogPublicationCursorRejectsTokenAndSeparatorCorruption(t *testing.T) {
	base := []string{"1", "2", `"720p"`, `"16:9"`, "4000", "24", "100"}
	cases := map[string]string{
		"outer_trailing_comma": `[[1,2,"720p","16:9",4000,24,100],]`,
		"tuple_trailing_comma": `[[1,2,"720p","16:9",4000,24,100,]]`,
		"double_outer_comma":   `[[1,2,"720p","16:9",4000,24,100],,[1,2,"720p","16:9",4000,24,100]]`,
		"missing_outer_comma":  `[[1,2,"720p","16:9",4000,24,100][1,2,"720p","16:9",4000,24,100]]`,
		"non_json_space":       "\u00a0[[1,2,\"720p\",\"16:9\",4000,24,100]]",
		"formfeed":             "[\f[1,2,\"720p\",\"16:9\",4000,24,100]]",
		"vertical_tab":         "[[1,2,\"720p\",\"16:9\",4000,24,100]]\v",
		"empty":                "", "outer_truncated": "[", "tuple_truncated": "[[",
	}
	for _, pos := range []int{0, 1, 4, 5, 6} {
		for _, value := range []string{"+0", "-", "--1", "-01", "00", "1e1", "1E+1", "1.0", ".1", "1.", "1 2", "0x1", "NaN", "false", "[]", "{}", strings.Repeat("1", 21)} {
			fields := append([]string(nil), base...)
			fields[pos] = value
			cases[fmt.Sprintf("integer_%d_%s", pos, value)] = "[[" + strings.Join(fields, ",") + "]]"
		}
	}
	for _, pos := range []int{0, 1} {
		for _, value := range []string{"-0", "-1", "-9223372036854775808"} {
			fields := append([]string(nil), base...)
			fields[pos] = value
			cases[fmt.Sprintf("unsigned_%d_%s", pos, value)] = "[[" + strings.Join(fields, ",") + "]]"
		}
	}
	for i, value := range []string{`"\x41"`, `"\u123"`, `"\u12GG"`, `"\ud800x"`, `"\ud800\ud800"`, `"abc\"`, `"abc""def"`, "\"raw\nnewline\"", "\"" + strings.Repeat("a", 193) + "\"", "123", "null", "[]"} {
		fields := append([]string(nil), base...)
		fields[2] = value
		cases[fmt.Sprintf("string_%d", i)] = "[[" + strings.Join(fields, ",") + "]]"
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			a := publicationAggregate(1, body)
			got, err := decodeCatalogMinimumAggregate(context.Background(), &a)
			if !errors.Is(err, ErrCatalogTariffUnavailable) || got != nil {
				t.Fatal("invalid fixed grammar accepted or partial facts returned", err)
			}
		})
	}
}

func TestCatalogPublicationCursorWhitespaceCancellation(t *testing.T) {
	for _, offset := range []int{0, 1, 4094, 4095, 4096} {
		t.Run(fmt.Sprintf("offset_%d", offset), func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &publicationCancelContext{Context: parent, cancel: cancel, at: 1}
			c := catalogMinimumCursor{raw: []byte(strings.Repeat("x", offset) + strings.Repeat(" ", 8192)), pos: offset, ctx: ctx}
			if c.space() || c.pos-offset > 4096 || !errors.Is(parent.Err(), context.Canceled) {
				t.Fatal("ASCII whitespace scanning bypassed bounded parent checks")
			}
		})
	}
	valid := `[1,2,"720p","16:9",4000,24,100]`
	for name, body := range map[string]string{
		"before_outer":   strings.Repeat(" ", 8192) + "[" + valid + "]",
		"before_tuple":   "[" + strings.Repeat("\t", 8192) + valid + "]",
		"after_outer":    "[" + valid + "]" + strings.Repeat("\n", 8192),
		"between_fields": `[[1,` + strings.Repeat("\r", 8192) + `2,"720p","16:9",4000,24,100]]`,
	} {
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &publicationCancelContext{Context: parent, cancel: cancel, at: 3}
			a := publicationAggregate(1, body)
			got, err := decodeCatalogMinimumAggregate(ctx, &a)
			if !errors.Is(err, context.Canceled) || got != nil {
				t.Fatal("long whitespace masked parent cancellation or leaked partial facts", err)
			}
		})
	}
}
