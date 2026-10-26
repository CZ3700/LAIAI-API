package invocation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var ErrVideoExecutionAuthorityUnavailable = errors.New("video evidence: execution authority unavailable")

const (
	videoExecutionDatabaseTimeout = time.Second
	videoExecutionClockTimeout    = 200 * time.Millisecond
)

type videoExecutionAuthorityError struct{ cause error }

func (e *videoExecutionAuthorityError) Error() string {
	return ErrVideoExecutionAuthorityUnavailable.Error()
}
func (e *videoExecutionAuthorityError) Unwrap() error { return e.cause }
func (e *videoExecutionAuthorityError) Is(target error) bool {
	return target == ErrVideoExecutionAuthorityUnavailable
}

func videoExecutionAuthorityFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return &videoExecutionAuthorityError{cause: cause}
}

func videoExecutionTransactionRequired(tx *gorm.DB) error {
	if tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.DryRun || tx.Dialector.Name() != "mysql" {
		return videoExecutionAuthorityFailure(ErrVideoTransactionRequired)
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return videoExecutionAuthorityFailure(ErrVideoTransactionRequired)
	}
	if tx.Statement.Context == nil {
		return videoExecutionAuthorityFailure(ErrVideoTransactionRequired)
	}
	if err := tx.Statement.Context.Err(); err != nil {
		return videoExecutionAuthorityFailure(err)
	}
	if tx.Error != nil {
		return videoExecutionAuthorityFailure(tx.Error)
	}
	return nil
}

// Read after the authority locks/metadata, never before their possible wait.
// The deployment supplies the primary MySQL connection with loc=UTC. No host,
// replica, SQLite or transaction-start timestamp is an acceptable substitute.
func videoExecutionAuthorityTimeTx(tx *gorm.DB) (time.Time, error) {
	if err := videoExecutionTransactionRequired(tx); err != nil {
		return time.Time{}, err
	}
	ctx, cancel := context.WithTimeout(tx.Statement.Context, videoExecutionClockTimeout)
	defer cancel()
	var row struct{ DatabaseTime time.Time }
	q := tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).Raw("SELECT UTC_TIMESTAMP(3) AS database_time").Scan(&row)
	if err := ctx.Err(); err != nil {
		return time.Time{}, videoExecutionAuthorityFailure(err)
	}
	if q.Error != nil {
		return time.Time{}, videoExecutionAuthorityFailure(q.Error)
	}
	if q.RowsAffected != 1 || row.DatabaseTime.IsZero() {
		return time.Time{}, ErrVideoExecutionAuthorityUnavailable
	}
	return row.DatabaseTime.UTC().Truncate(time.Millisecond), nil
}

// Bound this method's DB phase without replacing its caller's transaction.
func videoExecutionBoundTx(tx *gorm.DB) (*gorm.DB, context.CancelFunc, error) {
	if err := videoExecutionTransactionRequired(tx); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(tx.Statement.Context, videoExecutionDatabaseTimeout)
	return tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}), cancel, nil
}

func videoExecutionResultError(ctx context.Context, err error) error {
	if err == nil {
		if ctx.Err() != nil {
			return videoExecutionAuthorityFailure(ctx.Err())
		}
		return nil
	}
	// Preserve known interpretation/identity denials, but ALWAYS retain a
	// canceled DB phase as authority-unavailable too. Expired/audit/retention
	// alone must not turn an ambiguous canceled phase into a local NotSent.
	for _, known := range []error{ErrVideoInvalid, ErrVideoAccessDenied, ErrVideoSchemaUnsupported, ErrVideoCorrupt, ErrVideoConflict, ErrVideoNotFound, ErrVideoExpired, ErrVideoRetentionDenied, ErrVideoExecutionAuthorityUnavailable, ErrVideoAuditUnavailable} {
		if errors.Is(err, known) {
			if ctx.Err() != nil {
				return errors.Join(err, videoExecutionAuthorityFailure(ctx.Err()))
			}
			return err
		}
	}
	if ctx.Err() != nil {
		return videoExecutionAuthorityFailure(ctx.Err())
	}
	return videoExecutionAuthorityFailure(err)
}

func (s *VideoStore) executionDatabasePhase(ctx context.Context, readOnly bool, fn func(*gorm.DB) error) error {
	if s == nil || s.db == nil || s.db.Config == nil || s.db.Statement == nil || s.db.Dialector == nil || s.db.Dialector.Name() != "mysql" || s.db.DryRun || ctx == nil {
		return ErrVideoExecutionAuthorityUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, videoExecutionDatabaseTimeout)
	defer cancel()
	q := s.db.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard})
	err := q.Transaction(fn, &sql.TxOptions{ReadOnly: readOnly})
	return videoExecutionResultError(ctx, err)
}
