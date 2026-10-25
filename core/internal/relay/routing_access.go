package relay

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
	ErrRoutingAccessInvalid     = errors.New("relay: invalid routing access")
	ErrRoutingAccessUnavailable = errors.New("relay: routing access unavailable")
)

type RoutingChannel struct {
	ID, OwnerOrgID uint64
	Status         int8
}

// RoutingChannelSummary intentionally cannot hold credentials, addresses,
// model mappings, or any other provider configuration.
type RoutingChannelSummary struct {
	ID, OwnerOrgID uint64
	Name, Provider string
	Status         int8
}

// LockRoutingChannels performs current UPDATE reads on the supplied real
// transaction. Missing channels are omitted (a dangling binding can be removed).
// IDs must already be positive, strictly ascending and at most 100.
func LockRoutingChannels(ctx context.Context, tx *gorm.DB, ids []uint64) ([]RoutingChannel, error) {
	if err := validateRoutingAccess(ctx, tx, ids); err != nil {
		return nil, err
	}
	out := make([]RoutingChannel, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q := tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).
		Table("channels").Select("id, owner_org_id, status").Where("id IN ?", ids).
		Order("id ASC").Clauses(clause.Locking{Strength: "UPDATE"})
	if err := q.Find(&out).Error; err != nil {
		return nil, routingAccessError(ctx, err)
	}
	return out, nil
}

// ReadRoutingChannelSummaries is one bounded SELECT in the caller's read-only
// snapshot, never a per-channel lookup or a separate transaction.
func ReadRoutingChannelSummaries(ctx context.Context, tx *gorm.DB, ids []uint64) ([]RoutingChannelSummary, error) {
	if err := validateRoutingAccess(ctx, tx, ids); err != nil {
		return nil, err
	}
	out := make([]RoutingChannelSummary, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	columns, err := routingSelectColumns(ctx, tx, "id, name, provider, status, owner_org_id")
	if err != nil {
		return nil, err
	}
	q := tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).
		Table("channels").Select(columns).Where("id IN ?", ids).Order("id ASC")
	if err := q.Find(&out).Error; err != nil {
		return nil, routingAccessError(ctx, err)
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
