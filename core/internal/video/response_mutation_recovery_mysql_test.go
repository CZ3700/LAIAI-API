package video

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (m *mutationConsumerFixture) another(t *testing.T, key string) *mutationConsumerFixture {
	t.Helper()
	v := m.create(t, key)
	n := &mutationConsumerFixture{fixture: m.fixture, taskID: v.ID}
	a, claim := responseInboxRegister(t, n.fixture, v.ID)
	r := responseInboxAppend(t, n.fixture, a, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
	must(t, n.s.applyClaimedCreateInbox(context.Background(), claim, r.ReceiptID))
	return n
}

// Shared MySQL runs retain earlier tests' due work. Move only this receipt
// ahead of the real eligible queue and prove it is in the production batch;
// never clear unrelated rows or replace the consumer with a direct apply call.
func prioritizeMutationReceipt(t *testing.T, m *mutationConsumerFixture, r ResponseInbox) time.Time {
	t.Helper()
	var first []ReceiptApplication
	must(t, m.s.mutationResponseInboxCandidates(context.Background()).Order("ap.next_attempt_at, ap.receipt_id").Limit(1).Scan(&first).Error)
	var dbNow struct{ Value time.Time }
	must(t, m.s.DB.Raw("SELECT UTC_TIMESTAMP(3) AS value").Scan(&dbNow).Error)
	due := dbNow.Value.Add(-time.Second)
	if len(first) > 0 && !first[0].NextAttemptAt.After(due) {
		due = first[0].NextAttemptAt.Add(-time.Millisecond)
	}
	must(t, m.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", r.OrgID, r.ReceiptID).Update("next_attempt_at", due).Error)
	var batch []ReceiptApplication
	must(t, m.s.mutationResponseInboxCandidates(context.Background()).Order("ap.next_attempt_at, ap.receipt_id").Limit(30).Scan(&batch).Error)
	if len(batch) == 0 || batch[0].ReceiptID != r.ReceiptID {
		t.Fatal("fixture receipt is not first in the real bounded recovery queue")
	}
	return due
}

func TestMySQLResponseMutationUnknownFormatsExcludedBeforeLimit(t *testing.T) {
	m := newMutationConsumerFixture(t, "")
	executionRecoveryScope(t, m.fixture, []uint64{m.key.OrgID})
	var blocked []ResponseInbox
	for i := 0; i < 32; i++ {
		n := m.another(t, fmt.Sprintf("mutation-future-%02d", i))
		a, claim := n.register(t, "cancel_task")
		changes := map[string]any{}
		if i%2 == 0 {
			a.StorageProtocol = "future-action-protocol"
			changes["storage_protocol"] = a.StorageProtocol
		} else {
			a.ExecutionSchemaVersion = 3
			changes["execution_schema_version"] = 3
		}
		// Deliberately emulate an already registered future contract. Its
		// genuine input HMAC is left intact; receipt capture must not require
		// the current consumer to interpret that contract.
		must(t, m.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", a.OrgID, a.AttemptID).Updates(changes).Error)
		r := n.append(t, a, mutationResponse(ark.OutcomeAccepted, 200))
		n.recoverable(t, claim)
		_ = n.s.applyMutationInbox(context.Background(), n.key.OrgID, r.ReceiptID, nil)
		if responseInboxApp(t, n.fixture, r).State != "blocked" {
			t.Fatal("unsupported persisted format was interpreted")
		}
		blocked = append(blocked, r)
	}
	a, claim := m.register(t, "cancel_task")
	healthy := m.append(t, a, mutationResponse(ark.OutcomeAccepted, 200))
	m.recoverable(t, claim)
	due := prioritizeMutationReceipt(t, m, healthy)
	for _, r := range blocked {
		must(t, m.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", r.OrgID, r.ReceiptID).Update("next_attempt_at", due.Add(-time.Second)).Error)
	}
	var batch []ReceiptApplication
	must(t, m.s.mutationResponseInboxCandidates(context.Background()).Order("ap.next_attempt_at, ap.receipt_id").Limit(30).Scan(&batch).Error)
	if len(batch) == 0 || batch[0].ReceiptID != healthy.ReceiptID {
		t.Fatal("unknown formats consumed the bounded recovery window")
	}
	funds := m.funds(t)
	m.s.responsePaused.Store(true) // Latch is not permission to stop recovery.
	must(t, m.s.recoverMutationResponseInbox(context.Background()))
	if responseInboxApp(t, m.fixture, healthy).State != "applied" || m.funds(t) != funds {
		t.Fatal("compatible successor starved or recovery changed funds")
	}
	for _, r := range blocked {
		if responseInboxApp(t, m.fixture, r).State != "blocked" {
			t.Fatal("unsupported sibling was consumed during recovery")
		}
	}
}

func TestMySQLResponseMutationRestartAfterRealInputPurgeWithoutHistory(t *testing.T) {
	for _, action := range []string{"cancel_task", "delete_task"} {
		t.Run(action, func(t *testing.T) {
			final := ""
			if action == "delete_task" {
				final = "succeeded"
			}
			m := newMutationConsumerFixture(t, final)
			executionRecoveryScope(t, m.fixture, []uint64{m.key.OrgID})
			must(t, m.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Update("capture_history", false).Error)
			a, claim := executionOldMutationClaim(t, m, action, executionFixtureNow(t, m.s.DB).Add(-time.Second))
			r := m.append(t, a, mutationResponse(ark.OutcomeAccepted, 200))
			var history invocation.VideoPayload
			must(t, m.s.DB.Where("org_id = ? AND payload_id = ?", m.key.OrgID, r.PayloadRef).Take(&history).Error)
			if history.CaptureStatus != invocation.VideoCaptureDisabled || responseInboxCount(t, m.fixture, &invocation.VideoPayloadStaging{}, "payload_id = ?", r.PayloadRef) != 0 {
				t.Fatal("disabled response history retained ciphertext")
			}
			funds := m.funds(t)
			reads := responseInboxCount(t, m.fixture, &invocation.VideoPayloadAccess{}, "org_id = ?", m.key.OrgID)
			m.now = m.now.Add(31 * 24 * time.Hour)
			name := fmt.Sprintf("mutation-purge-%d", m.key.OrgID)
			must(t, m.s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "invocation_payloads" {
					tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "org_id", Value: m.key.OrgID}}})
				}
			}))
			t.Cleanup(func() { _ = m.s.DB.Callback().Query().Remove(name) })
			_, err := m.s.Evidence.PurgeExpired(context.Background(), 100)
			must(t, m.s.DB.Callback().Query().Remove(name))
			must(t, err)
			var input invocation.VideoPayload
			must(t, m.s.DB.Where("org_id = ? AND payload_id = ?", m.key.OrgID, a.InputPayloadRef).Take(&input).Error)
			if input.CaptureStatus != invocation.VideoCaptureExpired || input.WrappedKey != "" || len(input.Nonce) != 0 || responseInboxCount(t, m.fixture, &invocation.VideoPayloadStaging{}, "payload_id = ?", input.PayloadID) != 0 {
				t.Fatal("real lifecycle did not erase action execution plaintext capability")
			}
			old := m.s
			restarted, err := NewService(old.DB, old.Keyring, old.Secret, old.Log)
			must(t, err)
			restarted.Now = func() time.Time { return m.now }
			restarted.Client, restarted.Gate = old.Client, old.Gate
			m.s = restarted
			expireFixtureOperationLease(t, m.s.DB, claim)
			prioritizeMutationReceipt(t, m, r)
			must(t, m.s.recoverMutationResponseInbox(context.Background()))
			must(t, m.s.recoverMutationResponseInbox(context.Background()))
			if responseInboxApp(t, m.fixture, r).State != "applied" || m.funds(t) != funds || responseInboxCount(t, m.fixture, &invocation.VideoPayloadAccess{}, "org_id = ?", m.key.OrgID) != reads {
				t.Fatal("fresh consumer required expired body decryption or changed funds")
			}
			want := "unknown"
			if action == "delete_task" {
				want = "confirmed"
			}
			if m.operation(t, claim.ID).Status != want {
				t.Fatal("expired input prevented typed recovery")
			}
		})
	}
}

func TestMySQLResponseMutationExpiredInvalidInputDoesNotStarveSuccessor(t *testing.T) {
	for _, fault := range []string{"identity_hmac", "input_schema"} {
		t.Run(fault, func(t *testing.T) {
			m := newMutationConsumerFixture(t, "")
			executionRecoveryScope(t, m.fixture, []uint64{m.key.OrgID})
			badAttempt, badClaim := m.register(t, "cancel_task")
			n := m.another(t, "mutation-expired-successor")
			_, goodClaim := n.register(t, "cancel_task")
			if fault == "identity_hmac" {
				must(t, m.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", m.key.OrgID, badAttempt.AttemptID).Update("input_identity_hmac", "tampered-after-real-registration").Error)
			} else {
				must(t, m.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", m.key.OrgID, badAttempt.InputPayloadRef).Update("schema_version", 3).Error)
			}
			// Put this pair before every extant in-flight operation, including
			// unrelated formats. No existing row is removed or rescheduled.
			var oldest struct{ Lease *time.Time }
			must(t, m.s.DB.Model(&Operation{}).Select("MIN(lease_until) AS lease").Where("status = ?", "in_flight").Scan(&oldest).Error)
			var dbNow time.Time
			authorityFixtureTx(t, m.s.DB, func(tx *gorm.DB) (err error) {
				dbNow, err = authorityFixtureNow(tx)
				return err
			})
			due := dbNow.Add(-time.Second)
			if oldest.Lease != nil && !oldest.Lease.After(due) {
				due = oldest.Lease.Add(-time.Second)
			}
			for i, claim := range []Operation{badClaim, goodClaim} {
				expireFixtureOperationLease(t, m.s.DB, claim)
				priority := m.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ? AND resource_id = ? AND version = ? AND attempt_id = ?", claim.OrgID, claim.ID, claim.ResourceID, claim.Version, claim.AttemptID).UpdateColumn("lease_until", due.Add(time.Duration(i-1)*time.Millisecond))
				must(t, priority.Error)
				if priority.RowsAffected != 1 {
					t.Fatal("fixture failed to prioritize its exact expired claim")
				}
			}
			funds := m.funds(t)
			wantCause := error(ErrConflict)
			if fault == "input_schema" {
				wantCause = invocation.ErrVideoSchemaUnsupported
			}
			requireRecoveryCause(t, m.s.recoverExpiredMutationAttempts(context.Background()), wantCause, 1)
			if m.operation(t, badClaim.ID).Status != "needs_review" || n.operation(t, goodClaim.ID).Status != "unknown" || m.funds(t) != funds {
				t.Fatal("permanent bad metadata blocked later expiry recovery or released funds")
			}
			badBefore := m.operation(t, badClaim.ID)
			must(t, m.s.recoverExpiredMutationAttempts(context.Background()))
			badAfter := m.operation(t, badClaim.ID)
			if badAfter.Status != badBefore.Status || badAfter.Version != badBefore.Version || !badAfter.UpdatedAt.Equal(badBefore.UpdatedAt) || m.funds(t) != funds {
				t.Fatal("quarantined expired operation remained in an automatic recovery loop")
			}
			if row, _ := m.rows(t); row.Status != "queued" {
				t.Fatal("expiry or corrupt metadata changed the original task")
			}
		})
	}
}
