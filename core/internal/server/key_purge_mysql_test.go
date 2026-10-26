package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func keyPurgeMySQLDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	return keyPurgeMySQLAtVersion(t, 131)
}

func keyPurgeMySQLAtVersion(t *testing.T, version int) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN=ark_test required for real Key retention acceptance")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil || cfg.DBName != "ark_test" {
		t.Fatal("Key retention requires isolated ark_test database")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 35 * time.Second
	cfg.WriteTimeout = 35 * time.Second
	cfg.Logger = log.New(io.Discard, "", 0)
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal("cannot allocate owned schema")
	}
	name := "ark_test_keypurge_" + hex.EncodeToString(nonce[:])
	if !regexp.MustCompile(`^ark_test_keypurge_[0-9a-f]{24}$`).MatchString(name) {
		t.Fatal("invalid owned schema")
	}
	connector, err := mysqldriver.NewConnector(cfg.Clone())
	if err != nil {
		t.Fatal("cannot open schema administration")
	}
	admin := sql.OpenDB(connector)
	var pool *sql.DB
	created := false
	t.Cleanup(func() {
		if pool != nil {
			_ = pool.Close()
		}
		if created {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, e := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); e != nil {
				t.Error("cannot remove exact owned Key test schema")
			}
		}
		_ = admin.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("cannot create owned schema")
	}
	created = true
	cfg.DBName = name
	cfg.ReadTimeout = 5 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	connector, err = mysqldriver.NewConnector(cfg.Clone())
	if err != nil {
		t.Fatal("cannot configure owned business pool")
	}
	pool = sql.OpenDB(connector)
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot connect owned schema")
	}
	pool, err = db.DB()
	if err != nil {
		t.Fatal("cannot obtain owned pool")
	}
	pool.SetMaxOpenConns(8)
	pool.SetMaxIdleConns(8)
	files := keyPurgeMigrationFiles(t, version)
	if _, err = migrate.Up(db, files); err != nil {
		t.Fatal("tracked migration failed")
	}
	if ran, e := migrate.Up(db, files); e != nil || len(ran) != 0 {
		t.Fatal("tracked migration replay failed")
	}
	db, err = ledger.EnableActivityTracking(db)
	if err != nil {
		t.Fatal("cannot enable real ledger evidence")
	}
	return db
}

var keyPurgeAt = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

func keyPurgeBilling(t *testing.T, db *gorm.DB) videobilling.QuoteResult {
	t.Helper()
	rule := videobilling.PricingRule{RuleID: "key-retention-rule", Version: "v1", PricingKey: videobilling.PricingKey{ProviderScopeID: 11, Model: "seedance-2.0", Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.RequireFromString("8"), SupplierCNYPer1M: decimal.RequireFromString("46"), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "formula-v1", SourceHash: strings.Repeat("a", 64), EffectiveFrom: keyPurgeAt.Add(-time.Hour), Enabled: true}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal("cannot seed legal pricing")
	}
	limit := decimal.NewFromInt(100)
	policies := []videobilling.QuotaPolicy{
		{OrgID: 1, Scope: videobilling.ScopeOrg, OwnerID: 1, Metric: videobilling.MetricUSD, Period: videobilling.PeriodLifetime, Timezone: "UTC", LimitAmount: &limit, Version: "v1", Enabled: true},
		{OrgID: 0, Scope: videobilling.ScopeProvider, OwnerID: 11, Metric: videobilling.MetricCNY, Period: videobilling.PeriodLifetime, Timezone: "UTC", LimitAmount: &limit, Version: "v1", Enabled: true},
	}
	if err := db.Create(&policies).Error; err != nil {
		t.Fatal("cannot seed explicit budget policy fixtures")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		a, e := ledger.EnsureAccount(tx, 1, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		_, _, e = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "key-retention-credit", AccountID: a.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(10), RefType: "test", RefID: "key-retention"})
		return e
	})
	if err != nil {
		t.Fatal("cannot fund through ledger")
	}
	q, err := videobilling.Quote(rule, videobilling.MeteringInput{OutputDurationMS: 5000, Width: 1280, Height: 720, FPSNumerator: 24, FPSDenominator: 1, BoundsVerified: true, CapabilityVersion: "cap-v1", EffectiveRatio: decimal.NewFromInt(1), FXUSDPerCNY: decimal.RequireFromString("0.14"), FXVersion: "fx-v1", FXObservedAt: keyPurgeAt})
	if err != nil || q.HoldUSD.String() != "0.864" {
		t.Fatal("unexpected independent fixture hold")
	}
	return q
}

func keyPurgeReserve(tx *gorm.DB, q videobilling.QuoteResult, taskID string) (videobilling.ReservationResult, error) {
	return keyPurgeReserveKey(tx, q, taskID, 1)
}

func keyPurgeReserveKey(tx *gorm.DB, q videobilling.QuoteResult, taskID string, keyID uint64) (videobilling.ReservationResult, error) {
	return videobilling.ReserveTx(tx, videobilling.ReserveInput{OrgID: 1, APIKeyID: keyID, TaskID: taskID, RequestHMAC: strings.Repeat("a", 64), BillingSchemaVersion: videoruntime.BillingV1, Quote: q, Now: keyPurgeAt})
}

func keyPurgeSettle(db *gorm.DB, id string, tokens *int64) (out videobilling.SettlementResult, err error) {
	version := "v2"
	if tokens == nil {
		version = "v1"
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var e error
		out, e = videobilling.SettleUsageTx(tx, videobilling.SettlementInput{OrgID: 1, TaskID: id, Now: keyPurgeAt, Evidence: videobilling.UsageEvidence{EvidenceID: "usage-" + id + "-" + version, Source: "task_get", SourceRecordID: id, SourceVersion: version, CompletionTokens: tokens, ObservedAt: keyPurgeAt}})
		return e
	})
	return
}

var keyPurgeFinancialTables = []string{"api_keys", "ledger_accounts", "ledger_holds", "ledger_journal", "ledger_movements", "ledger_operations", "ledger_operation_events", "video_billing_snapshots", "video_billing_entries", "video_usage_evidence", "video_quota_reservations", "video_quota_buckets", "video_quota_policies", "video_adjustment_applications", "video_pricing_rules", "tasks", "invocation_payloads"}

func keyPurgeUnchanged(t *testing.T, f *keyPurgeHTTPFixture) {
	t.Helper()
	keyPurgeUnchangedForKey(t, f, 1)
}

func keyPurgeUnchangedForKey(t *testing.T, f *keyPurgeHTTPFixture, keyID uint64) {
	t.Helper()
	before := routingAdminFingerprints(t, f.db, keyPurgeFinancialTables...)
	keyPurgeStatus(t, f.request("DELETE", keyID, true), 409)
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, keyPurgeFinancialTables...)) {
		t.Fatal("purge refusal changed original financial or input facts")
	}
}

func TestMySQLKeyPurgeAllHistoricalStatesPreserveBilling(t *testing.T) {
	db := keyPurgeMySQLDatabase(t)
	f := keyPurgeHTTP(t, db)
	keyPurgeInsertKey(t, db, 1, 1, true)
	q := keyPurgeBilling(t, db)
	if err := db.Transaction(func(tx *gorm.DB) error { _, e := keyPurgeReserve(tx, q, "task-retained"); return e }); err != nil {
		t.Fatal("real reservation failed")
	}
	keyPurgeStatus(t, f.request("DELETE", 1, false), 200)
	keyPurgeUnchanged(t, f)
	if out, err := keyPurgeSettle(db, "task-retained", nil); err != nil || out.State != "pending_usage" {
		t.Fatal("missing usage was not retained")
	}
	keyPurgeUnchanged(t, f)
	tokens := int64(54000)
	if out, err := keyPurgeSettle(db, "task-retained", &tokens); err != nil || out.Entry == nil || out.Entry.CapturedUSD.String() != "0.432" {
		t.Fatal("revoked retained Key could not settle exactly")
	}
	keyPurgeUnchanged(t, f)
	before := routingAdminFingerprints(t, db, keyPurgeFinancialTables...)
	if out, err := keyPurgeSettle(db, "task-retained", &tokens); err != nil || !out.Replay {
		t.Fatal("settled history could not replay")
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, db, keyPurgeFinancialTables...)) {
		t.Fatal("settlement replay changed money")
	}
	// This Key has only a released snapshot; another settled reference must
	// not mask a regression that incorrectly permits purging release history.
	keyPurgeInsertKey(t, db, 2, 1, true)
	if err := db.Transaction(func(tx *gorm.DB) error { _, e := keyPurgeReserveKey(tx, q, "task-release", 2); return e }); err != nil {
		t.Fatal("release fixture reservation failed")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := videobilling.ReleaseConfirmedTx(tx, videobilling.ReleaseInput{OrgID: 1, TaskID: "task-release", Reason: videobilling.ReleaseNotDispatched, Confirmed: true, EvidenceID: "release-evidence", Now: keyPurgeAt})
		return e
	}); err != nil {
		t.Fatal("real confirmed release failed")
	}
	keyPurgeStatus(t, f.request("DELETE", 2, false), 200)
	keyPurgeUnchangedForKey(t, f, 2)
	keyPurgeReviewedCorrection(t, db)
	keyPurgeUnchanged(t, f)
	posted, held, err := ledger.OrgBalances(db, 1, ledger.AccountUserCredit)
	if err != nil || posted.String() != "9.784" || !held.IsZero() {
		t.Fatal("retained historical Key adjustment changed exact expected balances")
	}
	var key struct{ UsedAmount decimal.Decimal }
	if err := db.Table("api_keys").Select("used_amount").Where("id=1").Take(&key).Error; err != nil || key.UsedAmount.String() != "0.216" {
		t.Fatal("historical adjustment lost original Key usage")
	}
	var buckets []videobilling.QuotaBucket
	if err := db.Order("org_id").Find(&buckets).Error; err != nil || len(buckets) != 2 || buckets[0].OrgID != 0 || buckets[1].OrgID != 1 || buckets[0].ConsumedAmount.String() != "1.242" || buckets[1].ConsumedAmount.String() != "0.216" || !buckets[0].ReservedAmount.IsZero() || !buckets[1].ReservedAmount.IsZero() {
		t.Fatal("historical correction lost provider or organization budget facts")
	}
	if drift, e := ledger.CheckDrift(db); e != nil || len(drift) != 0 {
		t.Fatal("financial drift after retained Key history")
	}
}

func keyPurgeReviewedCorrection(t *testing.T, db *gorm.DB) {
	t.Helper()
	tokens := int64(27000)
	var imp videobilling.BeginImportResult
	var line videobilling.ImportLineResult
	var proposal videobilling.AdjustmentProposal
	steps := []func(*gorm.DB) error{
		func(tx *gorm.DB) error {
			var e error
			imp, e = videobilling.BeginImportTx(tx, videobilling.BeginImportInput{ImportKey: "retention-import", ProviderScopeID: 11, Source: "official-bill", SourceVersion: "v1", SourceHash: strings.Repeat("b", 64), PayloadRef: "payload-bill", ExpectedRows: 1, Now: keyPurgeAt})
			return e
		},
		func(tx *gorm.DB) error {
			var e error
			line, e = videobilling.ImportLineTx(tx, videobilling.ImportLineInput{ImportID: imp.Import.ID, RowNumber: 1, Line: videobilling.BillLine{ProviderScopeID: 11, Source: "official-bill", SourceRecordID: "record-retained", SourceVersion: "v1", SourceRevision: 1, EvidenceHash: strings.Repeat("b", 64), PayloadRef: "payload-line", Granularity: videobilling.BillTaskTotal, UpstreamTaskID: "upstream-retained", Model: "seedance-2.0", PeriodStart: keyPurgeAt.Add(-time.Hour), PeriodEnd: keyPurgeAt, CompletionTokens: &tokens, AmountCNY: decimal.RequireFromString("1.242"), ObservedAt: keyPurgeAt}, Binding: &videobilling.VerifiedBinding{ProviderScopeID: 11, UpstreamTaskID: "upstream-retained", OrgID: 1, TaskID: "task-retained", BindingVersion: "binding-v1"}, Now: keyPurgeAt})
			return e
		},
		func(tx *gorm.DB) error {
			var e error
			proposal, e = videobilling.ProposeAdjustmentTx(tx, videobilling.ProposeAdjustmentInput{OrgID: 1, TaskID: "task-retained", ProposalKey: "retained-correction", LineID: line.Line.ID, ProposerID: 100, Reason: "verified_bill", Now: keyPurgeAt, ExpiresAt: keyPurgeAt.Add(time.Hour)})
			return e
		},
		func(tx *gorm.DB) error {
			_, e := videobilling.ReviewAdjustmentTx(tx, videobilling.ReviewAdjustmentInput{OrgID: 1, ProposalID: proposal.ID, ReviewerID: 200, Approved: true, Reason: "verified_bill", Now: keyPurgeAt})
			return e
		},
	}
	for i, step := range steps {
		if err := db.Transaction(step); err != nil {
			t.Fatalf("real historical correction stage %d failed: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		before := routingAdminFingerprints(t, db, keyPurgeFinancialTables...)
		var out videobilling.AdjustmentResult
		err := db.Transaction(func(tx *gorm.DB) error {
			var e error
			out, e = videobilling.ApplyAdjustmentTx(tx, videobilling.ApplyAdjustmentInput{OrgID: 1, ProposalID: proposal.ID, Now: keyPurgeAt})
			return e
		})
		if err != nil || out.Replay != (i == 1) || out.Application.NetUSD.String() != "0.216" {
			t.Fatal("reviewed correction or replay failed after denied purge")
		}
		if i == 1 && !reflect.DeepEqual(before, routingAdminFingerprints(t, db, keyPurgeFinancialTables...)) {
			t.Fatal("reviewed correction replay changed original financial facts")
		}
	}
}

func keyPurgeConnection(t *testing.T, tx *gorm.DB) uint64 {
	t.Helper()
	var id uint64
	if err := tx.Raw("SELECT CONNECTION_ID()").Scan(&id).Error; err != nil || id == 0 {
		t.Fatal("cannot observe actual connection")
	}
	return id
}

func keyPurgeWaitBlocked(t *testing.T, db *gorm.DB, requester, blocker uint64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		var n int64
		err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.threads r ON r.THREAD_ID=w.REQUESTING_THREAD_ID JOIN performance_schema.threads b ON b.THREAD_ID=w.BLOCKING_THREAD_ID WHERE r.PROCESSLIST_ID=? AND b.PROCESSLIST_ID=?", requester, blocker).Scan(&n).Error
		if err != nil {
			t.Fatal("cannot prove exact cross-connection lock wait")
		}
		if n > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("expected actual Key lock wait did not occur")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestMySQLKeyPurgeSerializesActualReservation(t *testing.T) {
	for _, reserveFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("reserve_first_%t", reserveFirst), func(t *testing.T) {
			db := keyPurgeMySQLDatabase(t)
			keyPurgeHTTP(t, db)
			keyPurgeInsertKey(t, db, 1, 1, false)
			q := keyPurgeBilling(t, db)
			// A previously authenticated request can reach ReserveTx after revocation;
			// this is the existing real billing API, not an inserted fake snapshot.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first := db.WithContext(ctx).Begin()
			if first.Error != nil {
				t.Fatal("cannot begin first actor")
			}
			defer first.Rollback()
			firstID := keyPurgeConnection(t, first)
			if reserveFirst {
				if _, err := keyPurgeReserve(first, q, "task-race"); err != nil {
					t.Fatal("real reservation actor failed")
				}
			} else {
				if err := identity.PurgeRevokedKeyTx(first, 1, 1, videobilling.HasKeySnapshotReferenceTx); err != nil {
					t.Fatal("real purge actor failed")
				}
			}
			idCh := make(chan uint64, 1)
			result := make(chan error, 1)
			go func() {
				result <- db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var id uint64
					if err := tx.Raw("SELECT CONNECTION_ID()").Scan(&id).Error; err != nil {
						return err
					}
					idCh <- id
					if reserveFirst {
						return identity.PurgeRevokedKeyTx(tx, 1, 1, videobilling.HasKeySnapshotReferenceTx)
					}
					_, err := keyPurgeReserve(tx, q, "task-race")
					return err
				})
			}()
			joined := false
			t.Cleanup(func() {
				cancel()
				_ = first.Rollback().Error
				if !joined {
					select {
					case <-result:
					case <-time.After(12 * time.Second):
						t.Error("concurrent actor did not stop")
					}
				}
			})
			var secondID uint64
			select {
			case secondID = <-idCh:
			case err := <-result:
				joined = true
				t.Fatalf("second actor failed before lock: %v", err)
			case <-ctx.Done():
				t.Fatal("second actor did not start")
			}
			if firstID == secondID {
				t.Fatal("race reused one connection")
			}
			keyPurgeWaitBlocked(t, db, secondID, firstID)
			if err := first.Commit().Error; err != nil {
				t.Fatal("first actor could not commit")
			}
			var secondErr error
			select {
			case secondErr = <-result:
				joined = true
			case <-ctx.Done():
				t.Fatal("second actor did not complete")
			}
			if reserveFirst && !errors.Is(secondErr, identity.ErrKeyPurgeReferenced) {
				t.Fatal("purge ignored committed reservation")
			}
			if !reserveFirst && !errors.Is(secondErr, videobilling.ErrNotFound) {
				t.Fatal("reservation survived missing Key")
			}
			var keys, snapshots, holds int64
			for table, dest := range map[string]*int64{"api_keys": &keys, "video_billing_snapshots": &snapshots, "ledger_holds": &holds} {
				if err := db.Table(table).Count(dest).Error; err != nil {
					t.Fatal("cannot inspect race result")
				}
			}
			want := int64(0)
			if reserveFirst {
				want = 1
			}
			if keys != want || snapshots != want || holds != want {
				t.Fatal("purge/admission race left partial facts")
			}
			if drift, err := ledger.CheckDrift(db); err != nil || len(drift) != 0 {
				t.Fatal("race changed ledger invariants")
			}
		})
	}
}

func TestMySQLKeyPurgeCurrentReadAndBoundedIndex(t *testing.T) {
	db := keyPurgeMySQLDatabase(t)
	f := keyPurgeHTTP(t, db)
	keyPurgeInsertKey(t, db, 1, 1, false)
	keyPurgeInsertKey(t, db, 2, 1, false)
	q := keyPurgeBilling(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal("cannot start RR")
	}
	defer tx.Rollback()
	var n int64
	if err := tx.Table("video_billing_snapshots").Count(&n).Error; err != nil || n != 0 {
		t.Fatal("cannot establish old snapshot")
	}
	if err := db.Transaction(func(writer *gorm.DB) error { _, e := keyPurgeReserve(writer, q, "task-current-read"); return e }); err != nil {
		t.Fatal("independent real reserve failed")
	}
	// Selectivity-only imported history; these copies are never presented as
	// real admissions or financial movements. The target reference above came
	// from the actual ReserveTx on a separate committed connection.
	var original videobilling.Snapshot
	if err := db.Where("org_id=1 AND task_id=?", "task-current-read").Take(&original).Error; err != nil {
		t.Fatal("cannot obtain committed index fixture")
	}
	background := make([]videobilling.Snapshot, 512)
	for i := range background {
		background[i] = original
		background[i].ID = 0
		background[i].APIKeyID = uint64(i + 100)
		background[i].TaskID = fmt.Sprintf("imported-index-history-%d", i)
		background[i].HoldID = uint64(i + 10000)
	}
	if err := db.CreateInBatches(background, 128).Error; err != nil {
		t.Fatal("cannot populate selective index history")
	}
	if err := tx.Table("video_billing_snapshots").Count(&n).Error; err != nil || n != 0 {
		t.Fatal("RR positive control did not keep old snapshot")
	}
	const observer = "key_purge_index_observer"
	type observedQuery struct {
		sql  string
		args []any
	}
	var queries []observedQuery
	if err := db.Callback().Query().After("gorm:query").Register(observer, func(q *gorm.DB) {
		if q.Statement.Table == "video_billing_snapshots" {
			queries = append(queries, observedQuery{q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...)})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(observer) })
	if err := identity.PurgeRevokedKeyTx(tx, 1, 1, videobilling.HasKeySnapshotReferenceTx); !errors.Is(err, identity.ErrKeyPurgeReferenced) {
		t.Fatal("current reference lookup reused old RR snapshot")
	}
	keyPurgeOtherSnapshotLock(t, db, tx)
	if len(queries) != 1 {
		t.Fatal("unexpected referenced guard query count")
	}
	_ = tx.Rollback().Error
	tx = db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal("cannot start missing-reference actor")
	}
	defer tx.Rollback()
	if err := identity.PurgeRevokedKeyTx(tx, 1, 2, videobilling.HasKeySnapshotReferenceTx); err != nil {
		t.Fatal("missing-reference purge did not proceed")
	}
	keyPurgeOtherSnapshotLock(t, db, tx)
	_ = db.Callback().Query().Remove(observer)
	_ = tx.Rollback().Error
	if len(queries) != 2 {
		t.Fatal("hit/miss guard queries were not both observed")
	}
	for _, query := range queries {
		if !strings.Contains(query.sql, "FOR SHARE") || !strings.Contains(query.sql, "LIMIT") || strings.Contains(query.sql, "COUNT(") {
			t.Fatal("reference read lost bounded current semantics")
		}
		var plans []struct {
			Table, Type string
			Key         sql.NullString
		}
		if err := db.WithContext(ctx).Raw("EXPLAIN "+query.sql, query.args...).Scan(&plans).Error; err != nil || len(plans) != 1 || !plans[0].Key.Valid || plans[0].Key.String != "idx_video_snapshot_key" || plans[0].Type == "ALL" {
			t.Fatal("actual hit/miss reference query did not use Key prefix index")
		}
		t.Logf("reference index plan: table=%s type=%s key=%s", plans[0].Table, plans[0].Type, plans[0].Key.String)
	}
	var columns []struct {
		ColumnName string
		SeqInIndex int
	}
	if err := db.Raw("SELECT COLUMN_NAME AS column_name,SEQ_IN_INDEX AS seq_in_index FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='video_billing_snapshots' AND index_name='idx_video_snapshot_key' ORDER BY SEQ_IN_INDEX").Scan(&columns).Error; err != nil || len(columns) != 3 || columns[0].ColumnName != "org_id" || columns[1].ColumnName != "api_key_id" || columns[2].ColumnName != "id" {
		t.Fatal("tracked reference index mismatch")
	}
	keyPurgeUnchanged(t, f)
}

// This proves a selective lookup does not lock every existing reference in the
// Org. It intentionally says nothing about inserts into adjacent InnoDB gaps.
func keyPurgeOtherSnapshotLock(t *testing.T, db, owner *gorm.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	other := db.WithContext(ctx).Begin()
	if other.Error != nil {
		t.Fatal("cannot begin independent lock observer")
	}
	defer other.Rollback()
	if keyPurgeConnection(t, owner) == keyPurgeConnection(t, other) {
		t.Fatal("lock scope observer reused owner connection")
	}
	var row struct{ ID uint64 }
	if err := other.Raw("SELECT id FROM video_billing_snapshots WHERE org_id=1 AND api_key_id=500 FOR UPDATE NOWAIT").Scan(&row).Error; err != nil || row.ID == 0 {
		t.Fatal("target reference lookup blocked a different existing Key reference")
	}
}

func keyPurgeMigrationFiles(t *testing.T, version int) fstest.MapFS {
	t.Helper()
	if version != 130 && version != 131 {
		t.Fatal("invalid fixed migration baseline")
	}
	source := os.DirFS("../../../migrations")
	names, err := fs.Glob(source, "*.sql")
	if err != nil {
		t.Fatal("cannot enumerate tracked migrations")
	}
	files := fstest.MapFS{}
	for _, name := range names {
		if len(name) < 4 || name[:4] > fmt.Sprintf("%04d", version) {
			continue
		}
		raw, e := fs.ReadFile(source, name)
		if e != nil {
			t.Fatal("cannot read tracked migration")
		}
		files[name] = &fstest.MapFile{Data: raw, Mode: 0600}
	}
	if len(files) != version {
		t.Fatal("unexpected tracked migration count")
	}
	return files
}

type keyPurgeColumnFact struct {
	TableName, ColumnName, ColumnType, IsNullable, Extra string
	ColumnDefault, CollationName                         sql.NullString
	OrdinalPosition                                      int
}
type keyPurgeIndexFact struct {
	TableName, IndexName, ColumnName, IndexType string
	NonUnique, SeqInIndex                       int
	SubPart                                     sql.NullInt64
}

func keyPurgeSchemaFacts(t *testing.T, db *gorm.DB) ([]keyPurgeColumnFact, []keyPurgeIndexFact) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var cols []keyPurgeColumnFact
	var indexes []keyPurgeIndexFact
	if err := db.WithContext(ctx).Raw("SELECT TABLE_NAME AS table_name,COLUMN_NAME AS column_name,COLUMN_TYPE AS column_type,IS_NULLABLE AS is_nullable,COLUMN_DEFAULT AS column_default,COLLATION_NAME AS collation_name,ORDINAL_POSITION AS ordinal_position,EXTRA AS extra FROM information_schema.columns WHERE table_schema=DATABASE() ORDER BY TABLE_NAME,ORDINAL_POSITION").Scan(&cols).Error; err != nil || len(cols) == 0 {
		t.Fatal("cannot inspect original columns")
	}
	if err := db.WithContext(ctx).Raw("SELECT TABLE_NAME AS table_name,INDEX_NAME AS index_name,COLUMN_NAME AS column_name,INDEX_TYPE AS index_type,NON_UNIQUE AS non_unique,SEQ_IN_INDEX AS seq_in_index,SUB_PART AS sub_part FROM information_schema.statistics WHERE table_schema=DATABASE() ORDER BY TABLE_NAME,INDEX_NAME,SEQ_IN_INDEX").Scan(&indexes).Error; err != nil || len(indexes) == 0 {
		t.Fatal("cannot inspect original indexes")
	}
	for _, col := range cols {
		if col.TableName == "" || col.ColumnName == "" || col.ColumnType == "" || col.OrdinalPosition <= 0 {
			t.Fatal("column observation was not decoded")
		}
	}
	for _, idx := range indexes {
		if idx.TableName == "" || idx.IndexName == "" || idx.ColumnName == "" || idx.SeqInIndex <= 0 {
			t.Fatal("index observation was not decoded")
		}
	}
	return cols, indexes
}

func TestMySQLKeySnapshotIndexMigrationPreservesOldWriters(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("alter_already_applied_%t", partial), func(t *testing.T) {
			db := keyPurgeMySQLAtVersion(t, 130)
			keyPurgeHTTP(t, db)
			keyPurgeInsertKey(t, db, 1, 1, false)
			q := keyPurgeBilling(t, db)
			if err := db.Transaction(func(tx *gorm.DB) error { _, e := keyPurgeReserve(tx, q, "before-index"); return e }); err != nil {
				t.Fatal("baseline writer reservation failed")
			}
			before := routingAdminFingerprints(t, db, keyPurgeFinancialTables...)
			oldCols, oldIndexes := keyPurgeSchemaFacts(t, db)
			files := keyPurgeMigrationFiles(t, 131)
			const filename = "0131_video_snapshot_key_reference_index.sql"
			if partial {
				// Real MySQL DDL commit without schema_migrations bookkeeping: the
				// interruption boundary is immediately after this sole ALTER.
				if err := db.Exec(string(files[filename].Data)).Error; err != nil {
					t.Fatal("cannot establish completed ALTER interruption")
				}
				var n int64
				if err := db.Table("schema_migrations").Where("version=?", strings.TrimSuffix(filename, ".sql")).Count(&n).Error; err != nil || n != 0 {
					t.Fatal("interruption already recorded migration")
				}
			}
			ran, err := migrate.Up(db, files)
			if err != nil || !reflect.DeepEqual(ran, []string{strings.TrimSuffix(filename, ".sql")}) {
				t.Fatal("index migration did not resume exactly once")
			}
			if again, e := migrate.Up(db, files); e != nil || len(again) != 0 {
				t.Fatal("index migration replay was not empty")
			}
			if !reflect.DeepEqual(before, routingAdminFingerprints(t, db, keyPurgeFinancialTables...)) {
				t.Fatal("index migration changed original data or money")
			}
			cols, indexes := keyPurgeSchemaFacts(t, db)
			if !reflect.DeepEqual(cols, oldCols) {
				t.Fatal("index migration changed original column schema")
			}
			var kept, added []keyPurgeIndexFact
			for _, idx := range indexes {
				if idx.TableName == "video_billing_snapshots" && idx.IndexName == "idx_video_snapshot_key" {
					added = append(added, idx)
				} else {
					kept = append(kept, idx)
				}
			}
			if !reflect.DeepEqual(kept, oldIndexes) || len(added) != 3 {
				t.Fatal("index migration altered unrelated indexes")
			}
			for i, name := range []string{"org_id", "api_key_id", "id"} {
				if added[i].ColumnName != name || added[i].SeqInIndex != i+1 || added[i].NonUnique != 1 || added[i].IndexType != "BTREE" || added[i].SubPart.Valid {
					t.Fatal("reference index is not the full expected prefix")
				}
			}
			// ReserveTx is unchanged by this slice: the same original INSERT column
			// set must remain accepted after the index-only migration.
			if err := db.Transaction(func(tx *gorm.DB) error { _, e := keyPurgeReserve(tx, q, "after-index"); return e }); err != nil {
				t.Fatal("original reservation writer broke after index migration")
			}
			posted, held, err := ledger.OrgBalances(db, 1, ledger.AccountUserCredit)
			if err != nil || posted.String() != "10" || held.String() != "1.728" {
				t.Fatal("old writer financial result differs after migration")
			}
			if drift, e := ledger.CheckDrift(db); e != nil || len(drift) != 0 {
				t.Fatal("index migration introduced ledger drift")
			}
		})
	}
}
