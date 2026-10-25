package video

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

// The staging read occurs after the real claim transaction has committed and
// before the final send guard. Updating through another database operation in
// its After callback gives a deterministic stale-claim boundary without forging
// a registration, changing the input HMAC, or exposing a production test hook.
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
			name := fmt.Sprintf("producer-final-guard-%d", p.key.OrgID)
			must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table != "invocation_payload_staging" || !hit.CompareAndSwap(false, true) {
					return
				}
				operations := p.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", p.key.OrgID, op.ID)
				states := p.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID)
				switch fault {
				case "lease":
					must(t, operations.Update("lease_until", p.now.Add(-time.Second)).Error)
				case "status":
					must(t, operations.Update("status", "unknown").Error)
				case "version":
					must(t, operations.Update("version", gorm.Expr("version + 1")).Error)
				case "attempt":
					must(t, operations.Update("attempt_id", "att_replaced_by_another_claim").Error)
				case "target":
					must(t, p.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID).Update("upstream_task_id", "up_changed_target").Error)
				case "binding":
					must(t, p.s.DB.Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID).Delete(&UpstreamBinding{}).Error)
				case "state_runtime":
					must(t, states.Update("runtime_version", "future-runtime").Error)
				case "operation_runtime":
					must(t, operations.Update("runtime_version", "future-runtime").Error)
				case "soft_delete":
					must(t, states.Update("deleted_at", p.now).Error)
				case "pause":
					p.s.responsePaused.Store(true)
				case "delete_finances":
					must(t, states.Update("billing_state", "pending_usage").Error)
				case "delete_operation":
					must(t, states.Update("deletion_operation_id", "op_replaced_delete").Error)
				}
			}))
			t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
			_ = p.s.processMutation(context.Background(), op)
			must(t, p.s.DB.Callback().Query().Remove(name))
			current := p.operation(t, op.ID)
			if !hit.Load() || p.deletes.Load() != 0 || current.Status == "pending" || current.Status == "rejected" || p.funds(t) != before || responseInboxCount(t, p.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 || responseInboxCount(t, p.fixture, &Attempt{}, "org_id = ? AND operation_id = ? AND phase = ?", p.key.OrgID, op.ID, "not_sent") != 0 {
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
			op := p.pending(t, "cancel_task")
			before := p.funds(t)
			var hit atomic.Bool
			name := fmt.Sprintf("producer-action-input-%d", p.key.OrgID)
			if fault == "unknown_schema" {
				must(t, p.s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table != "invocation_payloads" {
						return
					}
					if _, inTx := tx.Statement.ConnPool.(gorm.TxCommitter); inTx {
						return
					}
					if !hit.CompareAndSwap(false, true) {
						return
					}
					a := p.execution(t, op)
					must(t, p.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", p.key.OrgID, a.InputPayloadRef).Update("schema_version", 3).Error)
				}))
			} else {
				must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "invocation_payload_staging" && hit.CompareAndSwap(false, true) {
						p.now = p.now.Add(25 * time.Hour)
					}
				}))
			}
			t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
			_ = p.s.processMutation(context.Background(), op)
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
