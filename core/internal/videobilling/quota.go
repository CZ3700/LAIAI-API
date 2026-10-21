package videobilling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func locked(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() == "mysql" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

type keyIdentity struct {
	ID    uint64
	OrgID uint64
}

func lockKey(tx *gorm.DB, orgID, keyID uint64) error {
	var k keyIdentity
	err := locked(tx).Table("api_keys").Select("id, org_id").Where("id = ? AND org_id = ?", keyID, orgID).Take(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func reserveLegacyKey(tx *gorm.DB, orgID, keyID uint64, amount decimal.Decimal) error {
	r := tx.Exec("UPDATE api_keys SET used_amount = ROUND(used_amount + ?, 8) WHERE id = ? AND org_id = ? AND (quota_limit = 0 OR ROUND(quota_limit - used_amount - ?, 8) >= 0)", amount, keyID, orgID, amount)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrQuotaExceeded
	}
	return nil
}
func returnLegacyKey(tx *gorm.DB, s Snapshot, returned decimal.Decimal) error {
	if returned.IsZero() {
		return nil
	}
	r := tx.Exec("UPDATE api_keys SET used_amount = ROUND(used_amount - ?, 8) WHERE id = ? AND org_id = ? AND ROUND(used_amount - ?, 8) >= 0", returned, s.APIKeyID, s.OrgID, returned)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrInvariant
	}
	return nil
}

func policyWindow(p QuotaPolicy, at time.Time) (time.Time, *time.Time, error) {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.Time{}, nil, ErrInvalid
	}
	t := at.In(loc)
	var start, end time.Time
	switch p.Period {
	case PeriodLifetime:
		return time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), nil, nil
	case PeriodDay:
		start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		end = start.AddDate(0, 0, 1)
	case PeriodMonth:
		start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 1, 0)
	default:
		return time.Time{}, nil, ErrInvalid
	}
	start, end = start.UTC(), end.UTC()
	return start, &end, nil
}

func validPolicy(p QuotaPolicy) bool {
	if !validID(p.Version) || p.OwnerID == 0 {
		return false
	}
	if p.Scope == ScopeProvider {
		if p.OrgID != 0 {
			return false
		}
	} else if p.OrgID == 0 {
		return false
	}
	switch p.Metric {
	case MetricUSD, MetricCNY:
		return p.LimitUnits == nil && (p.LimitAmount == nil || (validMoney(*p.LimitAmount) && p.LimitAmount.Equal(p.LimitAmount.Round(8)))) && (p.Metric != MetricCNY || p.Scope == ScopeProvider)
	case MetricTokens, MetricTasks:
		return p.LimitAmount == nil && (p.LimitUnits == nil || *p.LimitUnits >= 0)
	default:
		return false
	}
}

func reserveQuotas(tx *gorm.DB, in ReserveInput, q QuoteResult) error {
	var policies []QuotaPolicy
	err := tx.Where("enabled = ? AND ((org_id = ? AND ((scope = ? AND owner_id = ?) OR (scope = ? AND owner_id = ?) OR (scope = ? AND owner_id = ? AND model = ?))) OR (scope = ? AND org_id = 0 AND owner_id = ?)) AND (model = '' OR model = ?)", true, in.OrgID, ScopeOrg, in.OrgID, ScopeKey, in.APIKeyID, ScopeOrgModel, in.OrgID, q.Rule.Model, ScopeProvider, q.Rule.ProviderScopeID, q.Rule.Model).Order("id").Find(&policies).Error
	if err != nil {
		return err
	}
	// Stable scope order survives policy version changes and bucket creation order.
	sort.Slice(policies, func(i, j int) bool { return policyScopeKey(policies[i]) < policyScopeKey(policies[j]) })
	seen := make(map[string]bool)
	for _, p := range policies {
		if !validPolicy(p) {
			return ErrInvalid
		}
		identity := policyScopeKey(p)
		if seen[identity] {
			return ErrConflict
		}
		seen[identity] = true
		start, end, err := policyWindow(p, in.Now)
		if err != nil {
			return err
		}
		var previous QuotaBucket
		perr := tx.Where("scope_key = ?", identity).Take(&previous).Error
		if perr == nil && previous.Timezone != p.Timezone {
			return ErrConflict
		}
		if perr != nil && !errors.Is(perr, gorm.ErrRecordNotFound) {
			return perr
		}
		b := QuotaBucket{PolicyID: p.ID, ScopeKey: identity, Timezone: p.Timezone, OrgID: p.OrgID, PeriodStart: start, PeriodEnd: end, LimitAmount: p.LimitAmount, LimitUnits: p.LimitUnits}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&b).Error; err != nil {
			return err
		}
		if err := locked(tx).Where("scope_key = ? AND period_start = ?", identity, start).Take(&b).Error; err != nil {
			return err
		}
		b.LimitAmount, b.LimitUnits = p.LimitAmount, p.LimitUnits
		if err := tx.Model(&b).Updates(map[string]any{"limit_amount": p.LimitAmount, "limit_units": p.LimitUnits}).Error; err != nil {
			return err
		}
		if b.Blocked {
			return ErrQuotaExceeded
		}
		r := QuotaReservation{OrgID: in.OrgID, TaskID: in.TaskID, BucketID: b.ID, Metric: p.Metric, Supplier: p.Scope == ScopeProvider, State: "active", CreatedAt: in.Now.UTC()}
		switch p.Metric {
		case MetricUSD:
			r.ReservedAmount = q.HoldUSD
			if r.Supplier {
				r.ReservedAmount = q.SupplierReservedUSD
			}
		case MetricCNY:
			r.ReservedAmount = q.SupplierReservedCNY
		case MetricTokens:
			r.ReservedUnits = q.EstimatedTokens
		case MetricTasks:
			r.ReservedUnits = 1
		}
		if err := addReservation(tx, &b, r); err != nil {
			return err
		}
		if err := tx.Create(&r).Error; err != nil {
			return err
		}
	}
	return nil
}

func policyScopeKey(p QuotaPolicy) string {
	b, _ := json.Marshal([]any{p.OrgID, p.Scope, p.OwnerID, p.Model, p.Metric, p.Period})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func addReservation(tx *gorm.DB, b *QuotaBucket, r QuotaReservation) error {
	if r.Metric == MetricUSD || r.Metric == MetricCNY {
		sum := b.ConsumedAmount.Add(b.ReservedAmount).Add(r.ReservedAmount)
		if !validMoney(sum) || (b.LimitAmount != nil && sum.GreaterThan(*b.LimitAmount)) {
			return ErrQuotaExceeded
		}
		return tx.Model(b).Update("reserved_amount", b.ReservedAmount.Add(r.ReservedAmount)).Error
	}
	if b.ConsumedUnits < 0 || b.ReservedUnits < 0 || r.ReservedUnits > math.MaxInt64-b.ConsumedUnits || b.ReservedUnits > math.MaxInt64-b.ConsumedUnits-r.ReservedUnits {
		return ErrInvariant
	}
	sum := b.ConsumedUnits + b.ReservedUnits + r.ReservedUnits
	if b.LimitUnits != nil && sum > *b.LimitUnits {
		return ErrQuotaExceeded
	}
	return tx.Model(b).Update("reserved_units", b.ReservedUnits+r.ReservedUnits).Error
}

func settleQuotas(tx *gorm.DB, s Snapshot, e BillingEntry, at time.Time) (bool, error) {
	var rows []QuotaReservation
	// Same stable scope lock ordering as reservation (not policy/bucket ID).
	err := tx.Table("video_quota_reservations AS r").Select("r.*").Joins("JOIN video_quota_buckets b ON b.id = r.bucket_id").Where("r.org_id = ? AND r.task_id = ?", s.OrgID, s.TaskID).Order("b.scope_key, b.period_start").Scan(&rows).Error
	if err != nil {
		return false, err
	}
	overrun := false
	for _, r := range rows {
		var b QuotaBucket
		if err := locked(tx).Where("id = ?", r.BucketID).Take(&b).Error; err != nil {
			return false, err
		}
		if r.State != "active" {
			return false, ErrInvariant
		}
		amount := decimal.Zero
		units := int64(0)
		if e.State == Settled {
			switch r.Metric {
			case MetricUSD:
				amount = e.CapturedUSD
				if r.Supplier {
					amount = e.SupplierUSD
				}
			case MetricCNY:
				amount = e.SupplierCNY
			case MetricTokens:
				if e.CompletionTokens == nil {
					return false, ErrInvariant
				}
				units = *e.CompletionTokens
			case MetricTasks:
				units = 1
			default:
				return false, ErrInvariant
			}
		}
		if b.ReservedAmount.LessThan(r.ReservedAmount) || b.ReservedUnits < r.ReservedUnits {
			return false, ErrInvariant
		}
		if b.ConsumedUnits < 0 || units > math.MaxInt64-b.ConsumedUnits {
			return false, ErrInvariant
		}
		consumed := b.ConsumedAmount.Add(amount)
		reserved := b.ReservedAmount.Sub(r.ReservedAmount)
		if !validMoney(consumed) || !validMoney(consumed.Add(reserved)) {
			return false, ErrInvariant
		}
		consumedUnits := b.ConsumedUnits + units
		reservedUnits := b.ReservedUnits - r.ReservedUnits
		if reservedUnits > math.MaxInt64-consumedUnits {
			return false, ErrInvariant
		}
		exceeds := (b.LimitAmount != nil && consumed.Add(reserved).GreaterThan(*b.LimitAmount)) ||
			(b.LimitUnits != nil && consumedUnits+reservedUnits > *b.LimitUnits)
		// Estimation variance remains a financial fact, but only exhausting the
		// shared budget blocks other tasks. Never clear an existing block here.
		overrun = overrun || amount.GreaterThan(r.ReservedAmount) || units > r.ReservedUnits || exceeds
		if err := tx.Model(&b).Updates(map[string]any{"reserved_amount": reserved, "consumed_amount": consumed, "reserved_units": reservedUnits, "consumed_units": consumedUnits, "blocked": b.Blocked || exceeds}).Error; err != nil {
			return false, err
		}
		state := "consumed"
		if e.State == Released {
			state = "released"
		}
		res := tx.Model(&QuotaReservation{}).Where("id = ? AND state = ?", r.ID, "active").Updates(map[string]any{"actual_amount": amount, "actual_units": units, "state": state, "settled_at": at.UTC()})
		if res.Error != nil {
			return false, res.Error
		}
		if res.RowsAffected != 1 {
			return false, ErrInvariant
		}
	}
	return overrun, nil
}
