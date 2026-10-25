package video

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

func TestMySQLResponseMutationAcknowledgementHasNoFinancialAuthority(t *testing.T) {
	for _, action := range []string{"cancel_task", "delete_task"} {
		for _, tc := range []struct {
			name          string
			outcome       ark.Outcome
			status        int
			wantOperation string
			deleted       bool
		}{
			{"ack200", ark.OutcomeAccepted, 200, "unknown", true},
			{"ack202", ark.OutcomeAccepted, 202, "unknown", true},
			{"ack204", ark.OutcomeAccepted, 204, "unknown", true},
			{"missing404", ark.OutcomeRejected, 404, "unknown", true},
			{"not_sent", ark.OutcomeNotSent, 0, "rejected", false},
			{"rejected400", ark.OutcomeRejected, 400, "rejected", false},
			{"uncertain502", ark.OutcomeUnknown, 502, "unknown", false},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				final := ""
				if action == "delete_task" {
					final = "succeeded"
				}
				m := newMutationConsumerFixture(t, final)
				_, beforeState := m.rows(t)
				attempt, claim := m.register(t, action)
				funds := m.funds(t)
				r := m.append(t, attempt, mutationResponse(tc.outcome, tc.status))
				if r.UpstreamTaskID != "" || attempt.TargetUpstreamTaskID == nil || *attempt.TargetUpstreamTaskID == "" || attempt.InputPayloadRef == beforeState.EffectivePayloadRef {
					t.Fatal("action target was fabricated as a response ID or action input replaced create input")
				}
				must(t, m.s.applyClaimedMutationInbox(context.Background(), claim, r.ReceiptID))
				op := m.operation(t, claim.ID)
				row, st := m.rows(t)
				want := tc.wantOperation
				if action == "delete_task" && tc.deleted {
					want = "confirmed"
				}
				if op.Status != want || op.ReceivedOutcome != string(tc.outcome) || responseInboxApp(t, m.fixture, r).State != "applied" || m.funds(t) != funds {
					t.Fatalf("mutation result changed funds or has wrong operation state: status=%s outcome=%s", op.Status, op.ReceivedOutcome)
				}
				if action == "cancel_task" {
					if row.Status != "queued" || st.DeletedAt != nil || m.hold(t, m.taskID).Status != ledger.HoldActive {
						t.Fatal("cancel acknowledgement changed the original task or released its hold")
					}
				} else if (st.DeletedAt != nil) != tc.deleted || row.Status != "succeeded" {
					t.Fatal("delete acknowledgement did not preserve the terminal task")
				}
				if st.EffectivePayloadRef != beforeState.EffectivePayloadRef {
					t.Fatal("mutation overwrote the create execution input")
				}
				effects := m.effects(t)
				must(t, m.s.applyMutationInbox(context.Background(), m.key.OrgID, r.ReceiptID, nil))
				if m.funds(t) != funds || m.effects(t) != effects {
					t.Fatal("mutation replay repeated business or financial effects")
				}
			})
		}
	}
}

func TestMySQLResponseMutationAuthoritativeQueryWinsBeforeCancelACK(t *testing.T) {
	for _, status := range []ark.Status{ark.StatusSucceeded, ark.StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			m := newMutationConsumerFixture(t, "")
			attempt, claim := m.register(t, "cancel_task")
			m.observe(t, status, true)
			funds := m.funds(t)
			r := m.append(t, attempt, mutationResponse(ark.OutcomeAccepted, 200))
			// The query can have confirmed cancellation while the old producer
			// was in flight. The consumer uses current authority, not that claim.
			if status == ark.StatusSucceeded {
				m.recoverable(t, claim)
			}
			must(t, m.s.applyMutationInbox(context.Background(), m.key.OrgID, r.ReceiptID, nil))
			effects := m.effects(t)
			must(t, m.s.applyMutationInbox(context.Background(), m.key.OrgID, r.ReceiptID, nil))
			row, _ := m.rows(t)
			op := m.operation(t, claim.ID)
			if row.Status != string(status) || m.funds(t) != funds || m.effects(t) != effects || op.ReceivedOutcome != string(ark.OutcomeAccepted) || responseInboxApp(t, m.fixture, r).State != "applied" {
				t.Fatal("late cancel acknowledgement overwrote GET authority or omitted consumed evidence")
			}
			if status == ark.StatusCancelled && op.Status != "confirmed" {
				t.Fatal("late ACK regressed an authoritatively confirmed cancellation")
			}
		})
	}
}

func TestMySQLResponseMutationDeleteRechecksTerminalFinanceAndBinding(t *testing.T) {
	for _, fault := range []string{"running", "pending_usage", "binding_missing", "target_case", "target_space"} {
		t.Run(fault, func(t *testing.T) {
			m := newMutationConsumerFixture(t, "succeeded")
			attempt, claim := m.register(t, "delete_task")
			r := m.append(t, attempt, mutationResponse(ark.OutcomeAccepted, 200))
			row, _ := m.rows(t)
			switch fault {
			case "running":
				must(t, m.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Update("status", "running").Error)
			case "pending_usage":
				must(t, m.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Update("billing_state", "pending_usage").Error)
			case "binding_missing":
				must(t, m.s.DB.Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Delete(&UpstreamBinding{}).Error)
			case "target_case":
				must(t, m.s.DB.Model(&UpstreamBinding{}).Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Update("upstream_task_id", "UP_"+m.taskID).Error)
			case "target_space":
				must(t, m.s.DB.Model(&UpstreamBinding{}).Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Update("upstream_task_id", row.UpstreamTaskID+" ").Error)
			}
			funds, effects := m.funds(t), m.effects(t)
			_ = m.s.applyClaimedMutationInbox(context.Background(), claim, r.ReceiptID)
			_, st := m.rows(t)
			if st.DeletedAt != nil || m.operation(t, claim.ID).Status == "confirmed" || responseInboxApp(t, m.fixture, r).State == "applied" || m.funds(t) != funds || m.effects(t) != effects {
				t.Fatal("delete accepted a changed task, financial state or exact target identity")
			}
		})
	}
}

func TestMySQLResponseMutationStaleClaimCannotApplyOrResurrect(t *testing.T) {
	for _, fault := range []string{"version", "lease", "new_attempt", "deleted"} {
		t.Run(fault, func(t *testing.T) {
			m := newMutationConsumerFixture(t, "")
			attempt, claim := m.register(t, "cancel_task")
			r := m.append(t, attempt, mutationResponse(ark.OutcomeAccepted, 200))
			switch fault {
			case "version":
				m.recoverable(t, claim)
			case "lease":
				m.now = claim.LeaseUntil.Add(time.Millisecond)
			case "new_attempt":
				must(t, m.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", m.key.OrgID, claim.ID).Updates(map[string]any{"attempt_id": "other_" + claim.AttemptID, "status": "unknown", "version": claim.Version + 1}).Error)
			case "deleted":
				must(t, m.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Update("deleted_at", m.now).Error)
			}
			funds, effects := m.funds(t), m.effects(t)
			_ = m.s.applyClaimedMutationInbox(context.Background(), claim, r.ReceiptID)
			if fault == "deleted" {
				// A tombstone may consume evidence for audit, but must never
				// reappear or become a cancelled task merely because of an ACK.
				row, st := m.rows(t)
				if st.DeletedAt == nil || row.Status != "queued" || m.funds(t) != funds {
					t.Fatal("late response resurrected or financially changed a tombstone")
				}
				_ = m.s.applyMutationInbox(context.Background(), m.key.OrgID, r.ReceiptID, nil)
				if _, st = m.rows(t); st.DeletedAt == nil || m.funds(t) != funds {
					t.Fatal("recovery resurrected a tombstone")
				}
				return
			}
			if responseInboxApp(t, m.fixture, r).AppliedAt != nil || m.funds(t) != funds || m.effects(t) != effects {
				t.Fatal("stale producer applied or altered money/events")
			}
			if fault == "version" || fault == "lease" {
				must(t, m.s.applyMutationInbox(context.Background(), m.key.OrgID, r.ReceiptID, nil))
				if responseInboxApp(t, m.fixture, r).State != "applied" {
					t.Fatal("current recovery could not consume same-attempt evidence")
				}
			} else {
				_ = m.s.applyMutationInbox(context.Background(), m.key.OrgID, r.ReceiptID, nil)
				if responseInboxApp(t, m.fixture, r).State == "applied" {
					t.Fatal("recovery consumed evidence for a new attempt or tombstone")
				}
			}
			if m.funds(t) != funds {
				t.Fatal("stale claim recovery changed funds")
			}
		})
	}
}

func TestMySQLResponseMutationCrossOrganizationCannotConsume(t *testing.T) {
	m := newMutationConsumerFixture(t, "succeeded")
	attempt, claim := m.register(t, "delete_task")
	r := m.append(t, attempt, mutationResponse(ark.OutcomeAccepted, 200))
	funds, effects := m.funds(t), m.effects(t)
	if err := m.s.applyMutationInbox(context.Background(), m.key.OrgID+1000000000, r.ReceiptID, nil); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-org lookup returned %v", err)
	}
	if responseInboxApp(t, m.fixture, r).AppliedAt != nil || m.funds(t) != funds || m.effects(t) != effects {
		t.Fatal("cross-org consumer changed evidence or funds")
	}
	must(t, m.s.applyClaimedMutationInbox(context.Background(), claim, r.ReceiptID))
	if _, st := m.rows(t); st.DeletedAt == nil {
		t.Fatal("owner could not consume the same evidence")
	}
}

func TestMySQLResponseMutationUnknownThenACKAndContradiction(t *testing.T) {
	for _, action := range []string{"cancel_task", "delete_task"} {
		t.Run(action, func(t *testing.T) {
			final := ""
			if action == "delete_task" {
				final = "succeeded"
			}
			m := newMutationConsumerFixture(t, final)
			attempt, claim := m.register(t, action)
			funds := m.funds(t)
			uncertain := m.append(t, attempt, mutationResponse(ark.OutcomeUnknown, 502))
			must(t, m.s.applyClaimedMutationInbox(context.Background(), claim, uncertain.ReceiptID))
			ack := m.append(t, attempt, mutationResponse(ark.OutcomeAccepted, 200))
			must(t, m.s.applyMutationInbox(context.Background(), m.key.OrgID, ack.ReceiptID, nil))
			if responseInboxApp(t, m.fixture, ack).State != "applied" {
				t.Fatal("definite ACK could not follow uncertainty")
			}
			bad := mutationResponse(ark.OutcomeRejected, 400)
			bad.Body = []byte(fmt.Sprintf(`{"error":{"code":"InvalidParameter"},"case":%q}`, action))
			contradiction := m.append(t, attempt, bad)
			_ = m.s.applyMutationInbox(context.Background(), m.key.OrgID, contradiction.ReceiptID, nil)
			if responseInboxApp(t, m.fixture, contradiction).State != "review" || m.funds(t) != funds {
				t.Fatal("contradictory evidence escaped review or changed money")
			}
			// Mutation disagreement is scoped to the action, not the create
			// identity quarantine. Ordinary authoritative GET remains consumable.
			if action == "cancel_task" {
				count := responseInboxCount(t, m.fixture, &Operation{}, "org_id = ? AND resource_id = ? AND kind = ?", m.key.OrgID, m.taskID, "cancel_task")
				replayed, replay, err := m.s.Cancel(context.Background(), m.key.OrgID, m.taskID, "reviewed-cancel-again", "new-idem-after-review")
				must(t, err)
				if !replay || replayed.ID != claim.ID || replayed.Status != "needs_review" || responseInboxCount(t, m.fixture, &Operation{}, "org_id = ? AND resource_id = ? AND kind = ?", m.key.OrgID, m.taskID, "cancel_task") != count {
					t.Fatal("new idempotency key bypassed reviewed cancellation with a new operation")
				}
				_ = m.s.processMutation(context.Background(), replayed)
				m.observe(t, ark.StatusSucceeded, true)
				if row, st := m.rows(t); row.Status != "succeeded" || st.BillingState != "settled" {
					t.Fatal("mutation review blocked independent authoritative GET")
				}
			}
		})
	}
}

func TestMySQLResponseMutationUnsafeOrUncertain404IsNotAcknowledgement(t *testing.T) {
	for _, fault := range []string{"unknown404", "truncated", "credential_reflection"} {
		t.Run(fault, func(t *testing.T) {
			m := newMutationConsumerFixture(t, "succeeded")
			a, claim := m.register(t, "delete_task")
			result := mutationResponse(ark.OutcomeAccepted, 404)
			switch fault {
			case "unknown404":
				result.Outcome, result.HTTPStatus = ark.OutcomeUnknown, 404
			case "truncated":
				result.Truncated = true
			case "credential_reflection":
				result.TaskID = "official-test-credential"
			}
			funds := m.funds(t)
			r := m.append(t, a, result)
			must(t, m.s.applyClaimedMutationInbox(context.Background(), claim, r.ReceiptID))
			_, st := m.rows(t)
			if st.DeletedAt != nil || m.operation(t, claim.ID).Status != "unknown" || m.funds(t) != funds {
				t.Fatal("unsafe or uncertain response was interpreted as a deletion ACK")
			}
		})
	}
}
