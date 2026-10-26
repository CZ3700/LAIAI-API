package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
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
	"github.com/laiai/laiai-next/core/internal/migrate"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// This is a real SIGTERM subprocess using main's original lifecycle functions
// and the actual ProcessOnce -> mutation -> dedicated-pool save call stack.
// It is deliberately NOT runCore end-to-end, a production provider connection,
// or the old/new binary handoff required by A2c. No production SSRF is disabled.
const mutationShutdownKeySpec = "test:a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s="

type mutationShutdownTransport func(*http.Request) (*http.Response, error)

func (f mutationShutdownTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type mutationShutdownGate struct{}

func (mutationShutdownGate) GateKey(*identity.APIKey, string, string) (int, string, bool) {
	return 200, "", true
}
func (mutationShutdownGate) AllowModelRate(uint64, string) (bool, int) { return true, 0 }

type mutationShutdownFixture struct {
	db                               *gorm.DB
	dsn, schema, taskID, operationID string
	org                              uint64
	funds                            string
}

func mutationShutdownPrivateDB(t *testing.T) (*gorm.DB, string, string) {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN required for real mutation shutdown integration")
	}
	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil || cfg.DBName != "ark_test" {
		t.Fatal("mutation shutdown requires the isolated ark_test connection")
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("cannot allocate private mutation shutdown schema")
	}
	name := "ark_test_mshutdown_" + hex.EncodeToString(nonce[:])
	if !regexp.MustCompile(`^ark_test_mshutdown_[0-9a-f]{24}$`).MatchString(name) {
		t.Fatal("invalid generated schema")
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("cannot open test schema administration connection")
	}
	created := false
	var pool *sql.DB
	t.Cleanup(func() {
		if pool != nil {
			_ = pool.Close()
		}
		if created {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
				t.Error("cannot drop this test's private mutation schema")
			}
		}
		_ = admin.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("cannot create private mutation shutdown schema")
	}
	created = true
	cfg.DBName, cfg.ParseTime, cfg.Loc = name, true, time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	// Kept in the primary DSN so ConfigureResponseInbox clones the same
	// identity/options; server 1205 occurs before its fixed 2s socket timeout.
	cfg.Params["innodb_lock_wait_timeout"] = "1"
	privateDSN := cfg.FormatDSN()
	db, err := gorm.Open(mysql.Open(privateDSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot open private mutation shutdown database")
	}
	pool, err = db.DB()
	if err != nil {
		t.Fatal("cannot obtain private test pool")
	}
	pool.SetMaxOpenConns(8)
	if _, err := migrate.Up(db, os.DirFS("../../../migrations")); err != nil {
		t.Fatal("tracked migrations failed in private mutation shutdown schema")
	}
	return db, privateDSN, name
}

func mutationShutdownMoney(t *testing.T, db *gorm.DB, org uint64) string {
	t.Helper()
	var accounts []ledger.Account
	var holds []ledger.Hold
	var journals []ledger.JournalEntry
	if db.Where("org_id = ?", org).Order("id").Find(&accounts).Error != nil || len(accounts) != 1 {
		t.Fatal("cannot inspect nonempty fixture account")
	}
	if db.Where("account_id = ?", accounts[0].ID).Order("id").Find(&holds).Error != nil || len(holds) != 1 {
		t.Fatal("cannot inspect fixture hold")
	}
	if db.Where("account_id = ?", accounts[0].ID).Order("id").Find(&journals).Error != nil || len(journals) == 0 {
		t.Fatal("cannot inspect fixture journal")
	}
	encoded, err := json.Marshal([]any{accounts, holds, journals})
	if err != nil {
		t.Fatal("cannot fingerprint fixture money")
	}
	return string(encoded)
}

// Only this private fixture's already-observed query and its provider rate are
// made due. This is an explicit scheduling precondition, not elapsed wall time
// or permission to change the task, receipt, claim, or financial result.
func mutationShutdownMakeQueryDue(t *testing.T, db *gorm.DB, org uint64, taskID string, scope uint64) {
	t.Helper()
	funds := mutationShutdownMoney(t, db, org)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before, after video.State
		where := "org_id = ? AND task_id = ? AND provider_scope_id = ? AND query_sequence = ? AND applied_query_sequence = ?"
		args := []any{org, taskID, scope, 1, 1}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(where, args...).Take(&before).Error; err != nil {
			return err
		}
		if before.QueryLeaseUntil != nil {
			return errors.New("fixture first GET still owns a query lease")
		}
		changed := tx.Model(&video.State{}).Where(where, args...).UpdateColumn("next_poll_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)"))
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return errors.New("fixture query due update did not affect one observed sequence")
		}
		if err := tx.Where(where, args...).Take(&after).Error; err != nil {
			return err
		}
		var rateBefore, rateAfter video.ProviderRate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope_id = ? AND action = ?", scope, "query").Take(&rateBefore).Error; err != nil {
			return err
		}
		changed = tx.Model(&video.ProviderRate{}).Where("scope_id = ? AND action = ?", scope, "query").UpdateColumn("next_allowed_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)"))
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return errors.New("fixture query rate update did not affect one provider scope")
		}
		if err := tx.Where("scope_id = ? AND action = ?", scope, "query").Take(&rateAfter).Error; err != nil {
			return err
		}
		var clock struct{ DatabaseTime time.Time }
		if err := tx.Raw("SELECT UTC_TIMESTAMP(3) AS database_time").Scan(&clock).Error; err != nil {
			return err
		}
		if clock.DatabaseTime.IsZero() || !after.NextPollAt.Before(clock.DatabaseTime) || !rateAfter.NextAllowedAt.Before(clock.DatabaseTime) {
			return errors.New("fixture query or rate is not due in database time")
		}
		after.NextPollAt, rateAfter.NextAllowedAt = before.NextPollAt, rateBefore.NextAllowedAt
		if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(rateAfter, rateBefore) {
			return errors.New("fixture query scheduling changed unrelated fields")
		}
		return nil
	})
	if err != nil {
		t.Fatal("cannot establish exact database-due query fixture", err)
	}
	if mutationShutdownMoney(t, db, org) != funds {
		t.Fatal("query due fixture changed finances before the real GET")
	}
}

func newMutationShutdownFixture(t *testing.T, action string) mutationShutdownFixture {
	t.Helper()
	db, dsn, schema := mutationShutdownPrivateDB(t)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal("mutation shutdown fixture initialization failed", err)
		}
	}
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
	check(service.ProcessOnce(context.Background()))
	queued, err := service.Get(org.ID, view.ID)
	check(err)
	if posts.Load() != 1 || gets.Load() != 1 || queued.Status != "queued" {
		t.Fatal("fixture did not create through the actual provider dispatch path")
	}
	var operation video.Operation
	if action == "delete" {
		mutationShutdownMakeQueryDue(t, db, org.ID, view.ID, scope.ID)
		check(service.ProcessOnce(context.Background()))
		finished, err := service.Get(org.ID, view.ID)
		check(err)
		if finished.Status != "succeeded" || finished.BillingState != "settled" || gets.Load() != 2 {
			t.Fatal("delete fixture did not settle through the real GET reducer")
		}
		operation, _, err = service.Delete(context.Background(), org.ID, view.ID, "shutdown-delete-request")
		check(err)
	} else {
		operation, _, err = service.Cancel(context.Background(), org.ID, view.ID, "shutdown-cancel-request", "shutdown-cancel")
		check(err)
	}
	if operation.Status != "pending" {
		t.Fatal("fixture action was not pending")
	}
	// The child uses its real clock; retain an ordinary creation timestamp for
	// worker choice without changing the pending operation or its claim.
	check(db.Model(&video.Operation{}).Where("id = ?", operation.ID).Update("created_at", time.Now().UTC()).Error)
	return mutationShutdownFixture{db: db, dsn: dsn, schema: schema, taskID: view.ID, operationID: operation.ID, org: org.ID, funds: mutationShutdownMoney(t, db, org.ID)}
}

func TestMutationShutdownChildProcess(t *testing.T) {
	mode := os.Getenv("LAIAI_TEST_MUTATION_SHUTDOWN_CHILD")
	if mode == "" {
		return
	}
	if mode != "response" && mode != "retry" && mode != "timeout" {
		os.Exit(3)
	}
	cfg, err := config.Load()
	if err != nil {
		os.Exit(3)
	}
	dsn, err := mysqlDriver.ParseDSN(cfg.DSN)
	if err != nil || !regexp.MustCompile(`^ark_test_mshutdown_[0-9a-f]{24}$`).MatchString(dsn.DBName) || !cfg.VideoMutationResponseInboxEnabled || cfg.VideoResponseInboxEnabled {
		os.Exit(3)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		os.Exit(3)
	}
	service, err := video.NewService(db, cfg.Keyring, cfg.VideoHMACSecret, log)
	if err != nil || service.ConfigureResponseInbox(cfg.DSN, cfg.VideoResponseInboxEnabled) != nil {
		os.Exit(3)
	}
	service.MutationResponseInboxEnabled = cfg.VideoMutationResponseInboxEnabled
	service.Gate = mutationShutdownGate{}
	var deletes atomic.Int32
	service.Client = &http.Client{Transport: mutationShutdownTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v3/contents/generations/tasks/up_shutdown_fixture" {
			return nil, errors.New("unexpected child provider operation")
		}
		if deletes.Add(1) != 1 {
			return nil, errors.New("child repeated DELETE")
		}
		fmt.Println("DELETE_READY")
		if mode == "response" {
			<-r.Context().Done()
		} else {
			// The transport stays synchronous on the tracked worker stack. The
			// parent owns this pipe and releases it after taking the real row lock.
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil || line != "return\n" {
				return nil, errors.New("invalid child barrier command")
			}
		}
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
	jobs.goRun(func(ctx context.Context) {
		periodic(ctx, time.Millisecond, func(ctx context.Context) { _ = service.ProcessOnce(ctx) })
		fmt.Println("WORK_DONE")
	})
	runErr := awaitServerStop(stopCtx, jobs, func() error { return srv.Serve(listener) })
	budgets := productionShutdownBudgets
	if mode == "timeout" {
		budgets = shutdownBudgets{total: 400 * time.Millisecond, http: 80 * time.Millisecond, recorder: 50 * time.Millisecond}
	}
	drainErr := shutdownCore(srv, jobs, requests, cancelRequests, func() { recorder.Close(); fmt.Println("RECORDER_CLOSED") }, budgets)
	// Match main: neither success nor timeout proactively closes shared pools.
	// Process exit owns teardown; the parent verifies committed rows afterward.
	fmt.Printf("DELETE_COUNT=%d\n", deletes.Load())
	os.Exit(shutdownExitCode(runErr, drainErr))
}

func mutationShutdownWaitLock(t *testing.T, f mutationShutdownFixture, differentFrom string) string {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var ids []string
		err := f.db.WithContext(ctx).Raw(`SELECT CAST(w.REQUESTING_ENGINE_TRANSACTION_ID AS CHAR) FROM performance_schema.data_lock_waits w JOIN performance_schema.data_locks l ON l.ENGINE = w.ENGINE AND l.ENGINE_LOCK_ID = w.REQUESTING_ENGINE_LOCK_ID WHERE l.OBJECT_SCHEMA = ? AND l.OBJECT_NAME = 'video_execution_attempts' AND l.INDEX_NAME = 'PRIMARY'`, f.schema).Scan(&ids).Error
		cancel()
		if err != nil {
			t.Fatal("test connection must read actual MySQL data_lock_waits to prove save retry")
		}
		for _, id := range ids {
			if id != "" && id != differentFrom {
				return id
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("did not observe the required distinct real response-save locking transaction")
	return ""
}

func TestMySQLMutationShutdownSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("actual SIGTERM mutation business-stack subprocess runs on Linux CI")
	}
	for _, mode := range []string{"response", "retry", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			action := "delete"
			if mode == "response" {
				action = "cancel"
			}
			f := newMutationShutdownFixture(t, action)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMutationShutdownChildProcess$")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "LAIAI_") && !strings.HasPrefix(entry, "ARK_TEST_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "LAIAI_TEST_MUTATION_SHUTDOWN_CHILD="+mode, "LAIAI_DSN="+f.dsn, "LAIAI_AUTH_SECRET="+strings.Repeat("a", 32), "LAIAI_DATA_KEYS="+mutationShutdownKeySpec, "LAIAI_DATA_KEY_ACTIVE=test", "LAIAI_VIDEO_HMAC_SECRET="+strings.Repeat("h", 32), "LAIAI_VIDEO_RESPONSE_INBOX=false", "LAIAI_VIDEO_MUTATION_RESPONSE_INBOX=true")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal("cannot open child stdout")
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal("cannot open child barrier pipe")
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal("cannot start actual mutation shutdown child")
			}
			lines := make(chan string, 16)
			scanned := make(chan struct{})
			go func() {
				defer close(scanned)
				defer close(lines)
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					lines <- scanner.Text()
				}
			}()
			waited := false
			t.Cleanup(func() {
				_ = stdin.Close()
				if !waited {
					cancel()
					_ = cmd.Wait()
				}
				<-scanned
			})
			select {
			case line := <-lines:
				if line != "DELETE_READY" {
					t.Fatal("child did not reach the actual DELETE transport")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("child did not dispatch the pending mutation")
			}
			var attempt video.ExecutionAttempt
			if err := f.db.Where("org_id = ? AND operation_id = ?", f.org, f.operationID).Take(&attempt).Error; err != nil || attempt.StorageProtocol != "inbox-actions-v1" || attempt.InputPayloadRef == "" {
				t.Fatal("actual producer did not atomically register its action input")
			}
			var blocker *sql.Tx
			if mode != "response" {
				pool, err := f.db.DB()
				if err != nil {
					t.Fatal(err)
				}
				blocker, err = pool.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal("cannot start append blocker")
				}
				defer blocker.Rollback()
				var id string
				if err := blocker.QueryRowContext(ctx, "SELECT attempt_id FROM video_execution_attempts WHERE attempt_id=? FOR UPDATE", attempt.AttemptID).Scan(&id); err != nil || id != attempt.AttemptID {
					t.Fatal("cannot lock the exact immutable action registration")
				}
				if _, err := io.WriteString(stdin, "return\n"); err != nil {
					t.Fatal("cannot release DELETE response")
				}
				first := mutationShutdownWaitLock(t, f, "")
				if mode == "retry" {
					second := mutationShutdownWaitLock(t, f, first)
					t.Logf("observed distinct append transactions %s -> %s before SIGTERM; first attempt rolled back while parent retained the same row lock", first, second)
				}
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal("cannot deliver actual SIGTERM")
			}
			if mode == "retry" {
				// While detached saving is blocked, a truthful drain cannot exit.
				select {
				case line := <-lines:
					t.Fatalf("child left tracked save early: %s", line)
				case <-time.After(50 * time.Millisecond):
				}
				if err := blocker.Rollback(); err != nil {
					t.Fatal("cannot release response save lock")
				}
			}
			remaining := []string{}
			for line := range lines {
				remaining = append(remaining, line)
			}
			waitErr := cmd.Wait()
			waited = true
			if ctx.Err() != nil {
				t.Fatal("mutation child exceeded the test deadline")
			}
			output := strings.Join(remaining, "\n")
			if !strings.Contains(output, "DELETE_COUNT=1") {
				t.Fatal("child did not preserve one actual DELETE", output)
			}
			var count int64
			if err := f.db.Model(&video.ResponseInbox{}).Where("org_id = ? AND attempt_id = ?", f.org, attempt.AttemptID).Count(&count).Error; err != nil {
				t.Fatal("cannot inspect committed child evidence")
			}
			if mode == "timeout" {
				var exit *exec.ExitError
				if !errors.As(waitErr, &exit) || exit.ExitCode() != 1 || strings.Contains(output, "RECORDER_CLOSED") || strings.Contains(output, "WORK_DONE") || count != 0 {
					t.Fatal("undrained real save did not exit nonzero before Recorder close", waitErr, output, count)
				}
				if err := blocker.Rollback(); err != nil {
					t.Fatal("cannot release timeout fixture lock")
				}
			} else {
				if waitErr != nil || count != 1 || strings.Index(output, "WORK_DONE") < 0 || strings.Index(output, "RECORDER_CLOSED") < strings.Index(output, "WORK_DONE") {
					t.Fatal("SIGTERM exited before actual response evidence and worker drain", waitErr, output, count)
				}
				var receipt video.ResponseInbox
				if err := f.db.Where("org_id = ? AND attempt_id = ?", f.org, attempt.AttemptID).Take(&receipt).Error; err != nil || receipt.PayloadRef == "" || receipt.OperationID != f.operationID {
					t.Fatal("saved response does not bind the actual operation")
				}
				var payload invocation.VideoPayload
				if err := f.db.Where("payload_id = ? AND org_id = ?", receipt.PayloadRef, f.org).Take(&payload).Error; err != nil {
					t.Fatal("receipt committed without Invocation metadata")
				}
			}
			if mutationShutdownMoney(t, f.db, f.org) != f.funds {
				t.Fatal("shutdown/late response mutated settled or held money")
			}
		})
	}
}

// Keep the literal key fixture honest without ever printing key material.
func TestMutationShutdownKeyFixture(t *testing.T) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(mutationShutdownKeySpec, "test:"))
	if err != nil || len(decoded) != 32 {
		t.Fatal("invalid synthetic fixture key")
	}
}
