package video

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// These imported rows are ONLY a skewed candidate/EXPLAIN fixture. They are
// deliberately ineligible before LIMIT, not admissions or fabricated evidence
// authorizing money. The 42 consumed rows below all use real Stage/POST/GET/G.
func settlementQueueBackground(t *testing.T, f *fixture, base QueryReceipt) [32]byte {
	t.Helper()
	var originalState State
	var originalTask task.Task
	var originalSnapshot videobilling.Snapshot
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", base.OrgID, base.TaskID).Take(&originalState).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", base.OrgID, base.TaskID).Take(&originalTask).Error)
	must(t, f.s.DB.Where("id = ? AND org_id = ? AND task_id = ?", originalState.BillingSnapshotID, base.OrgID, base.TaskID).Take(&originalSnapshot).Error)
	for family, kind := range []string{"completed", "future", "null", "unknown", "invalid_kind", "missing_g"} {
		states := make([]State, 0, 512)
		rows := make([]QueryReceipt, 0, 512)
		tasks := make([]task.Task, 0, 512)
		snapshots := make([]videobilling.Snapshot, 0, 512)
		for i := 0; i < 512; i++ {
			id := fmt.Sprintf("settlement-plan-%s-%04d", kind, i)
			row, st, tr := base, originalState, originalTask
			row.TaskID, row.AttemptID = id, id+"-receipt"
			row.AppliedAt, row.ApplyErrorCode = nil, ""
			past, future := base.ReceivedAt.Add(-time.Hour), base.ReceivedAt.Add(24*time.Hour)
			row.RecoveryDueAt = &past
			st.TaskID, st.SettlementReceiptID = id, &row.AttemptID
			tr.ID, tr.TaskID, tr.UpstreamTaskID = 0, id, "up_"+id
			// Candidate-only snapshots satisfy the real identity/schema JOIN.
			// Their unique synthetic hold IDs are not real reservations and
			// cannot authorize settlement; all six families remain ineligible.
			snapshot := originalSnapshot
			snapshot.ID, snapshot.TaskID = 0, id
			snapshot.HoldID = uint64(1)<<63 + uint64(family*512+i)
			snapshots = append(snapshots, snapshot)
			switch kind {
			case "completed":
				row.AppliedAt = &row.ReceivedAt
			case "future":
				row.RecoveryDueAt = &future
			case "unknown":
				row.RuntimeVersion, st.RuntimeVersion = "unknown-plan-only", "unknown-plan-only"
			case "invalid_kind":
				row.SettlementKind = "invalid-plan-only"
			case "missing_g":
				row.GenerationAppliedAt = nil
			}
			states, rows, tasks = append(states, st), append(rows, row), append(tasks, tr)
		}
		must(t, f.s.DB.CreateInBatches(&tasks, 128).Error)
		must(t, f.s.DB.CreateInBatches(&snapshots, 128).Error)
		for i := range states {
			if snapshots[i].ID == 0 {
				t.Fatal("synthetic candidate snapshot ID missing")
			}
			states[i].BillingSnapshotID = snapshots[i].ID
		}
		must(t, f.s.DB.CreateInBatches(&states, 128).Error)
		must(t, f.s.DB.CreateInBatches(&rows, 128).Error)
		if kind == "null" {
			// Explicit NULL, not GORM's default-on-nil insert behavior.
			q := f.s.DB.Model(&QueryReceipt{}).Where("task_id LIKE ?", "settlement-plan-null-%").UpdateColumn("recovery_due_at", nil)
			must(t, q.Error)
			if q.RowsAffected != 512 {
				t.Fatal("NULL background did not cover the exact imported set")
			}
		}
		// Prove all background rows reach the intended contract/phase gates.
		// In particular none may disappear due to another task's snapshot.
		baseJoin := func() *gorm.DB {
			return f.s.DB.Table("video_query_receipts AS r").
				Joins("JOIN video_task_states v ON v.org_id = r.org_id AND v.task_id = r.task_id").
				Where("r.org_id = ? AND r.task_id LIKE ?", base.OrgID, "settlement-plan-"+kind+"-%")
		}
		var identityCount, supportedCount int64
		must(t, videobilling.WithTaskSnapshotContract(baseJoin()).Count(&identityCount).Error)
		must(t, supportedQueryReceiptRuntime(baseJoin()).Count(&supportedCount).Error)
		wantSupported := int64(512)
		if kind == "unknown" {
			wantSupported = 0 // Only this family deliberately fails runtime support.
		}
		if identityCount != 512 || supportedCount != wantSupported {
			t.Fatalf("candidate background contract kind=%s identity=%d supported=%d", kind, identityCount, supportedCount)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, f.s.DB.WithContext(ctx).Exec("ANALYZE TABLE video_query_receipts, video_task_states, video_receipt_applications").Error)
	return settlementQueueBackgroundHash(t, f)
}

func settlementQueueBackgroundHash(t *testing.T, f *fixture) [32]byte {
	t.Helper()
	return queryRecoveryHash(t, []any{
		queryRecoveryProjection(t, f.s.DB, "tasks", "task_id LIKE ?", "settlement-plan-%"),
		queryRecoveryProjection(t, f.s.DB, "video_task_states", "task_id LIKE ?", "settlement-plan-%"),
		queryRecoveryProjection(t, f.s.DB, "video_billing_snapshots", "task_id LIKE ?", "settlement-plan-%"),
		queryRecoveryProjection(t, f.s.DB, "video_query_receipts", "task_id LIKE ?", "settlement-plan-%"),
	})
}

func settlementQueueCauseOnly(err error) bool {
	if err == nil {
		return true
	}
	if err == context.DeadlineExceeded {
		return true
	}
	if typed, ok := err.(*querySettlementEvidenceError); ok {
		return typed != nil
	}
	group, ok := err.(*persistedRecoveryErrors)
	if !ok || group == nil || len(group.causes) == 0 {
		return false
	}
	for _, cause := range group.causes {
		if !settlementQueueCauseOnly(cause) {
			return false
		}
	}
	return true
}

func TestMySQLQuerySettlementBadDigestQueueProgress(t *testing.T) {
	f := newSettlementFixture(t)
	var rows []QueryReceipt
	n := int64(100)
	for index := 0; index < 42; index++ {
		v := submitSettlementV2(t, f, fmt.Sprintf("bad-digest-queue-%02d", index))
		row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
		if index < 40 {
			q := f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ?", row.OrgID, row.TaskID, row.AttemptID, row.RecoveryVersion).Updates(map[string]any{"settlement_evidence_sha256": strings.Repeat("0", 64), "recovery_due_at": gorm.Expr("UTC_TIMESTAMP(3) - INTERVAL 1 HOUR")})
			must(t, q.Error)
			if q.RowsAffected != 1 {
				t.Fatal("exact bad digest precondition missed receipt")
			}
			got := settlementReadReceipt(t, f, row)
			copy := got
			copy.SettlementEvidenceSHA256 = row.SettlementEvidenceSHA256
			copy.RecoveryDueAt = row.RecoveryDueAt
			if !reflect.DeepEqual(copy, row) {
				t.Fatal("bad digest fixture changed original evidence")
			}
			row = got
		}
		rows = append(rows, row)
	}
	background := settlementQueueBackground(t, f, rows[0])
	settlementForbidNetwork(t, f)
	var selected []QueryReceipt
	var actualSQL string
	var actualVars []any
	name := "test:settlement-candidate-sql"
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table == "r" && strings.Contains(q.Statement.SQL.String(), "ORDER BY r.recovery_due_at") {
			actualSQL = q.Statement.SQL.String()
			actualVars = append([]any(nil), q.Statement.Vars...)
		}
	}))
	must(t, f.s.recoverySettlementCandidates(context.Background()).Order("r.recovery_due_at, r.received_at, r.attempt_id").Limit(40).Find(&selected).Error)
	must(t, f.s.DB.Callback().Query().Remove(name))
	if len(selected) != 40 || actualSQL == "" || !strings.Contains(actualSQL, "LIMIT") {
		t.Fatal("real bounded candidate selection unavailable")
	}
	expected := make(map[string]bool, 40)
	for _, row := range rows[:40] {
		expected[row.AttemptID] = true
	}
	for _, r := range selected {
		if !expected[r.AttemptID] || r.SettlementEvidenceSHA256 != strings.Repeat("0", 64) {
			t.Fatal("digest was prefiltered instead of diagnosed under locks")
		}
		delete(expected, r.AttemptID)
	}
	if len(expected) != 0 {
		t.Fatal("background or duplicate row displaced a real due candidate")
	}
	var plan []struct {
		Table, Type string
		Key         *string
		Rows        int64
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	err := f.s.DB.WithContext(ctx).Raw("EXPLAIN "+actualSQL, actualVars...).Scan(&plan).Error
	stop()
	must(t, err)
	if len(plan) == 0 {
		t.Fatal("no actual candidate plan")
	}
	// This candidate has only closed runtime/code constants in Vars, no
	// credential, payload or customer input. Retain the real complete plan
	// before evaluating it so an unexpected optimizer choice is diagnosable.
	t.Logf("candidate SQL=%s Vars=%v", actualSQL, actualVars)
	for _, p := range plan {
		key := "none"
		if p.Key != nil {
			key = *p.Key
		}
		t.Logf("candidate table=%s access=%s key=%s rows=%d", p.Table, p.Type, key, p.Rows)
	}
	seenReceipt, seenState := false, false
	for _, p := range plan {
		if p.Table == "r" || p.Table == "v" {
			if p.Key == nil || (p.Type != "range" && p.Type != "ref" && p.Type != "eq_ref" && p.Type != "const") {
				t.Fatal("skewed candidate plan lost an indexed bounded access path")
			}
		}
		if p.Table == "r" {
			seenReceipt = true
			// G is a residual predicate on the approved index. Allow its 512
			// missing-G rows plus 42 real candidates, with estimate tolerance;
			// reject plans scanning the full 3,072-row historical population.
			if p.Rows < 1 || p.Rows > 2*(512+42) {
				t.Fatal("candidate estimated scan exceeds the declared residual window")
			}
		}
		if p.Table == "v" {
			seenState = true
			if p.Rows != 1 || (p.Type != "eq_ref" && p.Type != "const") {
				t.Fatal("candidate state lookup is not one unique row")
			}
		}
	}
	if !seenReceipt || !seenState {
		t.Fatal("actual candidate EXPLAIN omitted its receipt/state access")
	}
	before := make([]ledger.Hold, 40)
	for i := 0; i < 40; i++ {
		before[i] = f.hold(t, rows[i].TaskID)
	}
	complete := false
	for round := 0; round < 12; round++ {
		err := f.s.ProcessSettlementOnce(context.Background())
		if !settlementQueueCauseOnly(err) {
			t.Fatalf("unexpected queue cause type %T", err)
		}
		reviews, applied := 0, 0
		for i, row := range rows {
			got := settlementReadReceipt(t, f, row)
			if i < 40 && got.RecoveryDueAt == nil && got.ApplyErrorCode == querySettlementEvidenceInvalidCode && got.AppliedAt == nil {
				reviews++
			}
			if i >= 40 && got.AppliedAt != nil {
				applied++
			}
		}
		if reviews == 40 && applied == 2 {
			complete = true
			break
		}
	}
	if !complete {
		t.Fatal("bounded B rounds did not remove bad digest prefix and settle healthy successors")
	}
	for i, row := range rows {
		got := settlementReadReceipt(t, f, row)
		if got.RecoveryVersion != row.RecoveryVersion+1 {
			t.Fatal("review/settlement advanced more than one version")
		}
		if i < 40 {
			var entries int64
			must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", row.OrgID, row.TaskID).Count(&entries).Error)
			if !reflect.DeepEqual(f.hold(t, row.TaskID), before[i]) || entries != 0 {
				t.Fatal("damaged evidence changed its hold or initial")
			}
		}
	}
	var key identity.APIKey
	var account ledger.Account
	must(t, f.s.DB.Where("id = ? AND org_id = ?", f.key.ID, f.key.OrgID).Take(&key).Error)
	must(t, f.s.DB.Where("org_id = ? AND type = ? AND shard = 0", f.key.OrgID, ledger.AccountUserCredit).Take(&account).Error)
	if key.UsedAmount.String() != "34.5616" || account.HeldAmount.String() != "34.56" || account.PostedBalance.String() != "99.9984" {
		t.Fatal("bad prefix affected funds beyond exactly two 100-token settlements")
	}
	var remaining []QueryReceipt
	must(t, f.s.recoverySettlementCandidates(context.Background()).Find(&remaining).Error)
	if len(remaining) != 0 {
		t.Fatal("review/complete candidates still selected")
	}
	if background != settlementQueueBackgroundHash(t, f) {
		t.Fatal("B touched an ineligible historical/background row")
	}
	beforeStats := f.s.metrics().runtimeSnapshot()
	must(t, f.s.ProcessSettlementOnce(context.Background()))
	afterStats := f.s.metrics().runtimeSnapshot()
	for i, lane := range beforeStats.RecoveryLanes {
		if lane.Kind == "query_settlement" && (lane.Review != 40 || lane.Applied != 2 || afterStats.RecoveryLanes[i].Review != 40 || afterStats.RecoveryLanes[i].Applied != 2) {
			t.Fatal("B statistics do not reflect actual committed review/settlement")
		}
	}
}

func TestMySQLQuerySettlementFutureNullAndCancellation(t *testing.T) {
	for _, kind := range []string{"future", "null", "cancelled", "cancel_after_initial", "invalid_phase"} {
		t.Run(kind, func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "not-eligible-"+kind)
			n := int64(100)
			row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
			updates := map[string]any{}
			if kind == "future" {
				updates["recovery_due_at"] = gorm.Expr("UTC_TIMESTAMP(3) + INTERVAL 1 HOUR")
				updates["settlement_evidence_sha256"] = strings.Repeat("0", 64)
			}
			if kind == "null" {
				updates["recovery_due_at"] = nil
			}
			if kind == "invalid_phase" {
				updates["settlement_kind"] = "future-kind"
			}
			if len(updates) > 0 {
				must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ?", row.OrgID, row.TaskID, row.AttemptID).Updates(updates).Error)
				row = settlementReadReceipt(t, f, row)
			}
			settlementForbidNetwork(t, f)
			before := settlementMoneyAndProjections(t, f, v.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			name := "test:settlement-cancel-after-initial"
			hits := 0
			if kind == "cancel_after_initial" {
				must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
					if q.Statement.Table == "video_billing_entries" && q.Error == gorm.ErrRecordNotFound {
						hits++
						cancel()
					}
				}))
			}
			result, err := f.s.applyQuerySettlement(ctx, row)
			if kind == "cancel_after_initial" {
				must(t, f.s.DB.Callback().Query().Remove(name))
				if hits != 1 {
					t.Fatal("cancellation missed the actual initial read")
				}
			}
			if kind == "future" {
				if err != nil || result.disposition != queryRecoveryNoop {
					t.Fatal("future bad digest was applied or prematurely quarantined")
				}
			} else if err == nil {
				t.Fatal("barrier/invalid/cancelled direct application accepted")
			}
			if (kind == "cancelled" || kind == "cancel_after_initial") && !errors.Is(err, context.Canceled) {
				t.Fatal("B lost caller cancellation")
			}
			if !reflect.DeepEqual(row, settlementReadReceipt(t, f, row)) || settlementMoneyAndProjections(t, f, v.ID) != before {
				t.Fatal("ineligible B changed evidence/progress/funds")
			}
		})
	}
}

func TestMySQLQuerySettlementRoundDatabaseBudget(t *testing.T) {
	for _, shortParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole_round_2s", true: "shorter_parent"}[shortParent], func(t *testing.T) {
			f := newSettlementFixture(t)
			settlementForbidNetwork(t, f)
			name := "test:settlement-slow-candidate"
			hits := 0
			must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
				if q.Statement.Table != "r" {
					return
				}
				hits++
				var waited int
				err := q.Session(&gorm.Session{NewDB: true, Context: q.Statement.Context}).Raw("SELECT SLEEP(3)").Scan(&waited).Error
				q.AddError(err)
			}))
			ctx := context.Background()
			cancel := func() {}
			if shortParent {
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
			}
			defer cancel()
			started := time.Now()
			err := f.s.ProcessSettlementOnce(ctx)
			elapsed := time.Since(started)
			must(t, f.s.DB.Callback().Query().Remove(name))
			if hits != 1 || !errors.Is(err, context.DeadlineExceeded) || elapsed > 3*time.Second {
				t.Fatal("actual slow database candidate escaped whole-round/parent budget")
			}
			for _, lane := range f.s.metrics().runtimeSnapshot().RecoveryLanes {
				if lane.Kind == "query_settlement" && (lane.Selected != 0 || lane.Applied != 0 || lane.Review != 0 || lane.Deferred != 0 || lane.Failed != 1) {
					t.Fatal("candidate timeout invented committed progress")
				}
			}
		})
	}
}

func TestMySQLQuerySettlementClockAndDiagnosticBudgets(t *testing.T) {
	for _, mode := range []string{"clock_200ms", "diagnostic_1s", "diagnostic_remaining_parent"} {
		t.Run(mode, func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "bounded-"+mode)
			n := int64(100)
			row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
			if mode != "clock_200ms" {
				must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ?", row.OrgID, row.TaskID, row.AttemptID).UpdateColumn("settlement_evidence_sha256", strings.Repeat("0", 64)).Error)
				row = settlementReadReceipt(t, f, row)
			}
			settlementForbidNetwork(t, f)
			before := settlementMoneyAndProjections(t, f, v.ID)
			budget := 4 * time.Second
			if mode == "diagnostic_remaining_parent" {
				budget = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			parentDeadline, _ := ctx.Deadline()
			var firstPool gorm.ConnPool
			taskReads, slowHits := 0, 0
			name := "test:settlement-budget-" + mode
			must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
				if q.Statement.Table != "tasks" {
					return
				}
				taskReads++
				if taskReads == 1 {
					firstPool = q.Statement.ConnPool
					return
				}
				if mode == "clock_200ms" || taskReads != 2 {
					return
				}
				deadline, ok := q.Statement.Context.Deadline()
				if !ok || deadline.After(parentDeadline) || time.Until(deadline) > time.Second || firstPool == q.Statement.ConnPool {
					t.Error("diagnostic lost its independent transaction or bounded caller context")
				}
				slowHits++
				// Exercise the real driver's cancellation on the diagnostic's
				// connection, before the actual locking SELECT. No fake error.
				var waited int
				q.AddError(q.Session(&gorm.Session{NewDB: true, Context: q.Statement.Context}).Raw("SELECT SLEEP(3)").Scan(&waited).Error)
			}))
			must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
				if mode != "clock_200ms" || !strings.Contains(q.Statement.SQL.String(), "SELECT UTC_TIMESTAMP(3) AS database_time") {
					return
				}
				deadline, ok := q.Statement.Context.Deadline()
				if !ok || deadline.After(parentDeadline) || time.Until(deadline) > 200*time.Millisecond || firstPool != q.Statement.ConnPool {
					t.Error("clock lost its 200ms budget or current transaction")
				}
				slowHits++
				q.Statement.SQL.Reset()
				q.Statement.SQL.WriteString("SELECT UTC_TIMESTAMP(3) AS database_time, SLEEP(3) AS clock_delay")
				q.Statement.Vars = nil
			}))
			started := time.Now()
			result, err := f.s.applyQuerySettlement(ctx, row)
			elapsed := time.Since(started)
			must(t, f.s.DB.Callback().Query().Remove(name))
			must(t, f.s.DB.Callback().Row().Remove(name))
			wantTaskReads, maxElapsed := 2, 2*time.Second
			if mode == "clock_200ms" {
				wantTaskReads, maxElapsed = 1, time.Second
			}
			if slowHits != 1 || taskReads != wantTaskReads || !errors.Is(err, context.DeadlineExceeded) || result.disposition != queryRecoveryFailed || elapsed > maxElapsed {
				t.Fatalf("actual database deadline failed mode=%s slow=%d task_reads=%d result=%d elapsed=%s", mode, slowHits, taskReads, result.disposition, elapsed)
			}
			if mode != "diagnostic_remaining_parent" && ctx.Err() != nil {
				t.Fatal("child limit consumed the longer caller context")
			}
			if !reflect.DeepEqual(row, settlementReadReceipt(t, f, row)) || settlementMoneyAndProjections(t, f, v.ID) != before {
				t.Fatal("timed-out clock/diagnostic invented committed review or money")
			}
		})
	}
}
