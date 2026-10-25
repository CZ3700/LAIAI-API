package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
)

// Capture durable state, including the full operation rows and the account's
// journal. A rejected contract must not leave a new idempotency claim, attempt,
// terminal transition, or financial side effect behind.
func mutationRuntimeSnapshot(t *testing.T, f *fixture, id string) string {
	t.Helper()
	var state State
	var row task.Task
	var ops []Operation
	var account ledger.Account
	var journal []ledger.JournalEntry
	var attempts, events, idempotency, entries int64
	db := f.s.DB
	must(t, db.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).First(&state).Error)
	must(t, db.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).First(&row).Error)
	must(t, db.Where("org_id = ? AND resource_id = ?", f.key.OrgID, id).Order("id").Find(&ops).Error)
	hold := f.hold(t, id)
	must(t, db.Where("org_id = ? AND id = ?", f.key.OrgID, hold.AccountID).First(&account).Error)
	must(t, db.Where("account_id = ?", account.ID).Order("id").Find(&journal).Error)
	must(t, db.Model(&Attempt{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Count(&attempts).Error)
	must(t, db.Model(&Event{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Count(&events).Error)
	must(t, db.Model(&IdempotencyRecord{}).Where("org_id = ?", f.key.OrgID).Count(&idempotency).Error)
	must(t, db.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Count(&entries).Error)
	b, err := json.Marshal([]any{state, row, ops, account, journal, hold, attempts, events, idempotency, entries})
	must(t, err)
	return string(b)
}

func TestMySQLRuntimeMutationAdmissionPreservesUnknownTasks(t *testing.T) {
	cases := []struct{ name, table, column, value string }{
		{"unknown_runtime", "state", "runtime_version", "future-runtime"},
		{"empty_runtime", "state", "runtime_version", ""},
		{"unknown_adapter", "state", "adapter_version", "future-adapter"},
		{"empty_protocol", "state", "protocol_contract_version", ""},
		{"unknown_billing", "state", "billing_schema_version", "future-billing"},
		{"snapshot_mismatch", "snapshot", "billing_schema_version", "future-billing"},
	}
	for _, action := range []string{"cancel", "delete"} {
		for _, tc := range cases {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				f := mysqlFixture(t)
				v := f.create(t, "runtime-admission")
				if action == "delete" {
					_, _, err := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("prepare-terminal"), "prepare-terminal")
					must(t, err)
				}
				model := any(&State{})
				if tc.table == "snapshot" {
					model = &videobilling.Snapshot{}
				}
				must(t, f.s.DB.Model(model).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update(tc.column, tc.value).Error)
				before := mutationRuntimeSnapshot(t, f, v.ID)
				var calls atomic.Int32
				f.response = func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return reply(200, `{}`), nil
				}
				var err error
				if action == "cancel" {
					_, _, err = f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("blocked-cancel"), "blocked-cancel")
				} else {
					_, _, err = f.s.Delete(context.Background(), f.key.OrgID, v.ID, f.requestID("blocked-delete"))
				}
				if !errors.Is(err, videoruntime.ErrUnsupported) || calls.Load() != 0 || mutationRuntimeSnapshot(t, f, v.ID) != before {
					t.Fatal("unsupported mutation admitted, dispatched, or changed durable state", err)
				}
				if _, err = f.s.Get(f.key.OrgID, v.ID); err != nil {
					t.Fatal("unsupported task's persisted history became unreadable", err)
				}
			})
		}
	}
}

func TestMySQLRuntimeLocalCancelChecksCreateOperationIdentity(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "runtime-local-cancel")
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").Update("adapter_version", "").Error)
	before := mutationRuntimeSnapshot(t, f, v.ID)
	_, _, err := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("cancel"), "runtime-cancel")
	if !errors.Is(err, videoruntime.ErrUnsupported) || mutationRuntimeSnapshot(t, f, v.ID) != before {
		t.Fatal("local cancellation released an unknown create operation", err)
	}
}

func TestMySQLRuntimeLocalCancelRejectsUnknownExecutionFormat(t *testing.T) {
	for _, mode := range []string{"unknown", "unknown_expired", "unknown_disabled", "missing", "wrong_reference"} {
		t.Run(mode, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "runtime-cancel-format")
			var state State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&state).Error)
			want := invocation.ErrVideoSchemaUnsupported
			switch mode {
			case "missing":
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("effective_payload_ref", "missing_execution_payload").Error)
				must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").Update("payload_ref", "missing_execution_payload").Error)
				want = invocation.ErrVideoNotFound
			case "wrong_reference":
				must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").Update("payload_ref", "other_execution_payload").Error)
				want = videoruntime.ErrUnsupported
			default:
				updates := map[string]any{"schema_version": 2}
				if mode == "unknown_expired" {
					updates["expires_at"] = f.now
				} else if mode == "unknown_disabled" {
					updates["capture_status"] = invocation.VideoCaptureDisabled
				}
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, state.EffectivePayloadRef).Updates(updates).Error)
			}
			before := mutationRuntimeSnapshot(t, f, v.ID)
			_, _, err := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("format-cancel"), "runtime-format-cancel")
			if !errors.Is(err, want) || mutationRuntimeSnapshot(t, f, v.ID) != before || f.posts.Load() != 0 {
				t.Fatal("local cancellation interpreted an unknown/missing execution format as refundable", err)
			}
		})
	}
}

func TestMySQLRuntimeLocalCancelKnownExpiredExecutionStillReleases(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "runtime-known-expired-cancel")
	var state State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&state).Error)
	must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, state.EffectivePayloadRef).Updates(map[string]any{"expires_at": f.now, "capture_status": invocation.VideoCaptureExpired}).Error)
	op, _, err := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("expired-cancel"), "runtime-expired-cancel")
	must(t, err)
	if op.Status != "confirmed" || f.posts.Load() != 0 || f.hold(t, v.ID).Status != ledger.HoldReleased {
		t.Fatal("explicit v1 expiry broke an undispatched local cancellation")
	}
}

func TestMySQLRuntimeMutationClaimChecksPersistedIdentity(t *testing.T) {
	for _, kind := range []string{"cancel_task", "delete_task"} {
		for _, target := range []string{"state", "operation", "snapshot"} {
			t.Run(kind+"/"+target, func(t *testing.T) {
				f := mysqlFixture(t)
				v := f.create(t, "runtime-mutation-claim")
				f.dispatch(t, v.ID)
				var op Operation
				var err error
				if kind == "cancel_task" {
					op, _, err = f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("cancel"), "runtime-cancel-claim")
				} else {
					must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
					op, _, err = f.s.Delete(context.Background(), f.key.OrgID, v.ID, f.requestID("delete"))
				}
				must(t, err)
				var state State
				must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&state).Error)
				must(t, requireOperationRuntime(state, op))
				switch target {
				case "state":
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", "future-runtime").Error)
				case "operation":
					must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).Update("protocol_contract_version", "").Error)
				case "snapshot":
					must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("billing_schema_version", "future-billing").Error)
				}
				before := mutationRuntimeSnapshot(t, f, v.ID)
				var calls atomic.Int32
				f.response = func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return reply(200, `{}`), nil
				}
				// Pass the stale, supported candidate deliberately. Claim must
				// inspect the persisted operation and the current task contract.
				err = f.s.processMutation(context.Background(), op)
				if !errors.Is(err, videoruntime.ErrUnsupported) || calls.Load() != 0 || mutationRuntimeSnapshot(t, f, v.ID) != before {
					t.Fatal("mutation claim bypassed a persisted contract refusal", err)
				}
			})
		}
	}
}

func TestMySQLRuntimeMutationResponsePersistsBeforeCompatibilityCheck(t *testing.T) {
	for _, kind := range []string{"cancel_task", "delete_task"} {
		t.Run(kind, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "runtime-mutation-response")
			f.dispatch(t, v.ID)
			var op Operation
			var err error
			if kind == "cancel_task" {
				op, _, err = f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("cancel"), "runtime-response-cancel")
			} else {
				must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
				op, _, err = f.s.Delete(context.Background(), f.key.OrgID, v.ID, f.requestID("delete"))
			}
			must(t, err)
			holdBefore := f.hold(t, v.ID)
			var deletes atomic.Int32
			f.response = func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodDelete {
					t.Fatal("mutation made an unexpected provider request")
				}
				deletes.Add(1)
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("adapter_version", "future-adapter").Error)
				return reply(200, `{}`), nil
			}
			err = f.s.processMutation(context.Background(), op)
			if !errors.Is(err, videoruntime.ErrUnsupported) {
				t.Fatal("response was applied under an unsupported task contract", err)
			}
			var saved Operation
			must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&saved).Error)
			if saved.ReceiptRef == "" || saved.ReceiptAppliedAt != nil || saved.AdapterVersion != videoruntime.LegacyV1().AdapterVersion || saved.HTTPStatus != http.StatusOK {
				t.Fatal("sent response lost its producing contract or was marked applied")
			}
			var attempts int64
			must(t, f.s.DB.Model(&Attempt{}).Where("org_id = ? AND task_id = ? AND evidence_ref = ?", f.key.OrgID, v.ID, saved.ReceiptRef).Count(&attempts).Error)
			if attempts != 1 {
				t.Fatal("mutation response evidence was not durably recorded")
			}
			before := mutationRuntimeSnapshot(t, f, v.ID)
			err = f.s.applyMutationReceipt(context.Background(), saved)
			if !errors.Is(err, videoruntime.ErrUnsupported) || mutationRuntimeSnapshot(t, f, v.ID) != before {
				t.Fatal("recovery applied unsupported evidence", err)
			}
			must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("adapter_version", videoruntime.LegacyV1().AdapterVersion).Error)
			must(t, f.s.applyMutationReceipt(context.Background(), saved))
			afterFirst := mutationRuntimeSnapshot(t, f, v.ID)
			must(t, f.s.applyMutationReceipt(context.Background(), saved))
			if mutationRuntimeSnapshot(t, f, v.ID) != afterFirst || deletes.Load() != 1 {
				t.Fatal("compatible recovery reapplied or resent a mutation")
			}
			holdAfter := f.hold(t, v.ID)
			if holdAfter.Status != holdBefore.Status || !holdAfter.Amount.Equal(holdBefore.Amount) || !holdAfter.CapturedAmount.Equal(holdBefore.CapturedAmount) {
				t.Fatal("mutation compatibility recovery changed accounting")
			}
		})
	}
}
