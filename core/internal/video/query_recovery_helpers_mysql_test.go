package video

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type queryRecoveryFixture struct {
	db       *gorm.DB
	dsn      string
	keyring  *crypto.Keyring
	users    [2]*fixture
	scope    uint64
	blocked  atomic.Int32
	readKeys atomic.Int32
}

type queryRecoveryPending struct {
	f       *fixture
	row     QueryReceipt
	storage [32]byte
}

func queryRecoveryCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		// Do not emit SQL, credentials, response bodies or typed input details.
		t.Fatal("query recovery fixture operation failed")
	}
}

// A separate schema for every top-level test prevents unrelated historical
// candidates from changing the real global ORDER/LIMIT. No environment rewrite
// or shared ark_test reset is used. All schema comes from tracked migrations.
func queryRecoveryNew(t *testing.T) *queryRecoveryFixture {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN is required for real query recovery integration")
	}
	cfg, err := parseVideoTestControlDSN(dsn)
	if err != nil {
		t.Fatal("query recovery requires an approved isolated control database")
	}
	cfg = cfg.Clone()
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 35*time.Second, 35*time.Second
	cfg.Logger = log.New(io.Discard, "", 0)
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	connector, err := drivermysql.NewConnector(cfg.Clone())
	queryRecoveryCheck(t, err)
	admin := sql.OpenDB(connector)
	var nonce [12]byte
	_, err = rand.Read(nonce[:])
	queryRecoveryCheck(t, err)
	name := "ark_test_queryrec_" + hex.EncodeToString(nonce[:])
	var pool *sql.DB
	created := false
	t.Cleanup(func() {
		if pool != nil {
			_ = pool.Close()
		}
		if created && !t.Failed() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
				t.Error("failed to clean this exact private query recovery schema")
			}
		}
		_ = admin.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin")
	queryRecoveryCheck(t, err)
	created = true
	cfg.DBName = name
	cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 5*time.Second
	connector, err = drivermysql.NewConnector(cfg.Clone())
	queryRecoveryCheck(t, err)
	pool = sql.OpenDB(connector)
	pool.SetMaxOpenConns(12)
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, DSNConfig: cfg.Clone()}), &gorm.Config{Logger: logger.Discard})
	queryRecoveryCheck(t, err)
	ran, err := migrate.Up(db, os.DirFS("../../../migrations"))
	queryRecoveryCheck(t, err)
	if len(ran) != 135 || ran[len(ran)-1] != "0135_video_query_settlement_shape" {
		t.Fatal("query recovery fixture requires the complete tracked 0135 schema")
	}
	db, err = ledger.EnableActivityTracking(db)
	queryRecoveryCheck(t, err)
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
	queryRecoveryCheck(t, err)
	h := &queryRecoveryFixture{db: db, dsn: cfg.FormatDSN(), keyring: kr}
	now := time.Now().UTC().Truncate(time.Millisecond)
	encrypted, err := kr.Encrypt("official-test-credential")
	queryRecoveryCheck(t, err)
	channel := relay.Channel{Name: "Query recovery", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", CredentialEnc: encrypted, Models: ark.Model20, Weight: 1, Status: 1, MaxConcurrentTasks: 100}
	queryRecoveryCheck(t, db.Create(&channel).Error)
	scope := ProviderScope{ScopeKey: "query-recovery", AccountRef: "test-query-recovery", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "test-fx", FXObservedAt: now}
	queryRecoveryCheck(t, db.Create(&scope).Error)
	h.scope = scope.ID
	limit := decimal.NewFromInt(1000000)
	queryRecoveryCheck(t, db.Create(&videobilling.QuotaPolicy{Scope: videobilling.ScopeProvider, OwnerID: scope.ID, Metric: videobilling.MetricCNY, Period: videobilling.PeriodLifetime, Timezone: "Asia/Shanghai", LimitAmount: &limit, Version: "query-recovery", Enabled: true}).Error)
	capability := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "test-cap-v1", Enabled: true, Verified: true, Resolutions: []string{"720p"}, Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: 15, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4}
	raw, err := json.Marshal(capability)
	queryRecoveryCheck(t, err)
	queryRecoveryCheck(t, db.Create(&ModelConfig{PublicModel: ark.Model20, Version: "query-recovery", ProviderScopeID: scope.ID, ChannelID: channel.ID, Enabled: true, CapabilityJSON: string(raw), PricingVersion: "query-recovery", NoChargeStatuses: "failed,cancelled", BoundsJSON: `{"720p/16:9":{"width":1280,"height":720}}`, VerifiedAt: &now, UpdatedAt: now}).Error)
	queryRecoveryCheck(t, db.Create(&videobilling.PricingRule{RuleID: "query-recovery", Version: "query-recovery", PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: ark.Model20, Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "test-formula", SourceHash: strings.Repeat("a", 64), EffectiveFrom: now.Add(-time.Hour), Enabled: true}).Error)
	for index := range h.users {
		org := identity.Organization{Slug: fmt.Sprintf("query-org-%d", index), Name: "Query recovery", Group: "default", Status: 1}
		queryRecoveryCheck(t, db.Create(&org).Error)
		s := h.service(t)
		f := &fixture{s: s, now: now}
		s.Now = func() time.Time { return f.now }
		queryRecoveryCheck(t, db.Transaction(func(tx *gorm.DB) error {
			var err error
			f.plain, f.key, err = identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, Name: "query recovery", Scopes: "video:*"})
			if err != nil {
				return err
			}
			a, err := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
			if err != nil {
				return err
			}
			_, _, err = ledger.Post(tx, ledger.PostInput{IdempotencyKey: fmt.Sprintf("query-credit-%d", org.ID), AccountID: a.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100), RefType: "integration", RefID: org.Slug})
			return err
		}))
		queryRecoveryCheck(t, db.Create(&Policy{OrgID: org.ID, AsyncExecutionAllowed: true, CaptureHistory: true, RetentionDays: 30, UpdatedAt: now}).Error)
		s.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPost {
				f.posts.Add(1)
			} else if req.Method == http.MethodGet {
				f.gets.Add(1)
			}
			if f.response == nil {
				return nil, errors.New("unexpected query fixture HTTP")
			}
			return f.response(req)
		})}
		h.users[index] = f
	}
	return h
}

func (h *queryRecoveryFixture) service(t *testing.T) *Service {
	t.Helper()
	s, err := NewService(h.db, h.keyring, []byte(strings.Repeat("h", 32)), nil)
	queryRecoveryCheck(t, err)
	s.Gate = allowGate{}
	queryRecoveryCheck(t, s.ConfigureResponseInbox(h.dsn, false))
	t.Cleanup(func() { queryRecoveryCheck(t, s.CloseResponseInbox()) })
	return s
}

// The public Create, real POST/GET client, SQL evidence transaction and billing
// reducer run unchanged. Only the final scoped event INSERT is faulted.
func (h *queryRecoveryFixture) pending(t *testing.T, user, index int) queryRecoveryPending {
	t.Helper()
	f := h.users[user]
	posts, gets := f.posts.Load(), f.gets.Load()
	view := f.create(t, fmt.Sprintf("query-recovery-%d", index))
	upstream := "up_" + view.ID
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "ark.cn-beijing.volces.com" || req.Header.Get("Authorization") != "Bearer official-test-credential" {
			return nil, errors.New("query fixture target or credential mismatch")
		}
		path := "/api/v3/contents/generations/tasks"
		if req.Method == http.MethodPost && req.URL.Path == path {
			return reply(200, `{"id":"`+upstream+`"}`), nil
		}
		if req.Method == http.MethodGet && req.URL.Path == path+"/"+upstream {
			return reply(200, `{"id":"`+upstream+`","status":"succeeded","usage":{"completion_tokens":100,"total_tokens":999}}`), nil
		}
		return nil, errors.New("unexpected query fixture request")
	}
	// These are exact scheduling-only fixture preconditions, not renewal or
	// simulated wall-clock passage. The provider rate is shared by all tasks.
	var rates []ProviderRate
	queryRecoveryCheck(t, h.db.Where("scope_id = ?", h.scope).Find(&rates).Error)
	for _, rate := range rates {
		makeFixtureProviderRateDue(t, h.db, h.scope, rate.Action)
	}
	f.dispatch(t, view.ID)
	before := h.finances(t)
	var hits int
	hook := fmt.Sprintf("test:query-recovery-event-%d", index)
	queryRecoveryCheck(t, h.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		event, ok := tx.Statement.Dest.(*Event)
		if !ok || tx.Statement.Table != "video_events" || event.OrgID != f.key.OrgID || event.TaskID != view.ID || event.Type != "task.status_changed" {
			return
		}
		hits++
		// Real MySQL rejects a nonexistent column, not just AddError(fake).
		failure := tx.Exec("INSERT INTO video_events (query_recovery_missing_column) VALUES (1)").Error
		tx.AddError(failure)
	}))
	err := f.s.pollTask(context.Background(), f.key.OrgID, view.ID)
	queryRecoveryCheck(t, h.db.Callback().Create().Remove(hook))
	var mysqlErr *drivermysql.MySQLError
	if hits != 1 || !errors.As(err, &mysqlErr) || mysqlErr.Number != 1054 || h.finances(t) != before {
		t.Fatal("actual post-response SQL rollback did not preserve funds")
	}
	var row QueryReceipt
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ?", f.key.OrgID, view.ID).Take(&row).Error)
	if row.AppliedAt != nil || row.PayloadRef == "" || row.ObservationJSON == "" || row.RecoveryDueAt == nil || row.RecoveryVersion != 1 || row.ApplyErrorCode != "query_apply_failed" || row.Outcome != "accepted" || row.HTTPStatus != 200 {
		t.Fatal("real pending query evidence or committed backoff is missing")
	}
	var evidenceCount int64
	queryRecoveryCheck(t, h.db.Table("invocation_payloads").Where("org_id = ? AND payload_id = ?", row.OrgID, row.PayloadRef).Count(&evidenceCount).Error)
	if evidenceCount != 1 || f.posts.Load() != posts+1 || f.gets.Load() != gets+1 {
		t.Fatal("query response did not retain its original encrypted capsule")
	}
	return queryRecoveryPending{f: f, row: row}
}

func (h *queryRecoveryFixture) arrange(t *testing.T, item *queryRecoveryPending, invalid bool, seconds int) {
	t.Helper()
	before := item.row
	updates := map[string]any{"recovery_due_at": gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL ? SECOND)", seconds)}
	if invalid {
		updates["observation_json"] = `{"completion_tokens":"invalid-test-type"}`
	}
	q := h.db.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ?", before.OrgID, before.TaskID, before.AttemptID, before.RecoveryVersion).Updates(updates)
	queryRecoveryCheck(t, q.Error)
	if q.RowsAffected != 1 {
		t.Fatal("query fault fixture did not affect one exact receipt")
	}
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND attempt_id = ?", before.OrgID, before.AttemptID).Take(&item.row).Error)
	copy := item.row
	copy.RecoveryDueAt, copy.ObservationJSON = before.RecoveryDueAt, before.ObservationJSON
	if !reflect.DeepEqual(copy, before) {
		t.Fatal("fixture altered evidence outside explicit JSON/due injection")
	}
	item.storage = h.taskStorage(t, before.OrgID, before.TaskID)
}

func (h *queryRecoveryFixture) forbidNetwork(t *testing.T, s *Service) {
	t.Helper()
	s.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		h.blocked.Add(1)
		return nil, errors.New("recovery attempted provider HTTP")
	})}
	// clientFor must fetch the credential snapshot before constructing Ark.
	// Forbid this actual query boundary as well as any later HTTP request.
	name := fmt.Sprintf("test:query-recovery-credentials-%p", s)
	queryRecoveryCheck(t, s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_credential_snapshots" {
			h.readKeys.Add(1)
			tx.AddError(errors.New("recovery attempted credential loading"))
		}
	}))
	t.Cleanup(func() { s.DB.Callback().Query().Remove(name) })
}

var queryRecoveryFinancialTables = []string{"ledger_accounts", "ledger_holds", "ledger_journal", "ledger_operations", "ledger_operation_events", "ledger_movements", "video_billing_snapshots", "video_billing_entries", "video_usage_evidence", "video_quota_reservations", "video_quota_buckets", "api_keys"}

// Raw SQL column maps include all durable fields, regardless of DTO json tags.
// Every table uses its real primary-key order; only hashes leave this helper.
func queryRecoveryProjection(t *testing.T, db *gorm.DB, table, where string, args ...any) []map[string]any {
	t.Helper()
	var keys []string
	queryRecoveryCheck(t, db.Raw("SELECT column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name='PRIMARY' ORDER BY seq_in_index", table).Scan(&keys).Error)
	if len(keys) == 0 {
		t.Fatal("complete storage projection requires a real primary key")
	}
	var rows []map[string]any
	q := db.Table(table).Order("`" + strings.Join(keys, "`,`") + "`")
	if where != "" {
		q = q.Where(where, args...)
	}
	queryRecoveryCheck(t, q.Find(&rows).Error)
	return rows
}

func queryRecoveryHash(t *testing.T, value any) [32]byte {
	t.Helper()
	raw, err := json.Marshal(value)
	queryRecoveryCheck(t, err)
	return sha256.Sum256(raw)
}

func (h *queryRecoveryFixture) finances(t *testing.T) [32]byte {
	t.Helper()
	facts := map[string]any{}
	queryRecoveryCheck(t, h.db.Transaction(func(tx *gorm.DB) error {
		for _, table := range queryRecoveryFinancialTables {
			facts[table] = queryRecoveryProjection(t, tx, table, "")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}))
	return queryRecoveryHash(t, facts)
}

func (h *queryRecoveryFixture) taskStorage(t *testing.T, org uint64, taskID string) [32]byte {
	t.Helper()
	facts := map[string]any{}
	queryRecoveryCheck(t, h.db.Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"tasks", "video_task_states", "video_operations", "video_events", "video_outbox", "video_attempts", "video_query_receipts", "video_billing_snapshots", "video_quota_reservations"} {
			column := "task_id"
			if table == "video_operations" {
				column = "resource_id"
			}
			rows := queryRecoveryProjection(t, tx, table, "org_id = ? AND "+column+" = ?", org, taskID)
			if table == "video_query_receipts" {
				for _, row := range rows {
					// These four application/progress columns are checked separately.
					for _, field := range []string{"applied_at", "apply_error_code", "recovery_due_at", "recovery_version"} {
						delete(row, field)
					}
				}
			}
			facts[table] = rows
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}))
	return queryRecoveryHash(t, facts)
}

func (h *queryRecoveryFixture) evidenceStorage(t *testing.T) [32]byte {
	t.Helper()
	facts := map[string]any{}
	queryRecoveryCheck(t, h.db.Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"invocation_payloads", "invocation_payload_staging", "invocation_payload_accesses"} {
			facts[table] = queryRecoveryProjection(t, tx, table, "")
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}))
	return queryRecoveryHash(t, facts)
}
