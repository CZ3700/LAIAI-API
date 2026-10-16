package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Operation records an attempt, not a balance change. Failed/processing attempts
// have no committed movements. Actual effects are read only from Movement.
type Operation struct {
	ID              uint64          `gorm:"primaryKey" json:"id"`
	OrgID           uint64          `json:"org_id"`
	IdempotencyKey  string          `json:"-"`
	RequestHash     string          `json:"-"`
	Type            string          `json:"type"`
	Direction       string          `json:"direction"`
	RequestedAmount decimal.Decimal `json:"requested_amount"`
	Currency        string          `json:"currency"`
	Status          string          `json:"status"`
	RefType         string          `json:"ref_type"`
	RefID           string          `json:"ref_id"`
	FailureCode     string          `json:"failure_code,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func (Operation) TableName() string { return "ledger_operations" }

type OperationEvent struct {
	ID          uint64    `gorm:"primaryKey" json:"id"`
	OperationID uint64    `json:"operation_id"`
	Status      string    `json:"status"`
	Code        string    `json:"code,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (OperationEvent) TableName() string { return "ledger_operation_events" }

type Movement struct {
	ID                   uint64          `gorm:"primaryKey" json:"id"`
	OrgID                uint64          `json:"org_id"`
	AccountID            uint64          `json:"account_id"`
	AccountType          string          `json:"account_type"`
	OperationID          uint64          `json:"operation_id"`
	Type                 string          `json:"type"`
	Direction            string          `json:"direction"`
	Status               string          `json:"status"`
	Currency             string          `json:"currency"`
	Amount               decimal.Decimal `json:"amount"`
	PostedDelta          decimal.Decimal `json:"posted_delta"`
	HeldDelta            decimal.Decimal `json:"held_delta"`
	AvailableDelta       decimal.Decimal `json:"available_delta"`
	JournalID            *uint64         `json:"journal_id,omitempty"`
	HoldID               *uint64         `json:"hold_id,omitempty"`
	SourceType           string          `json:"source_type"`
	SourceID             uint64          `json:"source_id"`
	Component            string          `json:"component"`
	RefType              string          `json:"ref_type"`
	RefID                string          `json:"ref_id"`
	Quality              string          `json:"quality"`
	OriginalMovementID   *uint64         `json:"original_movement_id,omitempty"`
	OriginalJournalID    *uint64         `json:"original_journal_id,omitempty"`
	OriginalHoldID       *uint64         `json:"original_hold_id,omitempty"`
	CorrectionProposalID *uint64         `json:"correction_proposal_id,omitempty"`
	CorrectionReviewID   *uint64         `json:"correction_review_id,omitempty"`
	OccurredAt           *time.Time      `json:"occurred_at"`
	CreatedAt            time.Time       `json:"created_at"`
}

func (Movement) TableName() string { return "ledger_movements" }

// EnableActivityTracking is mandatory at production startup before sharing db
// with services. Missing migrations fail startup; enabled writes never fall back.
func EnableActivityTracking(db *gorm.DB) (*gorm.DB, error) {
	for _, table := range []string{"ledger_operations", "ledger_operation_events", "ledger_movements"} {
		if !db.Migrator().HasTable(table) {
			return nil, fmt.Errorf("ledger activity schema missing: %s", table)
		}
	}
	return db.Set("ledger:activity_tracking", true).Session(&gorm.Session{}), nil
}
func ActivityTrackingEnabled(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	v, ok := db.Get("ledger:activity_tracking")
	return ok && v == true
}

type OperationIntent struct {
	OrgID                                           uint64
	IdempotencyKey, Type, Direction, RefType, RefID string
	RequestedAmount                                 decimal.Decimal
	RequestFingerprint                              string // optional SHA-256 of canonical business input, never raw PII
}

var ErrOperationPending = errors.New("ledger: operation pending reconciliation")
var ErrOperationConflict = errors.New("ledger: operation idempotency conflict")
var ErrOperationFailed = errors.New("ledger: previous operation failed")

// RunOperation first durably records intent. Success and its movements commit
// together. Only a confirmed rollback becomes failed; commit uncertainty stays
// processing and is never automatically retried. fn must contain DB work only.
func RunOperation(db *gorm.DB, in OperationIntent, fn func(*gorm.DB) error) (*Operation, error) {
	if in.RequestFingerprint != "" {
		raw, e := hex.DecodeString(in.RequestFingerprint)
		if e != nil || len(raw) != 32 {
			return nil, ErrInvalidInput
		}
	}
	if !ActivityTrackingEnabled(db) || in.OrgID == 0 || len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 160 || in.Type == "" || len(in.Type) > 64 || (!validDirection(in.Direction) && !(in.Type == "admin_adjustment" && in.Direction == "" && in.RequestedAmount.IsZero())) || in.RequestedAmount.IsNegative() || !in.RequestedAmount.Equal(in.RequestedAmount.Truncate(8)) || in.RequestedAmount.GreaterThan(decimal.New(999999999999, 0)) || len(in.RefType) > 64 || len(in.RefID) > 160 || fn == nil {
		return nil, ErrInvalidInput
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	now := time.Now().UTC()
	op := Operation{OrgID: in.OrgID, IdempotencyKey: in.IdempotencyKey, RequestHash: hash, Type: in.Type, Direction: in.Direction, RequestedAmount: in.RequestedAmount, Currency: "USD", Status: "processing", RefType: in.RefType, RefID: in.RefID, CreatedAt: now, UpdatedAt: now}
	err := db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&op).Error; e != nil {
			return e
		}
		return tx.Create(&OperationEvent{OperationID: op.ID, Status: "processing", CreatedAt: now}).Error
	})
	if err != nil {
		if !isDuplicate(err) {
			return nil, err
		}
		if e := db.Where("org_id = ? AND idempotency_key = ?", in.OrgID, in.IdempotencyKey).Take(&op).Error; e != nil {
			return nil, e
		}
		if op.RequestHash != hash {
			return &op, ErrOperationConflict
		}
		switch op.Status {
		case "success":
			return &op, nil
		case "failed":
			return &op, ErrOperationFailed
		default:
			return &op, ErrOperationPending
		}
	}
	tx := db.Session(&gorm.Session{}).Set("ledger:operation_id", op.ID).Begin()
	if tx.Error != nil {
		return &op, tx.Error
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback().Error
			panic(p)
		}
	}()
	err = fn(tx)
	if err == nil {
		err = setOperationStatus(tx, &op, "success", "")
	}
	if err == nil {
		err = tx.Where("id = ? AND org_id = ?", op.ID, op.OrgID).Take(&op).Error
	}
	if err != nil {
		if re := tx.Rollback().Error; re != nil {
			return &op, fmt.Errorf("%w: rollback unconfirmed", ErrOperationPending)
		}
		code := "rejected"
		if errors.Is(err, ErrInsufficientBalance) {
			code = "insufficient_balance"
		}
		if e := db.Transaction(func(t *gorm.DB) error { return setOperationStatus(t, &op, "failed", code) }); e != nil {
			return &op, fmt.Errorf("%w: failure recording unavailable", ErrOperationPending)
		}
		return &op, err
	}
	if e := tx.Commit().Error; e != nil {
		op.Status = "processing"
		return &op, fmt.Errorf("%w: commit outcome unknown", ErrOperationPending)
	}
	return &op, nil
}
func setOperationStatus(tx *gorm.DB, op *Operation, status, code string) error {
	now := time.Now().UTC()
	r := tx.Model(&Operation{}).Where("id = ? AND org_id = ? AND status = ?", op.ID, op.OrgID, "processing").Updates(map[string]any{"status": status, "failure_code": code, "updated_at": now})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrOperationConflict
	}
	if e := tx.Create(&OperationEvent{OperationID: op.ID, Status: status, Code: code, CreatedAt: now}).Error; e != nil {
		return e
	}
	op.Status, op.FailureCode, op.UpdatedAt = status, code, now
	return nil
}
func validDirection(s string) bool {
	return s == "credit" || s == "debit" || s == "freeze" || s == "unfreeze"
}
func movementType(ref, direction string) string {
	switch ref {
	case "topup":
		return "topup"
	case "topup_bonus", "welcome_grant", "invite_grant":
		return "grant"
	case "admin_grant":
		return "admin_credit"
	case "admin_debit":
		return "admin_debit"
	case "admin_override":
		return "admin_adjustment"
	case "video_adjustment":
		return "video_adjustment"
	case "rebalance":
		return "internal_transfer"
	}
	if direction == "freeze" {
		return "reservation"
	}
	if direction == "unfreeze" {
		return "reservation_release"
	}
	if direction == "debit" {
		return "usage"
	}
	return "other_credit"
}
func recordMovement(tx *gorm.DB, m Movement) error {
	if !ActivityTrackingEnabled(tx) {
		return nil
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return errors.New("ledger: activity requires transaction")
	}
	var account Account
	if e := tx.Take(&account, m.AccountID).Error; e != nil {
		return e
	}
	m.OrgID, m.AccountType = account.OrgID, account.Type
	m.AvailableDelta = m.PostedDelta.Sub(m.HeldDelta)
	m.Type = movementType(m.RefType, m.Direction)
	m.Currency = "USD"
	m.Status = "success"
	m.CreatedAt = time.Now().UTC()
	if m.Quality == "" {
		m.Quality = "live"
	}
	if m.Quality == "live" {
		m.OccurredAt = &m.CreatedAt
	}
	if m.CorrectionProposalID != nil {
		m.Status = "reversed"
	}
	if value, ok := tx.Get("ledger:operation_id"); ok && account.Type == AccountUserCredit {
		id, _ := value.(uint64)
		var op Operation
		if e := tx.Where("id = ? AND org_id = ? AND status = ?", id, m.OrgID, "processing").Take(&op).Error; e != nil {
			return e
		}
		if op.Type == "admin_adjustment" && op.Direction == "" {
			if e := tx.Model(&Operation{}).Where("id = ? AND org_id = ? AND status = ? AND direction = ?", id, m.OrgID, "processing", "").Updates(map[string]any{"direction": m.Direction, "requested_amount": m.Amount}).Error; e != nil {
				return e
			}
		}
		m.OperationID = id
	} else {
		op := Operation{OrgID: m.OrgID, IdempotencyKey: fmt.Sprintf("source:%s:%d:%s", m.SourceType, m.SourceID, m.Component), Type: m.Type, Direction: m.Direction, RequestedAmount: m.Amount, Currency: "USD", Status: m.Status, RefType: m.RefType, RefID: m.RefID, CreatedAt: m.CreatedAt, UpdatedAt: m.CreatedAt}
		if account.Type != AccountUserCredit {
			op.Type = "internal_transfer"
		}
		if e := tx.Create(&op).Error; e != nil {
			return e
		}
		m.OperationID = op.ID
		if e := tx.Create(&OperationEvent{OperationID: op.ID, Status: m.Status, Code: "committed_source", CreatedAt: m.CreatedAt}).Error; e != nil {
			return e
		}
	}
	return tx.Create(&m).Error
}

// CorrectionLink is supplied only after the caller's two-person review has
// passed in this same transaction. A corrective entry never rewrites history.
type CorrectionLink struct{ ProposalID, ReviewID, HoldID uint64 }

func recordPost(tx *gorm.DB, e *JournalEntry, correction *CorrectionLink) error {
	direction := "credit"
	if e.Delta.IsNegative() {
		direction = "debit"
	}
	m := Movement{AccountID: e.AccountID, Direction: direction, Amount: e.Delta.Abs(), PostedDelta: e.Delta, JournalID: &e.ID, SourceType: "journal", SourceID: e.ID, Component: "posted", RefType: e.RefType, RefID: e.RefID}
	if correction != nil && ActivityTrackingEnabled(tx) {
		if correction.ProposalID == 0 || correction.ReviewID == 0 || correction.HoldID == 0 || e.RefType != "video_adjustment" {
			return ErrInvalidInput
		}
		var hold Hold
		if err := tx.Where("id = ? AND account_id = ? AND ref_id = ?", correction.HoldID, e.AccountID, e.RefID).Take(&hold).Error; err != nil {
			return err
		}
		m.OriginalHoldID = &hold.ID
		m.CorrectionProposalID = &correction.ProposalID
		m.CorrectionReviewID = &correction.ReviewID
		var original JournalEntry
		err := tx.Where("hold_id = ? AND account_id = ? AND kind = ?", hold.ID, e.AccountID, KindCapture).Take(&original).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var movement Movement
		q := tx.Where("account_id = ?", e.AccountID)
		if err == nil {
			m.OriginalJournalID = &original.ID
			q = q.Where("source_type = ? AND source_id = ? AND component = ?", "journal", original.ID, "posted")
		} else {
			q = q.Where("source_type = ? AND source_id = ? AND component = ?", "hold", hold.ID, "freeze")
		}
		if err = q.Take(&movement).Error; err == nil {
			m.OriginalMovementID = &movement.ID
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return recordMovement(tx, m)
}
func recordHold(tx *gorm.DB, h *Hold) error {
	return recordMovement(tx, Movement{AccountID: h.AccountID, Direction: "freeze", Amount: h.Amount, HeldDelta: h.Amount, HoldID: &h.ID, SourceType: "hold", SourceID: h.ID, Component: "freeze", RefType: h.RefType, RefID: h.RefID})
}
func recordRelease(tx *gorm.DB, h *Hold, amount decimal.Decimal, component string) error {
	if amount.IsZero() {
		return nil
	}
	return recordMovement(tx, Movement{AccountID: h.AccountID, Direction: "unfreeze", Amount: amount, HeldDelta: amount.Neg(), HoldID: &h.ID, SourceType: "hold", SourceID: h.ID, Component: component, RefType: h.RefType, RefID: h.RefID})
}
func recordCapture(tx *gorm.DB, h *Hold, actual decimal.Decimal, e *JournalEntry) error {
	if actual.IsPositive() {
		if err := recordMovement(tx, Movement{AccountID: h.AccountID, Direction: "debit", Amount: actual, PostedDelta: actual.Neg(), HeldDelta: actual.Neg(), JournalID: &e.ID, HoldID: &h.ID, SourceType: "journal", SourceID: e.ID, Component: "posted", RefType: h.RefType, RefID: h.RefID}); err != nil {
			return err
		}
	}
	return recordRelease(tx, h, h.Amount.Sub(actual), "release")
}

type ActivityFilter struct {
	Before                         uint64
	Limit                          int
	Type, Direction, Status, RefID string
	From, Until                    *time.Time
}

func filterActivities(q *gorm.DB, f ActivityFilter, timeColumn string) *gorm.DB {
	if f.Before > 0 {
		q = q.Where("id < ?", f.Before)
	}
	if f.Type != "" {
		q = q.Where("type = ?", f.Type)
	}
	if f.Direction != "" {
		q = q.Where("direction = ?", f.Direction)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.RefID != "" {
		q = q.Where("ref_id = ?", f.RefID)
	}
	if f.From != nil {
		q = q.Where(timeColumn+" >= ?", *f.From)
	}
	if f.Until != nil {
		q = q.Where(timeColumn+" < ?", *f.Until)
	}
	return q
}
func ListMovements(db *gorm.DB, org uint64, f ActivityFilter) ([]Movement, error) {
	if org == 0 || f.Limit < 1 || f.Limit > 1000 {
		return nil, ErrInvalidInput
	}
	rows := []Movement{}
	e := filterActivities(db.Where("org_id = ? AND account_type = ? AND type <> ?", org, AccountUserCredit, "internal_transfer"), f, "occurred_at").Order("id DESC").Limit(f.Limit).Find(&rows).Error
	return rows, e
}
func ListOperations(db *gorm.DB, org uint64, f ActivityFilter) ([]Operation, error) {
	if org == 0 || f.Limit < 1 || f.Limit > 1000 {
		return nil, ErrInvalidInput
	}
	rows := []Operation{}
	e := filterActivities(db.Where("org_id = ? AND type <> ?", org, "internal_transfer"), f, "created_at").Order("id DESC").Limit(f.Limit).Find(&rows).Error
	return rows, e
}
func OperationDetail(db *gorm.DB, org, id uint64) (Operation, []Movement, []OperationEvent, error) {
	var op Operation
	rows := []Movement{}
	events := []OperationEvent{}
	e := db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Where("id = ? AND org_id = ? AND type <> ?", id, org, "internal_transfer").Take(&op).Error; e != nil {
			return e
		}
		if e := tx.Where("operation_id = ? AND org_id = ?", id, org).Order("id").Find(&rows).Error; e != nil {
			return e
		}
		return tx.Where("operation_id = ?", op.ID).Order("id").Find(&events).Error
	})
	return op, rows, events, e
}

// ReconcileOperation resolves an uncertain commit ONLY from committed movement
// evidence. Absence is not proof of failure; it leaves the attempt processing.
func ReconcileOperation(db *gorm.DB, org, id uint64) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var op Operation
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND org_id = ?", id, org).Take(&op).Error; e != nil {
			return e
		}
		if op.Status != "processing" {
			return nil
		}
		var n int64
		if e := tx.Model(&Movement{}).Where("operation_id = ? AND org_id = ?", id, org).Count(&n).Error; e != nil {
			return e
		}
		if n == 0 {
			return ErrOperationPending
		}
		return setOperationStatus(tx, &op, "success", "")
	})
}
