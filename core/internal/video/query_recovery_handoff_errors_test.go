package video

// New-version-only pure tests. This file is not appended to the old archive.
// The shared harness remains buildable without the newer private types.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

type queryHandoffUnknownWrapper struct{ cause error }

func (e *queryHandoffUnknownWrapper) Error() string { panic("oracle must not format errors") }
func (e *queryHandoffUnknownWrapper) Unwrap() error { return e.cause }

func TestQueryRecoveryHandoffClosedErrorOracle(t *testing.T) {
	_, decoded := decodeQueryObservation(`{"completion_tokens":"invalid"}`)
	var nilDecode *queryObservationDecodeError
	var nilAggregate *persistedRecoveryErrors
	loop := &persistedRecoveryErrors{}
	loop.causes = []error{loop}
	for _, sample := range []struct {
		name    string
		err     error
		allowed bool
	}{
		{"valid", decoded, true},
		{"safe_deadline", safeRecoveryErrors(decoded, context.DeadlineExceeded), true},
		{"typed_nil", nilDecode, false},
		{"nil_aggregate", nilAggregate, false},
		{"unknown_kind", &queryObservationDecodeError{kind: 3}, false},
		{"mixed_sql", safeRecoveryErrors(decoded, &mysqlDriver.MySQLError{Number: 1054, Message: "must not print"}), false},
		{"external_join", errors.Join(decoded, context.DeadlineExceeded), false},
		{"fmt_wrapper", fmt.Errorf("foreign wrapper: %w", decoded), false},
		{"custom_wrapper", &queryHandoffUnknownWrapper{cause: decoded}, false},
		{"cancelled", safeRecoveryErrors(decoded, context.Canceled), false},
		{"bounded_cycle", loop, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			classes := queryHandoffErrorClasses(sample.err)
			if queryHandoffExpectedCycleClasses(classes) != sample.allowed {
				t.Fatal("closed handoff oracle accepted uncertainty or rejected known decoding", classes)
			}
		})
	}
	if queryHandoffPrivateType("example.invalid/video", "queryObservationDecodeError", "queryObservationDecodeError") || queryHandoffPrivateType(queryHandoffVideoPackage, "foreignDecoder", "queryObservationDecodeError") {
		t.Fatal("same type name in another package acquired private error meaning")
	}
	if classes := queryHandoffErrorClasses(&mysqlDriver.MySQLError{Number: 1205}); !reflect.DeepEqual(classes, []string{"mysql_1205"}) {
		t.Fatal("actual SQL type did not retain only its numeric classification")
	}
	// This only checks the controlled barrier's error-shape requirement. The
	// real command must additionally observe exact current locks/NULL and zero
	// durable mutations; a plain error alone never proves the review sentinel.
	plain := errors.New("unclassified test-only plain error")
	for _, sample := range []struct {
		err     error
		allowed bool
	}{
		{plain, true}, {safeRecoveryErrors(plain), true},
		{safeRecoveryErrors(plain, &mysqlDriver.MySQLError{Number: 1054}), false},
		{safeRecoveryErrors(plain, context.Canceled), false},
		{safeRecoveryErrors(decoded), false}, {context.Canceled, false},
		{errors.Join(plain), false}, {fmt.Errorf("foreign: %w", plain), false},
		{nilAggregate, false}, {loop, false},
	} {
		if queryHandoffPlainBarrierCause(sample.err) != sample.allowed {
			t.Fatal("controlled barrier error shape accepted a foreign cause")
		}
	}
}
