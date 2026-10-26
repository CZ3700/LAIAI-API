package videobilling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var ErrKeyReferenceUnavailable = errors.New("video_billing: key reference lookup unavailable")

type keyReferenceContextError struct{ cause error }

func (e *keyReferenceContextError) Error() string      { return ErrKeyReferenceUnavailable.Error() }
func (e *keyReferenceContextError) Unwrap() error      { return e.cause }
func (*keyReferenceContextError) Is(target error) bool { return target == ErrKeyReferenceUnavailable }

func keyReferenceFailure(err error) error {
	if errors.Is(err, context.Canceled) {
		return &keyReferenceContextError{context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &keyReferenceContextError{context.DeadlineExceeded}
	}
	return ErrKeyReferenceUnavailable
}

// HasKeySnapshotReferenceTx protects all historical snapshots, including those
// already settled: replay and reviewed corrections still need the original Key.
// The caller MUST already hold its exact Key lock and retain it through purge.
// FOR SHARE observes committed references even after an earlier RR snapshot.
func HasKeySnapshotReferenceTx(tx *gorm.DB, orgID, keyID uint64) (bool, error) {
	if tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.Statement.Context == nil || tx.DryRun || requireTx(tx) != nil || orgID == 0 || keyID == 0 {
		return false, ErrInvalid
	}
	if tx.Dialector.Name() != "mysql" && tx.Dialector.Name() != "sqlite" {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(tx.Statement.Context, 200*time.Millisecond)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return false, keyReferenceFailure(err)
	}
	columns := "id"
	if tx.Dialector.Name() == "mysql" {
		deadline, _ := ctx.Deadline()
		ms := min(time.Until(deadline).Milliseconds(), int64(200))
		if ms < 1 {
			return false, keyReferenceFailure(context.DeadlineExceeded)
		}
		columns = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ id", ms)
	}
	var row struct{ ID uint64 }
	err := tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).Model(&Snapshot{}).
		Select(columns).Where("org_id = ? AND api_key_id = ?", orgID, keyID).
		Clauses(clause.Locking{Strength: "SHARE"}).Limit(1).Take(&row).Error
	if contextErr := ctx.Err(); contextErr != nil {
		return false, keyReferenceFailure(contextErr)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil || row.ID == 0 {
		return false, keyReferenceFailure(err)
	}
	return true, nil
}
