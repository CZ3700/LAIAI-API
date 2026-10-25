package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// R2 coverage: exercise the real lifecycle erasure and a fresh service instance,
// preserving the immutable metadata identity captured before natural expiry.
func TestMySQLResponseInboxRestartRecoversAfterRealPayloadPurge(t *testing.T) {
	for _, outcome := range []ark.Outcome{ark.OutcomeAccepted, ark.OutcomeRejected} {
		t.Run(string(outcome), func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "inbox-restart-after-purge")
			attempt, _ := responseInboxRegister(t, f, v.ID)
			r := responseInboxAppend(t, f, attempt, responseInboxResult(outcome, "up_"+v.ID))
			before := responseInboxFunds(t, f, v.ID)
			reads := responseInboxCount(t, f, &invocation.VideoPayloadAccess{}, "org_id = ?", f.key.OrgID)
			f.now = f.now.Add(31 * 24 * time.Hour)

			// The lifecycle function is production code. Scope only its candidate
			// SELECTs to this fixture so a shared test DB's other organizations
			// are not purged by this deliberately advanced clock.
			name := fmt.Sprintf("test:inbox-purge-org-%d", f.key.OrgID)
			must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "invocation_payloads" {
					tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "org_id", Value: f.key.OrgID}}})
				}
			}))
			t.Cleanup(func() { _ = f.s.DB.Callback().Query().Remove(name) })
			purged, err := f.s.Evidence.PurgeExpired(context.Background(), 100)
			must(t, f.s.DB.Callback().Query().Remove(name))
			must(t, err)
			if purged < 2 {
				t.Fatal("natural expiry did not exercise both input and response erasure")
			}
			for _, id := range []string{attempt.InputPayloadRef, r.PayloadRef} {
				var meta invocation.VideoPayload
				must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, id).Take(&meta).Error)
				if meta.CaptureStatus != invocation.VideoCaptureExpired || meta.WrappedKey != "" || len(meta.Nonce) != 0 || responseInboxCount(t, f, &invocation.VideoPayloadStaging{}, "payload_id = ?", id) != 0 {
					t.Fatal("lifecycle purge left a decryptable execution or history payload")
				}
			}

			restarted, err := NewService(f.s.DB, f.s.Keyring, f.s.Secret, f.s.Log)
			must(t, err)
			restarted.Now = func() time.Time { return f.now }
			restarted.Client = f.s.Client // Mock only; count any unintended provider calls.
			restarted.Gate = f.s.Gate
			if restarted.ResponseInboxEnabled {
				t.Fatal("new process unexpectedly enabled the new-write gate")
			}
			f.s = restarted
			must(t, f.s.recoverResponseInbox(context.Background()))
			must(t, f.s.recoverResponseInbox(context.Background()))
			app := responseInboxApp(t, f, r)
			if app.State != "applied" || app.AppliedAt == nil || responseInboxCount(t, f, &invocation.VideoPayloadAccess{}, "org_id = ?", f.key.OrgID) != reads || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("fresh process required plaintext or another provider call to recover durable facts")
			}
			if outcome == ark.OutcomeAccepted {
				if responseInboxFunds(t, f, v.ID) != before || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 1 {
					t.Fatal("accepted recovery after purge altered reservation or duplicated its binding")
				}
			} else if f.hold(t, v.ID).Status != ledger.HoldReleased || responseInboxCount(t, f, &videobilling.BillingEntry{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 1 {
				t.Fatal("definite rejection after purge did not release exactly once")
			}
			settled := responseInboxFunds(t, f, v.ID)
			must(t, f.s.recoverResponseInbox(context.Background()))
			if responseInboxFunds(t, f, v.ID) != settled || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("repeated recovery after expiry repeated money or HTTP")
			}
		})
	}
}

func TestMySQLResponseInboxAppliedProgressSurvivesLaterCompatibilityFailures(t *testing.T) {
	for _, fault := range []string{"runtime", "history_metadata"} {
		t.Run(fault, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "inbox-applied-progress-preserved")
			attempt, claim := responseInboxRegister(t, f, v.ID)
			r := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
			must(t, f.s.applyClaimedCreateInbox(context.Background(), claim, r.ReceiptID))
			before, err := json.Marshal(responseInboxApp(t, f, r))
			must(t, err)
			funds := responseInboxFunds(t, f, v.ID)
			events := responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
			var restore func()
			if fault == "runtime" {
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", "temporarily-unavailable-runtime").Error)
				restore = func() {
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", videoruntime.RuntimeV1).Error)
				}
			} else {
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, r.PayloadRef).Update("schema_version", 2).Error)
				restore = func() {
					must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, r.PayloadRef).Update("schema_version", 1).Error)
				}
			}
			err = f.s.applyCreateInbox(context.Background(), f.key.OrgID, r.ReceiptID, nil)
			if err != nil && !errors.Is(err, videoruntime.ErrUnsupported) && !errors.Is(err, invocation.ErrVideoSchemaUnsupported) {
				t.Fatal("compatibility failure returned an unexpected error", err)
			}
			after, err := json.Marshal(responseInboxApp(t, f, r))
			must(t, err)
			if string(before) != string(after) || responseInboxFunds(t, f, v.ID) != funds || responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != events {
				t.Fatal("later compatibility failure downgraded completed progress or replayed side effects")
			}
			restore()
			must(t, f.s.applyCreateInbox(context.Background(), f.key.OrgID, r.ReceiptID, nil))
			if responseInboxFunds(t, f, v.ID) != funds || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("restoring compatibility replayed an already applied result")
			}
		})
	}
}

func TestMySQLResponseInboxReviewProgressSurvivesLaterCompatibilityFailures(t *testing.T) {
	for _, fault := range []string{"runtime", "history_metadata"} {
		t.Run(fault, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "inbox-review-progress-preserved")
			attempt, claim := responseInboxRegister(t, f, v.ID)
			first := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
			must(t, f.s.applyClaimedCreateInbox(context.Background(), claim, first.ReceiptID))
			conflicting := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "other_"+v.ID))
			if err := f.s.applyCreateInbox(context.Background(), f.key.OrgID, conflicting.ReceiptID, nil); !errors.Is(err, ErrConflict) {
				t.Fatal("current consumer did not quarantine contradictory accepted IDs", err)
			}
			app := responseInboxApp(t, f, conflicting)
			if app.State != "review" || app.ErrorCode != "response_evidence_conflict" {
				t.Fatal("fixture did not enter review through the actual conflict reducer")
			}
			before, err := json.Marshal(app)
			must(t, err)
			funds := responseInboxFunds(t, f, v.ID)
			events := responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
			var restore func()
			if fault == "runtime" {
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", "temporarily-unavailable-runtime").Error)
				restore = func() {
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", videoruntime.RuntimeV1).Error)
				}
			} else {
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, conflicting.PayloadRef).Update("schema_version", 2).Error)
				restore = func() {
					must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, conflicting.PayloadRef).Update("schema_version", 1).Error)
				}
			}
			err = f.s.applyCreateInbox(context.Background(), f.key.OrgID, conflicting.ReceiptID, nil)
			if !errors.Is(err, videoruntime.ErrUnsupported) && !errors.Is(err, invocation.ErrVideoSchemaUnsupported) {
				t.Fatal("reviewed receipt did not surface its temporary validation failure", err)
			}
			after, err := json.Marshal(responseInboxApp(t, f, conflicting))
			must(t, err)
			if string(before) != string(after) {
				t.Fatal("temporary validation failure removed the manual-review barrier")
			}
			restore()
			if err := f.s.applyCreateInbox(context.Background(), f.key.OrgID, conflicting.ReceiptID, nil); !errors.Is(err, ErrConflict) {
				t.Fatal("restored compatibility automatically accepted quarantined evidence", err)
			}
			must(t, f.s.recoverResponseInbox(context.Background()))
			var state State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&state).Error)
			after, err = json.Marshal(responseInboxApp(t, f, conflicting))
			must(t, err)
			if string(before) != string(after) || state.SyncState != "needs_review" || responseInboxFunds(t, f, v.ID) != funds || responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != events || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ? AND upstream_task_id = ?", f.key.OrgID, v.ID, first.UpstreamTaskID) != 1 || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("review recovery changed binding, state, events or funds without manual resolution")
			}
		})
	}
}
