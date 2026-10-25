package videobilling

import (
	"errors"

	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func validateSnapshotSchema(s Snapshot, expected string) error {
	if err := videoruntime.ValidateBillingSchema(expected); err != nil {
		return err
	}
	if err := videoruntime.ValidateBillingSchema(s.BillingSchemaVersion); err != nil {
		return err
	}
	if s.BillingSchemaVersion != expected {
		return videoruntime.ErrUnsupported
	}
	return nil
}

// WithTaskSnapshotContract scopes a host task candidate query using its fixed
// internal alias v. Keeping the financial table and identity join here prevents
// the host from interpreting or querying billing storage directly. No alias or
// SQL fragment is accepted from request data.
func WithTaskSnapshotContract(db *gorm.DB) *gorm.DB {
	return db.Joins("JOIN video_billing_snapshots bs ON bs.id = v.billing_snapshot_id AND bs.org_id = v.org_id AND bs.task_id = v.task_id").
		Where("bs.billing_schema_version = v.billing_schema_version")
}

// ValidateSnapshotContractTx checks the immutable task/snapshot contract without
// taking key, quota or ledger locks. Hosts may call it before their asset locks;
// money entrypoints independently validate again under their existing lock order.
func ValidateSnapshotContractTx(tx *gorm.DB, org uint64, taskID string, snapshotID uint64, expectedSchema string) error {
	if err := requireTx(tx); err != nil {
		return err
	}
	if org == 0 || !validID(taskID) || snapshotID == 0 {
		return ErrInvalid
	}
	if err := videoruntime.ValidateBillingSchema(expectedSchema); err != nil {
		return err
	}
	var s Snapshot
	err := tx.Select("id", "org_id", "task_id", "billing_schema_version").Where("id = ? AND org_id = ? AND task_id = ?", snapshotID, org, taskID).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return validateSnapshotSchema(s, expectedSchema)
}

// Review needs its own check because it deliberately does not acquire the
// money-path key/snapshot locks. An old proposal never authorizes a new schema.
func validateReviewSnapshotTx(tx *gorm.DB, org uint64, taskID string) error {
	var s Snapshot
	err := tx.Select("billing_schema_version").Where("org_id = ? AND task_id = ?", org, taskID).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return videoruntime.ValidateBillingSchema(s.BillingSchemaVersion)
}
