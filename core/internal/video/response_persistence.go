package video

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

var (
	ErrResponseSaveUnconfirmed = errors.New("video response save unconfirmed")
	ErrDispatchPaused          = errors.New("video dispatch paused")
	errResponseStorage         = errors.New("video response storage unavailable")
)

const responseSaveWindow = 10 * time.Second

// Driver causes remain classifiable, but Error never includes SQL, DSN,
// payload, provider text, or connection diagnostics.
type responseStorageError struct{ cause error }

func (e *responseStorageError) Error() string { return errResponseStorage.Error() }
func (e *responseStorageError) Unwrap() error { return e.cause }
func (e *responseStorageError) Is(target error) bool {
	return target == errResponseStorage || target == ErrUnavailable
}

func responseStorageFailure(err error) error {
	if err == nil || errors.Is(err, errResponseStorage) {
		return err
	}
	return &responseStorageError{cause: err}
}

func responseAppendRetryable(err error) bool {
	if err == nil {
		return false
	}
	for _, permanent := range []error{ErrInvalid, ErrConflict, videoruntime.ErrUnsupported,
		invocation.ErrVideoInvalid, invocation.ErrVideoConflict, invocation.ErrVideoCorrupt,
		invocation.ErrVideoNotFound, invocation.ErrVideoAccessDenied, invocation.ErrVideoExpired,
		invocation.ErrVideoRetentionDenied, invocation.ErrVideoSchemaUnsupported,
		invocation.ErrVideoTransactionRequired, gorm.ErrRecordNotFound,
		context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, permanent) {
			return false
		}
	}
	var mysqlErr *drivermysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1205, 1213: // lock wait timeout / deadlock; entire append rolls back
			return true
		default:
			return false
		}
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, drivermysql.ErrInvalidConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var network net.Error
	return errors.As(err, &network)
}

// DispatchPaused is process-local and sticky until a fresh Service is started.
// It cannot recall in-flight HTTP and is not a distributed lease/fence.
func (s *Service) DispatchPaused() bool { return s != nil && s.responsePaused.Load() }

func responseRetryWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// The context is created once by the orchestrator. Pool waits, statements and
// delays consume that same window; retries never get a fresh ten seconds.
// Socket idle timeouts additionally bound COMMIT waits that the driver does
// not interrupt using the transaction context. They are not an absolute
// wall-clock proof against a peer continuously trickling protocol bytes.
func appendPreparedResponseWithRetry(ctx context.Context, p *preparedResponseReceipt,
	appendFn func(context.Context, *preparedResponseReceipt) (ResponseInbox, error),
	waitFn func(context.Context, time.Duration) error) (ResponseInbox, error) {
	var last error
	for i, delay := range []time.Duration{0, 200 * time.Millisecond, 800 * time.Millisecond} {
		if err := ctx.Err(); err != nil {
			return ResponseInbox{}, err
		}
		if delay != 0 {
			if err := waitFn(ctx, delay); err != nil {
				return ResponseInbox{}, err
			}
		}
		if err := ctx.Err(); err != nil {
			return ResponseInbox{}, err
		}
		out, err := appendFn(ctx, p)
		if err == nil {
			return out, nil // A confirmed commit remains saved at the deadline.
		}
		last = err
		if i == 2 || !responseAppendRetryable(err) {
			return ResponseInbox{}, err
		}
	}
	return ResponseInbox{}, last
}

// saveCreateResponse does not interpret/apply the result and never retries
// HTTP. Even a paused service still saves responses already in flight.
func (s *Service) saveCreateResponse(ctx context.Context, attempt ExecutionAttempt, result ark.Result, credential string) (ResponseInbox, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), responseSaveWindow)
	defer cancel()
	p, err := s.prepareResponseReceipt(attempt, result, credential)
	if err == nil {
		var receipt ResponseInbox
		receipt, err = appendPreparedResponseWithRetry(saveCtx, p, s.appendResponseReceipt, responseRetryWait)
		if err == nil {
			return receipt, nil
		}
	}
	if s != nil {
		s.responsePaused.Store(true)
	}
	// Cause remains internal to append/retry classification. No caller needs
	// driver diagnostics to decide that evidence was not confirmed durable.
	return ResponseInbox{}, ErrResponseSaveUnconfirmed
}
