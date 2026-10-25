package migrate

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

const inboxMigrationFilename = "0128_video_response_inbox.sql"

var inboxMigrationTables = []string{
	"video_execution_attempts",
	"video_receipt_applications",
	"video_response_inbox",
}

func TestMySQLVideoInboxMigrationPreservesLegacyAndReplays(t *testing.T) {
	// Each invocation creates and cleans only its own random private schema via
	// the existing restricted ark_test helper. Every MySQL DDL commit boundary,
	// including all tables created before the version record, must be resumable.
	for completed := 0; completed <= 3; completed++ {
		t.Run(fmt.Sprintf("resume_after_%d_creates", completed), func(t *testing.T) {
			db := runtimeMigrationDatabase(t)
			base, update := inboxMigrationFiles(t)
			if _, err := Up(db, base); err != nil {
				t.Fatal(err)
			}
			seedRuntimeLegacyRows(t, db, "inbox-before")
			oldTables := inboxBusinessTables(t, db)
			oldRows, oldColumns := snapshotInboxExistingTables(t, db, oldTables)
			for _, table := range []string{"tasks", "video_task_states", "video_operations", "video_query_receipts", "video_billing_snapshots", "ledger_accounts", "ledger_journal", "ledger_holds"} {
				if oldRows[table].Count == 0 {
					t.Fatalf("preservation fixture %s is empty", table)
				}
			}
			statements := splitStatements(string(update[inboxMigrationFilename].Data))
			if len(statements) != 3 {
				t.Fatal("inbox migration must contain exactly three new-table DDL statements")
			}
			for _, statement := range statements[:completed] {
				if err := db.Exec(statement).Error; err != nil {
					t.Fatal(err)
				}
			}
			var versionCount int64
			if err := db.Model(&Record{}).Where("version = ?", strings.TrimSuffix(inboxMigrationFilename, ".sql")).Count(&versionCount).Error; err != nil || versionCount != 0 {
				t.Fatal("partial migration must not have a recorded version")
			}
			ran, err := Up(db, update)
			if err != nil || !reflect.DeepEqual(ran, []string{"0128_video_response_inbox"}) {
				t.Fatalf("inbox migration was not actually applied: versions=%v error=%v", ran, err)
			}
			assertInboxExistingTablesUnchanged(t, db, oldRows, oldColumns)
			wantTables := append(append([]string{}, oldTables...), inboxMigrationTables...)
			sort.Strings(wantTables)
			if got := inboxBusinessTables(t, db); !reflect.DeepEqual(got, wantTables) {
				t.Fatal("inbox migration must add only its three tables")
			}
			assertInboxMigrationSchema(t, db)
			assertInboxMigrationTablesEmpty(t, db)

			// This helper issues actual INSERTs containing only pre-0127 columns,
			// rather than current ORM models with freshly added contract fields.
			seedRuntimeLegacyRows(t, db, "inbox-oldwriter")
			assertRuntimeMigrationDefaults(t, db)
			if err := db.Exec("UPDATE video_operations SET version=version+1 WHERE id=?", "op-runtime-inbox-oldwriter-0").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("UPDATE video_task_states SET row_version=row_version+1 WHERE task_id=?", "runtime-inbox-oldwriter-0").Error; err != nil {
				t.Fatal(err)
			}
			// Existing accepted/unknown/settled rows never gain a new send
			// registration merely because the expand schema exists.
			assertInboxMigrationTablesEmpty(t, db)
			if drift, err := ledger.CheckDrift(db); err != nil || len(drift) != 0 {
				t.Fatal("legacy migration fixture ledger drifted")
			}
			assertInboxMigrationConstraints(t, db)

			replayRows, replayColumns := snapshotInboxExistingTables(t, db, inboxBusinessTables(t, db))
			if ran, err := Up(db, update); err != nil || len(ran) != 0 {
				t.Fatalf("recorded inbox migration replay is not a no-op: versions=%v error=%v", ran, err)
			}
			assertInboxExistingTablesUnchanged(t, db, replayRows, replayColumns)
			assertInboxMigrationSchema(t, db)
		})
	}
}

func inboxMigrationFiles(t *testing.T) (fstest.MapFS, fstest.MapFS) {
	t.Helper()
	// Keep the 0127 test's baseline frozen; extend a copy only for this test.
	base, runtime := runtimeMigrationFiles(t)
	for name, file := range runtime {
		base[name] = file
	}
	raw, err := fs.ReadFile(os.DirFS("../../../migrations"), inboxMigrationFilename)
	if err != nil {
		t.Fatal("formal response inbox migration is missing")
	}
	if len(base) != 127 {
		t.Fatal("inbox migration must follow the complete 0127 baseline")
	}
	return base, fstest.MapFS{inboxMigrationFilename: &fstest.MapFile{Data: raw}}
}

func inboxBusinessTables(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var names []string
	if err := db.Raw("SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE() AND table_type='BASE TABLE' AND table_name<>'schema_migrations' ORDER BY table_name").Scan(&names).Error; err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("migration preservation baseline has no business tables")
	}
	return names
}

type inboxMigrationColumn struct {
	ColumnName           string
	ColumnType           string
	IsNullable           string
	ColumnDefault        sql.NullString
	Extra                string
	CollationName        sql.NullString
	GenerationExpression string
}

func inboxMigrationColumns(t *testing.T, db *gorm.DB, table string) []inboxMigrationColumn {
	t.Helper()
	var columns []inboxMigrationColumn
	if err := db.Raw("SELECT column_name,column_type,is_nullable,column_default,extra,collation_name,generation_expression FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position", table).Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if len(columns) == 0 {
		t.Fatalf("table %s has no schema columns", table)
	}
	return columns
}

func snapshotInboxExistingTables(t *testing.T, db *gorm.DB, tables []string) (map[string]runtimeTableSnapshot, map[string][]inboxMigrationColumn) {
	t.Helper()
	rows := make(map[string]runtimeTableSnapshot, len(tables))
	columns := make(map[string][]inboxMigrationColumn, len(tables))
	for _, table := range tables {
		rows[table] = snapshotRuntimeTable(t, db, table, nil)
		columns[table] = inboxMigrationColumns(t, db, table)
	}
	return rows, columns
}

func assertInboxExistingTablesUnchanged(t *testing.T, db *gorm.DB, rows map[string]runtimeTableSnapshot, columns map[string][]inboxMigrationColumn) {
	t.Helper()
	for table, before := range rows {
		after := snapshotRuntimeTable(t, db, table, nil)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("inbox migration changed existing row data in %s", table)
		}
		if !reflect.DeepEqual(inboxMigrationColumns(t, db, table), columns[table]) {
			t.Fatalf("inbox migration changed existing schema columns in %s", table)
		}
	}
}

func assertInboxMigrationTablesEmpty(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range inboxMigrationTables {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("migration or legacy write populated new protocol table %s", table)
		}
	}
}

func assertInboxMigrationSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	wantColumns := map[string]string{
		"video_execution_attempts":   "attempt_id,org_id,task_id,operation_id,action,storage_protocol,provider_scope_id,credential_snapshot_id,billing_snapshot_id,input_payload_ref,input_identity_hmac,input_hmac_version,runtime_version,adapter_version,protocol_contract_version,billing_schema_version,execution_schema_version,observation_schema_version,operation_claim_version,evidence_hmac_version,capture_history,retention_days,registered_at",
		"video_response_inbox":       "receipt_id,org_id,task_id,operation_id,attempt_id,action,storage_protocol,provider_scope_id,credential_snapshot_id,billing_snapshot_id,runtime_version,adapter_version,protocol_contract_version,billing_schema_version,execution_schema_version,observation_schema_version,evidence_hmac_version,evidence_hmac,http_status,outcome,safe_error_code,upstream_task_id,provider_request_id,truncated,captured_body_bytes,payload_ref,received_at",
		"video_receipt_applications": "receipt_id,org_id,task_id,attempt_id,state,version,next_attempt_at,error_code,applied_at,applied_event_id,billing_entry_id,created_at,updated_at",
	}
	for table, names := range wantColumns {
		var gotNames []string
		for _, column := range inboxMigrationColumns(t, db, table) {
			gotNames = append(gotNames, column.ColumnName)
			if column.ColumnDefault.Valid {
				t.Fatalf("new protocol column %s.%s must not invent a default", table, column.ColumnName)
			}
			if column.CollationName.Valid {
				wantCollation := "utf8mb4_0900_bin"
				switch column.ColumnName {
				case "task_id", "operation_id", "attempt_id", "input_payload_ref":
					// Identifiers join pre-0128 columns without a conversion that
					// would fail with MySQL 1267 or prevent an indexed lookup.
					// Protocol/version text still requires exact NO PAD semantics.
					wantCollation = "utf8mb4_bin"
				}
				if column.CollationName.String != wantCollation {
					t.Fatalf("%s.%s collation mismatch: got=%s want=%s", table, column.ColumnName, column.CollationName.String, wantCollation)
				}
			}
			wantNullable := "NO"
			if table == "video_receipt_applications" && (column.ColumnName == "applied_at" || column.ColumnName == "billing_entry_id") {
				wantNullable = "YES"
			}
			if column.IsNullable != wantNullable || column.Extra != "" || column.GenerationExpression != "" {
				t.Fatalf("new protocol column %s.%s has unexpected nullability or generated behavior", table, column.ColumnName)
			}
		}
		if strings.Join(gotNames, ",") != names {
			t.Fatalf("new protocol table %s has unexpected columns", table)
		}
	}
	type schemaIndex struct {
		TableName  string
		IndexName  string
		NonUnique  int
		ColumnList string
	}
	wantIndexes := []schemaIndex{
		{"video_execution_attempts", "idx_video_execution_operation", 1, "org_id,operation_id"},
		{"video_execution_attempts", "idx_video_execution_task", 1, "org_id,task_id,action,registered_at"},
		{"video_execution_attempts", "PRIMARY", 0, "attempt_id"},
		{"video_execution_attempts", "uk_video_execution_claim", 0, "org_id,operation_id,operation_claim_version"},
		{"video_receipt_applications", "idx_video_receipt_apply", 1, "state,next_attempt_at,receipt_id"},
		{"video_receipt_applications", "idx_video_receipt_task", 1, "org_id,task_id,attempt_id"},
		{"video_receipt_applications", "PRIMARY", 0, "receipt_id"},
		{"video_response_inbox", "idx_video_inbox_attempt", 1, "org_id,attempt_id,received_at,receipt_id"},
		{"video_response_inbox", "PRIMARY", 0, "receipt_id"},
		{"video_response_inbox", "uk_video_inbox_evidence", 0, "org_id,attempt_id,evidence_hmac_version,evidence_hmac"},
	}
	var gotIndexes []schemaIndex
	if err := db.Raw("SELECT table_name,index_name,non_unique,GROUP_CONCAT(column_name ORDER BY seq_in_index) AS column_list FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name IN ? GROUP BY table_name,index_name,non_unique", inboxMigrationTables).Scan(&gotIndexes).Error; err != nil {
		t.Fatal(err)
	}
	less := func(a, b schemaIndex) bool {
		if a.TableName != b.TableName {
			return a.TableName < b.TableName
		}
		return a.IndexName < b.IndexName
	}
	sort.Slice(gotIndexes, func(i, j int) bool { return less(gotIndexes[i], gotIndexes[j]) })
	sort.Slice(wantIndexes, func(i, j int) bool { return less(wantIndexes[i], wantIndexes[j]) })
	if !reflect.DeepEqual(gotIndexes, wantIndexes) {
		t.Fatal("inbox indexes differ from immutable identity, deduplication and bounded recovery contract")
	}
}

func assertInboxMigrationConstraints(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Two old-writer batches seed four operations each. Check that the exact
	// targets exist before testing the same NOT EXISTS used by legacy recovery;
	// a missing fixture must not be mistaken for a SQL compatibility failure.
	var operationCount, targetCount int64
	if err := db.Table("video_operations").Where("org_id = ?", 1).Count(&operationCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("video_operations").Where("org_id = ? AND id IN ?", 1, []string{"op-runtime-inbox-before-0", "op-runtime-inbox-before-1"}).Count(&targetCount).Error; err != nil {
		t.Fatal(err)
	}
	if operationCount != 8 || targetCount != 2 {
		t.Fatalf("legacy exclusion fixture mismatch: operations=%d targets=%d", operationCount, targetCount)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	execution := map[string]any{
		"attempt_id": "migration-attempt", "org_id": 1, "task_id": "runtime-inbox-before-0",
		"operation_id": "op-runtime-inbox-before-0", "action": "create_task", "storage_protocol": "inbox-v1",
		"provider_scope_id": 1, "credential_snapshot_id": 1, "billing_snapshot_id": 1,
		"input_payload_ref": "synthetic-payload", "input_identity_hmac": strings.Repeat("a", 64), "input_hmac_version": "h1",
		"runtime_version": videoruntime.RuntimeV1, "adapter_version": videoruntime.AdapterV1,
		"protocol_contract_version": videoruntime.ProtocolV1, "billing_schema_version": videoruntime.BillingV1,
		"execution_schema_version": 1, "observation_schema_version": 1, "operation_claim_version": 10,
		"evidence_hmac_version": "h1", "capture_history": false, "retention_days": 30, "registered_at": now,
	}
	if err := db.Table("video_execution_attempts").Create(execution).Error; err != nil {
		t.Fatal(err)
	}
	execution["attempt_id"] = "migration-same-claim"
	assertInboxDuplicateKey(t, db.Table("video_execution_attempts").Create(execution).Error)
	for _, protocol := range []string{"INBOX-V1", "inbox-v1 ", ""} {
		var count int64
		if err := db.Table("video_execution_attempts").Where("storage_protocol=?", protocol).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("storage protocol SQL matched unsupported spelling")
		}
	}
	var supported int64
	if err := db.Table("video_execution_attempts").Where("storage_protocol=?", "inbox-v1").Count(&supported).Error; err != nil || supported != 1 {
		t.Fatal("storage protocol exact predicate has no positive control")
	}
	// Unknown future protocols must be retained, not defaulted to inbox-v1 or
	// mistaken for legacy because the current recovery registry cannot read it.
	execution["attempt_id"] = "migration-future-attempt"
	execution["task_id"] = "runtime-inbox-before-1"
	execution["operation_id"] = "op-runtime-inbox-before-1"
	execution["storage_protocol"] = "unknown-future-inbox"
	if err := db.Table("video_execution_attempts").Create(execution).Error; err != nil {
		t.Fatal(err)
	}
	var legacyCount int64
	if err := db.Table("video_operations AS o").Where("o.org_id=? AND NOT EXISTS (SELECT 1 FROM video_execution_attempts e WHERE e.org_id=o.org_id AND e.operation_id=o.id)", 1).Count(&legacyCount).Error; err != nil {
		t.Fatalf("legacy exclusion SQL failed: %v", err)
	}
	if legacyCount != operationCount-targetCount {
		t.Fatalf("registered and unknown-protocol operations must both be excluded from the legacy path: got=%d want=%d", legacyCount, operationCount-targetCount)
	}
	inbox := map[string]any{
		"receipt_id": "migration-receipt", "org_id": 1, "task_id": "runtime-inbox-before-0",
		"operation_id": "op-runtime-inbox-before-0", "attempt_id": "migration-attempt", "action": "create_task", "storage_protocol": "inbox-v1",
		"provider_scope_id": 1, "credential_snapshot_id": 1, "billing_snapshot_id": 1,
		"runtime_version": videoruntime.RuntimeV1, "adapter_version": videoruntime.AdapterV1,
		"protocol_contract_version": videoruntime.ProtocolV1, "billing_schema_version": videoruntime.BillingV1,
		"execution_schema_version": 1, "observation_schema_version": 1,
		"evidence_hmac_version": "h1", "evidence_hmac": strings.Repeat("b", 64),
		"http_status": 200, "outcome": "accepted", "safe_error_code": "", "upstream_task_id": "synthetic-upstream",
		"provider_request_id": "synthetic-request", "truncated": false, "captured_body_bytes": 0, "payload_ref": "synthetic-receipt", "received_at": now,
	}
	if err := db.Table("video_response_inbox").Create(inbox).Error; err != nil {
		t.Fatal(err)
	}
	inbox["receipt_id"] = "migration-duplicate-evidence"
	assertInboxDuplicateKey(t, db.Table("video_response_inbox").Create(inbox).Error)
	application := map[string]any{
		"receipt_id": "migration-receipt", "org_id": 1, "task_id": "runtime-inbox-before-0", "attempt_id": "migration-attempt",
		"state": "pending", "version": 1, "next_attempt_at": now, "error_code": "", "applied_event_id": "", "created_at": now, "updated_at": now,
	}
	if err := db.Table("video_receipt_applications").Create(application).Error; err != nil {
		t.Fatal(err)
	}
	assertInboxDuplicateKey(t, db.Table("video_receipt_applications").Create(application).Error)
}

func assertInboxDuplicateKey(t *testing.T, err error) {
	t.Helper()
	var duplicate *mysqlDriver.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		t.Fatal("immutable identity or evidence duplicate was not rejected by its database unique key")
	}
}
