package migrate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Real-engine coverage is deliberately separate from the synthetic SQL-driver
// suite. No environment means SKIP, never a claim of migration acceptance.
func TestMySQLVideoQuerySettlementShapeMigrationGuard(t *testing.T) {
	if os.Getenv("ARK_TEST_MYSQL_DSN") == "" {
		t.Skip("ARK_TEST_MYSQL_DSN=ark_test required; real 0135 schema guard migration coverage not executed")
	}
	for _, tc := range []struct {
		name                string
		prepare             string
		recorded, wantError bool
		wantDDL             int
	}{
		{name: "fresh_134_to_135", wantDDL: 1},
		{name: "whole_ddl_unrecorded_replay", prepare: "ddl", wantDDL: 1},
		{name: "recorded_exact", prepare: "ddl", recorded: true},
		{name: "unrecorded_same_name_plain_column", prepare: "plain", wantError: true},
		{name: "unrecorded_same_name_wrong_expression", prepare: "expression_only", wantError: true},
		{name: "unrecorded_index_only", prepare: "index_only", wantError: true},
		{name: "unrecorded_missing_index", prepare: "missing_index", wantError: true},
		{name: "unrecorded_wrong_index", prepare: "wrong_index", wantError: true},
		{name: "recorded_missing_index", prepare: "missing_index", recorded: true, wantError: true},
		{name: "recorded_wrong_index", prepare: "wrong_index", recorded: true, wantError: true},
		{name: "recorded_wrong_expression", prepare: "expression_drift", recorded: true, wantError: true},
		{name: "recorded_source_collation_drift", prepare: "source_drift", recorded: true, wantError: true},
		{name: "recorded_missing_column", prepare: "missing_column", recorded: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := settlementShapeMigrationDatabase(t)
			base, update := settlementShapeMigrationFiles(t)
			if ran, err := Up(db, base); err != nil || len(ran) != 134 {
				t.Fatal("complete 134 baseline failed", err)
			}
			// Minimal real old-column writers; this test changes only the receipt table.
			// The existing 0134 suite retains the broader encrypted/ledger preservation coverage.
			for i := 0; i < 2; i++ {
				if err := db.Exec(`INSERT INTO video_query_receipts (attempt_id,org_id,task_id,sequence,task_row_version,http_status,outcome,observation_json,payload_ref,received_at,generation_applied_at,settlement_kind) VALUES (?,1,?,1,7,200,'accepted','{"status":"succeeded"}','synthetic-private',UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),?)`, fmt.Sprintf("shape-%d", i), fmt.Sprintf("shape-task-%d", i), []string{"usage", "provider_nocharge"}[i]).Error; err != nil {
					t.Fatal("old writer fixture failed", err)
				}
			}
			ddl := string(update[settlementShapeMigration+".sql"].Data)
			exec := func(q string) {
				t.Helper()
				if err := db.Exec(q).Error; err != nil {
					t.Fatal("private fixture DDL failed", err)
				}
			}
			switch tc.prepare {
			case "plain":
				exec("ALTER TABLE video_query_receipts ADD COLUMN settlement_shape_ready TINYINT UNSIGNED NOT NULL DEFAULT 0")
			case "expression_only":
				exec("ALTER TABLE video_query_receipts ADD COLUMN settlement_shape_ready TINYINT UNSIGNED GENERATED ALWAYS AS (0) VIRTUAL NOT NULL")
			case "index_only":
				exec("ALTER TABLE video_query_receipts ADD KEY idx_video_query_settlement_due (attempt_id)")
			case "":
			default:
				exec(ddl)
			}
			// Arrange the recorded case through the real migrator before corrupting it.
			if tc.recorded {
				if ran, err := Up(db, update); err != nil || !reflect.DeepEqual(ran, []string{settlementShapeMigration}) {
					t.Fatal("could not record exact initial 135", ran, err)
				}
			}
			switch tc.prepare {
			case "missing_index":
				exec("ALTER TABLE video_query_receipts DROP INDEX idx_video_query_settlement_due")
			case "wrong_index":
				exec("ALTER TABLE video_query_receipts DROP INDEX idx_video_query_settlement_due, ADD KEY idx_video_query_settlement_due (attempt_id)")
			case "expression_drift":
				exec("ALTER TABLE video_query_receipts MODIFY COLUMN settlement_shape_ready TINYINT UNSIGNED GENERATED ALWAYS AS (0) VIRTUAL NOT NULL")
			case "source_drift":
				exec("ALTER TABLE video_query_receipts MODIFY COLUMN settlement_kind VARCHAR(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT ''")
			case "missing_column":
				exec("ALTER TABLE video_query_receipts DROP INDEX idx_video_query_settlement_due, DROP COLUMN settlement_shape_ready")
			}
			beforeMeta, err := readSettlementShapeMetadata(db)
			if err != nil {
				t.Fatal(err)
			}
			beforeRows := settlementShapeOldRows(t, db)
			beforeCount := settlementShapeVersionCount(t, db)
			expectedBefore := int64(0)
			if tc.recorded {
				expectedBefore = 1
			}
			if beforeCount != expectedBefore {
				t.Fatal("incorrect initial version fixture")
			}
			t.Logf("0135 guard before: %s", settlementShapeProofJSON(map[string]any{"case": tc.name, "recorded": beforeCount, "metadata_sha256": settlementShapeMetadataSHA(beforeMeta), "old_rows_sha256": beforeRows}))
			attempts := 0
			hook := "shape_guard_observe_ddl"
			if err := db.Callback().Raw().Before("gorm:raw").Register(hook, func(q *gorm.DB) {
				if strings.HasPrefix(strings.TrimSpace(q.Statement.SQL.String()), "ALTER TABLE video_query_receipts") {
					attempts++
				}
			}); err != nil {
				t.Fatal(err)
			}
			ran, err := Up(db, update)
			if removeErr := db.Callback().Raw().Remove(hook); removeErr != nil {
				t.Fatal(removeErr)
			}
			errorText := ""
			if err != nil {
				errorText = err.Error()
			}
			t.Logf("0135 guard result: %s", settlementShapeProofJSON(map[string]any{"case": tc.name, "expected_rejection": tc.wantError, "error": errorText, "ddl_attempts": attempts, "applied": ran}))
			if (err != nil) != tc.wantError || attempts != tc.wantDDL {
				t.Fatalf("guard result mismatch: error=%v DDL=%d expected=%d", err, attempts, tc.wantDDL)
			}
			if settlementShapeOldRows(t, db) != beforeRows {
				t.Fatal("guard migration/rejection changed preexisting receipt data")
			}
			afterMeta, metaErr := readSettlementShapeMetadata(db)
			if metaErr != nil {
				t.Fatal(metaErr)
			}
			t.Logf("0135 guard after: %s", settlementShapeProofJSON(map[string]any{"case": tc.name, "recorded": settlementShapeVersionCount(t, db), "metadata_sha256": settlementShapeMetadataSHA(afterMeta), "old_rows_sha256": settlementShapeOldRows(t, db)}))
			if tc.wantError {
				if len(ran) != 0 || settlementShapeVersionCount(t, db) != beforeCount || !reflect.DeepEqual(beforeMeta, afterMeta) {
					t.Fatal("failed guard changed metadata or migration completion")
				}
				return
			}
			if settlementShapeVersionCount(t, db) != 1 {
				t.Fatal("exact version not recorded once")
			}
			if tc.recorded {
				if len(ran) != 0 {
					t.Fatal("recorded migration ran again")
				}
			} else if !reflect.DeepEqual(ran, []string{settlementShapeMigration}) {
				t.Fatal("wrong migration record", ran)
			}
			guard, e := newSettlementShapeGuard(update[settlementShapeMigration+".sql"].Data, nil)
			if e != nil {
				t.Fatal(e)
			}
			if e = guard.matches(afterMeta, true); e != nil {
				t.Fatal("post-migration metadata not exact", e)
			}
			var flags []int
			if e = db.Raw("SELECT settlement_shape_ready FROM video_query_receipts ORDER BY attempt_id").Scan(&flags).Error; e != nil || !reflect.DeepEqual(flags, []int{1, 1}) {
				t.Fatal("generated shape differs from valid real rows", e)
			}
			// A second recorded call must still inspect metadata but execute no migration.
			rows := settlementShapeOldRows(t, db)
			if reran, e := Up(db, update); e != nil || len(reran) != 0 || rows != settlementShapeOldRows(t, db) {
				t.Fatal("recorded replay not idempotent", e)
			}
		})
	}
}

func settlementShapeMigrationFiles(t *testing.T) (fstest.MapFS, fstest.MapFS) {
	t.Helper()
	root := os.DirFS("../../../migrations")
	names, err := fs.Glob(root, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	base, update := fstest.MapFS{}, fstest.MapFS{}
	for _, name := range names {
		if name > settlementShapeMigration+".sql" {
			continue
		}
		raw, e := fs.ReadFile(root, name)
		if e != nil {
			t.Fatal(e)
		}
		if name == settlementShapeMigration+".sql" {
			update[name] = &fstest.MapFile{Data: raw}
		} else {
			base[name] = &fstest.MapFile{Data: raw}
		}
	}
	if len(base) != 134 || len(update) != 1 {
		t.Fatal("exact full 134 baseline and tracked 0135 required")
	}
	if _, err := newSettlementShapeGuard(update[settlementShapeMigration+".sql"].Data, nil); err != nil {
		t.Fatal(err)
	}
	return base, update
}

func settlementShapeVersionCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&Record{}).Where("version = ?", settlementShapeMigration).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func settlementShapeOldRows(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var names []string
	for _, c := range approvedSettlementShapeProfile().Columns {
		if c.ColumnName != settlementShapeColumnName {
			names = append(names, "`"+c.ColumnName+"`")
		}
	}
	rows, err := db.Raw("SELECT " + strings.Join(names, ",") + " FROM video_query_receipts ORDER BY attempt_id").Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hash := sha256.New()
	count := 0
	for rows.Next() {
		values := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(hash).Encode(values); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if err := rows.Err(); err != nil || count != 2 {
		t.Fatal("old row fingerprint incomplete", err, count)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// Follows the existing queryRecoveryMigrationDatabase discipline, with an
// explicit application-compatible charset and retained schemas on test failure.
func settlementShapeMigrationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN=ark_test required; no real 0135 guard test executed")
	}
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil || cfg.DBName != "ark_test" {
		t.Fatal("0135 migration tests require isolated ark_test control database")
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.Collation = "utf8mb4_general_ci"
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 35*time.Second, 35*time.Second
	cfg.Logger = log.New(io.Discard, "", 0)
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+08:00'"
	connector, err := drivermysql.NewConnector(cfg.Clone())
	if err != nil {
		t.Fatal("private control connector failed")
	}
	admin := sql.OpenDB(connector)
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		admin.Close()
		t.Fatal("private schema identity failed")
	}
	name := "ark_test_shape_guard_" + hex.EncodeToString(nonce[:])
	var pool *sql.DB
	created := false
	t.Cleanup(func() {
		if pool != nil {
			_ = pool.Close()
		}
		if created {
			if t.Failed() {
				t.Log("retained failed 0135 schema:", name)
			} else {
				ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
					t.Error("could not remove this invocation's exact private schema", name)
				}
			}
		}
		_ = admin.Close()
	})
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("private schema creation failed")
	}
	created = true
	cfg.DBName = name
	connector, err = drivermysql.NewConnector(cfg.Clone())
	if err != nil {
		t.Fatal("private database connector failed")
	}
	pool = sql.OpenDB(connector)
	pool.SetMaxOpenConns(6)
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("private migration database connection failed")
	}
	var environment struct{ CharsetClient, CollationConnection, TimeZone string }
	if err = db.Raw("SELECT @@character_set_client AS charset_client,@@collation_connection AS collation_connection,@@session.time_zone AS time_zone").Scan(&environment).Error; err != nil || environment.CharsetClient != "utf8mb4" || environment.CollationConnection != "utf8mb4_general_ci" || environment.TimeZone != "+08:00" {
		t.Fatal("private connection differs from the approved charset/time-zone contract")
	}
	t.Log("private 0135 schema:", name)
	return db
}

func settlementShapeProofJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "metadata serialization failed"
	}
	return string(raw)
}
func settlementShapeMetadataSHA(v settlementShapeMetadata) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "metadata serialization failed"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
