package video

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// E04: each local release entry has an expired, valid positive control. Unknown
// schema and broken immutable identity must win over exactly that expiry fact.
func TestMySQLExecutionInputIdentityPrecedesLocalRelease(t *testing.T) {
	for _, entry := range []string{"cancel", "legacy_not_sent", "shared_reducer"} {
		for _, fault := range []string{"valid_expired", "schema", "correlation", "request", "purpose", "organization"} {
			t.Run(entry+"/"+fault, func(t *testing.T) {
				f := mysqlFixture(t)
				v := f.create(t, "execution-identity-release")
				identity := executionOldCreateInput(t, f, v.ID, executionFixtureNow(t, f.s.DB).Add(-time.Second))
				_, _, op := executionCreateRows(t, f, v.ID)
				if entry != "cancel" {
					must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
						now, err := authorityFixtureNow(tx)
						if err != nil {
							return err
						}
						op.AttemptID, err = randomID("att_")
						if err != nil {
							return err
						}
						op.Version++
						op.Status = "in_flight"
						lease := now.Add(3 * time.Minute)
						op.LeaseUntil = &lease
						return tx.Model(&Operation{}).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Updates(map[string]any{"status": op.Status, "version": op.Version, "attempt_id": op.AttemptID, "lease_until": lease}).Error
					}))
				}
				updates := map[string]any{}
				switch fault {
				case "schema":
					updates["schema_version"] = 99
				case "correlation":
					updates["correlation_key"] = fmt.Sprintf("invalid-correlation-%d", f.key.OrgID)
				case "request":
					updates["create_request_id"] = "another-request"
				case "purpose":
					updates["purpose"] = invocation.VideoPurposeHistory
				case "organization":
					updates["org_id"] = f.key.OrgID + 1000000000
				}
				if fault == "request" {
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Updates(updates).Error)
				} else if len(updates) > 0 {
					must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, identity.PayloadID).Updates(updates).Error)
				}
				before := authorityFinancialFacts(t, f, v.ID)
				stateBefore := authorityTaskFacts(t, f, v.ID)
				var err error
				switch entry {
				case "cancel":
					_, _, err = f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "expiry-cancel", "expiry-cancel")
				case "legacy_not_sent":
					row, st, _ := executionCreateRows(t, f, v.ID)
					err = f.s.knownUnsent(context.Background(), row, st, op, "execution_unavailable")
				case "shared_reducer":
					err = f.s.DB.Transaction(func(tx *gorm.DB) error {
						row, st, e := loadTask(tx, f.key.OrgID, v.ID, true)
						if e != nil {
							return e
						}
						var current Operation
						if e = locked(tx).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Take(&current).Error; e != nil {
							return e
						}
						current.ReceivedOutcome, current.ReceiptRef = string(ark.OutcomeNotSent), "typed-no-send-evidence"
						return f.s.applyCreateResultTx(tx, row, st, current, current, "typed-no-send-evidence", f.now)
					})
				}
				if fault == "valid_expired" {
					must(t, err)
					if f.hold(t, v.ID).Status != ledger.HoldReleased {
						t.Fatal("valid expired input prevented authorized local release")
					}
				} else if err == nil || authorityFinancialFacts(t, f, v.ID) != before || authorityTaskFacts(t, f, v.ID) != stateBefore {
					t.Fatal("invalid identity became a local refund/state transition", err)
				}
				if f.posts.Load() != 0 || f.gets.Load() != 0 {
					t.Fatal("local identity validation issued HTTP")
				}
			})
		}
	}
}

// E04 early paths are before the plaintext reader. The actual asset query is
// failed, or the actual reserved supplier bucket is blocked; no fake reducer.
func TestMySQLExecutionInputEarlyFailureIdentityAndInbox(t *testing.T) {
	for _, inbox := range []bool{false, true} {
		for _, reason := range []string{"assets", "budget"} {
			for _, corrupt := range []bool{false, true} {
				t.Run(fmt.Sprintf("inbox=%v/%s/corrupt=%v", inbox, reason, corrupt), func(t *testing.T) {
					f := mysqlFixture(t)
					f.s.ResponseInboxEnabled = inbox
					v := f.create(t, "execution-early-failure")
					_, st, op := executionCreateRows(t, f, v.ID)
					if reason == "budget" {
						var r videobilling.QuotaReservation
						must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND supplier = ?", f.key.OrgID, v.ID, true).Take(&r).Error)
						must(t, f.s.DB.Model(&videobilling.QuotaBucket{}).Where("id = ?", r.BucketID).Update("blocked", true).Error)
					}
					before := authorityFinancialFacts(t, f, v.ID)
					var hit atomic.Bool
					name := fmt.Sprintf("execution-early-%d", f.key.OrgID)
					must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
						// Both failures occur after the initial runtime/metadata gate.
						if tx.Statement.Table != "video_task_asset_refs" || !hit.CompareAndSwap(false, true) {
							return
						}
						if corrupt {
							must(t, tx.Session(&gorm.Session{NewDB: true}).Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, st.EffectivePayloadRef).Update("correlation_key", fmt.Sprintf("broken-early-%d", f.key.OrgID)).Error)
						}
						if reason == "assets" {
							tx.AddError(errors.New("injected asset lookup failure"))
						}
					}))
					t.Cleanup(func() { _ = f.s.DB.Callback().Query().Remove(name) })
					err := f.s.processOperation(context.Background(), op)
					must(t, f.s.DB.Callback().Query().Remove(name))
					if !hit.Load() {
						t.Fatal("early validation fault did not execute")
					}
					if corrupt {
						if err == nil || authorityFinancialFacts(t, f, v.ID) != before || responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 0 {
							t.Fatal("early invalid identity became NotSent or refund", err)
						}
					} else {
						must(t, err)
						if f.hold(t, v.ID).Status != ledger.HoldReleased {
							t.Fatal("valid early failure did not use original release reducer")
						}
						want := int64(0)
						if inbox {
							want = 1
						}
						if responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND task_id = ? AND outcome = ?", f.key.OrgID, v.ID, string(ark.OutcomeNotSent)) != want {
							t.Fatal("registered no-send evidence missing or duplicated")
						}
					}
					if f.posts.Load() != 0 || f.gets.Load() != 0 {
						t.Fatal("early failure issued provider HTTP")
					}
				})
			}
		}
	}
}

func TestMySQLExecutionActionExpiredIdentityStillControlsNotSent(t *testing.T) {
	for _, fault := range []string{"valid_expired", "schema", "correlation", "identity_hmac"} {
		t.Run(fault, func(t *testing.T) {
			p := newMutationProducerFixture(t, "")
			a, op := executionOldMutationClaim(t, p.mutationConsumerFixture, "cancel_task", executionFixtureNow(t, p.s.DB).Add(-time.Second))
			switch fault {
			case "schema":
				must(t, p.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, a.InputPayloadRef).Update("schema_version", 99).Error)
			case "correlation":
				must(t, p.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, a.InputPayloadRef).Update("correlation_key", fmt.Sprintf("bad-action-%d", op.OrgID)).Error)
			case "identity_hmac":
				must(t, p.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", op.OrgID, a.AttemptID).Update("input_identity_hmac", "not-the-original-identity").Error)
			}
			before := authorityFinancialFacts(t, p.fixture, p.taskID)
			_, err := p.s.readMutationAction(context.Background(), op, a, "official-test-credential")
			if fault == "valid_expired" {
				if !errors.Is(err, invocation.ErrVideoExpired) {
					t.Fatal("valid old input did not report DB expiry", err)
				}
				must(t, p.s.saveMutationNotSent(context.Background(), op, a, "official-test-credential"))
				if p.operation(t, op.ID).Status != "rejected" || responseInboxCount(t, p.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ? AND outcome = ?", op.OrgID, op.ID, string(ark.OutcomeNotSent)) != 1 {
					t.Fatal("exact live sender could not persist supported expired no-send")
				}
			} else {
				if err == nil || executionReadAllowsNotSent(err) {
					t.Fatal("unknown identity was classified as expiry", err)
				}
				if err = p.s.saveMutationNotSent(context.Background(), op, a, "official-test-credential"); err == nil {
					t.Fatal("identity-invalid local no-send admitted")
				}
				if responseInboxCount(t, p.fixture, &ResponseInbox{}, "org_id = ? AND operation_id = ?", op.OrgID, op.ID) != 0 {
					t.Fatal("identity-invalid input produced a no-send receipt")
				}
			}
			if p.deletes.Load() != 0 || authorityFinancialFacts(t, p.fixture, p.taskID) != before {
				t.Fatal("input expiry/identity changed money or sent DELETE")
			}
		})
	}
}
