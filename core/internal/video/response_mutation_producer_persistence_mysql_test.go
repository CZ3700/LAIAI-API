package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestMySQLMutationProducerCallerCancellationSavesAndFlagOffRecovers(t *testing.T) {
	for _, action := range []string{"cancel_task", "delete_task"} {
		t.Run(action, func(t *testing.T) {
			final := ""
			if action == "delete_task" {
				final = "succeeded"
			}
			p := newMutationProducerFixture(t, final)
			executionRecoveryScope(t, p.fixture, []uint64{p.key.OrgID})
			op := p.pending(t, action)
			before := p.funds(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.respond = func(*http.Request) (*http.Response, error) { cancel(); return reply(204, ""), nil }
			err := p.s.processMutation(ctx, op)
			r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
			if !errors.Is(err, context.Canceled) || responseInboxApp(t, p.fixture, r).State != "pending" || p.deletes.Load() != 1 || p.funds(t) != before || p.s.DispatchPaused() {
				t.Fatal("caller cancellation discarded receipt, applied after cancellation or paused a durable save")
			}
			p.s.ResponseInboxEnabled, p.s.MutationResponseInboxEnabled = false, false
			expireFixtureOperationLease(t, p.s.DB, p.operation(t, op.ID))
			prioritizeMutationReceipt(t, p.mutationConsumerFixture, r)
			must(t, p.s.recoverMutationResponseInbox(context.Background()))
			want := "unknown"
			if action == "delete_task" {
				want = "confirmed"
			}
			if responseInboxApp(t, p.fixture, r).State != "applied" || p.operation(t, op.ID).Status != want || p.funds(t) != before {
				t.Fatal("flag-off recovery did not consume the previously registered response")
			}
			effects := p.effects(t)
			must(t, p.s.recoverMutationResponseInbox(context.Background()))
			_ = p.s.processMutation(context.Background(), p.operation(t, op.ID))
			if p.deletes.Load() != 1 || p.funds(t) != before || p.effects(t) != effects {
				t.Fatal("flag-off recovery or legacy dispatch repeated HTTP/effects")
			}
		})
	}
}

func TestMySQLMutationProducerLostClaimStillSavesItsResponse(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	executionRecoveryScope(t, p.fixture, []uint64{p.key.OrgID})
	op := p.pending(t, "cancel_task")
	before := p.funds(t)
	p.respond = func(*http.Request) (*http.Response, error) {
		// The response belongs to the already sent captured attempt even when
		// its operation version/lease changes while the provider is processing.
		must(t, p.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", p.key.OrgID, op.ID).Updates(map[string]any{"status": "unknown", "version": gorm.Expr("version + 1"), "lease_until": nil}).Error)
		return reply(202, `{}`), nil
	}
	err := p.s.processMutation(context.Background(), op)
	r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
	if !errors.Is(err, ErrConflict) || responseInboxApp(t, p.fixture, r).State != "pending" || p.deletes.Load() != 1 || p.funds(t) != before {
		t.Fatal("late response was lost or stale claimant applied it")
	}
	prioritizeMutationReceipt(t, p.mutationConsumerFixture, r)
	must(t, p.s.recoverMutationResponseInbox(context.Background()))
	if responseInboxApp(t, p.fixture, r).State != "applied" || p.operation(t, op.ID).ReceivedOutcome != "accepted" || p.funds(t) != before || p.deletes.Load() != 1 {
		t.Fatal("late captured response failed independent recovery")
	}
}

func TestMySQLMutationProducerGETWinsBeforeDeleteResponse(t *testing.T) {
	for _, result := range []string{"succeeded", "cancelled"} {
		t.Run(result, func(t *testing.T) {
			p := newMutationProducerFixture(t, "")
			executionRecoveryScope(t, p.fixture, []uint64{p.key.OrgID})
			op := p.pending(t, "cancel_task")
			_, currentState := p.rows(t)
			makeFixtureQueryDue(t, p.s.DB, p.key.OrgID, p.taskID, currentState.QuerySequence, false)
			var afterGET string
			p.respond = func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					tokens := 100
					if result == "cancelled" {
						tokens = 0
					}
					return reply(200, fmt.Sprintf(`{"id":%q,"status":%q,"usage":{"completion_tokens":%d,"total_tokens":%d}}`, path.Base(r.URL.Path), result, tokens, tokens)), nil
				}
				must(t, p.s.pollTask(context.Background(), p.key.OrgID, p.taskID))
				row, st := p.rows(t)
				if row.Status != result || !financialFinal(st.BillingState) || p.gets.Load() != 1 {
					t.Fatal("authoritative GET did not win the real producer race")
				}
				afterGET = p.funds(t)
				return reply(204, ""), nil
			}
			err := p.s.processMutation(context.Background(), op)
			if err != nil && !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
			if responseInboxApp(t, p.fixture, r).State != "applied" {
				prioritizeMutationReceipt(t, p.mutationConsumerFixture, r)
				must(t, p.s.recoverMutationResponseInbox(context.Background()))
			}
			row, st := p.rows(t)
			if row.Status != result || !financialFinal(st.BillingState) || p.funds(t) != afterGET || p.operation(t, op.ID).ReceivedOutcome != "accepted" || responseInboxApp(t, p.fixture, r).State != "applied" {
				t.Fatal("late DELETE acknowledgement regressed or repriced authoritative GET facts")
			}
			effects := p.effects(t)
			must(t, p.s.recoverMutationResponseInbox(context.Background()))
			_ = p.s.processMutation(context.Background(), p.operation(t, op.ID))
			if p.gets.Load() != 1 || p.deletes.Load() != 1 || p.funds(t) != afterGET || p.effects(t) != effects {
				t.Fatal("GET/DELETE replay duplicated settlement, events or network work")
			}
		})
	}
}

func TestMySQLMutationProducerAppendRetriesOnePreparedResponse(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	op := p.pending(t, "cancel_task")
	before := p.funds(t)
	var candidates []ResponseInbox
	name := fmt.Sprintf("producer-transient-save-%d", p.key.OrgID)
	must(t, p.s.responseDB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*ResponseInbox); ok {
			candidates = append(candidates, *row)
			if len(candidates) < 3 {
				tx.AddError(&mysqlDriver.MySQLError{Number: 1213, Message: "synthetic append deadlock"})
			}
		}
	}))
	t.Cleanup(func() { _ = p.s.responseDB.Callback().Create().Remove(name) })
	must(t, p.s.processMutation(context.Background(), op))
	r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
	if len(candidates) != 3 || p.deletes.Load() != 1 || p.s.DispatchPaused() || p.funds(t) != before || responseInboxApp(t, p.fixture, r).State != "applied" {
		t.Fatal("append retry repeated HTTP or failed to recover exactly three attempts")
	}
	for _, candidate := range candidates {
		if candidate.ReceiptID != r.ReceiptID || candidate.PayloadRef != r.PayloadRef || candidate.EvidenceHMAC != r.EvidenceHMAC {
			t.Fatal("append retry prepared another response identity")
		}
	}
}

func TestMySQLMutationProducerCommitAcknowledgementLossDoesNotResend(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	op := p.pending(t, "cancel_task")
	before := p.funds(t)
	pool, err := p.s.responseDB.DB()
	must(t, err)
	main, err := p.s.DB.DB()
	must(t, err)
	if pool == main {
		t.Fatal("evidence pool aliases primary pool")
	}
	wrapper := &responsePersistenceCommitPool{ConnPool: pool, db: pool}
	wrapper.loseACK.Store(true)
	var committed ResponseInbox
	wrapper.afterCommit = func() { committed = mutationOneReceipt(t, p.mutationConsumerFixture, op) }
	original := p.s.responseDB.Statement.ConnPool
	p.s.responseDB.Statement.ConnPool = wrapper
	t.Cleanup(func() { p.s.responseDB.Statement.ConnPool = original })
	must(t, p.s.processMutation(context.Background(), op))
	r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
	if wrapper.commits.Load() != 2 || committed.ReceiptID == "" || committed.ReceiptID != r.ReceiptID || committed.PayloadRef != r.PayloadRef || p.deletes.Load() != 1 || p.funds(t) != before || p.s.DispatchPaused() || responseInboxApp(t, p.fixture, r).State != "applied" {
		t.Fatal("real commit with lost acknowledgement was not deduplicated")
	}
}

func TestMySQLMutationProducerSaveFailurePausesSendsButAllowsGETAndRecovery(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	executionRecoveryScope(t, p.fixture, []uint64{p.key.OrgID})
	op := p.pending(t, "cancel_task")
	n := p.another(t, "producer-after-failed-save")
	other := n.pending(t, "cancel_task")
	before := p.funds(t)
	count := 0
	name := fmt.Sprintf("producer-save-exhausted-%d", p.key.OrgID)
	must(t, p.s.responseDB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_response_inbox" {
			count++
			tx.AddError(&mysqlDriver.MySQLError{Number: 1213, Message: "synthetic persistent append deadlock"})
		}
	}))
	t.Cleanup(func() { _ = p.s.responseDB.Callback().Create().Remove(name) })
	err := p.s.processMutation(context.Background(), op)
	must(t, p.s.responseDB.Callback().Create().Remove(name))
	if !errors.Is(err, ErrResponseSaveUnconfirmed) || count != 3 || !p.s.DispatchPaused() || p.deletes.Load() != 1 || p.funds(t) != before || responseInboxCount(t, p.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 {
		t.Fatal("exhausted save was hidden, leaked evidence or failed to pause dispatch")
	}
	if !errors.Is(p.s.processMutation(context.Background(), other), ErrDispatchPaused) || p.deletes.Load() != 1 {
		t.Fatal("next operation bypassed sticky pause")
	}
	_, currentState := p.rows(t)
	makeFixtureQueryDue(t, p.s.DB, p.key.OrgID, p.taskID, currentState.QuerySequence, false)
	must(t, p.s.pollTask(context.Background(), p.key.OrgID, p.taskID))
	if p.gets.Load() != 1 || p.funds(t) != before {
		t.Fatal("pause blocked authoritative GET or changed funds")
	}
	// Put only this legitimate expired registration first; shared-suite rows
	// are retained. No receipt is invented for the failed save.
	current := expireFixtureOperationLease(t, p.s.DB, p.operation(t, op.ID))
	priority := p.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ? AND resource_id = ? AND version = ? AND attempt_id = ?", current.OrgID, current.ID, current.ResourceID, current.Version, current.AttemptID).UpdateColumn("lease_until", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	must(t, priority.Error)
	if priority.RowsAffected != 1 {
		t.Fatal("fixture failed to prioritize its exact expired registration")
	}
	must(t, p.s.recoverMutationResponseInbox(context.Background()))
	if p.operation(t, op.ID).Status != "unknown" || !p.s.DispatchPaused() || p.funds(t) != before || p.deletes.Load() != 1 {
		t.Fatal("paused service did not recover no-response registration conservatively")
	}
	p.s.MutationResponseInboxEnabled = false
	_ = p.s.processMutation(context.Background(), p.operation(t, op.ID))
	if p.deletes.Load() != 1 {
		t.Fatal("flag-off legacy resent registered uncertain action")
	}
}

func TestMySQLMutationProducerOccupiedEvidencePoolDoesNotBlockPrimaryDB(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	op := p.pending(t, "cancel_task")
	before := p.funds(t)
	pool, err := p.s.responseDB.DB()
	must(t, err)
	if pool.Stats().MaxOpenConnections != 2 {
		t.Fatal("fixture must exercise bounded two-connection production pool")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := pool.Conn(ctx)
	must(t, err)
	second, err := pool.Conn(ctx)
	must(t, err)
	defer first.Close()
	defer second.Close()
	sent := make(chan struct{}, 1)
	p.respond = func(*http.Request) (*http.Response, error) { sent <- struct{}{}; return reply(204, ""), nil }
	done := make(chan error, 1)
	joined := false
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("producer failed to leave bounded save after pool release")
			}
		}
	})
	baselineWaits := pool.Stats().WaitCount
	go func() { done <- p.s.processMutation(ctx, op) }()
	select {
	case <-sent:
	case <-time.After(3 * time.Second):
		t.Fatal("producer never reached counted DELETE")
	}
	deadline := time.NewTimer(2 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
	waiting := false
	for !waiting {
		select {
		case <-tick.C:
			waiting = pool.Stats().WaitCount > baselineWaits
		case <-deadline.C:
			t.Fatal("save did not wait for the genuinely occupied evidence pool")
		}
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), time.Second)
	var one int
	err = p.s.DB.WithContext(probeCtx).Raw("SELECT 1").Scan(&one).Error
	probeCancel()
	if err != nil || one != 1 {
		t.Fatal("evidence pool starvation consumed primary database capacity")
	}
	must(t, first.Close())
	select {
	case err := <-done:
		joined = true
		must(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("released evidence connection did not complete bounded save")
	}
	r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
	if p.deletes.Load() != 1 || p.funds(t) != before || p.s.DispatchPaused() || responseInboxApp(t, p.fixture, r).State != "applied" {
		t.Fatal("pool wait lost evidence, resent HTTP or changed funds")
	}
}
