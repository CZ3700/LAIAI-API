package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/config"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// S08 exercises the job registration called by runCore, the actual public
// Create/Cancel/dispatch/query paths, and shutdownCore under real SIGTERM.
// This is not the complete runCore server or a production provider connection.
func newPersistedRecoveryHostFixture(t *testing.T) (mutationShutdownFixture, video.QueryReceipt) {
	t.Helper()
	db, dsn, schema := mutationShutdownPrivateDB(t)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal("mutation shutdown fixture initialization failed", err)
		}
	}
	tracked, trackErr := ledger.EnableActivityTracking(db)
	check(trackErr)
	db = tracked
	kr, err := crypto.NewKeyring(mutationShutdownKeySpec, "test")
	check(err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	service, err := video.NewService(db, kr, []byte(strings.Repeat("h", 32)), slog.New(slog.NewTextHandler(io.Discard, nil)))
	check(err)
	check(service.ConfigureResponseInbox(dsn, false))
	t.Cleanup(func() {
		if err := service.CloseResponseInbox(); err != nil {
			t.Error("fixture response pool did not close")
		}
	})
	service.Now = func() time.Time { return now }
	service.Gate = mutationShutdownGate{}
	org := identity.Organization{Slug: schema, Name: "Synthetic shutdown", Group: "default", Status: 1}
	check(db.Create(&org).Error)
	var key *identity.APIKey
	check(db.Transaction(func(tx *gorm.DB) error {
		_, issued, err := identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, Name: "shutdown", Scopes: "video:*"})
		key = issued
		return err
	}))
	check(db.Transaction(func(tx *gorm.DB) error {
		account, err := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		_, _, err = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "shutdown-credit", AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100)})
		return err
	}))
	ciphertext, err := kr.Encrypt("synthetic-shutdown-credential")
	check(err)
	channel := relay.Channel{Name: "Synthetic shutdown", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", CredentialEnc: ciphertext, Models: ark.Model20, Weight: 1, Status: 1, MaxConcurrentTasks: 100}
	check(db.Create(&channel).Error)
	scope := video.ProviderScope{ScopeKey: schema, AccountRef: "synthetic-shutdown", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "fixture", FXObservedAt: now}
	check(db.Create(&scope).Error)
	limit := decimal.NewFromInt(1000000)
	check(db.Create(&videobilling.QuotaPolicy{Scope: videobilling.ScopeProvider, OwnerID: scope.ID, Metric: videobilling.MetricCNY, Period: videobilling.PeriodLifetime, Timezone: "Asia/Shanghai", LimitAmount: &limit, Version: "fixture", Enabled: true}).Error)
	capability := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "fixture", Enabled: true, Verified: true, Resolutions: []string{"720p"}, Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: 15, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4}
	capJSON, err := json.Marshal(capability)
	check(err)
	check(db.Create(&video.ModelConfig{PublicModel: ark.Model20, Version: "fixture", ProviderScopeID: scope.ID, ChannelID: channel.ID, Enabled: true, CapabilityJSON: string(capJSON), PricingVersion: "fixture", NoChargeStatuses: "failed,cancelled", BoundsJSON: `{"720p/16:9":{"width":1280,"height":720}}`, VerifiedAt: &now, UpdatedAt: now}).Error)
	check(db.Create(&videobilling.PricingRule{RuleID: "fixture", Version: "fixture", PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: ark.Model20, Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "fixture", SourceHash: strings.Repeat("a", 64), EffectiveFrom: now.Add(-time.Hour), Enabled: true}).Error)
	check(db.Create(&video.Policy{OrgID: org.ID, AsyncExecutionAllowed: true, CaptureHistory: true, RetentionDays: 30, UpdatedAt: now}).Error)
	var posts, gets atomic.Int32
	service.Client = &http.Client{Transport: mutationShutdownTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			body = `{"id":"up_shutdown_fixture"}`
		case http.MethodGet:
			if gets.Add(1) == 1 {
				body = `{"id":"up_shutdown_fixture","status":"queued"}`
			} else {
				body = `{"id":"up_shutdown_fixture","status":"succeeded","usage":{"completion_tokens":100,"total_tokens":100}}`
			}
		default:
			return nil, errors.New("unexpected fixture HTTP method")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	view, _, err := service.Create(context.Background(), key, "shutdown-create-request", "shutdown-create", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "synthetic shutdown fixture"}}})
	check(err)
	check(service.ProcessDispatchOnce(context.Background()))
	queued, err := service.Get(org.ID, view.ID)
	check(err)
	if posts.Load() != 1 || gets.Load() != 1 || queued.Status != "queued" {
		t.Fatal("fixture did not create through the actual provider dispatch path")
	}

	// The second real GET is durable before this exact event INSERT failure.
	// The rollback preserves the hold and all financial facts; no receipt or
	// observation is inserted directly by this fixture.
	before := persistedHostFinancialFingerprint(t, db)
	var eventFailures atomic.Int32
	failEvent := errors.New("synthetic S08 event insert failure")
	const callback = "s08_terminal_apply_event"
	check(db.Callback().Create().Before("gorm:create").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "video_events" {
			eventFailures.Add(1)
			q.AddError(failEvent)
		}
	}))
	mutationShutdownMakeQueryDue(t, db, org.ID, view.ID, scope.ID)
	applyErr := service.ProcessDispatchOnce(context.Background())
	check(db.Callback().Create().Remove(callback))
	// Dispatch preserves the existing per-task error logging behavior. The
	// actual callback hit plus pending receipt/rolled-back money prove failure.
	if applyErr != nil || eventFailures.Load() != 1 || posts.Load() != 1 || gets.Load() != 2 {
		t.Fatal("fixture did not persist one terminal GET then roll back its actual apply")
	}
	if persistedHostFinancialFingerprint(t, db) != before {
		t.Fatal("failed query application changed financial facts")
	}
	var receipt video.QueryReceipt
	check(db.Where("org_id = ? AND task_id = ? AND sequence = ?", org.ID, view.ID, 2).Take(&receipt).Error)
	if receipt.AppliedAt != nil || receipt.PayloadRef == "" || receipt.ObservationJSON == "" || receipt.RecoveryDueAt == nil || receipt.ApplyErrorCode != "query_apply_failed" {
		t.Fatal("missing actual pending terminal query evidence")
	}
	persistedHostSetReceiptDue(t, db, receipt, false)
	operation, _, err := service.Cancel(context.Background(), org.ID, view.ID, "s08-cancel-request", "s08-cancel")
	check(err)
	if operation.Status != "pending" {
		t.Fatal("fixture cancellation was not genuinely pending")
	}
	check(db.Where("org_id = ? AND task_id = ? AND sequence = ?", org.ID, view.ID, 2).Take(&receipt).Error)
	t.Log("fixture public calls: POST=1 GET=2 DELETE=0; terminal receipt persisted; only scheduling due moved to future")
	return mutationShutdownFixture{db: db, dsn: dsn, schema: schema, taskID: view.ID, operationID: operation.ID, org: org.ID}, receipt
}

// Only scheduling is changed, under the exact current evidence/version lock.
// Future -> due is performed only after the child has entered the real DELETE.
// This is an explicit fixture precondition, not a claim of elapsed wall time.
func persistedHostSetReceiptDue(t *testing.T, db *gorm.DB, identity video.QueryReceipt, due bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var databaseTime, actualDue time.Time
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before, after video.QueryReceipt
		where := "org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL"
		args := []any{identity.OrgID, identity.TaskID, identity.AttemptID, identity.RecoveryVersion}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(where, args...).Take(&before).Error; e != nil {
			return e
		}
		expr := "DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 HOUR)"
		if due {
			expr = "DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)"
		}
		changed := tx.Model(&video.QueryReceipt{}).Where(where, args...).UpdateColumn("recovery_due_at", gorm.Expr(expr))
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return errors.New("S08 due update did not affect exactly one receipt")
		}
		if e := tx.Where(where, args...).Take(&after).Error; e != nil {
			return e
		}
		if e := tx.Raw("SELECT UTC_TIMESTAMP(3)").Scan(&databaseTime).Error; e != nil {
			return e
		}
		if databaseTime.IsZero() || after.RecoveryDueAt == nil {
			return errors.New("S08 database time unavailable")
		}
		actualDue = *after.RecoveryDueAt
		if due != actualDue.Before(databaseTime) {
			return errors.New("S08 scheduling does not match actual database time")
		}
		after.RecoveryDueAt = before.RecoveryDueAt
		if !reflect.DeepEqual(before, after) {
			return errors.New("S08 scheduling update changed evidence or progress version")
		}
		return nil
	})
	if err != nil {
		t.Fatal("cannot establish exact S08 receipt scheduling", err)
	}
	t.Logf("receipt scheduling only: due=%t database_time=%s actual_due=%s", due, databaseTime.Format(time.RFC3339Nano), actualDue.Format(time.RFC3339Nano))
}

func persistedHostFinancialFingerprint(t *testing.T, db *gorm.DB) string {
	t.Helper()
	// This is this test's exclusive random schema. Raw column maps include all
	// persisted fields, including model fields with json:"-". No raw rows log.
	tables := []string{"ledger_accounts", "ledger_holds", "ledger_journal", "ledger_movements", "ledger_operations", "ledger_operation_events", "video_billing_snapshots", "video_billing_entries", "video_usage_evidence", "video_quota_reservations", "video_quota_buckets", "api_keys"}
	h := sha256.New()
	for _, table := range tables {
		var rows []map[string]any
		if e := db.Table(table).Order("id").Find(&rows).Error; e != nil {
			t.Fatal("cannot inspect private S08 financial table", table)
		}
		raw, e := json.Marshal(rows)
		if e != nil {
			t.Fatal("cannot fingerprint S08 financial fields")
		}
		h.Write([]byte(table))
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The child uses the exact production registration and unchanged 2s periods.
// Only its provider transport and leader predicate are synthetic. Every job
// calls the real Service methods and is owned by the real mainJobs tracker.
func TestPersistedRecoveryHostChildProcess(t *testing.T) {
	if os.Getenv("LAIAI_TEST_PERSISTED_RECOVERY_HOST_CHILD") == "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		os.Exit(3)
	}
	parsed, err := mysqlDriver.ParseDSN(cfg.DSN)
	if err != nil || !regexp.MustCompile(`^ark_test_mshutdown_[0-9a-f]{24}$`).MatchString(parsed.DBName) || cfg.VideoResponseInboxEnabled || !cfg.VideoMutationResponseInboxEnabled {
		os.Exit(3)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		os.Exit(3)
	}
	db, err = ledger.EnableActivityTracking(db)
	if err != nil {
		os.Exit(3)
	}
	service, err := video.NewService(db, cfg.Keyring, cfg.VideoHMACSecret, log)
	if err != nil || service.ConfigureResponseInbox(cfg.DSN, cfg.VideoResponseInboxEnabled) != nil {
		os.Exit(3)
	}
	service.MutationResponseInboxEnabled = cfg.VideoMutationResponseInboxEnabled
	var deletes, unexpected atomic.Int32
	// Follow main's actual asset/console initialization before registering all
	// five jobs. The private schema has no asset intents; a separate rejecting
	// management transport prevents any unexpected asset call leaving the child.
	assetClient := videoasset.NewManagementClient(&http.Client{Transport: mutationShutdownTransport(func(*http.Request) (*http.Response, error) {
		unexpected.Add(1)
		return nil, errors.New("unexpected S08 asset HTTP")
	})})
	if service.ConfigureAssets(video.AssetIntegrationOptions{Client: assetClient}) != nil || service.Assets.ConfigureConsole(videoasset.ConsoleOptions{}) != nil {
		os.Exit(3)
	}
	service.Gate = mutationShutdownGate{}
	service.Client = &http.Client{Transport: mutationShutdownTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v3/contents/generations/tasks/up_shutdown_fixture" {
			unexpected.Add(1)
			return nil, errors.New("unexpected S08 provider call")
		}
		if deletes.Add(1) != 1 {
			return nil, errors.New("repeated S08 DELETE")
		}
		fmt.Println("DELETE_BLOCKED")
		<-r.Context().Done()
		fmt.Println("DELETE_CANCELLED")
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	jobs, requests := newMainJobs(), newDrainTracker()
	base, cancelRequests := context.WithCancel(context.Background())
	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	srv := &http.Server{Handler: requests.wrap(http.NotFoundHandler()), BaseContext: func(net.Listener) context.Context { return base }}
	recorder := invocation.NewRecorder(db, log)
	registerVideoJobs(jobs, service, func() bool { return true }, log)
	runErr := awaitServerStop(stopCtx, jobs, func() error { return srv.Serve(listener) })
	var recorderAfterSave atomic.Bool
	drainErr := shutdownCore(srv, jobs, requests, cancelRequests, func() {
		var count int64
		recorderAfterSave.Store(db.Model(&video.ResponseInbox{}).Where("action = ?", "cancel_task").Count(&count).Error == nil && count == 1)
		recorder.Close()
		fmt.Println("RECORDER_CLOSED")
	}, productionShutdownBudgets)
	jobs.tracker.mu.Lock()
	joined := jobs.tracker.sealed && jobs.tracker.active == 0
	jobs.tracker.mu.Unlock()
	fmt.Printf("JOBS_JOINED=%t DELETE_COUNT=%d UNEXPECTED_HTTP=%d SAVE_BEFORE_RECORDER=%t\n", joined, deletes.Load(), unexpected.Load(), recorderAfterSave.Load())
	if !joined || !recorderAfterSave.Load() || deletes.Load() != 1 || unexpected.Load() != 0 {
		os.Exit(1)
	}
	os.Exit(shutdownExitCode(runErr, drainErr))
}

func TestMySQLPersistedRecoveryHostLoopsAndSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real SIGTERM host-loop integration runs on Linux")
	}
	f, receipt := newPersistedRecoveryHostFixture(t)
	initialFinance := persistedHostFinancialFingerprint(t, f.db)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPersistedRecoveryHostChildProcess$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "LAIAI_") && !strings.HasPrefix(entry, "ARK_TEST_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "LAIAI_TEST_PERSISTED_RECOVERY_HOST_CHILD=1", "LAIAI_DSN="+f.dsn, "LAIAI_AUTH_SECRET="+strings.Repeat("a", 32), "LAIAI_DATA_KEYS="+mutationShutdownKeySpec, "LAIAI_DATA_KEY_ACTIVE=test", "LAIAI_VIDEO_HMAC_SECRET="+strings.Repeat("h", 32), "LAIAI_VIDEO_RESPONSE_INBOX=false", "LAIAI_VIDEO_MUTATION_RESPONSE_INBOX=true")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("cannot open S08 child output")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal("cannot start S08 host child")
	}
	lines := make(chan string, 16)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	waited := false
	t.Cleanup(func() {
		if !waited {
			cancel()
			_ = cmd.Wait()
		}
		<-scanned
	})
	waitLine := func(want string) {
		t.Helper()
		select {
		case line := <-lines:
			if line != want {
				t.Fatal("unexpected S08 lifecycle event", line)
			}
		case <-ctx.Done():
			t.Fatal("S08 lifecycle event deadline", want)
		}
	}
	waitLine("DELETE_BLOCKED")
	if persistedHostFinancialFingerprint(t, f.db) != initialFinance {
		t.Fatal("recovery ran before DELETE blocking was proven")
	}
	var registration video.ExecutionAttempt
	if err = f.db.Where("org_id = ? AND operation_id = ?", f.org, f.operationID).Take(&registration).Error; err != nil || registration.StorageProtocol != "inbox-actions-v1" || registration.ExecutionSchemaVersion != 2 || registration.InputPayloadRef == "" {
		t.Fatal("blocked DELETE lacks actual atomic action registration")
	}
	persistedHostSetReceiptDue(t, f.db, receipt, true)
	// Poll committed state, not time guessed to imply recovery completed.
	deadline := time.Now().Add(10 * time.Second)
	applied := false
	for time.Now().Before(deadline) {
		var current video.QueryReceipt
		if err = f.db.Where("org_id = ? AND attempt_id = ?", f.org, receipt.AttemptID).Take(&current).Error; err != nil {
			t.Fatal("cannot observe real recovery commit")
		}
		if current.AppliedAt != nil {
			applied = true
			break
		}
		select {
		case line := <-lines:
			t.Fatal("dispatch returned before independent recovery commit", line)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !applied {
		t.Fatal("independent host recovery did not commit while DELETE remained blocked")
	}
	persistedHostAssertSettlement(t, f)
	settledFinance := persistedHostFinancialFingerprint(t, f.db)
	if settledFinance == initialFinance {
		t.Fatal("recovery did not perform the real settlement")
	}
	// Hold the exact registration row before SIGTERM. The cancelled HTTP returns
	// ACK, then the real dedicated append pool must wait on this lock even though
	// the caller has been cancelled. No fake append or sleep proves persistence.
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("cannot create S08 save barrier")
	}
	defer blocker.Rollback()
	var attempt string
	if err = blocker.QueryRowContext(ctx, "SELECT attempt_id FROM video_execution_attempts WHERE attempt_id=? FOR UPDATE", registration.AttemptID).Scan(&attempt); err != nil || attempt != registration.AttemptID {
		t.Fatal("cannot lock exact S08 response-save registration")
	}
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("cannot signal S08 host")
	}
	waitLine("DELETE_CANCELLED")
	mutationShutdownWaitLock(t, f, "")
	var saved int64
	if err = f.db.Model(&video.ResponseInbox{}).Where("org_id = ? AND attempt_id = ?", f.org, registration.AttemptID).Count(&saved).Error; err != nil || saved != 0 {
		t.Fatal("save barrier did not protect uncommitted late response")
	}
	select {
	case line := <-lines:
		t.Fatal("host drained before blocked detached save committed", line)
	default:
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal("cannot release S08 response-save barrier")
	}
	remaining := []string{}
	for line := range lines {
		remaining = append(remaining, line)
	}
	waitErr := cmd.Wait()
	waited = true
	if ctx.Err() != nil || waitErr != nil {
		t.Fatal("S08 host did not exit cleanly after SIGTERM")
	}
	if !reflect.DeepEqual(remaining, []string{"RECORDER_CLOSED", "JOBS_JOINED=true DELETE_COUNT=1 UNEXPECTED_HTTP=0 SAVE_BEFORE_RECORDER=true"}) {
		t.Fatal("host did not prove tracked drain and evidence-before-recorder", remaining)
	}
	if err = f.db.Model(&video.ResponseInbox{}).Where("org_id = ? AND attempt_id = ?", f.org, registration.AttemptID).Count(&saved).Error; err != nil || saved != 1 {
		t.Fatal("late DELETE response was not saved exactly once")
	}
	if persistedHostFinancialFingerprint(t, f.db) != settledFinance {
		t.Fatal("late ACK or shutdown changed settled funds")
	}
	// A fresh service with both new-send flags off replays persisted evidence.
	// The cancelled sender still has its original 180s claim: the saved ACK
	// remains pending until that lease expires. Do not forge expiry to claim
	// immediate mutation recovery; the terminal query remains exact-once.
	kr, err := crypto.NewKeyring(mutationShutdownKeySpec, "test")
	if err != nil {
		t.Fatal(err)
	}
	service, err := video.NewService(f.db, kr, []byte(strings.Repeat("h", 32)), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ConfigureResponseInbox(f.dsn, false); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := service.CloseResponseInbox(); closeErr != nil {
			t.Error("S08 replay evidence pool did not close")
		}
	}()
	var replayHTTP atomic.Int32
	service.Client = &http.Client{Transport: mutationShutdownTransport(func(*http.Request) (*http.Response, error) {
		replayHTTP.Add(1)
		return nil, errors.New("recovery must not call provider")
	})}
	var queryBefore, queryAfter video.QueryReceipt
	if err = f.db.Where("org_id = ? AND attempt_id = ?", f.org, receipt.AttemptID).Take(&queryBefore).Error; err != nil {
		t.Fatal("cannot snapshot committed S08 query receipt")
	}
	for i := 0; i < 2; i++ {
		if err = service.ProcessPersistedEvidenceOnce(ctx); err != nil {
			t.Fatal("zero-network persisted recovery failed", err)
		}
	}
	if replayHTTP.Load() != 0 || persistedHostFinancialFingerprint(t, f.db) != settledFinance {
		t.Fatal("recovery replay issued HTTP or repeated finances")
	}
	if err = f.db.Where("org_id = ? AND attempt_id = ?", f.org, receipt.AttemptID).Take(&queryAfter).Error; err != nil || !reflect.DeepEqual(queryBefore, queryAfter) {
		t.Fatal("replay rewrote the already committed terminal query receipt")
	}
	var operation video.Operation
	var databaseTime time.Time
	if f.db.Where("org_id = ? AND id = ?", f.org, f.operationID).Take(&operation).Error != nil || f.db.Raw("SELECT UTC_TIMESTAMP(3)").Scan(&databaseTime).Error != nil || databaseTime.IsZero() || operation.Status != "in_flight" || operation.AttemptID != registration.AttemptID || operation.LeaseUntil == nil || !operation.LeaseUntil.After(databaseTime) {
		t.Fatal("S08 pending-ACK assertion lost its real active claim precondition")
	}
	var application video.ReceiptApplication
	if err = f.db.Where("org_id = ? AND attempt_id = ?", f.org, registration.AttemptID).Take(&application).Error; err != nil || application.State != "pending" || application.AppliedAt != nil {
		t.Fatal("recovery bypassed the still-active original mutation lease")
	}
	persistedHostAssertSettlement(t, f)
	t.Log("S08 real production jobs: blocked DELETE; independent query settlement; SIGTERM; detached append lock; joined; Recorder after save; replay HTTP=0")
}

func persistedHostAssertSettlement(t *testing.T, f mutationShutdownFixture) {
	t.Helper()
	var taskRow task.Task
	var state video.State
	var entry videobilling.BillingEntry
	var hold ledger.Hold
	var account ledger.Account
	if f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Take(&taskRow).Error != nil || f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Take(&state).Error != nil || f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Take(&entry).Error != nil || f.db.Where("id = ?", entry.HoldID).Take(&hold).Error != nil || f.db.Where("id = ?", hold.AccountID).Take(&account).Error != nil {
		t.Fatal("cannot inspect committed S08 financial facts")
	}
	want := decimal.RequireFromString("0.0008") // 100 actual tokens * 8 USD / 1,000,000.
	if taskRow.Status != "succeeded" || state.BillingState != "settled" || taskRow.ActualTokens != 100 || !taskRow.CostUSD.Equal(want) || entry.State != videobilling.Settled || !entry.CalculatedUSD.Equal(want) || !entry.AbsorbedUSD.IsZero() || !entry.ReleasedUSD.Equal(hold.Amount.Sub(want)) || !entry.CapturedUSD.Equal(want) || !hold.CapturedAmount.Equal(want) || hold.Status != ledger.HoldCaptured || !account.PostedBalance.Equal(decimal.NewFromInt(100).Sub(want)) || !account.HeldAmount.IsZero() {
		t.Fatal("S08 recovery violated exact independent financial oracle")
	}

	var key identity.APIKey
	var reservations []videobilling.QuotaReservation
	if f.db.Where("id = ? AND org_id = ?", taskRow.APIKeyID, f.org).Take(&key).Error != nil || !key.UsedAmount.Equal(want) {
		t.Fatal("S08 Key usage was not finalized exactly")
	}
	if f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Find(&reservations).Error != nil || len(reservations) != 1 {
		t.Fatal("S08 actual provider quota reservation missing")
	}
	reservation := reservations[0]
	var bucket videobilling.QuotaBucket
	if f.db.Where("id = ?", reservation.BucketID).Take(&bucket).Error != nil || reservation.State != "consumed" || !reservation.Supplier || reservation.Metric != videobilling.MetricCNY || !reservation.ActualAmount.Equal(decimal.RequireFromString("0.0046")) || !bucket.ReservedAmount.IsZero() || !bucket.ConsumedAmount.Equal(decimal.RequireFromString("0.0046")) {
		t.Fatal("S08 actual provider quota was not consumed exactly")
	}
	var debit ledger.JournalEntry
	if entry.JournalID == nil || f.db.Where("id = ? AND account_id = ?", entry.JournalID, account.ID).Take(&debit).Error != nil || !debit.Delta.Equal(want.Neg()) {
		t.Fatal("S08 real capture journal is missing or wrong")
	}
	var count int64
	if f.db.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Count(&count).Error != nil || count != 1 {
		t.Fatal("S08 settled more than once")
	}
	if entry.CompletionTokens == nil || *entry.CompletionTokens != 100 || !entry.SupplierCNY.Equal(decimal.RequireFromString("0.0046")) || !entry.SupplierUSD.Equal(decimal.RequireFromString("0.000644")) {
		t.Fatal("S08 supplier amount or actual usage differs")
	}
}
