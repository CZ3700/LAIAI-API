package video

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"gorm.io/gorm"
)

const workerObservationTTL = 15 * time.Second
const workerObservationTimeout = time.Second

// These are closed dictionaries, never resource identities or provider text.
var workerCallKinds = [...]string{"submit", "query", "cancel", "delete", "delivery_query", "other"}
var workerCallOutcomes = [...]string{"accepted", "rejected", "unknown", "not_sent", "other"}
var workerGuardKinds = [...]string{"create_task", "cancel_task", "delete_task", "other"}
var workerGuardOutcomes = [...]string{"allowed", "denied", "db_error"}
var workerDurationBoundsMS = [...]int64{10, 50, 100, 500, 1000, 5000, 10000, 30000, 120000}

type WorkerCounter struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Outcome string `json:"outcome,omitempty"`
	Value   uint64 `json:"value"`
}

type WorkerDurationBucket struct {
	UpperBoundMS int64  `json:"upper_bound_ms"`
	Count        uint64 `json:"count"`
}

// Buckets are cumulative; Count also includes durations above the final bound.
// These process-local samples are not a cluster percentile or an SLO verdict.
type WorkerDuration struct {
	Kind    string                 `json:"kind"`
	Count   uint64                 `json:"count"`
	SumMS   float64                `json:"sum_ms"`
	MaxMS   float64                `json:"max_ms"`
	Buckets []WorkerDurationBucket `json:"buckets"`
}

type WorkerRuntimeObservation struct {
	Scope                string                          `json:"scope"`
	DispatchPaused       bool                            `json:"dispatch_paused"`
	DispatchPauseReason  string                          `json:"dispatch_pause_reason"`
	StartedAt            time.Time                       `json:"started_at"`
	LastCycleStartedAt   *time.Time                      `json:"last_cycle_started_at"`
	LastCycleCompletedAt *time.Time                      `json:"last_cycle_completed_at"`
	LastCycleErrorAt     *time.Time                      `json:"last_cycle_error_at"`
	Counters             []WorkerCounter                 `json:"counters"`
	Durations            []WorkerDuration                `json:"durations"`
	Cycles               []WorkerCycleObservation        `json:"cycles"`
	RecoveryLanes        []WorkerRecoveryLaneObservation `json:"recovery_lanes"`
}

type WorkerTaskCounts struct {
	Accepted                int64      `json:"accepted"`
	Submitting              int64      `json:"submitting"`
	SubmissionUnknown       int64      `json:"submission_unknown"`
	Queued                  int64      `json:"queued"`
	Running                 int64      `json:"running"`
	TerminalReserved        int64      `json:"terminal_reserved"`
	OldestUnknownSubmission *time.Time `json:"oldest_unknown_submission_at"`
}

type WorkerOperationCount struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type WorkerReceiptCounts struct {
	RetryablePending  int64      `json:"retryable_pending"`
	ConflictPending   int64      `json:"conflict_pending"`
	OldestReceivedAt  *time.Time `json:"oldest_received_at"`
	GenerationPending int64      `json:"generation_pending"`
	SettlementDue     int64      `json:"settlement_due"`
	SettlementFuture  int64      `json:"settlement_future"`
	PhaseReview       int64      `json:"phase_review"`
	FinancialConflict int64      `json:"financial_conflict"`
	InvalidPhase      int64      `json:"invalid_phase"`
}

type WorkerOutboxCounts struct {
	Pending         int64      `json:"pending"`
	Due             int64      `json:"due"`
	OldestCreatedAt *time.Time `json:"oldest_created_at"`
}

// Independent populations: one task may also own an operation and a receipt.
// These counters must never be added into a purported unique-task total.
type WorkerBlockedContractCounts struct {
	Tasks         int64 `json:"tasks"`
	Operations    int64 `json:"operations"`
	QueryReceipts int64 `json:"query_receipts"`
}

type WorkerDatabaseSnapshot struct {
	DatabaseTime     time.Time                   `json:"database_time"`
	StartedAt        time.Time                   `json:"started_at"`
	CompletedAt      time.Time                   `json:"completed_at"`
	TaskCounts       WorkerTaskCounts            `json:"task_counts"`
	OperationCounts  []WorkerOperationCount      `json:"operation_counts"`
	QueryReceipts    WorkerReceiptCounts         `json:"query_receipts"`
	Outbox           WorkerOutboxCounts          `json:"outbox"`
	BlockedContracts WorkerBlockedContractCounts `json:"blocked_contracts"`
}

type WorkerDatabaseObservation struct {
	Source        string                  `json:"source"`
	Consistency   string                  `json:"consistency"`
	Available     bool                    `json:"available"`
	Stale         bool                    `json:"stale"`
	Refreshing    bool                    `json:"refreshing"`
	Complete      bool                    `json:"complete"`
	ErrorCode     string                  `json:"error_code,omitempty"`
	LastAttemptAt *time.Time              `json:"last_attempt_at"`
	Snapshot      *WorkerDatabaseSnapshot `json:"snapshot"`
	NotCollected  []string                `json:"not_collected"`
}

type WorkerDBPoolObservation struct {
	Available          bool  `json:"available"`
	MaxOpenConnections int   `json:"max_open_connections"`
	OpenConnections    int   `json:"open_connections"`
	InUse              int   `json:"in_use"`
	Idle               int   `json:"idle"`
	WaitCount          int64 `json:"wait_count"`
	WaitDurationMS     int64 `json:"wait_duration_ms"`
}

type WorkerObservabilitySnapshot struct {
	SchemaVersion int                       `json:"schema_version"`
	ObservedAt    time.Time                 `json:"observed_at"`
	Runtime       WorkerRuntimeObservation  `json:"runtime"`
	Database      WorkerDatabaseObservation `json:"database"`
	DBPool        WorkerDBPoolObservation   `json:"db_pool"`
}

type workerTiming struct {
	count   uint64
	sum     time.Duration
	max     time.Duration
	buckets [len(workerDurationBoundsMS)]uint64
}

type workerMetrics struct {
	mu                                                                 sync.Mutex
	refreshMu                                                          sync.Mutex
	startedAt                                                          time.Time
	lastStart, lastComplete, lastError                                 time.Time
	cyclesStarted, cyclesCompleted, cycleErrors                        uint64
	cycleKinds                                                         [workerCycleKindCount]workerCycleCounters
	recoveryLanes                                                      [recoveryLaneCount]workerRecoveryCounters
	operationCandidates, queryCandidates, operationErrors, queryErrors uint64
	guards                                                             [len(workerGuardKinds)][len(workerGuardOutcomes)]uint64
	calls                                                              [len(workerCallKinds)][len(workerCallOutcomes)]uint64
	limited                                                            [len(workerCallKinds)]uint64
	timings                                                            [len(workerCallKinds) + 1]workerTiming
	cacheAttempt                                                       time.Time
	cacheError                                                         string
	cacheRefreshing                                                    bool
	cache                                                              *WorkerDatabaseSnapshot
}

// Service is copied by some integration fixtures. Keep mutexes behind a pointer,
// and serialize nil initialization without embedding a copy-sensitive sync.Once.
var workerMetricsInit sync.Mutex

func newWorkerMetrics() *workerMetrics { return &workerMetrics{startedAt: time.Now().UTC()} }

func (s *Service) metrics() *workerMetrics {
	workerMetricsInit.Lock()
	defer workerMetricsInit.Unlock()
	if s.workerMetrics == nil {
		s.workerMetrics = newWorkerMetrics()
	}
	return s.workerMetrics
}

func workerEnumIndex(value string, values []string) int {
	for i, candidate := range values {
		if value == candidate {
			return i
		}
	}
	return len(values) - 1
}

func (m *workerMetrics) startCycle() func(error) {
	return m.startCycleKind(workerCycleCombined)
}

func (t *workerTiming) observe(elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	t.count++
	t.sum += elapsed
	if elapsed > t.max {
		t.max = elapsed
	}
	for i, bound := range workerDurationBoundsMS {
		if elapsed <= time.Duration(bound)*time.Millisecond {
			t.buckets[i]++
		}
	}
}

func (s *Service) observeGuard(kind string, allowed bool, err error) {
	m := s.metrics()
	k := workerEnumIndex(kind, workerGuardKinds[:])
	o := 1
	if err != nil && !errors.Is(err, ErrDispatchPaused) {
		o = 2
	} else if allowed {
		o = 0
	}
	m.mu.Lock()
	m.guards[k][o]++
	m.mu.Unlock()
}

func (s *Service) observeAdapterCall(kind string, result ark.Result, started time.Time) {
	m := s.metrics()
	k := workerEnumIndex(kind, workerCallKinds[:])
	o := workerEnumIndex(string(result.Outcome), workerCallOutcomes[:])
	elapsed := time.Since(started)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls[k][o]++
	if result.HTTPStatus == 429 {
		m.limited[k]++
	}
	m.timings[k].observe(elapsed)
}

func timePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	value := t.UTC()
	return &value
}

func (m *workerMetrics) runtimeSnapshot() WorkerRuntimeObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := WorkerRuntimeObservation{Scope: "process_since_start", StartedAt: m.startedAt, LastCycleStartedAt: timePointer(m.lastStart), LastCycleCompletedAt: timePointer(m.lastComplete), LastCycleErrorAt: timePointer(m.lastError), Counters: []WorkerCounter{
		{Name: "cycles_started", Kind: "cycle", Value: m.cyclesStarted},
		{Name: "cycles_completed", Kind: "cycle", Value: m.cyclesCompleted},
		{Name: "cycle_errors", Kind: "cycle", Value: m.cycleErrors},
		{Name: "candidates", Kind: "operation", Value: m.operationCandidates},
		{Name: "candidates", Kind: "query", Value: m.queryCandidates},
		{Name: "item_errors", Kind: "operation", Value: m.operationErrors},
		{Name: "item_errors", Kind: "query", Value: m.queryErrors},
	}}
	for k, kind := range workerCycleNames {
		counter := m.cycleKinds[k]
		out.Cycles = append(out.Cycles, WorkerCycleObservation{Kind: kind, Started: counter.started, Completed: counter.completed, Errors: counter.errors})
	}
	for k, kind := range recoveryLaneNames {
		counter := m.recoveryLanes[k]
		result := counter.result
		out.RecoveryLanes = append(out.RecoveryLanes, WorkerRecoveryLaneObservation{Kind: kind, Cycles: counter.cycles, Errors: counter.errors, Selected: result.Selected, Applied: result.Applied, Deferred: result.Deferred, Review: result.Review, Noop: result.Noop, Failed: result.Failed})
	}
	for k, kind := range workerGuardKinds {
		for o, outcome := range workerGuardOutcomes {
			out.Counters = append(out.Counters, WorkerCounter{Name: "dispatch_guard", Kind: kind, Outcome: outcome, Value: m.guards[k][o]})
		}
	}
	for k, kind := range workerCallKinds {
		for o, outcome := range workerCallOutcomes {
			out.Counters = append(out.Counters, WorkerCounter{Name: "adapter_calls", Kind: kind, Outcome: outcome, Value: m.calls[k][o]})
		}
		out.Counters = append(out.Counters, WorkerCounter{Name: "adapter_http_429", Kind: kind, Value: m.limited[k]})
	}
	for i, timing := range m.timings {
		kind := "cycle"
		if i < len(workerCallKinds) {
			kind = workerCallKinds[i]
		}
		d := WorkerDuration{Kind: kind, Count: timing.count, SumMS: float64(timing.sum) / float64(time.Millisecond), MaxMS: float64(timing.max) / float64(time.Millisecond), Buckets: make([]WorkerDurationBucket, len(workerDurationBoundsMS))}
		for b, bound := range workerDurationBoundsMS {
			d.Buckets[b] = WorkerDurationBucket{UpperBoundMS: bound, Count: timing.buckets[b]}
		}
		out.Durations = append(out.Durations, d)
	}
	return out
}

// WorkerObservability is read-only. It neither calls an adapter nor decrypts
// evidence, claims work, updates scheduling or participates in accounting.
func (s *Service) WorkerObservability(ctx context.Context) WorkerObservabilitySnapshot {
	m := s.metrics()
	out := WorkerObservabilitySnapshot{SchemaVersion: 1, ObservedAt: time.Now().UTC(), Runtime: m.runtimeSnapshot(), Database: s.databaseObservation(ctx, m)}
	out.Runtime.DispatchPaused = s.DispatchPaused()
	if out.Runtime.DispatchPaused {
		out.Runtime.DispatchPauseReason = "response_persistence_failed"
	}
	if s.DB != nil {
		if db, err := s.DB.DB(); err == nil {
			stats := db.Stats()
			out.DBPool = WorkerDBPoolObservation{Available: true, MaxOpenConnections: stats.MaxOpenConnections, OpenConnections: stats.OpenConnections, InUse: stats.InUse, Idle: stats.Idle, WaitCount: stats.WaitCount, WaitDurationMS: stats.WaitDuration.Milliseconds()}
		}
	}
	return out
}

func (m *workerMetrics) databaseView(now time.Time) WorkerDatabaseObservation {
	out := WorkerDatabaseObservation{Source: "primary_mysql", Consistency: "short_read_only_repeatable_read", Available: m.cache != nil, Stale: m.cache != nil && (m.cacheError != "" || now.Sub(m.cache.CompletedAt) >= workerObservationTTL), Refreshing: m.cacheRefreshing, Complete: m.cache != nil, ErrorCode: m.cacheError, LastAttemptAt: timePointer(m.cacheAttempt), NotCollected: []string{"pending_usage", "task_query_due", "query_leases", "provider_scope_capacity"}}
	if m.cache != nil {
		copy := *m.cache
		copy.OperationCounts = append([]WorkerOperationCount(nil), copy.OperationCounts...)
		// Copy pointer-valued aggregates as well: callers cannot mutate the cache.
		if copy.TaskCounts.OldestUnknownSubmission != nil {
			copy.TaskCounts.OldestUnknownSubmission = timePointer(*copy.TaskCounts.OldestUnknownSubmission)
		}
		if copy.QueryReceipts.OldestReceivedAt != nil {
			copy.QueryReceipts.OldestReceivedAt = timePointer(*copy.QueryReceipts.OldestReceivedAt)
		}
		if copy.Outbox.OldestCreatedAt != nil {
			copy.Outbox.OldestCreatedAt = timePointer(*copy.Outbox.OldestCreatedAt)
		}
		out.Snapshot = &copy
	}
	if out.Refreshing && out.Snapshot == nil && out.ErrorCode == "" {
		out.ErrorCode = "refresh_in_progress"
	}
	return out
}

func (s *Service) databaseObservation(ctx context.Context, m *workerMetrics) WorkerDatabaseObservation {
	now := time.Now()
	m.mu.Lock()
	fresh := !m.cacheAttempt.IsZero() && now.Sub(m.cacheAttempt) < workerObservationTTL
	view := m.databaseView(now)
	m.mu.Unlock()
	if fresh {
		return view
	}
	if !m.refreshMu.TryLock() {
		m.mu.Lock()
		view = m.databaseView(time.Now())
		m.mu.Unlock()
		view.Refreshing = true
		if view.Snapshot == nil && view.ErrorCode == "" {
			view.ErrorCode = "refresh_in_progress"
		}
		return view
	}
	defer m.refreshMu.Unlock()
	m.mu.Lock()
	// Another completed refresher may have won between the first check and lock.
	if !m.cacheAttempt.IsZero() && time.Since(m.cacheAttempt) < workerObservationTTL {
		view = m.databaseView(time.Now())
		m.mu.Unlock()
		return view
	}
	m.cacheAttempt, m.cacheRefreshing = time.Now(), true
	m.mu.Unlock()
	deadline, cancel := context.WithTimeout(ctx, workerObservationTimeout)
	defer cancel()
	snapshot, code := s.collectWorkerDatabase(deadline)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cacheRefreshing = false
	m.cacheError = code
	if snapshot != nil {
		m.cache = snapshot
	}
	return m.databaseView(time.Now())
}

// Five fixed aggregations. The hints cap individual SELECT execution, while
// the context also covers pool waits, transaction start and the whole refresh.
// tasks.task_id retains its historical unicode index collation. Lookups
// into that index use its collation, then require exact bytes and org;
// state lookups keep the existing binary index comparison.
// Only pending terminal financial work is added to the original active task
// population. Raw JSON, credentials and resource IDs are never selected.
const workerTaskSQL = `SELECT /*+ MAX_EXECUTION_TIME(200) */ UTC_TIMESTAMP(3) AS database_time,
 COUNT(CASE WHEN status='accepted' THEN 1 END) AS accepted,
 COUNT(CASE WHEN status='submitting' THEN 1 END) AS submitting,
 COUNT(CASE WHEN status='submission_unknown' THEN 1 END) AS submission_unknown,
 COUNT(CASE WHEN status='queued' THEN 1 END) AS queued,
 COUNT(CASE WHEN status='running' THEN 1 END) AS running,
 COUNT(CASE WHEN status IN ('succeeded','failed','cancelled','expired') THEN 1 END) AS terminal_reserved,
 MIN(CASE WHEN status='submission_unknown' THEN submitted_at END) AS oldest_unknown_submission
 FROM (
 SELECT t.status,t.submitted_at FROM tasks t JOIN video_task_states v ON v.task_id=t.task_id AND CAST(v.task_id AS BINARY)=CAST(t.task_id AS BINARY) AND v.org_id=t.org_id
 WHERE t.provider='ark_official' AND t.status IN ('accepted','submitting','submission_unknown','queued','running') AND v.deleted_at IS NULL
 UNION ALL
 SELECT t.status,t.submitted_at FROM video_task_states v JOIN tasks t ON t.task_id=v.task_id COLLATE utf8mb4_unicode_ci AND CAST(t.task_id AS BINARY)=CAST(v.task_id AS BINARY) AND t.org_id=v.org_id
 WHERE v.billing_state='reserved' AND t.provider='ark_official' AND t.status IN ('succeeded','failed','cancelled','expired')
 ) populations`

const workerOperationSQL = `SELECT /*+ MAX_EXECUTION_TIME(200) */
 CASE o.kind WHEN 'create_task' THEN 'create_task' WHEN 'cancel_task' THEN 'cancel_task' WHEN 'delete_task' THEN 'delete_task' ELSE 'other' END AS kind,
 o.status, COUNT(*) AS count
 FROM video_operations o JOIN tasks t ON t.task_id=o.resource_id COLLATE utf8mb4_unicode_ci AND CAST(t.task_id AS BINARY)=CAST(o.resource_id AS BINARY) AND t.org_id=o.org_id
 WHERE t.provider='ark_official' AND o.status IN ('pending','in_flight','unknown','needs_review')
 GROUP BY CASE o.kind WHEN 'create_task' THEN 'create_task' WHEN 'cancel_task' THEN 'cancel_task' WHEN 'delete_task' THEN 'delete_task' ELSE 'other' END,o.status ORDER BY kind,o.status`

const workerReceiptV2 = `(r.runtime_version='ark-official-generation-settlement-v2' AND r.adapter_version='ark-v3-v1' AND r.protocol_contract_version='video-contract-v1' AND r.observation_schema_version=1
 AND v.runtime_version=r.runtime_version AND v.adapter_version=r.adapter_version AND v.protocol_contract_version=r.protocol_contract_version AND v.billing_schema_version='usd-decimal-v1')`

const workerReceiptGeneration = `(r.generation_applied_at IS NULL AND r.settlement_kind='' AND r.settlement_evidence_sha256='')`
const workerReceiptSettlement = `(r.generation_applied_at >= CAST('1000-01-01 00:00:00' AS DATETIME) AND r.settlement_kind IN ('usage','provider_nocharge')
 AND v.settlement_receipt_id=r.attempt_id AND OCTET_LENGTH(v.settlement_receipt_id)=OCTET_LENGTH(r.attempt_id))`

const workerReceiptSQL = `SELECT /*+ MAX_EXECUTION_TIME(200) */ pending.*,
 (SELECT COUNT(*) FROM video_query_receipts closed WHERE closed.apply_error_code='query_financial_conflict') AS financial_conflict
 FROM (SELECT
 COUNT(CASE WHEN r.applied_at IS NULL AND r.apply_error_code NOT IN ('settlement_conflict','query_financial_conflict') THEN 1 END) AS retryable_pending,
 COUNT(CASE WHEN r.applied_at IS NULL AND r.apply_error_code IN ('settlement_conflict','query_financial_conflict') THEN 1 END) AS conflict_pending,
 MIN(CASE WHEN r.applied_at IS NULL THEN r.received_at END) AS oldest_received_at,
 COUNT(CASE WHEN r.applied_at IS NULL AND ` + workerReceiptV2 + ` AND r.recovery_due_at IS NOT NULL AND ` + workerReceiptGeneration + ` AND ` + noTaskSettlementConflict + ` THEN 1 END) AS generation_pending,
 COUNT(CASE WHEN r.applied_at IS NULL AND ` + workerReceiptV2 + ` AND ` + workerReceiptSettlement + ` AND r.recovery_due_at <= UTC_TIMESTAMP(3) AND ` + noTaskSettlementConflict + ` THEN 1 END) AS settlement_due,
 COUNT(CASE WHEN r.applied_at IS NULL AND ` + workerReceiptV2 + ` AND ` + workerReceiptSettlement + ` AND r.recovery_due_at > UTC_TIMESTAMP(3) AND ` + noTaskSettlementConflict + ` THEN 1 END) AS settlement_future,
 COUNT(CASE WHEN r.applied_at IS NULL AND ` + workerReceiptV2 + ` AND r.recovery_due_at IS NULL THEN 1 END) AS phase_review,
 COUNT(CASE WHEN r.applied_at IS NULL AND ` + workerReceiptV2 + ` AND r.recovery_due_at IS NOT NULL AND NOT (` + workerReceiptGeneration + ` OR COALESCE(` + workerReceiptSettlement + `,FALSE)) THEN 1 END) AS invalid_phase
 FROM video_query_receipts r LEFT JOIN video_task_states v ON v.org_id=r.org_id AND v.task_id=r.task_id
 WHERE r.applied_at IS NULL) pending`

const workerOutboxSQL = `SELECT /*+ MAX_EXECUTION_TIME(200) */ COUNT(*) AS pending,
 COUNT(CASE WHEN next_attempt_at <= ? THEN 1 END) AS due,
 MIN(created_at) AS oldest_created_at FROM video_outbox WHERE status='pending'`

// This is one SELECT with three independent counts, not three round trips.
// Positive support scopes are shared with dispatch/recovery; NOT EXISTS also
// counts incomplete identity joins rather than treating missing data as v1.
func workerBlockedContractsQuery(tx *gorm.DB) *gorm.DB {
	fresh := func() *gorm.DB { return tx.Session(&gorm.Session{NewDB: true}) }
	active := []string{"accepted", "submitting", "submission_unknown", "queued", "running"}
	terminal := []string{"succeeded", "failed", "cancelled", "expired"}
	activeTasks := fresh().Table("tasks AS t").Select("t.org_id, t.task_id").
		Joins("LEFT JOIN video_task_states c ON c.org_id = t.org_id AND c.task_id = t.task_id AND CAST(c.task_id AS BINARY) = CAST(t.task_id AS BINARY)").
		Where("t.provider = ? AND t.status IN ? AND c.deleted_at IS NULL", Provider, active)
	// Preserve the original pending_usage population. Reserved financial work
	// joins it only after a known generation terminal, matching workerTaskSQL.
	// Neither deletion nor an unknown status establishes a generation terminal.
	pendingTasks := fresh().Table("video_task_states AS c").Select("c.org_id, c.task_id").
		Joins("JOIN tasks t ON t.org_id = c.org_id AND t.task_id = c.task_id COLLATE utf8mb4_unicode_ci AND CAST(t.task_id AS BINARY) = CAST(c.task_id AS BINARY)").
		Where("t.provider = ? AND ((c.billing_state = ? AND (c.deleted_at IS NOT NULL OR t.status NOT IN ?)) OR (c.billing_state = ? AND t.status IN ?))", Provider, "pending_usage", active, "reserved", terminal)
	taskCandidates := fresh().Raw("? UNION ALL ?", activeTasks, pendingTasks)
	supportedTask := supportedTaskRuntime(fresh().Table("video_task_states AS v").Select("1")).
		Where("v.org_id = candidate.org_id AND v.task_id = candidate.task_id AND CAST(v.task_id AS BINARY) = CAST(candidate.task_id AS BINARY)")
	tasks := fresh().Table("(?) AS candidate", taskCandidates).Select("COUNT(*)").Where("NOT EXISTS (?)", supportedTask)
	supportedOperation := supportedOperationRuntime(fresh().Table("video_operations AS o").Select("1").
		Joins("JOIN video_task_states v ON v.org_id = o.org_id AND v.task_id = o.resource_id")).
		Where("o.org_id = candidate.org_id AND o.id = candidate.id")
	operations := fresh().Table("video_operations AS candidate").Select("COUNT(*)").
		Joins("JOIN tasks t ON t.org_id = candidate.org_id AND t.task_id = candidate.resource_id COLLATE utf8mb4_unicode_ci AND CAST(t.task_id AS BINARY) = CAST(candidate.resource_id AS BINARY)").
		Where("t.provider = ? AND candidate.status IN ?", Provider, []string{"pending", "in_flight", "unknown"}).
		Where("NOT EXISTS (?)", supportedOperation)
	supportedReceipt := supportedQueryReceiptRuntime(fresh().Table("video_query_receipts AS r").Select("1").
		Joins("JOIN video_task_states v ON v.org_id = r.org_id AND v.task_id = r.task_id")).
		Where("r.org_id = candidate.org_id AND r.task_id = candidate.task_id AND r.attempt_id = candidate.attempt_id")
	receipts := fresh().Table("video_query_receipts AS candidate").Select("COUNT(*)").
		Joins("JOIN tasks t ON t.org_id = candidate.org_id AND t.task_id = candidate.task_id COLLATE utf8mb4_unicode_ci AND CAST(t.task_id AS BINARY) = CAST(candidate.task_id AS BINARY)").
		Where("t.provider = ? AND candidate.applied_at IS NULL", Provider).
		Where("NOT EXISTS (?)", supportedReceipt)
	return tx.Raw("SELECT /*+ MAX_EXECUTION_TIME(200) */ /* blocked_contracts */ (?) AS tasks, (?) AS operations, (?) AS query_receipts", tasks, operations, receipts)
}

func (s *Service) collectWorkerDatabase(ctx context.Context) (*WorkerDatabaseSnapshot, string) {
	if s.DB == nil {
		return nil, "database_unavailable"
	}
	if s.DB.Dialector.Name() != "mysql" {
		return nil, "unsupported_database"
	}
	out := &WorkerDatabaseSnapshot{StartedAt: time.Now().UTC(), OperationCounts: []WorkerOperationCount{}}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tasks struct {
			DatabaseTime     time.Time
			WorkerTaskCounts `gorm:"embedded"`
		}
		if err := tx.Raw(workerTaskSQL).Scan(&tasks).Error; err != nil {
			return err
		}
		out.DatabaseTime, out.TaskCounts = tasks.DatabaseTime.UTC(), tasks.WorkerTaskCounts
		if err := tx.Raw(workerOperationSQL).Scan(&out.OperationCounts).Error; err != nil {
			return err
		}
		if err := tx.Raw(workerReceiptSQL).Scan(&out.QueryReceipts).Error; err != nil {
			return err
		}
		if err := tx.Raw(workerOutboxSQL, out.DatabaseTime).Scan(&out.Outbox).Error; err != nil {
			return err
		}
		return workerBlockedContractsQuery(tx).Scan(&out.BlockedContracts).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, workerDatabaseErrorCode(ctx, err)
	}
	out.CompletedAt = time.Now().UTC()
	return out, ""
}

func workerDatabaseErrorCode(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "snapshot_timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "snapshot_cancelled"
	}
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) && (mysqlErr.Number == 3024 || mysqlErr.Number == 1969) {
		return "snapshot_timeout"
	}
	return "database_unavailable"
}
