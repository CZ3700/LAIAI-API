package videobilling

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func initialReadRef(s Snapshot) InitialSettlementRef {
	return InitialSettlementRef{OrgID: s.OrgID, TaskID: s.TaskID, SnapshotID: s.ID, BillingSchemaVersion: s.BillingSchemaVersion}
}

func initialRead(t *testing.T, db *gorm.DB, ref InitialSettlementRef) InitialSettlement {
	t.Helper()
	var value InitialSettlement
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		value, err = ReadInitialSettlementTx(tx, ref)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestInitialSettlementReadPublicWritersAndCompare(t *testing.T) {
	for _, name := range []string{"absent", "missing_usage", "usage_zero", "usage_positive", "provider_nocharge", "not_dispatched"} {
		t.Run(name, func(t *testing.T) {
			db, q := setup(t, "100")
			s := reserve(t, db, q, "task_initial").Snapshot
			kind := InitialSettlementAbsent
			switch name {
			case "missing_usage":
				settle(t, db, s.TaskID, nil, "missing")
			case "usage_zero", "usage_positive":
				n := int64(0)
				if name == "usage_positive" {
					n = 100
				}
				result := settle(t, db, s.TaskID, &n, "v1")
				if n == 0 && result.Entry.JournalID != nil {
					t.Fatal("zero usage unexpectedly requires a journal")
				}
				kind = InitialSettlementUsageSettled
			case "provider_nocharge", "not_dispatched":
				reason := ReleaseProviderNoCharge
				kind = InitialSettlementProviderNoChargeReleased
				if name == "not_dispatched" {
					reason, kind = ReleaseNotDispatched, InitialSettlementNotDispatchedReleased
				}
				if err := db.Transaction(func(tx *gorm.DB) error {
					result, err := ReleaseConfirmedTx(tx, ReleaseInput{OrgID: 10, TaskID: s.TaskID, Reason: reason, Confirmed: true, EvidenceID: "release", Now: testAt})
					if err == nil && result.Entry.JournalID != nil {
						t.Fatal("release unexpectedly requires a journal")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := keyUsed(t, db)
			value := initialRead(t, db, initialReadRef(s))
			if value.Kind() != kind {
				t.Fatalf("kind=%s want=%s", value.Kind(), kind)
			}
			for _, n := range []int64{0, 100, 101} {
				got, err := value.Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: &n})
				want := InitialSettlementRelationConflict
				if kind == InitialSettlementAbsent {
					want = InitialSettlementRelationAbsent
				}
				if (name == "usage_zero" && n == 0) || (name == "usage_positive" && n == 100) {
					want = InitialSettlementRelationCompatible
				}
				if err != nil || got != want {
					t.Fatalf("compare tokens=%d: %s %v", n, got, err)
				}
			}
			got, err := value.Compare(InitialSettlementExpectation{Kind: InitialSettlementProviderNoChargeReleased})
			want := InitialSettlementRelationConflict
			if kind == InitialSettlementAbsent {
				want = InitialSettlementRelationAbsent
			}
			if kind == InitialSettlementProviderNoChargeReleased {
				want = InitialSettlementRelationCompatible
			}
			if err != nil || got != want || !before.Equal(keyUsed(t, db)) {
				t.Fatal("release comparison or financial preservation failed")
			}
			for _, invalid := range []InitialSettlementExpectation{{}, {Kind: InitialSettlementAbsent}, {Kind: InitialSettlementNotDispatchedReleased}, {Kind: InitialSettlementUsageSettled}, {Kind: InitialSettlementUsageSettled, CompletionTokens: token(-1)}, {Kind: InitialSettlementProviderNoChargeReleased, CompletionTokens: token(0)}} {
				if relation, err := value.Compare(invalid); relation != "" || !errors.Is(err, ErrInvalid) {
					t.Fatal("invalid expectation accepted")
				}
			}
		})
	}
	if (InitialSettlement{}).Kind() != "" {
		t.Fatal("zero kind")
	}
	if relation, err := (InitialSettlement{}).Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: token(0)}); relation != "" || !errors.Is(err, ErrInvalid) {
		t.Fatal("zero object accepted")
	}
}

func TestInitialSettlementReadPreservesReviewedAdjustment(t *testing.T) {
	db, q := setupReconciliation(t, "100")
	s := reserve(t, db, q, "task_1").Snapshot
	line := importLine(t, db, lineFixture("v1", 1, token(100), "0.0046"), bindingFixture()).Line
	if err := db.Transaction(func(tx *gorm.DB) error { _, err := SettleBillUsageTx(tx, 10, s.TaskID, line.ID, testAt); return err }); err != nil {
		t.Fatal(err)
	}
	line2 := importLine(t, db, lineFixture("v2", 2, token(90), "0.00414"), bindingFixture()).Line
	proposal := propose(t, db, line2.ID, "initial-read-adjust", false)
	approve(t, db, proposal)
	adjusted := apply(t, db, proposal)
	if adjusted.Application.CompletionTokens == nil || *adjusted.Application.CompletionTokens != 90 {
		t.Fatal("reviewed adjustment fixture failed")
	}
	before := keyUsed(t, db)
	value := initialRead(t, db, initialReadRef(s))
	if relation, err := value.Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: token(100)}); err != nil || relation != InitialSettlementRelationCompatible {
		t.Fatal(relation, err)
	}
	if relation, err := value.Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: token(90)}); err != nil || relation != InitialSettlementRelationConflict {
		t.Fatal(relation, err)
	}
	if !before.Equal(keyUsed(t, db)) || !before.Equal(d("0.00072")) {
		t.Fatal("reader changed adjusted budget")
	}
}

func TestInitialSettlementReadExactFactsAndOpaqueCopy(t *testing.T) {
	db, q := setup(t, "100")
	s := reserve(t, db, q, "task_initial").Snapshot
	settle(t, db, s.TaskID, token(100), "v1")
	ref := initialReadRef(s)
	value := initialRead(t, db, ref)
	for name, fields := range map[string]map[string]any{
		"hold": {"hold_id": s.HoldID + 1}, "snapshot": {"snapshot_id": s.ID + 1},
		"settled_null": {"completion_tokens": nil}, "negative": {"completion_tokens": -1},
		"state_padding": {"state": "settled "}, "reason_padding": {"reason": "actual_usage_settled "},
		"state_unknown": {"state": "adjusted"}, "reason_unknown": {"reason": "unknown"},
		"released_with_zero": {"state": Released, "reason": string(ReleaseProviderNoCharge), "completion_tokens": 0},
	} {
		t.Run(name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&BillingEntry{}).Where("org_id = ? AND task_id = ?", ref.OrgID, ref.TaskID).Updates(fields).Error; err != nil {
					return err
				}
				got, err := ReadInitialSettlementTx(tx, ref)
				if got.Kind() != "" || !errors.Is(err, ErrInvariant) {
					t.Fatalf("expected invariant, got %s %v", got.Kind(), err)
				}
				return ErrInvariant // Restore the legitimate initial entry.
			})
			if !errors.Is(err, ErrInvariant) {
				t.Fatal(err)
			}
		})
	}
	// A returned value owns the token value; subsequent rows or caller pointers
	// cannot mutate the already validated opaque observation.
	if err := db.Model(&BillingEntry{}).Where("org_id = ? AND task_id = ?", ref.OrgID, ref.TaskID).Update("completion_tokens", 90).Error; err != nil {
		t.Fatal(err)
	}
	n := int64(100)
	if relation, err := value.Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: &n}); err != nil || relation != InitialSettlementRelationCompatible {
		t.Fatal(relation, err)
	}
	n = 90
	if relation, err := value.Compare(InitialSettlementExpectation{Kind: InitialSettlementUsageSettled, CompletionTokens: &n}); err != nil || relation != InitialSettlementRelationConflict {
		t.Fatal(relation, err)
	}
}

func TestInitialSettlementReadCallerTransactionAndSafeFailures(t *testing.T) {
	db, q := setup(t, "100")
	s := reserve(t, db, q, "task_initial").Snapshot
	ref := initialReadRef(s)
	if got, err := ReadInitialSettlementTx(db, ref); got.Kind() != "" || !errors.Is(err, ErrTransactionRequired) {
		t.Fatal("nontransaction accepted")
	}
	if got, err := ReadInitialSettlementTx(nil, ref); got.Kind() != "" || !errors.Is(err, ErrTransactionRequired) {
		t.Fatal("nil transaction accepted")
	}
	var tables []string
	var pool gorm.ConnPool
	const callback = "initial_reader_test"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
		if pool != nil && q.Statement.ConnPool != pool {
			t.Error("reader changed transaction connection")
		}
		tables = append(tables, q.Statement.Table)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		pool = tx.Statement.ConnPool
		got, err := ReadInitialSettlementTx(tx.Table("not_a_real_table").Where("unrelated = ?", "private").Select("unknown_column").Limit(0), ref)
		if err != nil || got.Kind() != InitialSettlementAbsent {
			t.Fatal(got.Kind(), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tables, []string{"video_billing_snapshots", "api_keys", "video_billing_snapshots", "video_billing_entries"}) {
		t.Fatal(tables)
	}
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	loud := db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})})
	for stage := 1; stage <= 4; stage++ {
		t.Run(fmt.Sprint(stage), func(t *testing.T) {
			cause := errors.New("synthetic-private-driver-text")
			seen := 0
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
				seen++
				if seen == stage {
					q.AddError(cause)
				}
			}); err != nil {
				t.Fatal(err)
			}
			err := loud.Transaction(func(tx *gorm.DB) error {
				got, err := ReadInitialSettlementTx(tx, ref)
				if got.Kind() != "" || !errors.Is(err, cause) || err.Error() != "video_billing: initial settlement read unavailable" {
					t.Fatal("unsafe or lost driver failure", err)
				}
				return err
			})
			if !errors.Is(err, cause) || seen != stage {
				t.Fatal("did not fail at selected read")
			}
			if err := db.Callback().Query().Remove(callback); err != nil {
				t.Fatal(err)
			}
		})
	}
	joinedCause := errors.New("synthetic-private-mixed-failure")
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "video_billing_entries" {
			q.AddError(errors.Join(gorm.ErrRecordNotFound, joinedCause))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := loud.Transaction(func(tx *gorm.DB) error {
		got, err := ReadInitialSettlementTx(tx, ref)
		if got.Kind() != "" || !errors.Is(err, joinedCause) || err.Error() != "video_billing: initial settlement read unavailable" {
			t.Fatal("mixed failure became absence")
		}
		return err
	}); !errors.Is(err, joinedCause) {
		t.Fatal("mixed failure cause lost")
	}
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatal("reader inherited SQL logger")
	}
	_ = loud.Table("positive_control_missing_table").Find(&[]keyIdentity{}).Error
	if !strings.Contains(logs.String(), "positive_control_missing_table") {
		t.Fatal("Warn logger positive control missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) { cancel() }); err != nil {
		t.Fatal(err)
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		got, err := ReadInitialSettlementTx(tx, ref)
		if got.Kind() != "" || !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost")
		}
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
}

func TestInitialSettlementReadReferenceAndRollback(t *testing.T) {
	db, q := setup(t, "100")
	s := reserve(t, db, q, "task_initial").Snapshot
	ref := initialReadRef(s)
	for _, tc := range []struct {
		ref  InitialSettlementRef
		want error
	}{
		{InitialSettlementRef{}, ErrInvalid},
		{InitialSettlementRef{OrgID: 10, SnapshotID: s.ID, TaskID: s.TaskID, BillingSchemaVersion: "future"}, videoruntime.ErrUnsupported},
		{InitialSettlementRef{OrgID: 20, SnapshotID: s.ID, TaskID: s.TaskID, BillingSchemaVersion: videoruntime.BillingV1}, ErrNotFound},
	} {
		err := db.Transaction(func(tx *gorm.DB) error {
			got, err := ReadInitialSettlementTx(tx, tc.ref)
			if got.Kind() != "" {
				t.Fatal("invalid ref produced facts")
			}
			return err
		})
		if !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
	}
	abort := errors.New("caller rollback")
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: s.TaskID, Now: testAt, Evidence: UsageEvidence{EvidenceID: "same_tx", Source: "task_get", SourceRecordID: s.TaskID, SourceVersion: "v1", CompletionTokens: token(100), ObservedAt: testAt}}); err != nil {
			return err
		}
		got, err := ReadInitialSettlementTx(tx, ref)
		if err != nil || got.Kind() != InitialSettlementUsageSettled {
			t.Fatal("reader missed own transaction write", err)
		}
		return abort
	})
	if !errors.Is(err, abort) || initialRead(t, db, ref).Kind() != InitialSettlementAbsent || count(t, db, "video_billing_entries") != 0 {
		t.Fatal("caller rollback was not preserved")
	}
}
