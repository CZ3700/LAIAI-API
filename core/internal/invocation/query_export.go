package invocation

import (
	"context"
	"errors"

	"gorm.io/gorm"
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
