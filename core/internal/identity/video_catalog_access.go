package identity

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

var (
	ErrVideoCatalogInvalid     = errors.New("identity: invalid video catalog read")
	ErrVideoCatalogNotFound    = errors.New("identity: video catalog organization missing")
	ErrVideoCatalogUnavailable = errors.New("identity: video catalog facts unavailable")
)

// LegacyModelAllowed is only the legacy whitelist predicate, not authentication
// or a decision to enable a grouped key. Callers must validate that separately.
func LegacyModelAllowed(modelLimits, model string) bool {
	return routinggroup.LegacyModelAllowed(modelLimits, model)
}

// ReadVideoBillingGroupTx reads the legacy billing group, not a routing group.
// It performs one bounded non-locking SELECT on the caller's real transaction.
func ReadVideoBillingGroupTx(ctx context.Context, tx *gorm.DB, orgID uint64) (string, error) {
	if ctx == nil || tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.DryRun || orgID == 0 {
		return "", ErrVideoCatalogInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return "", ErrVideoCatalogInvalid
	}
	if dialect := tx.Dialector.Name(); dialect != "mysql" && dialect != "sqlite" {
		return "", ErrVideoCatalogInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if tx.Error != nil {
		return "", videoCatalogIdentityFailure(ctx, tx.Error)
	}
	length := "OCTET_LENGTH(group_name)"
	if tx.Dialector.Name() == "sqlite" {
		length = "LENGTH(CAST(group_name AS BLOB))"
	}
	// VARCHAR(64) can contain up to 256 UTF-8 bytes. Do not load malformed
	// oversized test/legacy data before applying the bound.
	q, cancel, err := routingCandidateSelect(ctx, tx, "id, CASE WHEN "+length+" <= 256 THEN group_name ELSE NULL END AS group_name")
	if err != nil {
		return "", err
	}
	var row struct {
		ID        uint64
		GroupName *string
	}
	err = q.Table("organizations").Where("id = ?", orgID).Take(&row).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	if err != nil {
		safe := videoCatalogIdentityFailure(q.Statement.Context, err)
		cancel()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrVideoCatalogNotFound
		}
		return "", safe
	}
	cancel() // SQL budget ends here; validation uses the caller's total budget.
	if row.ID != orgID || row.GroupName == nil || !utf8.ValidString(*row.GroupName) || utf8.RuneCountInString(*row.GroupName) > 64 {
		return "", ErrVideoCatalogUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return *row.GroupName, nil
}

func videoCatalogIdentityFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrVideoCatalogUnavailable
}
