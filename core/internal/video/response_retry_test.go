package video

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

func TestResponseRetryClassificationPreservesSafeStorageCauses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"deadlock", &drivermysql.MySQLError{Number: 1213, Message: "private-sql"}, true},
		{"lock-timeout", &drivermysql.MySQLError{Number: 1205}, true},
		{"commit-eof", io.EOF, true}, {"partial-response", io.ErrUnexpectedEOF, true},
		{"bad-connection", driver.ErrBadConn, true}, {"invalid-connection", drivermysql.ErrInvalidConn, true},
		{"network", &net.DNSError{Err: "private-host", IsTimeout: true}, true},
		{"missing-table", &drivermysql.MySQLError{Number: 1146, Message: "private-sql"}, false},
		{"syntax", &drivermysql.MySQLError{Number: 1064}, false},
		{"permission", &drivermysql.MySQLError{Number: 1045}, false},
		{"identity", ErrConflict, false}, {"invalid", ErrInvalid, false},
		{"missing-identity", gorm.ErrRecordNotFound, false},
		{"schema", invocation.ErrVideoSchemaUnsupported, false},
		{"generic-storage", invocation.ErrVideoStorage, false},
		{"missing-pool", ErrUnavailable, false},
		{"cancel", context.Canceled, false}, {"deadline", context.DeadlineExceeded, false},
		{"identity-takes-priority", errors.Join(ErrConflict, io.EOF), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := responseStorageFailure(tc.err)
			if responseAppendRetryable(tc.err) != tc.retry || responseAppendRetryable(wrapped) != tc.retry {
				t.Fatal("storage cause classification lost")
			}
			if !errors.Is(wrapped, tc.err) || wrapped.Error() != errResponseStorage.Error() || strings.Contains(wrapped.Error(), "private") {
				t.Fatal("cause unavailable or unsafe diagnostic exposed")
			}
		})
	}
}

func TestResponseRetryUsesOnePreparedIdentityAndOneDeadline(t *testing.T) {
	s, attempt, _ := inboxPreparationFixture(t)
	p, err := s.prepareResponseReceipt(attempt, ark.Result{Action: ark.ActionSubmit, Outcome: ark.OutcomeAccepted, TaskID: "safe-task", Body: []byte(`{"id":"safe-task"}`)}, "credential")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), responseSaveWindow)
	defer cancel()
	deadline, _ := ctx.Deadline()
	calls := 0
	var waits []time.Duration
	out, err := appendPreparedResponseWithRetry(ctx, p, func(got context.Context, candidate *preparedResponseReceipt) (ResponseInbox, error) {
		calls++
		gotDeadline, ok := got.Deadline()
		if got != ctx || !ok || !gotDeadline.Equal(deadline) || candidate != p {
			t.Fatal("retry replaced the prepared evidence or timeout")
		}
		if calls < 3 {
			return ResponseInbox{}, responseStorageFailure(io.EOF)
		}
		return candidate.receipt, nil
	}, func(got context.Context, delay time.Duration) error {
		if got != ctx {
			t.Fatal("retry wait received another context")
		}
		waits = append(waits, delay)
		return nil
	})
	if err != nil || calls != 3 || out.ReceiptID != p.receipt.ReceiptID || out.EvidenceHMAC != p.receipt.EvidenceHMAC || !reflect.DeepEqual(waits, []time.Duration{200 * time.Millisecond, 800 * time.Millisecond}) {
		t.Fatal("bounded identity-preserving retry failed")
	}
}

func TestResponseRetryStopsOnPermanentErrorAndSharedDeadline(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls, waits := 0, 0
		_, err := appendPreparedResponseWithRetry(ctx, nil, func(context.Context, *preparedResponseReceipt) (ResponseInbox, error) {
			calls++
			if permanent {
				return ResponseInbox{}, ErrConflict
			}
			return ResponseInbox{}, io.EOF
		}, func(got context.Context, _ time.Duration) error {
			waits++
			cancel()
			return got.Err()
		})
		cancel()
		if calls != 1 || permanent && (!errors.Is(err, ErrConflict) || waits != 0) || !permanent && (!errors.Is(err, context.Canceled) || waits != 1) {
			t.Fatal("retry ignored permanent failure or overall cancellation")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := appendPreparedResponseWithRetry(ctx, nil, func(context.Context, *preparedResponseReceipt) (ResponseInbox, error) {
		t.Fatal("expired overall window started another append")
		return ResponseInbox{}, nil
	}, responseRetryWait)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("expired context lost")
	}
}

func TestResponseSavePreparationFailureAndMissingPoolPauseDispatch(t *testing.T) {
	for _, validResult := range []bool{false, true} {
		s, attempt, _ := inboxPreparationFixture(t)
		result := ark.Result{}
		if validResult {
			result = ark.Result{Action: ark.ActionSubmit, Outcome: ark.OutcomeAccepted, TaskID: "safe-task", Body: []byte(`{}`)}
		}
		if _, err := s.saveCreateResponse(context.Background(), attempt, result, "credential"); !errors.Is(err, ErrResponseSaveUnconfirmed) || !s.DispatchPaused() {
			t.Fatal("unconfirmed evidence did not pause dispatch")
		}
		s.ResponseInboxEnabled = false
		if !s.DispatchPaused() {
			t.Fatal("write flag reset sticky pause")
		}
	}
}
