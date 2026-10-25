package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrQueryInvalidScope  = errors.New("invocation_query_invalid_scope")
	ErrQueryInvalidFilter = errors.New("invocation_query_invalid_filter")
	ErrQueryInvalidPage   = errors.New("invocation_query_invalid_page")
	ErrQueryUnavailable   = errors.New("invocation_query_unavailable")
)

const (
	queryTimeout   = 3 * time.Second
	queryMaxOffset = 100000
)

// QueryScope is authority supplied by the authenticated host, not by a filter.
// Its zero value is deliberately unusable.
type QueryScope struct {
	kind  uint8
	orgID uint64
}

func OrganizationQueryScope(orgID uint64) QueryScope { return QueryScope{kind: 1, orgID: orgID} }

// AdminQueryScope is only for a host that has checked current platform-admin
// authority. Possessing a filter with OrgID unset never grants this scope.
func AdminQueryScope() QueryScope { return QueryScope{kind: 2} }

// QueryFilter contains normalized, exact-match metadata filters. Times are an
// absolute half-open interval; Timezone is used only to enforce the calendar-day
// limit. A nil Modality means any modality, while a pointer to "" means historical
// records without modality. Zero optional IDs mean no filter.
type QueryFilter struct {
	Since     time.Time
	Until     time.Time
	Timezone  string
	Model     string
	RequestID string
	APIKeyID  uint64
	OrgID     uint64
	ChannelID uint64
	Modality  *string
	Result    string
	Status    string
	IsStream  *bool
}

type QueryTotals struct {
	RecordCount      int64  `json:"record_count"`
	SuccessCount     int64  `json:"success_count"`
	NonSuccessCount  int64  `json:"non_success_count"`
	PromptTokens     string `json:"prompt_tokens"`
	CompletionTokens string `json:"completion_tokens"`
	CachedTokens     string `json:"cached_tokens"`
	ReasoningTokens  string `json:"reasoning_tokens"`
	CostUSD          string `json:"cost_usd"`
}

type QueryResult struct {
	Items  []Invocation
	Totals QueryTotals
	// AsOf is an observation time, not a reusable database snapshot identifier.
	AsOf time.Time
}

type queryFailure struct{ cause error }

func (e *queryFailure) Error() string        { return ErrQueryUnavailable.Error() }
func (e *queryFailure) Unwrap() error        { return e.cause }
func (e *queryFailure) Is(target error) bool { return target == ErrQueryUnavailable }

// QueryLogs reads aggregates and a page from the same read-only RR transaction.
// It never retries against another DB handle and never returns partial success.
// The host's deadline includes its preceding FX read; this additional bound only
// shortens an unbounded direct caller and cannot extend the host's deadline.
func QueryLogs(ctx context.Context, db *gorm.DB, scope QueryScope, filter QueryFilter, page, pageSize int) (QueryResult, error) {
	return queryLogs(ctx, db, scope, filter, page, pageSize, false)
}

func queryLogs(ctx context.Context, db *gorm.DB, scope QueryScope, filter QueryFilter, page, pageSize int, export bool) (QueryResult, error) {
	// Take ownership of optional values before either query is built.
	if filter.Modality != nil {
		modality := *filter.Modality
		filter.Modality = &modality
	}
	if filter.IsStream != nil {
		stream := *filter.IsStream
		filter.IsStream = &stream
	}
	if err := validateQuery(scope, filter, page, pageSize); err != nil {
		return QueryResult{}, err
	}
	if ctx == nil || db == nil || db.Dialector == nil {
		return QueryResult{}, ErrQueryUnavailable
	}
	if dialect := db.Dialector.Name(); dialect != "mysql" && dialect != "sqlite" {
		return QueryResult{}, ErrQueryUnavailable
	}
	// A nested GORM transaction would use a savepoint in the caller's unknown
	// isolation/read-write transaction instead of establishing this contract.
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return QueryResult{}, ErrQueryUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return QueryResult{}, &queryFailure{cause: err}
	}
	result := QueryResult{Items: make([]Invocation, 0), AsOf: time.Now().UTC()}
	// Parent connections use a warning logger. Slow/error SQL must not reveal
	// client-supplied filters, even when the returned error is safely wrapped.
	err := db.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		prefix, err := querySelectPrefix(ctx, tx.Dialector.Name())
		if err != nil {
			return err
		}
		var aggregate queryAggregate
		// CAST to character data prevents both a SUM(BIGINT) > int64 and a
		// DECIMAL SUM from passing through a Go float or integer accumulator.
		if err := queryPredicate(tx, scope, filter).Select(prefix+queryAggregateSQL, StatusSuccess).
			Find(&aggregate).Error; err != nil {
			return err
		}
		totals, err := aggregate.totals()
		if err != nil {
			return err
		}
		if export && totals.RecordCount > QueryExportMaxRows {
			return ErrQueryExportLimit
		}
		prefix, err = querySelectPrefix(ctx, tx.Dialector.Name())
		if err != nil {
			return err
		}
		// Start a fresh chain: the aggregate's select/destination/order cannot
		// leak into this query. Both chains use this transaction and predicate.
		columns, limit, offset := "*", pageSize, (page-1)*pageSize
		if export {
			columns, limit, offset = queryExportColumns, QueryExportMaxRows, 0
		}
		if err := queryPredicate(tx, scope, filter).Select(prefix + columns).Order("id DESC").
			Limit(limit).Offset(offset).Find(&result.Items).Error; err != nil {
			return err
		}
		if export && int64(len(result.Items)) != totals.RecordCount {
			return ErrQueryUnavailable
		}
		result.Totals = totals
		return ctx.Err()
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return QueryResult{}, &queryFailure{cause: err}
	}
	return result, nil
}

func validateQuery(scope QueryScope, filter QueryFilter, page, pageSize int) error {
	if (scope.kind != 1 && scope.kind != 2) || (scope.kind == 1 && scope.orgID == 0) || (scope.kind == 2 && scope.orgID != 0) {
		return ErrQueryInvalidScope
	}
	if scope.kind == 1 && (filter.OrgID != 0 || filter.ChannelID != 0) {
		return ErrQueryInvalidScope
	}
	if page < 1 || (pageSize != 20 && pageSize != 50 && pageSize != 100) || (page-1) > queryMaxOffset/pageSize {
		return ErrQueryInvalidPage
	}
	if filter.Timezone == "" || filter.Timezone == "Local" || len(filter.Timezone) > 128 {
		return ErrQueryInvalidFilter
	}
	location, err := time.LoadLocation(filter.Timezone)
	if err != nil || filter.Since.IsZero() || filter.Until.IsZero() || !filter.Since.Before(filter.Until) {
		return ErrQueryInvalidFilter
	}
	if filter.Until.After(filter.Since.In(location).AddDate(0, 0, 90)) {
		return ErrQueryInvalidFilter
	}
	if !utf8.ValidString(filter.Model) || utf8.RuneCountInString(filter.Model) > 128 ||
		!utf8.ValidString(filter.RequestID) || utf8.RuneCountInString(filter.RequestID) > 32 {
		return ErrQueryInvalidFilter
	}
	if filter.Result != "" && filter.Result != "success" && filter.Result != "failed" {
		return ErrQueryInvalidFilter
	}
	if filter.Result != "" && filter.Status != "" {
		return ErrQueryInvalidFilter
	}
	if filter.Status != "" && filter.Status != StatusSuccess && filter.Status != StatusUpstreamError &&
		filter.Status != StatusNoChannel && filter.Status != StatusInsufficientBalance {
		return ErrQueryInvalidFilter
	}
	if filter.Modality != nil {
		switch *filter.Modality {
		case "", ModalityChat, ModalityEmbeddings, ModalityRerank, ModalityImage, ModalityAudioSpeech,
			ModalityAudioSTT, ModalityRealtime, ModalityVideo, ModalityMusic:
		default:
			return ErrQueryInvalidFilter
		}
	}
	return nil
}

func queryPredicate(tx *gorm.DB, scope QueryScope, filter QueryFilter) *gorm.DB {
	q := tx.Model(&Invocation{}).Where("created_at >= ? AND created_at < ?", filter.Since.UTC(), filter.Until.UTC())
	if scope.kind == 1 {
		q = q.Where("org_id = ?", scope.orgID)
	} else if filter.OrgID != 0 {
		q = q.Where("org_id = ?", filter.OrgID)
	}
	if filter.ChannelID != 0 {
		q = q.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.APIKeyID != 0 {
		q = q.Where("api_key_id = ?", filter.APIKeyID)
	}
	if filter.Model != "" {
		q = q.Where("model = ?", filter.Model)
	}
	if filter.RequestID != "" {
		q = q.Where("request_id = ?", filter.RequestID)
	}
	if filter.Modality != nil {
		q = q.Where("modality = ?", *filter.Modality)
	}
	if filter.Result == "success" {
		q = q.Where("status = ?", StatusSuccess)
	} else if filter.Result == "failed" {
		q = q.Where("status <> ?", StatusSuccess)
	} else if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.IsStream != nil {
		q = q.Where("is_stream = ?", *filter.IsStream)
	}
	return q
}

func querySelectPrefix(ctx context.Context, dialect string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if dialect != "mysql" {
		return "", nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return "", ErrQueryUnavailable
	}
	// Zero disables MySQL's limit; do not round a sub-millisecond remainder up
	// past the remaining deadline or accidentally emit MAX_EXECUTION_TIME(0).
	milliseconds := time.Until(deadline).Milliseconds()
	if milliseconds < 1 {
		return "", context.DeadlineExceeded
	}
	if milliseconds > queryTimeout.Milliseconds() {
		milliseconds = queryTimeout.Milliseconds()
	}
	return fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ ", milliseconds), nil
}

const queryAggregateSQL = `COUNT(*) AS record_count,
COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS success_count,
CAST(COALESCE(SUM(prompt_tokens), 0) AS CHAR) AS prompt_tokens,
CAST(COALESCE(SUM(completion_tokens), 0) AS CHAR) AS completion_tokens,
CAST(COALESCE(SUM(cached_tokens), 0) AS CHAR) AS cached_tokens,
CAST(COALESCE(SUM(reasoning_tokens), 0) AS CHAR) AS reasoning_tokens,
CAST(COALESCE(SUM(cost_usd), 0) AS CHAR) AS cost_usd`

type queryAggregate struct {
	RecordCount      int64
	SuccessCount     int64
	PromptTokens     string
	CompletionTokens string
	CachedTokens     string
	ReasoningTokens  string
	CostUSD          string
}

func (a queryAggregate) totals() (QueryTotals, error) {
	if a.RecordCount < 0 || a.SuccessCount < 0 || a.SuccessCount > a.RecordCount {
		return QueryTotals{}, ErrQueryUnavailable
	}
	out := QueryTotals{RecordCount: a.RecordCount, SuccessCount: a.SuccessCount, NonSuccessCount: a.RecordCount - a.SuccessCount}
	values := []struct {
		raw     string
		target  *string
		integer bool
	}{
		{a.PromptTokens, &out.PromptTokens, true},
		{a.CompletionTokens, &out.CompletionTokens, true},
		{a.CachedTokens, &out.CachedTokens, true},
		{a.ReasoningTokens, &out.ReasoningTokens, true},
		{a.CostUSD, &out.CostUSD, false},
	}
	for _, value := range values {
		d, err := decimal.NewFromString(value.raw)
		if err != nil || (value.integer && !d.Equal(d.Truncate(0))) {
			return QueryTotals{}, ErrQueryUnavailable
		}
		*value.target = d.String()
	}
	return out, nil
}
