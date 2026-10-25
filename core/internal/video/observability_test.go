package video

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
)

func observationCounter(t *testing.T, runtime WorkerRuntimeObservation, name, kind, outcome string) uint64 {
	t.Helper()
	for _, counter := range runtime.Counters {
		if counter.Name == name && counter.Kind == kind && counter.Outcome == outcome {
			return counter.Value
		}
	}
	t.Fatalf("missing fixed counter %s/%s/%s", name, kind, outcome)
	return 0
}

func TestWorkerObservabilityConcurrentLiteralAndFixedCardinality(t *testing.T) {
	s := &Service{Now: func() time.Time { panic("observability must not use the business clock") }}
	const workers, calls = 24, 50
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < calls; j++ {
				s.observeGuard("org_id=private-identity", false, nil)
				s.observeAdapterCall("https://private-provider.invalid/secret", ark.Result{Outcome: ark.Outcome("private upstream message"), HTTPStatus: 429}, time.Now())
				s.metrics().runtimeSnapshot()
			}
		}()
	}
	wg.Wait()
	out := s.WorkerObservability(context.Background())
	if observationCounter(t, out.Runtime, "dispatch_guard", "other", "denied") != workers*calls || observationCounter(t, out.Runtime, "adapter_calls", "other", "other") != workers*calls || observationCounter(t, out.Runtime, "adapter_http_429", "other", "") != workers*calls {
		t.Fatal("concurrent observations were lost")
	}
	if len(out.Runtime.Counters) != 55 || len(out.Runtime.Durations) != 7 {
		t.Fatal("metric cardinality changed", len(out.Runtime.Counters), len(out.Runtime.Durations))
	}
	if out.Database.Available || out.Database.Snapshot != nil || out.Database.ErrorCode != "database_unavailable" || out.DBPool.Available {
		t.Fatal("nil database fabricated a snapshot", out.Database)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-identity", "private-provider", "private upstream", "org_id", "task_id", "payload", "credential"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("observation leaked a free-form label", forbidden)
		}
	}
	// A distinct service starts a distinct process-local observation lifetime.
	other := (&Service{}).WorkerObservability(context.Background())
	if observationCounter(t, other.Runtime, "adapter_calls", "other", "other") != 0 {
		t.Fatal("metrics leaked into a new service")
	}
}

func TestWorkerObservabilityReportsStickySavePauseWithoutSensitiveCause(t *testing.T) {
	s := &Service{}
	before := s.WorkerObservability(context.Background())
	if before.Runtime.DispatchPaused || before.Runtime.DispatchPauseReason != "" {
		t.Fatal("fresh process reported a persistence pause")
	}
	// Exercise the real failure latch, rather than assigning the internal flag.
	_, err := s.saveCreateResponse(context.Background(), ExecutionAttempt{}, ark.Result{Body: []byte("private upstream response")}, "private-credential")
	if !errors.Is(err, ErrResponseSaveUnconfirmed) {
		t.Fatal("invalid evidence did not report unconfirmed persistence")
	}
	if allowed, err := s.operationMaySend(context.Background(), Operation{Kind: "create_task"}); allowed || !errors.Is(err, ErrDispatchPaused) {
		t.Fatal("paused process attempted another send")
	}
	for range 2 {
		out := s.WorkerObservability(context.Background())
		if !out.Runtime.DispatchPaused || out.Runtime.DispatchPauseReason != "response_persistence_failed" || out.Runtime.Scope != "process_since_start" {
			t.Fatal("read-only observation cleared or misrepresented process pause")
		}
		if observationCounter(t, out.Runtime, "dispatch_guard", "create_task", "denied") != 1 || observationCounter(t, out.Runtime, "dispatch_guard", "create_task", "db_error") != 0 {
			t.Fatal("known persistence pause was counted as a new database failure")
		}
		body, err := json.Marshal(out)
		if err != nil || strings.Contains(string(body), "private upstream") || strings.Contains(string(body), "private-credential") {
			t.Fatal("pause observation leaked captured response or credential")
		}
	}
	if (&Service{}).WorkerObservability(context.Background()).Runtime.DispatchPaused {
		t.Fatal("process-local pause leaked into an independent service")
	}
}

func TestWorkerObservabilityCycleAndDurationSemantics(t *testing.T) {
	m := newWorkerMetrics()
	m.startCycle()(nil)
	m.startCycle()(errors.New("private error must never be a label"))
	out := m.runtimeSnapshot()
	if observationCounter(t, out, "cycles_started", "cycle", "") != 2 || observationCounter(t, out, "cycles_completed", "cycle", "") != 2 || observationCounter(t, out, "cycle_errors", "cycle", "") != 1 || out.LastCycleStartedAt == nil || out.LastCycleCompletedAt == nil || out.LastCycleErrorAt == nil {
		t.Fatal("cycle result accounting is incorrect")
	}
	var timing workerTiming
	timing.observe(10 * time.Millisecond)
	timing.observe(50 * time.Millisecond)
	timing.observe(121 * time.Second)
	if timing.count != 3 || timing.buckets[0] != 1 || timing.buckets[1] != 2 || timing.buckets[len(timing.buckets)-1] != 2 || timing.max != 121*time.Second || timing.sum != 121*time.Second+60*time.Millisecond {
		t.Fatal("fixed cumulative buckets lost overflow or exact-boundary samples", timing)
	}
	s := &Service{}
	s.observeGuard("create_task", true, errors.New("partial result and DB error"))
	s.observeAdapterCall("submit", ark.Result{Outcome: ark.OutcomeNotSent}, time.Now())
	runtime := s.metrics().runtimeSnapshot()
	if observationCounter(t, runtime, "dispatch_guard", "create_task", "db_error") != 1 || observationCounter(t, runtime, "dispatch_guard", "create_task", "allowed") != 0 || observationCounter(t, runtime, "adapter_calls", "submit", "not_sent") != 1 {
		t.Fatal("error or not-sent semantics were flattened")
	}
}

func TestWorkerObservationCacheCopyAndUnavailableSemantics(t *testing.T) {
	s := &Service{}
	m := s.metrics()
	now := time.Now().UTC()
	m.cacheAttempt = time.Now()
	m.cache = &WorkerDatabaseSnapshot{StartedAt: now, CompletedAt: now, DatabaseTime: now, TaskCounts: WorkerTaskCounts{Accepted: 7, OldestUnknownSubmission: &now}, OperationCounts: []WorkerOperationCount{{Kind: "create_task", Status: "pending", Count: 7}}, BlockedContracts: WorkerBlockedContractCounts{Tasks: 3, Operations: 2, QueryReceipts: 1}}
	first := s.WorkerObservability(context.Background())
	if !first.Database.Available || first.Database.Stale || !first.Database.Complete {
		t.Fatal("fresh cache was marked unavailable")
	}
	first.Database.Snapshot.TaskCounts.Accepted = 999
	first.Database.Snapshot.OperationCounts[0].Count = 999
	first.Database.Snapshot.BlockedContracts.Tasks = 999
	*first.Database.Snapshot.TaskCounts.OldestUnknownSubmission = time.Time{}
	second := s.WorkerObservability(context.Background())
	if second.Database.Snapshot.TaskCounts.Accepted != 7 || second.Database.Snapshot.OperationCounts[0].Count != 7 || second.Database.Snapshot.TaskCounts.OldestUnknownSubmission.IsZero() || second.Database.Snapshot.BlockedContracts.Tasks != 3 {
		t.Fatal("caller mutated the cached snapshot")
	}
	m.cacheAttempt = time.Now().Add(-2 * workerObservationTTL)
	stale := s.WorkerObservability(context.Background())
	if !stale.Database.Available || !stale.Database.Stale || stale.Database.ErrorCode != "database_unavailable" || stale.Database.Snapshot.TaskCounts.Accepted != 7 {
		t.Fatal("failed refresh was converted to zero instead of stale data")
	}
	m.cacheAttempt = time.Time{}
	m.refreshMu.Lock()
	busy := s.WorkerObservability(context.Background())
	m.refreshMu.Unlock()
	if !busy.Database.Refreshing || busy.Database.Snapshot == nil {
		t.Fatal("contended refresh blocked or hid the prior snapshot")
	}
}

func TestWorkerDatabaseErrorCodesExcludeDetails(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, tc := range []struct {
		ctx  context.Context
		err  error
		want string
	}{
		{context.Background(), errors.New("secret mysql DSN and SQL"), "database_unavailable"},
		{cancelled, errors.New("driver cancellation"), "snapshot_cancelled"},
		{deadline, errors.New("driver timeout"), "snapshot_timeout"},
		{context.Background(), &mysqlDriver.MySQLError{Number: 3024, Message: "private query"}, "snapshot_timeout"},
	} {
		if got := workerDatabaseErrorCode(tc.ctx, tc.err); got != tc.want {
			t.Fatal("unexpected database error classification", got, tc.want)
		}
	}
}
