package invocation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A private fixture chooses time BEFORE encryption, never UPDATEs expiry/AAD.
// Only the regular factory test below claims the production fixed 24h policy.
func executionShortPrepared(t *testing.T, s *VideoStore, db *gorm.DB, request string, schema uint32, ttl time.Duration) *PreparedVideoPayload {
	t.Helper()
	now := videoExecutionDatabaseNow(t, db)
	attempt := ""
	if schema == 2 {
		attempt = "claim_" + request
	}
	p, err := s.prepareAt(VideoPayloadInput{OrgID: 1, RequestID: request, AttemptID: attempt, Purpose: VideoPurposeExecution, Direction: VideoEffectiveRequest, SchemaVersion: schema, CaptureEnabled: true, Body: []byte(`{"prompt":"quiet sea"}`), ExpiresAt: now.Add(ttl)}, now)
	if err != nil {
		t.Fatal(err)
	}
	p.executionPrepared = true
	return p
}

func executionStage(t *testing.T, s *VideoStore, db *gorm.DB, p *PreparedVideoPayload) {
	t.Helper()
	if err := db.Transaction(func(tx *gorm.DB) error { _, err := s.StageExecutionTx(tx, p); return err }); err != nil {
		t.Fatal(err)
	}
}

func executionPayloadCounts(t *testing.T, db *gorm.DB) (int64, int64) {
	t.Helper()
	var m, n int64
	if err := db.Model(&VideoPayload{}).Count(&m).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&VideoPayloadStaging{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return m, n
}

func TestMySQLVideoExecutionPrepareUsesPrimaryTransaction(t *testing.T) {
	s, db, host := videoExecutionMySQLStore(t)
	// Action preparation's standalone clock transaction is genuinely READ
	// ONLY. This uses a real forbidden INSERT; no production clock is replaced.
	var readonlyHits atomic.Int32
	var readonlyOK atomic.Bool
	const readonlyName = "execution_action_readonly_positive"
	if err := db.Callback().Row().Before("gorm:row").Register(readonlyName, func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") {
			readonlyHits.Add(1)
			probe := VideoPayloadAccess{AccessID: "vac_readonly_probe", Phase: "started", OrgID: 1, PayloadID: "vpl_probe", ActorRole: VideoRoleWorker, ActorID: "worker", ReasonCode: "fixture", Outcome: "started", CreatedAt: time.Now().UTC()}
			e := q.Session(&gorm.Session{NewDB: true}).Create(&probe).Error
			var mysqlErr *drivermysql.MySQLError
			readonlyOK.Store(errors.As(e, &mysqlErr) && mysqlErr.Number == 1792)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, readonlyErr := s.PrepareActionExecution(context.Background(), VideoExecutionInput{OrgID: 1, RequestID: "readonly_clock", AttemptID: "claim", Body: []byte(`{}`)})
	db.Callback().Row().Remove(readonlyName)
	if readonlyErr != nil || readonlyHits.Load() != 1 || !readonlyOK.Load() {
		t.Fatal("action clock transaction was not real read-only", readonlyErr)
	}
	for _, shift := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		*host = videoExecutionDatabaseNow(t, db).Add(shift)
		for _, schema := range []uint32{1, 2} {
			name := fmt.Sprintf("s%d_%d", schema, shift/time.Hour)
			before := videoExecutionDatabaseNow(t, db)
			in := VideoExecutionInput{OrgID: 1, RequestID: name, Body: []byte(`{"prompt":"quiet sea"}`)}
			var p *PreparedVideoPayload
			var err error
			if schema == 1 {
				err = db.Transaction(func(tx *gorm.DB) error { p, err = s.PrepareCreateExecutionTx(tx, in); return err })
			} else {
				in.AttemptID = "claim_" + name
				p, err = s.PrepareActionExecution(context.Background(), in)
			}
			if err != nil {
				t.Fatal(err)
			}
			after := videoExecutionDatabaseNow(t, db)
			if p.metadata.CreatedAt.Before(before) || p.metadata.CreatedAt.After(after) || p.metadata.ExpiresAt.Sub(p.metadata.CreatedAt) != 24*time.Hour || p.metadata.SchemaVersion != schema {
				t.Fatal("factory used host time or changed fixed TTL/schema")
			}
			executionStage(t, s, db, p)
			var saved VideoPayload
			if err := db.Where("payload_id = ?", p.PayloadID()).Take(&saved).Error; err != nil {
				t.Fatal(err)
			}
			executionStage(t, s, db, p)
			var replay VideoPayload
			if err := db.Where("payload_id = ?", p.PayloadID()).Take(&replay).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved, replay) {
				t.Fatal("same prepared replay mutated evidence")
			}
			newPrepared, err := s.prepareExecutionAt(in, schema, after)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Transaction(func(tx *gorm.DB) error { _, err := s.StageExecutionTx(tx, newPrepared); return err })
			if !errors.Is(err, ErrVideoConflict) {
				t.Fatal("fresh ciphertext overwrote occupied correlation")
			}
		}
	}
	beforeM, beforeS := executionPayloadCounts(t, db)
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rollback := errors.New("rollback complete business transaction")
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p, err := s.PrepareCreateExecutionTx(tx, VideoExecutionInput{OrgID: 1, RequestID: "one_connection", Body: []byte(`{}`)})
		if err != nil {
			return err
		}
		if _, err = s.StageExecutionTx(tx, p); err != nil {
			return err
		}
		w, err := CheckVideoExecutionWindowTx(tx, 1, p.PayloadID(), 1)
		if err != nil {
			return err
		}
		if w.Identity.PayloadID != p.PayloadID() {
			t.Fatal("original tx could not see its own staged input")
		}
		return rollback
	})
	pool.SetMaxOpenConns(8)
	if !errors.Is(err, rollback) {
		t.Fatal("factory/window tried to acquire another pool connection", err)
	}
	afterM, afterS := executionPayloadCounts(t, db)
	if beforeM != afterM || beforeS != afterS {
		t.Fatal("outer rollback left execution data")
	}
	// Separate connection proves uncommitted metadata is not externally visible.
	tx := db.Begin()
	defer tx.Rollback()
	p, err := s.PrepareCreateExecutionTx(tx, VideoExecutionInput{OrgID: 1, RequestID: "uncommitted", Body: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageExecutionTx(tx, p); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("second connection observed uncommitted input")
	}
}

func TestMySQLVideoExecutionStageRechecksAfterLockAndRollsBack(t *testing.T) {
	s, db, _ := videoExecutionMySQLStore(t)
	p := executionShortPrepared(t, s, db, "lock_expiry", 1, 650*time.Millisecond)
	executionStage(t, s, db, p)
	locker := db.Begin()
	defer locker.Rollback()
	var row VideoPayload
	if err := locker.Clauses(clause.Locking{Strength: "UPDATE"}).Where("payload_id = ?", p.PayloadID()).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	ready := make(chan uint64, 1)
	done := make(chan error, 1)
	go func() {
		done <- db.Transaction(func(tx *gorm.DB) error {
			var id uint64
			if err := tx.Raw("SELECT CONNECTION_ID()").Scan(&id).Error; err != nil {
				return err
			}
			ready <- id
			_, err := s.StageExecutionTx(tx, p)
			return err
		})
	}()
	var connection uint64
	select {
	case connection = <-ready:
	case <-time.After(time.Second):
		t.Fatal("stage did not start")
	}
	// Prove the independent transaction actually waited on the metadata lock.
	waiting := false
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		var n int64
		err := db.Raw(`SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.threads th ON th.THREAD_ID=w.REQUESTING_THREAD_ID WHERE th.PROCESSLIST_ID=?`, connection).Scan(&n).Error
		if err != nil {
			t.Fatal("observe actual Stage lock wait")
		}
		if n > 0 {
			waiting = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("Stage did not actually wait on exact metadata")
	}
	videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
	if err := locker.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrVideoExpired) {
			t.Fatal("post-lock Stage did not use current DB time", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stage remained blocked")
	}
	var after VideoPayload
	if err := db.Where("payload_id = ?", p.PayloadID()).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row, after) {
		t.Fatal("expired replay changed original metadata")
	}
	// A genuine SQL clock failure after both inserts must roll back this
	// savepoint even when the outer transaction handles the error and commits.
	p = executionShortPrepared(t, s, db, "clock_failure", 2, time.Hour)
	var hits atomic.Int32
	const callback = "execution_stage_clock_failure"
	if err := db.Callback().Row().Before("gorm:row").Register(callback, func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") {
			hits.Add(1)
			q.Statement.SQL.Reset()
			q.Statement.SQL.WriteString("SELECT unavailable_execution_clock_column")
			q.Statement.Vars = nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := s.StageExecutionTx(tx, p)
		if !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) {
			return errors.New("expected authority SQL failure")
		}
		var n int64
		if err := tx.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return errors.New("handled error left metadata")
		}
		return nil
	})
	db.Callback().Row().Remove(callback)
	if err != nil || hits.Load() != 1 {
		t.Fatal("clock SQL failure/savepoint was not exercised", err, hits.Load())
	}
	var count int64
	if err := db.Model(&VideoPayloadStaging{}).Where("payload_id = ?", p.PayloadID()).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed savepoint left staging")
	}
	// A new pair (not just a replay) expires after its actual staging INSERT.
	// The caller deliberately handles expiry and commits to prove rollback of
	// both new records belongs to Stage's own savepoint.
	p = executionShortPrepared(t, s, db, "insert_expiry", 1, 450*time.Millisecond)
	var inserted atomic.Int32
	const afterInsert = "execution_stage_insert_wait"
	if err := db.Callback().Create().After("gorm:create").Register(afterInsert, func(q *gorm.DB) {
		if stage, ok := q.Statement.Dest.(*VideoPayloadStaging); ok && stage.PayloadID == p.PayloadID() && q.Error == nil {
			inserted.Add(1)
			videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := s.StageExecutionTx(tx, p)
		if !errors.Is(err, ErrVideoExpired) {
			return fmt.Errorf("expected post-insert expiry: %w", err)
		}
		return nil
	})
	db.Callback().Create().Remove(afterInsert)
	if err != nil || inserted.Load() != 1 {
		t.Fatal("post-insert expiry was not exercised", err)
	}
	for _, model := range []any{&VideoPayload{}, &VideoPayloadStaging{}} {
		var n int64
		if err := db.Model(model).Where("payload_id = ?", p.PayloadID()).Count(&n).Error; err != nil || n != 0 {
			t.Fatal("handled expiry left a partial new pair")
		}
	}
	// Timeout after a successful INSERT invalidates only the method's child
	// ctx. Savepoint rollback must use a fresh bounded ctx on the SAME tx.
	p = executionShortPrepared(t, s, db, "insert_timeout", 1, time.Hour)
	inserted.Store(0)
	if err := db.Callback().Create().After("gorm:create").Register(afterInsert, func(q *gorm.DB) {
		if stage, ok := q.Statement.Dest.(*VideoPayloadStaging); ok && stage.PayloadID == p.PayloadID() && q.Error == nil {
			inserted.Add(1)
			<-q.Statement.Context.Done()
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := s.StageExecutionTx(tx, p)
		if !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
			return errors.New("child timeout lost safe authority cause")
		}
		return nil
	})
	db.Callback().Create().Remove(afterInsert)
	if err != nil || inserted.Load() != 1 {
		t.Fatal("savepoint cleanup after child deadline failed", err)
	}
	for _, model := range []any{&VideoPayload{}, &VideoPayloadStaging{}} {
		var n int64
		if err := db.Model(model).Where("payload_id = ?", p.PayloadID()).Count(&n).Error; err != nil || n != 0 {
			t.Fatal("expired child ctx prevented savepoint rollback")
		}
	}
}
