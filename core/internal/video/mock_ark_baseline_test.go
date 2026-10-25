package video

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const mockArkBaselineCount = 10

type mockBaselineTask struct {
	ID           string     `json:"task_id"`
	UpstreamID   string     `json:"upstream_task_id,omitempty"`
	AcceptedAt   time.Time  `json:"api_accepted_at"`
	AdmissionMS  float64    `json:"admission_ms"`
	VisibleAt    *time.Time `json:"api_terminal_visible_at,omitempty"`
	SyncMS       *float64   `json:"terminal_sync_ms,omitempty"`
	LastStatus   string     `json:"last_status"`
	BillingState string     `json:"billing_state"`
	HoldUSD      string     `json:"hold_usd,omitempty"`
	CapturedUSD  string     `json:"captured_usd,omitempty"`
	ReleasedUSD  string     `json:"released_usd,omitempty"`
}

// Separate from ordinary TestMySQL runs: ProcessOnce scans the entire DB and
// mysqlFixture updates shared model configuration. The runner owns a fresh
// MySQL container, and the preflight below refuses any prior task data.
func TestMySQLMockArkWorkerBaseline(t *testing.T) {
	if os.Getenv("ARK_MOCK_BASELINE") != "1" {
		t.Skip("explicit ARK_MOCK_BASELINE=1 and isolated runner required")
	}
	path := os.Getenv("ARK_MOCK_BASELINE_REPORT")
	if path == "" || !filepath.IsAbs(path) {
		t.Fatal("an absolute ARK_MOCK_BASELINE_REPORT is required")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("baseline report destination must not already exist")
	}
	started := time.Now()
	report := map[string]any{
		"schema_version": 1, "passed": false, "status": "running",
		"commit": os.Getenv("ARK_MOCK_BASELINE_COMMIT"), "go_version": runtime.Version(),
		"started_at": started.UTC(), "task_count": mockArkBaselineCount,
		"scope_count": 1, "worker_count": 1, "worker_tick_ms": 2000,
		"provider_gate_per_scope_action_ms": 1000, "generation_ms": 30000,
		"query_delay_ms": 50, "client_observation_interval_ms": 500,
		"provider_transport": "in_process_mock_no_network", "api_transport": "gin_http_handlers_in_process",
		"official_calls": 0, "paid_calls": 0,
		"capacity_claim": "10-task single-scope short baseline; not 500-task capacity evidence",
	}
	fixtureHash := sha256.Sum256([]byte("mock-ark-baseline-v1/tasks=10/scopes=1/tick=2s/generation=30s/query=50ms/observe=500ms/tokens=100/retail=8/supplier=46"))
	report["fixture_sha256"] = fmt.Sprintf("%x", fixtureHash)
	var provider *mockArkProvider
	tasks := make([]mockBaselineTask, 0, mockArkBaselineCount)
	var workerMu sync.Mutex
	workerDurations := []float64{}
	t.Cleanup(func() {
		report["finished_at"] = time.Now().UTC()
		report["elapsed_ms"] = float64(time.Since(started).Microseconds()) / 1000
		report["passed"] = !t.Failed() && report["status"] == "passed"
		if t.Failed() {
			report["status"] = "failed"
		}
		report["tasks"] = tasks
		workerMu.Lock()
		report["process_once_durations_ms"] = append([]float64(nil), workerDurations...)
		workerMu.Unlock()
		if provider != nil {
			states, requests, rejected := provider.snapshot()
			report["mock_transitions"] = states
			report["provider_requests"] = requests
			report["unexpected_provider_requests"] = rejected
			report["provider_summary"] = mockBaselineRequestStats(requests, started)
		}
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error("could not encode baseline report")
			return
		}
		// O_EXCL prevents reruns from overwriting prior evidence.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Error("could not create baseline report")
			return
		}
		if _, err = file.Write(append(body, '\n')); err != nil {
			t.Error("could not write baseline report")
		}
		if err = file.Close(); err != nil {
			t.Error("could not close baseline report")
		}
	})
	report["mysql_version"] = mockBaselineEmptyDatabase(t)
	f := mysqlFixture(t) // Exactly one fixture, one scope, ten tasks.
	var schemaVersion string
	must(t, f.s.DB.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&schemaVersion).Error)
	report["schema_version_max"] = schemaVersion
	f.s.Now = time.Now
	provider = newMockArkProvider(30*time.Second, 50*time.Millisecond)
	f.s.Client = &http.Client{Transport: provider, Timeout: 5 * time.Second}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/v1/video/generations", f.s.Middleware(), f.s.HandleCreate)
	router.GET("/v1/video/generations/:id", f.s.Middleware(), f.s.HandleGet)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	call := func(method, path, idem string) TaskView {
		t.Helper()
		body := ""
		if method == http.MethodPost {
			body = `{"model":"seedance-2.0","content":[{"type":"text","text":"Synthetic mock baseline"}]}`
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+f.plain)
		req.Header.Set("Content-Type", "application/json")
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		want := http.StatusOK
		if method == http.MethodPost {
			want = http.StatusAccepted
		}
		if w.Code != want {
			t.Fatalf("baseline API %s returned %d, expected %d (body intentionally omitted)", method, w.Code, want)
		}
		var view TaskView
		must(t, json.Unmarshal(w.Body.Bytes(), &view))
		if view.ID == "" {
			t.Fatal("baseline API returned no task identity")
		}
		return view
	}
	for i := 0; i < mockArkBaselineCount; i++ {
		before := time.Now()
		view := call(http.MethodPost, "/v1/video/generations", fmt.Sprintf("baseline-%02d", i))
		tasks = append(tasks, mockBaselineTask{ID: view.ID, AcceptedAt: time.Now(), AdmissionMS: float64(time.Since(before).Microseconds()) / 1000, LastStatus: view.Status, BillingState: view.BillingState})
	}
	if replay := call(http.MethodPost, "/v1/video/generations", "baseline-00"); replay.ID != tasks[0].ID {
		t.Fatal("idempotency replay admitted a second task")
	}
	var scopes int64
	must(t, f.s.DB.Model(&State{}).Distinct("provider_scope_id").Count(&scopes).Error)
	if scopes != 1 {
		t.Fatal("baseline must not mask the single-scope gate with extra scopes")
	}
	report["verified_scope_count"] = scopes
	report["peak_accepted_inflight"] = len(tasks)
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	workerErrors := make(chan error, 1)
	go func() {
		defer close(workerDone)
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-tick.C:
				before := time.Now()
				err := f.s.ProcessOnce(workerCtx)
				workerMu.Lock()
				workerDurations = append(workerDurations, float64(time.Since(before).Microseconds())/1000)
				workerMu.Unlock()
				if err != nil && workerCtx.Err() == nil {
					workerErrors <- err
					return
				}
			}
		}
	}()
	// Registered after fixture cleanup so no worker can use a closed DB.
	t.Cleanup(func() { stopWorker(); <-workerDone })
	observe := time.NewTicker(500 * time.Millisecond)
	defer observe.Stop()
	complete := 0
	for complete < len(tasks) {
		select {
		case <-ctx.Done():
			t.Fatalf("baseline deadline exceeded: %d/%d terminal", complete, len(tasks))
		case <-workerErrors:
			t.Fatal("ProcessOnce failed (inspect isolated DB/test evidence)")
		case <-observe.C:
			for i := range tasks {
				item := &tasks[i]
				if item.VisibleAt != nil {
					continue
				}
				view := call(http.MethodGet, "/v1/video/generations/"+item.ID, "")
				item.LastStatus, item.BillingState = view.Status, view.BillingState
				if view.Status == "succeeded" && view.BillingState == "settled" {
					now := time.Now()
					item.VisibleAt = &now
					complete++
				}
			}
		}
	}
	// Let the real scheduler repeat after completion, without direct invocation
	// of processOperation/pollTask or manually editing its rate/poll timestamps.
	if err := mockArkWait(ctx, 4500*time.Millisecond); err != nil {
		t.Fatal("baseline timed out before replay verification")
	}
	stopWorker()
	<-workerDone
	select {
	case <-workerErrors:
		t.Fatal("ProcessOnce failed during final replay verification")
	default:
	}
	states, requests, rejected := provider.snapshot()
	if rejected != 0 || len(states) != len(tasks) {
		t.Fatal("unexpected provider operation or duplicate creation")
	}
	postCount := 0
	for _, request := range requests {
		if request.Method == http.MethodPost {
			postCount++
		}
	}
	if postCount != mockArkBaselineCount {
		t.Fatal("provider POST count must equal logical task count")
	}
	syncMS := []float64{}
	captured, reserved, released := decimal.Zero, decimal.Zero, decimal.Zero
	for i := range tasks {
		item := &tasks[i]
		var stored task.Task
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, item.ID).First(&stored).Error)
		item.UpstreamID = stored.UpstreamTaskID
		mock, ok := states[item.UpstreamID]
		if !ok || item.VisibleAt == nil || item.VisibleAt.Before(mock.SucceededAt) {
			t.Fatal("terminal observation lacks a valid mock transition")
		}
		lag := float64(item.VisibleAt.Sub(mock.SucceededAt).Microseconds()) / 1000
		item.SyncMS = &lag
		syncMS = append(syncMS, lag)
		hold := f.hold(t, item.ID)
		var entries []videobilling.BillingEntry
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, item.ID).Find(&entries).Error)
		if len(entries) != 1 || entries[0].SettlementKey != "initial" || entries[0].State != "settled" {
			t.Fatal("each task must have exactly one initial settlement")
		}
		entry := entries[0]
		want := decimal.RequireFromString("0.0008") // 100 synthetic tokens × $8 / million.
		if hold.Status != ledger.HoldCaptured || !hold.CapturedAmount.Equal(want) || !entry.CapturedUSD.Equal(want) || !hold.Amount.Equal(entry.CapturedUSD.Add(entry.ReleasedUSD)) {
			t.Fatal("hold/capture/release invariant failed")
		}
		item.HoldUSD, item.CapturedUSD, item.ReleasedUSD = hold.Amount.StringFixed(8), entry.CapturedUSD.StringFixed(8), entry.ReleasedUSD.StringFixed(8)
		reserved, captured, released = reserved.Add(hold.Amount), captured.Add(entry.CapturedUSD), released.Add(entry.ReleasedUSD)
	}
	posted, held, err := ledger.OrgBalances(f.s.DB, f.key.OrgID, ledger.AccountUserCredit)
	must(t, err)
	if !held.IsZero() || !posted.Equal(decimal.NewFromInt(100).Sub(captured)) || !reserved.Equal(captured.Add(released)) {
		t.Fatal("aggregate wallet/reservation invariant failed")
	}
	var key identity.APIKey
	must(t, f.s.DB.First(&key, f.key.ID).Error)
	if !key.UsedAmount.Equal(captured) {
		t.Fatal("API Key usage differs from settled capture")
	}
	drift, err := ledger.CheckDrift(f.s.DB)
	must(t, err)
	if len(drift) != 0 {
		t.Fatal("ledger drift detected")
	}
	var quota []videobilling.QuotaReservation
	must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Find(&quota).Error)
	if len(quota) != mockArkBaselineCount {
		t.Fatal("missing shared supplier quota reservations")
	}
	for _, q := range quota {
		if q.State != "consumed" || !q.ActualAmount.Equal(decimal.RequireFromString("0.0046")) {
			t.Fatal("supplier quota reservation differs from synthetic actual cost")
		}
	}
	var buckets []videobilling.QuotaBucket
	must(t, f.s.DB.Find(&buckets).Error)
	if len(buckets) != 1 || !buckets[0].ReservedAmount.IsZero() || !buckets[0].ConsumedAmount.Equal(decimal.RequireFromString("0.046")) {
		t.Fatal("single-scope supplier quota bucket invariant failed")
	}
	for _, check := range []struct {
		model any
		where string
		want  int64
	}{
		{&invocation.Invocation{}, "org_id = ?", mockArkBaselineCount},
		{&Attempt{}, "org_id = ? AND phase = 'delivery_query_sending'", 0},
		{&ledger.JournalEntry{}, "account_id IN (SELECT id FROM ledger_accounts WHERE org_id = ?) AND kind = 'capture'", mockArkBaselineCount},
	} {
		var count int64
		must(t, f.s.DB.Model(check.model).Where(check.where, f.key.OrgID).Count(&count).Error)
		if count != check.want {
			t.Fatal("duplicate/missing settlement, invocation, or unexpected delivery refresh")
		}
	}
	report["terminal_sync_ms"] = mockBaselinePercentiles(syncMS)
	report["accounting"] = map[string]any{"hold_usd": reserved.StringFixed(8), "captured_usd": captured.StringFixed(8), "released_usd": released.StringFixed(8), "wallet_posted_usd": posted.StringFixed(8), "wallet_held_usd": held.StringFixed(8), "key_used_usd": key.UsedAmount.StringFixed(8), "supplier_consumed_cny": buckets[0].ConsumedAmount.StringFixed(8), "ledger_drift_count": len(drift), "initial_settlements": len(tasks), "capture_journals": len(tasks), "delivery_refresh_queries": 0, "balanced": true}
	report["status"] = "passed"
}

func mockBaselineEmptyDatabase(t *testing.T) string {
	t.Helper()
	parsed, err := drivermysql.ParseDSN(os.Getenv("ARK_TEST_MYSQL_DSN"))
	if err != nil || parsed.DBName != "ark_test" || parsed.Net != "tcp" {
		t.Fatal("baseline requires an isolated ark_test TCP database")
	}
	host, _, err := net.SplitHostPort(parsed.Addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("baseline database must use a loopback address")
	}
	db, err := gorm.Open(gormmysql.Open(parsed.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot connect to isolated baseline database")
	}
	sqlDB, err := db.DB()
	must(t, err)
	defer sqlDB.Close()
	for _, table := range []string{"tasks", "video_task_states", "video_operations", "video_query_receipts", "video_attempts", "video_events", "video_outbox", "video_billing_snapshots", "video_billing_entries", "video_quota_reservations", "video_quota_buckets", "video_provider_rates", "video_upstream_bindings", "video_credential_snapshots", "video_idempotency_records", "api_request_logs"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil {
			t.Fatal("baseline schema is incomplete; run formal migrations")
		}
		if count != 0 {
			t.Fatalf("baseline refuses existing data in %s; use a new isolated database", table)
		}
	}
	var version string
	must(t, db.Raw("SELECT VERSION()").Scan(&version).Error)
	return version
}

func mockBaselinePercentiles(values []float64) map[string]any {
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	if len(v) == 0 {
		return map[string]any{"samples": 0}
	}
	return map[string]any{"samples": len(v), "p50": v[(len(v)*50+99)/100-1], "p95": v[(len(v)*95+99)/100-1], "max": v[len(v)-1], "method": "nearest_rank"}
}

func mockBaselineRequestStats(requests []mockArkRequest, start time.Time) map[string]any {
	counts := map[string]int{}
	buckets := map[string]int{}
	last := map[string]time.Time{}
	intervals := map[string][]float64{}
	for _, request := range requests {
		counts[request.Method]++
		bucket := fmt.Sprintf("%s/%d", request.Method, int(request.Started.Sub(start).Seconds()))
		buckets[bucket]++
		if previous, ok := last[request.Method]; ok {
			intervals[request.Method] = append(intervals[request.Method], float64(request.Started.Sub(previous).Microseconds())/1000)
		}
		last[request.Method] = request.Started
	}
	return map[string]any{"counts": counts, "one_second_buckets": buckets, "interarrival_ms": intervals}
}
