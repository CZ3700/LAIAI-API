package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoveryErrorClassificationRejectsForeignSemantics(t *testing.T) {
	unknown := errors.New("private-database-marker")
	cycle := &persistedRecoveryErrors{}
	cycle.causes = []error{cycle}
	for name, err := range map[string]error{
		"nil":                     nil,
		"database":                unknown,
		"foreign_wrapper":         fmt.Errorf("storage failed: %w", ErrConflict),
		"foreign_join":            errors.Join(ErrConflict, invocation.ErrVideoCorrupt),
		"mixed_cancel":            safeRecoveryErrors(ErrConflict, context.Canceled),
		"mixed_deadline":          safeRecoveryErrors(videoruntime.ErrUnsupported, context.DeadlineExceeded),
		"mixed_database":          safeRecoveryErrors(gorm.ErrRecordNotFound, unknown),
		"authority_wrapped_known": authorityClockFailure(ErrConflict),
		"nested_authority":        safeRecoveryErrors(ErrConflict, authorityClockFailure(invocation.ErrVideoCorrupt)),
		"empty_private":           &persistedRecoveryErrors{},
		"nil_private":             (*persistedRecoveryErrors)(nil),
		"cyclic_private":          cycle,
	} {
		t.Run(name, func(t *testing.T) {
			if recoveryPermanentCreateError(err) || permanentMutationIdentityError(err) || recoveryOnlyErrors(err, videoruntime.ErrUnsupported, gorm.ErrRecordNotFound) {
				t.Fatal("unproven error semantics authorized isolation/noop")
			}
		})
	}
	for _, err := range []error{ErrConflict, invocation.ErrVideoCorrupt, safeRecoveryErrors(ErrConflict, safeRecoveryErrors(invocation.ErrVideoCorrupt))} {
		if !recoveryPermanentCreateError(err) || !permanentMutationIdentityError(err) {
			t.Fatal("known permanent metadata cause lost its existing classification")
		}
	}
	if !recoveryOnlyErrors(safeRecoveryErrors(videoruntime.ErrUnsupported, gorm.ErrRecordNotFound), videoruntime.ErrUnsupported, gorm.ErrRecordNotFound) {
		t.Fatal("pure known expired-candidate noop rejected")
	}
	wrapped := safeRecoveryErrors(unknown, context.Canceled)
	if !errors.Is(wrapped, unknown) || !errors.Is(wrapped, context.Canceled) || strings.Contains(wrapped.Error(), "private-database-marker") {
		t.Fatal("safe aggregation lost causes or exposed a database diagnostic")
	}
}

func TestRecoveryCommittedDispositionAndFailureAreIndependent(t *testing.T) {
	var got recoveryLaneResult
	got.record(recoveryUnchanged, nil)              // replay
	got.record(recoveryUnchanged, context.Canceled) // failed/unknown commit
	got.record(recoveryApplied, nil)
	got.record(recoveryDeferred, ErrConflict)
	got.record(recoveryReview, invocation.ErrVideoCorrupt)
	if got != (recoveryLaneResult{Applied: 1, Deferred: 1, Review: 1, Noop: 1, Failed: 3}) {
		t.Fatalf("disposition/failure accounting: %+v", got)
	}
}

// The callback substitutes only the reducer boundary. Backoff is a real SQL
// failure against a missing table; it must not erase the original cause or
// prevent the following independent item from being processed.
func TestInboxRecoveryRetainsApplyAndBackoffFailureAndContinues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	s := &Service{DB: db}
	original, prior := errors.New("synthetic-apply-failure"), errors.New("prior-expired-failure")
	calls := 0
	result, err := s.recoverInboxApplications(context.Background(), []ReceiptApplication{{OrgID: 1, ReceiptID: "first"}, {OrgID: 1, ReceiptID: "second"}}, recoveryLaneResult{}, prior,
		func(_ context.Context, _ uint64, receipt string, _ *Operation) (recoveryDisposition, error) {
			calls++
			if receipt == "first" {
				return recoveryUnchanged, original
			}
			return recoveryApplied, nil
		})
	if calls != 2 || result != (recoveryLaneResult{Selected: 2, Applied: 1, Failed: 1}) || !errors.Is(err, original) || !errors.Is(err, prior) {
		t.Fatalf("recovery lost failure/progress: calls=%d result=%+v", calls, result)
	}
	var leaves []error
	var flatten func(error)
	flatten = func(err error) {
		if wrapped, ok := err.(*persistedRecoveryErrors); ok {
			for _, cause := range wrapped.causes {
				flatten(cause)
			}
			return
		}
		if err != nil {
			leaves = append(leaves, err)
		}
	}
	flatten(err)
	if len(leaves) != 3 || strings.Contains(err.Error(), "synthetic-") || strings.Contains(err.Error(), "no such table") {
		t.Fatal("backoff SQL error not retained safely alongside both original causes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = s.recoverInboxApplications(ctx, []ReceiptApplication{{ReceiptID: "unprocessed"}}, recoveryLaneResult{}, nil,
		func(context.Context, uint64, string, *Operation) (recoveryDisposition, error) {
			t.Fatal("cancelled lane entered reducer")
			return recoveryApplied, nil
		})
	if !errors.Is(err, context.Canceled) || result != (recoveryLaneResult{Selected: 1}) {
		t.Fatal("cancelled unprocessed item counted as successful work")
	}
}
