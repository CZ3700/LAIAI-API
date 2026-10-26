package video

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This alias is used only to exercise rejection/error paths against a local
// driver. No test here claims SQLite can provide MySQL authority time.
type authorityClockTestDialect struct{ gorm.Dialector }

func (authorityClockTestDialect) Name() string { return "mysql" }

func authorityClockUnitDB(t *testing.T, output *bytes.Buffer) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.New(log.New(output, "", 0), logger.Config{LogLevel: logger.Info})})
	if err != nil {
		t.Fatal("cannot open local clock rejection fixture")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("cannot get local fixture pool")
	}
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func authorityClockUnitMySQLName(tx *gorm.DB) *gorm.DB {
	copy := tx.Session(&gorm.Session{})
	copy.Dialector = authorityClockTestDialect{tx.Dialector}
	return copy
}

func TestAuthorityClockRejectsUnsupportedHandles(t *testing.T) {
	var output bytes.Buffer
	db := authorityClockUnitDB(t, &output)
	tx := db.Begin()
	defer tx.Rollback()
	for name, input := range map[string]*gorm.DB{
		"nil": nil, "empty": {}, "sqlite_database": db, "sqlite_transaction": tx,
		"mysql_name_without_transaction": authorityClockUnitMySQLName(db),
		"dry_run_transaction":            authorityClockUnitMySQLName(tx.Session(&gorm.Session{DryRun: true})),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := authorityTimeTx(input)
			if !got.IsZero() || !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() {
				t.Fatal("unsupported clock handle returned a time or unsafe error")
			}
		})
	}
	if output.Len() != 0 {
		t.Fatal("rejected clock handles executed or logged SQL")
	}
}

func TestAuthorityClockErrorsStaySafeAndClassifiable(t *testing.T) {
	var output bytes.Buffer
	db := authorityClockUnitDB(t, &output)
	tx := db.Begin()
	defer tx.Rollback()
	for name, cause := range map[string]error{
		"driver":    errors.New("private DSN mysql://fixture-secret SELECT private_payload"),
		"cancelled": context.Canceled, "deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			input := authorityClockUnitMySQLName(tx)
			input.Error = cause
			got, err := authorityTimeTx(input)
			if !got.IsZero() || !errors.Is(err, cause) || !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() {
				t.Fatal("clock failure exposed a cause or lost its classification")
			}
		})
	}
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var expected error = context.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
			expected = context.DeadlineExceeded
		} else {
			cancel()
		}
		input := authorityClockUnitMySQLName(tx.WithContext(ctx))
		got, err := authorityTimeTx(input)
		cancel()
		if !got.IsZero() || !errors.Is(err, expected) || !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() {
			t.Fatal("caller cancellation did not fail closed")
		}
	}
	if output.Len() != 0 {
		t.Fatal("pre-existing clock failure reached the SQL logger")
	}
}

func TestAuthorityClockSQLFailureAndZeroHaveNoFallback(t *testing.T) {
	var output bytes.Buffer
	db := authorityClockUnitDB(t, &output)
	tx := db.Begin()
	defer tx.Rollback()
	input := authorityClockUnitMySQLName(tx)
	// SQLite genuinely rejects UTC_TIMESTAMP. This proves the SQL failure path
	// does not fall back to time.Now; it is not a MySQL integration assertion.
	got, err := authorityTimeTx(input)
	if !got.IsZero() || !errors.Is(err, ErrUnavailable) || err.Error() != ErrUnavailable.Error() {
		t.Fatal("SQL clock failure fell back or exposed driver diagnostics")
	}
	if output.Len() != 0 {
		t.Fatal("clock SQL failure inherited the caller's SQL logger")
	}
	const name = "authority_clock_unit_zero"
	if err := db.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "UTC_TIMESTAMP(3)") {
			q.Statement.SQL.Reset()
			q.Statement.SQL.WriteString("SELECT NULL AS database_time")
			q.Statement.Vars = nil
		}
	}); err != nil {
		t.Fatal("cannot install zero-result unit fixture")
	}
	defer db.Callback().Row().Remove(name)
	got, err = authorityTimeTx(input)
	if !got.IsZero() || !errors.Is(err, ErrUnavailable) {
		t.Fatal("zero database time became usable authority")
	}
	wrapped, wrapperErr := responseDatabaseTimeTx(input)
	if !wrapped.IsZero() || !errors.Is(wrapperErr, ErrUnavailable) {
		t.Fatal("legacy response wrapper used different clock semantics")
	}
}
