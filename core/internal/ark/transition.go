package ark

func terminal(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCancelled || status == StatusExpired
}
func known(status Status) bool {
	return terminal(status) || status == StatusAccepted || status == StatusSubmitting || status == StatusSubmissionUnknown || status == StatusQueued || status == StatusRunning
}

// ApplyObservation never regresses a terminal state and never translates a
// timeout/404 into failure. It does not authorize billing or releasing holds.
func ApplyObservation(current Status, result Result) Transition {
	out := Transition{Status: current}
	if !known(current) {
		out.NeedsReview = true
		out.Error = failure("invalid_state", "unknown local task state", "", 409)
		return out
	}
	if result.Action == ActionSubmit {
		if current != StatusAccepted && current != StatusSubmitting && current != StatusSubmissionUnknown {
			out.NeedsReview = true
			out.Error = failure("invalid_state", "submit result conflicts with task state", "", 409)
			return out
		}
		switch result.Outcome {
		case OutcomeAccepted:
			if result.TaskID != "" {
				out.Status = StatusQueued
			} else {
				out.Status = StatusSubmissionUnknown
				out.NeedsReview = true
			}
		case OutcomeRejected:
			out.Status = StatusFailed
		case OutcomeNotSent: // Retain state; orchestration may retry only with not-sent proof.
		default:
			out.Status = StatusSubmissionUnknown
			out.NeedsReview = true
		}
	} else if result.Action == ActionQuery {
		if result.Outcome != OutcomeAccepted || result.Observation == nil {
			out.NeedsReview = true
			return out
		}
		observed := result.Observation.Status
		if !known(observed) || observed == StatusAccepted || observed == StatusSubmitting || observed == StatusSubmissionUnknown {
			out.NeedsReview = true
			return out
		}
		if terminal(current) && observed != current {
			out.NeedsReview = true
			return out
		}
		if current == StatusRunning && observed == StatusQueued {
			out.NeedsReview = true
			return out
		}
		out.Status = observed
		out.UsagePending = observed == StatusSucceeded && result.Observation.Usage.CompletionTokens == nil
		if observed == StatusSucceeded && result.Observation.HasError {
			out.NeedsReview = true
			out.UsagePending = true
		}
	} else {
		// DELETE acknowledgements carry no new task status.
		out.NeedsReview = true
	}
	out.Changed = out.Status != current
	return out
}

// CancellationDecision requires dispatchStarted to be read under the same
// transactional lock/fence as a local accepted-task cancellation. It provides
// no monetary release decision. deleteResult can acknowledge DELETE but cannot
// prove queued->cancelled: a raced success could have been deleted instead.
func CancellationDecision(current Status, dispatchStarted bool, deleteResult *Result) CancelDecision {
	out := CancelDecision{OperationStatus: "pending"}
	if deleteResult != nil {
		if deleteResult.Action != ActionDelete {
			out.OperationStatus = "rejected"
			out.Error = failure("invalid_state", "not a deletion result", "", 409)
			return out
		}
		if deleteResult.Outcome == OutcomeRejected && deleteResult.HTTPStatus != 404 {
			out.OperationStatus = "rejected"
			out.Error = failure("invalid_state", "upstream did not accept cancellation", "", 409)
			return out
		}
		// Neither 2xx nor 404, alone or combined, confirms non-billable cancellation.
		out.OperationStatus = "unknown"
		out.NeedsReview = true
		return out
	}
	switch current {
	case StatusAccepted:
		if !dispatchStarted {
			out.CancelLocally = true
			out.OperationStatus = "confirmed"
		}
	case StatusSubmitting, StatusSubmissionUnknown: // Record intent and reconcile first.
	case StatusQueued:
		out.SendDelete = true
	case StatusCancelled:
		out.OperationStatus = "confirmed"
	default:
		out.OperationStatus = "rejected"
		out.Error = failure("invalid_state", "task cannot be cancelled in its current state", "", 409)
	}
	return out
}
