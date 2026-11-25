package server

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
)

var sqlLikeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeSQLLike(s string) string {
	if len(s) > 128 {
		s = strings.ToValidUTF8(s[:128], "")
	}
	return sqlLikeEscaper.Replace(s)
}

// UnifiedLogEntry 统一调用流水与异步视频任务聚合视图项（供 Console / Admin 日志页展示）。
type UnifiedLogEntry struct {
	Kind             string          `json:"kind"` // invocation | task
	ID               uint64          `json:"id"`
	RequestID        string          `json:"request_id,omitempty"`
	TaskID           string          `json:"task_id,omitempty"`
	UpstreamTaskID   string          `json:"upstream_task_id,omitempty"`
	OrgID            uint64          `json:"org_id,omitempty"`
	UserID           *uint64         `json:"user_id,omitempty"`
	APIKeyID         uint64          `json:"api_key_id"`
	Model            string          `json:"model"`
	Modality         string          `json:"modality,omitempty"`
	Protocol         string          `json:"protocol,omitempty"`
	Status           string          `json:"status"`
	StatusCode       int             `json:"status_code,omitempty"`
	IsStream         bool            `json:"is_stream"`
	PromptTokens     int64           `json:"prompt_tokens"`
	CompletionTokens int64           `json:"completion_tokens"`
	CachedTokens     int64           `json:"cached_tokens"`
	ReasoningTokens  int64           `json:"reasoning_tokens"`
	CostUSD          decimal.Decimal `json:"cost_usd"`
	UsageSource      string          `json:"usage_source,omitempty"`
	ForwardRuleEID   string          `json:"forward_rule_eid,omitempty"`
	BillingRulePID   string          `json:"billing_rule_pid,omitempty"`
	BillingDetail    string          `json:"billing_detail,omitempty"`
	ResultURL        string          `json:"result_url,omitempty"`
	FailReason       string          `json:"fail_reason,omitempty"`
	ErrorBrief       string          `json:"error_brief,omitempty"`
	FRTMs            int             `json:"frt_ms"`
	DurationMs       int             `json:"duration_ms"`
	CreatedAt        time.Time       `json:"created_at"`
	// 以下三字段仅 Admin 视角返回；租户视角恒零值不回显（渠道隔离红线）。
	ChannelID         uint64           `json:"channel_id,omitempty"`
	Provider          string           `json:"provider,omitempty"`
	UpstreamCostRatio *decimal.Decimal `json:"upstream_cost_ratio,omitempty"`
}

func (s *Server) getUnifiedLogs(c *gin.Context) {
	claims := mustClaims(c)
	s.handleUnifiedLogs(c, claims.OrgID, false)
}

func (s *Server) adminListUnifiedLogs(c *gin.Context) {
	var orgID uint64
	if raw := strings.TrimSpace(c.Query("org_id")); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid org_id")
			return
		}
		orgID = v
	}
	s.handleUnifiedLogs(c, orgID, true)
}

func (s *Server) handleUnifiedLogs(c *gin.Context, orgID uint64, admin bool) {
	kindFilter := strings.ToLower(strings.TrimSpace(c.DefaultQuery("kind", "all")))
	switch kindFilter {
	case "all", "invocation", "task":
	default:
		fail(c, http.StatusBadRequest, "kind must be all|invocation|task")
		return
	}
	model := strings.TrimSpace(c.Query("model"))
	status := strings.TrimSpace(c.Query("status"))
	protocol := strings.TrimSpace(c.Query("protocol"))
	modality := strings.TrimSpace(c.Query("modality"))
	q := strings.TrimSpace(c.Query("q"))

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	page = max(1, page)
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize < 1 {
		pageSize = 20
	}
	pageSize = min(100, pageSize)
	fetchLimit := min(1000, page*pageSize+50)

	rdb := s.readOnly().WithContext(c.Request.Context())
	likePattern := ""
	if q != "" {
		likePattern = "%" + escapeSQLLike(q) + "%"
	}

	taskByID := make(map[string]task.Task)
	taskRows := []task.Task{}
	if (kindFilter == "all" || kindFilter == "task") && protocol == "" && (modality == "" || modality == "video") {
		tq := rdb.Model(&task.Task{})
		if orgID > 0 {
			tq = tq.Where("org_id = ?", orgID)
		}
		if model != "" {
			tq = tq.Where("model = ?", model)
		}
		if status != "" {
			tq = tq.Where("status = ?", status)
		}
		if likePattern != "" {
			tq = tq.Where(`task_id LIKE ? ESCAPE '\' OR upstream_task_id LIKE ? ESCAPE '\'`, likePattern, likePattern)
		}
		if err := tq.Order("submitted_at DESC, id DESC").Limit(fetchLimit).Find(&taskRows).Error; err != nil {
			fail(c, http.StatusInternalServerError, "query unified task logs failed")
			return
		}
		for _, t := range taskRows {
			taskByID[t.TaskID] = t
		}
	}

	invRows := []invocation.Invocation{}
	if kindFilter == "all" || kindFilter == "invocation" {
		iq := rdb.Model(&invocation.Invocation{})
		if orgID > 0 {
			iq = iq.Where("org_id = ?", orgID)
		}
		if model != "" {
			iq = iq.Where("model = ?", model)
		}
		if status != "" {
			iq = iq.Where("status = ?", status)
		}
		if protocol != "" {
			iq = iq.Where("protocol = ?", protocol)
		}
		if modality != "" {
			iq = iq.Where("modality = ?", modality)
		}
		if likePattern != "" {
			iq = iq.Where(`request_id LIKE ? ESCAPE '\' OR upstream_request_id LIKE ? ESCAPE '\'`, likePattern, likePattern)
		}
		if err := iq.Order("created_at DESC, id DESC").Limit(fetchLimit).Find(&invRows).Error; err != nil {
			fail(c, http.StatusInternalServerError, "query unified invocation logs failed")
			return
		}
	}

	seenTaskIDs := make(map[string]struct{}, len(taskRows))
	entries := make([]UnifiedLogEntry, 0, len(invRows)+len(taskRows))

	for _, inv := range invRows {
		item := UnifiedLogEntry{
			Kind:             "invocation",
			ID:               inv.ID,
			RequestID:        inv.RequestID,
			OrgID:            inv.OrgID,
			UserID:           inv.UserID,
			APIKeyID:         inv.APIKeyID,
			Model:            inv.Model,
			Modality:         inv.Modality,
			Protocol:         inv.Protocol,
			Status:           inv.Status,
			StatusCode:       inv.StatusCode,
			IsStream:         inv.IsStream,
			PromptTokens:     inv.PromptTokens,
			CompletionTokens: inv.CompletionTokens,
			CachedTokens:     inv.CachedTokens,
			ReasoningTokens:  inv.ReasoningTokens,
			CostUSD:          inv.CostUSD,
			UsageSource:      inv.UsageSource,
			ForwardRuleEID:   inv.ForwardRuleEID,
			BillingRulePID:   inv.BillingRulePID,
			ErrorBrief:       inv.ErrorBrief,
			FRTMs:            inv.FRTMs,
			DurationMs:       inv.DurationMs,
			CreatedAt:        inv.CreatedAt,
		}
		if admin {
			item.ChannelID = inv.ChannelID
			item.Provider = inv.Provider
			r := inv.UpstreamCostRatio
			item.UpstreamCostRatio = &r
		} else {
			item.OrgID = 0
		}
		if t, ok := taskByID[inv.RequestID]; ok {
			seenTaskIDs[t.TaskID] = struct{}{}
			item.Kind = "task"
			item.TaskID = t.TaskID
			item.UpstreamTaskID = t.UpstreamTaskID
			item.ResultURL = t.ResultURL
			item.FailReason = t.FailReason
			item.BillingDetail = t.BillingDetail
			item.ForwardRuleEID = cmp.Or(item.ForwardRuleEID, t.ForwardRuleEID)
			item.BillingRulePID = cmp.Or(item.BillingRulePID, t.BillingRulePID)
		}
		entries = append(entries, item)
	}

	for _, t := range taskRows {
		if _, seen := seenTaskIDs[t.TaskID]; seen {
			continue
		}
		durMs := 0
		if t.FinishedAt != nil {
			durMs = max(0, int(t.FinishedAt.Sub(t.SubmittedAt).Milliseconds()))
		}
		item := UnifiedLogEntry{
			Kind:             "task",
			ID:               t.ID,
			TaskID:           t.TaskID,
			UpstreamTaskID:   t.UpstreamTaskID,
			OrgID:            t.OrgID,
			UserID:           t.UserID,
			APIKeyID:         t.APIKeyID,
			Model:            t.Model,
			Modality:         "video",
			Status:           t.Status,
			CompletionTokens: t.ActualTokens,
			CostUSD:          t.CostUSD,
			ForwardRuleEID:   t.ForwardRuleEID,
			BillingRulePID:   t.BillingRulePID,
			BillingDetail:    t.BillingDetail,
			ResultURL:        t.ResultURL,
			FailReason:       t.FailReason,
			DurationMs:       durMs,
			CreatedAt:        t.SubmittedAt,
		}
		if admin {
			item.ChannelID = t.ChannelID
			item.Provider = t.Provider
		} else {
			item.OrgID = 0
		}
		entries = append(entries, item)
	}

	slices.SortFunc(entries, func(a, b UnifiedLogEntry) int {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return b.CreatedAt.Compare(a.CreatedAt)
		}
		return cmp.Compare(b.ID, a.ID)
	})

	totalCost := decimal.Zero
	for _, it := range entries {
		totalCost = totalCost.Add(it.CostUSD)
	}

	total := len(entries)
	start := min((page-1)*pageSize, total)
	end := min(start+pageSize, total)

	c.JSON(http.StatusOK, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"summary": gin.H{
			"total_cost_usd": totalCost,
			"count":          total,
		},
		"items": entries[start:end],
	})
}
