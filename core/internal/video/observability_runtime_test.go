package video

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func TestMySQLWorkerObservationBlockedContractsAreIndependentAndIndexed(t *testing.T) {
	f := mysqlFixture(t)
	baseline, code := f.s.collectWorkerDatabase(context.Background())
	if code != "" {
		t.Fatal("baseline unavailable", code)
	}
	type cell struct {
		name                      string
		state, operation          map[string]any
		status, billing, provider string
		snapshot                  string
		wrongSnapshot, deleted    bool
		receiptSchema             uint32
		receiptRuntime            string
		applied                   bool
		expected                  WorkerBlockedContractCounts
	}
	cases := []cell{
		{name: "supported"},
		{name: "empty_state", state: map[string]any{"runtime_version": ""}, expected: WorkerBlockedContractCounts{1, 1, 1}},
		{name: "unknown_snapshot", snapshot: "private-future-billing", expected: WorkerBlockedContractCounts{1, 1, 1}},
		{name: "unknown_adapter", state: map[string]any{"adapter_version": "private-future-adapter"}, operation: map[string]any{"status": "confirmed"}, expected: WorkerBlockedContractCounts{1, 0, 1}},
		{name: "operation_mismatch", operation: map[string]any{"protocol_contract_version": ""}, expected: WorkerBlockedContractCounts{0, 1, 0}},
		{name: "unknown_receipt", receiptRuntime: "private-future-runtime", expected: WorkerBlockedContractCounts{0, 0, 1}},
		{name: "unknown_observation_schema", receiptSchema: 2, expected: WorkerBlockedContractCounts{0, 0, 1}},
		{name: "finished_excluded", status: "succeeded", billing: "settled", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "confirmed"}, applied: true},
		{name: "deleted_excluded", deleted: true, status: "accepted", billing: "reserved", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "rejected"}, applied: true},
		{name: "terminal_reserved_unknown", status: "succeeded", billing: "reserved", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "confirmed"}, applied: true, expected: WorkerBlockedContractCounts{1, 0, 0}},
		{name: "deleted_terminal_reserved_unknown", deleted: true, status: "failed", billing: "reserved", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "confirmed"}, applied: true, expected: WorkerBlockedContractCounts{1, 0, 0}},
		{name: "cancelled_reserved_unknown", status: "cancelled", billing: "reserved", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "confirmed"}, applied: true, expected: WorkerBlockedContractCounts{1, 0, 0}},
		{name: "expired_reserved_unknown", status: "expired", billing: "reserved", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "confirmed"}, applied: true, expected: WorkerBlockedContractCounts{1, 0, 0}},
		{name: "supported_terminal_reserved", status: "succeeded", billing: "reserved", operation: map[string]any{"status": "confirmed"}, applied: true},
		{name: "deleted_nonterminal_reserved", deleted: true, status: "running", billing: "reserved", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "confirmed"}, applied: true},
		{name: "deleted_unknown_status_reserved", deleted: true, status: "future_status", billing: "reserved", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "confirmed"}, applied: true},
		{name: "deleted_pending_usage_included", deleted: true, status: "succeeded", billing: "pending_usage", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "confirmed"}, applied: true, expected: WorkerBlockedContractCounts{1, 0, 0}},
		{name: "active_pending_not_duplicated", billing: "pending_usage", state: map[string]any{"runtime_version": ""}, operation: map[string]any{"status": "unknown"}, expected: WorkerBlockedContractCounts{1, 1, 1}},
		{name: "supported_pending_usage", status: "succeeded", billing: "pending_usage", operation: map[string]any{"status": "confirmed"}, applied: true},
		{name: "wrong_snapshot_identity", wrongSnapshot: true, expected: WorkerBlockedContractCounts{1, 1, 1}},
		{name: "legacy_excluded", provider: "legacy", state: map[string]any{"runtime_version": ""}},
		{name: "completed_operation_excluded", operation: map[string]any{"adapter_version": "private-future-adapter", "status": "rejected"}},
	}
	var firstSnapshot uint64
	want := baseline.BlockedContracts
	for i, tc := range cases {
		v := f.create(t, fmt.Sprintf("observe-runtime-%d", i))
		var snapshot videobilling.Snapshot
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&snapshot).Error)
		if i == 0 {
			firstSnapshot = snapshot.ID
		}
		state := tc.state
		if state == nil {
			state = map[string]any{}
		}
		if tc.deleted {
			state["deleted_at"] = f.now
		}
		if tc.billing != "" {
			state["billing_state"] = tc.billing
		}
		if tc.wrongSnapshot {
			state["billing_snapshot_id"] = firstSnapshot
		}
		if len(state) > 0 {
			must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Updates(state).Error)
		}
		if tc.snapshot != "" {
			must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("billing_schema_version", tc.snapshot).Error)
		}
		if tc.status != "" {
			must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("status", tc.status).Error)
		}
		if tc.provider != "" {
			must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("provider", tc.provider).Error)
		}
		if len(tc.operation) > 0 {
			must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ?", f.key.OrgID, v.ID).Updates(tc.operation).Error)
		}
		runtime := tc.receiptRuntime
		if runtime == "" {
			runtime = videoruntime.RuntimeV1
		}
		schema := tc.receiptSchema
		if schema == 0 {
			schema = 1
		}
		r := QueryReceipt{AttemptID: fmt.Sprintf("observe_%d_%d", f.key.OrgID, i), OrgID: f.key.OrgID, TaskID: v.ID, Sequence: 1, RuntimeVersion: runtime, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, ObservationSchemaVersion: schema, ReceivedAt: f.now, Outcome: string(ark.OutcomeAccepted)}
		if tc.applied {
			r.AppliedAt = &f.now
		}
		must(t, f.s.DB.Create(&r).Error)
		want.Tasks += tc.expected.Tasks
		want.Operations += tc.expected.Operations
		want.QueryReceipts += tc.expected.QueryReceipts
	}
	t.Cleanup(func() {
		_ = f.s.DB.Model(&State{}).Where("org_id = ?", f.key.OrgID).Update("runtime_version", "observation-fixture-only").Error
	})
	out := f.s.WorkerObservability(context.Background())
	if !out.Database.Available || out.Database.Stale || out.Database.Snapshot == nil || out.Database.Snapshot.BlockedContracts != want {
		t.Fatalf("blocked contract populations differ: error=%s got=%+v want=%+v", out.Database.ErrorCode, out.Database.Snapshot, want)
	}
	raw, err := json.Marshal(out)
	must(t, err)
	for _, forbidden := range []string{"private-future", "runtime_version", "adapter_version", "schema_version\":\"", "payload", "credential"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("contract counts leaked a version or protected data")
		}
	}
	query := workerBlockedContractsQuery(f.s.DB)
	var plan []map[string]any
	must(t, f.s.DB.Raw("EXPLAIN "+query.Statement.SQL.String(), query.Statement.Vars...).Scan(&plan).Error)
	for _, index := range []string{"idx_tasks_unfinished", "idx_video_runtime_pending_usage", "idx_video_operation_dispatch", "idx_video_query_unapplied"} {
		usable := false
		for _, row := range plan {
			for _, field := range []string{"possible_keys", "key"} {
				value := fmt.Sprint(row[field])
				if bytes, ok := row[field].([]byte); ok {
					value = string(bytes)
				}
				usable = usable || strings.Contains(value, index)
			}
		}
		if !usable {
			t.Fatalf("blocked-contract plan cannot use %s: %+v", index, plan)
		}
	}
	if f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("contract observation contacted the provider")
	}
}

func TestMySQLWorkerObservationFifthQueryTimeoutKeepsCompleteCache(t *testing.T) {
	f := mysqlFixture(t)
	first := f.s.WorkerObservability(context.Background())
	if !first.Database.Available || first.Database.Snapshot == nil {
		t.Fatal("initial complete snapshot unavailable", first.Database.ErrorCode)
	}
	var queries atomic.Int32
	name := fmt.Sprintf("test:blocked-observation-timeout-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(tx *gorm.DB) {
		if !workerObservationSQL(tx) {
			return
		}
		queries.Add(1)
		if strings.Contains(tx.Statement.SQL.String(), "/* blocked_contracts */") {
			// Actual MySQL I/O exceeds the shared context; the fifth aggregation
			// cannot publish the first four as a supposedly complete snapshot.
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT SLEEP(3) + 0 AS tasks")
			tx.Statement.Vars = nil
		}
	}))
	t.Cleanup(func() { f.s.DB.Callback().Row().Remove(name) })
	m := f.s.metrics()
	m.mu.Lock()
	m.cacheAttempt = time.Now().Add(-2 * workerObservationTTL)
	m.mu.Unlock()
	started := time.Now()
	stale := f.s.WorkerObservability(context.Background())
	if elapsed := time.Since(started); elapsed > 2*time.Second || !stale.Database.Available || !stale.Database.Stale || !stale.Database.Complete || stale.Database.ErrorCode != "snapshot_timeout" || stale.Database.Snapshot == nil || !stale.Database.Snapshot.CompletedAt.Equal(first.Database.Snapshot.CompletedAt) || stale.Database.Snapshot.BlockedContracts != first.Database.Snapshot.BlockedContracts || queries.Load() != 5 {
		t.Fatal("fifth timeout bypassed deadline or published partial data", elapsed, stale.Database.ErrorCode, queries.Load())
	}
	f.s.WorkerObservability(context.Background())
	if queries.Load() != 5 {
		t.Fatal("failed fifth aggregation bypassed the 15-second retry interval")
	}
}
