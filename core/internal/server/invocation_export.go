package server

import (
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
)

const invocationExportMaxBytes = 8 << 20

var errInvocationExportSize = errors.New("invocation export size exceeded")

func (s *Server) dispatchInvocationExportV1(c *gin.Context) bool {
	if !hasInvocationQueryVersion(c.Request.URL.RawQuery) {
		return false
	}
	s.handleInvocationExportV1(c, false)
	return true
}

func parseInvocationExport(values url.Values, admin bool, now time.Time) (invocationQuerySpec, error) {
	for _, name := range []string{"page", "page_size"} {
		if values.Has(name) {
			return invocationQuerySpec{}, invalidInvocationFilter(name, "pagination_not_supported")
		}
	}
	return parseInvocationQuery(values, admin, now)
}

func invocationExportFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, invocation.ErrQueryExportLimit):
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": "too many matching invocation records; narrow the filters", "code": "export_limit_exceeded"})
	case errors.Is(err, errInvocationExportSize):
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": "invocation export is too large; narrow the filters", "code": "export_size_exceeded"})
	default:
		invocationQueryFailure(c, err)
	}
}

func (s *Server) handleInvocationExportV1(c *gin.Context, admin bool) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if len(c.Request.URL.RawQuery) > 8192 {
		invocationExportFailure(c, invalidInvocationFilter("query", "query_too_long"))
		return
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		invocationExportFailure(c, invalidInvocationFilter("query", "invalid_encoding"))
		return
	}
	spec, err := parseInvocationExport(values, admin, s.now())
	if err != nil {
		invocationExportFailure(c, err)
		return
	}
	subject, orgID, ok := invocationQueryIdentity(c, admin)
	if !ok {
		fail(c, http.StatusForbidden, "invocation query access required")
		return
	}
	release, ok := s.invocationQueries.tryAcquireExport(subject)
	if !ok {
		c.Header("Retry-After", "1")
		fail(c, http.StatusTooManyRequests, "invocation query capacity exceeded")
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(c.Request.Context(), invocationQueryTimeout)
	defer cancel()
	money, rateSource, err := s.invocationMoneySnapshot(ctx, spec.DisplayCurrency)
	if err != nil {
		invocationExportFailure(c, err)
		return
	}
	scope := invocation.OrganizationQueryScope(orgID)
	if admin {
		scope = invocation.AdminQueryScope()
	}
	result, err := invocation.QueryLogExport(ctx, s.readOnly(), scope, spec.Filter)
	if ctx.Err() != nil {
		invocationExportFailure(c, ctx.Err())
		return
	}
	if err != nil {
		invocationExportFailure(c, err)
		return
	}
	totalCost, err := decimal.NewFromString(result.Totals.CostUSD)
	if err != nil {
		invocationExportFailure(c, errors.New("invocation query unavailable"))
		return
	}
	filterHash := spec.filterHash(admin, orgID)
	encoded, err := encodeInvocationExport(ctx, result, admin, money, rateSource, filterHash)
	if err != nil {
		invocationExportFailure(c, err)
		return
	}
	// No CSV response metadata or partial body is visible until every row has
	// been read, encoded within bounds, and checked against the same deadline.
	if ctx.Err() != nil {
		invocationExportFailure(c, ctx.Err())
		return
	}
	invocationExportHeaders(c, result, money, rateSource, filterHash, totalCost)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", encoded)
}

func invocationExportHeader(admin bool) []string {
	header := []string{"time", "request_id", "model", "status", "modality", "api_key_id",
		"prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens", "cost_usd", "cost_display",
		"currency", "cny_per_usd", "rate_snapshot_at", "rate_source", "as_of", "filter_hash",
		"amount_source", "completeness", "usage_presence"}
	if admin {
		header = append(header, "org_id", "user_id", "channel_id", "provider")
	}
	return header
}

// External text is quoted as text even when a spreadsheet trims whitespace,
// control characters or a BOM before deciding whether a cell is a formula.
func invocationExportText(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\uFEFF' })
	if trimmed != "" && strings.ContainsAny(trimmed[:1], "=+-@") {
		return "'" + value
	}
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") {
		return "'" + value
	}
	return value
}

// This writer caps allocation as well as length. It does not let bytes.Buffer
// double its allocation past the limit and only then discover an oversized CSV.
type invocationExportBuffer struct {
	ctx  context.Context
	data []byte
}

func (b *invocationExportBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > invocationExportMaxBytes-len(b.data) {
		return 0, errInvocationExportSize
	}
	needed := len(b.data) + len(p)
	if cap(b.data) < needed {
		capacity := max(4096, cap(b.data)*2, needed)
		capacity = min(capacity, invocationExportMaxBytes)
		grown := make([]byte, len(b.data), capacity)
		copy(grown, b.data)
		b.data = grown
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func encodeInvocationExport(ctx context.Context, result invocation.QueryResult, admin bool, money *exportMoneySnapshot, rateSource, filterHash string) ([]byte, error) {
	buffer := &invocationExportBuffer{ctx: ctx}
	w := csv.NewWriter(buffer)
	if err := w.Write(invocationExportHeader(admin)); err != nil {
		return nil, err
	}
	for _, row := range result.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		exact := invocationUsageExact(row)
		values := []string{row.CreatedAt.UTC().Format(time.RFC3339Nano), invocationExportText(row.RequestID), invocationExportText(row.Model),
			invocationExportText(row.Status), invocationExportText(row.Modality), strconv.FormatUint(row.APIKeyID, 10),
			exact.PromptTokens, exact.CompletionTokens, exact.CachedTokens, exact.ReasoningTokens, row.CostUSD.String(), money.amount(row.CostUSD),
			money.Currency, money.CNYPerUSD, money.SnapshotAt.UTC().Format(time.RFC3339Nano), rateSource, result.AsOf.UTC().Format(time.RFC3339Nano), filterHash,
			"log_recorded_cost", "best_effort", "legacy_not_distinguishable"}
		if admin {
			userID := ""
			if row.UserID != nil {
				userID = strconv.FormatUint(*row.UserID, 10)
			}
			values = append(values, strconv.FormatUint(row.OrgID, 10), userID, strconv.FormatUint(row.ChannelID, 10), invocationExportText(row.Provider))
		}
		if err := w.Write(values); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return buffer.data, nil
}

func invocationExportHeaders(c *gin.Context, result invocation.QueryResult, money *exportMoneySnapshot, rateSource, filterHash string, totalCost decimal.Decimal) {
	totals := result.Totals
	for name, value := range map[string]string{
		"X-Invocation-Filter-Version": "1", "X-Invocation-Record-Count": strconv.FormatInt(totals.RecordCount, 10),
		"X-Invocation-Success-Count": strconv.FormatInt(totals.SuccessCount, 10), "X-Invocation-Non-Success-Count": strconv.FormatInt(totals.NonSuccessCount, 10),
		"X-Invocation-Prompt-Tokens": totals.PromptTokens, "X-Invocation-Completion-Tokens": totals.CompletionTokens,
		"X-Invocation-Cached-Tokens": totals.CachedTokens, "X-Invocation-Reasoning-Tokens": totals.ReasoningTokens,
		"X-Invocation-Cost-USD": totals.CostUSD, "X-Invocation-Cost-Display": money.amount(totalCost),
		"X-Invocation-As-Of": result.AsOf.UTC().Format(time.RFC3339Nano), "X-Invocation-Filter-Hash": filterHash,
		"X-Invocation-Amount-Source": "log_recorded_cost", "X-Invocation-Completeness": "best_effort", "X-Invocation-Usage-Presence": "legacy_not_distinguishable",
		"X-Invocation-Consistency": "response_snapshot", "X-Invocation-Cross-Request-Consistency": "live",
		"X-Display-Currency": money.Currency, "X-CNY-Per-USD": money.CNYPerUSD,
		"X-Rate-Snapshot-At": money.SnapshotAt.UTC().Format(time.RFC3339Nano), "X-Rate-Source": rateSource,
	} {
		c.Header(name, value)
	}
	c.Header("Content-Disposition", `attachment; filename="invocation-log-`+money.Currency+"-"+result.AsOf.UTC().Format("20060102T150405Z")+`.csv"`)
}
