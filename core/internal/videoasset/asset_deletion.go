package videoasset

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A resource may only expose receipts belonging to its original immutable scope.
func resourceOperation(tx *gorm.DB, org uint64, kind, id, action, operationID string, scope Scope) (*Operation, error) {
	q := tx.Where("org_id = ? AND resource_type = ? AND resource_id = ? AND action = ? AND scope_id = ? AND scope_version = ? AND account_id = ? AND region = ? AND project = ?",
		org, kind, id, action, scope.ID, scope.Version, scope.AccountID, scope.Region, scope.Project)
	if operationID != "" {
		q = q.Where("operation_id = ?", operationID)
	}
	var op Operation
	if err := q.Order("CASE WHEN status IN ('pending', 'in_flight', 'unknown') THEN 0 ELSE 1 END").Order("created_at DESC, operation_id DESC").Take(&op).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, ErrStorage
	}
	return &op, nil
}

func (s *Service) assetDeletionState(tx *gorm.DB, a Asset, g Group) (string, string, *Operation, error) {
	block := func(reason string, latest *Operation) (string, string, *Operation, error) {
		return "blocked", reason, latest, nil
	}
	if a.OrgID != g.OrgID || a.GroupID != g.ID || a.ScopeID != g.ScopeID || a.ScopeVersion != g.ScopeVersion {
		return block("resource_identity_mismatch", nil)
	}
	latest, err := resourceOperation(tx, a.OrgID, "asset", a.ID, "DeleteAsset", "", groupScope(g))
	if err != nil {
		return "", "", nil, err
	}
	var pending []Operation
	if err := tx.Where("org_id = ? AND resource_type = ? AND resource_id = ? AND status IN ?", a.OrgID, "asset", a.ID,
		[]string{"pending", "in_flight", "unknown"}).Find(&pending).Error; err != nil {
		return "", "", nil, ErrStorage
	}
	for _, op := range pending {
		if op.Status == "unknown" {
			return block("submission_uncertain", latest)
		}
	}
	if len(pending) > 0 {
		return block("operation_pending", latest)
	}
	var count int64
	if err := tx.Model(&TaskReference{}).Where("org_id = ? AND asset_id = ? AND released_at IS NULL", a.OrgID, a.ID).Count(&count).Error; err != nil {
		return "", "", nil, ErrStorage
	}
	if count > 0 {
		return block("resource_in_use", latest)
	}
	if a.ProviderID == nil {
		if a.Status != StatusFailed || a.ReviewState != ReviewClear || a.CreateOperationID == "" {
			return block("creation_not_rejected", latest)
		}
		created, err := resourceOperation(tx, a.OrgID, "asset", a.ID, "CreateAsset", a.CreateOperationID, groupScope(g))
		if err != nil {
			return "", "", nil, err
		}
		if created == nil || created.Status != "rejected" {
			return block("creation_not_rejected", latest)
		}
		return "local_failed", "", latest, nil
	}
	if !identifier.MatchString(*a.ProviderID) || (a.Status != StatusActive && a.Status != StatusFailed) {
		return block("asset_not_ready", latest)
	}
	if g.DeletedAt != nil || g.Status != StatusActive || g.ReviewState != ReviewClear {
		return block("group_not_ready", latest)
	}
	return "official", "", latest, nil
}

func (s *Service) deleteFailedAsset(ctx context.Context, c CreateContext, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, c.OrgID); err != nil {
			return err
		}
		var a Asset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND asset_id = ?", c.OrgID, id).Take(&a).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return ErrStorage
		}
		if a.DeletedAt != nil {
			return nil
		}
		var g Group
		if err := tx.Where("org_id = ? AND group_id = ?", c.OrgID, a.GroupID).Take(&g).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return ErrStorage
		}
		mode, reason, _, err := s.assetDeletionState(tx, a, g)
		if err != nil {
			return err
		}
		if mode != "local_failed" {
			return deletionBlockError(reason)
		}
		now := nowMillis(s)
		res := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ? AND row_version = ? AND deleted_at IS NULL", c.OrgID, id, a.RowVersion).
			Updates(map[string]any{"status": StatusDeleted, "deleted_at": now, "row_version": gorm.Expr("row_version + 1"), "updated_at": now})
		if res.Error != nil || res.RowsAffected != 1 {
			return ErrStorage
		}
		return s.event(tx, c.OrgID, "asset", id, a.CreateOperationID, "local_failed_asset_deleted", "", callerLabel(c))
	})
}
