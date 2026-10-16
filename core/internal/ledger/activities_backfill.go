package ledger

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// BackfillActivities is a bounded operator job. It projects existing immutable
// sources and NEVER touches balances or journal. Live/backfill share source
// uniqueness; each source is transactional and safely retryable. Release time
// cannot be reconstructed from a hold's current state, so it stays NULL.
func BackfillActivities(db *gorm.DB, cutoff time.Time, limit int) (int, error) {
	if !ActivityTrackingEnabled(db) || cutoff.IsZero() || cutoff.After(time.Now().UTC()) || limit < 1 || limit > 500 {
		return 0, ErrInvalidInput
	}
	count := 0
	type journalSource struct {
		JournalEntry
		SourceCreatedAt *time.Time
	}
	var js []journalSource
	if e := db.Table("ledger_journal j").Select("j.*, j.created_at AS source_created_at").Where("j.created_at <= ? AND NOT EXISTS (SELECT 1 FROM ledger_movements m WHERE m.source_type = 'journal' AND m.source_id = j.id AND m.component = 'posted')", cutoff).Order("j.id").Limit(limit).Scan(&js).Error; e != nil {
		return count, e
	}
	for _, src := range js {
		e := db.Transaction(func(tx *gorm.DB) error {
			j := src.JournalEntry
			m := Movement{AccountID: j.AccountID, Amount: j.Delta.Abs(), PostedDelta: j.Delta, JournalID: &j.ID, SourceType: "journal", SourceID: j.ID, Component: "posted", RefType: j.RefType, RefID: j.RefID, Quality: "historical_journal", OccurredAt: src.SourceCreatedAt, Direction: "credit"}
			if j.Delta.IsNegative() {
				m.Direction = "debit"
			}
			if j.Kind == KindCapture && j.HoldID != nil {
				m.HoldID = j.HoldID
				m.HeldDelta = j.Delta
			}
			return recordMovement(tx, m)
		})
		if e != nil && !isDuplicate(e) {
			return count, e
		}
		if e == nil {
			count++
		}
	}
	if count >= limit {
		return count, nil
	}
	type holdSource struct {
		Hold
		SourceCreatedAt *time.Time
	}
	var hs []holdSource
	if e := db.Table("ledger_holds h").Select("h.*, h.created_at AS source_created_at").Where("h.created_at <= ? AND NOT EXISTS (SELECT 1 FROM ledger_movements m WHERE m.source_type = 'hold' AND m.source_id = h.id AND m.component = 'freeze')", cutoff).Order("h.id").Limit(limit - count).Scan(&hs).Error; e != nil {
		return count, e
	}
	for _, src := range hs {
		h := src.Hold
		e := db.Transaction(func(tx *gorm.DB) error {
			return recordMovement(tx, Movement{AccountID: h.AccountID, Direction: "freeze", Amount: h.Amount, HeldDelta: h.Amount, HoldID: &h.ID, SourceType: "hold", SourceID: h.ID, Component: "freeze", RefType: h.RefType, RefID: h.RefID, Quality: "historical_hold", OccurredAt: src.SourceCreatedAt})
		})
		if e != nil && !isDuplicate(e) {
			return count, e
		}
		if e == nil {
			count++
		}
	}
	if count >= limit {
		return count, nil
	}
	hs = nil
	if e := db.Table("ledger_holds h").Select("h.*").Where("h.created_at <= ? AND h.status <> ? AND NOT EXISTS (SELECT 1 FROM ledger_movements m WHERE m.source_type = 'hold' AND m.source_id = h.id AND m.component = 'release')", cutoff, HoldActive).Where("h.amount > h.captured_amount").Order("h.id").Limit(limit - count).Scan(&hs).Error; e != nil {
		return count, e
	}
	for _, src := range hs {
		h := src.Hold
		released := h.Amount
		if h.Status == HoldCaptured {
			released = h.Amount.Sub(h.CapturedAmount)
		}
		if !released.IsPositive() || h.CapturedAmount.IsNegative() || h.CapturedAmount.GreaterThan(h.Amount) {
			return count, fmt.Errorf("%w: historical hold invariant", ErrInvalidInput)
		}
		e := db.Transaction(func(tx *gorm.DB) error {
			return recordMovement(tx, Movement{AccountID: h.AccountID, Direction: "unfreeze", Amount: released, PostedDelta: decimal.Zero, HeldDelta: released.Neg(), HoldID: &h.ID, SourceType: "hold", SourceID: h.ID, Component: "release", RefType: h.RefType, RefID: h.RefID, Quality: "derived_time_unknown"})
		})
		if e != nil && !isDuplicate(e) {
			return count, e
		}
		if e == nil {
			count++
		}
	}
	return count, nil
}
