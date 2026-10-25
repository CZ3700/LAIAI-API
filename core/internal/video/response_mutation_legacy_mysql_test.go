package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func mutationLegacySnapshot(t *testing.T, m *mutationConsumerFixture, operationID string) string {
	t.Helper()
	row, st := m.rows(t)
	op := m.operation(t, operationID)
	var attempts []ExecutionAttempt
	var receipts []ResponseInbox
	var progress []ReceiptApplication
	var history []invocation.VideoPayload
	must(t, m.s.DB.Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Order("attempt_id").Find(&attempts).Error)
	must(t, m.s.DB.Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Order("receipt_id").Find(&receipts).Error)
	must(t, m.s.DB.Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Order("receipt_id").Find(&progress).Error)
	must(t, m.s.DB.Where("org_id = ?", m.key.OrgID).Order("payload_id").Find(&history).Error)
	data, err := json.Marshal([]any{row, st, op, op.Version, op.AttemptID, op.PayloadRef, op.ReceiptRef, op.ReceivedOutcome, op.ReceivedTaskID, op.LeaseUntil, op.ReceiptAppliedAt, attempts, receipts, progress, history, m.funds(t), m.effects(t)})
	must(t, err)
	return string(data)
}

func TestMySQLResponseMutationUnknownRegistrationBlocksEveryLegacyEntrypoint(t *testing.T) {
	for _, action := range []string{"cancel_task", "delete_task"} {
		t.Run(action, func(t *testing.T) {
			final := ""
			if action == "delete_task" {
				final = "succeeded"
			}
			m := newMutationConsumerFixture(t, final)
			a, claim := m.register(t, action)
			r := m.append(t, a, mutationResponse(ark.OutcomeAccepted, 200))
			must(t, m.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", m.key.OrgID, a.AttemptID).Update("storage_protocol", "future-mutation-protocol").Error)
			row, _ := m.rows(t)
			before := mutationLegacySnapshot(t, m, claim.ID)
			if send, err := m.s.operationMaySend(context.Background(), claim); err == nil && send {
				t.Fatal("legacy final send guard accepted a future registered mutation")
			}
			if _, err := m.s.persistResponse(context.Background(), row, claim, mutationResponse(ark.OutcomeAccepted, 200), "official-test-credential"); err == nil {
				t.Fatal("legacy persistence overwrote registered evidence")
			}
			if err := m.s.applyMutationReceipt(context.Background(), claim); err == nil {
				t.Fatal("legacy application accepted registered evidence")
			}
			if mutationLegacySnapshot(t, m, claim.ID) != before {
				t.Fatal("legacy guard changed task, operation, receipt or financial effects")
			}
			// A stale old process may still hold a pending candidate. Reset only
			// this fixture's projection, retaining the real immutable attempt.
			must(t, m.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", m.key.OrgID, claim.ID).Update("status", "pending").Error)
			pending := m.operation(t, claim.ID)
			before = mutationLegacySnapshot(t, m, claim.ID)
			var candidates []Operation
			must(t, m.s.pendingOperationCandidates(context.Background()).Where("o.org_id = ? AND o.id = ?", m.key.OrgID, claim.ID).Scan(&candidates).Error)
			if len(candidates) != 0 {
				t.Fatal("registered mutation entered legacy pending candidates")
			}
			_ = m.s.processMutation(context.Background(), pending)
			if mutationLegacySnapshot(t, m, claim.ID) != before {
				t.Fatal("legacy claim changed an already registered mutation")
			}
			must(t, m.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", m.key.OrgID, claim.ID).Updates(map[string]any{"status": "in_flight", "lease_until": m.now.Add(-time.Second), "receipt_ref": r.PayloadRef, "received_outcome": string(ark.OutcomeAccepted), "http_status": 200}).Error)
			before = mutationLegacySnapshot(t, m, claim.ID)
			candidates = nil
			must(t, m.s.recoveryOperationCandidates(context.Background()).Where("o.org_id = ? AND o.id = ?", m.key.OrgID, claim.ID).Scan(&candidates).Error)
			if len(candidates) != 0 {
				t.Fatal("registered mutation entered legacy recovery candidates")
			}
			must(t, m.s.recoverOperations(context.Background()))
			if mutationLegacySnapshot(t, m, claim.ID) != before {
				t.Fatal("legacy recovery applied or changed a future registered mutation")
			}
		})
	}
}

func TestMySQLResponseMutationAnyCreateRegistrationBlocksLegacyLocalRefund(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "mutation-legacy-local-refund")
	m := &mutationConsumerFixture{fixture: f, taskID: v.ID}
	f.s.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("legacy guard attempted HTTP")
		return nil, errors.New("no HTTP")
	})}
	a, claim := responseInboxRegister(t, f, v.ID)
	must(t, f.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, a.AttemptID).Update("storage_protocol", "future-create-protocol").Error)
	row, st := m.rows(t)
	before := mutationLegacySnapshot(t, m, claim.ID)
	if err := f.s.knownUnsent(context.Background(), row, st, claim, "execution_unavailable"); err == nil {
		t.Fatal("legacy not-sent path released a registered attempt")
	}
	if mutationLegacySnapshot(t, m, claim.ID) != before {
		t.Fatal("legacy not-sent guard changed financial or task state")
	}
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, claim.ID).Update("status", "pending").Error)
	must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("status", "accepted").Error)
	before = mutationLegacySnapshot(t, m, claim.ID)
	if _, _, err := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "legacy-local", "legacy-local-idem"); err == nil {
		t.Fatal("local cancel refunded a possibly sent registered creation")
	}
	if mutationLegacySnapshot(t, m, claim.ID) != before || responseInboxCount(t, f, &Operation{}, "org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "cancel_task") != 0 {
		t.Fatal("failed local cancel left partial operations or released funds")
	}
}

func TestMySQLResponseMutationRegisteredCreateValidationFailureUsesInbox(t *testing.T) {
	for _, failure := range []string{"assets", "quota"} {
		t.Run(failure, func(t *testing.T) {
			f := mysqlFixture(t)
			f.s.ResponseInboxEnabled = true
			v := f.create(t, "mutation-create-validation-inbox")
			f.response = func(*http.Request) (*http.Response, error) {
				t.Error("known-unsent validation attempted HTTP")
				return nil, errors.New("no HTTP")
			}
			op := runtimeWorkerCreateOperation(t, f, v.ID)
			if failure == "assets" {
				injected := false
				name := fmt.Sprintf("mutation-create-asset-failure-%d", f.key.OrgID)
				must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "video_task_asset_refs" && !injected {
						injected = true
						tx.AddError(errors.New("controlled asset validation failure"))
					}
				}))
				t.Cleanup(func() { _ = f.s.DB.Callback().Query().Remove(name) })
			} else {
				owned := f.s.DB.Model(&videobilling.QuotaReservation{}).Select("bucket_id").Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
				must(t, f.s.DB.Model(&videobilling.QuotaBucket{}).Where("id IN (?)", owned).Update("blocked", true).Error)
			}
			must(t, f.s.processOperation(context.Background(), op))
			var receipts []ResponseInbox
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Find(&receipts).Error)
			if len(receipts) != 1 || receipts[0].Outcome != string(ark.OutcomeNotSent) || receipts[0].Action != "create_task" || responseInboxApp(t, f, receipts[0]).State != "applied" || f.hold(t, v.ID).Status != ledger.HoldReleased || f.posts.Load() != 0 || f.gets.Load() != 0 || responseInboxCount(t, f, &videobilling.BillingEntry{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 1 {
				t.Fatal("registered validation failure bypassed typed evidence or did not release exactly once")
			}
			funds := responseInboxFunds(t, f, v.ID)
			must(t, f.s.applyCreateInbox(context.Background(), f.key.OrgID, receipts[0].ReceiptID, nil))
			if err := f.s.processOperation(context.Background(), runtimeWorkerCreateOperation(t, f, v.ID)); err != nil && !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			if responseInboxFunds(t, f, v.ID) != funds || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("typed not-sent replay repeated release or HTTP")
			}
		})
	}
}
