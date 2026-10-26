package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type executionWindowBlobs struct {
	body []byte
	get  func(context.Context) error
}

func (b *executionWindowBlobs) Put(context.Context, string, []byte) error {
	return errors.New("fixture does not upload")
}
func (b *executionWindowBlobs) Delete(context.Context, string) error {
	return errors.New("fixture does not delete")
}
func (b *executionWindowBlobs) Get(ctx context.Context, _ string) ([]byte, error) {
	if b.get != nil {
		if err := b.get(ctx); err != nil {
			return nil, err
		}
	}
	return append([]byte(nil), b.body...), nil
}

func executionRead(s *VideoStore, ctx context.Context, p *PreparedVideoPayload) ([]byte, error) {
	if p.metadata.SchemaVersion == 2 {
		return s.ReadForActionExecutionWithDatabaseTime(ctx, 1, p.PayloadID(), videoAccess(VideoRoleWorker))
	}
	return s.ReadForExecutionSchemaWithDatabaseTime(ctx, 1, p.PayloadID(), 1, videoAccess(VideoRoleWorker))
}

func executionAuditCount(t *testing.T, db *gorm.DB, id, outcome string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&VideoPayloadAccess{}).Where("payload_id = ? AND phase = 'finished' AND outcome = ?", id, outcome).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMySQLVideoExecutionReadUsesCurrentWindow(t *testing.T) {
	s, db, host := videoExecutionMySQLStore(t)
	for _, schema := range []uint32{1, 2} {
		p := executionShortPrepared(t, s, db, fmt.Sprintf("read_%d", schema), schema, time.Hour)
		executionStage(t, s, db, p)
		*host = videoExecutionDatabaseNow(t, db).Add(24 * time.Hour)
		plain, err := executionRead(s, context.Background(), p)
		if err != nil || string(plain) != `{"prompt":"quiet sea"}` {
			t.Fatal("valid DB window used host expiry", err)
		}
		clear(plain)
		if executionAuditCount(t, db, p.PayloadID(), "succeeded") != 1 {
			t.Fatal("successful read was not audited")
		}
		// A real RR snapshot is deliberately stale, while FOR SHARE must see
		// the independent writer's latest capture lifecycle.
		tx := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		var prior VideoPayload
		if err := tx.Where("payload_id = ?", p.PayloadID()).Take(&prior).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Update("capture_status", VideoCaptureExpired).Error; err != nil {
			t.Fatal(err)
		}
		var snapshot VideoPayload
		if err := tx.Where("payload_id = ?", p.PayloadID()).Take(&snapshot).Error; err != nil || snapshot.CaptureStatus != VideoCaptureStaged {
			t.Fatal("RR positive control did not remain old")
		}
		var w VideoExecutionWindow
		if schema == 1 {
			w, err = CheckVideoExecutionWindowTx(tx, 1, p.PayloadID(), 1)
		} else {
			w, err = CheckVideoActionExecutionWindowTx(tx, 1, p.PayloadID())
		}
		if !errors.Is(err, ErrVideoExpired) || w.Identity.PayloadID != "" {
			t.Fatal("window used stale RR snapshot", err)
		}
		if err := tx.Rollback().Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestMySQLVideoExecutionReaderRejectsAfterBlobAndAuditWait(t *testing.T) {
	s, db, _ := videoExecutionMySQLStore(t)
	p := executionShortPrepared(t, s, db, "blob_expiry", 2, 600*time.Millisecond)
	executionStage(t, s, db, p)
	// Model an already stored object using exact ciphertext and immutable
	// metadata. This test does not enable or claim to validate UploadPending.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(map[string]any{"capture_status": VideoCaptureStored, "object_ref": "private-fixture-object"}).Error; err != nil {
			return err
		}
		return tx.Where("payload_id = ?", p.PayloadID()).Delete(&VideoPayloadStaging{}).Error
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.blobs = &executionWindowBlobs{body: p.staging.Ciphertext, get: func(ctx context.Context) error {
		calls.Add(1)
		// NOWAIT succeeds only if the first reader DB stage has released its
		// metadata share lock before entering Blob Get.
		locker := db.WithContext(ctx).Begin()
		defer locker.Rollback()
		var m VideoPayload
		if err := locker.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("payload_id = ?", p.PayloadID()).Take(&m).Error; err != nil {
			return err
		}
		if err := locker.Commit().Error; err != nil {
			return err
		}
		videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
		return nil
	}}
	plain, err := executionRead(s, context.Background(), p)
	if len(plain) != 0 || !errors.Is(err, ErrVideoExpired) || calls.Load() != 1 {
		t.Fatal("expired Blob plaintext escaped/no-lock positive control failed", err)
	}
	if executionAuditCount(t, db, p.PayloadID(), "succeeded") != 0 || executionAuditCount(t, db, p.PayloadID(), "denied") != 1 {
		t.Fatal("Blob expiry audit is misleading")
	}
	// A completed success-audit INSERT is itself a possible delay boundary.
	p = executionShortPrepared(t, s, db, "audit_expiry", 1, 500*time.Millisecond)
	executionStage(t, s, db, p)
	var hit atomic.Int32
	const name = "execution_success_audit_wait"
	if err := db.Callback().Create().After("gorm:create").Register(name, func(q *gorm.DB) {
		if a, ok := q.Statement.Dest.(*VideoPayloadAccess); ok && a.PayloadID == p.PayloadID() && a.Outcome == "succeeded" && q.Error == nil {
			hit.Add(1)
			videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
		}
	}); err != nil {
		t.Fatal(err)
	}
	plain, err = executionRead(s, context.Background(), p)
	db.Callback().Create().Remove(name)
	if len(plain) != 0 || !errors.Is(err, ErrVideoExpired) || hit.Load() != 1 {
		t.Fatal("reader omitted clock after success audit", err)
	}
	if executionAuditCount(t, db, p.PayloadID(), "succeeded") != 0 || executionAuditCount(t, db, p.PayloadID(), "denied") != 1 {
		t.Fatal("rolled-back success audit survived")
	}
}

func TestMySQLVideoExecutionAuthorityFaultsNeverReturnPlaintext(t *testing.T) {
	for _, point := range []string{"before_read", "after_decrypt", "after_success_audit"} {
		t.Run(point, func(t *testing.T) {
			s, db, _ := videoExecutionMySQLStore(t)
			p := executionShortPrepared(t, s, db, point, 1, time.Hour)
			executionStage(t, s, db, p)
			var before VideoPayload
			if err := db.Where("payload_id = ?", p.PayloadID()).Take(&before).Error; err != nil {
				t.Fatal(err)
			}
			var clocks, failures, metadataQueries atomic.Int32
			var succeeded atomic.Bool
			const rowName = "execution_reader_clock_failure"
			const auditName = "execution_reader_audit_observe"
			const metadataName = "execution_reader_metadata_failure"
			if err := db.Callback().Query().Before("gorm:query").Register(metadataName, func(q *gorm.DB) {
				if q.Statement.Table == "invocation_payloads" && metadataQueries.Add(1) == 2 && point == "after_decrypt" {
					failures.Add(1)
					q.Statement.Table = "missing_private_execution_metadata"
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Create().After("gorm:create").Register(auditName, func(q *gorm.DB) {
				if a, ok := q.Statement.Dest.(*VideoPayloadAccess); ok && a.Outcome == "succeeded" && q.Error == nil {
					succeeded.Store(true)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Row().Before("gorm:row").Register(rowName, func(q *gorm.DB) {
				if !strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") {
					return
				}
				n := clocks.Add(1)
				fail := point == "before_read" && n == 1 || point == "after_success_audit" && succeeded.Load()
				if fail {
					failures.Add(1)
					q.Statement.SQL.Reset()
					q.Statement.SQL.WriteString("SELECT private_execution_clock_failure")
					q.Statement.Vars = nil
				}
			}); err != nil {
				t.Fatal(err)
			}
			plain, err := executionRead(s, context.Background(), p)
			db.Callback().Row().Remove(rowName)
			db.Callback().Create().Remove(auditName)
			db.Callback().Query().Remove(metadataName)
			if len(plain) != 0 || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) || failures.Load() != 1 || strings.Contains(err.Error(), "private_execution") {
				t.Fatal("real clock fault returned plaintext/lost authority/safe error", err)
			}
			var after VideoPayload
			if err := db.Where("payload_id = ?", p.PayloadID()).Take(&after).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || executionAuditCount(t, db, p.PayloadID(), "succeeded") != 0 {
				t.Fatal("failed read mutated input or claimed success")
			}
			if point == "after_success_audit" && !succeeded.Load() {
				t.Fatal("fault did not reach actual success audit boundary")
			}
			if point == "after_decrypt" && (metadataQueries.Load() != 2 || succeeded.Load()) {
				t.Fatal("metadata fault did not precede final success audit")
			}
		})
	}
}

func TestMySQLVideoExecutionIdentityPrecedesExpiry(t *testing.T) {
	s, db, _ := videoExecutionMySQLStore(t)
	for _, kind := range []string{"schema", "purpose", "correlation", "foreign_org", "schema1_reader_schema2", "schema2_reader_schema1"} {
		schema := uint32(1)
		if kind == "schema1_reader_schema2" {
			schema = 2
		}
		p := executionShortPrepared(t, s, db, kind, schema, time.Hour)
		executionStage(t, s, db, p)
		changes := map[string]any{"capture_status": VideoCaptureExpired}
		want := ErrVideoSchemaUnsupported
		switch kind {
		case "schema":
			changes["schema_version"] = 99
		case "purpose":
			changes["purpose"] = VideoPurposeHistory
			want = ErrVideoAccessDenied
		case "correlation":
			changes["correlation_key"] = strings.Repeat("a", 64)
			want = ErrVideoCorrupt
		case "foreign_org":
			changes["org_id"] = 2
			want = ErrVideoNotFound
		}
		if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(changes).Error; err != nil {
			t.Fatal(err)
		}
		// Metadata-only identity priority, before an expired capture/DEK check.
		var plain []byte
		var err error
		if kind == "schema2_reader_schema1" {
			plain, err = s.ReadForActionExecutionWithDatabaseTime(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker))
		} else {
			plain, err = s.ReadForExecutionSchemaWithDatabaseTime(context.Background(), 1, p.PayloadID(), 1, videoAccess(VideoRoleWorker))
		}
		if len(plain) != 0 || !errors.Is(err, want) || errors.Is(err, ErrVideoExpired) {
			t.Fatal("expiry overrode fixed identity", kind, err)
		}
		if executionAuditCount(t, db, p.PayloadID(), "succeeded") != 0 {
			t.Fatal("denied input was marked successful")
		}
	}
	// A failed audit cannot replace the interpretation denial with generic I/O.
	p := executionShortPrepared(t, s, db, "unknown_and_audit_failure", 1, time.Hour)
	executionStage(t, s, db, p)
	if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(map[string]any{"schema_version": 99, "capture_status": VideoCaptureExpired}).Error; err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	const name = "execution_denial_audit_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(q *gorm.DB) {
		if a, ok := q.Statement.Dest.(*VideoPayloadAccess); ok && a.Phase == "finished" {
			hits.Add(1)
			q.Statement.Table = "missing_private_audit_table"
		}
	}); err != nil {
		t.Fatal(err)
	}
	plain, err := executionRead(s, context.Background(), p)
	db.Callback().Create().Remove(name)
	if len(plain) != 0 || !errors.Is(err, ErrVideoSchemaUnsupported) || !errors.Is(err, ErrVideoAuditUnavailable) || hits.Load() != 1 {
		t.Fatal("audit failure erased unknown-input priority", err)
	}
	// The first audit INSERT fails too: fixed identity must have been checked
	// already, so neither unknown schema nor corrupt correlation is hidden.
	for _, corrupt := range []bool{false, true} {
		p = executionShortPrepared(t, s, db, fmt.Sprintf("started_audit_failure_%t", corrupt), 1, time.Hour)
		executionStage(t, s, db, p)
		updates := map[string]any{"schema_version": 99, "capture_status": VideoCaptureExpired}
		want := ErrVideoSchemaUnsupported
		if corrupt {
			delete(updates, "schema_version")
			updates["correlation_key"] = strings.Repeat("b", 64)
			want = ErrVideoCorrupt
		}
		if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
		hits.Store(0)
		if err := db.Callback().Create().Before("gorm:create").Register(name, func(q *gorm.DB) {
			if a, ok := q.Statement.Dest.(*VideoPayloadAccess); ok && a.Phase == "started" {
				hits.Add(1)
				q.Statement.Table = "missing_private_audit_table"
			}
		}); err != nil {
			t.Fatal(err)
		}
		plain, err = executionRead(s, context.Background(), p)
		db.Callback().Create().Remove(name)
		if len(plain) != 0 || !errors.Is(err, want) || !errors.Is(err, ErrVideoAuditUnavailable) || hits.Load() != 1 {
			t.Fatal("started audit hid immutable identity", err)
		}
	}
}

func TestMySQLVideoExecutionReadCancellationPreservesAuthority(t *testing.T) {
	for _, at := range []int32{1, 2} {
		t.Run(fmt.Sprintf("clock_%d", at), func(t *testing.T) {
			s, db, _ := videoExecutionMySQLStore(t)
			p := executionShortPrepared(t, s, db, "cancelled_read", 2, time.Hour)
			executionStage(t, s, db, p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var hits atomic.Int32
			const name = "execution_read_parent_cancel"
			if err := db.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
				if strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") && hits.Add(1) == at {
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			plain, err := executionRead(s, ctx, p)
			db.Callback().Row().Remove(name)
			if len(plain) != 0 || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) || hits.Load() != at {
				t.Fatal("canceled real DB phase returned plaintext or lost safe cause", err)
			}
			if executionAuditCount(t, db, p.PayloadID(), "succeeded") != 0 {
				t.Fatal("canceled read committed success audit")
			}
			// Canceled SQL/transaction released its pool connection; a new request
			// still reads the exact original encrypted input successfully.
			plain, err = executionRead(s, context.Background(), p)
			if err != nil || string(plain) != `{"prompt":"quiet sea"}` {
				t.Fatal("cancellation damaged evidence or pool", err)
			}
			clear(plain)
		})
	}
}
