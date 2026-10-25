package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const invocationQueryTimeout = 3 * time.Second

// Zero-value usable; bounded by four global slots, with no wait queue. Empty
// subjects are rejected and completed subjects do not accumulate map entries.
type invocationQueryGate struct {
	mu       sync.Mutex
	active   int
	subjects map[string]int
}

func (g *invocationQueryGate) tryAcquire(subject string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if subject == "" || g.active >= 4 || g.subjects[subject] >= 2 {
		return nil, false
	}
	if g.subjects == nil {
		g.subjects = make(map[string]int)
	}
	g.active++
	g.subjects[subject]++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			g.subjects[subject]--
			if g.subjects[subject] == 0 {
				delete(g.subjects, subject)
			}
		})
	}, true
}

// Detect a version key even when its value is malformed percent encoding.
// A malformed/unknown explicit version must not widen into the legacy path.
func (s *Server) dispatchInvocationQueryV1(c *gin.Context, admin bool) bool {
	for _, pair := range strings.Split(c.Request.URL.RawQuery, "&") {
		name, _, _ := strings.Cut(pair, "=")
		decoded, err := url.QueryUnescape(name)
		if err == nil && decoded == "filter_version" {
			s.handleInvocationQueryV1(c, admin)
			return true
		}
	}
	return false
}

func invocationQueryIdentity(c *gin.Context, admin bool) (subject string, orgID uint64, ok bool) {
	if admin {
		role, _ := c.Get("actor_role")
		if role != identity.PlatRoleSuperadmin {
			return "", 0, false
		}
		value, _ := c.Get("actor_id")
		uid, present := value.(uint64)
		if !present {
			return "", 0, false
		}
		if uid != 0 {
			return "user:" + strconv.FormatUint(uid, 10), 0, true
		}
		label, _ := c.Get("actor_label")
		return "root-token", 0, label == "root-token"
	}
	value, _ := c.Get("claims")
	claims, ok := value.(*identity.SessionClaims)
	if !ok || claims == nil || claims.UserID == 0 || claims.OrgID == 0 {
		return "", 0, false
	}
	return "user:" + strconv.FormatUint(claims.UserID, 10), claims.OrgID, true
}

func (s *Server) invocationMoneySnapshot(ctx context.Context, currency string) (*exportMoneySnapshot, string, error) {
	if s.readOnly() == nil {
		return nil, "", errors.New("invocation query unavailable")
	}
	var row Setting
	query := s.readOnly().Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx).Where("name = ?", "cny_per_usd")
	if query.Dialector.Name() == "mysql" {
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline).Milliseconds()
		if !ok || remaining <= 0 {
			return nil, "", errors.New("invocation query unavailable")
		}
		if remaining > invocationQueryTimeout.Milliseconds() {
			remaining = invocationQueryTimeout.Milliseconds()
		}
		query = query.Select(fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ name, value", remaining))
	}
	err := query.Take(&row).Error
	source, raw := "setting", strings.TrimSpace(row.Value)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		source, raw = "default_missing", defaultCNYPerUSD
	case err != nil:
		return nil, "", errors.New("invocation query unavailable")
	case raw == "":
		source, raw = "default_empty", defaultCNYPerUSD
	}
	rate, err := parseCNYPerUSD(raw)
	if err != nil {
		return nil, "", errors.New("invocation query unavailable")
	}
	money := &exportMoneySnapshot{Currency: currency, CNYPerUSD: rate.String(), SnapshotAt: s.now().UTC()}
	if err := money.validate(); err != nil {
		return nil, "", errors.New("invocation query unavailable")
	}
	return money, source, nil
}

func invocationQueryFailure(c *gin.Context, err error) {
	var field *invocationFilterError
	if errors.As(err, &field) {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid invocation query", "code": "invalid_query", "field": field.Field, "reason": field.Reason})
		return
	}
	fail(c, http.StatusServiceUnavailable, "invocation query unavailable")
}

func (s *Server) handleInvocationQueryV1(c *gin.Context, admin bool) {
	c.Header("Cache-Control", "private, no-store")
	if len(c.Request.URL.RawQuery) > 8192 {
		invocationQueryFailure(c, invalidInvocationFilter("query", "query_too_long"))
		return
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		invocationQueryFailure(c, invalidInvocationFilter("query", "invalid_encoding"))
		return
	}
	spec, err := parseInvocationQuery(values, admin, s.now())
	if err != nil {
		invocationQueryFailure(c, err)
		return
	}
	subject, orgID, ok := invocationQueryIdentity(c, admin)
	if !ok {
		fail(c, http.StatusForbidden, "invocation query access required")
		return
	}
	release, ok := s.invocationQueries.tryAcquire(subject)
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
		invocationQueryFailure(c, err)
		return
	}
	scope := invocation.OrganizationQueryScope(orgID)
	if admin {
		scope = invocation.AdminQueryScope()
	}
	result, err := invocation.QueryLogs(ctx, s.readOnly(), scope, spec.Filter, spec.Page, spec.PageSize)
	if err != nil || ctx.Err() != nil {
		invocationQueryFailure(c, errors.New("invocation query unavailable"))
		return
	}
	cost, err := decimal.NewFromString(result.Totals.CostUSD)
	if err != nil {
		invocationQueryFailure(c, errors.New("invocation query unavailable"))
		return
	}
	var items any = result.Items // Existing public json tags remain the organization boundary.
	if admin {
		items = invocationAdminItems(result.Items)
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items, "total": result.Totals.RecordCount, "page": spec.Page, "page_size": spec.PageSize,
		"query": gin.H{
			"filter_version": 1, "normalized_filters": spec.normalized(), "filter_hash": spec.filterHash(admin, orgID),
			"as_of": result.AsOf, "data_watermark": gin.H{"kind": "observation", "recorded_through_id": nil},
			"consistency": "response_snapshot", "cross_request_consistency": "live", "data_source": "invocation_log",
			"amount_source": "log_recorded_cost", "completeness": "best_effort",
			"display_money": gin.H{"currency": money.Currency, "cny_per_usd": money.CNYPerUSD, "rate_snapshot_at": money.SnapshotAt, "rate_source": rateSource},
		},
		"totals": gin.H{
			"record_count": result.Totals.RecordCount, "success_count": result.Totals.SuccessCount, "non_success_count": result.Totals.NonSuccessCount,
			"prompt_tokens": result.Totals.PromptTokens, "completion_tokens": result.Totals.CompletionTokens,
			"cached_tokens": result.Totals.CachedTokens, "reasoning_tokens": result.Totals.ReasoningTokens,
			"cost_usd": result.Totals.CostUSD, "cost_display": money.amount(cost),
			"usage_presence": "legacy_not_distinguishable", "missing_usage_count": nil,
		},
	})
}

func invocationAdminItems(rows []invocation.Invocation) []gin.H {
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{
			"id": r.ID, "created_at": r.CreatedAt, "request_id": r.RequestID,
			"org_id": r.OrgID, "user_id": r.UserID, "api_key_id": r.APIKeyID, "model": r.Model, "modality": r.Modality,
			"channel_id": r.ChannelID, "provider": r.Provider, "status": r.Status, "status_code": r.StatusCode, "is_stream": r.IsStream,
			"attempts": r.Attempts, "prompt_tokens": r.PromptTokens, "completion_tokens": r.CompletionTokens,
			"cached_tokens": r.CachedTokens, "reasoning_tokens": r.ReasoningTokens, "cost_usd": r.CostUSD, "usage_source": r.UsageSource,
			"frt_ms": r.FRTMs, "duration_ms": r.DurationMs, "upstream_request_id": r.UpstreamRequestID, "error_brief": r.ErrorBrief,
		})
	}
	return items
}
