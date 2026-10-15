// Package videobilling provides transaction-scoped video pricing and accounting.
// The caller owns the transaction, task CAS, evidence/events and outbox. Every
// returned error MUST roll back that transaction. No API performs network I/O.
package videobilling

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrInvalid             = errors.New("video_billing: invalid input")
	ErrTransactionRequired = errors.New("video_billing: transaction required")
	ErrNotFound            = errors.New("video_billing: not found")
	ErrConflict            = errors.New("video_billing: conflict")
	ErrQuotaExceeded       = errors.New("video_billing: quota exceeded")
	ErrPricingUnavailable  = errors.New("video_billing: pricing unavailable")
	ErrUnconfirmedRelease  = errors.New("video_billing: release not confirmed")
	ErrInvariant           = errors.New("video_billing: invariant violation")
)

const (
	Reserved       = "reserved"
	PendingUsage   = "pending_usage"
	Settled        = "settled"
	Released       = "released"
	ScopeOrg       = "org"
	ScopeKey       = "key"
	ScopeOrgModel  = "org_model"
	ScopeProvider  = "provider"
	MetricUSD      = "usd"
	MetricCNY      = "cny"
	MetricTokens   = "tokens"
	MetricTasks    = "tasks"
	PeriodLifetime = "lifetime"
	PeriodDay      = "day"
	PeriodMonth    = "month"
)

type PricingKey struct {
	ProviderScopeID uint64
	Model           string
	Mode            string
	Resolution      string
	InputVideo      bool
	ServiceTier     string
	Draft           bool
}

// PricingRule is append-only after publication. No production price is seeded
// by this package: commercial prices and capability bounds require approval.
type PricingRule struct {
	ID                 uint64 `gorm:"primaryKey"`
	RuleID             string
	Version            string
	PricingKey         `gorm:"embedded"`
	RetailUSDPer1M     decimal.Decimal `gorm:"column:retail_usd_per_1m"`
	SupplierCNYPer1M   decimal.Decimal `gorm:"column:supplier_cny_per_1m"`
	MinimumTokens      int64
	MinimumRuleVersion string
	RiskMultiplier     decimal.Decimal
	FormulaVersion     string
	SourceHash         string
	EffectiveFrom      time.Time
	EffectiveUntil     *time.Time
	Enabled            bool
}

func (PricingRule) TableName() string { return "video_pricing_rules" }

// MeteringInput contains VERIFIED upper bounds, not client-asserted durations.
// Adaptive duration/ratio and Draft inheritance must be resolved by the caller.
type MeteringInput struct {
	InputVideo        bool
	InputDurationMS   int64
	OutputDurationMS  int64
	OutputFrames      int64 // mutually exclusive with OutputDurationMS
	Width             int64
	Height            int64
	FPSNumerator      int64
	FPSDenominator    int64
	BoundsVerified    bool
	CapabilityVersion string
	InputContextHash  string
	ParentDraftTaskID string
	EffectiveRatio    decimal.Decimal
	FXUSDPerCNY       decimal.Decimal
	FXVersion         string
	FXObservedAt      time.Time
}

type QuoteResult struct {
	Rule                PricingRule
	Metering            MeteringInput
	EstimatedTokens     int64
	HoldUSD             decimal.Decimal
	SupplierReservedCNY decimal.Decimal
	SupplierReservedUSD decimal.Decimal
}

type QuotaPolicy struct {
	ID          uint64 `gorm:"primaryKey"`
	OrgID       uint64 // zero only for internal supplier policies
	Scope       string
	OwnerID     uint64
	Model       string
	Metric      string
	Period      string
	Timezone    string
	LimitAmount *decimal.Decimal
	LimitUnits  *int64
	Version     string
	Enabled     bool
}

func (QuotaPolicy) TableName() string { return "video_quota_policies" }

type QuotaBucket struct {
	ID             uint64 `gorm:"primaryKey"`
	PolicyID       uint64
	ScopeKey       string // stable across policy versions; UNIQUE with PeriodStart
	Timezone       string
	OrgID          uint64
	PeriodStart    time.Time
	PeriodEnd      *time.Time
	LimitAmount    *decimal.Decimal
	LimitUnits     *int64
	ReservedAmount decimal.Decimal
	ConsumedAmount decimal.Decimal
	ReservedUnits  int64
	ConsumedUnits  int64
	Blocked        bool // actual usage overrun: block further reservations
}

func (QuotaBucket) TableName() string { return "video_quota_buckets" }

type QuotaReservation struct {
	ID             uint64 `gorm:"primaryKey"`
	OrgID          uint64
	TaskID         string
	BucketID       uint64
	Metric         string
	Supplier       bool
	ReservedAmount decimal.Decimal
	ReservedUnits  int64
	ActualAmount   decimal.Decimal
	ActualUnits    int64
	State          string
	CreatedAt      time.Time
	SettledAt      *time.Time
}

func (QuotaReservation) TableName() string { return "video_quota_reservations" }

// Snapshot stores immutable creation-time calculation facts and the hold link.
// MeteringJSON is typed, numerical/version metadata only; never prompt or URL.
type Snapshot struct {
	ID                  uint64 `gorm:"primaryKey"`
	OrgID               uint64
	APIKeyID            uint64
	TaskID              string
	ProviderScopeID     uint64
	Model               string
	RuleID              string
	RuleVersion         string
	FormulaVersion      string
	MinimumRuleVersion  string
	CapabilityVersion   string
	RequestHMAC         string
	MeteringJSON        string
	HoldID              uint64
	EstimatedTokens     int64
	HoldUSD             decimal.Decimal
	RetailUSDPer1M      decimal.Decimal `gorm:"column:retail_usd_per_1m"`
	SupplierCNYPer1M    decimal.Decimal `gorm:"column:supplier_cny_per_1m"`
	EffectiveRatio      decimal.Decimal
	FXUSDPerCNY         decimal.Decimal `gorm:"column:fx_usd_per_cny"`
	FXVersion           string
	FXObservedAt        time.Time
	SupplierReservedCNY decimal.Decimal
	SupplierReservedUSD decimal.Decimal
	CreatedAt           time.Time
}

func (Snapshot) TableName() string { return "video_billing_snapshots" }

type UsageEvidence struct {
	ID               uint64 `gorm:"primaryKey"`
	OrgID            uint64
	TaskID           string
	EvidenceID       string
	Source           string
	SourceRecordID   string
	SourceVersion    string
	CompletionTokens *int64
	PayloadRef       string
	ObservedAt       time.Time
}

func (UsageEvidence) TableName() string { return "video_usage_evidence" }

type BillingEntry struct {
	ID               uint64 `gorm:"primaryKey"`
	OrgID            uint64
	TaskID           string
	SettlementKey    string // initial, shared by capture and release
	State            string
	SnapshotID       uint64
	EvidenceID       string
	HoldID           uint64
	JournalID        *uint64
	CompletionTokens *int64
	CalculatedUSD    decimal.Decimal
	CapturedUSD      decimal.Decimal
	ReleasedUSD      decimal.Decimal
	AbsorbedUSD      decimal.Decimal
	SupplierCNY      decimal.Decimal
	SupplierUSD      decimal.Decimal
	Reason           string
	CreatedAt        time.Time
}

func (BillingEntry) TableName() string { return "video_billing_entries" }

type ReserveInput struct {
	OrgID       uint64
	APIKeyID    uint64
	TaskID      string
	RequestHMAC string
	Quote       QuoteResult
	Now         time.Time
}
type ReservationResult struct {
	Snapshot Snapshot
	Replay   bool
}
type SettlementInput struct {
	OrgID    uint64
	TaskID   string
	Evidence UsageEvidence
	Now      time.Time
}
type ReleaseReason string

const (
	ReleaseNotDispatched    ReleaseReason = "not_dispatched"
	ReleaseProviderNoCharge ReleaseReason = "provider_no_charge"
)

type ReleaseInput struct {
	OrgID      uint64
	TaskID     string
	EvidenceID string
	Reason     ReleaseReason
	Confirmed  bool // caller must verify persisted evidence; never bind from JSON
	Now        time.Time
}
type SettlementResult struct {
	State        string
	Entry        *BillingEntry // nil while pending usage; no money changed
	Replay       bool
	QuotaOverrun bool
}
