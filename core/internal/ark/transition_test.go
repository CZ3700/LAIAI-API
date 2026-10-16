package ark

import "testing"

func TestTransitionsPreserveUncertaintyAndTerminalEvidence(t *testing.T) {
	r := ApplyObservation(StatusSubmitting, Result{Action: ActionSubmit, Outcome: OutcomeUnknown})
	if r.Status != StatusSubmissionUnknown || !r.NeedsReview {
		t.Fatalf("lost submission uncertainty: %+v", r)
	}
	for _, current := range []Status{StatusQueued, StatusRunning, StatusSucceeded} {
		r := ApplyObservation(current, Result{Action: ActionQuery, Outcome: OutcomeRejected, HTTPStatus: 404})
		if r.Status != current || !r.NeedsReview {
			t.Fatal("404 manufactured failure")
		}
	}
	r = ApplyObservation(StatusSucceeded, Result{Action: ActionQuery, Outcome: OutcomeAccepted, Observation: &Observation{Status: StatusRunning}})
	if r.Status != StatusSucceeded || !r.NeedsReview {
		t.Fatal("terminal state regressed")
	}
	r = ApplyObservation(StatusRunning, Result{Action: ActionQuery, Outcome: OutcomeAccepted, Observation: &Observation{Status: StatusSucceeded}})
	if r.Status != StatusSucceeded || !r.UsagePending {
		t.Fatal("missing usage did not preserve generated outcome")
	}
	r = ApplyObservation(StatusQueued, Result{Action: ActionQuery, Outcome: OutcomeAccepted, Observation: &Observation{RawStatus: "new"}})
	if r.Status != StatusQueued || !r.NeedsReview {
		t.Fatal("unknown status guessed")
	}
}

func TestCancellationDoesNotTreatDelete200And404AsRefundProof(t *testing.T) {
	if d := CancellationDecision(StatusAccepted, false, nil); !d.CancelLocally || d.OperationStatus != "confirmed" {
		t.Fatal("local unsent cancellation unavailable")
	}
	if d := CancellationDecision(StatusAccepted, true, nil); d.CancelLocally || d.SendDelete {
		t.Fatal("cancel raced dispatch")
	}
	if d := CancellationDecision(StatusQueued, true, nil); !d.SendDelete {
		t.Fatal("queued cancellation not proposed")
	}
	if d := CancellationDecision(StatusRunning, true, nil); d.OperationStatus != "rejected" || d.Error == nil {
		t.Fatal("running cancellation allowed")
	}
	for _, r := range []Result{{Action: ActionDelete, Outcome: OutcomeAccepted, HTTPStatus: 200}, {Action: ActionDelete, Outcome: OutcomeRejected, HTTPStatus: 404}, {Action: ActionDelete, Outcome: OutcomeUnknown, HTTPStatus: 504}} {
		d := CancellationDecision(StatusQueued, true, &r)
		if d.OperationStatus != "unknown" || !d.NeedsReview || d.CancelLocally {
			t.Fatalf("ambiguous deletion confirmed: %+v", d)
		}
	}
	if d := CancellationDecision(StatusSubmissionUnknown, true, nil); d.SendDelete || d.CancelLocally || d.OperationStatus != "pending" {
		t.Fatal("unknown submit was cancelled upstream")
	}
}
