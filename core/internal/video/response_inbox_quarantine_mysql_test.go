package video

import (
	"context"
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
)

func responseInboxAcceptWithoutHTTP(t *testing.T, f *fixture, key string) (TaskView, ExecutionAttempt, ResponseInbox) {
	t.Helper()
	v := f.create(t, key)
	attempt, claim := responseInboxRegister(t, f, v.ID)
	accepted := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
	must(t, f.s.applyClaimedCreateInbox(context.Background(), claim, accepted.ReceiptID))
	return v, attempt, accepted
}

// R3: a query already on the wire must still retain its response, but a new
// create-identity quarantine wins before the query can settle or release funds.
func TestMySQLResponseInboxReviewBlocksInFlightQuerySettlement(t *testing.T) {
	for _, status := range []string{"succeeded", "failed"} {
		t.Run(status, func(t *testing.T) {
			f := mysqlFixture(t)
			v, attempt, accepted := responseInboxAcceptWithoutHTTP(t, f, "inbox-query-review-race")
			before := responseInboxFunds(t, f, v.ID)
			var conflict ResponseInbox
			f.response = func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					return nil, errors.New("unexpected mutation in query quarantine test")
				}
				conflict = responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "other_"+v.ID))
				if err := f.s.applyCreateInbox(context.Background(), f.key.OrgID, conflict.ReceiptID, nil); !errors.Is(err, ErrConflict) {
					t.Fatal("in-flight GET did not overlap a real create-identity quarantine", err)
				}
				if status == "succeeded" {
					return reply(200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100,"total_tokens":100}}`, accepted.UpstreamTaskID)), nil
				}
				return reply(200, fmt.Sprintf(`{"id":%q,"status":"failed"}`, accepted.UpstreamTaskID)), nil
			}
			if err := f.s.pollTask(context.Background(), f.key.OrgID, v.ID); !errors.Is(err, errCreateInboxReview) {
				t.Fatal("query response bypassed the newly established review barrier", err)
			}
			var receipt QueryReceipt
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&receipt).Error)
			var payload invocation.VideoPayload
			must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, receipt.PayloadRef).Take(&payload).Error)
			var row task.Task
			var state State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&row).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&state).Error)
			if receipt.PayloadRef == "" || receipt.AppliedAt != nil || receipt.ApplyErrorCode != "create_identity_review" || payload.CaptureStatus != invocation.VideoCaptureStaged || responseInboxCount(t, f, &invocation.VideoPayloadStaging{}, "payload_id = ?", receipt.PayloadRef) != 1 || row.Status != "queued" || state.SyncState != "needs_review" || state.ObservedCompletionTokens != nil || state.AppliedQuerySequence != 0 || responseInboxFunds(t, f, v.ID) != before {
				t.Fatal("quarantined query lost its evidence or advanced task, usage or money")
			}
			must(t, f.s.recoverQueryReceipts(context.Background()))
			must(t, f.s.recoverQueryReceipts(context.Background()))
			f.now = f.now.Add(4 * time.Minute) // Past both next poll time and old query lease.
			if err := f.s.pollTask(context.Background(), f.key.OrgID, v.ID); !errors.Is(err, errCreateInboxReview) {
				t.Fatal("new query claim did not honor the existing review barrier", err)
			}
			if responseInboxFunds(t, f, v.ID) != before || responseInboxApp(t, f, conflict).State != "review" || f.gets.Load() != 1 || f.posts.Load() != 0 {
				t.Fatal("quarantined recovery or polling repeated money or provider traffic")
			}

			// Test-only database state models a trusted, explicit adjudication
			// selecting the already-bound upstream task. It does not implement or
			// claim that production has an administrator review-release endpoint.
			must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ? AND state = ?", f.key.OrgID, conflict.ReceiptID, "review").Updates(map[string]any{"state": "applied", "error_code": "test_adjudicated", "applied_at": f.now}).Error)
			must(t, f.s.applyQueryReceipt(context.Background(), receipt))
			must(t, f.s.applyQueryReceipt(context.Background(), receipt))
			must(t, f.s.DB.Where("org_id = ? AND attempt_id = ?", f.key.OrgID, receipt.AttemptID).Take(&receipt).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&row).Error)
			hold := f.hold(t, v.ID)
			if receipt.AppliedAt == nil || receipt.ApplyErrorCode != "" || row.Status != status || responseInboxCount(t, f, &videobilling.BillingEntry{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != 1 {
				t.Fatal("saved response could not apply exactly once after explicit test adjudication")
			}
			if status == "succeeded" {
				if hold.Status != ledger.HoldCaptured || hold.CapturedAmount.String() != "0.0008" {
					t.Fatal("adjudicated successful response did not settle its original price once")
				}
			} else if hold.Status != ledger.HoldReleased || !hold.CapturedAmount.IsZero() {
				t.Fatal("adjudicated no-charge failure did not release its original reservation")
			}
			settled := responseInboxFunds(t, f, v.ID)
			must(t, f.s.recoverQueryReceipts(context.Background()))
			if responseInboxFunds(t, f, v.ID) != settled || f.gets.Load() != 1 || f.posts.Load() != 0 {
				t.Fatal("adjudicated replay repeated funds or fetched the response again")
			}
		})
	}
}

func TestMySQLResponseInboxReviewFiltersBothQueryQueuesBeforeLimit(t *testing.T) {
	f := mysqlFixture(t)
	var quarantined []string
	completion := int64(100)
	for i := range 41 { // Both production query batches are bounded at 40.
		v, attempt, _ := responseInboxAcceptWithoutHTTP(t, f, fmt.Sprintf("inbox-quarantined-query-%d", i))
		conflict := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "other_"+v.ID))
		if err := f.s.applyCreateInbox(context.Background(), f.key.OrgID, conflict.ReceiptID, nil); !errors.Is(err, ErrConflict) {
			t.Fatal("candidate fixture did not enter real review", err)
		}
		receipt := f.receipt(t, v.ID, 1, &completion)
		must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, receipt.AttemptID).Update("received_at", f.now.Add(-5*time.Hour)).Error)
		must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("next_poll_at", f.now.Add(-5*time.Hour)).Error)
		quarantined = append(quarantined, v.ID)
	}
	healthy, _, _ := responseInboxAcceptWithoutHTTP(t, f, "inbox-healthy-after-quarantined-queries")
	healthyReceipt := f.receipt(t, healthy.ID, 1, &completion)
	must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, healthyReceipt.AttemptID).Update("received_at", f.now.Add(-4*time.Hour)).Error)
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, healthy.ID).Update("next_poll_at", f.now.Add(-4*time.Hour)).Error)
	var pending []State
	must(t, f.s.pendingQueryCandidates(context.Background()).Where("v.org_id = ?", f.key.OrgID).Order("v.next_poll_at, v.task_id").Limit(40).Scan(&pending).Error)
	if len(pending) != 1 || pending[0].TaskID != healthy.ID {
		t.Fatal("quarantined tasks consumed the bounded new-query window")
	}
	var recovery []QueryReceipt
	must(t, f.s.recoveryQueryCandidates(context.Background()).Where("r.org_id = ?", f.key.OrgID).Order("r.received_at, r.attempt_id").Limit(40).Scan(&recovery).Error)
	if len(recovery) != 1 || recovery[0].AttemptID != healthyReceipt.AttemptID {
		t.Fatal("quarantined responses consumed the bounded recovery window")
	}
	must(t, f.s.recoverQueryReceipts(context.Background()))
	must(t, f.s.DB.Where("org_id = ? AND attempt_id = ?", f.key.OrgID, healthyReceipt.AttemptID).Take(&healthyReceipt).Error)
	if healthyReceipt.AppliedAt == nil || f.hold(t, healthy.ID).CapturedAmount.String() != "0.0008" || responseInboxCount(t, f, &videobilling.BillingEntry{}, "org_id = ? AND task_id = ?", f.key.OrgID, healthy.ID) != 1 {
		t.Fatal("healthy saved response was starved by create-identity quarantine")
	}
	for _, id := range quarantined {
		if f.hold(t, id).Status != ledger.HoldActive || responseInboxCount(t, f, &QueryReceipt{}, "org_id = ? AND task_id = ? AND applied_at IS NOT NULL", f.key.OrgID, id) != 0 || responseInboxCount(t, f, &videobilling.BillingEntry{}, "org_id = ? AND task_id = ?", f.key.OrgID, id) != 0 {
			t.Fatal("query recovery consumed a quarantined task's reservation")
		}
	}
	if f.gets.Load() != 0 || f.posts.Load() != 0 {
		t.Fatal("saved-response recovery performed provider HTTP")
	}
}

func TestMySQLResponseInboxNonReviewEvidenceDoesNotBlockOrdinaryPolling(t *testing.T) {
	f := mysqlFixture(t)
	v, attempt, accepted := responseInboxAcceptWithoutHTTP(t, f, "inbox-nonreview-query-compatible")
	weak := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeUnknown, ""))
	// A temporarily unreadable, weaker response is retryable progress, not a
	// contradictory create identity. Restore its schema while retaining the
	// real blocked application marker established by the reducer.
	must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, weak.PayloadRef).Update("schema_version", 2).Error)
	if err := f.s.applyCreateInbox(context.Background(), f.key.OrgID, weak.ReceiptID, nil); !errors.Is(err, invocation.ErrVideoSchemaUnsupported) {
		t.Fatal("fixture failed to create retryable, non-review progress", err)
	}
	must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, weak.PayloadRef).Update("schema_version", 1).Error)
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("sync_state", "needs_review").Error)
	if responseInboxApp(t, f, weak).State != "blocked" {
		t.Fatal("fixture did not retain a non-review application marker")
	}
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, errors.New("unexpected HTTP mutation in compatible polling")
		}
		return reply(200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100}}`, accepted.UpstreamTaskID)), nil
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	var row task.Task
	var state State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&row).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&state).Error)
	if row.Status != "succeeded" || state.SyncState != "fresh" || f.hold(t, v.ID).CapturedAmount.String() != "0.0008" || responseInboxApp(t, f, weak).State != "blocked" || f.gets.Load() != 1 || f.posts.Load() != 0 {
		t.Fatal("ordinary synchronization state or retryable progress was mistaken for identity quarantine")
	}
}

func TestMySQLResponseInboxSiblingReviewPreventsEquivalentRejectionRelease(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "inbox-sibling-review-no-release")
	attempt, claim := responseInboxRegister(t, f, v.ID)
	first := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeRejected, ""))
	before := responseInboxFunds(t, f, v.ID)
	events := responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
	var input invocation.VideoPayload
	must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, attempt.InputPayloadRef).Take(&input).Error)
	// Correlation and schema remain valid, but authenticated execution metadata
	// no longer matches the identity fixed before the request was dispatched.
	must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, input.PayloadID).Update("plaintext_bytes", input.PlaintextBytes+1).Error)
	if err := f.s.applyClaimedCreateInbox(context.Background(), claim, first.ReceiptID); !errors.Is(err, ErrConflict) {
		t.Fatal("changed execution identity did not reject receipt A", err)
	}
	firstApp := responseInboxApp(t, f, first)
	if firstApp.State != "review" || firstApp.ErrorCode != "execution_identity_conflict" {
		t.Fatal("receipt A did not enter review through its authenticated metadata mismatch")
	}
	must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, input.PayloadID).Update("plaintext_bytes", input.PlaintextBytes).Error)

	// B has the same definite outcome but distinct response evidence. It must
	// inherit the existing task quarantine rather than refund through a fresh
	// pending application after A's metadata happens to become readable again.
	result := responseInboxResult(ark.OutcomeRejected, "")
	result.Body = []byte(`{"error":{"code":"InvalidParameter","detail":"second safe rejection evidence"}}`)
	second := responseInboxAppend(t, f, attempt, result)
	if second.ReceiptID == first.ReceiptID || second.EvidenceHMAC == first.EvidenceHMAC || responseInboxApp(t, f, second).State != "pending" {
		t.Fatal("equivalent rejection did not create its own pending evidence record")
	}
	if err := f.s.applyClaimedCreateInbox(context.Background(), claim, second.ReceiptID); !errors.Is(err, errCreateInboxReview) {
		t.Fatal("receipt B bypassed its sibling's unresolved review", err)
	}
	secondApp := responseInboxApp(t, f, second)
	if secondApp.State != "review" || secondApp.ErrorCode != "create_identity_review" || secondApp.AppliedAt != nil {
		t.Fatal("fresh equivalent evidence did not retain the task quarantine")
	}
	f.now = f.now.Add(4 * time.Minute)
	must(t, f.s.recoverResponseInbox(context.Background()))
	must(t, f.s.recoverResponseInbox(context.Background()))
	if responseInboxFunds(t, f, v.ID) != before || f.hold(t, v.ID).Status != ledger.HoldActive || responseInboxCount(t, f, &Event{}, "org_id = ? AND task_id = ?", f.key.OrgID, v.ID) != events || responseInboxApp(t, f, first).State != "review" || responseInboxApp(t, f, second).State != "review" || responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, attempt.AttemptID) != 2 || f.gets.Load() != 0 || f.posts.Load() != 0 {
		t.Fatal("equivalent rejected evidence released quarantined funds or replayed side effects")
	}
}
