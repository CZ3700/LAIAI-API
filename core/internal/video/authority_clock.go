package video

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Keep the database cause classifiable without exposing SQL, driver diagnostics
// or connection details to a caller. A clock failure never proves not-sent.
type authorityClockError struct{ cause error }

func (e *authorityClockError) Error() string        { return ErrUnavailable.Error() }
func (e *authorityClockError) Unwrap() error        { return e.cause }
func (e *authorityClockError) Is(target error) bool { return target == ErrUnavailable }

func authorityClockFailure(err error) error {
	if err == nil {
		return nil
	}
	return &authorityClockError{cause: err}
}

// authorityTimeTx reads the primary transaction's clock in a new statement.
// Callers must acquire their authority locks BEFORE calling this function. The
// value is not a fence or a reusable send permit, and never falls back to a host
// clock. SQLite does not implement the worker authority contract.
func authorityTimeTx(tx *gorm.DB) (time.Time, error) {
	if tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.DryRun || tx.Dialector.Name() != "mysql" {
		return time.Time{}, ErrUnavailable
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return time.Time{}, ErrUnavailable
	}
	ctx := tx.Statement.Context
	if ctx == nil {
		return time.Time{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, authorityClockFailure(err)
	}
	if tx.Error != nil {
		return time.Time{}, authorityClockFailure(tx.Error)
	}
	if bounded, _ := ctx.Value(recoveryClockBudgetKey{}).(bool); bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, recoveryClockTimeout)
		defer cancel()
	}
	var row struct{ DatabaseTime time.Time }
	q := tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard})
	if err := q.Raw("SELECT UTC_TIMESTAMP(3) AS database_time").Scan(&row).Error; err != nil {
		if ctx.Err() != nil {
			return time.Time{}, authorityClockFailure(ctx.Err())
		}
		return time.Time{}, authorityClockFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, authorityClockFailure(err)
	}
	if row.DatabaseTime.IsZero() {
		return time.Time{}, ErrUnavailable
	}
	return row.DatabaseTime.UTC().Truncate(time.Millisecond), nil
}
