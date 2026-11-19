package invocation

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const QueryExportMaxRows = 5000

var ErrQueryExportLimit = errors.New("invocation_export_limit_exceeded")

// Export only metadata needed by the explicit CSV projection. In particular,
// don't fetch payload object keys, raw error text or upstream request IDs.
const queryExportColumns = "id, org_id, user_id, api_key_id, channel_id, provider, request_id, model, modality, status, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, cost_usd, created_at"

// QueryLogExport uses the same scope, predicate and read-only snapshot as
// QueryLogs. It returns every matching row, or an error; never a truncated CSV.
// errors.Is(err, ErrQueryExportLimit) identifies the fixed 5000-row boundary.
func QueryLogExport(ctx context.Context, db *gorm.DB, scope QueryScope, filter QueryFilter) (QueryResult, error) {
	return queryLogs(ctx, db, scope, filter, 1, 20, true)
}

// QueryLogExportBatch reads a bounded page from one time slice. The cursor uses
// both sort columns so records sharing a millisecond are never skipped.
func QueryLogExportBatch(ctx context.Context, db *gorm.DB, orgID uint64, filter QueryFilter, maxID uint64, before *time.Time, beforeID uint64, limit int) ([]Invocation, error) {
	scope := OrganizationQueryScope(orgID)
	if err := validateQuery(scope, filter, 1, 20); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrQueryInvalidPage
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	prefix, err := querySelectPrefix(ctx, db.Dialector.Name())
	if err != nil {
		return nil, err
	}
	q := queryPredicate(db.Session(&gorm.Session{Context: ctx, Logger: logger.Discard}), scope, filter).
		Where("id <= ?", maxID)
	if before != nil {
		q = q.Where("(created_at < ? OR (created_at = ? AND id < ?))", before.UTC(), before.UTC(), beforeID)
	}
	var rows []Invocation
	err = q.Select(prefix + queryExportColumns).Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
