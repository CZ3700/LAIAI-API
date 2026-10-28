package migrate

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const querySettlementMigration = "0134_video_query_generation_settlement.sql"

func querySettlementMigrationFiles(t *testing.T) (fstest.MapFS, fstest.MapFS) {
	t.Helper()
	base, prior := queryRecoveryMigrationFiles(t)
	for name, f := range prior {
		base[name] = f
	}
	raw, err := fs.ReadFile(os.DirFS("../../../migrations"), querySettlementMigration)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 133 {
		t.Fatal("expected exact tracked 133 baseline")
	}
	return base, fstest.MapFS{querySettlementMigration: &fstest.MapFile{Data: raw}}
}

// Real encrypted metadata/staging are included in preservation fingerprints;
// no raw capsule is logged or written to repository artifacts.
func querySettlementMigrationCipher(t *testing.T, db *gorm.DB) {
	t.Helper()
	key, err := crypto.NewKeyring("fixture:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	now := queryRecoveryDatabaseTime(t, db)
	store, err := invocation.NewVideoStore(db, key, invocation.VideoStoreOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(invocation.VideoPayloadInput{OrgID: 1, RequestID: "s2-migration-request", AttemptID: "s2-migration-attempt", Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeExecution, SchemaVersion: 1, Body: []byte(`{"model":"synthetic","prompt":"migration preservation"}`), CaptureEnabled: true, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Transaction(func(tx *gorm.DB) error { _, e := store.StageTx(tx, prepared); return e }); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLVideoQuerySettlementMigrationPreservesAndReplays(t *testing.T) {
	for completed := 0; completed <= 2; completed++ {
		t.Run(fmt.Sprintf("resume_after_%d_statements", completed), func(t *testing.T) {
			db := queryRecoveryMigrationDatabase(t) // dedicated random schema, session +08:00
			base, update := querySettlementMigrationFiles(t)
			if _, err := Up(db, base); err != nil {
				t.Fatal("tracked 133 baseline failed")
			}
			seedRuntimeLegacyRows(t, db, "qrecovery")
			querySettlementMigrationCipher(t, db)
			tables := inboxBusinessTables(t, db)
			before, columns := snapshotInboxExistingTables(t, db, tables)
			indexes := queryRecoveryIndexes(t, db)
			for _, table := range []string{"tasks", "video_task_states", "video_operations", "video_query_receipts", "video_billing_snapshots", "ledger_accounts", "ledger_holds", "ledger_journal", "invocation_payloads", "invocation_payload_staging"} {
				if before[table].Count == 0 {
					t.Fatal("preservation fixture empty", table)
				}
			}
			statements := splitStatements(string(update[querySettlementMigration].Data))
			if len(statements) != 2 || !strings.HasPrefix(statements[0], "ALTER TABLE video_task_states") || !strings.HasPrefix(statements[1], "ALTER TABLE video_query_receipts") {
				t.Fatal("expected two atomic table expands")
			}
			for _, sql := range statements[:completed] {
				if err := db.Exec(sql).Error; err != nil {
					t.Fatal("partial DDL failed")
				}
			}
			var recorded int64
			if err := db.Model(&Record{}).Where("version = ?", strings.TrimSuffix(querySettlementMigration, ".sql")).Count(&recorded).Error; err != nil || recorded != 0 {
				t.Fatal("partial DDL recorded completion")
			}
			if ran, err := Up(db, update); err != nil || !reflect.DeepEqual(ran, []string{strings.TrimSuffix(querySettlementMigration, ".sql")}) {
				t.Fatal("134 migration/replay failed", err)
			}
			if !reflect.DeepEqual(tables, inboxBusinessTables(t, db)) {
				t.Fatal("table inventory changed")
			}
			for table, old := range before {
				if !reflect.DeepEqual(old, snapshotRuntimeTable(t, db, table, old.Columns)) {
					t.Fatal("existing data/ciphertext changed", table)
				}
				got := inboxMigrationColumns(t, db, table)
				n := len(columns[table])
				switch table {
				case "video_task_states":
					if len(got) != n+1 {
						t.Fatal("pin column count")
					}
					querySettlementAssertColumns(t, got[n:], true)
				case "video_query_receipts":
					if len(got) != n+3 {
						t.Fatal("receipt column count")
					}
					querySettlementAssertColumns(t, got[n:], false)
				default:
					if len(got) != n {
						t.Fatal("unrelated schema changed")
					}
				}
				if !reflect.DeepEqual(got[:n], columns[table]) {
					t.Fatal("old column metadata changed", table)
				}
			}
			querySettlementAssertIndexes(t, db, indexes)
			querySettlementAssertWriters(t, db)
			// Old UPDATE cannot erase a committed phase/pin; interruption replay must
			// preserve it even when a previous migration record was never committed.
			at := queryRecoveryDatabaseTime(t, db)
			if err := db.Table("video_query_receipts").Where("attempt_id = ?", "query-runtime-qrecovery-0").Updates(map[string]any{"generation_applied_at": at, "settlement_kind": "usage", "settlement_evidence_sha256": strings.Repeat("a", 64), "recovery_version": 7}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Table("video_task_states").Where("task_id = ?", "runtime-qrecovery-0").Update("settlement_receipt_id", "query-runtime-qrecovery-0").Error; err != nil {
				t.Fatal(err)
			}
			phaseBefore := snapshotRuntimeTable(t, db, "video_query_receipts", []string{"attempt_id", "generation_applied_at", "settlement_kind", "settlement_evidence_sha256", "recovery_version"})
			if err := db.Exec("UPDATE video_query_receipts SET apply_error_code='old-writer-label' WHERE attempt_id='query-runtime-qrecovery-0'").Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(phaseBefore, snapshotRuntimeTable(t, db, "video_query_receipts", phaseBefore.Columns)) {
				t.Fatal("old UPDATE altered new phase")
			}
			full, _ := snapshotInboxExistingTables(t, db, tables)
			if err := db.Where("version = ?", strings.TrimSuffix(querySettlementMigration, ".sql")).Delete(&Record{}).Error; err != nil {
				t.Fatal(err)
			}
			if ran, err := Up(db, update); err != nil || len(ran) != 1 {
				t.Fatal("unrecorded whole DDL replay failed", err)
			}
			for table, old := range full {
				if !reflect.DeepEqual(old, snapshotRuntimeTable(t, db, table, nil)) {
					t.Fatal("replay overwrote current data", table)
				}
			}
			if ran, err := Up(db, update); err != nil || len(ran) != 0 {
				t.Fatal("recorded replay is not noop")
			}
			if drift, err := ledger.CheckDrift(db); err != nil || len(drift) != 0 {
				t.Fatal("ledger changed")
			}
		})
	}
}

func querySettlementAssertColumns(t *testing.T, cols []inboxMigrationColumn, pin bool) {
	t.Helper()
	type expect struct {
		name, typ, nullable, collation string
		defaultValue                   *string
	}
	empty := ""
	want := []expect{{"generation_applied_at", "datetime(3)", "YES", "", nil}, {"settlement_kind", "varchar(24)", "NO", "utf8mb4_0900_bin", &empty}, {"settlement_evidence_sha256", "char(64)", "NO", "ascii_bin", &empty}}
	if pin {
		want = []expect{{"settlement_receipt_id", "varchar(64)", "YES", "utf8mb4_bin", nil}}
	}
	if len(cols) != len(want) {
		t.Fatal("unexpected expanded schema")
	}
	for i, c := range cols {
		w := want[i]
		if c.ColumnName != w.name || c.ColumnType != w.typ || c.IsNullable != w.nullable || c.CollationName.String != w.collation || c.Extra != "" || c.GenerationExpression != "" || c.ColumnDefault.Valid != (w.defaultValue != nil) || (w.defaultValue != nil && c.ColumnDefault.String != *w.defaultValue) {
			t.Fatal("exact S2 column metadata differs", w.name)
		}
	}
}
func querySettlementAssertIndexes(t *testing.T, db *gorm.DB, old []queryRecoveryIndex) {
	t.Helper()
	want := map[string]string{"idx_video_query_phase_due": "runtime_version,adapter_version,protocol_contract_version,observation_schema_version,settlement_kind,applied_at,recovery_due_at,received_at,attempt_id", "idx_video_query_task_financial_conflict": "org_id,task_id,apply_error_code,attempt_id"}
	var kept []queryRecoveryIndex
	added := 0
	for _, idx := range queryRecoveryIndexes(t, db) {
		if cols, ok := want[idx.IndexName]; ok && idx.TableName == "video_query_receipts" {
			if idx.NonUnique != 1 || idx.ColumnList != cols {
				t.Fatal("wrong S2 index")
			}
			added++
			continue
		}
		kept = append(kept, idx)
	}
	if added != 2 || !reflect.DeepEqual(kept, old) {
		t.Fatal("S2 changed old index or lost one new index")
	}
}
func querySettlementAssertWriters(t *testing.T, db *gorm.DB) {
	t.Helper()
	var oldRows []video.QueryReceipt
	var oldStates []video.State
	if err := db.Find(&oldRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Find(&oldStates).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range oldRows {
		if r.GenerationAppliedAt != nil || r.SettlementKind != "" || r.SettlementEvidenceSHA256 != "" {
			t.Fatal("DDL invented generation")
		}
	}
	for _, s := range oldStates {
		if s.SettlementReceiptID != nil {
			t.Fatal("DDL invented pin")
		}
	}
	lower := queryRecoveryDatabaseTime(t, db)
	if err := db.Table("video_query_receipts").Create(queryRecoveryOldRow("s2-old-writer", 20, "")).Error; err != nil {
		t.Fatal("old INSERT failed")
	}
	row := video.QueryReceipt{AttemptID: "s2-new-v1-writer", OrgID: 1, TaskID: "runtime-qrecovery-0", Sequence: 21, RuntimeVersion: oldRows[0].RuntimeVersion, AdapterVersion: oldRows[0].AdapterVersion, ProtocolContractVersion: oldRows[0].ProtocolContractVersion, ObservationSchemaVersion: 1, HTTPStatus: 200, Outcome: "accepted", ObservationJSON: `{}`, ReceivedAt: lower}
	if err := db.Omit("RecoveryDueAt", "RecoveryVersion").Create(&row).Error; err != nil {
		t.Fatal("current V1 ORM writer failed")
	}
	upper := queryRecoveryDatabaseTime(t, db)
	for _, id := range []string{"s2-old-writer", "s2-new-v1-writer"} {
		var r video.QueryReceipt
		if err := db.Where("attempt_id = ?", id).Take(&r).Error; err != nil {
			t.Fatal(err)
		}
		queryRecoveryAssertUTCWindow(t, r.RecoveryDueAt, lower, upper)
		if r.RecoveryVersion != 0 || r.GenerationAppliedAt != nil || r.SettlementKind != "" || r.SettlementEvidenceSHA256 != "" {
			t.Fatal("writer changed phase/default")
		}
	}
}
