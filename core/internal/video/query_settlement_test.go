package video

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"gorm.io/gorm"
)

func settlementBarrierDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := authorityClockUnitDB(t, &bytes.Buffer{})
	must(t, db.Exec(`CREATE TABLE video_query_receipts (
attempt_id TEXT PRIMARY KEY, org_id INTEGER, task_id TEXT, sequence INTEGER, task_row_version INTEGER,
runtime_version TEXT, adapter_version TEXT, protocol_contract_version TEXT, observation_schema_version INTEGER,
http_status INTEGER,outcome TEXT,provider_request_id TEXT,observation_json TEXT,has_video INTEGER,has_last_frame INTEGER,
payload_ref TEXT,received_at DATETIME,applied_at DATETIME,apply_error_code TEXT,recovery_due_at DATETIME,recovery_version INTEGER,
generation_applied_at DATETIME,settlement_kind TEXT,settlement_evidence_sha256 TEXT)`).Error)
	return db
}

func TestQueryFinancialBarrierPreservesPinProgress(t *testing.T) {
	for _, form := range []string{"pending", "review", "applied", "already_closed"} {
		t.Run(form, func(t *testing.T) {
			db := settlementBarrierDB(t)
			_, st, pin := querySettlementPending(t)
			if form == "review" {
				pin.RecoveryDueAt = nil
				pin.ApplyErrorCode = "query_apply_failed"
			}
			if form == "applied" {
				at := pin.ReceivedAt
				pin.AppliedAt = &at
			}
			if form == "already_closed" {
				pin.ApplyErrorCode = queryFinancialConflictCode
				pin.RecoveryDueAt = nil
				pin.RuntimeVersion = "damaged"
			}
			current := pin
			current.AttemptID = "new-conflicting-receipt"
			current.RuntimeVersion = st.RuntimeVersion
			current.AppliedAt, current.GenerationAppliedAt = nil, nil
			current.SettlementKind, current.SettlementEvidenceSHA256, current.ApplyErrorCode = "", "", ""
			due := current.ReceivedAt
			current.RecoveryDueAt = &due
			must(t, db.Create(&pin).Error)
			must(t, db.Create(&current).Error)
			var result queryRecoveryResult
			must(t, db.Transaction(func(tx *gorm.DB) error {
				var e error
				result, e = markQueryFinancialConflictTx(tx, st, current, current.ReceivedAt)
				return e
			}))
			if result.disposition != queryRecoveryReview || result.code != queryFinancialConflictCode {
				t.Fatal("new closed marker did not report committed review")
			}
			for _, before := range []QueryReceipt{current, pin} {
				var after QueryReceipt
				must(t, db.Where("attempt_id = ?", before.AttemptID).Take(&after).Error)
				want := before
				if before.ApplyErrorCode != queryFinancialConflictCode {
					want.ApplyErrorCode = queryFinancialConflictCode
					want.RecoveryVersion++
					if before.AppliedAt == nil {
						want.RecoveryDueAt = nil
					}
				}
				if !reflect.DeepEqual(after, want) {
					t.Fatal("barrier rewrote evidence or completed/NULL progress")
				}
			}
			var closed QueryReceipt
			must(t, db.Where("attempt_id = ?", current.AttemptID).Take(&closed).Error)
			must(t, db.Transaction(func(tx *gorm.DB) error {
				var e error
				result, e = markQueryFinancialConflictTx(tx, st, closed, current.ReceivedAt)
				return e
			}))
			if result.disposition != queryRecoveryNoop {
				t.Fatal("replayed barrier counted a second review")
			}
		})
	}
}

func TestQueryFinancialBarrierCASAndRollback(t *testing.T) {
	for _, bad := range []string{"stale_version", "stale_phase", "pin_identity", "overflow", "cancelled", "rollback"} {
		t.Run(bad, func(t *testing.T) {
			db := settlementBarrierDB(t)
			_, st, current := querySettlementPending(t)
			// SQLite signed storage cannot represent uint64 max. The overflow
			// guard is exercised with the captured value, before any UPDATE.
			must(t, db.Create(&current).Error)
			before := current
			requested := current
			want := ErrConflict
			switch bad {
			case "stale_version":
				requested.RecoveryVersion++
			case "stale_phase":
				requested.SettlementEvidenceSHA256 = "stale"
			case "pin_identity":
				p := "missing "
				st.SettlementReceiptID = &p
				want = gorm.ErrRecordNotFound
			case "overflow":
				requested.RecoveryVersion = math.MaxUint64
				want = ErrState
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if bad == "cancelled" {
				cancel()
				want = context.Canceled
			}
			rollback := errors.New("synthetic rollback")
			if bad == "rollback" {
				want = rollback
			}
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				_, e := markQueryFinancialConflictTx(tx, st, requested, current.ReceivedAt)
				if e == nil && bad == "rollback" {
					return rollback
				}
				return e
			})
			if !errors.Is(err, want) {
				t.Fatalf("wrong guard error type %T", err)
			}
			var after QueryReceipt
			must(t, db.Where("attempt_id = ?", current.AttemptID).Take(&after).Error)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed conflict CAS changed durable progress")
			}
		})
	}
}

func TestQuerySettlementRoundRejectsNonPrimaryAndCancellation(t *testing.T) {
	db := settlementBarrierDB(t)
	s := &Service{DB: db}
	if err := s.ProcessSettlementOnce(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("SQLite became a worker clock authority")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.ProcessSettlementOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("round lost cancellation cause")
	}
	obs := s.metrics().runtimeSnapshot()
	for _, cycle := range obs.Cycles {
		if cycle.Kind == "settlement" && (cycle.Started != 2 || cycle.Completed != 2 || cycle.Errors != 2) {
			t.Fatal("settlement cycle double counted or lost failure")
		}
	}
}
