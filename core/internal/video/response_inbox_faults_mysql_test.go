package video

import (
	"context"
	"errors"
	"fmt"
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

func TestMySQLResponseInboxUnknownProducerStillRetainsEvidence(t *testing.T) {
	for _, format := range []string{"runtime", "observation_schema"} {
		t.Run(format, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "inbox-unknown-producer")
			attempt, op := responseInboxRegister(t, f, v.ID)
			if format == "runtime" {
				attempt.RuntimeVersion = "future-runtime"
				must(t, f.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", attempt.OrgID, attempt.AttemptID).Update("runtime_version", attempt.RuntimeVersion).Error)
			} else {
				attempt.ObservationSchemaVersion = 2
				must(t, f.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", attempt.OrgID, attempt.AttemptID).Update("observation_schema_version", attempt.ObservationSchemaVersion).Error)
			}
			before := responseInboxFunds(t, f, v.ID)
			receipt := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
			err := f.s.applyClaimedCreateInbox(context.Background(), op, receipt.ReceiptID)
			if !errors.Is(err, videoruntime.ErrUnsupported) {
				t.Fatal("unknown producer application did not return the contract refusal", err)
			}
			var payload invocation.VideoPayload
			must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, receipt.PayloadRef).First(&payload).Error)
			f.now = f.now.Add(4 * time.Minute)
			must(t, f.s.recoverResponseInbox(context.Background()))
			if responseInboxApp(t, f, receipt).AppliedAt != nil || responseInboxFunds(t, f, v.ID) != before || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 0 || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("unsupported producer was applied, refunded or lost its evidence")
			}
		})
	}
}

func TestMySQLResponseInboxAppendFailureRollsBackEveryReference(t *testing.T) {
	for _, table := range []string{"video_response_inbox", "invocation_payloads", "invocation_payload_staging", "video_receipt_applications", "video_attempts"} {
		t.Run(table, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "inbox-append-rollback")
			attempt, _ := responseInboxRegister(t, f, v.ID)
			before := responseInboxFunds(t, f, v.ID)
			payloads := responseInboxCount(t, f, &invocation.VideoPayload{}, "org_id = ?", f.key.OrgID)
			p, err := f.s.prepareResponseReceipt(attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID), "official-test-credential")
			must(t, err)
			injected := errors.New("synthetic inbox append failure")
			name := fmt.Sprintf("test:inbox-append-%d", f.key.OrgID)
			fired := false
			must(t, f.s.responseDB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					fired = true
					tx.AddError(injected)
				}
			}))
			_, err = f.s.appendResponseReceipt(context.Background(), p)
			must(t, f.s.responseDB.Callback().Create().Remove(name))
			if !fired || err == nil {
				t.Fatal("append failure did not reach its intended transactional write")
			}
			if responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, attempt.AttemptID) != 0 || responseInboxCount(t, f, &ReceiptApplication{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, attempt.AttemptID) != 0 || responseInboxCount(t, f, &invocation.VideoPayload{}, "org_id = ?", f.key.OrgID) != payloads || responseInboxCount(t, f, &Attempt{}, "org_id = ? AND attempt_id = ? AND phase = ?", f.key.OrgID, attempt.AttemptID, "response_received") != 0 || responseInboxFunds(t, f, v.ID) != before {
				t.Fatal("failed append left a partial receipt, payload, progress or financial change")
			}
			// Retry the same prepared evidence after rollback, without HTTP.
			receipt, err := f.s.appendResponseReceipt(context.Background(), p)
			must(t, err)
			if responseInboxApp(t, f, receipt).State != "pending" || responseInboxCount(t, f, &invocation.VideoPayloadStaging{}, "payload_id = ?", receipt.PayloadRef) != 1 || f.posts.Load() != 0 {
				t.Fatal("failed append could not be retried as one complete record")
			}
		})
	}
}

func TestMySQLResponseInboxApplicationRollbackKeepsEvidenceAndReplaysOnce(t *testing.T) {
	for _, outcome := range []ark.Outcome{ark.OutcomeAccepted, ark.OutcomeRejected} {
		t.Run(string(outcome), func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "inbox-apply-rollback")
			attempt, op := responseInboxRegister(t, f, v.ID)
			receipt := responseInboxAppend(t, f, attempt, responseInboxResult(outcome, "up_"+v.ID))
			before := responseInboxFunds(t, f, v.ID)
			events := responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
			outbox := responseInboxCount(t, f, &Outbox{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
			injected := errors.New("synthetic application event failure")
			name := fmt.Sprintf("test:inbox-application-%d", f.key.OrgID)
			fired := false
			must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "video_events" {
					fired = true
					tx.AddError(injected)
				}
			}))
			err := f.s.applyClaimedCreateInbox(context.Background(), op, receipt.ReceiptID)
			must(t, f.s.DB.Callback().Create().Remove(name))
			if !fired || !errors.Is(err, injected) || responseInboxApp(t, f, receipt).AppliedAt != nil || responseInboxFunds(t, f, v.ID) != before || responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != events || responseInboxCount(t, f, &Outbox{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != outbox || responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND receipt_id = ?", f.key.OrgID, receipt.ReceiptID) != 1 {
				t.Fatal("failed application lost evidence or partially advanced funds/events")
			}
			f.now = f.now.Add(4 * time.Minute)
			must(t, f.s.recoverResponseInbox(context.Background()))
			must(t, f.s.recoverResponseInbox(context.Background()))
			app := responseInboxApp(t, f, receipt)
			if app.State != "applied" || app.AppliedAt == nil || responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != events+1 || responseInboxCount(t, f, &Outbox{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != outbox+1 {
				t.Fatal("application recovery did not commit one event and one outbox record")
			}
			hold := f.hold(t, v.ID)
			if outcome == ark.OutcomeAccepted {
				if responseInboxFunds(t, f, v.ID) != before || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 1 {
					t.Fatal("accepted creation recovery altered funds or duplicated its binding")
				}
			} else if hold.Status != ledger.HoldReleased || !hold.CapturedAmount.IsZero() || responseInboxCount(t, f, &videobilling.BillingEntry{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 1 {
				t.Fatal("confirmed rejection recovery did not release exactly once")
			}
			settled := responseInboxFunds(t, f, v.ID)
			must(t, f.s.recoverResponseInbox(context.Background()))
			if responseInboxFunds(t, f, v.ID) != settled || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("already applied receipt repeated money or provider operations")
			}
		})
	}
}

func TestMySQLResponseInboxExpiredWithoutResponseNeverUsesLegacyOrResends(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "inbox-no-response")
	attempt, _ := responseInboxRegister(t, f, v.ID)
	before := responseInboxFunds(t, f, v.ID)
	f.now = f.now.Add(4 * time.Minute)
	var legacy []Operation
	must(t, f.s.recoveryOperationCandidates(context.Background()).Where("o.org_id = ?", f.key.OrgID).Find(&legacy).Error)
	if len(legacy) != 0 {
		t.Fatal("registered attempt leaked into the legacy recovery queue")
	}
	f.s.ResponseInboxEnabled = false
	must(t, f.s.recoverOperations(context.Background()))
	must(t, f.s.recoverResponseInbox(context.Background()))
	must(t, f.s.recoverResponseInbox(context.Background()))
	current := runtimeWorkerCreateOperation(t, f, v.ID)
	must(t, f.s.processOperation(context.Background(), current))
	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
	if current.Status != "unknown" || row.Status != "submission_unknown" || responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, attempt.AttemptID) != 1 || responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 0 || responseInboxFunds(t, f, v.ID) != before || f.posts.Load() != 0 {
		t.Fatal("expired registered send was retried, refunded or erased")
	}
}
