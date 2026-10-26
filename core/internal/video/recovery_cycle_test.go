package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRecoveryErrorsHideMessagesAndPreserveEveryCause(t *testing.T) {
	private := errors.New("private SQL url key and response marker")
	wrapped := safeRecoveryErrors(nil, private, fmt.Errorf("private wrapper: %w", context.Canceled))
	if wrapped == nil || strings.Contains(wrapped.Error(), "private") || !errors.Is(wrapped, private) || !errors.Is(wrapped, context.Canceled) {
		t.Fatal("recovery must preserve causes without exposing their messages")
	}
	if safeRecoveryErrors(nil, nil) != nil {
		t.Fatal("successful recovery acquired an error")
	}
	// A caller inspecting causes must not be able to replace the stored tree.
	causes := wrapped.(interface{ Unwrap() []error }).Unwrap()
	causes[0] = context.DeadlineExceeded
	if !errors.Is(wrapped, private) || errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatal("error inspection modified the original causes")
	}
}

func TestRecoveryLaneDeadlineDoesNotStarveNextLane(t *testing.T) {
	s := &Service{}
	called := false
	err := s.runRecoveryLanes(context.Background(), []recoveryLane{
		{recoveryCreateInbox, func(ctx context.Context) (recoveryLaneResult, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > recoveryLaneTimeout {
				t.Fatal("lane has no bounded deadline")
			}
			<-ctx.Done()
			return recoveryLaneResult{Selected: 1, Failed: 1}, ctx.Err()
		}},
		{recoveryQueryReceipts, func(ctx context.Context) (recoveryLaneResult, error) {
			if ctx.Err() != nil {
				t.Fatal("previous lane consumed the next lane budget")
			}
			called = true
			return recoveryLaneResult{Selected: 2, Applied: 2}, nil
		}},
	})
	if !called || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lane timeout either vanished or prevented healthy recovery")
	}
	observation := s.metrics().runtimeSnapshot()
	if len(observation.RecoveryLanes) != 4 || observation.RecoveryLanes[0].Failed != 1 || observation.RecoveryLanes[0].Errors != 1 || observation.RecoveryLanes[3].Applied != 2 || observation.RecoveryLanes[3].Errors != 0 {
		t.Fatal("recovery outcomes are not bounded and independently observable")
	}
}

func TestRecoveryParentCancellationStopsBeforeNextLane(t *testing.T) {
	s := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	err := s.runRecoveryLanes(ctx, []recoveryLane{
		{recoveryCreateInbox, func(context.Context) (recoveryLaneResult, error) {
			cancel()
			return recoveryLaneResult{Selected: 1, Applied: 1}, nil
		}},
		{recoveryMutationInbox, func(context.Context) (recoveryLaneResult, error) {
			called = true
			return recoveryLaneResult{}, nil
		}},
	})
	if called || !errors.Is(err, context.Canceled) {
		t.Fatal("parent cancellation must stop without starting another lane")
	}
	if s.metrics().runtimeSnapshot().RecoveryLanes[0].Applied != 1 {
		t.Fatal("cancellation erased already committed work")
	}
}
