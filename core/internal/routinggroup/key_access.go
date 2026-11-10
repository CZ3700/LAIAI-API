package routinggroup

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ActiveKeyGroupTx checks the current group and organization grant. Callers
// must perform their model/channel and action checks separately; an active
// grant alone is never permission to send an upstream request.
func ActiveKeyGroupTx(tx *gorm.DB, orgID, groupID uint64) error {
	if tx == nil || orgID == 0 || groupID == 0 {
		return ErrInvalid
	}
	var group Group
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id, owner_org_id, status, policy_version").
		Where("id = ? AND (owner_org_id = 0 OR owner_org_id = ?)", groupID, orgID).Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrInactive
	}
	if err != nil {
		return err
	}
	if group.ID != groupID || group.PolicyVersion == 0 || group.Status != Enabled {
		return ErrInactive
	}
	var grant Grant
	err = tx.Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id, org_id, group_id, status, grant_version").
		Where("org_id = ? AND group_id = ?", orgID, groupID).Take(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrInactive
	}
	if err != nil {
		return err
	}
	if grant.OrgID != orgID || grant.GroupID != groupID || grant.GrantVersion == 0 || grant.Status != Enabled {
		return ErrInactive
	}
	return nil
}

// DefaultKeyGroupTx resolves the administrator's default pointer. Issuance
// callers hold the organization row lock, which serializes default changes.
// An absent pointer preserves legacy issuance; an invalid pointer fails closed.
func DefaultKeyGroupTx(tx *gorm.DB, orgID uint64) (*uint64, error) {
	if tx == nil || orgID == 0 {
		return nil, ErrInvalid
	}
	// Discover the pointer without a row lock, then lock Group before Grant.
	// Administrative writes use the same order. The final current-read below
	// rejects a pointer switched after discovery.
	var discovered Grant
	err := tx.Select("id, group_id").
		Where("org_id = ? AND default_slot = 1", orgID).Take(&discovered).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if discovered.GroupID == 0 {
		return nil, ErrInactive
	}
	if err := ActiveKeyGroupTx(tx, orgID, discovered.GroupID); err != nil {
		return nil, err
	}
	var grant Grant
	err = tx.Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id, org_id, group_id, status, grant_version, default_slot").
		Where("id = ? AND org_id = ?", discovered.ID, orgID).Take(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInactive
	}
	if err != nil {
		return nil, err
	}
	if grant.OrgID != orgID || grant.GroupID == 0 || grant.DefaultSlot == nil || *grant.DefaultSlot != 1 || grant.Status != Enabled || grant.GrantVersion == 0 {
		return nil, ErrInactive
	}
	if grant.GroupID != discovered.GroupID {
		return nil, ErrInactive
	}
	id := grant.GroupID
	return &id, nil
}
