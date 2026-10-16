package videobilling

import (
	"github.com/laiai/laiai-next/core/internal/ledger"
	"testing"
)

func TestReviewedAdjustmentMovementProvenance(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	if e := db.Exec(adjustmentActivitySchema).Error; e != nil {
		t.Fatal(e)
	}
	var e error
	db, e = ledger.EnableActivityTracking(db)
	if e != nil {
		t.Fatal(e)
	}
	reserve(t, db, q, "task_1")
	settle(t, db, "task_1", token(54000), "v1")
	line := importLine(t, db, lineFixture("v1", 1, token(27000), "1.242"), bindingFixture())
	p := propose(t, db, line.Line.ID, "p1", false)
	approve(t, db, p)
	first := apply(t, db, p)
	if !first.Application.CustomerDeltaUSD.Equal(d("-0.216")) {
		t.Fatal("unexpected refund")
	}
	rows, e := ledger.ListMovements(db, 10, ledger.ActivityFilter{Limit: 100, Status: "reversed"})
	if e != nil || len(rows) != 1 {
		t.Fatalf("reversed %v %#v", e, rows)
	}
	r := rows[0]
	if r.Direction != "credit" || r.OriginalMovementID == nil || r.OriginalJournalID == nil || r.OriginalHoldID == nil || r.CorrectionProposalID == nil || *r.CorrectionProposalID != p.ID || r.CorrectionReviewID == nil {
		t.Fatalf("missing correction evidence %#v", r)
	}
	var original ledger.Movement
	if e = db.First(&original, *r.OriginalMovementID).Error; e != nil || original.Status != "success" || !original.Amount.Equal(d("0.432")) {
		t.Fatal("original mutated", e)
	}
	apply(t, db, p)
	rows, _ = ledger.ListMovements(db, 10, ledger.ActivityFilter{Limit: 100, Status: "reversed"})
	if len(rows) != 1 {
		t.Fatal("replay duplicated corrective posting")
	}
	line = importLine(t, db, lineFixture("v2", 2, token(40000), "1.84"), bindingFixture())
	p = propose(t, db, line.Line.ID, "p2", false)
	approve(t, db, p)
	apply(t, db, p)
	rows, _ = ledger.ListMovements(db, 10, ledger.ActivityFilter{Limit: 100, Status: "reversed", Direction: "debit"})
	if len(rows) != 1 || rows[0].OriginalJournalID == nil {
		t.Fatal("reviewed additional charge not linked")
	}
	checkBalance(t, db, "9.68", "0")
}

const adjustmentActivitySchema = `
CREATE TABLE ledger_operations(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,idempotency_key TEXT NOT NULL,request_hash TEXT NOT NULL,type TEXT NOT NULL,direction TEXT NOT NULL,requested_amount NUMERIC NOT NULL,currency TEXT NOT NULL,status TEXT NOT NULL,ref_type TEXT NOT NULL,ref_id TEXT NOT NULL,failure_code TEXT NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,idempotency_key));
CREATE TABLE ledger_operation_events(id INTEGER PRIMARY KEY AUTOINCREMENT,operation_id INTEGER NOT NULL,status TEXT NOT NULL,code TEXT NOT NULL,created_at DATETIME NOT NULL);
CREATE TABLE ledger_movements(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,account_id INTEGER NOT NULL,account_type TEXT NOT NULL,operation_id INTEGER NOT NULL,type TEXT NOT NULL,direction TEXT NOT NULL,status TEXT NOT NULL,currency TEXT NOT NULL,amount NUMERIC NOT NULL,posted_delta NUMERIC NOT NULL,held_delta NUMERIC NOT NULL,available_delta NUMERIC NOT NULL,journal_id INTEGER,hold_id INTEGER,source_type TEXT NOT NULL,source_id INTEGER NOT NULL,component TEXT NOT NULL,ref_type TEXT NOT NULL,ref_id TEXT NOT NULL,quality TEXT NOT NULL,original_movement_id INTEGER,original_journal_id INTEGER,original_hold_id INTEGER,correction_proposal_id INTEGER,correction_review_id INTEGER,occurred_at DATETIME,created_at DATETIME NOT NULL,UNIQUE(source_type,source_id,component));`
