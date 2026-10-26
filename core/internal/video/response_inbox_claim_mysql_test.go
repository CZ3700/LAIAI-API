package video

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
)

// Added after the first P0-2A1 MySQL snapshot. This regression belongs to the
// second validation pass: recording contradictory facts cannot grant a stale
// producer the current consumer's state-changing permission.
func TestMySQLResponseInboxStaleProducerCannotApplyConflictOrDowngradeAppliedReceipt(t *testing.T) {
	f := mysqlFixture(t)
	executionRecoveryScope(t, f, []uint64{f.key.OrgID})
	v := f.create(t, "inbox-stale-producer-conflict")
	attempt, claim := responseInboxRegister(t, f, v.ID)
	accepted := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
	must(t, f.s.applyClaimedCreateInbox(context.Background(), claim, accepted.ReceiptID))
	conflicting := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "other_"+v.ID))
	beforeFunds := responseInboxFunds(t, f, v.ID)
	var state State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&state).Error)
	beforeState, err := json.Marshal(state)
	must(t, err)
	beforeApp, err := json.Marshal(responseInboxApp(t, f, accepted))
	must(t, err)

	// The old exact producer can replay its own already-applied receipt as a
	// read-only success; it must not move that application back to pending.
	must(t, f.s.applyClaimedCreateInbox(context.Background(), claim, accepted.ReceiptID))
	afterApp, err := json.Marshal(responseInboxApp(t, f, accepted))
	must(t, err)
	if string(afterApp) != string(beforeApp) {
		t.Fatal("stale idempotent replay modified an already-applied receipt")
	}

	err = f.s.applyClaimedCreateInbox(context.Background(), claim, conflicting.ReceiptID)
	if !errors.Is(err, ErrConflict) {
		t.Fatal("confirmed operation granted its stale producer conflict-application permission", err)
	}
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&state).Error)
	afterState, err := json.Marshal(state)
	must(t, err)
	app := responseInboxApp(t, f, conflicting)
	if string(afterState) != string(beforeState) || responseInboxFunds(t, f, v.ID) != beforeFunds || app.State != "pending" || app.AppliedAt != nil || responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND attempt_id = ?", f.key.OrgID, attempt.AttemptID) != 2 {
		t.Fatal("stale producer altered state, funds, or conflict evidence")
	}

	// A current recovery consumer independently acquires application authority
	// and detects the contradiction without replacing the first binding.
	responseInboxMakeDue(t, f, conflicting)
	// Other suites retain their own retryable receipts in the shared MySQL
	// database. This test exercises recovery authority, not queue fairness:
	// prioritize only this fixture's receipt ahead of the actual eligible
	// backlog, without changing its production filters or 30-item batch.
	var candidates []ReceiptApplication
	must(t, f.s.responseInboxCandidates(context.Background()).Order("ap.next_attempt_at, ap.receipt_id").Limit(1).Scan(&candidates).Error)
	if len(candidates) != 1 {
		t.Fatal("recovery queue unexpectedly has no eligible receipt")
	}
	must(t, f.s.DB.Model(&ReceiptApplication{}).Where("org_id = ? AND receipt_id = ?", conflicting.OrgID, conflicting.ReceiptID).Update("next_attempt_at", candidates[0].NextAttemptAt.Add(-time.Millisecond)).Error)
	candidates = nil
	must(t, f.s.responseInboxCandidates(context.Background()).Order("ap.next_attempt_at, ap.receipt_id").Limit(30).Scan(&candidates).Error)
	if len(candidates) == 0 || candidates[0].OrgID != conflicting.OrgID || candidates[0].ReceiptID != conflicting.ReceiptID {
		t.Fatal("fixture's contradictory receipt did not enter the first recovery batch")
	}
	requireRecoveryCause(t, f.s.recoverResponseInbox(context.Background()), ErrConflict, 1)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&state).Error)
	if responseInboxApp(t, f, conflicting).State != "review" || state.SyncState != "needs_review" || responseInboxApp(t, f, accepted).State != "applied" || responseInboxCount(t, f, &UpstreamBinding{}, "org_id = ? AND task_id = ? AND upstream_task_id = ?", f.key.OrgID, v.ID, accepted.UpstreamTaskID) != 1 || responseInboxFunds(t, f, v.ID) != beforeFunds || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("current recovery did not quarantine contradictory accepted facts exactly once")
	}
}
