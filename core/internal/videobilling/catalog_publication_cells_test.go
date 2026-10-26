package videobilling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func publicationAggregate(count int64, body string) catalogMinimumAggregate {
	return catalogMinimumAggregate{CellCount: sql.NullInt64{Int64: count, Valid: true}, CellsJSON: []byte(body)}
}

func TestCatalogPublicationAggregateCodecPreservesExactFacts(t *testing.T) {
	emojis := strings.Repeat("😀", 16)
	escaped := strings.Repeat(`\ud83d\ude00`, 16)
	a := publicationAggregate(2, `[[18446744073709551615,9223372036854775808,"`+escaped+`","16:9",-9223372036854775808,9223372036854775807,9007199254740993],`+
		`[9007199254740993,1,"invalid","bad",0,-1,-9223372036854775808]]`)
	cells, err := decodeCatalogMinimumAggregate(context.Background(), &a)
	want := []MinimumRuleCell{
		{ID: math.MaxUint64, RuleSetID: uint64(math.MaxInt64) + 1, Resolution: emojis, Ratio: "16:9", OutputDurationMS: math.MinInt64, FPS: math.MaxInt64, MinimumTokens: 9007199254740993},
		{ID: 9007199254740993, RuleSetID: 1, Resolution: "invalid", Ratio: "bad", OutputDurationMS: 0, FPS: -1, MinimumTokens: math.MinInt64},
	}
	if err != nil || !reflect.DeepEqual(cells, want) || a.DecodedCount != 2 {
		t.Fatal("aggregate changed exact integers, escaped strings or invalid business facts", err)
	}
	// Quotes, slashes, controls and literal supplementary characters are copied
	// as scalar values, not reinterpreted as JSON or normalized business fields.
	for _, value := range []string{"\"\\/\b\f\n\r\t", strings.Repeat("\x00", 16), emojis, "<>&\u2028\u2029"} {
		encoded, _ := json.Marshal(value)
		a = publicationAggregate(1, `[[1,1,`+string(encoded)+`,"1:1",1,1,1]]`)
		cells, err = decodeCatalogMinimumAggregate(context.Background(), &a)
		if err != nil || len(cells) != 1 || cells[0].Resolution != value || a.DecodedCount != 1 {
			t.Fatal("aggregate string round trip changed a fact", err)
		}
	}
	for _, body := range [][]byte{nil, []byte("[]"), []byte(" \n[ ]\t")} {
		a = catalogMinimumAggregate{CellCount: sql.NullInt64{Int64: 0, Valid: true}, CellsJSON: body}
		cells, err = decodeCatalogMinimumAggregate(context.Background(), &a)
		if err != nil || len(cells) != 0 || a.DecodedCount != 0 {
			t.Fatal("explicit empty aggregate rejected", err)
		}
	}
}

func TestCatalogPublicationAggregateCodecRejectsCorruption(t *testing.T) {
	valid := `[[1,2,"720p","16:9",4000,24,100]]`
	cases := []struct {
		name string
		data catalogMinimumAggregate
		want error
	}{
		{"null_count", catalogMinimumAggregate{CellsJSON: []byte(valid)}, ErrCatalogTariffUnavailable},
		{"negative_count", publicationAggregate(-1, valid), ErrCatalogTariffUnavailable},
		{"impossible_count", publicationAggregate(32002, valid), ErrCatalogTariffUnavailable},
		{"probe_count", publicationAggregate(32001, valid), ErrCatalogTariffLimit},
		{"positive_sql_null", catalogMinimumAggregate{CellCount: sql.NullInt64{Int64: 1, Valid: true}}, ErrCatalogTariffUnavailable},
		{"positive_json_null", publicationAggregate(1, "null"), ErrCatalogTariffUnavailable},
		{"zero_json_null", publicationAggregate(0, "null"), ErrCatalogTariffUnavailable},
		{"empty_blob", publicationAggregate(0, ""), ErrCatalogTariffUnavailable},
		{"outer_object", publicationAggregate(1, `{}`), ErrCatalogTariffUnavailable},
		{"tuple_null", publicationAggregate(1, `[null]`), ErrCatalogTariffUnavailable},
		{"tuple_object", publicationAggregate(1, `[{}]`), ErrCatalogTariffUnavailable},
		{"short_tuple", publicationAggregate(1, `[[1,2,"720p","16:9",4000,24]]`), ErrCatalogTariffUnavailable},
		{"long_tuple", publicationAggregate(1, `[[1,2,"720p","16:9",4000,24,100,101]]`), ErrCatalogTariffUnavailable},
		{"missing_tuple", publicationAggregate(2, valid), ErrCatalogTariffUnavailable},
		{"extra_tuple", publicationAggregate(0, valid), ErrCatalogTariffUnavailable},
		{"truncated", publicationAggregate(1, strings.TrimSuffix(valid, "]")), ErrCatalogTariffUnavailable},
		{"trailing_value", publicationAggregate(1, valid+` null`), ErrCatalogTariffUnavailable},
		{"trailing_garbage", publicationAggregate(1, valid+` x`), ErrCatalogTariffUnavailable},
		{"unpaired_high", publicationAggregate(1, `[[1,2,"\ud800","16:9",4000,24,100]]`), ErrCatalogTariffUnavailable},
		{"unpaired_low", publicationAggregate(1, `[[1,2,"\udfff","16:9",4000,24,100]]`), ErrCatalogTariffUnavailable},
		{"wrong_surrogate_pair", publicationAggregate(1, `[[1,2,"\ud800\u0041","16:9",4000,24,100]]`), ErrCatalogTariffUnavailable},
		{"invalid_utf8", publicationAggregate(1, "[[1,2,\"\xff\",\"16:9\",4000,24,100]]"), ErrCatalogTariffUnavailable},
		{"wide_text", publicationAggregate(1, `[[1,2,"`+strings.Repeat("a", 17)+`","16:9",4000,24,100]]`), ErrCatalogTariffUnavailable},
		{"huge_blob", publicationAggregate(0, strings.Repeat(" ", maxCatalogMinimumJSONBytes+1)), ErrCatalogTariffLimit},
	}
	for position := 0; position < 7; position++ {
		fields := []string{"1", "2", `"720p"`, `"16:9"`, "4000", "24", "100"}
		fields[position] = "null"
		cases = append(cases, struct {
			name string
			data catalogMinimumAggregate
			want error
		}{fmt.Sprintf("null_field_%d", position), publicationAggregate(1, "[["+strings.Join(fields, ",")+"]]"), ErrCatalogTariffUnavailable})
	}
	for _, position := range []int{0, 1, 4, 5, 6} {
		for _, bad := range []string{`"1"`, "1.0", "1e0", "+1", "01", "true", "18446744073709551616", "-9223372036854775809"} {
			fields := []string{"1", "2", `"720p"`, `"16:9"`, "4000", "24", "100"}
			fields[position] = bad
			cases = append(cases, struct {
				name string
				data catalogMinimumAggregate
				want error
			}{fmt.Sprintf("number_%d_%s", position, bad), publicationAggregate(1, "[["+strings.Join(fields, ",")+"]]"), ErrCatalogTariffUnavailable})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cells, err := decodeCatalogMinimumAggregate(context.Background(), &tc.data)
			if !errors.Is(err, tc.want) || cells != nil {
				t.Fatalf("corrupt aggregate accepted or leaked partial data: %v", err)
			}
		})
	}
	partial := publicationAggregate(2, valid[:len(valid)-1]+`,[1]]`)
	if cells, err := decodeCatalogMinimumAggregate(context.Background(), &partial); err == nil || cells != nil || partial.DecodedCount != 1 {
		t.Fatal("failed decode mislabeled completed or returned partial cells")
	}
}

type publicationCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
	at     int
	after  bool
}

func (c *publicationCancelContext) Err() error {
	c.checks++
	observed := c.Context.Err()
	if c.checks == c.at {
		c.cancel()
		if c.after {
			// Model cancellation immediately after the caller observed Err=nil,
			// before it starts decoding. No timing sleeps or parser hooks.
			return observed
		}
	}
	return c.Context.Err()
}

func TestCatalogPublicationAggregateMalformedAndCancelledPreservesParent(t *testing.T) {
	for _, raw := range []string{
		`[[1,1,"720p","16:9",4000,24,"bad-integer"]]`,
		`[[1,1,"720p","16:9",4000,24,100,101]]`,
		`[[1,1,"\ud800","16:9",4000,24,100]]`,
	} {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &publicationCancelContext{Context: parent, cancel: cancel, at: 2, after: true}
		a := publicationAggregate(1, raw)
		cells, err := decodeCatalogMinimumAggregate(ctx, &a)
		cancel()
		if !errors.Is(parent.Err(), context.Canceled) || !errors.Is(err, context.Canceled) || cells != nil || a.DecodedCount != 0 {
			t.Fatal("simultaneous malformed input masked parent cancellation or leaked facts")
		}
	}
}

func TestCatalogPublicationAggregateCodecParentBudgetAndLimit(t *testing.T) {
	body := `[[1,1,"720p","16:9",4000,24,100],[2,1,"720p","16:9",5000,24,101]]`
	for _, at := range []int{1, 3, 4} {
		t.Run(fmt.Sprintf("cancel_check_%d", at), func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &publicationCancelContext{Context: parent, cancel: cancel, at: at}
			a := publicationAggregate(2, body)
			cells, err := decodeCatalogMinimumAggregate(ctx, &a)
			if !errors.Is(err, context.Canceled) || cells != nil || ctx.checks < at {
				t.Fatal("decoder ignored original parent cancellation or leaked partial facts")
			}
		})
	}
	// Independent arithmetic from tracked physical types, not an arbitrary MiB
	// value. Each tuple is at most 502 bytes plus its outer comma and one space.
	if maxCatalogMinimumJSONBytes != 16_384_514 || 5*20+2*(16*12+2)+2+6*2+2 > 512 {
		t.Fatal("aggregate acceptance limit no longer follows physical column bounds")
	}
	// This is a pure codec sample, not evidence of a real SQL deadline.
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 32000; i++ {
		if i != 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `[%d,1,"720p","16:9",4000,24,100]`, i+1)
	}
	b.WriteByte(']')
	a := publicationAggregate(32000, b.String())
	cells, err := decodeCatalogMinimumAggregate(context.Background(), &a)
	if err != nil || len(cells) != 32000 || a.DecodedCount != 32000 || cells[31999].ID != 32000 {
		t.Fatal("complete codec boundary changed", err)
	}
}

func TestCatalogPublicationAggregateSQLitePhysicalExtremes(t *testing.T) {
	for _, mode := range []string{"signed_extremes", "wide_text", "real_number", "empty"} {
		t.Run(mode, func(t *testing.T) {
			_, tx, _ := publicationRowsFixture(t)
			setID := uint64(math.MaxInt64)
			if err := tx.Exec("UPDATE video_minimum_rule_sets SET id=? WHERE id=1", setID).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Exec("DELETE FROM video_minimum_rule_cells").Error; err != nil {
				t.Fatal(err)
			}
			want := []MinimumRuleCell{
				{ID: 9007199254740993, RuleSetID: setID, Resolution: strings.Repeat("😀", 16), Ratio: "\"\\/\n", OutputDurationMS: math.MinInt64, FPS: math.MaxInt64, MinimumTokens: 9007199254740993},
				{ID: math.MaxInt64, RuleSetID: setID, Resolution: "invalid", Ratio: "bad", OutputDurationMS: math.MaxInt64, FPS: math.MinInt64, MinimumTokens: math.MinInt64},
			}
			if mode != "empty" {
				if err := tx.Create(&want).Error; err != nil {
					t.Fatal("SQLite cannot store its actual signed64 physical extremes", err)
				}
			}
			switch mode {
			case "wide_text":
				if err := tx.Exec("UPDATE video_minimum_rule_cells SET resolution=? WHERE id=?", strings.Repeat("a", 17), want[0].ID).Error; err != nil {
					t.Fatal(err)
				}
			case "real_number":
				if err := tx.Exec("UPDATE video_minimum_rule_cells SET fps=1.5 WHERE id=?", want[0].ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := ReadCatalogTariffsTx(context.Background(), tx, []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
			switch mode {
			case "signed_extremes":
				if err != nil || !reflect.DeepEqual(snapshot.cells, want) {
					t.Fatal("SQLite actual aggregate changed representable facts", err)
				}
			case "empty":
				if err != nil || len(snapshot.cells) != 0 {
					t.Fatal("SQLite actual empty aggregate failed", err)
				}
			default:
				if !errors.Is(err, ErrCatalogTariffUnavailable) || !reflect.DeepEqual(snapshot, CatalogTariffSnapshot{}) {
					t.Fatal("SQLite loose physical types were coerced or partially accepted", err)
				}
			}
		})
	}
}
