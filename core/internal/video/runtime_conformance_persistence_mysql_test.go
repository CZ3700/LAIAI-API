package video

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videobilling"
)

// The 9 wire samples each exercise both existing create persistence paths.
// Unknown 2xx results remain unknown even when their body contains an error;
// HTTP status alone is not evidence that the provider did no work.
func TestMySQLRuntimeHTTPCreatePersistence(t *testing.T) {
	samples := []struct {
		name    string
		status  int
		body    string
		outcome ark.Outcome
		history bool
	}{
		{"accepted_200", 200, `{"id":%q}`, ark.OutcomeAccepted, true},
		{"accepted_201", 201, `{"id":%q}`, ark.OutcomeAccepted, true},
		{"accepted_202", 202, `{"id":%q}`, ark.OutcomeAccepted, false},
		{"missing_id_200", 200, `{}`, ark.OutcomeUnknown, true},
		{"null_id_201", 201, `{"id":null}`, ark.OutcomeUnknown, true},
		{"empty_id_202", 202, `{"id":""}`, ark.OutcomeUnknown, true},
		{"conflicting_error_202", 202, `{"id":%q,"error":{"code":"InvalidParameter"}}`, ark.OutcomeUnknown, true},
		{"only_error_200", 200, `{"error":{"code":"InvalidParameter"}}`, ark.OutcomeUnknown, false},
		{"definite_rejection_400", 400, `{"error":{"code":"InvalidParameter"}}`, ark.OutcomeRejected, true},
	}
	for _, sample := range samples {
		for _, inbox := range []bool{false, true} {
			path := "legacy"
			if inbox {
				path = "inbox"
			}
			t.Run(sample.name+"/"+path, func(t *testing.T) {
				h := runtimeHTTPNew(t, inbox, sample.history)
				body := sample.body
				if strings.Contains(body, "%q") {
					body = fmt.Sprintf(body, h.upstreamID)
				}
				pinnedPublication := (!inbox && sample.name == "accepted_200") || (inbox && sample.name == "accepted_201")
				if pinnedPublication {
					h.reprice(t)
				}
				h.wire(t, http.MethodPost, sample.status, body)
				before := h.funds(t)
				events, outbox := h.count(t, &Event{}), h.count(t, &Outbox{})
				h.failApply(t, func() error { return h.f.s.processOperation(context.Background(), h.operation(t)) })
				op := h.operation(t)
				row, _ := h.rows(t)
				// The legitimate claim committed one task.submitting event before
				// HTTP. The failed response reducer must add no application event.
				if op.ReceiptAppliedAt != nil || row.UpstreamTaskID != "" || row.Status != "submitting" || h.funds(t) != before || h.count(t, &Event{}) != events+1 || h.count(t, &Outbox{}) != outbox+1 || h.count(t, &UpstreamBinding{}) != 0 || h.f.posts.Load() != 1 || h.f.gets.Load() != 0 {
					t.Fatal("create application rollback lost atomicity or repeated HTTP")
				}
				if responseInboxCount(t, h.f, &Event{}, "org_id = ? AND task_id = ? AND type = ?", h.f.key.OrgID, h.id, "task.submitting") != 1 || responseInboxCount(t, h.f, &Event{}, "org_id = ? AND task_id = ? AND type IN ?", h.f.key.OrgID, h.id, []string{"task.status_changed", "task.submission_unknown"}) != 0 {
					t.Fatal("application failure did not preserve exactly the legitimate claim event")
				}
				var receipt ResponseInbox
				ref := op.ReceiptRef
				if inbox {
					must(t, h.f.s.DB.Where("org_id = ? AND task_id = ?", h.f.key.OrgID, h.id).Take(&receipt).Error)
					var attempt ExecutionAttempt
					must(t, h.f.s.DB.Where("org_id = ? AND attempt_id = ?", h.f.key.OrgID, receipt.AttemptID).Take(&attempt).Error)
					app := responseInboxApp(t, h.f, receipt)
					if receipt.HTTPStatus != sample.status || receipt.Outcome != string(sample.outcome) || receipt.RuntimeVersion != h.state.RuntimeVersion || receipt.AdapterVersion != h.state.AdapterVersion || receipt.ProtocolContractVersion != h.state.ProtocolContractVersion || receipt.BillingSchemaVersion != h.state.BillingSchemaVersion || receipt.ObservationSchemaVersion != 1 || attempt.InputPayloadRef != h.state.EffectivePayloadRef || attempt.CredentialSnapshotID != h.state.CredentialSnapshotID || app.State != "pending" || app.AppliedAt != nil || h.count(t, &ResponseInbox{}) != 1 || h.count(t, &ExecutionAttempt{}) != 1 {
						t.Fatal("inbox did not retain the raw response and captured fixed identity")
					}
					ref = receipt.PayloadRef
				} else if op.HTTPStatus != sample.status || op.ReceivedOutcome != string(sample.outcome) || h.count(t, &ResponseInbox{}) != 0 || h.count(t, &ExecutionAttempt{}) != 0 {
					t.Fatal("legacy response did not persist its actual HTTP classification")
				}
				if ref == "" {
					t.Fatal("response evidence reference is missing")
				}
				h.evidence(t, ref, body)
				if op.LeaseUntil == nil {
					t.Fatal("dispatch never created a real claim")
				}
				expireFixtureOperationLease(t, h.f.s.DB, op)
				h.restart(t)
				reads := responseInboxCount(t, h.f, &invocation.VideoPayloadAccess{}, "org_id = ?", h.f.key.OrgID)
				apply := func() error {
					if inbox {
						return h.f.s.applyCreateInbox(context.Background(), h.f.key.OrgID, receipt.ReceiptID, nil)
					}
					return h.f.s.applyCreateReceipt(context.Background(), h.operation(t))
				}
				must(t, apply())
				after := h.funds(t)
				must(t, apply())
				must(t, h.f.s.processOperation(context.Background(), h.operation(t)))
				if h.funds(t) != after || h.count(t, &Event{}) != events+2 || h.count(t, &Outbox{}) != outbox+2 || h.f.posts.Load() != 1 || h.f.gets.Load() != 0 || responseInboxCount(t, h.f, &invocation.VideoPayloadAccess{}, "org_id = ?", h.f.key.OrgID) != reads {
					t.Fatal("create recovery/replay repeated HTTP, decryption, money or events")
				}
				op = h.operation(t)
				row, state := h.rows(t)
				if op.ReceiptAppliedAt == nil || (inbox && responseInboxApp(t, h.f, receipt).State != "applied") {
					t.Fatal("saved creation did not complete through its own recovery path")
				}
				switch sample.outcome {
				case ark.OutcomeAccepted:
					if row.Status != "queued" || row.UpstreamTaskID != h.upstreamID || op.Status != "confirmed" || h.count(t, &UpstreamBinding{}) != 1 || after != before {
						t.Fatal("accepted response did not bind exactly once while preserving its hold")
					}
				case ark.OutcomeUnknown:
					if row.Status != "submission_unknown" || row.UpstreamTaskID != "" || state.SyncState != "needs_review" || op.Status != "unknown" || h.count(t, &UpstreamBinding{}) != 0 || after != before || h.count(t, &videobilling.BillingEntry{}) != 0 || h.count(t, &videobilling.UsageEvidence{}) != 0 {
						t.Fatal("uncertain 2xx was bound, retried, settled or refunded")
					}
				case ark.OutcomeRejected:
					hold := h.f.hold(t, h.id)
					var account ledger.Account
					must(t, h.f.s.DB.First(&account, hold.AccountID).Error)
					if row.Status != "failed" || op.Status != "rejected" || state.BillingState != "released" || hold.Status != ledger.HoldReleased || !hold.CapturedAmount.IsZero() || !account.PostedBalance.Equal(h.account.PostedBalance) || !account.HeldAmount.Equal(h.account.HeldAmount.Sub(h.hold.Amount)) || h.count(t, &videobilling.BillingEntry{}) != 1 || h.count(t, &videobilling.UsageEvidence{}) != 0 || h.count(t, &UpstreamBinding{}) != 0 {
						t.Fatal("definite HTTP rejection did not release exactly the original hold once")
					}
					h.quotaFinal(t, 0, true)
				}
				h.pinned(t)
				if pinnedPublication {
					h.queryWithRecovery(t, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100,"total_tokens":999}}`, h.upstreamID))
					h.settle(t, 100)
				}
			})
		}
	}
}

func TestMySQLRuntimeHTTPQueryAcceptedPersistence(t *testing.T) {
	for _, status := range []int{201, 202} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := runtimeHTTPNew(t, status == 202, true)
			h.wire(t, http.MethodPost, 200, fmt.Sprintf(`{"id":%q}`, h.upstreamID))
			h.f.dispatch(t, h.id)
			h.reprice(t)
			r := h.queryWithRecovery(t, status, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100,"total_tokens":999}}`, h.upstreamID))
			_, state := h.rows(t)
			if r.Outcome != string(ark.OutcomeAccepted) || state.TotalTokens == nil || *state.TotalTokens != 999 {
				t.Fatal("accepted non-200 observation lost its total/completion distinction")
			}
			h.settle(t, 100)
		})
	}
}

func TestMySQLRuntimeHTTPNullableUsagePersistence(t *testing.T) {
	samples := []struct {
		name, usage string
		status      int
	}{
		{"missing", "", 200},
		{"null", `,"usage":null`, 201},
		{"empty", `,"usage":{}`, 202},
		{"null_completion", `,"usage":{"completion_tokens":null}`, 201},
		{"total_only", `,"usage":{"total_tokens":11}`, 202},
	}
	for i, sample := range samples {
		t.Run(sample.name, func(t *testing.T) {
			h := runtimeHTTPNew(t, i%2 == 1, sample.name != "total_only")
			h.wire(t, http.MethodPost, 200, fmt.Sprintf(`{"id":%q}`, h.upstreamID))
			h.f.dispatch(t, h.id)
			before := h.funds(t)
			r := h.queryWithRecovery(t, sample.status, fmt.Sprintf(`{"id":%q,"status":"succeeded"%s}`, h.upstreamID, sample.usage))
			row, state := h.rows(t)
			if r.Outcome != string(ark.OutcomeAccepted) || row.Status != "succeeded" || state.BillingState != "pending_usage" || state.ObservedCompletionTokens != nil || h.f.hold(t, h.id).Status != ledger.HoldActive || h.funds(t) != before || h.count(t, &videobilling.BillingEntry{}) != 0 || h.count(t, &videobilling.UsageEvidence{}) != 0 {
				t.Fatal("missing usage was promoted to a real zero or released")
			}
			if sample.name == "total_only" && (state.TotalTokens == nil || *state.TotalTokens != 11) {
				t.Fatal("known total was lost while completion remained absent")
			}
			makeFixtureTaskQueryDue(t, h.f, h.id)
			h.queryWithRecovery(t, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":0}}`, h.upstreamID))
			h.settle(t, 0)
			settled := h.funds(t)
			must(t, h.f.s.applyQueryReceipt(context.Background(), r))
			_, state = h.rows(t)
			if h.funds(t) != settled || state.ObservedCompletionTokens == nil || *state.ObservedCompletionTokens != 0 {
				t.Fatal("replay of an older missing-usage receipt erased confirmed zero")
			}
		})
	}
}

func TestMySQLRuntimeHTTPUninterpretableQueryPersistence(t *testing.T) {
	for i, sample := range []struct {
		name, observation string
		outcome           ark.Outcome
	}{
		{"unknown_status", `"status":"future_http_status"`, ark.OutcomeAccepted},
		{"video_array", `"status":"succeeded","usage":{"completion_tokens":100},"content":{"video_url":["https://example.invalid/video.mp4"]}`, ark.OutcomeUnknown},
		{"last_frame_array", `"status":"succeeded","usage":{"completion_tokens":100},"content":{"last_frame_url":["https://example.invalid/frame.png"]}`, ark.OutcomeUnknown},
	} {
		t.Run(sample.name, func(t *testing.T) {
			h := runtimeHTTPNew(t, i%2 == 1, true)
			h.wire(t, http.MethodPost, 200, fmt.Sprintf(`{"id":%q}`, h.upstreamID))
			h.f.dispatch(t, h.id)
			before := h.funds(t)
			r := h.queryWithRecovery(t, 200, fmt.Sprintf(`{"id":%q,%s}`, h.upstreamID, sample.observation))
			row, state := h.rows(t)
			if r.Outcome != string(sample.outcome) || row.Status != "queued" || state.SyncState != "needs_review" || state.ObservedCompletionTokens != nil || state.ResultPayloadRef != "" || r.HasVideo || r.HasLastFrame || h.f.hold(t, h.id).Status != ledger.HoldActive || h.funds(t) != before || h.count(t, &videobilling.BillingEntry{}) != 0 || h.count(t, &videobilling.UsageEvidence{}) != 0 || strings.Contains(r.ObservationJSON, "future_http_status") || strings.Contains(r.ObservationJSON, "https://") {
				t.Fatal("uninterpretable observation leaked raw data, delivered a guessed artifact or changed money")
			}
			if sample.name != "unknown_status" && r.ObservationJSON != "" {
				t.Fatal("unsupported artifact array acquired an executable observation")
			}
		})
	}
}
