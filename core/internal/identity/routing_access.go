package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrRoutingAccessInvalid     = errors.New("identity: invalid routing access")
	ErrRoutingAccessUnavailable = errors.New("identity: routing access unavailable")
)

// RoutingOrganization is the only organization fact exposed to routing policy.
// An unknown or disabled organization status is never interpreted as active.
type RoutingOrganization struct {
	ID     uint64
	Active bool
}

// LockRoutingOrganizations performs current UPDATE reads in the caller's real
// transaction. It never commits, opens a transaction, or reads other domains.
// IDs must already be positive, strictly ascending and at most 100. Missing
// rows are omitted so the caller can distinguish absence from a disabled row.
func LockRoutingOrganizations(ctx context.Context, tx *gorm.DB, ids []uint64) ([]RoutingOrganization, error) {
	return readRoutingOrganizations(ctx, tx, ids, true)
}

// ReadRoutingOrganizations participates in the caller's read-only snapshot;
// unlike the command bridge, this function does not acquire UPDATE locks.
func ReadRoutingOrganizations(ctx context.Context, tx *gorm.DB, ids []uint64) ([]RoutingOrganization, error) {
	return readRoutingOrganizations(ctx, tx, ids, false)
}

func readRoutingOrganizations(ctx context.Context, tx *gorm.DB, ids []uint64, lock bool) ([]RoutingOrganization, error) {
	if err := validateRoutingAccess(ctx, tx, ids); err != nil {
		return nil, err
	}
	out := make([]RoutingOrganization, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	columns := "id, status"
	if !lock {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		var err error
		columns, err = routingSelectColumns(ctx, tx, columns)
		if err != nil {
			return nil, err
		}
	}
	// NewDB clears caller query clauses but keeps the identical transaction
	// connection. Discard also prevents inherited Warn loggers leaking SQL.
	q := tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).
		Table("organizations").Select(columns).Where("id IN ?", ids).Order("id ASC")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []struct {
		ID     uint64
		Status int8
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, routingAccessError(ctx, err)
	}
	for _, row := range rows {
		out = append(out, RoutingOrganization{ID: row.ID, Active: row.Status == 1})
	}
	return out, nil
}

func validateRoutingAccess(ctx context.Context, tx *gorm.DB, ids []uint64) error {
	if ctx == nil || tx == nil || tx.Config == nil || tx.Statement == nil || tx.DryRun || len(ids) > 100 {
		return ErrRoutingAccessInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrRoutingAccessInvalid
	}
	for i, id := range ids {
		if id == 0 || i > 0 && id <= ids[i-1] {
			return ErrRoutingAccessInvalid
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx.Error != nil {
		return routingAccessError(ctx, tx.Error)
	}
	return nil
}

func routingSelectColumns(ctx context.Context, tx *gorm.DB, columns string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if tx.Dialector.Name() != "mysql" {
		return columns, nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return "", ErrRoutingAccessInvalid
	}
	milliseconds := min(int64(200), time.Until(deadline).Milliseconds())
	if milliseconds < 1 {
		return "", context.DeadlineExceeded
	}
	// Only a locally bounded integer and a constant whitelist reach SQL text.
	return fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", milliseconds, columns), nil
}

func routingAccessError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrRoutingAccessUnavailable
}
