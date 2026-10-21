package videobilling

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type currentBilling struct {
	ApplicationID, LineID                           uint64
	NetUSD, CalculatedUSD, SupplierCNY, SupplierUSD decimal.Decimal
	Tokens                                          *int64
}

func currentBillingState(tx *gorm.DB, s Snapshot) (currentBilling, error) {
	e, err := initialEntry(tx, s.OrgID, s.TaskID)
	if err != nil {
		return currentBilling{}, err
	}
	if e == nil {
		return currentBilling{}, ErrNotSettled
	}
	c := currentBilling{NetUSD: e.CapturedUSD, CalculatedUSD: e.CalculatedUSD, SupplierCNY: e.SupplierCNY, SupplierUSD: e.SupplierUSD, Tokens: e.CompletionTokens}
	var a AdjustmentApplication
	err = tx.Where("org_id = ? AND task_id = ?", s.OrgID, s.TaskID).Order("id DESC").Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return currentBilling{ApplicationID: a.ID, LineID: a.LineID, NetUSD: a.NetUSD, CalculatedUSD: a.CalculatedUSD, SupplierCNY: a.SupplierCNY, SupplierUSD: a.SupplierUSD, Tokens: a.CompletionTokens}, nil
}

func latestBoundBill(tx *gorm.DB, org uint64, task string, lineID uint64) (BillLine, error) {
	var b BillBinding
	if err := tx.Where("line_id = ? AND org_id = ? AND task_id = ?", lineID, org, task).Take(&b).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BillLine{}, ErrUnmapped
		}
		return BillLine{}, err
	}
	var l BillLine
	if err := tx.Where("id = ?", lineID).Take(&l).Error; err != nil {
		return l, err
	}
	if l.Granularity != BillTaskTotal {
		return l, ErrUnmapped
	}
	h, err := lockBillHead(tx, l)
	if err != nil {
		return l, err
	}
	if h.LineID != l.ID {
		return l, ErrStale
	}
	return l, nil
}

// SettleBillUsageTx is an explicit recovery command, never invoked by import.
// Caller must establish the task's official billing outcome and write its CAS,
// events and audit in this transaction. Missing usage keeps the hold active.
// Initial supplier cost uses the immutable quote; bill cost differences remain
// separate reviewable corrections and do not set a different retail rate.
func SettleBillUsageTx(tx *gorm.DB, org uint64, task string, lineID uint64, now time.Time) (SettlementResult, error) {
	if err := requireTx(tx); err != nil {
		return SettlementResult{}, err
	}
	if org == 0 || !validID(task) || lineID == 0 || now.IsZero() {
		return SettlementResult{}, ErrInvalid
	}
	if _, err := loadLockedSnapshot(tx, org, task); err != nil {
		return SettlementResult{}, err
	}
	l, err := latestBoundBill(tx, org, task, lineID)
	if err != nil {
		return SettlementResult{}, err
	}
	return SettleUsageTx(tx, SettlementInput{OrgID: org, TaskID: task, Now: now, Evidence: UsageEvidence{EvidenceID: fmt.Sprintf("bill-line-%d", l.ID), Source: "bill_exact", SourceRecordID: l.SourceRecordID, SourceVersion: l.SourceVersion, CompletionTokens: l.CompletionTokens, PayloadRef: l.PayloadRef, ObservedAt: l.ObservedAt}})
}

func sameBillSeries(tx *gorm.DB, previous uint64, l BillLine) error {
	if previous == 0 {
		return nil
	}
	var old BillLine
	if err := tx.Where("id = ?", previous).Take(&old).Error; err != nil {
		return err
	}
	if old.ProviderScopeID != l.ProviderScopeID || old.Source != l.Source || old.SourceRecordID != l.SourceRecordID {
		return ErrConflict
	}
	if old.SourceRevision > l.SourceRevision {
		return ErrStale
	}
	return nil
}

// ProposeAdjustmentTx computes the exact review artifact from a verified latest
// task-total bill and immutable retail/FX snapshots. It performs no money write.
// SupplierOnly=true cannot alter the customer's charge, even if usage differs.
func ProposeAdjustmentTx(tx *gorm.DB, in ProposeAdjustmentInput) (AdjustmentProposal, error) {
	if err := requireTx(tx); err != nil {
		return AdjustmentProposal{}, err
	}
	in.ExpiresAt = in.ExpiresAt.UTC().Truncate(time.Millisecond)
	if in.OrgID == 0 || !validID(in.TaskID) || !validID(in.ProposalKey) || in.LineID == 0 || in.ProposerID == 0 || !validID(in.Reason) || in.Now.IsZero() || !in.ExpiresAt.After(in.Now) {
		return AdjustmentProposal{}, ErrInvalid
	}
	s, err := loadLockedSnapshot(tx, in.OrgID, in.TaskID)
	if err != nil {
		return AdjustmentProposal{}, err
	}
	var p AdjustmentProposal
	err = tx.Where("org_id = ? AND proposal_key = ?", in.OrgID, in.ProposalKey).Take(&p).Error
	if err == nil {
		if p.TaskID != in.TaskID || p.LineID != in.LineID || p.ProposerID != in.ProposerID || p.Reason != in.Reason || p.SupplierOnly != in.SupplierOnly || !p.ExpiresAt.Equal(in.ExpiresAt) {
			return p, ErrConflict
		}
		return p, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return p, err
	}
	l, err := latestBoundBill(tx, in.OrgID, in.TaskID, in.LineID)
	if err != nil {
		return p, err
	}
	c, err := currentBillingState(tx, s)
	if err != nil {
		return p, err
	}
	if err := sameBillSeries(tx, c.LineID, l); err != nil {
		return p, err
	}
	p = AdjustmentProposal{ProposalKey: in.ProposalKey, OrgID: in.OrgID, TaskID: in.TaskID, LineID: l.ID, ProposerID: in.ProposerID, Reason: in.Reason, SupplierOnly: in.SupplierOnly, BaseApplicationID: c.ApplicationID, BaseNetUSD: c.NetUSD, TargetNetUSD: c.NetUSD, TargetCalculatedUSD: c.CalculatedUSD, TargetCNY: l.AmountCNY, TargetSupplierUSD: l.AmountCNY.Mul(s.FXUSDPerCNY).Round(8), TargetTokens: c.Tokens, CreatedAt: in.Now.UTC(), ExpiresAt: in.ExpiresAt.UTC()}
	if l.CompletionTokens != nil {
		p.TargetTokens = l.CompletionTokens
	}
	if !in.SupplierOnly {
		if l.CompletionTokens == nil {
			return p, ErrInvalid
		}
		p.TargetCalculatedUSD = s.RetailUSDPer1M.Mul(decimal.NewFromInt(*l.CompletionTokens)).Shift(-6).Mul(s.EffectiveRatio).Round(8)
		p.TargetNetUSD = decimal.Min(p.TargetCalculatedUSD, s.HoldUSD)
	}
	if !validMoney(p.TargetSupplierUSD) || !validMoney(p.TargetCalculatedUSD) || p.TargetNetUSD.GreaterThan(s.HoldUSD) {
		return p, ErrInvalid
	}
	if err := tx.Create(&p).Error; err != nil {
		return p, err
	}
	return p, nil
}

// ReviewAdjustmentTx records one immutable decision by a different actor.
// Caller must authenticate/admin-authorize both actor identities; never accept
// ReviewerID/ProposerID from an untrusted public body. There is no bypass flag.
func ReviewAdjustmentTx(tx *gorm.DB, in ReviewAdjustmentInput) (AdjustmentReview, error) {
	if err := requireTx(tx); err != nil {
		return AdjustmentReview{}, err
	}
	if in.OrgID == 0 || in.ProposalID == 0 || in.ReviewerID == 0 || !validID(in.Reason) || in.Now.IsZero() {
		return AdjustmentReview{}, ErrInvalid
	}
	var p AdjustmentProposal
	if err := tx.Where("id = ? AND org_id = ?", in.ProposalID, in.OrgID).Take(&p).Error; err != nil {
		return AdjustmentReview{}, err
	}
	if in.ReviewerID == p.ProposerID {
		return AdjustmentReview{}, ErrReviewRequired
	}
	var r AdjustmentReview
	err := tx.Where("proposal_id = ?", p.ID).Take(&r).Error
	if err == nil {
		if r.ReviewerID != in.ReviewerID || r.Approved != in.Approved || r.Reason != in.Reason {
			return r, ErrConflict
		}
		return r, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return r, err
	}
	if !in.Now.Before(p.ExpiresAt) {
		return r, ErrStale
	}
	r = AdjustmentReview{ProposalID: p.ID, OrgID: p.OrgID, ReviewerID: in.ReviewerID, Approved: in.Approved, Reason: in.Reason, CreatedAt: in.Now.UTC()}
	if err := tx.Create(&r).Error; err != nil {
		return r, err
	}
	return r, nil
}

// ApplyAdjustmentTx applies an approved artifact exactly once. True supplier
// cost/token corrections and customer quota/journal writes share the caller's
// transaction. An ambiguous/missing initial settlement cannot enter this flow.
func ApplyAdjustmentTx(tx *gorm.DB, in ApplyAdjustmentInput) (AdjustmentResult, error) {
	if err := requireTx(tx); err != nil {
		return AdjustmentResult{}, err
	}
	if in.OrgID == 0 || in.ProposalID == 0 || in.Now.IsZero() {
		return AdjustmentResult{}, ErrInvalid
	}
	var p AdjustmentProposal
	if err := tx.Where("id = ? AND org_id = ?", in.ProposalID, in.OrgID).Take(&p).Error; err != nil {
		return AdjustmentResult{}, err
	}
	s, err := loadLockedSnapshot(tx, in.OrgID, p.TaskID)
	if err != nil {
		return AdjustmentResult{}, err
	}
	var old AdjustmentApplication
	err = tx.Where("proposal_id = ? AND org_id = ?", p.ID, p.OrgID).Take(&old).Error
	if err == nil {
		return AdjustmentResult{Application: old, Replay: true, QuotaOverrun: old.AbsorbedUSD.IsPositive() || (old.CompletionTokens != nil && *old.CompletionTokens > s.EstimatedTokens)}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return AdjustmentResult{}, err
	}
	var r AdjustmentReview
	err = tx.Where("proposal_id = ? AND org_id = ?", p.ID, p.OrgID).Take(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (!r.Approved || r.ReviewerID == p.ProposerID)) {
		return AdjustmentResult{}, ErrReviewRequired
	}
	if err != nil {
		return AdjustmentResult{}, err
	}
	if !in.Now.Before(p.ExpiresAt) {
		return AdjustmentResult{}, ErrStale
	}
	l, err := latestBoundBill(tx, p.OrgID, p.TaskID, p.LineID)
	if err != nil {
		return AdjustmentResult{}, err
	}
	c, err := currentBillingState(tx, s)
	if err != nil {
		return AdjustmentResult{}, err
	}
	if c.ApplicationID != p.BaseApplicationID || !c.NetUSD.Equal(p.BaseNetUSD) {
		return AdjustmentResult{}, ErrStale
	}
	if err := sameBillSeries(tx, c.LineID, l); err != nil {
		return AdjustmentResult{}, err
	}
	if !validMoney(p.TargetNetUSD) || p.TargetNetUSD.GreaterThan(s.HoldUSD) {
		return AdjustmentResult{}, ErrInvariant
	}
	a := AdjustmentApplication{ProposalID: p.ID, ReviewID: r.ID, OrgID: p.OrgID, TaskID: p.TaskID, LineID: l.ID, CustomerDeltaUSD: p.TargetNetUSD.Sub(c.NetUSD), SupplierDeltaCNY: p.TargetCNY.Sub(c.SupplierCNY), SupplierDeltaUSD: p.TargetSupplierUSD.Sub(c.SupplierUSD), NetUSD: p.TargetNetUSD, CalculatedUSD: p.TargetCalculatedUSD, AbsorbedUSD: decimal.Max(decimal.Zero, p.TargetCalculatedUSD.Sub(p.TargetNetUSD)), SupplierCNY: p.TargetCNY, SupplierUSD: p.TargetSupplierUSD, CompletionTokens: p.TargetTokens, CreatedAt: in.Now.UTC()}
	before, after := int64(0), int64(0)
	if c.Tokens != nil {
		before = *c.Tokens
	}
	if p.TargetTokens != nil {
		after = *p.TargetTokens
	}
	a.TokensDelta = after - before
	if p.SupplierOnly && !a.CustomerDeltaUSD.IsZero() {
		return AdjustmentResult{}, ErrInvariant
	}
	if a.CustomerDeltaUSD.IsPositive() {
		if err := reserveLegacyKey(tx, s.OrgID, s.APIKeyID, a.CustomerDeltaUSD); err != nil {
			return AdjustmentResult{}, err
		}
	}
	if a.CustomerDeltaUSD.IsNegative() {
		if err := returnLegacyKey(tx, s, a.CustomerDeltaUSD.Neg()); err != nil {
			return AdjustmentResult{}, err
		}
	}
	overrun, err := adjustQuotas(tx, s, a)
	if err != nil {
		return AdjustmentResult{}, err
	}
	if !a.CustomerDeltaUSD.IsZero() {
		var h ledger.Hold
		if err := tx.Where("id = ?", s.HoldID).Take(&h).Error; err != nil {
			return AdjustmentResult{}, err
		}
		var account ledger.Account
		if err := tx.Where("id = ? AND org_id = ? AND type = ?", h.AccountID, s.OrgID, ledger.AccountUserCredit).Take(&account).Error; err != nil {
			return AdjustmentResult{}, ErrInvariant
		}
		kind := ledger.KindCredit
		if a.CustomerDeltaUSD.IsPositive() {
			kind = ledger.KindDebit
		}
		j, replay, err := ledger.Post(tx, ledger.PostInput{IdempotencyKey: fmt.Sprintf("video-adjustment:%d", p.ID), AccountID: h.AccountID, Kind: kind, Delta: a.CustomerDeltaUSD.Neg(), RefType: "video_adjustment", RefID: s.TaskID, Reason: p.Reason, Correction: &ledger.CorrectionLink{ProposalID: p.ID, ReviewID: r.ID, HoldID: s.HoldID}})
		if err != nil {
			return AdjustmentResult{}, err
		}
		if replay {
			return AdjustmentResult{}, ErrInvariant
		}
		a.JournalID = &j.ID
	}
	if err := tx.Create(&a).Error; err != nil {
		return AdjustmentResult{}, err
	}
	return AdjustmentResult{Application: a, QuotaOverrun: overrun || a.AbsorbedUSD.IsPositive() || (a.CompletionTokens != nil && *a.CompletionTokens > s.EstimatedTokens)}, nil
}

func adjustQuotas(tx *gorm.DB, s Snapshot, a AdjustmentApplication) (bool, error) {
	var rows []QuotaReservation
	if err := tx.Table("video_quota_reservations AS r").Select("r.*").Joins("JOIN video_quota_buckets b ON b.id = r.bucket_id").Where("r.org_id = ? AND r.task_id = ?", s.OrgID, s.TaskID).Order("b.scope_key,b.period_start").Scan(&rows).Error; err != nil {
		return false, err
	}
	overrun := false
	for _, r := range rows {
		if r.State == "active" {
			return false, ErrInvariant
		}
		var b QuotaBucket
		if err := locked(tx).Where("id = ?", r.BucketID).Take(&b).Error; err != nil {
			return false, err
		}
		delta := decimal.Zero
		units := int64(0)
		switch r.Metric {
		case MetricUSD:
			delta = a.CustomerDeltaUSD
			if r.Supplier {
				delta = a.SupplierDeltaUSD
			}
		case MetricCNY:
			if !r.Supplier {
				return false, ErrInvariant
			}
			delta = a.SupplierDeltaCNY
		case MetricTokens:
			units = a.TokensDelta
		case MetricTasks:
			continue // task outcome is not inferred from billing corrections
		default:
			return false, ErrInvariant
		}
		amount := b.ConsumedAmount.Add(delta)
		if !validMoney(amount) || !validMoney(amount.Add(b.ReservedAmount)) || (units > 0 && b.ConsumedUnits > math.MaxInt64-units) || (units < 0 && b.ConsumedUnits < -units) {
			return false, ErrInvariant
		}
		used := b.ConsumedUnits + units
		if b.ReservedUnits > math.MaxInt64-used {
			return false, ErrInvariant
		}
		exceeds := (b.LimitAmount != nil && amount.Add(b.ReservedAmount).GreaterThan(*b.LimitAmount)) || (b.LimitUnits != nil && used+b.ReservedUnits > *b.LimitUnits)
		// Monetary customer limits remain hard after initial settlement. True
		// supplier/token corrections remain factual; only a total budget overrun
		// blocks new admissions.
		if !r.Supplier && r.Metric == MetricUSD && delta.IsPositive() && (b.Blocked || exceeds) {
			return false, ErrQuotaExceeded
		}
		factOverrun := exceeds || (r.Metric == MetricTokens && a.CompletionTokens != nil && *a.CompletionTokens > r.ReservedUnits) || (r.Supplier && ((r.Metric == MetricCNY && a.SupplierCNY.GreaterThan(r.ReservedAmount)) || (r.Metric == MetricUSD && a.SupplierUSD.GreaterThan(r.ReservedAmount))))
		overrun = overrun || factOverrun
		if err := tx.Model(&b).Updates(map[string]any{"consumed_amount": amount, "consumed_units": used, "blocked": b.Blocked || exceeds}).Error; err != nil {
			return false, err
		}
	}
	return overrun, nil
}
