package video

import (
	"context"
	"errors"
	"fmt"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

func mutationOneReceipt(t *testing.T, m *mutationConsumerFixture, op Operation) ResponseInbox {
	t.Helper()
	var rows []ResponseInbox
	must(t, m.s.DB.Where("org_id = ? AND operation_id = ?", m.key.OrgID, op.ID).Find(&rows).Error)
	if len(rows) != 1 {
		t.Fatalf("mutation receipt count=%d, want 1", len(rows))
	}
	r := rows[0]
	if responseInboxCount(t, m.fixture, &ReceiptApplication{}, "org_id = ? AND receipt_id = ?", m.key.OrgID, r.ReceiptID) != 1 || responseInboxCount(t, m.fixture, &invocation.VideoPayload{}, "org_id = ? AND payload_id = ?", m.key.OrgID, r.PayloadRef) != 1 || responseInboxCount(t, m.fixture, &invocation.VideoPayloadStaging{}, "payload_id = ?", r.PayloadRef) != 1 || responseInboxCount(t, m.fixture, &Attempt{}, "org_id = ? AND attempt_id = ? AND phase = ?", m.key.OrgID, r.AttemptID, "response_received") != 1 {
		t.Fatal("mutation response has partial or duplicated dependent records")
	}
	return r
}

func TestMySQLResponseMutationCancelledCallerStillRetriesAndSaves(t *testing.T) {
	m := newMutationConsumerFixture(t, "")
	executionRecoveryScope(t, m.fixture, []uint64{m.key.OrgID})
	a, claim := m.register(t, "cancel_task")
	funds := m.funds(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var candidates []ResponseInbox
	name := fmt.Sprintf("mutation-save-cancel-%d", m.key.OrgID)
	must(t, m.s.responseDB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*ResponseInbox); ok {
			candidates = append(candidates, *row)
			if len(candidates) <= 2 {
				tx.AddError(&mysqldriver.MySQLError{Number: 1213, Message: "controlled response deadlock"})
			}
		}
	}))
	t.Cleanup(func() { _ = m.s.responseDB.Callback().Create().Remove(name) })
	r, err := m.s.saveMutationResponse(ctx, a, mutationResponse(ark.OutcomeAccepted, 200), "official-test-credential")
	must(t, err)
	must(t, m.s.responseDB.Callback().Create().Remove(name))
	if len(candidates) != 3 || m.s.DispatchPaused() {
		t.Fatal("cancelled parent prevented the bounded response retries")
	}
	for _, c := range candidates {
		if c.ReceiptID != r.ReceiptID || c.PayloadRef != r.PayloadRef || c.EvidenceHMAC != r.EvidenceHMAC {
			t.Fatal("save retry prepared different evidence")
		}
	}
	mutationOneReceipt(t, m, claim)
	if err := m.s.applyClaimedMutationInbox(ctx, claim, r.ReceiptID); err == nil {
		t.Fatal("cancelled application context was replaced")
	}
	if responseInboxApp(t, m.fixture, r).State != "pending" || m.funds(t) != funds {
		t.Fatal("cancelled caller applied saved evidence")
	}
	m.recoverable(t, claim)
	prioritizeMutationReceipt(t, m, r)
	must(t, m.s.recoverMutationResponseInbox(context.Background()))
	if responseInboxApp(t, m.fixture, r).State != "applied" || m.funds(t) != funds {
		t.Fatal("saved response did not recover independently of its cancelled caller")
	}
}

func TestMySQLResponseMutationCommitAcknowledgementLossReplaysUniqueEvidence(t *testing.T) {
	m := newMutationConsumerFixture(t, "succeeded")
	a, claim := m.register(t, "delete_task")
	funds := m.funds(t)
	pool, err := m.s.responseDB.DB()
	must(t, err)
	primary, err := m.s.DB.DB()
	must(t, err)
	if pool == primary {
		t.Fatal("response save shares the primary SQL pool")
	}
	// A real MySQL COMMIT succeeds before this decorator returns EOF. This
	// proves ambiguous-commit replay, not a network/proxy fault injection.
	wrapper := &responsePersistenceCommitPool{ConnPool: pool, db: pool}
	wrapper.loseACK.Store(true)
	var committed ResponseInbox
	wrapper.afterCommit = func() { committed = mutationOneReceipt(t, m, claim) }
	original := m.s.responseDB.Statement.ConnPool
	m.s.responseDB.Statement.ConnPool = wrapper
	t.Cleanup(func() { m.s.responseDB.Statement.ConnPool = original })
	r, err := m.s.saveMutationResponse(context.Background(), a, mutationResponse(ark.OutcomeAccepted, 200), "official-test-credential")
	must(t, err)
	if wrapper.commits.Load() != 2 || committed.ReceiptID != r.ReceiptID || committed.PayloadRef != r.PayloadRef || committed.EvidenceHMAC != r.EvidenceHMAC || m.s.DispatchPaused() {
		t.Fatal("ambiguous commit did not return its unique committed response")
	}
	mutationOneReceipt(t, m, claim)
	must(t, m.s.applyClaimedMutationInbox(context.Background(), claim, r.ReceiptID))
	effects := m.effects(t)
	must(t, m.s.applyMutationInbox(context.Background(), m.key.OrgID, r.ReceiptID, nil))
	if m.funds(t) != funds || m.effects(t) != effects {
		t.Fatal("ambiguous commit repeated application or money")
	}
}

func TestMySQLResponseMutationSaveExhaustionAndPermanentFailurePauseDispatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     uint16
		attempts int
	}{{"transient", 1213, 3}, {"permanent", 1146, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMutationConsumerFixture(t, "")
			executionRecoveryScope(t, m.fixture, []uint64{m.key.OrgID})
			a, claim := m.register(t, "cancel_task")
			funds := m.funds(t)
			count := 0
			name := fmt.Sprintf("mutation-save-failure-%d", m.key.OrgID)
			must(t, m.s.responseDB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "video_response_inbox" {
					count++
					tx.AddError(&mysqldriver.MySQLError{Number: tc.code, Message: "private controlled storage diagnostic"})
				}
			}))
			t.Cleanup(func() { _ = m.s.responseDB.Callback().Create().Remove(name) })
			_, err := m.s.saveMutationResponse(context.Background(), a, mutationResponse(ark.OutcomeAccepted, 200), "official-test-credential")
			must(t, m.s.responseDB.Callback().Create().Remove(name))
			if !errors.Is(err, ErrResponseSaveUnconfirmed) || count != tc.attempts || !m.s.DispatchPaused() || responseInboxCount(t, m.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", m.key.OrgID, claim.ID) != 0 || m.funds(t) != funds {
				t.Fatal("unconfirmed save failed to bound retries, preserve funds or latch dispatch pause")
			}
			// Responses already observed remain saveable after the dispatch latch.
			r, err := m.s.saveMutationResponse(context.Background(), a, mutationResponse(ark.OutcomeAccepted, 200), "official-test-credential")
			must(t, err)
			m.recoverable(t, claim)
			prioritizeMutationReceipt(t, m, r)
			must(t, m.s.recoverMutationResponseInbox(context.Background()))
			if responseInboxApp(t, m.fixture, r).State != "applied" || !m.s.DispatchPaused() || m.funds(t) != funds {
				t.Fatal("pause prevented recovery or recovery cleared the sticky pause")
			}
		})
	}
}
