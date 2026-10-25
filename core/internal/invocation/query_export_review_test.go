package invocation

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

// The public query entry points own every predicate and projection. A host's
// previously composed handle must not silently narrow results or redirect SQL.
func TestQueryExportAndListDiscardCallerQueryClauses(t *testing.T) {
	for _, test := range []struct {
		name  string
		dirty func(*gorm.DB) *gorm.DB
	}{
		{"foreign_tenant_predicate", func(db *gorm.DB) *gorm.DB { return db.Where("org_id = ?", 2) }},
		{"caller_pagination", func(db *gorm.DB) *gorm.DB { return db.Limit(1).Offset(1).Order("id ASC") }},
		{"caller_private_projection", func(db *gorm.DB) *gorm.DB { return db.Table("nonexistent_private_table").Select("private_column") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, filter := queryFixture(t)
			for _, export := range []bool{false, true} {
				var result QueryResult
				var err error
				if export {
					result, err = QueryLogExport(context.Background(), test.dirty(db), OrganizationQueryScope(1), filter)
				} else {
					result, err = QueryLogs(context.Background(), test.dirty(db), OrganizationQueryScope(1), filter, 1, 20)
				}
				wantRows := 20
				if export {
					wantRows = 25
				}
				if err != nil || result.Totals.RecordCount != 25 || result.Totals.CostUSD != "325" || len(result.Items) != wantRows {
					t.Errorf("export=%v caller clauses changed authoritative filter: rows=%d totals=%+v err=%v", export, len(result.Items), result.Totals, err)
					continue
				}
				for i, row := range result.Items {
					if row.OrgID != 1 || row.ID != uint64(25-i) {
						t.Fatalf("export=%v caller altered tenant/order at row %d", export, i)
					}
				}
			}
		})
	}
}
