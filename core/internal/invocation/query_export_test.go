package invocation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestQueryLogExportAllRowsSameFiltersAndExactUsage(t *testing.T) {
	db, filter := queryFixture(t)
	for _, test := range []struct {
		name   string
		scope  QueryScope
		change func(*QueryFilter)
		count  int
	}{
		{"organization", OrganizationQueryScope(1), func(*QueryFilter) {}, 25},
		{"platform", AdminQueryScope(), func(*QueryFilter) {}, 26},
		{"model", OrganizationQueryScope(1), func(f *QueryFilter) { f.Model = "model-b" }, 12},
		{"failed", OrganizationQueryScope(1), func(f *QueryFilter) { f.Result = "failed" }, 12},
		{"exact", OrganizationQueryScope(1), func(f *QueryFilter) { f.RequestID = "query-03" }, 1},
		{"foreign", OrganizationQueryScope(1), func(f *QueryFilter) { f.APIKeyID = 21 }, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := filter
			test.change(&f)
			result, err := QueryLogExport(context.Background(), db, test.scope, f)
			if err != nil || len(result.Items) != test.count || result.Items == nil || result.AsOf.IsZero() {
				t.Fatalf("export rows=%d err=%v", len(result.Items), err)
			}
			var ids []uint64
			var tokenSum, costSum decimal.Decimal
			for i, row := range result.Items {
				if i > 0 && result.Items[i-1].ID <= row.ID {
					t.Fatal("export order is not descending ID")
				}
				ids = append(ids, row.ID)
				tokenSum = tokenSum.Add(decimal.NewFromInt(row.PromptTokens))
				costSum = costSum.Add(row.CostUSD)
				if row.ID == 3 && row.PromptTokens != 9007199254740993 {
					t.Fatal("row token precision lost")
				}
			}
			if tokenSum.String() != result.Totals.PromptTokens || costSum.String() != result.Totals.CostUSD {
				t.Fatal("export rows do not reconcile with exact aggregates")
			}
			var pageIDs []uint64
			for page := 1; ; page++ {
				rows, err := QueryLogs(context.Background(), db, test.scope, f, page, 20)
				if err != nil || rows.Totals != result.Totals {
					t.Fatal("export and paginated filter totals differ", err)
				}
				for _, row := range rows.Items {
					pageIDs = append(pageIDs, row.ID)
				}
				if len(rows.Items) < 20 {
					break
				}
			}
			if !reflect.DeepEqual(ids, pageIDs) {
				t.Fatal("export and paginated row identities differ")
			}
		})
	}
}

func TestQueryLogExportBoundaryNeverReturnsTruncatedResult(t *testing.T) {
	db, filter := queryFixture(t)
	rows := make([]Invocation, 0, QueryExportMaxRows)
	for i := 0; i < QueryExportMaxRows; i++ {
		rows = append(rows, Invocation{OrgID: 3, RequestID: fmt.Sprintf("export-boundary-%04d", i), Model: "boundary", Status: StatusSuccess, CreatedAt: filter.Since})
	}
	if err := db.CreateInBatches(&rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	result, err := QueryLogExport(context.Background(), db, OrganizationQueryScope(3), filter)
	if err != nil || len(result.Items) != 5000 || result.Totals.RecordCount != 5000 {
		t.Fatalf("5000-row boundary: count=%d err=%v", len(result.Items), err)
	}
	if err := db.Create(&Invocation{OrgID: 3, RequestID: "export-boundary-overflow", Model: "boundary", Status: StatusSuccess, CreatedAt: filter.Since}).Error; err != nil {
		t.Fatal(err)
	}
	rowQueries := 0
	if err := db.Callback().Query().Before("gorm:query").Register("export_no_rows_after_limit", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]Invocation); ok {
			rowQueries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	result, err = QueryLogExport(context.Background(), db, OrganizationQueryScope(3), filter)
	if !errors.Is(err, ErrQueryExportLimit) || !reflect.DeepEqual(result, QueryResult{}) || rowQueries != 0 {
		t.Fatalf("overflow must not fetch/return partial rows: err=%v queries=%d", err, rowQueries)
	}
}

func TestQueryLogExportFailureDoesNotReturnPartialSnapshot(t *testing.T) {
	db, filter := queryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := QueryLogExport(ctx, db, OrganizationQueryScope(1), filter); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, QueryResult{}) {
		t.Fatal("cancelled export returned data", err)
	}
	if result, err := QueryLogExport(context.Background(), db, QueryScope{}, filter); !errors.Is(err, ErrQueryInvalidScope) || !reflect.DeepEqual(result, QueryResult{}) {
		t.Fatal("zero authority accepted", err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		result, err := QueryLogExport(context.Background(), tx, OrganizationQueryScope(1), filter)
		if !errors.Is(err, ErrQueryUnavailable) || !reflect.DeepEqual(result, QueryResult{}) {
			t.Fatal("export accepted caller transaction", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().After("gorm:query").Register("export_inconsistent_rows", func(tx *gorm.DB) {
		if rows, ok := tx.Statement.Dest.(*[]Invocation); ok && len(*rows) > 0 {
			*rows = (*rows)[1:]
		}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := QueryLogExport(context.Background(), db, OrganizationQueryScope(1), filter)
	if !errors.Is(err, ErrQueryUnavailable) || !reflect.DeepEqual(result, QueryResult{}) {
		t.Fatal("mismatched row count accepted", err)
	}
}
