package server

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func (s *Server) callerIsAdmin(c *gin.Context) bool {
	if _, role, _, ok := s.resolveAdmin(c); ok && identity.RoleRank(role) >= identity.RoleRank(identity.PlatRoleAdmin) {
		return true
	}
	return false
}

func (s *Server) v1TkeDashboard(c *gin.Context) {
	ctx := c.Request.Context()
	claims := mustClaims(c)
	rdb := s.readOnly().WithContext(ctx)
	isAdmin := s.callerIsAdmin(c) && !strings.EqualFold(c.Query("scope"), "self")

	now := s.now().UTC()
	startDay := now
	endDay := now
	if raw := strings.TrimSpace(c.Query("start_date")); raw != "" {
		if t, err := time.Parse("2006-01-02", raw); err == nil {
			startDay = t.UTC()
		}
	}
	if raw := strings.TrimSpace(c.Query("end_date")); raw != "" {
		if t, err := time.Parse("2006-01-02", raw); err == nil {
			endDay = t.UTC()
		}
	}
	if endDay.Before(startDay) {
		endDay = startDay
	}

	sinceDate := startDay.Format("2006-01-02")
	var periodRows []invocation.Invocation
	pq := rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", sinceDate)
	if !isAdmin {
		pq = pq.Where("org_id = ?", claims.OrgID)
	}
	_ = pq.Select("model, status, prompt_tokens, completion_tokens, cost_usd, frt_ms, duration_ms, created_at").Find(&periodRows).Error

	var totalReq, totalTok int64
	totalCost := decimal.Zero
	dailyReq := map[string]int64{}
	dailyCost := map[string]decimal.Decimal{}
	modelCnt := map[string]int64{}
	modelTok := map[string]int64{}
	modelCost := map[string]decimal.Decimal{}

	for _, r := range periodRows {
		totalReq++
		tok := r.PromptTokens + r.CompletionTokens
		totalTok += tok
		totalCost = totalCost.Add(r.CostUSD)
		day := r.CreatedAt.UTC().Format("2006-01-02")
		dailyReq[day]++
		dailyCost[day] = dailyCost[day].Add(r.CostUSD)
		modelCnt[r.Model]++
		modelTok[r.Model] += tok
		modelCost[r.Model] = modelCost[r.Model].Add(r.CostUSD)
	}

	todayStr := now.Format("2006-01-02")
	yestStr := now.AddDate(0, 0, -1).Format("2006-01-02")
	var twoDayRows []invocation.Invocation
	tdq := rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", yestStr)
	if !isAdmin {
		tdq = tdq.Where("org_id = ?", claims.OrgID)
	}
	_ = tdq.Select("api_key_id, prompt_tokens, completion_tokens, cost_usd, created_at").Find(&twoDayRows).Error

	var todayReq, todayTok, yestReq, yestTok int64
	todayCost, yestCost := decimal.Zero, decimal.Zero
	todayKeys, yestKeys := map[uint64]struct{}{}, map[uint64]struct{}{}
	for _, r := range twoDayRows {
		d := r.CreatedAt.UTC().Format("2006-01-02")
		tok := r.PromptTokens + r.CompletionTokens
		switch d {
		case todayStr:
			todayReq++
			todayTok += tok
			todayCost = todayCost.Add(r.CostUSD)
			if r.APIKeyID > 0 {
				todayKeys[r.APIKeyID] = struct{}{}
			}
		case yestStr:
			yestReq++
			yestTok += tok
			yestCost = yestCost.Add(r.CostUSD)
			if r.APIKeyID > 0 {
				yestKeys[r.APIKeyID] = struct{}{}
			}
		}
	}

	var recent []invocation.Invocation
	rq := rdb.Model(&invocation.Invocation{}).Order("id DESC").Limit(10)
	if !isAdmin {
		rq = rq.Where("org_id = ?", claims.OrgID)
	}
	_ = rq.Find(&recent).Error
	recentOut := make([]gin.H, 0, len(recent))
	for _, r := range recent {
		recentOut = append(recentOut, gin.H{
			"id":          r.ID,
			"model":       r.Model,
			"status_code": r.StatusCode,
			"endpoint":    cmp.Or(r.Protocol, "/v1/chat/completions"),
			"latency_ms":  r.DurationMs,
			"cost":        decFloat(r.CostUSD),
			"created_at":  r.CreatedAt,
		})
	}

	type mStat struct {
		Model       string  `json:"model"`
		Count       int64   `json:"count"`
		TotalTokens int64   `json:"total_tokens"`
		TotalCost   float64 `json:"total_cost"`
	}
	modelStats := make([]mStat, 0, len(modelCnt))
	for m, cnt := range modelCnt {
		modelStats = append(modelStats, mStat{
			Model:       m,
			Count:       cnt,
			TotalTokens: modelTok[m],
			TotalCost:   decFloat(modelCost[m]),
		})
	}
	slices.SortFunc(modelStats, func(a, b mStat) int {
		if c := cmp.Compare(b.TotalCost, a.TotalCost); c != 0 {
			return c
		}
		return cmp.Compare(b.Count, a.Count)
	})
	if len(modelStats) > 15 {
		modelStats = modelStats[:15]
	}

	daysCount := max(1, min(90, int(endDay.Sub(startDay).Hours()/24)+1))
	trends := make([]gin.H, 0, daysCount)
	for i := range daysCount {
		d := startDay.AddDate(0, 0, i).Format("2006-01-02")
		trends = append(trends, gin.H{
			"date":     d,
			"requests": dailyReq[d],
			"cost":     decFloat(dailyCost[d]),
		})
	}

	var totalUsers, totalChannels, totalTokensCount int64
	_ = rdb.Model(&identity.User{}).Count(&totalUsers).Error
	if isAdmin {
		_ = rdb.Model(&relay.Channel{}).Count(&totalChannels).Error
		_ = rdb.Model(&identity.APIKey{}).Where("status = 1").Count(&totalTokensCount).Error
	} else {
		_ = rdb.Model(&identity.APIKey{}).Where("org_id = ? AND status = 1", claims.OrgID).Count(&totalTokensCount).Error
	}

	c.JSON(http.StatusOK, gin.H{
		"total_requests":          totalReq,
		"total_tokens":            totalTok,
		"total_cost":              decFloat(totalCost),
		"total_users":             totalUsers,
		"total_channels":          totalChannels,
		"total_api_tokens":        totalTokensCount,
		"today_requests":          todayReq,
		"today_tokens":            todayTok,
		"today_cost":              decFloat(todayCost),
		"today_active_tokens":     len(todayKeys),
		"yesterday_requests":      yestReq,
		"yesterday_tokens":        yestTok,
		"yesterday_cost":          decFloat(yestCost),
		"yesterday_active_tokens": len(yestKeys),
		"recent_logs":             recentOut,
		"model_stats":             modelStats,
		"daily_trends":            trends,
		"live_metrics":            s.computeLiveMetrics(c, isAdmin, claims.OrgID),
	})
}

func (s *Server) computeLiveMetrics(c *gin.Context, isAdmin bool, orgID uint64) gin.H {
	rdb := s.readOnly().WithContext(c.Request.Context())
	since := s.now().UTC().Add(-60 * time.Second).Format("2006-01-02 15:04:05")
	var rpm int64
	var tpm int64
	q := rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", since)
	if !isAdmin {
		q = q.Where("org_id = ?", orgID)
	}
	_ = q.Count(&rpm).Error
	_ = q.Select("COALESCE(SUM(prompt_tokens + completion_tokens), 0)").Scan(&tpm).Error

	var pendingTasks int64
	tq := rdb.Model(&task.Task{}).Where("status IN ?", []string{"queued", "pending", "submitted", "running"})
	if !isAdmin {
		tq = tq.Where("org_id = ?", orgID)
	}
	_ = tq.Count(&pendingTasks).Error

	return gin.H{
		"qps":           float64(rpm) / 60.0,
		"rpm":           rpm,
		"tpm":           tpm,
		"task":          pendingTasks,
		"pending_tasks": pendingTasks,
	}
}

func (s *Server) v1LiveMetrics(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := s.callerIsAdmin(c) && !strings.EqualFold(c.Query("scope"), "self")
	c.JSON(http.StatusOK, gin.H{
		"metrics": s.computeLiveMetrics(c, isAdmin, claims.OrgID),
		"scope":   map[bool]string{true: "global", false: "self"}[isAdmin],
		"ts":      s.now().Unix(),
	})
}

func (s *Server) v1Models30d(c *gin.Context) {
	ctx := c.Request.Context()
	claims := mustClaims(c)
	rdb := s.readOnly().WithContext(ctx)
	isAdmin := s.callerIsAdmin(c) && !strings.EqualFold(c.Query("scope"), "self")

	days := 30
	if d, err := strconv.Atoi(c.Query("days")); err == nil && d >= 1 && d <= 90 {
		days = d
	}
	since := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	var rows []invocation.Invocation
	q := rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", since)
	if !isAdmin {
		q = q.Where("org_id = ?", claims.OrgID)
	}
	_ = q.Select("model, prompt_tokens, completion_tokens, cost_usd, created_at").Find(&rows).Error

	type mAgg struct {
		count  int64
		tokens int64
		cost   decimal.Decimal
	}
	byModel := map[string]*mAgg{}
	type dmKey struct{ day, model string }
	byDayModel := map[dmKey]*mAgg{}

	for _, r := range rows {
		tok := r.PromptTokens + r.CompletionTokens
		if byModel[r.Model] == nil {
			byModel[r.Model] = &mAgg{}
		}
		byModel[r.Model].count++
		byModel[r.Model].tokens += tok
		byModel[r.Model].cost = byModel[r.Model].cost.Add(r.CostUSD)

		k := dmKey{day: r.CreatedAt.UTC().Format("2006-01-02"), model: r.Model}
		if byDayModel[k] == nil {
			byDayModel[k] = &mAgg{}
		}
		byDayModel[k].count++
		byDayModel[k].cost = byDayModel[k].cost.Add(r.CostUSD)
	}

	top := make([]gin.H, 0, len(byModel))
	type sortableTop struct {
		model  string
		count  int64
		tokens int64
		cost   float64
	}
	list := make([]sortableTop, 0, len(byModel))
	for m, a := range byModel {
		list = append(list, sortableTop{m, a.count, a.tokens, decFloat(a.cost)})
	}
	slices.SortFunc(list, func(a, b sortableTop) int {
		return cmp.Compare(b.cost, a.cost)
	})
	if len(list) > 15 {
		list = list[:15]
	}
	for _, it := range list {
		top = append(top, gin.H{
			"model":        it.model,
			"count":        it.count,
			"total_tokens": it.tokens,
			"total_cost":   it.cost,
		})
	}

	dailyData := make([]gin.H, 0, len(byDayModel))
	for k, a := range byDayModel {
		dailyData = append(dailyData, gin.H{
			"date":       k.day,
			"model":      k.model,
			"count":      a.count,
			"total_cost": decFloat(a.cost),
		})
	}
	c.JSON(http.StatusOK, gin.H{"top_models": top, "daily_data": dailyData})
}

func (s *Server) v1ListLogs(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := s.callerIsAdmin(c) && !strings.EqualFold(c.Query("scope"), "self")

	page, _ := strconv.Atoi(cmp.Or(c.Query("page"), "1"))
	page = max(1, page)
	pageSize, _ := strconv.Atoi(cmp.Or(c.Query("per_page"), c.Query("page_size"), "20"))
	pageSize = min(100, max(1, pageSize))

	q := s.applyV1LogFilters(c, isAdmin, claims.OrgID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query logs failed")
		return
	}
	var rows []invocation.Invocation
	if err := q.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query logs failed")
		return
	}

	out := s.formatInvocationRowsForV1(c, rows, isAdmin)

	var totalCost decimal.Decimal
	var okCnt, failCnt, pTok, cTok int64
	if page == 1 {
		var agg struct {
			Cost decimal.Decimal
			PTok int64
			CTok int64
		}
		_ = s.applyV1LogFilters(c, isAdmin, claims.OrgID).
			Select("COALESCE(SUM(cost_usd), 0) AS cost, COALESCE(SUM(prompt_tokens), 0) AS p_tok, COALESCE(SUM(completion_tokens), 0) AS c_tok").
			Scan(&agg).Error
		totalCost = agg.Cost
		pTok = agg.PTok
		cTok = agg.CTok
		_ = s.applyV1LogFilters(c, isAdmin, claims.OrgID).Where("status = ?", invocation.StatusSuccess).Count(&okCnt).Error
		failCnt = max(0, total-okCnt)
	}

	c.JSON(http.StatusOK, gin.H{
		"data":                    out,
		"total":                   total,
		"page":                    page,
		"per_page":                pageSize,
		"allow_details":           true,
		"total_cost":              decFloat(totalCost),
		"success_count":           okCnt,
		"fail_count":              failCnt,
		"total_prompt_tokens":     pTok,
		"total_completion_tokens": cTok,
	})
}

func (s *Server) applyV1LogFilters(c *gin.Context, isAdmin bool, orgID uint64) *gorm.DB {
	q := s.readOnly().WithContext(c.Request.Context()).Model(&invocation.Invocation{})
	if !isAdmin {
		q = q.Where("org_id = ?", orgID)
	} else if rawOrg := strings.TrimSpace(c.Query("org_id")); rawOrg != "" {
		if parsed, err := strconv.ParseUint(rawOrg, 10, 64); err == nil {
			q = q.Where("org_id = ?", parsed)
		}
	}
	if m := strings.TrimSpace(c.Query("model")); m != "" {
		q = q.Where("model LIKE ?", "%"+escapeSQLLike(m)+"%")
	}
	if st := strings.TrimSpace(c.Query("status")); st != "" {
		if st == "failed" || st == "error" || st == "fail" {
			q = q.Where("status <> ?", invocation.StatusSuccess)
		} else if st == "success" {
			q = q.Where("status = ?", invocation.StatusSuccess)
		}
	}
	if scRaw := strings.TrimSpace(c.Query("status_code")); scRaw != "" {
		if sc, err := strconv.Atoi(scRaw); err == nil {
			q = q.Where("status_code = ?", sc)
		}
	}
	if at := mapTkeActionToModality(c.Query("action_type")); at != "" {
		q = q.Where("modality = ?", at)
	} else if mod := strings.TrimSpace(c.Query("modality")); mod != "" {
		q = q.Where("modality = ?", mod)
	}
	if kw := strings.TrimSpace(cmp.Or(c.Query("search_keyword"), c.Query("q"))); kw != "" {
		pat := "%" + escapeSQLLike(kw) + "%"
		q = q.Where(`request_id LIKE ? ESCAPE '\' OR upstream_request_id LIKE ? ESCAPE '\' OR model LIKE ? ESCAPE '\'`, pat, pat, pat)
	}
	return q
}

func (s *Server) formatInvocationRowsForV1(c *gin.Context, rows []invocation.Invocation, isAdmin bool) []gin.H {
	ctx := c.Request.Context()
	rdb := s.readOnly().WithContext(ctx)

	keyMap := map[uint64]identity.APIKey{}
	chanMap := map[uint64]relay.Channel{}
	userMap := map[uint64]identity.User{}
	if len(rows) > 0 {
		keyIDs := make([]uint64, 0, len(rows))
		userIDs := make([]uint64, 0, len(rows))
		chanIDs := make([]uint64, 0, len(rows))
		for _, r := range rows {
			if r.APIKeyID > 0 {
				keyIDs = append(keyIDs, r.APIKeyID)
			}
			if r.UserID != nil && *r.UserID > 0 {
				userIDs = append(userIDs, *r.UserID)
			}
			if isAdmin && r.ChannelID > 0 {
				chanIDs = append(chanIDs, r.ChannelID)
			}
		}
		if len(keyIDs) > 0 {
			var ks []identity.APIKey
			_ = rdb.Select("id, name, prefix").Where("id IN ?", keyIDs).Find(&ks).Error
			for _, k := range ks {
				keyMap[k.ID] = k
			}
		}
		if len(userIDs) > 0 {
			var us []identity.User
			_ = rdb.Select("id, email, display_name, admin_remark").Where("id IN ?", userIDs).Find(&us).Error
			for _, u := range us {
				userMap[u.ID] = u
			}
		}
		if isAdmin && len(chanIDs) > 0 {
			var chs []relay.Channel
			_ = rdb.Select("id, name, provider").Where("id IN ?", chanIDs).Find(&chs).Error
			for _, ch := range chs {
				chanMap[ch.ID] = ch
			}
		}
	}

	out := make([]gin.H, 0, len(rows))
	for _, inv := range rows {
		k := keyMap[inv.APIKeyID]
		streamInt := 0
		if inv.IsStream {
			streamInt = 1
		}
		entry := gin.H{
			"id":                inv.ID,
			"log_id":            inv.RequestID,
			"request_id":        inv.RequestID,
			"api_key_id":        inv.APIKeyID,
			"token_name":        k.Name,
			"token_kid":         k.Prefix,
			"model":             inv.Model,
			"modality":          inv.Modality,
			"action_type":       modalityToCN(inv.Modality),
			"protocol":          inv.Protocol,
			"endpoint":          protocolToEndpoint(inv.Protocol, inv.Modality),
			"status":            inv.Status,
			"status_code":       inv.StatusCode,
			"is_stream":         streamInt,
			"prompt_tokens":     inv.PromptTokens,
			"completion_tokens": inv.CompletionTokens,
			"cached_tokens":     inv.CachedTokens,
			"reasoning_tokens":  inv.ReasoningTokens,
			"cost":              decFloat(inv.CostUSD),
			"cost_usd":          inv.CostUSD,
			"usage_source":      inv.UsageSource,
			"frt_ms":            inv.FRTMs,
			"latency_ms":        inv.DurationMs,
			"duration_ms":       inv.DurationMs,
			"finish_reason":     inv.FinishReason,
			"error_brief":       inv.ErrorBrief,
			"error_message":     inv.ErrorBrief,
			"billing_pid":       inv.BillingRulePID,
			"billing_rule_pid":  inv.BillingRulePID,
			"forward_eid":       inv.ForwardRuleEID,
			"forward_rule_eid":  inv.ForwardRuleEID,
			"created_at":        inv.CreatedAt,
		}
		if inv.UserID != nil {
			u := userMap[*inv.UserID]
			entry["user_id"] = strconv.FormatUint(*inv.UserID, 10)
			entry["user_uid"] = strconv.FormatUint(*inv.UserID, 10)
			entry["user_nickname"] = cmp.Or(u.DisplayName, u.Email)
			if isAdmin {
				entry["user_admin_remark"] = u.AdminRemark
			}
		}
		if isAdmin {
			ch := chanMap[inv.ChannelID]
			entry["org_id"] = inv.OrgID
			entry["channel_id"] = inv.ChannelID
			entry["channel_name"] = cmp.Or(ch.Name, inv.Provider)
			entry["sub_channel_name"] = cmp.Or(ch.Name, inv.Provider)
			if inv.ChannelID > 0 {
				entry["channel_group_aid"] = "A" + strconv.FormatUint(inv.ChannelID, 10)
			}
			entry["provider"] = inv.Provider
			entry["upstream_cost_ratio"] = inv.UpstreamCostRatio
			entry["upstream_request_id"] = inv.UpstreamRequestID
		}
		out = append(out, entry)
	}
	return out
}

func (s *Server) v1LogDetail(c *gin.Context) {
	claims := mustClaims(c)
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	isAdmin := s.callerIsAdmin(c)
	var inv invocation.Invocation
	q := s.readOnly().WithContext(c.Request.Context()).Where("id = ?", id)
	if !isAdmin {
		q = q.Where("org_id = ?", claims.OrgID)
	}
	if err := q.First(&inv).Error; err != nil {
		fail(c, http.StatusNotFound, "log not found")
		return
	}
	if isAdmin {
		s.writeAudit(c, "invocation_detail_read", strconv.FormatUint(inv.ID, 10), "viewed log metadata "+inv.RequestID)
	}
	rows := s.formatInvocationRowsForV1(c, []invocation.Invocation{inv}, isAdmin)
	out := rows[0]
	out["client_ip"] = inv.ClientIP
	out["user_agent"] = inv.UserAgent
	out["upstream_error_code"] = inv.UpstreamErrorCode
	c.JSON(http.StatusOK, out)
}

func (s *Server) v1ExportLogs(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := s.callerIsAdmin(c) && !strings.EqualFold(c.Query("scope"), "self")
	var rows []invocation.Invocation
	_ = s.applyV1LogFilters(c, isAdmin, claims.OrgID).Order("id DESC").Limit(5000).Find(&rows).Error

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="logs.csv"`)
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"time", "log_id", "model", "status_code", "latency_ms", "prompt_tokens", "completion_tokens", "cost_usd"})
	for _, r := range rows {
		_ = w.Write([]string{
			r.CreatedAt.UTC().Format(time.RFC3339), r.RequestID, r.Model,
			strconv.Itoa(r.StatusCode), strconv.Itoa(r.DurationMs),
			strconv.FormatInt(r.PromptTokens, 10), strconv.FormatInt(r.CompletionTokens, 10), r.CostUSD.String(),
		})
	}
	w.Flush()
}

func (s *Server) formatV1TaskRow(t task.Task, isAdmin bool) gin.H {
	durMs := 0
	if t.FinishedAt != nil {
		durMs = max(0, int(t.FinishedAt.Sub(t.SubmittedAt).Milliseconds()))
	}
	previews := []string{}
	if strings.TrimSpace(t.ResultURL) != "" {
		previews = []string{t.ResultURL}
	}
	statusCode := 0
	switch t.Status {
	case task.StatusSucceeded:
		statusCode = http.StatusOK
	case task.StatusFailed:
		statusCode = http.StatusInternalServerError
	case "cancelled", "canceled":
		statusCode = 499
	}
	respPayload, _ := json.Marshal(gin.H{
		"status":     t.Status,
		"task_id":    t.TaskID,
		"result_url": t.ResultURL,
		"error":      t.FailReason,
	})
	entry := gin.H{
		"id":                t.ID,
		"log_id":            t.TaskID,
		"task_id":           t.TaskID,
		"api_key_id":        t.APIKeyID,
		"model":             t.Model,
		"endpoint":          "/v1/video/generations",
		"action_type":       "视频",
		"status":            t.Status,
		"status_code":       statusCode,
		"fail_reason":       t.FailReason,
		"error_message":     t.FailReason,
		"response_content":  string(respPayload),
		"result_url":        t.ResultURL,
		"preview_urls":      previews,
		"prompt_tokens":     t.EstTokens,
		"completion_tokens": t.ActualTokens,
		"est_tokens":        t.EstTokens,
		"actual_tokens":     t.ActualTokens,
		"hold_amount_usd":   t.HoldAmountUSD,
		"cost":              decFloat(t.CostUSD),
		"cost_usd":          t.CostUSD,
		"billing_frozen":    t.FinishedAt == nil && t.HoldAmountUSD.IsPositive(),
		"billing_pid":       t.BillingRulePID,
		"billing_rule_pid":  t.BillingRulePID,
		"forward_eid":       t.ForwardRuleEID,
		"forward_rule_eid":  t.ForwardRuleEID,
		"billing_detail":    t.BillingDetail,
		"features_json":     t.FeaturesJSON,
		"time_multiplier":   t.TimeMultiplier,
		"latency_ms":        durMs,
		"duration_ms":       durMs,
		"submitted_at":      t.SubmittedAt,
		"started_at":        t.StartedAt,
		"finished_at":       t.FinishedAt,
		"created_at":        t.SubmittedAt,
	}
	if t.UserID != nil {
		entry["user_id"] = strconv.FormatUint(*t.UserID, 10)
		entry["user_uid"] = strconv.FormatUint(*t.UserID, 10)
	}
	if isAdmin {
		entry["org_id"] = t.OrgID
		entry["channel_id"] = t.ChannelID
		entry["channel_name"] = t.Provider
		entry["provider"] = t.Provider
		entry["upstream_task_id"] = t.UpstreamTaskID
		entry["upstream_request_id"] = t.UpstreamTaskID
	}
	return entry
}

func (s *Server) v1ListTaskLogs(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := s.callerIsAdmin(c) && !strings.EqualFold(c.Query("scope"), "self")

	page, _ := strconv.Atoi(cmp.Or(c.Query("page"), "1"))
	page = max(1, page)
	pageSize, _ := strconv.Atoi(cmp.Or(c.Query("per_page"), c.Query("page_size"), "20"))
	pageSize = min(100, max(1, pageSize))

	q := s.applyV1TaskFilters(c, isAdmin, claims.OrgID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query task logs failed")
		return
	}
	var rows []task.Task
	if err := q.Order("submitted_at DESC, id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query task logs failed")
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, t := range rows {
		out = append(out, s.formatV1TaskRow(t, isAdmin))
	}
	c.JSON(http.StatusOK, gin.H{
		"data":          out,
		"total":         total,
		"page":          page,
		"per_page":      pageSize,
		"allow_details": true,
	})
}

func (s *Server) applyV1TaskFilters(c *gin.Context, isAdmin bool, orgID uint64) *gorm.DB {
	q := s.readOnly().WithContext(c.Request.Context()).Model(&task.Task{})
	if !isAdmin {
		q = q.Where("org_id = ?", orgID)
	} else if rawOrg := strings.TrimSpace(c.Query("org_id")); rawOrg != "" {
		if parsed, err := strconv.ParseUint(rawOrg, 10, 64); err == nil {
			q = q.Where("org_id = ?", parsed)
		}
	}
	if m := strings.TrimSpace(c.Query("model")); m != "" {
		q = q.Where("model LIKE ?", "%"+escapeSQLLike(m)+"%")
	}
	if st := strings.TrimSpace(c.Query("status")); st != "" {
		switch st {
		case "pending", "processing":
			q = q.Where("status IN ?", []string{task.StatusQueued, task.StatusRunning})
		case "success":
			q = q.Where("status = ?", task.StatusSucceeded)
		default:
			q = q.Where("status = ?", st)
		}
	}
	if kw := strings.TrimSpace(cmp.Or(c.Query("search_keyword"), c.Query("q"))); kw != "" {
		pat := "%" + escapeSQLLike(kw) + "%"
		q = q.Where(`task_id LIKE ? ESCAPE '\' OR upstream_task_id LIKE ? ESCAPE '\' OR model LIKE ? ESCAPE '\'`, pat, pat, pat)
	}
	return q
}

func (s *Server) v1TaskLogDetail(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := s.callerIsAdmin(c)
	ref := strings.TrimSpace(c.Param("id"))
	q := s.readOnly().WithContext(c.Request.Context()).Where("task_id = ?", ref)
	if !isAdmin {
		q = q.Where("org_id = ?", claims.OrgID)
	}
	var t task.Task
	err := q.First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if numID, perr := strconv.ParseUint(ref, 10, 64); perr == nil && numID > 0 {
			qNum := s.readOnly().WithContext(c.Request.Context()).Where("id = ?", numID)
			if !isAdmin {
				qNum = qNum.Where("org_id = ?", claims.OrgID)
			}
			err = qNum.First(&t).Error
		}
	}
	if err != nil {
		fail(c, http.StatusNotFound, "task not found")
		return
	}
	c.JSON(http.StatusOK, s.formatV1TaskRow(t, isAdmin))
}

func (s *Server) v1SyncTaskLog(c *gin.Context) {
	if s.taskService == nil {
		fail(c, http.StatusServiceUnavailable, "task service unavailable")
		return
	}
	claims := mustClaims(c)
	isAdmin := s.callerIsAdmin(c)
	t, err := s.taskService.SyncTask(c.Request.Context(), claims.OrgID, c.Param("id"), isAdmin)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "sync task failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "task synced", "task": s.formatV1TaskRow(*t, isAdmin)})
}

func (s *Server) v1CancelTaskLog(c *gin.Context) {
	if s.taskService == nil {
		fail(c, http.StatusServiceUnavailable, "task service unavailable")
		return
	}
	claims := mustClaims(c)
	isAdmin := s.callerIsAdmin(c)
	t, err := s.taskService.CancelTask(c.Request.Context(), claims.OrgID, c.Param("id"), isAdmin)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "task not found")
		return
	}
	if errors.Is(err, task.ErrTaskNotCancellable) {
		fail(c, http.StatusConflict, "task cannot be cancelled in current state")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "cancel task failed")
		return
	}
	if isAdmin {
		s.writeAudit(c, "task_cancel", t.TaskID, "cancelled task and refunded hold")
	}
	c.JSON(http.StatusOK, gin.H{"message": "task cancelled and hold refunded", "task": s.formatV1TaskRow(*t, isAdmin)})
}

func (s *Server) v1ExportTaskLogs(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := s.callerIsAdmin(c) && !strings.EqualFold(c.Query("scope"), "self")
	var rows []task.Task
	_ = s.applyV1TaskFilters(c, isAdmin, claims.OrgID).Order("submitted_at DESC, id DESC").Limit(5000).Find(&rows).Error

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="task_logs.csv"`)
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"submitted_at", "task_id", "model", "status", "est_tokens", "actual_tokens", "cost_usd"})
	for _, r := range rows {
		_ = w.Write([]string{
			r.SubmittedAt.UTC().Format(time.RFC3339), r.TaskID, r.Model, r.Status,
			strconv.FormatInt(r.EstTokens, 10), strconv.FormatInt(r.ActualTokens, 10), r.CostUSD.String(),
		})
	}
	w.Flush()
}

func decFloat(d decimal.Decimal) float64 {
	v, _ := d.Float64()
	return v
}

func mapTkeActionToModality(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "聊天", "chat":
		return "chat"
	case "图片", "image":
		return "image"
	case "视频", "video":
		return "video"
	case "音频", "audio":
		return "audio"
	case "向量", "embedding":
		return "embedding"
	case "排序", "rerank":
		return "rerank"
	default:
		return ""
	}
}

func modalityToCN(m string) string {
	switch m {
	case "image":
		return "图片"
	case "video":
		return "视频"
	case "audio":
		return "音频"
	case "embedding":
		return "向量"
	case "rerank":
		return "排序"
	default:
		return "聊天"
	}
}

func protocolToEndpoint(proto, modality string) string {
	switch proto {
	case "anthropic":
		return "/v1/messages"
	case "gemini":
		return "/v1beta/models"
	case "ark", "volcengine":
		return "/api/v3/contents/generations/tasks"
	}
	switch modality {
	case "image":
		return "/v1/images/generations"
	case "video":
		return "/v1/video/generations"
	case "embedding":
		return "/v1/embeddings"
	default:
		return "/v1/chat/completions"
	}
}
