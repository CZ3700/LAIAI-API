package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

// This decorates a real MySQL transaction: the first Commit really succeeds,
// then its caller receives EOF instead of the acknowledgement. It proves the
// application's ambiguous-commit replay, not a network/proxy fault scenario.
type responsePersistenceCommitPool struct {
	gorm.ConnPool
	db          *sql.DB
	loseACK     atomic.Bool
	commits     atomic.Int32
	afterCommit func()
}

func (p *responsePersistenceCommitPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &responsePersistenceCommitTx{Tx: tx, pool: p}, nil
}

func (p *responsePersistenceCommitPool) GetDBConn() (*sql.DB, error) { return p.db, nil }

type responsePersistenceCommitTx struct {
	*sql.Tx
	pool *responsePersistenceCommitPool
}

func (tx *responsePersistenceCommitTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	tx.pool.commits.Add(1)
	if tx.pool.loseACK.CompareAndSwap(true, false) {
		if tx.pool.afterCommit != nil {
			tx.pool.afterCommit()
		}
		return io.EOF
	}
	return nil
}

func responsePersistenceOneReceipt(t *testing.T, f *fixture, taskID string) ResponseInbox {
	t.Helper()
	var rows []ResponseInbox
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, taskID).Find(&rows).Error)
	if len(rows) != 1 {
		t.Fatalf("durable response count = %d, want one", len(rows))
	}
	r := rows[0]
	if responseInboxCount(t, f, &ReceiptApplication{}, "org_id = ? AND receipt_id = ?", r.OrgID, r.ReceiptID) != 1 ||
		responseInboxCount(t, f, &invocation.VideoPayload{}, "org_id = ? AND payload_id = ?", r.OrgID, r.PayloadRef) != 1 ||
		responseInboxCount(t, f, &invocation.VideoPayloadStaging{}, "payload_id = ?", r.PayloadRef) != 1 ||
		responseInboxCount(t, f, &Attempt{}, "org_id = ? AND attempt_id = ? AND phase = ?", r.OrgID, r.AttemptID, "response_received") != 1 {
		t.Fatal("durable response has missing or duplicate dependent evidence")
	}
	return r
}

func TestMySQLResponsePersistenceCommitAcknowledgementLossReplaysOneReceipt(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ResponseInboxEnabled = true
	v := f.create(t, "persistence-commit-ack-loss")
	beforeFunds := responseInboxFunds(t, f, v.ID)
	pool, err := f.s.responseDB.DB()
	must(t, err)
	mainPool, err := f.s.DB.DB()
	must(t, err)
	if pool == mainPool {
		t.Fatal("response append is sharing the primary SQL pool")
	}
	wrapper := &responsePersistenceCommitPool{ConnPool: pool, db: pool}
	wrapper.loseACK.Store(true)
	var committed ResponseInbox
	wrapper.afterCommit = func() {
		// The main pool is a different connection and can only observe committed
		// rows. This runs before the first append receives its synthetic EOF.
		committed = responsePersistenceOneReceipt(t, f, v.ID)
	}
	original := f.s.responseDB.Statement.ConnPool
	f.s.responseDB.Statement.ConnPool = wrapper
	t.Cleanup(func() { f.s.responseDB.Statement.ConnPool = original })
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			return nil, errors.New("unexpected method in commit acknowledgement test")
		}
		return reply(200, `{"id":"up_`+v.ID+`"}`), nil
	}
	f.dispatch(t, v.ID)
	r := responsePersistenceOneReceipt(t, f, v.ID)
	if wrapper.commits.Load() != 2 || committed.ReceiptID == "" || committed.ReceiptID != r.ReceiptID || committed.PayloadRef != r.PayloadRef || committed.EvidenceHMAC != r.EvidenceHMAC || responseInboxApp(t, f, r).State != "applied" || f.s.DispatchPaused() {
		t.Fatal("ambiguous commit was not replayed as its one committed receipt")
	}
	must(t, f.s.recoverResponseInbox(context.Background()))
	must(t, f.s.recoverResponseInbox(context.Background()))
	if responseInboxFunds(t, f, v.ID) != beforeFunds || f.posts.Load() != 1 || f.gets.Load() != 0 || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 1 {
		t.Fatal("ambiguous commit replay changed money, duplicated binding or resent HTTP")
	}
}

func TestMySQLResponsePersistenceTransientAppendRetriesSamePreparedEvidence(t *testing.T) {
	for _, table := range []string{"video_response_inbox", "invocation_payloads", "invocation_payload_staging", "video_receipt_applications", "video_attempts"} {
		t.Run(table, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "persistence-transient-stage")
			attempt, _ := responseInboxRegister(t, f, v.ID)
			before := responseInboxFunds(t, f, v.ID)
			var candidates []ResponseInbox
			failures := 0
			name := fmt.Sprintf("test:persistence-transient-%d", f.key.OrgID)
			must(t, f.s.responseDB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
				if row, ok := tx.Statement.Dest.(*ResponseInbox); ok {
					candidates = append(candidates, *row)
				}
				if tx.Statement.Table == table && failures < 2 {
					failures++
					tx.AddError(&mysqlDriver.MySQLError{Number: 1213, Message: "synthetic append deadlock"})
				}
			}))
			t.Cleanup(func() { _ = f.s.responseDB.Callback().Create().Remove(name) })
			r, err := f.s.saveCreateResponse(context.Background(), attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID), "official-test-credential")
			must(t, err)
			if failures != 2 || len(candidates) != 3 || f.s.DispatchPaused() {
				t.Fatal("transient append did not retry exactly twice before success")
			}
			for _, candidate := range candidates {
				if candidate.ReceiptID != r.ReceiptID || candidate.PayloadRef != r.PayloadRef || candidate.EvidenceHMAC != r.EvidenceHMAC {
					t.Fatal("database retry prepared a new receipt, payload or evidence hash")
				}
			}
			responsePersistenceOneReceipt(t, f, v.ID)
			if responseInboxApp(t, f, r).State != "pending" || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("append retry applied state, changed money or issued HTTP")
			}
		})
	}
}

func TestMySQLResponsePersistencePermanentIdentityErrorDoesNotRetry(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "persistence-permanent-identity")
	attempt, _ := responseInboxRegister(t, f, v.ID)
	before := responseInboxFunds(t, f, v.ID)
	// A validly sealed prepared object still cannot change its registration's
	// immutable credential identity. This is a real identity failure, not SQL.
	attempt.CredentialSnapshotID++
	reads := 0
	name := fmt.Sprintf("test:persistence-permanent-%d", f.key.OrgID)
	must(t, f.s.responseDB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_execution_attempts" {
			reads++
		}
	}))
	t.Cleanup(func() { _ = f.s.responseDB.Callback().Query().Remove(name) })
	_, err := f.s.saveCreateResponse(context.Background(), attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID), "official-test-credential")
	if !errors.Is(err, ErrResponseSaveUnconfirmed) || reads != 1 || !f.s.DispatchPaused() {
		t.Fatal("permanent registration mismatch was retried or did not pause new dispatch", err)
	}
	if responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 0 || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 0 {
		t.Fatal("permanent append failure left evidence or changed money/HTTP")
	}
}

func TestMySQLResponsePersistenceCancelledCallerStillRetriesEvidence(t *testing.T) {
	f := mysqlFixture(t)
	f.s.ResponseInboxEnabled = true
	v := f.create(t, "persistence-cancelled-caller")
	before := responseInboxFunds(t, f, v.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts int
	name := fmt.Sprintf("test:persistence-cancelled-%d", f.key.OrgID)
	must(t, f.s.responseDB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_response_inbox" {
			attempts++
			if tx.Statement.Context.Err() != nil {
				t.Error("caller cancellation propagated into detached evidence persistence")
			}
			if attempts == 1 {
				tx.AddError(&mysqlDriver.MySQLError{Number: 1213, Message: "synthetic first append deadlock"})
			}
		}
	}))
	t.Cleanup(func() { _ = f.s.responseDB.Callback().Create().Remove(name) })
	f.response = func(*http.Request) (*http.Response, error) {
		cancel()
		return reply(200, `{"id":"up_`+v.ID+`"}`), nil
	}
	err := f.s.processOperation(ctx, runtimeWorkerCreateOperation(t, f, v.ID))
	r := responsePersistenceOneReceipt(t, f, v.ID)
	if !errors.Is(err, context.Canceled) || attempts != 2 || responseInboxApp(t, f, r).State != "pending" || f.s.DispatchPaused() || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 1 {
		t.Fatal("cancelled caller lost its response, retried HTTP or changed its reservation")
	}
	// Applying under the cancelled caller is optional; a current background
	// consumer must complete from the durable response without another POST.
	f.now = f.now.Add(4 * time.Minute)
	responsePersistencePrioritizeInbox(t, f, r)
	must(t, f.s.recoverResponseInbox(context.Background()))
	if responseInboxApp(t, f, r).State != "applied" || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 1 {
		t.Fatal("cancelled producer's durable receipt did not recover once")
	}
}

func TestMySQLResponsePersistencePoolExhaustionLeavesPrimaryAvailable(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "persistence-pool-isolation")
	attempt, _ := responseInboxRegister(t, f, v.ID)
	pool, err := f.s.responseDB.DB()
	must(t, err)
	mainPool, err := f.s.DB.DB()
	must(t, err)
	if pool == mainPool || pool.Stats().MaxOpenConnections != 2 {
		t.Fatal("evidence pool is not an independent two-connection pool")
	}
	first, err := pool.Conn(context.Background())
	must(t, err)
	defer first.Close()
	second, err := pool.Conn(context.Background())
	must(t, err)
	defer second.Close()
	priorWaits := pool.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		_, saveErr := f.s.saveCreateResponse(context.Background(), attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID), "official-test-credential")
		done <- saveErr
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for pool.Stats().WaitCount == priorWaits {
		select {
		case <-deadline.C:
			t.Fatal("append did not wait for the occupied dedicated pool")
		case err := <-done:
			t.Fatal("append bypassed its occupied dedicated pool", err)
		case <-tick.C:
		}
	}
	queryCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var one int
	must(t, f.s.DB.WithContext(queryCtx).Raw("SELECT 1").Scan(&one).Error)
	if one != 1 || pool.Stats().InUse != 2 {
		t.Fatal("occupied evidence pool prevented a primary-pool query")
	}
	must(t, first.Close())
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("append did not resume after a dedicated connection was returned")
	}
	responsePersistenceOneReceipt(t, f, v.ID)
	if f.s.DispatchPaused() || f.posts.Load() != 0 {
		t.Fatal("temporary dedicated-pool pressure paused dispatch or caused HTTP")
	}
}
