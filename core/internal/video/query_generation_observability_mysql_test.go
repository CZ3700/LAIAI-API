package video

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type generationObservationTrace struct {
	logger.Interface
	mu      sync.Mutex
	elapsed []time.Duration
	failed  bool
}

func (l *generationObservationTrace) Trace(ctx context.Context, begin time.Time, query func() (string, int64), err error) {
	sql, _ := query()
	if !strings.Contains(sql, "MAX_EXECUTION_TIME(200)") {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.elapsed = append(l.elapsed, time.Since(begin))
	l.failed = l.failed || err != nil || ctx.Err() != nil
}

func TestMySQLQueryGenerationObservabilitySkewAndBudgets(t *testing.T) {
	h := queryRecoveryNew(t)
	f := h.users[0]
	now, err := authorityFixtureNow(h.db)
	must(t, err)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	org := f.key.OrgID
	// Planner/aggregation-only rows, never used as provider evidence. The large
	// completed population makes historical scans visible in the real plan.
	const historyTasks = 4096
	const receiptsPerTask = 8
	must(t, h.db.Transaction(func(tx *gorm.DB) error {
		for start := 0; start < historyTasks; start += 128 {
			tasks := make([]task.Task, 0, 128)
			states := make([]State, 0, 128)
			receipts := make([]QueryReceipt, 0, 128*receiptsPerTask)
			for index := start; index < start+128; index++ {
				id := fmt.Sprintf("metric-history-%04d", index)
				tasks = append(tasks, task.Task{OrgID: org, TaskID: id, Provider: Provider, Status: "succeeded", SubmittedAt: past, FinishedAt: &past})
				states = append(states, State{OrgID: org, TaskID: id, BillingState: "settled", RuntimeVersion: videoruntime.RuntimeV1, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, BillingSchemaVersion: videoruntime.BillingV1, CreatedAt: past, UpdatedAt: past, NextPollAt: past})
				for n := 0; n < receiptsPerTask; n++ {
					receipts = append(receipts, QueryReceipt{OrgID: org, TaskID: id, AttemptID: fmt.Sprintf("metric-history-%04d-%d", index, n), Sequence: uint64(n + 1), RuntimeVersion: videoruntime.RuntimeV1, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, ObservationSchemaVersion: 1, Outcome: "accepted", ObservationJSON: strings.Repeat("x", 1024), ReceivedAt: past, AppliedAt: &past, RecoveryDueAt: &past})
				}
			}
			if err := tx.CreateInBatches(tasks, 128).Error; err != nil {
				return err
			}
			if err := tx.CreateInBatches(states, 128).Error; err != nil {
				return err
			}
			if err := tx.CreateInBatches(receipts, 128).Error; err != nil {
				return err
			}
		}
		for _, kind := range []string{"g", "b", "future", "review", "closed", "invalid"} {
			id, attempt := "metric-"+kind, "metric-receipt-"+kind
			taskStatus, billing := "succeeded", "reserved"
			if kind == "g" || kind == "invalid" {
				taskStatus = "running"
			}
			if kind == "closed" {
				billing = "adjusted"
			}
			row := task.Task{OrgID: org, TaskID: id, Provider: Provider, Status: taskStatus, SubmittedAt: past}
			st := State{OrgID: org, TaskID: id, BillingState: billing, RuntimeVersion: videoruntime.RuntimeV2, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, BillingSchemaVersion: videoruntime.BillingV1, CreatedAt: past, UpdatedAt: past, NextPollAt: past}
			r := QueryReceipt{OrgID: org, TaskID: id, AttemptID: attempt, Sequence: 1, RuntimeVersion: videoruntime.RuntimeV2, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, ObservationSchemaVersion: 1, Outcome: "accepted", ObservationJSON: "{}", ReceivedAt: past, RecoveryDueAt: &past}
			if kind != "g" && kind != "invalid" {
				st.SettlementReceiptID = &attempt
				r.GenerationAppliedAt = &past
				r.SettlementKind = querySettlementUsage
				r.SettlementEvidenceSHA256 = strings.Repeat("a", 64)
			}
			if kind == "future" {
				r.RecoveryDueAt = &future
			}
			if kind == "closed" {
				r.AppliedAt = &past
				r.ApplyErrorCode = queryFinancialConflictCode
			}
			if kind == "invalid" {
				r.SettlementKind = querySettlementUsage
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			if err := tx.Create(&st).Error; err != nil {
				return err
			}
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
			if kind == "review" {
				if err := tx.Model(&QueryReceipt{}).Where("attempt_id = ?", attempt).UpdateColumn("recovery_due_at", nil).Error; err != nil {
					return err
				}
			}
			if kind == "closed" {
				r.AttemptID = "metric-blocked-g"
				r.Sequence = 2
				r.AppliedAt = nil
				r.GenerationAppliedAt = nil
				r.SettlementKind = ""
				r.SettlementEvidenceSHA256 = ""
				r.ApplyErrorCode = ""
				if err := tx.Create(&r).Error; err != nil {
					return err
				}
			}
		}
		return nil
	}))
	var count int64
	must(t, h.db.Model(&QueryReceipt{}).Count(&count).Error)
	if count != historyTasks*receiptsPerTask+7 {
		t.Fatal("metrics skew fixture cardinality differs")
	}
	stats, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = h.db.WithContext(stats).Exec("ANALYZE TABLE tasks,video_task_states,video_query_receipts").Error
	cancel()
	must(t, err)
	for _, query := range []struct{ name, sql string }{{"tasks", workerTaskSQL}, {"receipts", workerReceiptSQL}} {
		var plans []struct {
			Table  string  `gorm:"column:table"`
			Access string  `gorm:"column:type"`
			Key    *string `gorm:"column:key"`
			Rows   uint64  `gorm:"column:rows"`
			Extra  string  `gorm:"column:Extra"`
		}
		planCtx, planCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = h.db.WithContext(planCtx).Raw("EXPLAIN " + query.sql).Scan(&plans).Error
		planCancel()
		must(t, err)
		raw, e := json.Marshal(plans)
		must(t, e)
		// Keep the complete real plan even if a later requirement fails. A
		// covering index scan is still a historical index scan, not a point read.
		t.Logf("aggregation=%s sql_sha256=%x plan=%s", query.name, sha256.Sum256([]byte(query.sql)), raw)
		physical := 0
		for _, plan := range plans {
			if plan.Table == "t" || plan.Table == "v" || plan.Table == "r" || plan.Table == "closed" {
				physical++
				if plan.Access == "ALL" || plan.Key == nil {
					t.Fatal("skewed metrics plan scans historical physical rows")
				}
				if plan.Table == "closed" && !strings.Contains(plan.Extra, "Using index") {
					t.Fatal("closed marker aggregation is not an index-only scan")
				}
			}
		}
		if physical < 2 {
			t.Fatal("EXPLAIN omitted the real aggregation branches")
		}
	}
	trace := &generationObservationTrace{Interface: logger.Discard}
	s := h.service(t)
	s.DB = s.DB.Session(&gorm.Session{Logger: trace})
	h.forbidNetwork(t, s)
	funds := h.finances(t)
	out := s.WorkerObservability(context.Background())
	if !out.Database.Available || !out.Database.Complete || out.Database.ErrorCode != "" || out.Database.Snapshot == nil {
		t.Fatal("bounded aggregation failed on skewed history")
	}
	snapshot := out.Database.Snapshot
	got := snapshot.QueryReceipts
	if snapshot.TaskCounts.TerminalReserved != 3 || got.GenerationPending != 1 || got.SettlementDue != 1 || got.SettlementFuture != 1 || got.PhaseReview != 1 || got.FinancialConflict != 1 || got.InvalidPhase != 1 {
		t.Fatal("phase metrics hid reserved/review/closed work or counted blocked G")
	}
	trace.mu.Lock()
	durations := append([]time.Duration(nil), trace.elapsed...)
	failed := trace.failed
	trace.mu.Unlock()
	if len(durations) != 5 || failed || snapshot.CompletedAt.Sub(snapshot.StartedAt) > time.Second {
		t.Fatal("metrics changed five-query or one-second budget")
	}
	for i, elapsed := range durations {
		if elapsed > 200*time.Millisecond {
			t.Fatal("actual aggregation exceeded its per-SQL budget")
		}
		t.Logf("aggregation=%d elapsed_us=%d", i, elapsed.Microseconds())
	}
	before := len(durations)
	s.WorkerObservability(context.Background())
	trace.mu.Lock()
	after := len(trace.elapsed)
	trace.mu.Unlock()
	if before != after || funds != h.finances(t) || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
		t.Fatal("metrics refreshed cache or changed money/network")
	}
	encoded, e := json.Marshal(out)
	must(t, e)
	for _, private := range []string{"metric-history-", "metric-receipt-", "metric-blocked-g", "org_id", "task_id", "SELECT "} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("metrics exposed private identity or SQL")
		}
	}
}
