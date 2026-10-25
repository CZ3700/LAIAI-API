package migrate

import (
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

const actionTargetMigration = "0130_video_action_target.sql"

func actionTargetMigrationFiles(t *testing.T) (fstest.MapFS, fstest.MapFS) {
	t.Helper()
	base, routing := routingMigrationFiles(t)
	for name, file := range routing {
		base[name] = file
	}
	if len(base) != 129 {
		t.Fatal("action target requires the complete 0129 baseline")
	}
	raw, err := fs.ReadFile(os.DirFS("../../../migrations"), actionTargetMigration)
	if err != nil {
		t.Fatal(err)
	}
	return base, fstest.MapFS{actionTargetMigration: &fstest.MapFile{Data: raw}}
}

func TestMySQLVideoActionTargetMigrationPreservesLegacyAndReplays(t *testing.T) {
	for completed := 0; completed <= 1; completed++ {
		t.Run(fmt.Sprintf("resume_after_%d_alters", completed), func(t *testing.T) {
			db := runtimeMigrationDatabase(t)
			base, update := actionTargetMigrationFiles(t)
			if _, err := Up(db, base); err != nil {
				t.Fatal(err)
			}
			seedRuntimeLegacyRows(t, db, "inbox-before")
			seedRuntimeLegacyRows(t, db, "inbox-oldwriter")
			// Seed actual pre-0130 rows, including pending response/application
			// and future protocol registrations. No current video model is used.
			assertInboxMigrationConstraints(t, db)
			tables := inboxBusinessTables(t, db)
			before, columns := snapshotInboxExistingTables(t, db, tables)
			for _, name := range []string{"video_execution_attempts", "video_response_inbox", "video_receipt_applications", "ledger_accounts", "ledger_journal", "ledger_holds"} {
				if before[name].Count == 0 {
					t.Fatal("empty preservation fixture", name)
				}
			}
			statements := splitStatements(string(update[actionTargetMigration].Data))
			if len(statements) != 1 {
				t.Fatal("action target must be a single expand ALTER")
			}
			if completed == 1 {
				if err := db.Exec(statements[0]).Error; err != nil {
					t.Fatal(err)
				}
			}
			var recorded int64
			if err := db.Model(&Record{}).Where("version = ?", strings.TrimSuffix(actionTargetMigration, ".sql")).Count(&recorded).Error; err != nil || recorded != 0 {
				t.Fatal("partial DDL must not record success", err)
			}
			ran, err := Up(db, update)
			if err != nil || !reflect.DeepEqual(ran, []string{"0130_video_action_target"}) {
				t.Fatal("action migration/replay failed", ran, err)
			}
			if !reflect.DeepEqual(tables, inboxBusinessTables(t, db)) {
				t.Fatal("target expand changed business tables")
			}
			for name, old := range before {
				if got := snapshotRuntimeTable(t, db, name, old.Columns); !reflect.DeepEqual(old, got) {
					t.Fatal("target expand changed existing data", name)
				}
				got := inboxMigrationColumns(t, db, name)
				if name == "video_execution_attempts" {
					if len(got) != len(columns[name])+1 {
						t.Fatal("target expand must add only one column")
					}
					target := got[len(got)-1]
					if target.ColumnName != "target_upstream_task_id" || target.ColumnType != "varchar(128)" || target.IsNullable != "YES" || target.ColumnDefault.Valid || target.CollationName.String != "utf8mb4_bin" || target.Extra != "" || target.GenerationExpression != "" {
						t.Fatal("target column violates nullable legacy/indexed identity contract")
					}
					got = got[:len(got)-1]
				}
				if !reflect.DeepEqual(got, columns[name]) {
					t.Fatal("target expand changed old schema", name)
				}
			}
			seedActionTargetOldWriter(t, db)
			var populated int64
			if err := db.Table("video_execution_attempts").Where("target_upstream_task_id IS NOT NULL").Count(&populated).Error; err != nil || populated != 0 {
				t.Fatal("old rows/writers must retain NULL target", err)
			}
			assertActionTargetIndexedIdentity(t, db)
			rows, schema := snapshotInboxExistingTables(t, db, tables)
			if ran, err := Up(db, update); err != nil || len(ran) != 0 {
				t.Fatal("recorded target migration replay is not a no-op", ran, err)
			}
			assertInboxExistingTablesUnchanged(t, db, rows, schema)
		})
	}
}

func seedActionTargetOldWriter(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Deliberately omit the added column, as an actual A1.1 binary would.
	row := map[string]any{
		"attempt_id": "action-old-writer", "org_id": 1, "task_id": "runtime-inbox-before-0",
		"operation_id": "action-old-operation", "action": "create_task", "storage_protocol": "inbox-v1",
		"provider_scope_id": 1, "credential_snapshot_id": 1, "billing_snapshot_id": 1,
		"input_payload_ref": "old-input", "input_identity_hmac": strings.Repeat("c", 64), "input_hmac_version": "v1",
		"runtime_version": videoruntime.RuntimeV1, "adapter_version": videoruntime.AdapterV1,
		"protocol_contract_version": videoruntime.ProtocolV1, "billing_schema_version": videoruntime.BillingV1,
		"execution_schema_version": 1, "observation_schema_version": 1, "operation_claim_version": 2,
		"evidence_hmac_version": "v1", "capture_history": false, "retention_days": 30, "registered_at": time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := db.Table("video_execution_attempts").Create(row).Error; err != nil {
		t.Fatal("pre-action INSERT failed", err)
	}
}

func assertActionTargetIndexedIdentity(t *testing.T, db *gorm.DB) {
	t.Helper()
	const target = "provider-Task-1"
	if err := db.Exec("INSERT INTO video_upstream_bindings(provider_scope_id,upstream_task_id,org_id,task_id,created_at) VALUES(1,?,1,'runtime-inbox-before-0',UTC_TIMESTAMP(3))", target).Error; err != nil {
		t.Fatal(err)
	}
	const query = "SELECT b.org_id FROM video_execution_attempts ea JOIN video_upstream_bindings b ON b.provider_scope_id=ea.provider_scope_id AND b.upstream_task_id=ea.target_upstream_task_id AND OCTET_LENGTH(b.upstream_task_id)=OCTET_LENGTH(ea.target_upstream_task_id) WHERE ea.org_id=1 AND ea.attempt_id='action-old-writer'"
	for _, tc := range []struct {
		value string
		count int
	}{{target, 1}, {strings.ToLower(target), 0}, {target + " ", 0}, {"other-task", 0}} {
		if err := db.Table("video_execution_attempts").Where("org_id=1 AND attempt_id='action-old-writer'").Update("target_upstream_task_id", tc.value).Error; err != nil {
			t.Fatal(err)
		}
		var ids []uint64
		if err := db.Raw(query).Scan(&ids).Error; err != nil || len(ids) != tc.count {
			t.Fatal("target binding must use exact case and byte length", err, len(ids))
		}
		if tc.value != target {
			continue
		}
		var plan []struct {
			Table string
			Type  string
			Key   string
		}
		if err := db.Raw("EXPLAIN " + query).Scan(&plan).Error; err != nil || len(plan) != 2 {
			t.Fatal("indexed target join plan missing", err)
		}
		for _, item := range plan {
			if item.Key != "PRIMARY" || (item.Type != "const" && item.Type != "eq_ref") {
				t.Fatalf("target join must keep primary-key lookups: table=%s type=%s key=%s", item.Table, item.Type, item.Key)
			}
			t.Logf("target join table=%s type=%s key=%s", item.Table, item.Type, item.Key)
		}
	}
}
