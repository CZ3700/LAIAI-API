package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A dialect name alias exercises rejection and real SQL errors only. It is
// never accepted as evidence that SQLite implements primary MySQL time.
type executionClockTestDialect struct{ gorm.Dialector }

func (executionClockTestDialect) Name() string { return "mysql" }

// Complete the real metadata SELECT, then model a driver diagnostic arriving
// concurrently with cancellation. This tests error causality, not MySQL time.
type executionMetadataCancelPool struct {
	gorm.ConnPool
	cancel  context.CancelFunc
	queries int
}

func (p *executionMetadataCancelPool) Commit() error { return p.ConnPool.(gorm.TxCommitter).Commit() }
func (p *executionMetadataCancelPool) Rollback() error {
	return p.ConnPool.(gorm.TxCommitter).Rollback()
}
func (p *executionMetadataCancelPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := p.ConnPool.QueryContext(ctx, query, args...)
	if err != nil {
		return rows, err
	}
	if strings.Contains(query, "invocation_payloads") {
		p.queries++
		_ = rows.Close()
		p.cancel()
		return nil, errors.New("private-metadata-driver-detail")
	}
	return rows, nil
}

func TestVideoExecutionAuthorityRejectsAndPreservesErrors(t *testing.T) {
	var output bytes.Buffer
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info})})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	tx := db.Begin()
	defer tx.Rollback()
	alias := func(d *gorm.DB) *gorm.DB {
		q := d.Session(&gorm.Session{})
		q.Dialector = executionClockTestDialect{d.Dialector}
		return q
	}
	for _, q := range []*gorm.DB{nil, {}, db, tx, alias(db), alias(tx.Session(&gorm.Session{DryRun: true}))} {
		got, err := videoExecutionAuthorityTimeTx(q)
		if !got.IsZero() || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) || err.Error() != ErrVideoExecutionAuthorityUnavailable.Error() {
			t.Fatal("unsupported clock handle was accepted")
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("private-dsn-password-and-SQL")} {
		q := alias(tx)
		q.Error = cause
		got, err := videoExecutionAuthorityTimeTx(q)
		if !got.IsZero() || !errors.Is(err, cause) || err.Error() != ErrVideoExecutionAuthorityUnavailable.Error() {
			t.Fatal("clock cause lost or leaked")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := videoExecutionAuthorityTimeTx(alias(tx.WithContext(ctx))); !errors.Is(err, context.Canceled) {
		t.Fatal("parent cancellation lost")
	}
	// Actual SQLite rejects the MySQL function: no fake success/host fallback.
	if _, err := videoExecutionAuthorityTimeTx(alias(tx)); !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) {
		t.Fatal("SQL failure accepted")
	}
	if output.Len() != 0 {
		t.Fatal("authority failure inherited SQL logging")
	}
	var observed time.Duration
	const name = "execution_clock_deadline_zero"
	if err := db.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") {
			deadline, ok := q.Statement.Context.Deadline()
			if !ok {
				t.Fatal("clock SQL has no deadline")
			}
			observed = time.Until(deadline)
			q.Statement.SQL.Reset()
			q.Statement.SQL.WriteString("SELECT NULL AS database_time")
			q.Statement.Vars = nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Row().Remove(name)
	if now, err := videoExecutionAuthorityTimeTx(alias(tx)); !now.IsZero() || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) || observed <= 0 || observed > 200*time.Millisecond {
		t.Fatal("zero clock/deadline boundary invalid")
	}
	short, stop := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer stop()
	if _, err := videoExecutionAuthorityTimeTx(alias(tx.WithContext(short))); !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) || observed > 80*time.Millisecond {
		t.Fatal("clock extended caller deadline")
	}
}

func TestVideoExecutionFactoryBoundaryAndLegacyEnvelope(t *testing.T) {
	s, db, _ := videoSetup(t)
	in := VideoExecutionInput{OrgID: 1, RequestID: "create", Body: []byte(`{"prompt":"quiet sea"}`)}
	if _, err := s.PrepareCreateExecutionTx(db, in); !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) {
		t.Fatal("factory accepted ordinary database")
	}
	if _, err := s.PrepareActionExecution(context.Background(), in); !errors.Is(err, ErrVideoInvalid) {
		t.Fatal("action accepted missing claim identity")
	}
	in.AttemptID = "claim"
	if _, err := s.PrepareCreateExecutionTx(nil, in); !errors.Is(err, ErrVideoInvalid) {
		t.Fatal("create accepted claim identity")
	}
	small := *s
	small.maxRetention = time.Hour
	if _, err := small.PrepareActionExecution(context.Background(), in); !errors.Is(err, ErrVideoRetentionDenied) {
		t.Fatal("fixed TTL silently shortened")
	}
	old, err := s.Prepare(videoInput(s))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageExecutionTx(db, old); !errors.Is(err, ErrVideoInvalid) {
		t.Fatal("legacy Prepare impersonated fixed factory")
	}
	for _, schema := range []uint32{0, 2, 99} {
		if _, err := CheckVideoExecutionWindowTx(nil, 1, "vpl_test", schema); !errors.Is(err, ErrVideoSchemaUnsupported) {
			t.Fatal("create window allowed action/unknown format")
		}
	}
	// The extracted private encryption path retains old canonical bytes/AAD.
	p, err := s.prepareExecutionAt(in, 2, s.now())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.decrypt(p.metadata, p.staging.Ciphertext)
	if err != nil || string(plain) != `{"prompt":"quiet sea"}` || p.metadata.CaptureRepresentation != "legacy_unknown" || !p.executionPrepared {
		t.Fatal("factory changed envelope semantics")
	}
	clear(plain)
	if strings.Contains(string(videoAAD(p.metadata)), "executionPrepared") {
		t.Fatal("in-memory provenance entered AAD")
	}
}

func TestVideoExecutionCancellationOutranksKnownLifecycleFailure(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		var ctx context.Context
		var cancel context.CancelFunc
		if cause == context.Canceled {
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		} else {
			ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
			defer cancel()
		}
		for _, known := range []error{ErrVideoExpired, ErrVideoRetentionDenied, ErrVideoAuditUnavailable, ErrVideoSchemaUnsupported, ErrVideoCorrupt, ErrVideoAccessDenied} {
			err := videoExecutionResultError(ctx, known)
			if !errors.Is(err, known) || !errors.Is(err, cause) || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) {
				t.Fatal("canceled DB phase lost authority/identity precedence", known, err)
			}
		}
	}
}

func TestVideoExecutionWindowMetadataFailureRetainsCancellation(t *testing.T) {
	s, db, _ := videoSetup(t)
	p := videoStage(t, s, db, videoInput(s))
	tx := db.Begin()
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := tx.Session(&gorm.Session{NewDB: true, Context: ctx})
	q.Dialector = executionClockTestDialect{q.Dialector}
	pool := &executionMetadataCancelPool{ConnPool: q.Statement.ConnPool, cancel: cancel}
	q.Statement.ConnPool = pool
	w, err := CheckVideoExecutionWindowTx(q, 1, p.PayloadID(), 1)
	if pool.queries != 1 || w.Identity.PayloadID != "" || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) || strings.Contains(err.Error(), "private-metadata") {
		t.Fatal("current metadata failure lost cancellation or disclosed driver cause", err)
	}
}

func TestVideoExecutionWindowIdentityResultRetainsCancellation(t *testing.T) {
	for _, kind := range []string{"missing", "schema", "correlation"} {
		t.Run(kind, func(t *testing.T) {
			s, db, _ := videoSetup(t)
			p := videoStage(t, s, db, videoInput(s))
			id, want := p.PayloadID(), ErrVideoNotFound
			switch kind {
			case "missing":
				id = "vpl_missing"
			case "schema":
				want = ErrVideoSchemaUnsupported
				if err := db.Model(&VideoPayload{}).Where("payload_id = ?", id).Update("schema_version", 99).Error; err != nil {
					t.Fatal(err)
				}
			case "correlation":
				want = ErrVideoCorrupt
				if err := db.Model(&VideoPayload{}).Where("payload_id = ?", id).Update("correlation_key", "invalid-correlation").Error; err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			queries := 0
			const callback = "execution_metadata_identity_cancel"
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
				if q.Statement.Table == "invocation_payloads" {
					queries++
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Query().Remove(callback)
			tx := db.WithContext(ctx).Begin()
			defer tx.Rollback()
			tx.Dialector = executionClockTestDialect{tx.Dialector}
			// SQLite supplies a real metadata result. This does not exercise or
			// claim a MySQL clock: each identity rejection precedes that query.
			window, err := CheckVideoExecutionWindowTx(tx, 1, id, 1)
			if queries != 1 || window != (VideoExecutionWindow{}) || !errors.Is(err, want) || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) {
				t.Fatal("metadata result lost concurrent cancellation", queries, err)
			}
		})
	}
}
