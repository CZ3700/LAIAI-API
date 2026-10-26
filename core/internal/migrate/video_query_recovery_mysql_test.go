package migrate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const queryRecoveryMigration = "0133_video_query_recovery_progress.sql"

// This is a schema/old-writer test, not a replacement for the video package's
// real admission/recovery/settlement and production-candidate EXPLAIN tests.
func TestMySQLVideoQueryRecoveryMigrationPreservesAndReplays(t *testing.T) {
	for completed := 0; completed <= 2; completed++ {
		t.Run(fmt.Sprintf("resume_after_%d_statements", completed), func(t *testing.T) {
			db := queryRecoveryMigrationDatabase(t)
			base, update := queryRecoveryMigrationFiles(t)
			if _, err := Up(db, base); err != nil {
				t.Fatal("tracked pre-0133 migrations failed")
			}
			seedRuntimeLegacyRows(t, db, "qrecovery")
			for index, code := range []string{"settlement_conflict", "SETTLEMENT_CONFLICT", "settlement_conflict ", "settlement_conflict"} {
				row := queryRecoveryOldRow(fmt.Sprintf("query-schema-%d", index), uint64(index+10), code)
				if index == 3 {
					row["applied_at"] = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				}
				if err := db.Table("video_query_receipts").Create(row).Error; err != nil {
					t.Fatal("old query receipt writer failed")
				}
			}
			tables := inboxBusinessTables(t, db)
			before, columns := snapshotInboxExistingTables(t, db, tables)
			indexes := queryRecoveryIndexes(t, db)
			for _, name := range []string{"tasks", "video_task_states", "video_operations", "video_query_receipts", "video_billing_snapshots", "ledger_accounts", "ledger_holds", "ledger_journal"} {
				if before[name].Count == 0 {
					t.Fatal("preservation fixture unexpectedly empty", name)
				}
			}
			statements := splitStatements(string(update[queryRecoveryMigration].Data))
			if len(statements) != 2 || !strings.HasPrefix(statements[0], "ALTER TABLE video_query_receipts") || !strings.HasPrefix(statements[1], "UPDATE video_query_receipts") {
				t.Fatal("expected one atomic expand ALTER and one new-column backfill")
			}
			lower := queryRecoveryDatabaseTime(t, db)
			for _, statement := range statements[:completed] {
				if err := db.Exec(statement).Error; err != nil {
					t.Fatal("partial migration statement failed")
				}
			}
			var recorded int64
			if err := db.Model(&Record{}).Where("version = ?", strings.TrimSuffix(queryRecoveryMigration, ".sql")).Count(&recorded).Error; err != nil || recorded != 0 {
				t.Fatal("partial migration was incorrectly recorded")
			}
			if ran, err := Up(db, update); err != nil || !reflect.DeepEqual(ran, []string{"0133_video_query_recovery_progress"}) {
				t.Fatal("query progress migration/replay failed")
			}
			upper := queryRecoveryDatabaseTime(t, db)
			if !reflect.DeepEqual(tables, inboxBusinessTables(t, db)) {
				t.Fatal("query progress changed business table inventory")
			}
			for name, old := range before {
				if got := snapshotRuntimeTable(t, db, name, old.Columns); !reflect.DeepEqual(got, old) {
					t.Fatal("migration changed pre-existing data", name)
				}
				got := inboxMigrationColumns(t, db, name)
				if name == "video_query_receipts" {
					if len(got) != len(columns[name])+2 {
						t.Fatal("query progress must add exactly two columns")
					}
					queryRecoveryAssertColumns(t, got[len(got)-2:])
					got = got[:len(got)-2]
				}
				if !reflect.DeepEqual(got, columns[name]) {
					t.Fatal("migration changed pre-existing schema columns", name)
				}
			}
			queryRecoveryAssertIndexes(t, db, indexes)
			var rows []video.QueryReceipt
			if err := db.Order("attempt_id").Find(&rows).Error; err != nil || len(rows) != 8 {
				t.Fatal("query fixture row count changed")
			}
			for _, row := range rows {
				if row.RecoveryVersion != 0 {
					t.Fatal("migration invented an application revision")
				}
				if row.AttemptID == "query-schema-0" {
					if row.RecoveryDueAt != nil || row.AppliedAt != nil {
						t.Fatal("exact old conflict did not acquire the NULL barrier")
					}
				} else {
					queryRecoveryAssertUTCWindow(t, row.RecoveryDueAt, lower, upper)
				}
			}
			queryRecoveryAssertWriters(t, db)

			// Simulate progress written by N after the DDL but before an old
			// migrator observes its version record. Replaying must not reset it.
			if err := db.Exec("UPDATE video_query_receipts SET recovery_version=7,recovery_due_at=DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 1 HOUR) WHERE attempt_id='query-schema-0'").Error; err != nil {
				t.Fatal("could not arrange already advanced progress")
			}
			progress := snapshotRuntimeTable(t, db, "video_query_receipts", nil)
			if err := db.Where("version = ?", strings.TrimSuffix(queryRecoveryMigration, ".sql")).Delete(&Record{}).Error; err != nil {
				t.Fatal("could not arrange missing migration record")
			}
			if ran, err := Up(db, update); err != nil || len(ran) != 1 {
				t.Fatal("progress-aware interrupted replay failed")
			}
			if !reflect.DeepEqual(progress, snapshotRuntimeTable(t, db, "video_query_receipts", nil)) {
				t.Fatal("interrupted replay overwrote completed or newer progress")
			}
			if ran, err := Up(db, update); err != nil || len(ran) != 0 {
				t.Fatal("recorded replay must be a no-op")
			}
			if drift, err := ledger.CheckDrift(db); err != nil || len(drift) != 0 {
				t.Fatal("schema upgrade changed ledger balance consistency")
			}
		})
	}
}

func queryRecoveryMigrationFiles(t *testing.T) (fstest.MapFS, fstest.MapFS) {
	t.Helper()
	source := os.DirFS("../../../migrations")
	names, err := fs.Glob(source, "*.sql")
	if err != nil {
		t.Fatal("cannot list tracked migrations")
	}
	base, update := fstest.MapFS{}, fstest.MapFS{}
	for _, name := range names {
		if name > queryRecoveryMigration {
			continue
		}
		raw, err := fs.ReadFile(source, name)
		if err != nil {
			t.Fatal("cannot read tracked migration")
		}
		file := &fstest.MapFile{Data: raw}
		if name == queryRecoveryMigration {
			update[name] = file
		} else {
			base[name] = file
		}
	}
	if len(base) != 132 || len(update) != 1 {
		t.Fatal("complete 0132 baseline and unique 0133 are required")
	}
	return base, update
}

func queryRecoveryMigrationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN=ark_test required for real query progress migration")
	}
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil || cfg.DBName != "ark_test" {
		t.Fatal("query progress migration requires isolated ark_test control database")
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 35*time.Second, 35*time.Second
	cfg.Logger = log.New(io.Discard, "", 0)
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	// Every connection deliberately has a non-UTC MySQL session. loc=UTC
	// only controls driver decoding and is not a substitute for this test.
	cfg.Params["time_zone"] = "'+08:00'"
	connector, err := drivermysql.NewConnector(cfg.Clone())
	if err != nil {
		t.Fatal("cannot create private control connector")
	}
	admin := sql.OpenDB(connector)
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("cannot allocate private schema identity")
	}
	name := "ark_test_qrecovery_" + hex.EncodeToString(nonce[:])
	var pool *sql.DB
	created := false
	t.Cleanup(func() {
		if pool != nil {
			_ = pool.Close()
		}
		if created {
			ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
				t.Error("could not remove this invocation's exact private schema")
			}
		}
		_ = admin.Close()
	})
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("cannot create private migration schema")
	}
	created = true
	cfg.DBName = name
	connector, err = drivermysql.NewConnector(cfg.Clone())
	if err != nil {
		t.Fatal("cannot create private business connector")
	}
	pool = sql.OpenDB(connector)
	pool.SetMaxOpenConns(6)
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot connect private migration schema")
	}
	var zone string
	if err := db.Raw("SELECT @@session.time_zone").Scan(&zone).Error; err != nil || zone != "+08:00" {
		t.Fatal("non-UTC session positive control failed")
	}
	return db
}

func queryRecoveryOldRow(attempt string, sequence uint64, code string) map[string]any {
	return map[string]any{"attempt_id": attempt, "org_id": 1, "task_id": "runtime-qrecovery-0", "sequence": sequence, "task_row_version": 7,
		"http_status": 200, "outcome": "accepted", "observation_json": `{"status":"queued"}`, "payload_ref": "legacy-evidence",
		"received_at": time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "apply_error_code": code}
}

func queryRecoveryDatabaseTime(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	var value time.Time
	if err := db.Raw("SELECT UTC_TIMESTAMP(3)").Scan(&value).Error; err != nil || value.IsZero() {
		t.Fatal("cannot read primary UTC clock")
	}
	return value
}

func queryRecoveryAssertUTCWindow(t *testing.T, due *time.Time, lower, upper time.Time) {
	t.Helper()
	if due == nil || due.Before(lower) || due.After(upper) {
		t.Fatal("database default is NULL, host time, or non-UTC session time")
	}
}

func queryRecoveryAssertColumns(t *testing.T, columns []inboxMigrationColumn) {
	t.Helper()
	due, revision := columns[0], columns[1]
	defaultText := strings.ToLower(due.ColumnDefault.String)
	if due.ColumnName != "recovery_due_at" || due.ColumnType != "datetime(3)" || due.IsNullable != "YES" || !due.ColumnDefault.Valid || (defaultText != "utc_timestamp(3)" && defaultText != "(utc_timestamp(3))") || due.Extra != "DEFAULT_GENERATED" || due.GenerationExpression != "" || due.CollationName.Valid {
		t.Fatal("nullable UTC expression default schema mismatch")
	}
	if revision.ColumnName != "recovery_version" || revision.ColumnType != "bigint unsigned" || revision.IsNullable != "NO" || revision.ColumnDefault.String != "0" || !revision.ColumnDefault.Valid || revision.Extra != "" || revision.GenerationExpression != "" || revision.CollationName.Valid {
		t.Fatal("unsigned progress version schema mismatch")
	}
}

type queryRecoveryIndex struct {
	TableName, IndexName, ColumnList string
	NonUnique                        int
}

func queryRecoveryIndexes(t *testing.T, db *gorm.DB) []queryRecoveryIndex {
	t.Helper()
	var indexes []queryRecoveryIndex
	if err := db.Raw("SELECT table_name,index_name,non_unique,GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',') AS column_list FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name<>'schema_migrations' GROUP BY table_name,index_name,non_unique ORDER BY table_name,index_name").Scan(&indexes).Error; err != nil {
		t.Fatal("cannot inspect exact index metadata")
	}
	return indexes
}

func queryRecoveryAssertIndexes(t *testing.T, db *gorm.DB, old []queryRecoveryIndex) {
	t.Helper()
	var retained []queryRecoveryIndex
	added := 0
	for _, index := range queryRecoveryIndexes(t, db) {
		if index.TableName == "video_query_receipts" && index.IndexName == "idx_video_query_recovery_due" {
			added++
			if index.NonUnique != 1 || index.ColumnList != "runtime_version,adapter_version,protocol_contract_version,observation_schema_version,applied_at,recovery_due_at,received_at,attempt_id" {
				t.Fatal("query recovery index is not the exact candidate prefix")
			}
			continue
		}
		retained = append(retained, index)
	}
	if added != 1 || !reflect.DeepEqual(retained, old) {
		t.Fatal("query progress removed or changed an old index")
	}
}

func queryRecoveryAssertWriters(t *testing.T, db *gorm.DB) {
	t.Helper()
	var inserts []map[string]bool
	invalidCapture := false
	const hook = "test:query-recovery-insert-columns"
	if err := db.Callback().Create().After("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table != "video_query_receipts" {
			return
		}
		values, ok := tx.Statement.Clauses["VALUES"].Expression.(clause.Values)
		if !ok || tx.Statement.SQL.Len() == 0 {
			invalidCapture = true
			return
		}
		columns := map[string]bool{}
		for _, column := range values.Columns {
			columns[column.Name] = true
		}
		inserts = append(inserts, columns)
	}); err != nil {
		t.Fatal("cannot observe actual ORM insert columns")
	}
	defer db.Callback().Create().Remove(hook)
	lower := queryRecoveryDatabaseTime(t, db)
	if err := db.Table("video_query_receipts").Create(queryRecoveryOldRow("query-old-writer", 20, "")).Error; err != nil {
		t.Fatal("old writer omitting both new columns failed")
	}
	// These use the actual current model. The ordinary ORM zero value must
	// omit the default column; the actual producer additionally omits both.
	for index, explicitOmit := range []bool{false, true} {
		row := video.QueryReceipt{AttemptID: fmt.Sprintf("query-new-orm-%d", index), OrgID: 1, TaskID: "runtime-qrecovery-0", Sequence: uint64(21 + index), TaskRowVersion: 7,
			RuntimeVersion: videoruntime.RuntimeV1, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, ObservationSchemaVersion: 1,
			HTTPStatus: 200, Outcome: "accepted", ObservationJSON: `{}`, PayloadRef: "evidence", ReceivedAt: lower}
		writer := db
		if explicitOmit {
			writer = db.Omit("RecoveryDueAt", "RecoveryVersion")
		}
		if err := writer.Create(&row).Error; err != nil {
			t.Fatal("current ORM receipt writer failed")
		}
	}
	if invalidCapture || len(inserts) != 3 {
		t.Fatal("actual INSERT observation did not cover all three writers")
	}
	for index, columns := range inserts {
		if !columns["attempt_id"] || columns["recovery_due_at"] || columns["recovery_version"] != (index == 1) {
			t.Fatal("INSERT did not omit DB default columns as required")
		}
	}
	if err := db.Callback().Create().Remove(hook); err != nil {
		t.Fatal("cannot remove private insert observation")
	}
	upper := queryRecoveryDatabaseTime(t, db)
	for _, attempt := range []string{"query-old-writer", "query-new-orm-0", "query-new-orm-1"} {
		var row video.QueryReceipt
		if err := db.Where("attempt_id = ?", attempt).Take(&row).Error; err != nil {
			t.Fatal("cannot reread a real receipt insert")
		}
		queryRecoveryAssertUTCWindow(t, row.RecoveryDueAt, lower, upper)
		if row.RecoveryVersion != 0 {
			t.Fatal("fresh writer invented a progress revision")
		}
	}
	// Explicit SQL NULL has a different meaning than omission and must remain
	// a barrier across a real old UPDATE that knows none of the new columns.
	nullRow := queryRecoveryOldRow("query-explicit-null", 23, "query_observation_invalid")
	nullRow["recovery_due_at"] = nil
	if err := db.Table("video_query_receipts").Create(nullRow).Error; err != nil {
		t.Fatal("explicit NULL writer failed")
	}
	var before video.QueryReceipt
	if err := db.Where("attempt_id='query-explicit-null'").Take(&before).Error; err != nil || before.RecoveryDueAt != nil {
		t.Fatal("explicit NULL was incorrectly replaced by a default")
	}
	if err := db.Exec("UPDATE video_query_receipts SET apply_error_code='query_apply_failed' WHERE attempt_id='query-explicit-null'").Error; err != nil {
		t.Fatal("old-column-only UPDATE failed")
	}
	var after video.QueryReceipt
	if err := db.Where("attempt_id='query-explicit-null'").Take(&after).Error; err != nil {
		t.Fatal("cannot read old UPDATE")
	}
	after.ApplyErrorCode = before.ApplyErrorCode
	if !reflect.DeepEqual(after, before) {
		t.Fatal("old UPDATE cleared the barrier or changed durable evidence")
	}
}
