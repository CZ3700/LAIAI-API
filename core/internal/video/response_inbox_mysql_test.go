package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// Store/application tests register a genuine accepted task and its exact
// in-flight claim in one transaction. Dispatch tests below use processOperation
// itself, including the registration, send guard and Mock Ark response.
func responseInboxRegister(t *testing.T, f *fixture, id string) (ExecutionAttempt, Operation) {
	t.Helper()
	var registered ExecutionAttempt
	var op Operation
	err := f.s.DB.Transaction(func(tx *gorm.DB) error {
		row, state, err := loadTask(tx, f.key.OrgID, id, true)
		if err != nil {
			return err
		}
		if err = locked(tx).Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, id, "create_task").First(&op).Error; err != nil {
			return err
		}
		op.AttemptID, err = randomID("att_")
		if err != nil {
			return err
		}
		op.Version++
		op.Status = "in_flight"
		lease := f.now.Add(3 * time.Minute)
		op.LeaseUntil = &lease
		if err = tx.Model(&Operation{}).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Updates(map[string]any{"status": op.Status, "version": op.Version, "attempt_id": op.AttemptID, "lease_until": lease}).Error; err != nil {
			return err
		}
		row.Status = "submitting"
		if err = tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", row.OrgID, row.TaskID).Update("status", row.Status).Error; err != nil {
			return err
		}
		registered, err = f.s.registerExecutionAttemptTx(tx, row, state, op)
		return err
	})
	must(t, err)
	return registered, op
}

func responseInboxResult(outcome ark.Outcome, id string) ark.Result {
	status, body := 200, fmt.Sprintf(`{"id":%q}`, id)
	if outcome == ark.OutcomeUnknown {
		status, body, id = 502, `{}`, ""
	} else if outcome == ark.OutcomeRejected {
		status, body, id = 400, `{"error":{"code":"InvalidParameter"}}`, ""
	}
	return ark.Result{Action: ark.ActionSubmit, Outcome: outcome, HTTPStatus: status, TaskID: id, Body: []byte(body)}
}

func responseInboxAppend(t *testing.T, f *fixture, attempt ExecutionAttempt, result ark.Result) ResponseInbox {
	t.Helper()
	prepared, err := f.s.prepareResponseReceipt(attempt, result, "official-test-credential")
	must(t, err)
	receipt, err := f.s.appendResponseReceipt(context.Background(), prepared)
	must(t, err)
	return receipt
}

func responseInboxCount(t *testing.T, f *fixture, model any, where string, args ...any) int64 {
	t.Helper()
	var n int64
	must(t, f.s.DB.Model(model).Where(where, args...).Count(&n).Error)
	return n
}

func responseInboxApp(t *testing.T, f *fixture, receipt ResponseInbox) ReceiptApplication {
	t.Helper()
	var app ReceiptApplication
	must(t, f.s.DB.Where("org_id = ? AND receipt_id = ?", receipt.OrgID, receipt.ReceiptID).First(&app).Error)
	return app
}

func responseInboxMakeDue(t *testing.T, f *fixture, receipt ResponseInbox) {
	t.Helper()
	// Receipt scheduling uses DB time, independently of the fixture's
	// deliberately advanced legacy lease clock. Avoid sleeps in recovery tests.
	must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", receipt.OrgID, receipt.ReceiptID).Update("next_attempt_at", gorm.Expr("UTC_TIMESTAMP(3) - INTERVAL 1 SECOND")).Error)
}

func responseInboxFunds(t *testing.T, f *fixture, id string) string {
	t.Helper()
	hold := f.hold(t, id)
	var account ledger.Account
	var journal []ledger.JournalEntry
	var entries []videobilling.BillingEntry
	var reservations []videobilling.QuotaReservation
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, hold.AccountID).First(&account).Error)
	must(t, f.s.DB.Where("account_id = ?", hold.AccountID).Order("id").Find(&journal).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("id").Find(&entries).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("id").Find(&reservations).Error)
	data, err := json.Marshal([]any{hold, account, journal, entries, reservations})
	must(t, err)
	return string(data)
}

func TestMySQLResponseInboxDispatchedEvidenceSurvivesLostApplyPermission(t *testing.T) {
	for _, fault := range []string{"version_changed", "lease_expired", "caller_cancelled", "runtime_changed", "soft_deleted"} {
		t.Run(fault, func(t *testing.T) {
			f := mysqlFixture(t)
			f.s.ResponseInboxEnabled = true
			v := f.create(t, "inbox-after-http")
			op := runtimeWorkerCreateOperation(t, f, v.ID)
			before := responseInboxFunds(t, f, v.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.response = func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					return nil, errors.New("unexpected provider operation")
				}
				current := runtimeWorkerCreateOperation(t, f, v.ID)
				if responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, current.AttemptID) != 1 {
					t.Fatal("HTTP began before durable execution registration")
				}
				switch fault {
				case "version_changed":
					must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", current.OrgID, current.ID).Updates(map[string]any{"version": current.Version + 1, "status": "unknown"}).Error)
				case "lease_expired":
					f.now = current.LeaseUntil.UTC()
				case "caller_cancelled":
					cancel()
				case "runtime_changed":
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", "future-runtime").Error)
				case "soft_deleted":
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("deleted_at", f.now).Error)
				}
				return reply(200, fmt.Sprintf(`{"id":%q}`, "up_"+v.ID)), nil
			}
			_ = f.s.processOperation(ctx, op) // Lost permission may return a safe conflict/context error.
			var receipt ResponseInbox
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&receipt).Error)
			var payload invocation.VideoPayload
			must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, receipt.PayloadRef).First(&payload).Error)
			var row task.Task
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
			if receipt.Outcome != string(ark.OutcomeAccepted) || receipt.UpstreamTaskID != "up_"+v.ID || receipt.RuntimeVersion != videoruntime.RuntimeV1 || receipt.PayloadRef == "" || payload.CaptureStatus != invocation.VideoCaptureStaged || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 1 {
				t.Fatal("sent response was lost, applied with stale permission, or changed funds")
			}
			// Caller cancellation does not revoke a still-live durable claim.
			// A detached save/apply context may complete it immediately; the
			// remaining faults invalidate application and must leave it pending.
			if fault != "caller_cancelled" && (row.UpstreamTaskID != "" || responseInboxApp(t, f, receipt).AppliedAt != nil) {
				t.Fatal("response was applied after its claim or task identity changed")
			}
			if fault == "runtime_changed" {
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", videoruntime.RuntimeV1).Error)
			}
			f.now = f.now.Add(4 * time.Minute)
			f.s.ResponseInboxEnabled = false // Disabling writes must not disable recovery.
			responseInboxMakeDue(t, f, receipt)
			must(t, f.s.recoverResponseInbox(context.Background()))
			must(t, f.s.recoverResponseInbox(context.Background()))
			current := runtimeWorkerCreateOperation(t, f, v.ID)
			if dispatchErr := f.s.processOperation(context.Background(), current); fault != "soft_deleted" {
				must(t, dispatchErr)
			}
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
			app := responseInboxApp(t, f, receipt)
			bindings := responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
			if fault == "soft_deleted" {
				if row.UpstreamTaskID != "" || bindings != 0 || app.State == "applied" {
					t.Fatal("late response resurrected a soft-deleted task")
				}
			} else if row.Status != "queued" || row.UpstreamTaskID != receipt.UpstreamTaskID || current.Status != "confirmed" || app.State != "applied" || app.AppliedAt == nil || bindings != 1 {
				t.Fatal("compatible recovery did not apply the saved creation once")
			}
			if f.posts.Load() != 1 || f.gets.Load() != 0 || responseInboxFunds(t, f, v.ID) != before {
				t.Fatal("recovery repeated provider HTTP or changed the active reservation")
			}
		})
	}
}

func TestMySQLResponseInboxConcurrentAppendIsImmutableAndIdempotent(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "inbox-duplicate")
	attempt, _ := responseInboxRegister(t, f, v.ID)
	before := responseInboxFunds(t, f, v.ID)
	const workers = 12
	prepared := make([]*preparedResponseReceipt, workers)
	for i := range prepared {
		var err error
		prepared[i], err = f.s.prepareResponseReceipt(attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID), "official-test-credential")
		must(t, err)
		f.now = f.now.Add(time.Millisecond) // Arrival time must not enter evidence identity.
	}
	var wg sync.WaitGroup
	receipts, errs := make(chan ResponseInbox, workers), make(chan error, workers)
	for _, p := range prepared {
		wg.Add(1)
		go func(p *preparedResponseReceipt) {
			defer wg.Done()
			r, err := f.s.appendResponseReceipt(context.Background(), p)
			receipts <- r
			errs <- err
		}(p)
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		must(t, err)
	}
	var first ResponseInbox
	for r := range receipts {
		if first.ReceiptID == "" {
			first = r
		} else if r.ReceiptID != first.ReceiptID || r.PayloadRef != first.PayloadRef || r.EvidenceHMAC != first.EvidenceHMAC {
			t.Fatal("duplicate append returned different committed evidence")
		}
	}
	if responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, attempt.AttemptID) != 1 || responseInboxCount(t, f, &ReceiptApplication{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, attempt.AttemptID) != 1 || responseInboxCount(t, f, &invocation.VideoPayload{}, "org_id = ? AND request_id = ?", f.key.OrgID, first.ReceiptID) != 1 || responseInboxCount(t, f, &invocation.VideoPayloadStaging{}, "payload_id = ?", first.PayloadRef) != 1 || responseInboxCount(t, f, &Attempt{}, "org_id = ? AND attempt_id = ? AND phase = ?", f.key.OrgID, attempt.AttemptID, "response_received") != 1 || responseInboxFunds(t, f, v.ID) != before {
		t.Fatal("duplicate append created partial/duplicate payloads, progress, audit or money changes")
	}
	stored, err := json.Marshal(first)
	must(t, err)
	again := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
	replayed, err := json.Marshal(again)
	must(t, err)
	if string(stored) != string(replayed) || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("retry overwrote immutable evidence or invoked the provider")
	}
}

func TestMySQLResponseInboxConflictingAcceptedEvidenceRequiresReview(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "inbox-conflicting-ids")
	attempt, op := responseInboxRegister(t, f, v.ID)
	before := responseInboxFunds(t, f, v.ID)
	first := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_first_"+v.ID))
	must(t, f.s.applyClaimedCreateInbox(context.Background(), op, first.ReceiptID))
	second := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_other_"+v.ID))
	if second.ReceiptID == first.ReceiptID || second.PayloadRef == first.PayloadRef {
		t.Fatal("different evidence shared its invocation correlation or overwrote the receipt")
	}
	f.now = f.now.Add(4 * time.Minute)
	must(t, f.s.recoverResponseInbox(context.Background()))
	must(t, f.s.recoverResponseInbox(context.Background()))
	var row task.Task
	var state State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&state).Error)
	if row.UpstreamTaskID != first.UpstreamTaskID || state.SyncState != "needs_review" || responseInboxApp(t, f, second).State != "review" || responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, attempt.AttemptID) != 2 || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 1 || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 0 {
		t.Fatal("confirmed operation hid contradictory evidence or replaced its binding")
	}
}

func TestMySQLResponseInboxUnknownThenAcceptedUsesTypedFactsWithoutHistory(t *testing.T) {
	f := mysqlFixture(t)
	must(t, f.s.DB.Model(&Policy{}).Where("org_id = ?", f.key.OrgID).Update("capture_history", false).Error)
	v := f.create(t, "inbox-history-disabled")
	attempt, op := responseInboxRegister(t, f, v.ID)
	before := responseInboxFunds(t, f, v.ID)
	unknown := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeUnknown, ""))
	must(t, f.s.applyClaimedCreateInbox(context.Background(), op, unknown.ReceiptID))
	accepted := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
	reads := responseInboxCount(t, f, &invocation.VideoPayloadAccess{}, "org_id = ?", f.key.OrgID)
	f.now = f.now.Add(4 * time.Minute)
	must(t, f.s.recoverResponseInbox(context.Background()))
	must(t, f.s.recoverResponseInbox(context.Background()))
	for _, receipt := range []ResponseInbox{unknown, accepted} {
		var meta invocation.VideoPayload
		must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, receipt.PayloadRef).First(&meta).Error)
		if meta.CaptureStatus != invocation.VideoCaptureDisabled || responseInboxCount(t, f, &invocation.VideoPayloadStaging{}, "payload_id = ?", receipt.PayloadRef) != 0 {
			t.Fatal("recovery bypassed the disabled body-retention policy")
		}
	}
	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
	if row.Status != "queued" || row.UpstreamTaskID != accepted.UpstreamTaskID || responseInboxApp(t, f, accepted).State != "applied" || responseInboxFunds(t, f, v.ID) != before || responseInboxCount(t, f, &invocation.VideoPayloadAccess{}, "org_id = ?", f.key.OrgID) != reads || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("late accepted facts did not recover without decrypting history or resending")
	}
}

func TestMySQLResponseInboxRejectsForgedAttemptIdentity(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "inbox-forged-identity")
	attempt, _ := responseInboxRegister(t, f, v.ID)
	for _, field := range []string{"org", "task", "operation", "attempt", "scope", "credential", "input", "runtime"} {
		t.Run(field, func(t *testing.T) {
			forged := attempt
			switch field {
			case "org":
				forged.OrgID++
			case "task":
				forged.TaskID = "task_forged"
			case "operation":
				forged.OperationID = "op_forged"
			case "attempt":
				forged.AttemptID = "att_unregistered"
			case "scope":
				forged.ProviderScopeID++
			case "credential":
				forged.CredentialSnapshotID++
			case "input":
				forged.InputIdentityHMAC = strings.Repeat("0", 64)
			case "runtime":
				forged.RuntimeVersion = "future-producer"
			}
			p, err := f.s.prepareResponseReceipt(forged, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID), "official-test-credential")
			if err == nil {
				_, err = f.s.appendResponseReceipt(context.Background(), p)
			}
			if err == nil || responseInboxCount(t, f, &ResponseInbox{}, "attempt_id IN ?", []string{attempt.AttemptID, forged.AttemptID}) != 0 || responseInboxCount(t, f, &ReceiptApplication{}, "attempt_id IN ?", []string{attempt.AttemptID, forged.AttemptID}) != 0 || f.posts.Load() != 0 {
				t.Fatal("unregistered or forged producer identity injected a response")
			}
		})
	}
}
