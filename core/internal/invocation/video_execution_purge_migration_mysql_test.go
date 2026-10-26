package invocation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/laiai/laiai-next/core/internal/migrate"
	"gorm.io/gorm"
)

const videoPurgeIndexMigration = "0132_video_payload_purge_indexes.sql"

type purgeIndexColumn struct {
	ColumnName, ColumnType, IsNullable string
	ColumnDefault                      sql.NullString
	Extra                              string
	CollationName                      sql.NullString
	GenerationExpression               string
}

type purgeIndexPart struct {
	IndexName, ColumnName, IndexType, IsVisible string
	SeqInIndex, NonUnique                       int
	SubPart                                     sql.NullInt64
}

func purgeIndexColumns(t *testing.T, db *gorm.DB) []purgeIndexColumn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var rows []purgeIndexColumn
	if err := db.WithContext(ctx).Raw("SELECT column_name,column_type,is_nullable,column_default,extra,collation_name,generation_expression FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='invocation_payloads' ORDER BY ordinal_position").Scan(&rows).Error; err != nil {
		t.Fatal("read purge migration columns", err)
	}
	return rows
}

func purgeIndexParts(t *testing.T, db *gorm.DB) []purgeIndexPart {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var rows []purgeIndexPart
	if err := db.WithContext(ctx).Raw("SELECT index_name,column_name,seq_in_index,non_unique,index_type,is_visible,sub_part FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='invocation_payloads' ORDER BY index_name,seq_in_index").Scan(&rows).Error; err != nil {
		t.Fatal("read purge migration indexes", err)
	}
	return rows
}

// Projection through the pre-existing model intentionally excludes derived
// columns. Hash all original encrypted metadata/staging/access fields, never
// print their values. Counts make an empty preservation fixture fail closed.
func purgeIndexOriginalFacts(t *testing.T, db *gorm.DB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var metadata []VideoPayload
	var staging []VideoPayloadStaging
	var access []VideoPayloadAccess
	for _, q := range []*gorm.DB{
		db.WithContext(ctx).Order("payload_id").Find(&metadata),
		db.WithContext(ctx).Order("payload_id").Find(&staging),
		db.WithContext(ctx).Order("access_id,phase").Find(&access),
	} {
		if q.Error != nil {
			t.Fatal("read original invocation projection", q.Error)
		}
	}
	if len(metadata) == 0 || len(staging) == 0 || len(access) == 0 {
		t.Fatal("empty encrypted migration preservation fixture")
	}
	raw, err := json.Marshal([]any{metadata, staging, access})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestMySQLVideoExecutionPurgeIndexMigration(t *testing.T) {
	for _, alreadyApplied := range []bool{false, true} {
		t.Run(fmt.Sprintf("ddl_committed_%t", alreadyApplied), func(t *testing.T) {
			s, db, _ := videoExecutionMySQLStoreWithPurgeIndex(t, false)
			in := videoInput(s)
			in.AttemptID = ""
			p := videoStage(t, s, db, in) // Actual pre-0132 generic writer.
			plain, err := s.ReadForExecutionSchema(context.Background(), 1, p.PayloadID(), 1, videoAccess(VideoRoleWorker))
			if err != nil || !bytes.Equal(plain, in.Body) {
				t.Fatal("old encrypted input is not readable", err)
			}
			clear(plain)
			before := purgeIndexOriginalFacts(t, db)
			columns, indexes := purgeIndexColumns(t, db), purgeIndexParts(t, db)
			_, file, _, ok := runtime.Caller(0)
			if !ok {
				t.Fatal("locate tracked purge migration")
			}
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations", videoPurgeIndexMigration))
			if err != nil {
				t.Fatal(err)
			}
			// One atomic ALTER is the only actual interruption boundary.
			if strings.Count(string(raw), ";") != 1 || strings.Count(string(raw), "ALTER TABLE") != 1 {
				t.Fatal("purge migration must remain one atomic ALTER")
			}
			if alreadyApplied {
				if err := db.Exec(string(raw)).Error; err != nil {
					t.Fatal("execute unrecorded purge ALTER", err)
				}
			}
			fs := fstest.MapFS{videoPurgeIndexMigration: &fstest.MapFile{Data: raw}}
			ran, err := migrate.Up(db, fs)
			if err != nil || !reflect.DeepEqual(ran, []string{"0132_video_payload_purge_indexes"}) {
				t.Fatal("purge migration/replay did not record exact version", ran, err)
			}
			if purgeIndexOriginalFacts(t, db) != before {
				t.Fatal("purge index DDL changed original encrypted data")
			}
			assertPurgeIndexSchema(t, db, columns, indexes)
			// The old writer continues to omit every added generated field.
			in.RequestID = "old-writer-after-0132"
			after := videoStage(t, s, db, in)
			plain, err = s.ReadForExecutionSchema(context.Background(), 1, after.PayloadID(), 1, videoAccess(VideoRoleWorker))
			if err != nil || !bytes.Equal(plain, in.Body) {
				t.Fatal("old writer/read format no longer works", err)
			}
			clear(plain)
			assertPurgeIndexTruth(t, db, s.now())
			facts := purgeIndexOriginalFacts(t, db)
			allColumns, allIndexes := purgeIndexColumns(t, db), purgeIndexParts(t, db)
			if ran, err = migrate.Up(db, fs); err != nil || len(ran) != 0 || purgeIndexOriginalFacts(t, db) != facts || !reflect.DeepEqual(allColumns, purgeIndexColumns(t, db)) || !reflect.DeepEqual(allIndexes, purgeIndexParts(t, db)) {
				t.Fatal("recorded purge migration is not a no-op", ran, err)
			}
		})
	}
}

func assertPurgeIndexSchema(t *testing.T, db *gorm.DB, oldColumns []purgeIndexColumn, oldIndexes []purgeIndexPart) {
	t.Helper()
	columns := purgeIndexColumns(t, db)
	if len(columns) != len(oldColumns)+2 || !reflect.DeepEqual(columns[:len(oldColumns)], oldColumns) {
		t.Fatal("purge migration changed existing columns")
	}
	wantExpressions := map[string]bool{
		"purge_is_execution":  true,
		"purge_needs_erasure": true,
	}
	for _, column := range columns[len(oldColumns):] {
		if !wantExpressions[column.ColumnName] || column.ColumnType != "tinyint unsigned" || column.IsNullable != "YES" || column.ColumnDefault.Valid || column.Extra != "VIRTUAL GENERATED" || column.CollationName.Valid || !purgeIndexExpressionMatches(column.ColumnName, column.GenerationExpression) {
			t.Fatal("purge generated column metadata differs from the exact contract", column.ColumnName)
		}
		delete(wantExpressions, column.ColumnName)
	}
	if len(wantExpressions) != 0 {
		t.Fatal("missing purge generated column")
	}
	wantIndexes := map[string][]string{
		"idx_video_payload_purge_due":         {"purge_is_execution", "purge_needs_erasure", "expires_at", "payload_id", "org_id"},
		"idx_video_payload_cleanup_partition": {"purge_is_execution", "capture_status", "object_deleted_at", "cleanup_after", "payload_id", "org_id"},
	}
	var preserved []purgeIndexPart
	seen := map[string]int{}
	for _, part := range purgeIndexParts(t, db) {
		want, added := wantIndexes[part.IndexName]
		if !added {
			preserved = append(preserved, part)
			continue
		}
		i := seen[part.IndexName]
		if i >= len(want) || part.ColumnName != want[i] || part.SeqInIndex != i+1 || part.NonUnique != 1 || part.IndexType != "BTREE" || part.IsVisible != "YES" || part.SubPart.Valid {
			t.Fatal("purge covering index order/type changed", part.IndexName)
		}
		seen[part.IndexName]++
	}
	if !reflect.DeepEqual(preserved, oldIndexes) {
		t.Fatal("purge migration removed or changed an old index")
	}
	for name, want := range wantIndexes {
		if seen[name] != len(want) {
			t.Fatal("missing purge index components", name)
		}
	}
}

// MySQL 8.4 prints CAST AS BINARY as CAST AS CHAR CHARSET binary and escapes
// the literal quotes in GENERATION_EXPRESSION. The dev3 information_schema
// HEX probe established those bytes independently of the mysql/JSON display.
// Accept only the exact source expression or that exact server spelling:
// never lowercase quoted values or strip syntax that could change semantics.
func purgeIndexExpressionMatches(column, expression string) bool {
	var source, server string
	switch column {
	case "purge_is_execution":
		source = "CAST(purpose AS BINARY) = CAST(_utf8mb4'execution' AS BINARY)"
		server = "(cast(`purpose` as char charset binary) = cast(_utf8mb4\\'execution\\' as char charset binary))"
	case "purge_needs_erasure":
		source = "capture_status <> _utf8mb4'expired' COLLATE utf8mb4_bin"
		server = "(`capture_status` <> (_utf8mb4\\'expired\\' collate utf8mb4_bin))"
	default:
		return false
	}
	expression = strings.TrimSpace(expression)
	return expression == source || expression == server
}

func TestPurgeIndexExpressionContract(t *testing.T) {
	for _, tc := range []struct {
		column, expression string
	}{
		{"purge_is_execution", "CAST(purpose AS BINARY) = CAST(_utf8mb4'execution' AS BINARY)"},
		{"purge_is_execution", "(cast(`purpose` as char charset binary) = cast(_utf8mb4\\'execution\\' as char charset binary))"},
		{"purge_needs_erasure", "capture_status <> _utf8mb4'expired' COLLATE utf8mb4_bin"},
		{"purge_needs_erasure", "(`capture_status` <> (_utf8mb4\\'expired\\' collate utf8mb4_bin))"},
	} {
		if !purgeIndexExpressionMatches(tc.column, tc.expression) {
			t.Fatal("rejected exact source/server expression", tc.column)
		}
		bad := []string{
			strings.ReplaceAll(tc.expression, "binary", "utf8mb4"),
			strings.ReplaceAll(tc.expression, "BINARY", "CHAR"),
			strings.ReplaceAll(tc.expression, "purpose", "purpose_other"),
			strings.ReplaceAll(tc.expression, "execution", "Execution"),
			strings.ReplaceAll(tc.expression, "execution", "execution "),
			strings.ReplaceAll(tc.expression, "capture_status", "capture_status_other"),
			strings.ReplaceAll(tc.expression, "expired", "Expired"),
			strings.ReplaceAll(tc.expression, "utf8mb4_bin", "utf8mb4_0900_bin"),
			strings.ReplaceAll(tc.expression, " COLLATE utf8mb4_bin", ""),
			strings.ReplaceAll(tc.expression, " collate utf8mb4_bin", ""),
			tc.expression + " OR 1=1",
		}
		for _, expression := range bad {
			if expression != tc.expression && purgeIndexExpressionMatches(tc.column, expression) {
				t.Fatal("accepted changed expression semantics", tc.column)
			}
		}
	}
	if purgeIndexExpressionMatches("unknown", "") || purgeIndexExpressionMatches("purge_is_execution", "") {
		t.Fatal("empty or unknown expression accepted")
	}
}

func assertPurgeIndexTruth(t *testing.T, db *gorm.DB, now time.Time) {
	t.Helper()
	// Literal expectations are independent of SQL collation and both the old
	// and new predicates. These are metadata-only rows, never ciphertext.
	purposes := []struct {
		value string
		want  int
	}{{"execution", 1}, {"execution ", 0}, {"Execution", 0}, {"history", 0}, {"delivery", 0}, {"future", 0}, {"", 0}}
	statuses := []struct {
		value string
		want  int
	}{{"staged", 1}, {"stored", 1}, {"disabled", 1}, {"future", 1}, {"Expired", 1}, {"expired", 0}, {"expired ", 0}, {"", 1}}
	for i, purpose := range purposes {
		for j, status := range statuses {
			id := fmt.Sprintf("truth-%d-%d", i, j)
			row := VideoPayload{PayloadID: id, OrgID: 7, RequestID: id, CorrelationKey: fmt.Sprintf("%064x", 1000+i*10+j), Direction: VideoEffectiveRequest, Purpose: purpose.value, SchemaVersion: 99, BodyFormat: "json", Nonce: []byte{}, RedactionVersion: "metadata-only", CaptureStatus: status.value, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal("old metadata INSERT omitted generated fields", err)
			}
			var flags struct{ Execution, Erasure, OriginalExecution, OriginalErasure int }
			if err := db.Raw("SELECT purge_is_execution AS execution,purge_needs_erasure AS erasure,(BINARY purpose=BINARY 'execution') AS original_execution,(capture_status<>'expired') AS original_erasure FROM invocation_payloads WHERE payload_id=?", id).Scan(&flags).Error; err != nil || flags.Execution != purpose.want || flags.Erasure != status.want || flags.OriginalExecution != purpose.want || flags.OriginalErasure != status.want {
				t.Fatal("generated flags changed exact-purpose/PAD-status semantics", i, j, err)
			}
		}
	}
	// Updating only old columns must move the derived candidate automatically.
	if q := db.Model(&VideoPayload{}).Where("payload_id = ?", "truth-0-0").Updates(map[string]any{"purpose": "execution ", "capture_status": "expired "}); q.Error != nil || q.RowsAffected != 1 {
		t.Fatal("old metadata UPDATE failed", q.Error)
	}
	var flags struct{ Execution, Erasure int }
	if err := db.Raw("SELECT purge_is_execution AS execution,purge_needs_erasure AS erasure FROM invocation_payloads WHERE payload_id='truth-0-0'").Scan(&flags).Error; err != nil || flags.Execution != 0 || flags.Erasure != 0 {
		t.Fatal("old UPDATE did not maintain generated index qualification", err)
	}
}
