package videobilling

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func isHash(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == 32 }

// ReserveTx atomically reserves the legacy Key budget, all applicable policy
// buckets, and wallet hold. Caller MUST include task/intent/events in this tx.
// Policies are immutable; publish new versions only through a serialized admin
// operation. There are no implicit provider limits or invented retail prices.
func ReserveTx(tx *gorm.DB, in ReserveInput) (ReservationResult, error) {
	if err := requireTx(tx); err != nil {
		return ReservationResult{}, err
	}
	if in.OrgID == 0 || in.APIKeyID == 0 || !validID(in.TaskID) || !isHash(in.RequestHMAC) || in.Now.IsZero() {
		return ReservationResult{}, ErrInvalid
	}
	if err := lockKey(tx, in.OrgID, in.APIKeyID); err != nil {
		return ReservationResult{}, err
	}
	var old Snapshot
	err := tx.Where("org_id = ? AND task_id = ?", in.OrgID, in.TaskID).Take(&old).Error
	if err == nil {
		if old.RequestHMAC != in.RequestHMAC {
			return ReservationResult{}, ErrConflict
		}
		return ReservationResult{Snapshot: old, Replay: true}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ReservationResult{}, err
	}
	rule, err := ResolvePricingTx(tx, in.Quote.Rule.PricingKey, in.Now)
	if err != nil {
		return ReservationResult{}, err
	}
	if rule.RuleID != in.Quote.Rule.RuleID || rule.Version != in.Quote.Rule.Version {
		return ReservationResult{}, ErrConflict
	}
	q, err := requoteReservationTx(tx, rule, in.Quote)
	if err != nil {
		return ReservationResult{}, err
	}
	if q.EstimatedTokens != in.Quote.EstimatedTokens || !q.HoldUSD.Equal(in.Quote.HoldUSD) || !q.SupplierReservedCNY.Equal(in.Quote.SupplierReservedCNY) || !q.SupplierReservedUSD.Equal(in.Quote.SupplierReservedUSD) {
		return ReservationResult{}, ErrConflict
	}
	if q.Metering.InputContextHash != "" && !isHash(q.Metering.InputContextHash) {
		return ReservationResult{}, ErrInvalid
	}
	if err := reserveLegacyKey(tx, in.OrgID, in.APIKeyID, q.HoldUSD); err != nil {
		return ReservationResult{}, err
	}
	if err := reserveQuotas(tx, in, q); err != nil {
		return ReservationResult{}, err
	}
	h, existing, err := ledger.CreateHoldSharded(tx, in.OrgID, ledger.AccountUserCredit, in.TaskID, ledger.HoldInput{IdempotencyKey: "video:" + in.TaskID + ":hold:v1", Amount: q.HoldUSD, RefType: "video", RefID: in.TaskID})
	if err != nil {
		return ReservationResult{}, err
	}
	if existing {
		return ReservationResult{}, ErrInvariant
	}
	meta, err := json.Marshal(q.Metering)
	if err != nil {
		return ReservationResult{}, err
	}
	s := Snapshot{OrgID: in.OrgID, APIKeyID: in.APIKeyID, TaskID: in.TaskID, ProviderScopeID: rule.ProviderScopeID, Model: rule.Model, RuleID: rule.RuleID, RuleVersion: rule.Version, FormulaVersion: rule.FormulaVersion, MinimumRuleVersion: rule.MinimumRuleVersion, CapabilityVersion: q.Metering.CapabilityVersion, RequestHMAC: in.RequestHMAC, MeteringJSON: string(meta), HoldID: h.ID, EstimatedTokens: q.EstimatedTokens, HoldUSD: q.HoldUSD, RetailUSDPer1M: rule.RetailUSDPer1M, SupplierCNYPer1M: rule.SupplierCNYPer1M, EffectiveRatio: q.Metering.EffectiveRatio, FXUSDPerCNY: q.Metering.FXUSDPerCNY, FXVersion: q.Metering.FXVersion, FXObservedAt: q.Metering.FXObservedAt.UTC(), SupplierReservedCNY: q.SupplierReservedCNY, SupplierReservedUSD: q.SupplierReservedUSD, CreatedAt: in.Now.UTC()}
	if err := tx.Create(&s).Error; err != nil {
		return ReservationResult{}, err
	}
	return ReservationResult{Snapshot: s}, nil
}

func loadLockedSnapshot(tx *gorm.DB, org uint64, task string) (Snapshot, error) {
	var s Snapshot
	err := tx.Where("org_id = ? AND task_id = ?", org, task).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	// Same first lock as reservation; prevents quota/key lock inversion.
	if err := lockKey(tx, org, s.APIKeyID); err != nil {
		return s, err
	}
	err = locked(tx).Where("org_id = ? AND task_id = ?", org, task).Take(&s).Error
	return s, err
}

func initialEntry(tx *gorm.DB, org uint64, task string) (*BillingEntry, error) {
	var e BillingEntry
	// The identity read before acquiring the key lock may have opened an older
	// MySQL repeatable-read snapshot. Read committed facts under the existing
	// lock order so a waiting settlement observes the winner, not that snapshot.
	err := locked(tx).Where("org_id = ? AND task_id = ? AND settlement_key = ?", org, task, "initial").Take(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &e, err
}

func equalTokens(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func recordEvidence(tx *gorm.DB, s Snapshot, e UsageEvidence) (UsageEvidence, error) {
	if !validID(e.EvidenceID) || e.SourceRecordID == "" || len(e.SourceRecordID) > 128 || !validID(e.SourceVersion) || e.ObservedAt.IsZero() || len(e.PayloadRef) > 128 {
		return e, ErrInvalid
	}
	if (e.OrgID != 0 && e.OrgID != s.OrgID) || (e.TaskID != "" && e.TaskID != s.TaskID) {
		return e, ErrInvalid
	}
	switch e.Source {
	case "task_get", "usage_export", "bill_exact", "manual_verified":
	default:
		return e, ErrInvalid
	}
	if e.CompletionTokens != nil && *e.CompletionTokens < 0 {
		return e, ErrInvalid
	}
	e.OrgID, e.TaskID = s.OrgID, s.TaskID
	var old UsageEvidence
	// Missing-usage observations have no initial entry yet, but their evidence
	// must still be deduplicated against the latest committed transaction.
	err := locked(tx).Where("org_id = ? AND task_id = ? AND source = ? AND source_record_id = ? AND source_version = ?", s.OrgID, s.TaskID, e.Source, e.SourceRecordID, e.SourceVersion).Take(&old).Error
	if err == nil {
		if !equalTokens(old.CompletionTokens, e.CompletionTokens) {
			return e, ErrConflict
		}
		return old, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return e, err
	}
	if err := tx.Create(&e).Error; err != nil {
		return e, err
	}
	return e, nil
}

// SettleUsageTx records only supplied real usage. A missing value returns
// pending_usage and retains every reservation. It NEVER falls back to Quote.
func SettleUsageTx(tx *gorm.DB, in SettlementInput) (SettlementResult, error) {
	if err := requireTx(tx); err != nil {
		return SettlementResult{}, err
	}
	if in.OrgID == 0 || !validID(in.TaskID) || in.Now.IsZero() {
		return SettlementResult{}, ErrInvalid
	}
	s, err := loadLockedSnapshot(tx, in.OrgID, in.TaskID)
	if err != nil {
		return SettlementResult{}, err
	}
	old, err := initialEntry(tx, in.OrgID, in.TaskID)
	if err != nil {
		return SettlementResult{}, err
	}
	if old != nil {
		if old.State != Settled || (in.Evidence.CompletionTokens != nil && !equalTokens(old.CompletionTokens, in.Evidence.CompletionTokens)) {
			return SettlementResult{}, ErrConflict
		}
		return SettlementResult{State: old.State, Entry: old, Replay: true, QuotaOverrun: old.AbsorbedUSD.IsPositive() || (old.CompletionTokens != nil && *old.CompletionTokens > s.EstimatedTokens)}, nil
	}
	evidence, err := recordEvidence(tx, s, in.Evidence)
	if err != nil {
		return SettlementResult{}, err
	}
	if evidence.CompletionTokens == nil {
		return SettlementResult{State: PendingUsage}, nil
	}
	n := decimal.NewFromInt(*evidence.CompletionTokens)
	calculated := s.RetailUSDPer1M.Mul(n).Shift(-6).Mul(s.EffectiveRatio).Round(8)
	cny := s.SupplierCNYPer1M.Mul(n).Shift(-6).Round(8)
	usd := cny.Mul(s.FXUSDPerCNY).Round(8)
	if !validMoney(calculated) || !validMoney(cny) || !validMoney(usd) {
		return SettlementResult{}, ErrInvalid
	}
	captured := decimal.Min(calculated, s.HoldUSD)
	e := BillingEntry{OrgID: s.OrgID, TaskID: s.TaskID, SettlementKey: "initial", State: Settled, SnapshotID: s.ID, EvidenceID: evidence.EvidenceID, HoldID: s.HoldID, CompletionTokens: evidence.CompletionTokens, CalculatedUSD: calculated, CapturedUSD: captured, ReleasedUSD: s.HoldUSD.Sub(captured), AbsorbedUSD: calculated.Sub(captured), SupplierCNY: cny, SupplierUSD: usd, Reason: "actual_usage_settled", CreatedAt: in.Now.UTC()}
	return finish(tx, s, e, in.Now)
}

// ReleaseConfirmedTx is an internal evidence-gated operation. It must NEVER be
// called on a timeout, unknown submit, missing usage or an ambiguous DELETE.
func ReleaseConfirmedTx(tx *gorm.DB, in ReleaseInput) (SettlementResult, error) {
	if err := requireTx(tx); err != nil {
		return SettlementResult{}, err
	}
	if !in.Confirmed || (in.Reason != ReleaseNotDispatched && in.Reason != ReleaseProviderNoCharge) {
		return SettlementResult{}, ErrUnconfirmedRelease
	}
	if in.OrgID == 0 || !validID(in.TaskID) || !validID(in.EvidenceID) || in.Now.IsZero() {
		return SettlementResult{}, ErrInvalid
	}
	s, err := loadLockedSnapshot(tx, in.OrgID, in.TaskID)
	if err != nil {
		return SettlementResult{}, err
	}
	old, err := initialEntry(tx, in.OrgID, in.TaskID)
	if err != nil {
		return SettlementResult{}, err
	}
	if old != nil {
		if old.State != Released {
			return SettlementResult{}, ErrConflict
		}
		return SettlementResult{State: Released, Entry: old, Replay: true}, nil
	}
	e := BillingEntry{OrgID: s.OrgID, TaskID: s.TaskID, SettlementKey: "initial", State: Released, SnapshotID: s.ID, EvidenceID: in.EvidenceID, HoldID: s.HoldID, ReleasedUSD: s.HoldUSD, Reason: string(in.Reason), CreatedAt: in.Now.UTC()}
	return finish(tx, s, e, in.Now)
}

func finish(tx *gorm.DB, s Snapshot, e BillingEntry, at time.Time) (SettlementResult, error) {
	// Lock budget rows before ledger hold/account, same as ReserveTx.
	overrun, err := settleQuotas(tx, s, e, at)
	if err != nil {
		return SettlementResult{}, err
	}
	if e.State == Released {
		err = ledger.Release(tx, s.HoldID)
	} else {
		var j *ledger.JournalEntry
		j, err = ledger.Capture(tx, s.HoldID, e.CapturedUSD, "video actual usage")
		if j != nil {
			e.JournalID = &j.ID
		}
	}
	if err != nil {
		return SettlementResult{}, err
	}
	if err := returnLegacyKey(tx, s, e.ReleasedUSD); err != nil {
		return SettlementResult{}, err
	}
	if err := tx.Create(&e).Error; err != nil {
		return SettlementResult{}, err
	}
	return SettlementResult{State: e.State, Entry: &e, QuotaOverrun: overrun || e.AbsorbedUSD.IsPositive() || (e.CompletionTokens != nil && *e.CompletionTokens > s.EstimatedTokens)}, nil
}
