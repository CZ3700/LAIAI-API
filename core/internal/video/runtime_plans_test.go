package video

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// Use the actual candidate scopes, including the billing-domain join. This
// catches an index that exists in DDL but is unusable by the shipped predicate.
func TestMySQLRuntimeCandidatePlans(t *testing.T) {
	f := mysqlFixture(t)
	f.create(t, "runtime-plan-fixture")
	ctx := context.Background()
	for _, tc := range []struct {
		name, index string
		query       *gorm.DB
	}{
		{"query", "idx_video_runtime_poll", f.s.pendingQueryCandidates(ctx).Order("v.next_poll_at, v.task_id").Limit(40)},
		{"dispatch", "idx_video_runtime_operation", f.s.pendingOperationCandidates(ctx)},
		{"operation_recovery", "idx_video_runtime_operation", f.s.recoveryOperationCandidates(ctx).Order("o.updated_at, o.id").Limit(30)},
		{"receipt_recovery", "idx_video_runtime_receipt", f.s.recoveryQueryCandidates(ctx).Order("r.received_at, r.attempt_id").Limit(40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ignored []map[string]any
			statement := tc.query.Session(&gorm.Session{DryRun: true}).Find(&ignored).Statement
			var plan []map[string]any
			must(t, f.s.DB.Raw("EXPLAIN "+statement.SQL.String(), statement.Vars...).Scan(&plan).Error)
			usable := false
			for _, row := range plan {
				for _, column := range []string{"possible_keys", "key"} {
					value := runtimePlanValue(row[column])
					usable = usable || strings.Contains(value, tc.index)
				}
				t.Logf("table=%s type=%s possible_keys=%s key=%s rows=%s extra=%s", runtimePlanValue(row["table"]), runtimePlanValue(row["type"]), runtimePlanValue(row["possible_keys"]), runtimePlanValue(row["key"]), runtimePlanValue(row["rows"]), runtimePlanValue(row["Extra"]))
			}
			if !usable {
				t.Fatalf("runtime candidate cannot use %s", tc.index)
			}
		})
	}
}

func runtimePlanValue(v any) string {
	if raw, ok := v.([]byte); ok {
		return string(raw)
	}
	return fmt.Sprint(v)
}
