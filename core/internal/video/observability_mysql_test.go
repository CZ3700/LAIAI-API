package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

func workerObservationSQL(tx *gorm.DB) bool {
	return strings.Contains(tx.Statement.SQL.String(), "MAX_EXECUTION_TIME(200)")
}

func workerOperationTotal(snapshot *WorkerDatabaseSnapshot, kind, status string) int64 {
	var count int64
	for _, row := range snapshot.OperationCounts {
		if row.Kind == kind && row.Status == status {
			count += row.Count
		}
	}
	return count
}

func TestMySQLWorkerObservationAggregatesReadOnlyAndIndexed(t *testing.T) {
	f := mysqlFixture(t)
	baseline, code := f.s.collectWorkerDatabase(context.Background())
	if code != "" {
		t.Fatal("baseline unavailable", code)
	}
	// Earlier integration fixtures retain pending outbox rows. They may become
	// due between the two database snapshots, so retain their scheduling facts
	// and evaluate the exact expected count at the final database timestamp.
	// Static backlog counts still use exact deltas against the baseline.
	var priorOutboxDueTimes []time.Time
	must(t, f.s.DB.Model(&Outbox{}).Where("status = ?", "pending").Pluck("next_attempt_at", &priorOutboxDueTimes).Error)
	if int64(len(priorOutboxDueTimes)) != baseline.Outbox.Pending {
		t.Fatal("outbox baseline changed before fixture insertion", len(priorOutboxDueTimes), baseline.Outbox.Pending)
	}
	ids := []string{}
	seedTask := func(status, provider string, deleted bool) string {
		id, err := randomID("task_")
		must(t, err)
		ids = append(ids, id)
		must(t, f.s.DB.Create(&task.Task{TaskID: id, OrgID: f.key.OrgID, Provider: provider, Status: status, SubmittedAt: f.now}).Error)
		state := State{TaskID: id, OrgID: f.key.OrgID, BillingState: "reserved", NextPollAt: f.now, CreatedAt: f.now, UpdatedAt: f.now}
		if deleted {
			state.DeletedAt = &f.now
		}
		must(t, f.s.DB.Create(&state).Error)
		return id
	}
	t.Cleanup(func() {
		// Only synthetic rows owned by this fixture, never a table reset.
		for _, table := range []string{"video_query_receipts", "video_outbox", "video_operations", "video_task_states", "tasks"} {
			if err := f.s.DB.Exec("DELETE FROM "+table+" WHERE org_id = ?", f.key.OrgID).Error; err != nil {
				t.Errorf("fixture cleanup failed for %s: %v", table, err)
			}
		}
	})
	accepted := seedTask("accepted", Provider, false)
	seedTask("submitting", Provider, false)
	seedTask("submission_unknown", Provider, false)
	seedTask("queued", Provider, false)
	seedTask("running", Provider, false)
	seedTask("succeeded", Provider, false)
	seedTask("accepted", "legacy", false)
	seedTask("accepted", Provider, true)
	for _, cell := range []struct{ kind, status string }{
		{"create_task", "pending"}, {"cancel_task", "unknown"}, {"delete_task", "in_flight"},
		{"secret-kind-one", "pending"}, {"secret-kind-two", "pending"}, {"create_task", "confirmed"},
		{"create_task", "needs_review"}, {"cancel_task", "needs_review"}, {"delete_task", "needs_review"}, {"secret-kind-one", "needs_review"},
	} {
		id, err := randomID("op_")
		must(t, err)
		must(t, f.s.DB.Create(&Operation{ID: id, OrgID: f.key.OrgID, ResourceID: accepted, Kind: cell.kind, Status: cell.status, Version: 1, CreatedAt: f.now, UpdatedAt: f.now}).Error)
	}
	for i, failure := range []string{"", "settlement_conflict", "query_apply_failed", "already_applied"} {
		id, err := randomID("att_")
		must(t, err)
		r := QueryReceipt{AttemptID: id, OrgID: f.key.OrgID, TaskID: accepted, Sequence: uint64(i + 1), ReceivedAt: f.now, ApplyErrorCode: failure}
		if failure == "already_applied" {
			r.AppliedAt = &f.now
		}
		must(t, f.s.DB.Create(&r).Error)
	}
	for i, state := range []string{"pending", "pending", "processed"} {
		id, err := randomID("evt_")
		must(t, err)
		due := f.now.Add(-time.Hour)
		if i == 1 {
			due = f.now.Add(time.Hour)
		}
		must(t, f.s.DB.Create(&Outbox{EventID: id, OrgID: f.key.OrgID, TaskID: accepted, Status: state, NextAttemptAt: due, CreatedAt: f.now}).Error)
	}
	var readOnly atomic.Bool
	var statements atomic.Int32
	name := fmt.Sprintf("test:worker-observation-read-only-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if !workerObservationSQL(tx) {
			return
		}
		if statements.Add(1) == 1 {
			// Prove that the server enforces READ ONLY, not merely that these
			// five queries happen to be SELECTs. No row can match this statement.
			_, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context, "UPDATE tasks SET status = status WHERE 1 = 0")
			var dbErr *mysqlDriver.MySQLError
			if errors.As(err, &dbErr) && dbErr.Number == 1792 {
				readOnly.Store(true)
			} else {
				tx.AddError(errors.New("observation transaction is not read only"))
			}
		}
	}))
	t.Cleanup(func() { f.s.DB.Callback().Row().Remove(name) })
	out := f.s.WorkerObservability(context.Background())
	if !out.Database.Available || !out.Database.Complete || out.Database.Stale || out.Database.Snapshot == nil || !readOnly.Load() || statements.Load() != 5 {
		t.Fatal("observation is not a complete five-query read-only snapshot", out.Database.ErrorCode, statements.Load())
	}
	snapshot := out.Database.Snapshot
	if snapshot.DatabaseTime.IsZero() || snapshot.CompletedAt.Before(snapshot.StartedAt) || !out.DBPool.Available {
		t.Fatal("missing snapshot or pool metadata")
	}
	got, before := snapshot.TaskCounts, baseline.TaskCounts
	if got.Accepted-before.Accepted != 1 || got.Submitting-before.Submitting != 1 || got.SubmissionUnknown-before.SubmissionUnknown != 1 || got.Queued-before.Queued != 1 || got.Running-before.Running != 1 {
		t.Fatal("task counts included terminal, deleted or legacy rows", got, before)
	}
	for _, cell := range []struct {
		kind, status string
		delta        int64
	}{
		{"create_task", "pending", 1}, {"cancel_task", "unknown", 1}, {"delete_task", "in_flight", 1}, {"other", "pending", 2},
		{"create_task", "needs_review", 1}, {"cancel_task", "needs_review", 1}, {"delete_task", "needs_review", 1}, {"other", "needs_review", 1},
	} {
		if workerOperationTotal(snapshot, cell.kind, cell.status)-workerOperationTotal(baseline, cell.kind, cell.status) != cell.delta {
			t.Fatal("operation aggregation lost its closed enum", cell)
		}
	}
	// Every persisted status, including manual-review work, must remain in the
	// four-kind/four-status closed projection. Unknown kinds share "other";
	// completed operations never become an additional status cell.
	operationCells := make(map[[2]string]bool)
	for _, cell := range snapshot.OperationCounts {
		switch cell.Kind {
		case "create_task", "cancel_task", "delete_task", "other":
		default:
			t.Fatal("operation aggregation exposed an unrecognized kind")
		}
		switch cell.Status {
		case "pending", "in_flight", "unknown", "needs_review":
		default:
			t.Fatal("operation aggregation included a completed or unrecognized status")
		}
		identity := [2]string{cell.Kind, cell.Status}
		if operationCells[identity] || cell.Count <= 0 {
			t.Fatal("operation aggregation returned a duplicate or nonpositive cell", identity)
		}
		operationCells[identity] = true
	}
	var priorDueAtSnapshot int64
	for _, nextAttemptAt := range priorOutboxDueTimes {
		if !nextAttemptAt.After(snapshot.DatabaseTime) {
			priorDueAtSnapshot++
		}
	}
	if len(snapshot.OperationCounts) > 16 || snapshot.QueryReceipts.RetryablePending-baseline.QueryReceipts.RetryablePending != 2 || snapshot.QueryReceipts.ConflictPending-baseline.QueryReceipts.ConflictPending != 1 || snapshot.Outbox.Pending-baseline.Outbox.Pending != 2 || snapshot.Outbox.Due-priorDueAtSnapshot != 1 {
		t.Fatalf("backlog counts incorrectly include completed or future work: operation_cells=%d receipts=%+v baseline_receipts=%+v outbox=%+v baseline_outbox=%+v prior_due_at_snapshot=%d database_time=%s baseline_database_time=%s", len(snapshot.OperationCounts), snapshot.QueryReceipts, baseline.QueryReceipts, snapshot.Outbox, baseline.Outbox, priorDueAtSnapshot, snapshot.DatabaseTime, baseline.DatabaseTime)
	}
	if priorDueAtSnapshot != baseline.Outbox.Due {
		t.Logf("existing pending outbox rows becoming due between snapshots: %d", priorDueAtSnapshot-baseline.Outbox.Due)
	}
	body, err := json.Marshal(out)
	must(t, err)
	for _, secret := range append(ids, "secret-kind-one", "secret-kind-two", "org_id", "task_id", "SELECT ", "credential") {
		if strings.Contains(string(body), secret) {
			t.Fatal("observation exposed protected identity or SQL", secret)
		}
	}
	// A cached GET performs no additional aggregation and owns no transaction.
	again := f.s.WorkerObservability(context.Background())
	if statements.Load() != 5 || !again.Database.Snapshot.CompletedAt.Equal(snapshot.CompletedAt) {
		t.Fatal("fresh cache reran database work")
	}
	// Capture actual plans on the production database engine. Small fixtures
	// may prefer a table scan; require the intended existing keys to be usable.
	for _, query := range []struct {
		sql, index string
		args       []any
	}{
		{workerTaskSQL, "idx_tasks_unfinished", nil},
		{workerOperationSQL, "idx_video_operation_dispatch", nil},
		{workerReceiptSQL, "idx_video_query_unapplied", nil},
		{workerOutboxSQL, "idx_video_outbox_ready", []any{snapshot.DatabaseTime}},
	} {
		var plan []map[string]any
		must(t, f.s.DB.Raw("EXPLAIN "+query.sql, query.args...).Scan(&plan).Error)
		usable := false
		for _, row := range plan {
			for _, field := range []string{"possible_keys", "key"} {
				value := fmt.Sprint(row[field])
				if bytes, ok := row[field].([]byte); ok {
					value = string(bytes)
				}
				usable = usable || strings.Contains(value, query.index)
			}
		}
		if !usable {
			t.Fatalf("observation plan cannot use %s: %+v", query.index, plan)
		}
	}
}

func TestMySQLWorkerObservationSingleRefreshAndStaleFailure(t *testing.T) {
	f := mysqlFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	defer resume()
	var queries atomic.Int32
	var failRefresh atomic.Bool
	name := fmt.Sprintf("test:worker-observation-cache-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if !workerObservationSQL(tx) {
			return
		}
		if queries.Add(1) == 1 {
			close(entered)
			<-release
		}
		if failRefresh.Load() && strings.Contains(tx.Statement.SQL.String(), "/* blocked_contracts */") {
			tx.AddError(errors.New("secret database failure"))
		}
	}))
	t.Cleanup(func() { f.s.DB.Callback().Row().Remove(name) })
	done := make(chan WorkerObservabilitySnapshot, 1)
	go func() { done <- f.s.WorkerObservability(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			view := f.s.WorkerObservability(context.Background())
			if !view.Database.Refreshing || view.Database.Snapshot != nil {
				t.Error("concurrent refresh did not return an explicit in-progress state")
			}
		}()
	}
	wg.Wait()
	if queries.Load() != 1 {
		t.Fatal("concurrent callers duplicated the refresh")
	}
	resume()
	first := <-done
	if !first.Database.Available || queries.Load() != 5 {
		t.Fatal("initial refresh failed", first.Database.ErrorCode, queries.Load())
	}
	// Change the first aggregate, then fail the fifth SQL. No partial refresh
	// may be published even though the first four queries succeeded.
	id, err := randomID("task_")
	must(t, err)
	must(t, f.s.DB.Create(&task.Task{TaskID: id, OrgID: f.key.OrgID, Provider: Provider, Status: "accepted", SubmittedAt: f.now}).Error)
	must(t, f.s.DB.Create(&State{TaskID: id, OrgID: f.key.OrgID, BillingState: "reserved", NextPollAt: f.now, CreatedAt: f.now, UpdatedAt: f.now}).Error)
	t.Cleanup(func() {
		for _, table := range []string{"video_task_states", "tasks"} {
			if err := f.s.DB.Exec("DELETE FROM "+table+" WHERE org_id = ? AND task_id = ?", f.key.OrgID, id).Error; err != nil {
				t.Errorf("fixture cleanup failed for %s: %v", table, err)
			}
		}
	})
	m := f.s.metrics()
	m.mu.Lock()
	m.cacheAttempt = time.Now().Add(-2 * workerObservationTTL)
	m.mu.Unlock()
	failRefresh.Store(true)
	stale := f.s.WorkerObservability(context.Background())
	if !stale.Database.Available || !stale.Database.Stale || stale.Database.ErrorCode != "database_unavailable" || !stale.Database.Snapshot.CompletedAt.Equal(first.Database.Snapshot.CompletedAt) || stale.Database.Snapshot.TaskCounts.Accepted != first.Database.Snapshot.TaskCounts.Accepted || queries.Load() != 10 {
		t.Fatal("failed refresh erased or misrepresented the previous snapshot")
	}
	f.s.WorkerObservability(context.Background())
	if queries.Load() != 10 {
		t.Fatal("failed refresh bypassed the retry interval")
	}
}

func TestMySQLWorkerObservationTimeoutAndCancellation(t *testing.T) {
	f := mysqlFixture(t)
	name := fmt.Sprintf("test:worker-observation-timeout-%d", f.key.OrgID)
	var queries atomic.Int32
	must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if workerObservationSQL(tx) && queries.Add(1) == 1 {
			// Exercise the actual driver's context cancellation on a read-only
			// query, not a local sleep or a synthetic context error.
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT SLEEP(3) AS database_time")
		}
	}))
	t.Cleanup(func() { f.s.DB.Callback().Row().Remove(name) })
	started := time.Now()
	out := f.s.WorkerObservability(context.Background())
	if elapsed := time.Since(started); elapsed > 2*time.Second || out.Database.Available || out.Database.Snapshot != nil || out.Database.ErrorCode != "snapshot_timeout" {
		t.Fatal("observation did not respect its total deadline", elapsed, out.Database.ErrorCode)
	}
	m := f.s.metrics()
	m.mu.Lock()
	m.cacheAttempt = time.Time{}
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out = f.s.WorkerObservability(ctx)
	if out.Database.Available || out.Database.Snapshot != nil || out.Database.ErrorCode != "snapshot_cancelled" {
		t.Fatal("cancelled refresh became a valid zero-valued snapshot", out.Database.ErrorCode)
	}
}

func TestMySQLWorkerObservationAdapterAndGuardWiring(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "observability-wiring-create")
	f.dispatch(t, v.ID)
	runtime := f.s.metrics().runtimeSnapshot()
	if observationCounter(t, runtime, "dispatch_guard", "create_task", "allowed") != 1 || observationCounter(t, runtime, "adapter_calls", "submit", "accepted") != 1 {
		t.Fatal("create did not observe its single allowed adapter call")
	}
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").First(&op).Error)
	allowed, err := f.s.operationMaySend(context.Background(), op)
	must(t, err)
	if allowed {
		t.Fatal("completed operation regained send permission")
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	runtime = f.s.metrics().runtimeSnapshot()
	if observationCounter(t, runtime, "dispatch_guard", "create_task", "denied") != 1 || observationCounter(t, runtime, "adapter_calls", "submit", "accepted") != 1 || observationCounter(t, runtime, "adapter_calls", "query", string(ark.OutcomeAccepted)) != 1 {
		t.Fatal("denied guard was counted as a call or query was not observed")
	}
}
