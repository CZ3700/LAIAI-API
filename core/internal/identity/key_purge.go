package identity

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrKeyPurgeActive     = errors.New("identity: revoke key before purge")
	ErrKeyPurgeReferenced = errors.New("identity: key retained for billing history")
)

// KeyPurgeReferenceGuard reads durable references in the caller's transaction.
// The host must supply the billing domain's guard; nil never permits deletion.
type KeyPurgeReferenceGuard func(tx *gorm.DB, orgID, keyID uint64) (bool, error)

// PurgeRevokedKeyTx serializes with billing reservation/settlement on the Key.
// It never locks Task or ledger rows. The caller owns the transaction and must
// return any error so a failed command cannot be committed independently.
func PurgeRevokedKeyTx(tx *gorm.DB, orgID, keyID uint64, guard KeyPurgeReferenceGuard) error {
	if tx == nil || tx.Config == nil || tx.Statement == nil || tx.Statement.Context == nil || tx.Dialector == nil || tx.DryRun || orgID == 0 || keyID == 0 || guard == nil {
		return ErrKeyInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrKeyInvalid
	}
	if tx.Dialector.Name() != "mysql" && tx.Dialector.Name() != "sqlite" {
		return ErrKeyInvalid
	}
	q := tx.Session(&gorm.Session{NewDB: true, Context: tx.Statement.Context, Logger: logger.Discard})
	var key struct {
		ID, OrgID uint64
		Status    int8
	}
	if err := q.Model(&APIKey{}).Select("id, org_id, status").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND org_id = ?", keyID, orgID).Take(&key).Error; err != nil {
		return err
	}
	if key.ID != keyID || key.OrgID != orgID {
		return ErrKeyInvalid
	}
	if key.Status == StatusActive {
		return ErrKeyPurgeActive
	}
	if key.Status != StatusDisabled {
		return ErrKeyInvalid
	}
	referenced, err := guard(q, orgID, keyID)
	if err != nil {
		return err
	}
	if referenced {
		return ErrKeyPurgeReferenced
	}
	result := q.Where("id = ? AND org_id = ? AND status = ?", keyID, orgID, StatusDisabled).Delete(&APIKey{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrKeyInvalid
	}
	return nil
}
