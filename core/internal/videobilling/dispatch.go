package videobilling

import (
	"errors"

	"gorm.io/gorm"
)

// ValidateDispatchBudgetTx rechecks the reservations owned by an accepted task
// before its first dispatch. The caller must hold its task/state lock and must
// never call this to reinterpret an in-flight or uncertain submission.
//
// Key, snapshot and bucket locks use the same order as reserve/settlement. It
// creates no new quota window and does not move a reservation after midnight or
// after a policy revision. Only a positively observed blocked bucket returns
// ErrQuotaExceeded; missing financial evidence and DB failures remain errors.
func ValidateDispatchBudgetTx(tx *gorm.DB, org uint64, taskID string) error {
	if err := requireTx(tx); err != nil {
		return err
	}
	if org == 0 || !validID(taskID) {
		return ErrInvalid
	}
	snapshot, err := loadLockedSnapshot(tx, org, taskID)
	if err != nil {
		return err
	}
	var reservations []QuotaReservation
	if err := tx.Table("video_quota_reservations AS r").Select("r.*").Joins("LEFT JOIN video_quota_buckets b ON b.id = r.bucket_id").Where("r.org_id = ? AND r.task_id = ?", org, taskID).Order("b.scope_key, b.period_start, r.id").Scan(&reservations).Error; err != nil {
		return err
	}
	if len(reservations) == 0 {
		return ErrInvariant
	}
	hasSupplierBudget, blocked := false, false
	for _, reservation := range reservations {
		if reservation.State != "active" {
			return ErrInvariant
		}
		var bucket QuotaBucket
		if err := locked(tx).Where("id = ?", reservation.BucketID).Take(&bucket).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvariant
			}
			return err
		}
		if bucket.ReservedAmount.LessThan(reservation.ReservedAmount) || bucket.ReservedUnits < reservation.ReservedUnits {
			return ErrInvariant
		}
		if reservation.Supplier && (reservation.Metric == MetricCNY || reservation.Metric == MetricUSD) {
			var policy QuotaPolicy
			if err := tx.Where("id = ?", bucket.PolicyID).Take(&policy).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrInvariant
				}
				return err
			}
			if bucket.OrgID != 0 || policy.Scope != ScopeProvider || policy.OrgID != 0 || policy.OwnerID != snapshot.ProviderScopeID || policy.Metric != reservation.Metric || bucket.LimitAmount == nil || !bucket.LimitAmount.IsPositive() {
				return ErrInvariant
			}
			hasSupplierBudget = true
		}
		blocked = blocked || bucket.Blocked
	}
	if !hasSupplierBudget {
		return ErrInvariant
	}
	if blocked {
		return ErrQuotaExceeded
	}
	return nil
}
