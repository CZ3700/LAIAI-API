package migrate

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A private randomly named schema makes initial-install coverage real even
// when the main ark_test fixture has already applied the new migration. This
// test never resets an existing database or deletes migration history.
func TestMySQLVideoRuntimeMigrationPreservesLegacyAndReplays(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "fresh"
		if partial {
			name = "resume_after_two_atomic_alters"
		}
		t.Run(name, func(t *testing.T) {
			db := runtimeMigrationDatabase(t)
			base, update := runtimeMigrationFiles(t)
			if _, err := Up(db, base); err != nil {
				t.Fatal(err)
			}
			seedRuntimeLegacyRows(t, db, "before")
			preserved := map[string]runtimeTableSnapshot{}
			for _, table := range []string{"tasks", "video_task_states", "video_operations", "video_query_receipts", "video_billing_snapshots", "ledger_accounts", "ledger_journal", "ledger_holds"} {
				preserved[table] = snapshotRuntimeTable(t, db, table, nil)
				if preserved[table].Count == 0 {
					t.Fatalf("preservation fixture %s is empty", table)
				}
			}
			if partial {
				// Simulate MySQL's DDL implicit commits before the migration's
				// version record. The next run must finish the other tables.
				statements := splitStatements(string(update["0127_video_runtime_contracts.sql"].Data))
				if len(statements) != 4 {
					t.Fatal("runtime migration must use one atomic ALTER per table")
				}
				for _, statement := range statements[:2] {
					if err := db.Exec(statement).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			ran, err := Up(db, update)
			if err != nil || !reflect.DeepEqual(ran, []string{"0127_video_runtime_contracts"}) {
				t.Fatalf("runtime migration was not actually applied: versions=%v error=%v", ran, err)
			}
			for table, before := range preserved {
				after := snapshotRuntimeTable(t, db, table, before.Columns)
				if after.Count != before.Count || after.Hash != before.Hash {
					t.Fatalf("migration changed pre-existing columns in %s", table)
				}
			}
			assertRuntimeMigrationDefaults(t, db)
			var pendingColumns string
			if err := db.Raw("SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='video_task_states' AND index_name='idx_video_runtime_pending_usage'").Scan(&pendingColumns).Error; err != nil || pendingColumns != "billing_state,task_id,org_id" {
				t.Fatal("runtime pending-usage observability index is missing or changed")
			}
			// These inserts use only pre-0127 columns, like an actual old binary.
			seedRuntimeLegacyRows(t, db, "oldwriter")
			assertRuntimeMigrationDefaults(t, db)
			assertRuntimeVersionSQLIsExact(t, db)
			// A new writer explicitly supplying an empty value must not receive
			// the compatibility default. Replays must not normalize it either.
			if err := db.Exec("UPDATE video_task_states SET runtime_version = '' WHERE task_id = ?", "runtime-oldwriter-0").Error; err != nil {
				t.Fatal(err)
			}
			var contract videoruntime.Contract
			if err := db.Table("video_task_states").Select("runtime_version,adapter_version,protocol_contract_version,billing_schema_version").Where("task_id = ?", "runtime-oldwriter-0").Scan(&contract).Error; err != nil {
				t.Fatal(err)
			}
			if contract.RuntimeVersion != "" {
				t.Fatal("database silently replaced an explicit empty runtime")
			}
			if _, err := videoruntime.Resolve(contract); err != videoruntime.ErrUnsupported {
				t.Fatal("empty stored runtime did not fail closed")
			}
			for table := range preserved {
				preserved[table] = snapshotRuntimeTable(t, db, table, nil)
			}
			if ran, err := Up(db, update); err != nil || len(ran) != 0 {
				t.Fatalf("recorded runtime migration replay is not a no-op: versions=%v error=%v", ran, err)
			}
			for table, before := range preserved {
				after := snapshotRuntimeTable(t, db, table, before.Columns)
				if after.Count != before.Count || after.Hash != before.Hash {
					t.Fatalf("migration replay changed %s", table)
				}
			}
			if drift, err := ledger.CheckDrift(db); err != nil || len(drift) != 0 {
				t.Fatal("migration fixture ledger drifted")
			}
		})
	}
}

func runtimeMigrationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN required for real runtime migration upgrade coverage")
	}
	connection, err := mysqlDriver.ParseDSN(dsn)
	if err != nil || connection.DBName != "ark_test" {
		t.Fatal("runtime migration tests require a dedicated ark_test connection")
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("could not allocate a test schema identity")
	}
	name := "ark_test_runtime_" + hex.EncodeToString(random[:])
	if !regexp.MustCompile(`^ark_test_runtime_[0-9a-f]{24}$`).MatchString(name) {
		t.Fatal("invalid generated test schema identity")
	}
	admin, err := sql.Open("mysql", connection.FormatDSN())
	if err != nil {
		t.Fatal("could not open the isolated migration test connection")
	}
	created := false
	var testDB *sql.DB
	t.Cleanup(func() {
		if testDB != nil {
			_ = testDB.Close()
		}
		// name is a generated, validated identifier and this invocation alone
		// created it. Never remove ark_test, a wildcard, or a pre-existing DB.
		if created {
			if _, err := admin.Exec("DROP DATABASE `" + name + "`"); err != nil {
				t.Error("could not remove this invocation's private runtime test schema")
			}
		}
		_ = admin.Close()
	})
	if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("could not create a private runtime test schema (test user needs CREATE/DROP DATABASE)")
	}
	created = true
	connection.DBName = name
	connection.ParseTime = true
	db, err := gorm.Open(mysql.Open(connection.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("could not connect to the private runtime test schema")
	}
	testDB, err = db.DB()
	if err != nil {
		t.Fatal("could not obtain the private test database pool")
	}
	testDB.SetMaxOpenConns(4)
	return db
}

func runtimeMigrationFiles(t *testing.T) (fstest.MapFS, fstest.MapFS) {
	t.Helper()
	source := os.DirFS("../../../migrations")
	names, err := fs.Glob(source, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	base, update := fstest.MapFS{}, fstest.MapFS{}
	for _, name := range names {
		if name > "0127_video_runtime_contracts.sql" {
			continue
		}
		raw, err := fs.ReadFile(source, name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "0127_video_runtime_contracts.sql" {
			update[name] = &fstest.MapFile{Data: raw}
		} else {
			base[name] = &fstest.MapFile{Data: raw}
		}
	}
	if len(base) != 126 || len(update) != 1 {
		t.Fatal("formal pre-runtime or runtime migrations are missing")
	}
	return base, update
}

func assertRuntimeMigrationDefaults(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []string{"video_task_states", "video_operations", "video_query_receipts"} {
		var bad int64
		query := db.Table(table).Where("runtime_version <> ? OR adapter_version <> ? OR protocol_contract_version <> ?", videoruntime.RuntimeV1, videoruntime.AdapterV1, videoruntime.ProtocolV1)
		if err := query.Count(&bad).Error; err != nil || bad != 0 {
			t.Fatalf("legacy execution rows in %s lack explicit v1 classification", table)
		}
	}
	for _, table := range []string{"video_task_states", "video_billing_snapshots"} {
		var bad int64
		if err := db.Table(table).Where("billing_schema_version <> ?", videoruntime.BillingV1).Count(&bad).Error; err != nil || bad != 0 {
			t.Fatalf("legacy billing rows in %s lack explicit v1 classification", table)
		}
	}
	var bad int64
	if err := db.Table("video_query_receipts").Where("observation_schema_version <> ?", videoruntime.ObservationSchemaV1).Count(&bad).Error; err != nil || bad != 0 {
		t.Fatal("legacy receipt format was not classified as v1")
	}
}

func assertRuntimeVersionSQLIsExact(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, column := range []struct{ table, name, value string }{
		{"video_task_states", "runtime_version", videoruntime.RuntimeV1},
		{"video_task_states", "adapter_version", videoruntime.AdapterV1},
		{"video_task_states", "protocol_contract_version", videoruntime.ProtocolV1},
		{"video_task_states", "billing_schema_version", videoruntime.BillingV1},
		{"video_billing_snapshots", "billing_schema_version", videoruntime.BillingV1},
		{"video_operations", "runtime_version", videoruntime.RuntimeV1},
		{"video_operations", "adapter_version", videoruntime.AdapterV1},
		{"video_operations", "protocol_contract_version", videoruntime.ProtocolV1},
		{"video_query_receipts", "runtime_version", videoruntime.RuntimeV1},
		{"video_query_receipts", "adapter_version", videoruntime.AdapterV1},
		{"video_query_receipts", "protocol_contract_version", videoruntime.ProtocolV1},
	} {
		var collation string
		if err := db.Raw("SELECT collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?", column.table, column.name).Scan(&collation).Error; err != nil || collation != "utf8mb4_0900_bin" {
			t.Fatalf("%s.%s lacks exact NO PAD binary collation", column.table, column.name)
		}
		var positive int64
		if err := db.Table(column.table).Where(column.name+" = ?", column.value).Count(&positive).Error; err != nil || positive == 0 {
			t.Fatal("exact-match predicate has no positive control")
		}
		for _, unknown := range []string{column.value + " ", strings.ToUpper(column.value), ""} {
			var matched int64
			if err := db.Table(column.table).Where(column.name+" = ?", unknown).Count(&matched).Error; err != nil || matched != 0 {
				t.Fatalf("%s.%s SQL accepts an unsupported spelling before LIMIT", column.table, column.name)
			}
		}
	}
}

type runtimeTableSnapshot struct {
	Columns []string
	Count   int
	Hash    string
}

func snapshotRuntimeTable(t *testing.T, db *gorm.DB, table string, columns []string) runtimeTableSnapshot {
	t.Helper()
	selection := "*"
	if len(columns) != 0 {
		selection = "`" + strings.Join(columns, "`,`") + "`"
	}
	rows, err := db.Table(table).Select(selection).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err = rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	serialized := []string{}
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		row := make([]any, len(columns))
		for i, value := range values {
			if value != nil {
				row[i] = string(value)
			}
		}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		serialized = append(serialized, string(raw))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(serialized)
	digest := sha256.Sum256([]byte(strings.Join(serialized, "\n")))
	return runtimeTableSnapshot{Columns: columns, Count: len(serialized), Hash: hex.EncodeToString(digest[:])}
}

// Deliberately use maps of pre-0127 columns, not the current versioned models.
func seedRuntimeLegacyRows(t *testing.T, db *gorm.DB, suffix string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	err := db.Transaction(func(tx *gorm.DB) error {
		account, err := ledger.EnsureAccount(tx, 1, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		if _, _, err := ledger.Post(tx, ledger.PostInput{IdempotencyKey: "runtime-credit-" + suffix, AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100)}); err != nil {
			return err
		}
		for i, status := range []string{"accepted", "submission_unknown", "succeeded", "succeeded"} {
			id := "runtime-" + suffix + "-" + string(rune('0'+i))
			hold, _, err := ledger.CreateHold(tx, ledger.HoldInput{IdempotencyKey: id, AccountID: account.ID, Amount: decimal.RequireFromString("1.23456789"), RefType: "task", RefID: id})
			if err != nil {
				return err
			}
			billingState, upstream := "reserved", ""
			var deleted any
			var observed any
			cost := "0.00000000"
			if i == 1 || i == 2 {
				billingState = "pending_usage"
			}
			if i >= 2 {
				upstream = "synthetic-" + id
			}
			if i == 3 {
				billingState, cost, observed, deleted = "settled", "0.12345678", int64(123), now
				if _, err := ledger.Capture(tx, hold.ID, decimal.RequireFromString(cost), "runtime migration fixture"); err != nil {
					return err
				}
			}
			if err := tx.Table("tasks").Create(map[string]any{"task_id": id, "org_id": 1, "api_key_id": 1, "model": "seedance-2.0", "channel_id": 1, "provider": "ark_official", "status": status, "upstream_task_id": upstream, "hold_id": hold.ID, "cost_usd": cost, "price_out_usd_per_1m": "8.123456789012", "submitted_at": now}).Error; err != nil {
				return err
			}
			snapshot := map[string]any{"org_id": 1, "api_key_id": 1, "task_id": id, "provider_scope_id": 1, "model": "seedance-2.0", "rule_id": "old-rule", "rule_version": "old-price-v1", "formula_version": "old-formula", "minimum_rule_version": "old-minimum", "capability_version": "old-capability", "request_hmac": strings.Repeat("a", 64), "metering_json": `{"tokens":123}`, "hold_id": hold.ID, "estimated_tokens": 12345, "hold_usd": "1.23456789", "retail_usd_per_1m": "8.123456789012", "supplier_cny_per_1m": "46.123456789012", "effective_ratio": "1.123456789012", "fx_usd_per_cny": "0.142857142857", "fx_version": "old-fx", "fx_observed_at": now, "supplier_reserved_cny": "8.64200000", "supplier_reserved_usd": "1.23456789", "created_at": now}
			if err := tx.Table("video_billing_snapshots").Create(snapshot).Error; err != nil {
				return err
			}
			var snapshotID uint64
			if err := tx.Table("video_billing_snapshots").Select("id").Where("org_id = 1 AND task_id = ?", id).Scan(&snapshotID).Error; err != nil {
				return err
			}
			state := map[string]any{"task_id": id, "org_id": 1, "create_request_id": "synthetic", "provider_scope_id": 1, "credential_snapshot_id": 1, "provider_base_url": "https://ark.cn-beijing.volces.com/api/v3", "config_version": "old-config", "capability_version": "old-capability", "upstream_model": "synthetic-upstream", "billing_state": billingState, "capture_history": false, "retention_days": 30, "sync_state": "needs_review", "artifact_status": "unavailable", "effective_payload_ref": "synthetic-evidence", "observed_completion_tokens": observed, "billing_snapshot_id": snapshotID, "idempotency_record_id": 1, "row_version": 7, "query_sequence": 4, "applied_query_sequence": 3, "next_poll_at": now, "deleted_at": deleted, "created_at": now, "updated_at": now}
			if err := tx.Table("video_task_states").Create(state).Error; err != nil {
				return err
			}
			operation := map[string]any{"id": "op-" + id, "org_id": 1, "resource_id": id, "kind": "create_task", "status": "unknown", "version": 9, "attempt_id": "attempt-" + id, "receipt_ref": "synthetic-receipt", "received_outcome": "unknown", "created_at": now, "updated_at": now}
			if err := tx.Table("video_operations").Create(operation).Error; err != nil {
				return err
			}
			receipt := map[string]any{"attempt_id": "query-" + id, "org_id": 1, "task_id": id, "sequence": 4, "task_row_version": 7, "http_status": 200, "outcome": "accepted", "observation_json": `{"status":"succeeded","usage":{}}`, "payload_ref": "synthetic-result", "received_at": now, "apply_error_code": "pending_usage"}
			if err := tx.Table("video_query_receipts").Create(receipt).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
