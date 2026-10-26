package video

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestMySQLQueryRecoveryCandidatesIndexedAndBounded(t *testing.T) {
	h := queryRecoveryNew(t)
	first, second := h.pending(t, 0, 0), h.pending(t, 1, 1)
	h.arrange(t, &first, false, 1)
	h.arrange(t, &second, false, 1)
	now, err := authorityFixtureNow(h.db)
	queryRecoveryCheck(t, err)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	const perClass = 4096
	classes := []string{"applied", "review", "future", "unknown_runtime", "unknown_schema", "old_settlement_conflict"}
	// Planner-only background: explicitly synthetic receipt rows sharing a
	// genuine accepted task/snapshot. They are NEVER applied as provider facts.
	// Two real public Create/POST/GET receipts are the only eligible work.
	queryRecoveryCheck(t, h.db.Transaction(func(tx *gorm.DB) error {
		for category, name := range classes {
			rows := make([]QueryReceipt, perClass)
			for index := range rows {
				row := first.row
				row.AttemptID = fmt.Sprintf("query_plan_%d_%04d", category, index)
				row.Sequence = uint64(100 + category*perClass + index)
				row.ReceivedAt, row.RecoveryDueAt = past, &past
				row.RecoveryVersion, row.ApplyErrorCode = 0, ""
				switch name {
				case "applied":
					row.AppliedAt = &past
				case "review":
					row.ApplyErrorCode = "query_observation_invalid"
				case "future":
					row.RecoveryDueAt = &future
				case "unknown_runtime":
					row.RuntimeVersion = "future-query-plan"
				case "unknown_schema":
					row.ObservationSchemaVersion = 2
				case "old_settlement_conflict":
					row.ApplyErrorCode = "settlement_conflict"
				}
				rows[index] = row
			}
			if err := tx.CreateInBatches(rows, 128).Error; err != nil {
				return err
			}
			if name == "review" {
				// NULL is explicit: an omitted nil field would invoke the real
				// UTC default and fail to exercise the isolation barrier.
				q := tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id LIKE ?", first.row.OrgID, first.row.TaskID, fmt.Sprintf("query_plan_%d_%%", category)).UpdateColumn("recovery_due_at", nil)
				if q.Error != nil {
					return q.Error
				}
				if q.RowsAffected != perClass {
					return fmt.Errorf("planner review fixture cardinality differs")
				}
			}
		}
		return nil
	}))
	var count int64
	queryRecoveryCheck(t, h.db.Model(&QueryReceipt{}).Count(&count).Error)
	if count != int64(len(classes)*perClass+2) {
		t.Fatal("planner background did not reach its declared size")
	}
	// Refresh statistics only in this test's private schema, after its complete
	// declared background exists. Do not force an index or change the production
	// candidate query merely to obtain a particular plan.
	statsCtx, statsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	statsErr := h.db.WithContext(statsCtx).Exec("ANALYZE TABLE video_query_receipts, video_task_states, video_billing_snapshots").Error
	statsCancel()
	queryRecoveryCheck(t, statsErr)
	s := h.service(t)
	h.forbidNetwork(t, s)
	funds, evidence := h.finances(t), h.evidenceStorage(t)
	var dryRows []QueryReceipt
	dry := s.recoveryQueryCandidates(context.Background()).Order("r.recovery_due_at, r.received_at, r.attempt_id").Limit(40).Session(&gorm.Session{DryRun: true}).Find(&dryRows)
	queryRecoveryCheck(t, dry.Error)
	sqlText := dry.Statement.SQL.String()
	if sqlText == "" || strings.Contains(strings.ToUpper(sqlText), "FORCE INDEX") {
		t.Fatal("planner did not use the real unforced candidate query")
	}
	varsJSON, err := json.Marshal(dry.Statement.Vars)
	queryRecoveryCheck(t, err)
	planCtx, planCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer planCancel()
	var plans []struct {
		Table    string  `gorm:"column:table"`
		Access   string  `gorm:"column:type"`
		Key      *string `gorm:"column:key"`
		Rows     uint64  `gorm:"column:rows"`
		Filtered float64 `gorm:"column:filtered"`
		Extra    string  `gorm:"column:Extra"`
	}
	queryRecoveryCheck(t, h.db.WithContext(planCtx).Raw("EXPLAIN "+sqlText, dry.Statement.Vars...).Scan(&plans).Error)
	indexed := false
	for _, plan := range plans {
		if plan.Table == "r" && plan.Key != nil && *plan.Key != "" && plan.Access != "ALL" {
			indexed = true
		}
	}
	if !indexed {
		t.Fatal("real receipt candidate plan did not use an index at this declared fixture size")
	}
	planJSON, err := json.Marshal(plans)
	queryRecoveryCheck(t, err)
	// Safe structural evidence only; raw SQL Vars may contain tenant IDs and
	// are intentionally represented by a digest rather than emitted values.
	t.Logf("candidate plan sql_sha256=%x vars_sha256=%x rows=%d plan=%s", sha256.Sum256([]byte(sqlText)), sha256.Sum256(varsJSON), count, planJSON)
	ctx, cancel := context.WithTimeout(context.Background(), recoveryLaneTimeout)
	defer cancel()
	started := time.Now()
	var candidates []QueryReceipt
	queryRecoveryCheck(t, s.recoveryQueryCandidates(ctx).Order("r.recovery_due_at, r.received_at, r.attempt_id").Limit(40).Find(&candidates).Error)
	elapsed := time.Since(started)
	if ctx.Err() != nil || len(candidates) != 2 {
		t.Fatal("bounded real query did not exclude all six background classes before LIMIT")
	}
	ids := map[string]bool{first.row.AttemptID: true, second.row.AttemptID: true}
	for _, row := range candidates {
		if !ids[row.AttemptID] {
			t.Fatal("a disqualified planner row occupied the recovery window")
		}
		delete(ids, row.AttemptID)
	}
	if len(ids) != 0 || h.finances(t) != funds || h.evidenceStorage(t) != evidence || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
		t.Fatal("read-only planner verification changed money, evidence or called the provider")
	}
	t.Logf("candidate query elapsed_us=%d selected=%d deadline_error=false", elapsed.Microseconds(), len(candidates))
}
