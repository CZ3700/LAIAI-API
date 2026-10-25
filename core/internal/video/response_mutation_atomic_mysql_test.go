package video

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

func TestMySQLResponseMutationRegistrationIsAtomic(t *testing.T) {
	for _, table := range []string{"invocation_payloads", "invocation_payload_staging", "video_execution_attempts"} {
		t.Run(table, func(t *testing.T) {
			m := newMutationConsumerFixture(t, "")
			op, p := m.prepareClaim(t, "cancel_task")
			before, funds := m.operation(t, op.ID), m.funds(t)
			payloads := responseInboxCount(t, m.fixture, &invocation.VideoPayload{}, "org_id = ?", m.key.OrgID)
			name := fmt.Sprintf("mutation-registration-%d", m.key.OrgID)
			injected := errors.New("controlled mutation registration failure")
			must(t, m.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(injected)
				}
			}))
			t.Cleanup(func() { _ = m.s.DB.Callback().Create().Remove(name) })
			if _, err := m.commitClaim(context.Background(), op, p); err == nil {
				t.Fatal("registration failure was ignored")
			}
			must(t, m.s.DB.Callback().Create().Remove(name))
			after := m.operation(t, op.ID)
			if after.Status != before.Status || after.Version != before.Version || after.AttemptID != before.AttemptID || after.PayloadRef != before.PayloadRef || m.funds(t) != funds || responseInboxCount(t, m.fixture, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", m.key.OrgID, op.ID) != 0 || responseInboxCount(t, m.fixture, &invocation.VideoPayload{}, "org_id = ?", m.key.OrgID) != payloads || responseInboxCount(t, m.fixture, &invocation.VideoPayloadStaging{}, "payload_id = ?", p.payload.PayloadID()) != 0 {
				t.Fatal("failed registration committed a partial claim, input or immutable attempt")
			}
			attempt, err := m.commitClaim(context.Background(), op, p)
			must(t, err)
			if attempt.InputPayloadRef != p.payload.PayloadID() || m.operation(t, op.ID).PayloadRef != attempt.InputPayloadRef {
				t.Fatal("retry did not atomically bind the prepared action input")
			}
		})
	}
}

func TestMySQLResponseMutationConcurrentAppendAndApplyAreIdempotent(t *testing.T) {
	for _, action := range []string{"cancel_task", "delete_task"} {
		t.Run(action, func(t *testing.T) {
			final := ""
			if action == "delete_task" {
				final = "succeeded"
			}
			m := newMutationConsumerFixture(t, final)
			attempt, claim := m.register(t, action)
			funds := m.funds(t)
			const workers = 8
			prepared := make([]*preparedResponseReceipt, workers)
			for i := range prepared {
				// Reload from MySQL so equal target strings have independent
				// pointer addresses. Pointer equality is not attempt identity.
				var loaded ExecutionAttempt
				must(t, m.s.DB.Where("org_id = ? AND attempt_id = ?", m.key.OrgID, attempt.AttemptID).Take(&loaded).Error)
				var err error
				prepared[i], err = m.s.prepareMutationResponse(loaded, mutationResponse(ark.OutcomeAccepted, 200), "official-test-credential")
				must(t, err)
			}
			var wg sync.WaitGroup
			rows := make(chan ResponseInbox, workers)
			errs := make(chan error, workers)
			for _, p := range prepared {
				wg.Add(1)
				go func(p *preparedResponseReceipt) {
					defer wg.Done()
					r, err := m.s.appendResponseReceipt(context.Background(), p)
					rows <- r
					errs <- err
				}(p)
			}
			wg.Wait()
			close(rows)
			close(errs)
			for err := range errs {
				must(t, err)
			}
			var first ResponseInbox
			for r := range rows {
				if first.ReceiptID == "" {
					first = r
				} else if first.ReceiptID != r.ReceiptID || first.PayloadRef != r.PayloadRef || first.EvidenceHMAC != r.EvidenceHMAC {
					t.Fatal("concurrent duplicate evidence created different receipts")
				}
			}
			if responseInboxCount(t, m.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", m.key.OrgID, claim.ID) != 1 || responseInboxCount(t, m.fixture, &ReceiptApplication{}, "org_id = ? AND attempt_id = ?", m.key.OrgID, attempt.AttemptID) != 1 || responseInboxCount(t, m.fixture, &invocation.VideoPayload{}, "org_id = ? AND request_id = ?", m.key.OrgID, first.ReceiptID) != 1 || responseInboxCount(t, m.fixture, &invocation.VideoPayloadStaging{}, "payload_id = ?", first.PayloadRef) != 1 {
				t.Fatal("duplicate append created partial or duplicate dependent evidence")
			}
			m.recoverable(t, claim)
			must(t, m.s.applyMutationInbox(context.Background(), m.key.OrgID, first.ReceiptID, nil))
			effects := m.effects(t)
			errs = make(chan error, workers)
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- m.s.applyMutationInbox(context.Background(), m.key.OrgID, first.ReceiptID, nil)
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				must(t, err)
			}
			if responseInboxApp(t, m.fixture, first).State != "applied" || m.funds(t) != funds || m.effects(t) != effects {
				t.Fatal("concurrent replay repeated application or financial effects")
			}
		})
	}
}

func TestMySQLResponseMutationApplyRollbackPreservesEvidence(t *testing.T) {
	m := newMutationConsumerFixture(t, "succeeded")
	attempt, claim := m.register(t, "delete_task")
	r := m.append(t, attempt, mutationResponse(ark.OutcomeAccepted, 200))
	funds, effects := m.funds(t), m.effects(t)
	name := fmt.Sprintf("mutation-apply-failure-%d", m.key.OrgID)
	injected := errors.New("controlled application failure")
	must(t, m.s.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_receipt_applications" {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = m.s.DB.Callback().Update().Remove(name) })
	if err := m.s.applyClaimedMutationInbox(context.Background(), claim, r.ReceiptID); err == nil {
		t.Fatal("application failure was ignored")
	}
	must(t, m.s.DB.Callback().Update().Remove(name))
	_, st := m.rows(t)
	if st.DeletedAt != nil || m.operation(t, claim.ID).Status != "in_flight" || responseInboxApp(t, m.fixture, r).State != "pending" || m.funds(t) != funds || m.effects(t) != effects || m.s.DispatchPaused() {
		t.Fatal("application rollback lost durable evidence or partially deleted the task")
	}
	must(t, m.s.applyClaimedMutationInbox(context.Background(), claim, r.ReceiptID))
	effects = m.effects(t)
	must(t, m.s.applyMutationInbox(context.Background(), m.key.OrgID, r.ReceiptID, nil))
	if m.funds(t) != funds || m.effects(t) != effects {
		t.Fatal("application retry repeated side effects")
	}
}

func TestMySQLResponseMutationForgedCapturedIdentityCannotAppend(t *testing.T) {
	for _, fault := range []string{"organization", "target", "operation", "credential"} {
		t.Run(fault, func(t *testing.T) {
			m := newMutationConsumerFixture(t, "")
			a, claim := m.register(t, "cancel_task")
			forged := a
			switch fault {
			case "organization":
				forged.OrgID++
			case "target":
				target := "other_" + *a.TargetUpstreamTaskID
				forged.TargetUpstreamTaskID = &target
			case "operation":
				forged.OperationID = "other_" + a.OperationID
			case "credential":
				forged.CredentialSnapshotID++
			}
			funds := m.funds(t)
			p, err := m.s.prepareMutationResponse(forged, mutationResponse(ark.OutcomeAccepted, 200), "official-test-credential")
			if err == nil {
				_, err = m.s.appendResponseReceipt(context.Background(), p)
			}
			if err == nil || responseInboxCount(t, m.fixture, &ResponseInbox{}, "attempt_id = ?", a.AttemptID) != 0 || m.funds(t) != funds || m.operation(t, claim.ID).Status != "in_flight" {
				t.Fatal("forged captured identity appended evidence or changed funds")
			}
		})
	}
}
