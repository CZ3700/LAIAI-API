package videobilling

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrUnmapped       = errors.New("video_billing: bill is not uniquely mapped")
	ErrNotSettled     = errors.New("video_billing: initial settlement required")
	ErrReviewRequired = errors.New("video_billing: independent approval required")
	ErrStale          = errors.New("video_billing: stale reconciliation basis")
)

const (
	BillTaskTotal        = "task_total"
	BillAggregate        = "aggregate"
	ReconcileMatched     = "matched"
	ReconcileDifference  = "difference"
	ReconcilePending     = "pending_usage"
	ReconcileQuarantined = "quarantined"
	ReconcileStale       = "stale"
)

// VerifiedBinding is INTERNAL: the caller must resolve the unique upstream
// binding in the same transaction, with provider credentials scoped correctly.
// It is never a DTO to bind from an HTTP body or an untrusted CSV column.
type VerifiedBinding struct {
	ProviderScopeID uint64
	UpstreamTaskID  string
	OrgID           uint64
	TaskID          string
	BindingVersion  string
}

type BillImport struct {
	ID              uint64 `gorm:"primaryKey"`
	ImportKey       string
	ProviderScopeID uint64
	Source          string
	SourceVersion   string
	SourceHash      string
	PayloadRef      string
	ExpectedRows    int64
	State           string
	CreatedAt       time.Time
	CompletedAt     *time.Time
}

func (BillImport) TableName() string { return "video_provider_bill_imports" }

// BillLine is immutable supplier evidence. AmountCNY is the authoritative
// task-total charge, never an allocation from an aggregated invoice.
type BillLine struct {
	ID               uint64 `gorm:"primaryKey"`
	ProviderScopeID  uint64
	Source           string
	SourceRecordID   string
	SourceVersion    string
	SourceRevision   int64
	ContentHash      string
	EvidenceHash     string
	PayloadRef       string
	Granularity      string
	UpstreamTaskID   string
	Model            string
	PeriodStart      time.Time
	PeriodEnd        time.Time
	CompletionTokens *int64
	AmountCNY        decimal.Decimal
	ObservedAt       time.Time
}

func (BillLine) TableName() string { return "video_provider_bill_lines" }

// BillBinding is appended only after unique mapping has been independently
// verified. This permits recovery of a quarantined line without rewriting it
// or inventing another supplier source version.
type BillBinding struct {
	LineID         uint64 `gorm:"primaryKey"`
	OrgID          uint64
	TaskID         string
	BindingVersion string
	CreatedAt      time.Time
}

func (BillBinding) TableName() string { return "video_provider_bill_bindings" }

type BillImportRow struct {
	ID          uint64 `gorm:"primaryKey"`
	ImportID    uint64
	RowNumber   int64 `gorm:"column:source_ordinal"`
	LineID      uint64
	ContentHash string
}

func (BillImportRow) TableName() string { return "video_provider_bill_import_rows" }

// BillHead is a rebuildable projection. Only this row and import progress are
// mutable; historical bill lines and reconciliation items are append-only.
type BillHead struct {
	ID              uint64 `gorm:"primaryKey"`
	ProviderScopeID uint64
	Source          string
	SourceRecordID  string
	SourceRevision  int64
	LineID          uint64
}

func (BillHead) TableName() string { return "video_provider_bill_heads" }

type ReconciliationItem struct {
	ID             uint64 `gorm:"primaryKey"`
	LineID         uint64
	OrgID          uint64
	TaskID         string
	BindingVersion string
	State          string
	Reason         string
	InitialEntryID *uint64
	ObservedTokens *int64
	ExpectedTokens *int64
	ObservedCNY    decimal.Decimal
	ExpectedCNY    decimal.Decimal
	DifferenceCNY  decimal.Decimal
	CreatedAt      time.Time
}

func (ReconciliationItem) TableName() string { return "video_reconciliation_items" }

type BeginImportInput struct {
	ImportKey                                     string
	ProviderScopeID                               uint64
	Source, SourceVersion, SourceHash, PayloadRef string
	ExpectedRows                                  int64
	Now                                           time.Time
}
type BeginImportResult struct {
	Import BillImport
	Replay bool
}
type ImportLineInput struct {
	ImportID  uint64
	RowNumber int64            // stable 1-based source ordinal, permits crash-safe replay
	Line      BillLine         // ID, tenant mapping and ContentHash are assigned internally
	Binding   *VerifiedBinding // nil => quarantine; aggregate cannot be mapped
	Now       time.Time
}
type ImportLineResult struct {
	Line   BillLine
	Item   ReconciliationItem
	Replay bool
}

// A proposal is an immutable, exact-value review artifact, not an instruction
// to debit. SupplierOnly changes supplier costs/usage without customer money.
type AdjustmentProposal struct {
	ID                  uint64 `gorm:"primaryKey"`
	ProposalKey         string
	OrgID               uint64
	TaskID              string
	LineID              uint64
	ProposerID          uint64
	Reason              string
	SupplierOnly        bool
	BaseApplicationID   uint64
	BaseNetUSD          decimal.Decimal
	TargetNetUSD        decimal.Decimal
	TargetCalculatedUSD decimal.Decimal
	TargetCNY           decimal.Decimal
	TargetSupplierUSD   decimal.Decimal
	TargetTokens        *int64
	CreatedAt           time.Time
	ExpiresAt           time.Time
}

func (AdjustmentProposal) TableName() string { return "video_adjustment_proposals" }

type AdjustmentReview struct {
	ID         uint64 `gorm:"primaryKey"`
	ProposalID uint64
	OrgID      uint64
	ReviewerID uint64
	Approved   bool
	Reason     string
	CreatedAt  time.Time
}

func (AdjustmentReview) TableName() string { return "video_adjustment_reviews" }

type AdjustmentApplication struct {
	ID               uint64 `gorm:"primaryKey"`
	ProposalID       uint64
	ReviewID         uint64
	OrgID            uint64
	TaskID           string
	LineID           uint64
	JournalID        *uint64
	CustomerDeltaUSD decimal.Decimal // positive means debit, negative refund
	SupplierDeltaCNY decimal.Decimal
	SupplierDeltaUSD decimal.Decimal
	TokensDelta      int64
	NetUSD           decimal.Decimal
	CalculatedUSD    decimal.Decimal
	AbsorbedUSD      decimal.Decimal
	SupplierCNY      decimal.Decimal
	SupplierUSD      decimal.Decimal
	CompletionTokens *int64
	CreatedAt        time.Time
}

func (AdjustmentApplication) TableName() string { return "video_adjustment_applications" }

type ProposeAdjustmentInput struct {
	OrgID               uint64
	TaskID, ProposalKey string
	LineID, ProposerID  uint64
	Reason              string
	SupplierOnly        bool // default false explicitly permits reviewed retail correction
	Now, ExpiresAt      time.Time
}
type ReviewAdjustmentInput struct {
	OrgID, ProposalID, ReviewerID uint64
	Approved                      bool
	Reason                        string
	Now                           time.Time
}
type ApplyAdjustmentInput struct {
	OrgID, ProposalID uint64
	Now               time.Time
}
type AdjustmentResult struct {
	Application          AdjustmentApplication
	Replay, QuotaOverrun bool
}
