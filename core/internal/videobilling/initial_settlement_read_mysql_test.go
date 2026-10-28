package videobilling

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func initialSettlementMySQL(t *testing.T) (*gorm.DB, QuoteResult) {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN=ark_test required for real initial settlement acceptance")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil || cfg.DBName != "ark_test" {
		t.Fatal("initial settlement tests require isolated ark_test")
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	cfg.Logger = log.New(io.Discard, "", 0)
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("cannot allocate private test schema")
	}
	name := "ark_test_initial_" + hex.EncodeToString(nonce[:])
	adminCfg := cfg.Clone()
	adminCfg.ReadTimeout = 15 * time.Second
	connector, err := mysqldriver.NewConnector(adminCfg)
	if err != nil {
		t.Fatal("cannot configure administration connection")
	}
	admin := sql.OpenDB(connector)
	created := false
	var pool *sql.DB
	t.Cleanup(func() {
		if pool != nil {
			if err := pool.Close(); err != nil {
				t.Error("private pool close failed")
			}
		}
		if created && t.Failed() {
			t.Logf("preserving failed synthetic schema: %s", name)
		} else if created {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`")
			cancel()
			if err != nil {
				t.Error("private schema cleanup failed")
			}
		}
		_ = admin.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin")
	cancel()
	if err != nil {
		t.Fatal("cannot create private initial settlement schema")
	}
	created = true
	cfg.DBName = name
	connector, err = mysqldriver.NewConnector(cfg)
	if err != nil {
		t.Fatal("cannot configure private business pool")
	}
	pool = sql.OpenDB(connector)
	pool.SetMaxOpenConns(6)
	pool.SetMaxIdleConns(6)
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot open private schema")
	}
	if _, err = migrate.Up(db, os.DirFS("../../../migrations")); err != nil {
		t.Fatal("tracked private schema migrations failed")
	}
	db, err = ledger.EnableActivityTracking(db)
	if err != nil {
		t.Fatal("cannot enable ledger activity tracking")
	}
	if err := db.Exec("INSERT INTO api_keys(id,org_id,prefix,key_hash) VALUES(1,10,?,?)", "synthetic", strings.Repeat("a", 64)).Error; err != nil {
		t.Fatal("synthetic key setup failed")
	}
	rule := ruleFixture()
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal("synthetic pricing setup failed")
	}
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, Period: PeriodLifetime, LimitAmount: amount("100")})
	policy(t, db, QuotaPolicy{OrgID: 0, Scope: ScopeProvider, OwnerID: 11, Metric: MetricCNY, Period: PeriodLifetime, LimitAmount: amount("100")})
	if err := db.Transaction(func(tx *gorm.DB) error {
		a, err := ledger.EnsureAccount(tx, 10, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		_, _, err = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "initial-fixture-credit", AccountID: a.ID, Kind: ledger.KindCredit, Delta: d("100"), RefType: "test", RefID: "initial"})
		return err
	}); err != nil {
		t.Fatal("synthetic credit setup failed")
	}
	quote, err := Quote(rule, meterFixture())
	if err != nil || quote.EstimatedTokens != 108000 || !quote.HoldUSD.Equal(d("0.864")) {
		t.Fatal("independent quote fixture failed")
	}
	return db, quote
}

type initialReadCaptureKey struct{}

func TestMySQLInitialSettlementCurrentReadAndRetainedLocks(t *testing.T) {
	db, q := initialSettlementMySQL(t)
	s := reserve(t, db, q, "task_initial").Snapshot
	ref := initialReadRef(s)
	type statement struct {
		table, sql string
		vars       []any
		pool       gorm.ConnPool
		rows       int64
	}
	var captured []statement
	var callerPool gorm.ConnPool
	writerCommitted := false
	const callback = "initial_current_reader"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(query *gorm.DB) {
		if query.Statement.Context.Value(initialReadCaptureKey{}) != true {
			return
		}
		captured = append(captured, statement{query.Statement.Table, query.Statement.SQL.String(), append([]any(nil), query.Statement.Vars...), query.Statement.ConnPool, query.RowsAffected})
		if len(captured) == 1 && query.Error == nil {
			// This second connection commits after the reader establishes its RR
			// snapshot. No reader callback fabricates a result or changes its SQL.
			err := db.Transaction(func(writer *gorm.DB) error {
				if writer.Statement.ConnPool == query.Statement.ConnPool {
					return errors.New("writer reused reader transaction")
				}
				_, err := SettleUsageTx(writer, SettlementInput{OrgID: 10, TaskID: s.TaskID, Now: testAt, Evidence: UsageEvidence{EvidenceID: "current-winner", Source: "manual_verified", SourceRecordID: s.TaskID, SourceVersion: "v1", CompletionTokens: token(100), ObservedAt: testAt}})
				return err
			})
			if err != nil {
				query.AddError(err)
				return
			}
			writerCommitted = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), initialReadCaptureKey{}, true), 2*time.Second)
	defer cancel()
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		callerPool = tx.Statement.ConnPool
		value, err := ReadInitialSettlementTx(tx.Table("polluted_table").Where("polluted_predicate = ?", "private").Select("missing"), ref)
		if err != nil {
			return err
		}
		if relation, err := value.Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: token(100)}); err != nil || relation != InitialSettlementRelationCompatible {
			return errors.New("RR initial winner not observed")
		}
		// Each actual lock remains owned by the caller after reader return.
		for _, query := range []struct {
			sql string
			arg any
		}{
			{"SELECT id FROM api_keys WHERE id = ? FOR UPDATE NOWAIT", s.APIKeyID},
			{"SELECT id FROM video_billing_snapshots WHERE id = ? FOR UPDATE NOWAIT", s.ID},
			{"SELECT id FROM video_billing_entries WHERE org_id = 10 AND task_id = ? AND settlement_key = 'initial' FOR UPDATE NOWAIT", s.TaskID},
		} {
			otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
			err := db.WithContext(otherCtx).Transaction(func(other *gorm.DB) error { var id uint64; return other.Raw(query.sql, query.arg).Scan(&id).Error })
			otherCancel()
			var mysqlErr *mysqldriver.MySQLError
			if !errors.As(err, &mysqlErr) || mysqlErr.Number != 3572 {
				return errors.New("caller lock was not retained")
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead}); err != nil {
		t.Fatal(err)
	}
	if !writerCommitted || len(captured) != 4 {
		t.Fatal("actual four-read RR proof missing")
	}
	wantTables := []string{"video_billing_snapshots", "api_keys", "video_billing_snapshots", "video_billing_entries"}
	for i, read := range captured {
		if read.table != wantTables[i] || read.pool != callerPool || read.rows != 1 || strings.Contains(read.sql, "polluted") {
			t.Fatal("read scope/connection/count failed")
		}
		if strings.Contains(read.sql, "FOR UPDATE") != (i > 0) {
			t.Fatal("current-read lock order failed")
		}
		var plan []struct {
			Type string
			Key  sql.NullString
			Rows sql.NullInt64
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := db.WithContext(ctx).Raw("EXPLAIN "+read.sql, read.vars...).Scan(&plan).Error
		cancel()
		if err != nil || len(plan) != 1 {
			t.Fatal("actual read EXPLAIN failed")
		}
		allowedKey := plan[0].Key.Valid && plan[0].Key.String == "PRIMARY"
		if read.table == "video_billing_snapshots" {
			allowedKey = allowedKey || (plan[0].Key.Valid && plan[0].Key.String == "uk_video_snapshot_task")
		}
		if read.table == "video_billing_entries" {
			allowedKey = plan[0].Key.Valid && plan[0].Key.String == "uk_video_initial_settlement"
		}
		access := plan[0].Type
		if !allowedKey || (access != "const" && access != "ref" && access != "eq_ref") || !plan[0].Rows.Valid || plan[0].Rows.Int64 != 1 {
			t.Fatalf("initial read did not use a single-row unique lookup: ordinal=%d access=%s key=%s rows=%d", i+1, access, plan[0].Key.String, plan[0].Rows.Int64)
		}
		t.Logf("initial read ordinal=%d rows=%d access=%s key=%s estimate=%d", i+1, read.rows, plan[0].Type, plan[0].Key.String, plan[0].Rows.Int64)
	}
	checkBalance(t, db, "99.9992", "0")
}

func TestMySQLInitialSettlementPublicWinnersAndAdjustment(t *testing.T) {
	db, q := initialSettlementMySQL(t)
	s := reserve(t, db, q, "task_1").Snapshot
	ref := initialReadRef(s)
	if initialRead(t, db, ref).Kind() != InitialSettlementAbsent {
		t.Fatal("new reservation has initial")
	}
	settle(t, db, s.TaskID, nil, "missing")
	if initialRead(t, db, ref).Kind() != InitialSettlementAbsent {
		t.Fatal("missing usage became zero")
	}
	line := importLine(t, db, lineFixture("v1", 1, token(100), "0.0046"), bindingFixture()).Line
	if err := db.Transaction(func(tx *gorm.DB) error { _, err := SettleBillUsageTx(tx, 10, s.TaskID, line.ID, testAt); return err }); err != nil {
		t.Fatal(err)
	}
	line2 := importLine(t, db, lineFixture("v2", 2, token(90), "0.00414"), bindingFixture()).Line
	proposal := propose(t, db, line2.ID, "initial-reader-reviewed", false)
	approve(t, db, proposal)
	adjustment := apply(t, db, proposal)
	if adjustment.Application.CompletionTokens == nil || *adjustment.Application.CompletionTokens != 90 {
		t.Fatal("reviewed adjustment not applied")
	}
	var before []BillingEntry
	if err := db.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	initial := initialRead(t, db, ref)
	for _, tc := range []struct {
		tokens   int64
		relation InitialSettlementRelation
	}{{100, InitialSettlementRelationCompatible}, {90, InitialSettlementRelationConflict}} {
		relation, err := initial.Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: token(tc.tokens)})
		if err != nil || relation != tc.relation {
			t.Fatal("original initial confused with reviewed adjustment")
		}
	}
	var after []BillingEntry
	if err := db.Order("id").Find(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("reader changed initial entry")
	}
	if !keyUsed(t, db).Equal(d("0.00072")) {
		t.Fatal("reader changed adjusted Key usage")
	}
	for _, reason := range []ReleaseReason{ReleaseProviderNoCharge, ReleaseNotDispatched} {
		snapshot := reserve(t, db, q, "task_"+string(reason)).Snapshot
		if err := db.Transaction(func(tx *gorm.DB) error {
			_, err := ReleaseConfirmedTx(tx, ReleaseInput{OrgID: 10, TaskID: snapshot.TaskID, Reason: reason, Confirmed: true, EvidenceID: "release_" + string(reason), Now: testAt})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		value := initialRead(t, db, initialReadRef(snapshot))
		want := InitialSettlementRelationConflict
		if reason == ReleaseProviderNoCharge {
			want = InitialSettlementRelationCompatible
		}
		if relation, err := value.Compare(InitialSettlementExpectation{Kind: InitialSettlementProviderNoChargeReleased}); err != nil || relation != want {
			t.Fatal("different release reasons conflated")
		}
	}
	zero := reserve(t, db, q, "task_zero").Snapshot
	result := settle(t, db, zero.TaskID, token(0), "v1")
	if result.Entry.JournalID != nil {
		t.Fatal("zero usage unexpectedly has capture journal")
	}
	if relation, err := initialRead(t, db, initialReadRef(zero)).Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: token(0)}); err != nil || relation != InitialSettlementRelationCompatible {
		t.Fatal("zero usage rejected")
	}
}

func TestMySQLInitialSettlementExactIdentityAndSafeFailure(t *testing.T) {
	db, q := initialSettlementMySQL(t)
	s := reserve(t, db, q, "task_initial").Snapshot
	settle(t, db, s.TaskID, token(100), "v1")
	ref := initialReadRef(s)
	for _, tc := range []struct {
		name, table, column string
		value               any
		want                error
	}{
		{"entry_task_pad", "video_billing_entries", "task_id", s.TaskID + " ", ErrInvariant},
		{"initial_key_pad", "video_billing_entries", "settlement_key", "initial ", ErrInvariant},
		{"state_pad", "video_billing_entries", "state", "settled ", ErrInvariant},
		{"reason_pad", "video_billing_entries", "reason", "actual_usage_settled ", ErrInvariant},
		{"snapshot_task_pad", "video_billing_snapshots", "task_id", s.TaskID + " ", ErrInvariant},
		{"hold_mismatch", "video_billing_entries", "hold_id", s.HoldID + 1, ErrInvariant},
		{"snapshot_mismatch", "video_billing_entries", "snapshot_id", s.ID + 1, ErrInvariant},
		{"settled_missing_usage", "video_billing_entries", "completion_tokens", nil, ErrInvariant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Table(tc.table).Where("org_id = ? AND task_id = ?", 10, s.TaskID).Update(tc.column, tc.value).Error; err != nil {
					return err
				}
				value, err := ReadInitialSettlementTx(tx, ref)
				if value.Kind() != "" || !errors.Is(err, tc.want) {
					t.Fatalf("invalid persisted fact accepted: kind=%s error=%v", value.Kind(), err)
				}
				return tc.want
			})
			if !errors.Is(err, tc.want) {
				t.Fatal("fixture rollback failed")
			}
		})
	}
	const callback = "initial_sql_fault"
	for stage := 1; stage <= 4; stage++ {
		seen := 0
		if err := db.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
			seen++
			if seen == stage {
				q.Statement.Selects = []string{"initial_reader_missing_column"}
			}
		}); err != nil {
			t.Fatal(err)
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			value, err := ReadInitialSettlementTx(tx, ref)
			if value.Kind() != "" {
				t.Fatal("SQL failure returned facts")
			}
			return err
		})
		var driver *mysqldriver.MySQLError
		if !errors.As(err, &driver) || driver.Number != 1054 || err.Error() != "video_billing: initial settlement read unavailable" || seen != stage {
			t.Fatal("driver failure was leaked or lost")
		}
		if err := db.Callback().Query().Remove(callback); err != nil {
			t.Fatal(err)
		}
	}
	// A real other-connection Key lock forces the reader to honor its caller's
	// deadline. No clock mutation, SQL SLEEP or fabricated driver error is used.
	holder := db.Begin()
	if holder.Error != nil {
		t.Fatal("cannot start real lock holder")
	}
	defer holder.Rollback()
	var key uint64
	if err := holder.Raw("SELECT id FROM api_keys WHERE id = 1 FOR UPDATE").Scan(&key).Error; err != nil {
		t.Fatal("cannot hold Key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		value, err := ReadInitialSettlementTx(tx, ref)
		if value.Kind() != "" {
			t.Fatal("timed-out read returned facts")
		}
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "video_billing: initial settlement read unavailable" {
		t.Fatal("caller deadline/cause was not preserved")
	}
}
