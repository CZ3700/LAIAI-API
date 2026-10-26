package videobilling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCatalogPublicationAggregateTupleCardinality(t *testing.T) {
	fields := []string{"1", "2", `"720p"`, `"16:9"`, "4000", "24", "100"}
	valid := "[" + strings.Join(fields, ",") + "]"
	cases := map[string]string{
		"eighth_null":        valid[:len(valid)-1] + ",null]",
		"eighth_array":       valid[:len(valid)-1] + ",[]]",
		"eighth_object":      valid[:len(valid)-1] + ",{}]",
		"ninth_after_null":   valid[:len(valid)-1] + ",null,9]",
		"many_after_null":    valid[:len(valid)-1] + strings.Repeat(",null", 24) + "]",
		"tuple_null":         "null",
		"nested_first_field": `[[1],2,"720p","16:9",4000,24,100]`,
		"nested_tuple":       "[" + valid + "]",
	}
	for n := 0; n < 7; n++ {
		cases[fmt.Sprintf("missing_%d_fields", 7-n)] = "[" + strings.Join(fields[:n], ",") + "]"
	}
	for name, tuple := range cases {
		t.Run(name, func(t *testing.T) {
			for _, prefix := range []bool{false, true} {
				count, body, completed := int64(1), "["+tuple+"]", 0
				if prefix {
					count, body, completed = 2, "["+valid+","+tuple+"]", 1
				}
				a := publicationAggregate(count, body)
				cells, err := decodeCatalogMinimumAggregate(context.Background(), &a)
				if !errors.Is(err, ErrCatalogTariffUnavailable) || cells != nil || a.DecodedCount != completed {
					t.Fatal("tuple shape accepted, partial facts leaked or decoded count invented")
				}
			}
		})
	}
	// Sentinel checks must not reject a legitimate maximum-width integer tuple.
	a := publicationAggregate(1, `[[18446744073709551615,9223372036854775808,"720p","16:9",-9223372036854775808,9223372036854775807,9007199254740993]]`)
	cells, err := decodeCatalogMinimumAggregate(context.Background(), &a)
	if err != nil || len(cells) != 1 || a.DecodedCount != 1 || cells[0].MinimumTokens != 9007199254740993 {
		t.Fatal("valid seven-field tuple rejected or precision lost", err)
	}
}

func TestCatalogPublicationAggregateTupleCancellationWins(t *testing.T) {
	for name, tuple := range map[string]string{
		"extra_null":      `[1,2,"720p","16:9",4000,24,100,null]`,
		"extra_many":      `[1,2,"720p","16:9",4000,24,100,null,{},[]]`,
		"missing":         `[1,2,"720p","16:9",4000,24]`,
		"bad_surrogate":   `[1,2,"\ud800","16:9",4000,24,100]`,
		"malformed_extra": `[1,2,"720p","16:9",4000,24,100,]`,
	} {
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Cancellation immediately follows the tuple's pre-decode check.
			// The same input is invalid; that error must not mask parent cause.
			ctx := &publicationCancelContext{Context: parent, cancel: cancel, at: 2, after: true}
			a := publicationAggregate(1, "["+tuple+"]")
			cells, err := decodeCatalogMinimumAggregate(ctx, &a)
			if !errors.Is(err, context.Canceled) || cells != nil || a.DecodedCount != 0 {
				t.Fatal("tuple failure masked cancellation or returned partial facts")
			}
		})
	}
}
