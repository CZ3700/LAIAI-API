package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Only the successful, non-locking staging read in the execution reader occurs
// after claim commit. StageExecutionTx also reads this table FOR UPDATE inside
// the claim transaction; that earlier read must never trigger these mutations.
// An independent connection first proves the complete committed claim/EA, then
// changes the captured fact before the final send guard.
func TestMySQLMutationProducerFinalGuardRejectsChangedFacts(t *testing.T) {
	for _, fault := range []string{"lease", "status", "version", "attempt", "target", "binding", "state_runtime", "operation_runtime", "soft_delete", "pause", "delete_finances", "delete_operation"} {
		t.Run(fault, func(t *testing.T) {
			final, action := "", "cancel_task"
			if strings.HasPrefix(fault, "delete_") {
				final, action = "succeeded", "delete_task"
			}
			p := newMutationProducerFixture(t, final)
			op := p.pending(t, action)
			before := p.funds(t)
			var hit atomic.Bool
			var stageLockReads atomic.Int32
			var readerHits atomic.Int32
			var committedPayload string
			name := fmt.Sprintf("producer-final-guard-%d", p.key.OrgID)
			must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table != "invocation_payload_staging" {
					return
				}
				if _, lockedRead := tx.Statement.Clauses["FOR"]; lockedRead {
					stageLockReads.Add(1)
					return
				}
				stage, ok := tx.Statement.Dest.(*invocation.VideoPayloadStaging)
				if !ok || tx.Error != nil || tx.RowsAffected != 1 || stage.PayloadID == "" {
					return
				}
				if readerHits.Add(1) != 1 || !hit.CompareAndSwap(false, true) {
					t.Fatal("fault callback matched more than one execution read")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				// This handle cannot reuse the execution reader's transaction.
				// Seeing the new immutable registration proves the claim committed;
				// merely inspecting the callback transaction would not prove that.
				db := p.s.DB.WithContext(ctx)
				var committed Operation
				must(t, db.Where("org_id = ? AND id = ? AND resource_id = ?", p.key.OrgID, op.ID, p.taskID).Take(&committed).Error)
				if committed.Status != "in_flight" || committed.Version != op.Version+1 || committed.AttemptID == "" || committed.PayloadRef != stage.PayloadID || committed.LeaseUntil == nil {
					t.Fatal("fault callback ran before a complete claim was independently visible")
				}
				var registered ExecutionAttempt
				must(t, db.Where("org_id = ? AND task_id = ? AND operation_id = ? AND attempt_id = ?", p.key.OrgID, p.taskID, op.ID, committed.AttemptID).Take(&registered).Error)
				if registered.InputPayloadRef != stage.PayloadID || registered.OperationClaimVersion != committed.Version || registered.ExecutionSchemaVersion != 2 || registered.InputIdentityHMAC == "" {
					t.Fatal("fault callback did not observe the committed immutable input")
				}
				committedPayload = stage.PayloadID
				operations := db.Model(&Operation{}).Where("org_id = ? AND id = ? AND resource_id = ? AND version = ? AND attempt_id = ?", p.key.OrgID, op.ID, p.taskID, committed.Version, committed.AttemptID)
				states := db.Model(&State{}).Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID)
				var changed *gorm.DB
				switch fault {
				case "lease":
					expireFixtureOperationLease(t, db, committed)
				case "status":
					changed = operations.Update("status", "unknown")
				case "version":
					changed = operations.Update("version", gorm.Expr("version + 1"))
				case "attempt":
					changed = operations.Update("attempt_id", "att_replaced_by_another_claim")
				case "target":
					changed = db.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID).Update("upstream_task_id", "up_changed_target")
				case "binding":
					changed = db.Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID).Delete(&UpstreamBinding{})
				case "state_runtime":
					changed = states.Update("runtime_version", "future-runtime")
				case "operation_runtime":
					changed = operations.Update("runtime_version", "future-runtime")
				case "soft_delete":
					changed = states.Update("deleted_at", p.now)
				case "pause":
					p.s.responsePaused.Store(true)
				case "delete_finances":
					changed = states.Update("billing_state", "pending_usage")
				case "delete_operation":
					changed = states.Update("deletion_operation_id", "op_replaced_delete")
				}
				if changed != nil {
					must(t, changed.Error)
					if changed.RowsAffected != 1 {
						t.Fatal("fault did not change exactly one committed row")
					}
				}
			}))
			t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
			_ = p.s.processMutation(context.Background(), op)
			must(t, p.s.DB.Callback().Query().Remove(name))
			current := p.operation(t, op.ID)
			var successfulReads int64
			must(t, p.s.DB.Model(&invocation.VideoPayloadAccess{}).Where("org_id = ? AND operation_id = ? AND payload_id = ? AND phase = ? AND outcome = ?", p.key.OrgID, op.ID, committedPayload, "finished", "succeeded").Count(&successfulReads).Error)
			if stageLockReads.Load() == 0 || readerHits.Load() != 1 || !hit.Load() || successfulReads != 1 {
				t.Fatal("test did not cross Stage, committed claim and successful execution read before the final guard")
			}
			if p.deletes.Load() != 0 || current.Status == "pending" || current.Status == "rejected" || p.funds(t) != before || responseInboxCount(t, p.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 || responseInboxCount(t, p.fixture, &Attempt{}, "org_id = ? AND operation_id = ? AND phase = ?", p.key.OrgID, op.ID, "not_sent") != 0 {
				t.Fatal("lost claim sent HTTP, fabricated no-send evidence or changed funds")
			}
			p.execution(t, op)
		})
	}
}

func TestMySQLMutationProducerPauseAfterFinalDatabaseReadPreventsHTTP(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	op := p.pending(t, "cancel_task")
	before := p.funds(t)
	var hit atomic.Int32
	name := fmt.Sprintf("producer-after-final-select-%d", p.key.OrgID)
	must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		sql := strings.ToLower(tx.Statement.SQL.String())
		if strings.Contains(sql, "count(*)") && strings.Contains(sql, "ea.operation_claim_version") && strings.Contains(sql, "video_upstream_bindings ub") {
			hit.Add(1)
			p.s.responsePaused.Store(true)
		}
	}))
	t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
	_ = p.s.processMutation(context.Background(), op)
	must(t, p.s.DB.Callback().Query().Remove(name))
	if hit.Load() != 1 || !p.s.DispatchPaused() || p.deletes.Load() != 0 || p.operation(t, op.ID).Status != "in_flight" || p.funds(t) != before || responseInboxCount(t, p.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 {
		t.Fatal("pause after the successful final database read was ignored")
	}
}

func TestMySQLMutationProducerActionInputUnknownOrExpiredCannotSend(t *testing.T) {
	for _, fault := range []string{"unknown_schema", "expired_during_read"} {
		t.Run(fault, func(t *testing.T) {
			p := newMutationProducerFixture(t, "")
			var op Operation
			var oldAttempt ExecutionAttempt
			var expires time.Time
			if fault == "expired_during_read" {
				expires = executionFixtureNow(t, p.s.DB).Add(3 * time.Second)
				oldAttempt, op = executionOldMutationClaim(t, p.mutationConsumerFixture, "cancel_task", expires)
			} else {
				op = p.pending(t, "cancel_task")
			}
			before := p.funds(t)
			var hit atomic.Bool
			name := fmt.Sprintf("producer-action-input-%d", p.key.OrgID)
			if fault == "unknown_schema" {
				must(t, p.s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
					lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
					if tx.Statement.Table != "invocation_payloads" || !ok || lock.Strength != "SHARE" || !hit.CompareAndSwap(false, true) {
						return
					}
					// Before the short reader acquires its SHARE lock, on a
					// second connection; never UPDATE behind our own held lock.
					a := p.execution(t, op)
					must(t, p.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", p.key.OrgID, a.InputPayloadRef).Update("schema_version", 3).Error)
				}))
			} else {
				must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "invocation_payload_staging" && hit.CompareAndSwap(false, true) {
						executionWaitExpired(t, p.s.DB, expires)
						expireFixtureOperationLease(t, p.s.DB, op)
					}
				}))
			}
			t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
			if fault == "expired_during_read" {
				// The new factory cannot legitimately make a short-lived input.
				// Exercise its production reader/no-send gates with a real N-1
				// capsule, never rewrite expires/AAD after registration.
				executionWaitExpired(t, p.s.DB, expires.Add(-500*time.Millisecond))
				_, err := p.s.readMutationAction(context.Background(), op, oldAttempt, "official-test-credential")
				if !errors.Is(err, invocation.ErrVideoExpired) {
					t.Fatal("input did not expire during its actual read", err)
				}
				if err := p.s.saveMutationNotSent(context.Background(), op, oldAttempt, "official-test-credential"); !errors.Is(err, ErrConflict) {
					t.Fatal("expired claim obtained no-send permission", err)
				}
			} else {
				_ = p.s.processMutation(context.Background(), op)
			}
			must(t, p.s.DB.Callback().Query().Remove(name))
			current := p.operation(t, op.ID)
			if !hit.Load() || p.deletes.Load() != 0 || current.Status != "in_flight" || p.funds(t) != before || responseInboxCount(t, p.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 {
				t.Fatal("unreadable action input was sent, refunded or reset to pending")
			}
			p.execution(t, op)
		})
	}
}

func TestMySQLMutationProducerCoolingCancellationCanStillFinishLocally(t *testing.T) {
	for _, terminalStatus := range []string{"cancelled", "running"} {
		t.Run(terminalStatus, func(t *testing.T) {
			p := newMutationProducerFixture(t, "")
			op := p.pending(t, "cancel_task")
			// Real preparation fails once and writes the persisted cooldown.
			name := fmt.Sprintf("producer-local-cooldown-%d", p.key.OrgID)
			var reads atomic.Int32
			must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				if row, ok := tx.Statement.Dest.(*CredentialSnapshot); ok && row.TaskID == p.taskID {
					reads.Add(1)
					tx.AddError(fmt.Errorf("synthetic preparation failure"))
				}
			}))
			t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
			_ = p.s.processMutation(context.Background(), op)
			if p.operation(t, op.ID).ErrorCode != "action_prepare_deferred" {
				t.Fatal("fixture failed to enter cooldown")
			}
			// Authoritative GET progress is applied through the existing reducer,
			// not by pretending that unavailable credentials mean no charge.
			p.observe(t, ark.Status(terminalStatus), true)
			before := p.funds(t)
			must(t, p.s.processMutation(context.Background(), op))
			want := "confirmed"
			if terminalStatus == "running" {
				want = "rejected"
			}
			if p.operation(t, op.ID).Status != want || reads.Load() != 1 || p.deletes.Load() != 0 || p.funds(t) != before {
				t.Fatal("cooldown/credential dependency blocked a locally decidable cancellation")
			}
		})
	}
}

func TestMySQLMutationProducerFinalSQLRejectsPaddedIdentities(t *testing.T) {
	for _, fault := range []string{"execution_target", "deletion_operation"} {
		t.Run(fault, func(t *testing.T) {
			final, action := "", "cancel_task"
			if fault == "deletion_operation" {
				final, action = "succeeded", "delete_task"
			}
			p := newMutationProducerFixture(t, final)
			op := p.pending(t, action)
			before := p.funds(t)
			var hit atomic.Bool
			name := fmt.Sprintf("producer-final-target-padding-%d", p.key.OrgID)
			must(t, p.s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table != "o" {
					return
				}
				isFinal := false
				for _, join := range tx.Statement.Joins {
					if strings.Contains(join.Name, "video_upstream_bindings ub") {
						isFinal = true
					}
				}
				if !isFinal || !hit.CompareAndSwap(false, true) {
					return
				}
				// All prior metadata and Go byte-equality checks have completed. The
				// stored column uses PAD SPACE for legacy joins; its SQL comparison
				// must also check length to retain the captured exact target identity.
				if fault == "execution_target" {
					a := p.execution(t, op)
					must(t, p.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", a.OrgID, a.AttemptID).Update("target_upstream_task_id", *a.TargetUpstreamTaskID+" ").Error)
				} else {
					must(t, p.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID).Update("deletion_operation_id", op.ID+" ").Error)
				}
			}))
			t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
			_ = p.s.processMutation(context.Background(), op)
			must(t, p.s.DB.Callback().Query().Remove(name))
			if !hit.Load() || p.deletes.Load() != 0 || p.operation(t, op.ID).Status != "in_flight" || p.funds(t) != before || responseInboxCount(t, p.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 {
				t.Fatal("final SQL accepted a padded execution target after immutable checks")
			}
		})
	}
}

func TestMySQLMutationProducerAnyRegisteredProtocolBlocksLegacyFallback(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	op := p.pending(t, "cancel_task")
	var hit atomic.Bool
	name := fmt.Sprintf("producer-register-before-restart-%d", p.key.OrgID)
	must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "invocation_payload_staging" && hit.CompareAndSwap(false, true) {
			p.s.responsePaused.Store(true)
		}
	}))
	t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
	_ = p.s.processMutation(context.Background(), op)
	must(t, p.s.DB.Callback().Query().Remove(name))
	a := p.execution(t, op)
	if !hit.Load() || p.deletes.Load() != 0 {
		t.Fatal("fixture did not register through producer before stopping dispatch")
	}
	// Emulate a future-compatible writer that retained its registration while
	// a legacy pending selector can still see the operation. The input itself
	// remains the genuine staged schema-2 identity; no fake HMAC is inserted.
	must(t, p.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", a.OrgID, a.AttemptID).Update("storage_protocol", "future-actions-v3").Error)
	must(t, p.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Update("status", "pending").Error)
	current := p.operation(t, op.ID)
	before := p.funds(t)
	for _, enabled := range []bool{false, true} {
		restarted, err := NewService(p.s.DB, p.s.Keyring, p.s.Secret, p.s.Log)
		must(t, err)
		restarted.Now, restarted.Client, restarted.Gate = p.s.Now, p.s.Client, p.s.Gate
		restarted.MutationResponseInboxEnabled = enabled
		must(t, restarted.ConfigureResponseInbox(testMutationProducerDSN(t), false))
		err = restarted.processMutation(context.Background(), current)
		must(t, restarted.CloseResponseInbox())
		if err == nil || p.deletes.Load() != 0 || p.operation(t, op.ID).Version != current.Version || p.funds(t) != before || responseInboxCount(t, p.fixture, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", op.OrgID, op.ID) != 1 {
			t.Fatal("flag change fell back around an unknown immutable registration")
		}
	}
}
