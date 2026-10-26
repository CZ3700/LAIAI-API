package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/videobilling"
)

type queryUnknownWrapper struct{ cause error }

func (*queryUnknownWrapper) Error() string   { return "private unknown wrapper semantics" }
func (e *queryUnknownWrapper) Unwrap() error { return e.cause }

type queryUnknownMultiWrapper struct{ causes []error }

func (*queryUnknownMultiWrapper) Error() string     { return "private multi-wrapper semantics" }
func (e *queryUnknownMultiWrapper) Unwrap() []error { return e.causes }

func TestQueryRecoveryDecoderAndClosedErrorTree(t *testing.T) {
	_, syntax := decodeQueryObservation(`{"task_id":"private-syntax-marker"`)
	_, mismatch := decodeQueryObservation(`{"completion_tokens":"private-type-marker"}`)
	if syntax == nil || mismatch == nil {
		t.Fatal("actual typed JSON rejection did not occur")
	}
	var external any
	externalErr := json.Unmarshal([]byte(`{`), &external)
	cycle := &persistedRecoveryErrors{}
	cycle.causes = []error{cycle}
	for name, sample := range map[string]struct {
		err  error
		want queryRecoveryCause
	}{
		"syntax": {syntax, queryCauseObservation}, "type": {mismatch, queryCauseObservation},
		"safe_nested":         {safeRecoveryErrors(safeRecoveryErrors(syntax), mismatch), queryCauseObservation},
		"conflict":            {videobilling.ErrConflict, queryCauseSettlement},
		"safe_conflict":       {safeRecoveryErrors(videobilling.ErrConflict), queryCauseSettlement},
		"create_quarantine":   {safeRecoveryErrors(errCreateInboxReview), queryCauseCreateReview},
		"bare_external_json":  {externalErr, queryCauseTemporary},
		"bare_sentinel":       {errQueryObservationInvalid, queryCauseTemporary},
		"unknown_wrapper":     {&queryUnknownWrapper{syntax}, queryCauseTemporary},
		"unknown_multi":       {&queryUnknownMultiWrapper{[]error{syntax}}, queryCauseTemporary},
		"unknown_conflict":    {&queryUnknownWrapper{videobilling.ErrConflict}, queryCauseTemporary},
		"unknown_quarantine":  {&queryUnknownWrapper{errCreateInboxReview}, queryCauseTemporary},
		"quarantine_cancel":   {safeRecoveryErrors(errCreateInboxReview, context.Canceled), queryCauseTemporary},
		"fmt_wrapper":         {fmt.Errorf("private-wrapper: %w", syntax), queryCauseTemporary},
		"external_join":       {errors.Join(syntax, mismatch), queryCauseTemporary},
		"safe_external_join":  {safeRecoveryErrors(errors.Join(syntax, mismatch)), queryCauseTemporary},
		"cancelled":           {safeRecoveryErrors(syntax, context.Canceled), queryCauseTemporary},
		"deadline":            {safeRecoveryErrors(mismatch, context.DeadlineExceeded), queryCauseTemporary},
		"sql":                 {safeRecoveryErrors(syntax, &mysql.MySQLError{Number: 1205, Message: "private SQL parameter"}), queryCauseTemporary},
		"authority":           {safeRecoveryErrors(syntax, authorityClockFailure(syntax)), queryCauseTemporary},
		"other":               {safeRecoveryErrors(syntax, errors.New("private other cause")), queryCauseTemporary},
		"mixed_permanent":     {safeRecoveryErrors(syntax, videobilling.ErrConflict), queryCauseTemporary},
		"invalid_kind":        {&queryObservationDecodeError{kind: 99}, queryCauseTemporary},
		"empty_safe_wrapper":  {&persistedRecoveryErrors{}, queryCauseTemporary},
		"cyclic_safe_wrapper": {cycle, queryCauseTemporary},
		"nil":                 {nil, queryCauseTemporary},
	} {
		t.Run(name, func(t *testing.T) {
			if got := classifyQueryRecoveryCause(sample.err); got != sample.want {
				t.Fatal("incorrect closed error-tree classification", got, sample.want)
			}
		})
	}
	for _, err := range []error{syntax, mismatch, safeRecoveryErrors(syntax, context.Canceled)} {
		if strings.Contains(err.Error(), "private-") {
			t.Fatal("decoder or safe wrapper exposed private evidence")
		}
	}
	if !errors.Is(safeRecoveryErrors(syntax, context.Canceled), context.Canceled) {
		t.Fatal("safe decoder classification lost caller cancellation")
	}
	// Preserve the old decoder's null/missing/zero semantics; this change does
	// not reinterpret provider observations or require a positive usage value.
	for _, raw := range []string{`null`, `{}`, `{"completion_tokens":null}`, `{"completion_tokens":0}`} {
		if _, err := decodeQueryObservation(raw); err != nil {
			t.Fatal("changed existing JSON decoding semantics")
		}
	}
}

func TestQueryRecoveryEvidenceDigestCoversDurableFields(t *testing.T) {
	row := QueryReceipt{OrgID: 3, TaskID: "task", AttemptID: "attempt", Sequence: 5, ReceivedAt: time.Unix(1, 0).UTC()}
	before, err := queryEvidenceDigest(row)
	if err != nil {
		t.Fatal(err)
	}
	mutable := map[string]bool{"AppliedAt": true, "ApplyErrorCode": true, "RecoveryDueAt": true, "RecoveryVersion": true}
	typ := reflect.TypeOf(row)
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		t.Run(field.Name, func(t *testing.T) {
			// Storage fields must never silently disappear behind a JSON DTO tag.
			if field.Tag.Get("json") != "" {
				t.Fatal("evidence hashing requires explicit review of new JSON tags")
			}
			copy := row
			value := reflect.ValueOf(&copy).Elem().Field(index)
			switch value.Kind() {
			case reflect.String:
				value.SetString(value.String() + "changed")
			case reflect.Uint64, reflect.Uint32:
				value.SetUint(value.Uint() + 1)
			case reflect.Int:
				value.SetInt(value.Int() + 1)
			case reflect.Bool:
				value.SetBool(!value.Bool())
			case reflect.Struct:
				value.Set(reflect.ValueOf(row.ReceivedAt.Add(time.Second)))
			case reflect.Pointer:
				when := time.Unix(5, 0).UTC()
				value.Set(reflect.ValueOf(&when))
			default:
				t.Fatal("new persistent field needs an independent mutation oracle")
			}
			after, err := queryEvidenceDigest(copy)
			if err != nil || (after == before) != mutable[field.Name] {
				t.Fatal("evidence fingerprint omitted an immutable field or included mutable progress")
			}
		})
	}
	row.RecoveryVersion = math.MaxUint64
	if token, err := observeQueryRecovery(row); err == nil || token.valid {
		t.Fatal("progress version overflow became a valid write token")
	}
}

func TestQueryRecoveryDatabaseClockFailsClosed(t *testing.T) {
	var output bytes.Buffer
	db := authorityClockUnitDB(t, &output)
	tx := db.Begin()
	defer tx.Rollback()
	if when, err := queryRecoveryTimeTx(tx); !when.IsZero() || !errors.Is(err, ErrUnavailable) {
		t.Fatal("SQLite progress clock became primary authority")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if when, err := queryRecoveryTimeTx(authorityClockUnitMySQLName(tx.WithContext(ctx))); !when.IsZero() || !errors.Is(err, context.Canceled) {
		t.Fatal("progress clock lost earlier caller cancellation")
	}
	result, err := (&Service{DB: db}).recoverQueryReceiptsProgress(context.Background())
	if !errors.Is(err, ErrUnavailable) || result != (recoveryLaneResult{Failed: 1}) {
		t.Fatal("non-MySQL candidate failure was hidden or counted as a selected receipt")
	}
	if output.Len() != 0 {
		t.Fatal("rejected clock leaked SQL")
	}
}

func TestQueryRecoveryCountsDoNotTurnFailureIntoNoop(t *testing.T) {
	for _, sample := range []struct {
		name string
		kind queryRecoveryDisposition
		err  error
		want recoveryLaneResult
	}{
		{"replay", queryRecoveryNoop, nil, recoveryLaneResult{Noop: 1}},
		{"metadata_failure", queryRecoveryNoop, context.Canceled, recoveryLaneResult{Failed: 1}},
		{"existing_review_barrier", queryRecoveryNoop, errQueryRecoveryReview, recoveryLaneResult{Failed: 1}},
		{"diagnostic_failure", queryRecoveryFailed, ErrConflict, recoveryLaneResult{Failed: 1}},
		{"applied", queryRecoveryApplied, nil, recoveryLaneResult{Applied: 1}},
		{"deferred_after_failure", queryRecoveryDeferred, ErrConflict, recoveryLaneResult{Deferred: 1, Failed: 1}},
		{"review_after_failure", queryRecoveryReview, errQueryObservationInvalid, recoveryLaneResult{Review: 1, Failed: 1}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			var got recoveryLaneResult
			queryRecoveryResult{disposition: sample.kind}.countIn(&got, sample.err)
			if got != sample.want {
				t.Fatal("failed query was reported as a successful no-op, or committed progress was lost")
			}
		})
	}
}
