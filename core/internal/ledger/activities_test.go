package ledger

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const activitySchema = `
CREATE TABLE ledger_operations(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,idempotency_key TEXT NOT NULL,request_hash TEXT NOT NULL,type TEXT NOT NULL,direction TEXT NOT NULL,requested_amount NUMERIC NOT NULL,currency TEXT NOT NULL,status TEXT NOT NULL,ref_type TEXT NOT NULL,ref_id TEXT NOT NULL,failure_code TEXT NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,idempotency_key));
CREATE TABLE ledger_operation_events(id INTEGER PRIMARY KEY AUTOINCREMENT,operation_id INTEGER NOT NULL,status TEXT NOT NULL,code TEXT NOT NULL,created_at DATETIME NOT NULL);
CREATE TABLE ledger_movements(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,account_id INTEGER NOT NULL,account_type TEXT NOT NULL,operation_id INTEGER NOT NULL,type TEXT NOT NULL,direction TEXT NOT NULL,status TEXT NOT NULL,currency TEXT NOT NULL,amount NUMERIC NOT NULL,posted_delta NUMERIC NOT NULL,held_delta NUMERIC NOT NULL,available_delta NUMERIC NOT NULL,journal_id INTEGER,hold_id INTEGER,source_type TEXT NOT NULL,source_id INTEGER NOT NULL,component TEXT NOT NULL,ref_type TEXT NOT NULL,ref_id TEXT NOT NULL,quality TEXT NOT NULL,original_movement_id INTEGER,original_journal_id INTEGER,original_hold_id INTEGER,correction_proposal_id INTEGER,correction_review_id INTEGER,occurred_at DATETIME,created_at DATETIME NOT NULL,UNIQUE(source_type,source_id,component));`

func trackedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if e := db.Exec(activitySchema).Error; e != nil {
		t.Fatal(e)
	}
	db, e := EnableActivityTracking(db)
	if e != nil {
		t.Fatal(e)
	}
	return db
}
func TestActivityAllLedgerPathsAndClones(t *testing.T) {
	db := trackedDB(t).WithContext(context.Background())
	a := seedAccount(t, db, 10, "100")
	var h *Hold
	err := db.Transaction(func(tx *gorm.DB) error {
		if !ActivityTrackingEnabled(tx) {
			t.Fatal("setting lost")
		}
		var e error
		h, _, e = CreateHold(tx, HoldInput{IdempotencyKey: "video:one", AccountID: a.ID, Amount: d("10"), RefType: "video", RefID: "task1"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if e := db.Transaction(func(tx *gorm.DB) error { _, e := Capture(tx, h.ID, d("4"), "test"); return e }); e != nil {
		t.Fatal(e)
	}
	if e := db.Transaction(func(tx *gorm.DB) error {
		other, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "video:two", AccountID: a.ID, Amount: d("5"), RefType: "video", RefID: "task2"})
		if e != nil {
			return e
		}
		return Release(tx, other.ID)
	}); e != nil {
		t.Fatal(e)
	}
	rows, e := ListMovements(db, 10, ActivityFilter{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 6 {
		t.Fatalf("movements=%d %#v", len(rows), rows)
	}
	posted, held := decimal.Zero, decimal.Zero
	freeze, debit, unfreeze := decimal.Zero, decimal.Zero, decimal.Zero
	for _, r := range rows {
		posted = posted.Add(r.PostedDelta)
		held = held.Add(r.HeldDelta)
		if !r.AvailableDelta.Equal(r.PostedDelta.Sub(r.HeldDelta)) {
			t.Fatal("delta identity")
		}
		switch r.Direction {
		case "freeze":
			freeze = freeze.Add(r.Amount)
		case "debit":
			debit = debit.Add(r.Amount)
		case "unfreeze":
			unfreeze = unfreeze.Add(r.Amount)
		}
	}
	if !posted.Equal(d("96")) || !held.IsZero() || !freeze.Equal(d("15")) || !debit.Equal(d("4")) || !unfreeze.Equal(d("11")) {
		t.Fatalf("incorrect financial semantics %s %s %s %s %s", posted, held, freeze, debit, unfreeze)
	}
	drifts, e := CheckDrift(db)
	if e != nil || len(drifts) != 0 {
		t.Fatalf("ledger drift: %#v %v", drifts, e)
	}
	other, e := ListMovements(db, 20, ActivityFilter{Limit: 100})
	if e != nil || len(other) != 0 {
		t.Fatal("tenant leak")
	}
	if _, _, _, e := OperationDetail(db, 20, rows[0].OperationID); e == nil {
		t.Fatal("cross org detail")
	}
	// Replay creates no duplicate movement, even after spending the balance.
	if e := db.Transaction(func(tx *gorm.DB) error {
		_, already, e := CreateHold(tx, HoldInput{IdempotencyKey: "video:one", AccountID: a.ID, Amount: d("10"), RefType: "video", RefID: "task1"})
		if !already {
			t.Fatal("lost idem")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	var n int64
	db.Model(&Movement{}).Count(&n)
	if n != 6 {
		t.Fatal("duplicate movement")
	}
}
func TestActivityFailureAndMissingSchemaFailClosed(t *testing.T) {
	db := setupDB(t)
	if _, e := EnableActivityTracking(db); e == nil {
		t.Fatal("missing schema accepted")
	}
	db = trackedDB(t)
	a := seedAccount(t, db, 10, "2")
	in := OperationIntent{OrgID: 10, IdempotencyKey: "debit-attempt", Type: "admin_debit", Direction: "debit", RequestedAmount: d("3"), RefType: "admin_debit", RefID: "case1"}
	calls := 0
	op, e := RunOperation(db, in, func(tx *gorm.DB) error {
		calls++
		_, _, e := Post(tx, PostInput{IdempotencyKey: "actual-debit", AccountID: a.ID, Kind: KindDebit, Delta: d("-3"), RefType: in.RefType, RefID: in.RefID})
		return e
	})
	if !errors.Is(e, ErrInsufficientBalance) || op.Status != "failed" {
		t.Fatalf("failure=%#v %v", op, e)
	}
	_, e = RunOperation(db, in, func(*gorm.DB) error { calls++; return nil })
	if !errors.Is(e, ErrOperationFailed) || calls != 1 {
		t.Fatal("failed replay executed")
	}
	_, moves, events, e := OperationDetail(db, 10, op.ID)
	if e != nil || len(moves) != 0 || len(events) != 2 {
		t.Fatal("failure fabricated movements", e)
	}
	posted, held, e := OrgBalances(db, 10, AccountUserCredit)
	if e != nil || !posted.Equal(d("2")) || !held.IsZero() {
		t.Fatal("failed mutation survived")
	}
	// After startup schema loss is a hard financial transaction error.
	if e := db.Exec("DROP TABLE ledger_movements").Error; e != nil {
		t.Fatal(e)
	}
	e = db.Transaction(func(tx *gorm.DB) error {
		_, _, e := Post(tx, PostInput{IdempotencyKey: "must-rollback", AccountID: a.ID, Kind: KindCredit, Delta: d("1")})
		return e
	})
	if e == nil {
		t.Fatal("silently skipped movements")
	}
	posted, _, _ = OrgBalances(db, 10, AccountUserCredit)
	if !posted.Equal(d("2")) {
		t.Fatal("journal committed without movement")
	}
}
func TestActivityRunOperationSuccessAndZeroCapture(t *testing.T) {
	db := trackedDB(t)
	a := seedAccount(t, db, 10, "10")
	in := OperationIntent{OrgID: 10, IdempotencyKey: "zero-capture", Type: "reservation_release", Direction: "unfreeze", RequestedAmount: d("2"), RefType: "video", RefID: "zero"}
	op, e := RunOperation(db, in, func(tx *gorm.DB) error {
		h, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "hzero", AccountID: a.ID, Amount: d("2"), RefType: "video", RefID: "zero"})
		if e != nil {
			return e
		}
		_, e = Capture(tx, h.ID, decimal.Zero, "")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	_, moves, events, e := OperationDetail(db, 10, op.ID)
	if e != nil || len(moves) != 2 || len(events) != 2 {
		t.Fatalf("%v %#v %#v", e, moves, events)
	}
	for _, m := range moves {
		if m.Direction == "debit" || m.JournalID != nil {
			t.Fatal("zero capture fabricated charge")
		}
	}
	_, e = RunOperation(db, in, func(*gorm.DB) error { t.Fatal("success replay executed"); return nil })
	if e != nil {
		t.Fatal(e)
	}
	in.RequestedAmount = d("3")
	if _, e = RunOperation(db, in, func(*gorm.DB) error { return nil }); !errors.Is(e, ErrOperationConflict) {
		t.Fatal("body mismatch accepted")
	}
}

func TestActivityBackfillHonestTimeAndIdempotency(t *testing.T) {
	db := setupDB(t)
	a := seedAccount(t, db, 10, "20")
	if e := db.Transaction(func(tx *gorm.DB) error {
		h, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "oldhold", AccountID: a.ID, Amount: d("10"), RefType: "video", RefID: "oldtask"})
		if e != nil {
			return e
		}
		_, e = Capture(tx, h.ID, d("4"), "")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e := db.Exec(activitySchema).Error; e != nil {
		t.Fatal(e)
	}
	db, e := EnableActivityTracking(db)
	if e != nil {
		t.Fatal(e)
	}
	cutoff := time.Now().UTC()
	n, e := BackfillActivities(db, cutoff, 100)
	if e != nil || n != 4 {
		t.Fatalf("backfill %d %v", n, e)
	}
	n, e = BackfillActivities(db, cutoff, 100)
	if e != nil || n != 0 {
		t.Fatal("backfill duplicated", n, e)
	}
	rows, e := ListMovements(db, 10, ActivityFilter{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	var posted, held decimal.Decimal
	for _, m := range rows {
		posted = posted.Add(m.PostedDelta)
		held = held.Add(m.HeldDelta)
		if m.Direction == "unfreeze" && (m.OccurredAt != nil || m.Quality != "derived_time_unknown") {
			t.Fatal("invented historical release time")
		}
	}
	if !posted.Equal(d("16")) || !held.IsZero() {
		t.Fatal("historical deltas do not reconcile")
	}
	from := time.Now().UTC().Add(-time.Hour)
	rows, _ = ListMovements(db, 10, ActivityFilter{Limit: 100, Direction: "unfreeze", From: &from})
	if len(rows) != 0 {
		t.Fatal("unknown historical release counted today")
	}
	if ds, e := CheckDrift(db); e != nil || len(ds) != 0 {
		t.Fatal("backfill mutated money", e)
	}
}
func TestActivityOverrideBindsTargetAndResolvesDirection(t *testing.T) {
	db := trackedDB(t)
	a := seedAccount(t, db, 10, "10")
	in := OperationIntent{OrgID: 10, IdempotencyKey: "override", Type: "admin_adjustment", RefType: "admin_override", RefID: "10", RequestFingerprint: strings.Repeat("a", 64)}
	op, e := RunOperation(db, in, func(tx *gorm.DB) error {
		_, _, e := Post(tx, PostInput{IdempotencyKey: "override-post", AccountID: a.ID, Kind: KindDebit, Delta: d("-2"), RefType: "admin_override", RefID: "10"})
		return e
	})
	if e != nil || op.Direction != "debit" || !op.RequestedAmount.Equal(d("2")) {
		t.Fatalf("actual direction missing: %#v %v", op, e)
	}
	in.RequestFingerprint = strings.Repeat("b", 64)
	if _, e = RunOperation(db, in, func(*gorm.DB) error { t.Fatal("changed target executed"); return nil }); !errors.Is(e, ErrOperationConflict) {
		t.Fatal("changed target not rejected", e)
	}
}

func TestActivityDBClonesDoNotLeakScopes(t *testing.T) {
	db := trackedDB(t)
	seedAccount(t, db, 10, "10")
	seedAccount(t, db, 20, "20")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			org := uint64(10)
			if i%2 != 0 {
				org = 20
			}
			q := db.WithContext(context.Background())
			var a Account
			if e := q.Where("org_id = ? AND type = ?", org, AccountUserCredit).Take(&a).Error; e != nil || a.OrgID != org {
				t.Errorf("account clone scope %d: %v", org, e)
			}
			var n int64
			if e := q.Model(&Movement{}).Where("org_id = ?", org).Count(&n).Error; e != nil || n != 1 {
				t.Errorf("movement clone scope %d %d: %v", org, n, e)
			}
			if _, ok := q.Get("ledger:operation_id"); ok {
				t.Error("operation context leaked")
			}
		}(i)
	}
	wg.Wait()
	var n int64
	if e := db.Model(&Account{}).Count(&n).Error; e != nil || n != 2 {
		t.Fatalf("shared DB polluted %d %v", n, e)
	}
}

func TestActivityOccurrencePaginationWithLateBackfill(t *testing.T) {
	db := trackedDB(t)
	seedAccount(t, db, 10, "10")
	seedAccount(t, db, 20, "10")
	var account Account
	db.Where("org_id = ?", 10).Take(&account)
	old := time.Now().UTC().Add(-24 * time.Hour)
	older := old.Add(-time.Hour)
	for i, occurred := range []*time.Time{&old, nil, &older, &old} {
		if e := db.Transaction(func(tx *gorm.DB) error {
			return recordMovement(tx, Movement{AccountID: account.ID, Direction: "credit", Amount: d("1"), PostedDelta: d("1"), SourceType: "historical-fixture", SourceID: uint64(i + 100), Component: "posted", RefType: "test", RefID: "history", Quality: "historical_fixture", OccurredAt: occurred})
		}); e != nil {
			t.Fatal(e)
		}
	}
	seen := map[uint64]bool{}
	cursor := uint64(0)
	lastTime := time.Now().UTC()
	unknownSeen := false
	for {
		rows, e := ListMovements(db, 10, ActivityFilter{Limit: 2, Before: cursor})
		if e != nil {
			t.Fatal(e)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			if seen[r.ID] {
				t.Fatal("pagination duplicated row")
			}
			seen[r.ID] = true
			if r.OccurredAt == nil {
				unknownSeen = true
			} else {
				if unknownSeen || r.OccurredAt.After(lastTime) {
					t.Fatal("historical chronology incorrect")
				}
				lastTime = *r.OccurredAt
			}
		}
		cursor = rows[len(rows)-1].ID
	}
	if len(seen) != 5 {
		t.Fatalf("lost rows %d", len(seen))
	}
	var foreign Movement
	db.Where("org_id = ?", 20).Take(&foreign)
	if _, e := ListMovements(db, 10, ActivityFilter{Limit: 2, Before: foreign.ID}); e == nil {
		t.Fatal("foreign cursor accepted")
	}
}
