package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 平台管理 API（M1 内部验证阶段）：X-Admin-Token 门禁（常数时间比较）。
// M3 Console 落地后升级为正式 root RBAC；资金类操作的审计 = journal 本身（kind=grant 全留痕）。

func (s *Server) adminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(s.adminToken) == 0 {
			fail(c, http.StatusNotFound, "admin api disabled") // 未配置 = 整组端点不存在
			return
		}
		got := c.GetHeader("X-Admin-Token")
		if subtle.ConstantTimeCompare([]byte(got), s.adminToken) != 1 {
			fail(c, http.StatusUnauthorized, "admin token invalid")
			return
		}
		c.Next()
	}
}

type grantReq struct {
	OrgID          uint64 `json:"org_id" binding:"required"`
	AmountUSD      string `json:"amount_usd" binding:"required"`
	Reason         string `json:"reason" binding:"required"`
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
}

// adminGrantCredit：授信入账（红线 2：幂等键由调用方提供，UNIQUE 兜底）。
func (s *Server) adminGrantCredit(c *gin.Context) {
	var req grantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "org_id, amount_usd, reason, idempotency_key required")
		return
	}
	amount, err := decimal.NewFromString(req.AmountUSD)
	if err != nil || !amount.IsPositive() {
		fail(c, http.StatusBadRequest, "amount_usd must be a positive decimal string")
		return
	}
	var entry *ledger.JournalEntry
	var applied bool
	err = s.db.Transaction(func(tx *gorm.DB) error {
		acc, err := ledger.EnsureAccount(tx, req.OrgID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		entry, applied, err = ledger.Post(tx, ledger.PostInput{
			IdempotencyKey: "admin_grant:" + req.IdempotencyKey,
			AccountID:      acc.ID,
			Kind:           ledger.KindGrant,
			Delta:          amount,
			RefType:        "admin_grant",
			Reason:         req.Reason,
		})
		return err
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "grant failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"entry_id": entry.ID, "balance_after": entry.BalanceAfter, "already_applied": applied,
	})
}

type channelReq struct {
	Name     string   `json:"name" binding:"required"`
	Provider string   `json:"provider" binding:"required"`
	BaseURL  string   `json:"base_url"` // 缺省取厂商档案默认端点（providers.go）
	APIKey   string   `json:"api_key" binding:"required"`
	Models   []string `json:"models" binding:"required"`
	Weight   int      `json:"weight"`
	Priority int      `json:"priority"`             // 大者先用；重试降层
	MaxConcurrentTasks int `json:"max_concurrent_tasks"` // 任务并发闸，默认 3
	ModelMapping map[string]string `json:"model_mapping"` // 对外名→上游名（可选）
}

// marshalMapping：映射序列化为存储格式（空 map → 空串 = 不映射）。
func marshalMapping(m map[string]string) (string, bool) {
	if len(m) == 0 {
		return "", true
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (s *Server) adminCreateChannel(c *gin.Context) {
	if s.keyring == nil {
		fail(c, http.StatusServiceUnavailable, "LAIAI_DATA_KEYS not configured")
		return
	}
	var req channelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name, provider, api_key, models required")
		return
	}
	if req.BaseURL == "" {
		req.BaseURL = relay.DefaultBaseURLFor(req.Provider)
		if req.BaseURL == "" {
			fail(c, http.StatusBadRequest, "base_url required for provider "+req.Provider)
			return
		}
	}
	enc, err := s.keyring.Encrypt(req.APIKey)
	if err != nil {
		fail(c, http.StatusInternalServerError, "credential encrypt failed")
		return
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1
	}
	maxTasks := req.MaxConcurrentTasks
	if maxTasks <= 0 {
		maxTasks = 3 // Seedance 公测档默认
	}
	mapping, ok := marshalMapping(req.ModelMapping)
	if !ok {
		fail(c, http.StatusBadRequest, "invalid model_mapping")
		return
	}
	ch := &relay.Channel{
		Name: req.Name, Provider: req.Provider,
		BaseURL: strings.TrimRight(req.BaseURL, "/"),
		CredentialEnc: enc, Models: strings.Join(req.Models, ","),
		Weight: weight, Priority: req.Priority,
		MaxConcurrentTasks: maxTasks, StrictResponse: 1,
		ModelMapping: mapping, Status: 1,
	}
	if err := s.db.Create(ch).Error; err != nil {
		fail(c, http.StatusInternalServerError, "channel create failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": ch.ID, "name": ch.Name})
}

type updateChannelReq struct {
	BaseURL  *string  `json:"base_url"`
	APIKey   *string  `json:"api_key"` // 省略=不改凭证
	Models   []string `json:"models"`
	Weight   *int     `json:"weight"`
	Priority *int     `json:"priority"`
	StrictResponse *bool `json:"strict_response"`
	ModelMapping *map[string]string `json:"model_mapping"` // 指针区分"不改"与"清空"
}

// adminUpdateChannel：PUT /api/admin/channels/:id —— 编辑渠道（部署冒烟暴露的高频缺口）。
// 只更新提供的字段；api_key 省略则保留原凭证。
func (s *Server) adminUpdateChannel(c *gin.Context) {
	if s.keyring == nil {
		fail(c, http.StatusServiceUnavailable, "LAIAI_DATA_KEYS not configured")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	var req updateChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]any{}
	if req.BaseURL != nil {
		updates["base_url"] = strings.TrimRight(*req.BaseURL, "/")
	}
	if len(req.Models) > 0 {
		updates["models"] = strings.Join(req.Models, ",")
	}
	if req.Weight != nil {
		updates["weight"] = *req.Weight
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.StrictResponse != nil {
		if *req.StrictResponse {
			updates["strict_response"] = 1
		} else {
			updates["strict_response"] = 0
		}
	}
	if req.ModelMapping != nil {
		mapping, ok := marshalMapping(*req.ModelMapping)
		if !ok {
			fail(c, http.StatusBadRequest, "invalid model_mapping")
			return
		}
		updates["model_mapping"] = mapping
	}
	if req.APIKey != nil && *req.APIKey != "" {
		enc, err := s.keyring.Encrypt(*req.APIKey)
		if err != nil {
			fail(c, http.StatusInternalServerError, "credential encrypt failed")
			return
		}
		updates["credential_enc"] = enc
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "nothing to update")
		return
	}
	res := s.db.Model(&relay.Channel{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "channel update failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": id})
}

func (s *Server) adminListChannels(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	var channels []relay.Channel
	if err := rdb.Order("id").Find(&channels).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	// 累计调用数（14 天窗口，与 overview 同口径）
	since := s.now().AddDate(0, 0, -13).Format("2006-01-02")
	calls := s.channelCallCounts(rdb, since)
	out := make([]gin.H, 0, len(channels))
	for _, ch := range channels {
		modelCount := 0
		if ch.Models != "" {
			modelCount = len(strings.Split(ch.Models, ","))
		}
		out = append(out, gin.H{ // 凭证永不回显，连密文都不给
			"id": ch.ID, "name": ch.Name, "provider": ch.Provider,
			"base_url": ch.BaseURL, "models": ch.Models, "model_count": modelCount,
			"weight": ch.Weight, "priority": ch.Priority,
			"strict_response": ch.EnforcesStrictResponse(),
			"model_mapping": ch.ModelMapping, "calls": calls[ch.ID], "status": ch.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"channels": out})
}

// adminDisableChannel：禁用而非删除（调度池即时剔除；审计留痕）。
func (s *Server) adminDisableChannel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	res := s.db.Model(&relay.Channel{}).Where("id = ?", id).Update("status", 0)
	if res.Error != nil || res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"disabled": id})
}

type priceReq struct {
	Model          string `json:"model" binding:"required"`
	InputUSDPer1M  string `json:"input_usd_per_1m" binding:"required"`
	OutputUSDPer1M string `json:"output_usd_per_1m" binding:"required"`
	ImageUSD       string `json:"image_usd"` // 每张图 USD；图像模型必填，其他模型省略
}

// adminUpsertPrice：模型定价 upsert（uk_model_prices_model 兜底）。
func (s *Server) adminUpsertPrice(c *gin.Context) {
	var req priceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "model, input_usd_per_1m, output_usd_per_1m required")
		return
	}
	in, err1 := decimal.NewFromString(req.InputUSDPer1M)
	out, err2 := decimal.NewFromString(req.OutputUSDPer1M)
	if err1 != nil || err2 != nil || in.IsNegative() || out.IsNegative() {
		fail(c, http.StatusBadRequest, "prices must be non-negative decimal strings")
		return
	}
	img := decimal.Zero
	if req.ImageUSD != "" {
		var err error
		if img, err = decimal.NewFromString(req.ImageUSD); err != nil || img.IsNegative() {
			fail(c, http.StatusBadRequest, "image_usd must be a non-negative decimal string")
			return
		}
	}
	p := &relay.ModelPrice{Model: req.Model, InputUSDPer1M: in, OutputUSDPer1M: out, ImageUSD: img}
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{"input_usd_per_1m", "output_usd_per_1m", "image_usd"}),
	}).Create(p).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "price upsert failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"model": p.Model})
}

// adminListProviders：厂商注册表（Console 下拉数据源）。
func (s *Server) adminListProviders(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"providers": relay.ListProviders()})
}

func (s *Server) adminListPrices(c *gin.Context) {
	var prices []relay.ModelPrice
	if err := s.db.Order("model").Find(&prices).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

// adminTestChannel：POST /api/admin/channels/:id/test {model?}
// 真实最小请求探活（不计费不记录不触发禁用）。model 缺省取渠道模型列表第一个。
func (s *Server) adminTestChannel(c *gin.Context) {
	if s.relay == nil {
		fail(c, http.StatusServiceUnavailable, "relay not configured")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	ch := &relay.Channel{}
	if err := s.db.First(ch, id).Error; err != nil {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	_ = c.ShouldBindJSON(&req) // body 可空
	model := req.Model
	if model == "" {
		model = strings.TrimSpace(strings.Split(ch.Models, ",")[0])
	}
	if model == "" {
		fail(c, http.StatusBadRequest, "channel has no models configured")
		return
	}
	c.JSON(http.StatusOK, s.relay.TestChannel(c.Request.Context(), ch, model))
}

// adminListOrgs：GET /api/admin/orgs —— 组织清单 + 余额/冻结/成员数（运营视图）。
// 只读聚合，不回显任何 PII 之外字段（email 是运营定位用户的必需最小集）。
func (s *Server) adminListOrgs(c *gin.Context) {
	var rows []struct {
		ID            uint64          `json:"id"`
		Name          string          `json:"name"`
		Kind          int8            `json:"kind"`
		Status        int8            `json:"status"`
		OwnerEmail    string          `json:"owner_email"`
		Members       int64           `json:"members"`
		PostedBalance decimal.Decimal `json:"balance"`
		HeldAmount    decimal.Decimal `json:"held"`
	}
	err := s.db.Raw(`
		SELECT o.id, o.name, o.kind, o.status,
		       COALESCE(u.email, '') AS owner_email,
		       (SELECT COUNT(*) FROM org_members m WHERE m.org_id = o.id) AS members,
		       COALESCE(a.posted_balance, 0) AS posted_balance,
		       COALESCE(a.held_amount, 0) AS held_amount
		FROM organizations o
		LEFT JOIN org_members om ON om.org_id = o.id AND om.role = 'owner'
		LEFT JOIN users u ON u.id = om.user_id
		LEFT JOIN ledger_accounts a ON a.org_id = o.id AND a.type = 'user_credit'
		ORDER BY o.id`).Scan(&rows).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"orgs": rows})
}

// channelCallCounts：各渠道窗口内累计调用数（channel_id → count）。
func (s *Server) channelCallCounts(rdb *gorm.DB, since string) map[uint64]int64 {
	type row struct {
		ChannelID uint64
		Calls     int64
	}
	var rows []row
	rdb.Model(&invocation.Invocation{}).
		Select("channel_id, COUNT(*) AS calls").
		Where("created_at >= ? AND channel_id > 0", since).
		Group("channel_id").Scan(&rows)
	out := make(map[uint64]int64, len(rows))
	for _, r := range rows {
		out[r.ChannelID] = r.Calls
	}
	return out
}

// adminOverview：GET /api/admin/overview —— 平台级运营概览（admin 仪表盘）。
// 渠道健康分布 + 调用量/成功率 + 总收入 + 模型 top + 各渠道调用。窗口 14 天。
func (s *Server) adminOverview(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	const windowDays = 14
	now := s.now()
	since := now.AddDate(0, 0, -windowDays+1).Format("2006-01-02")
	today := now.Format("2006-01-02")

	// 渠道健康分布（status: 1=active, 0=disabled, 2=auto-disabled）
	type chCount struct {
		Status int8
		N      int64
	}
	var chCounts []chCount
	rdb.Model(&relay.Channel{}).Select("status, COUNT(*) AS n").Group("status").Scan(&chCounts)
	var total, active, disabled, autoDisabled int64
	for _, cc := range chCounts {
		total += cc.N
		switch cc.Status {
		case 1:
			active = cc.N
		case 2:
			autoDisabled = cc.N
		default:
			disabled = cc.N
		}
	}

	var callsToday, callsWindow, succeeded int64
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", today).Count(&callsToday)
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", since).Count(&callsWindow)
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ? AND status = ?", since, "success").Count(&succeeded)

	var revenue decimal.Decimal
	rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", since).
		Select("COALESCE(SUM(cost_usd), 0)").Scan(&revenue)

	type modelStat struct {
		Model string          `json:"model"`
		Calls int64           `json:"calls"`
		Cost  decimal.Decimal `json:"cost"`
	}
	var topModels []modelStat
	rdb.Model(&invocation.Invocation{}).
		Select("model, COUNT(*) AS calls, COALESCE(SUM(cost_usd), 0) AS cost").
		Where("created_at >= ?", since).
		Group("model").Order("calls DESC").Limit(8).Scan(&topModels)
	if topModels == nil {
		topModels = []modelStat{}
	}

	// 各渠道调用（含禁用渠道，运营要看历史活跃度）
	calls := s.channelCallCounts(rdb, since)
	var channels []relay.Channel
	rdb.Order("status DESC, id").Find(&channels)
	type chHealth struct {
		ID     uint64 `json:"id"`
		Name   string `json:"name"`
		Status int8   `json:"status"`
		Calls  int64  `json:"calls"`
	}
	health := make([]chHealth, 0, len(channels))
	for _, ch := range channels {
		health = append(health, chHealth{ch.ID, ch.Name, ch.Status, calls[ch.ID]})
	}

	successRate := 1.0
	if callsWindow > 0 {
		successRate = float64(succeeded) / float64(callsWindow)
	}
	c.JSON(http.StatusOK, gin.H{
		"window_days": windowDays,
		"channels":    gin.H{"total": total, "active": active, "disabled": disabled, "auto_disabled": autoDisabled},
		"calls_today": callsToday,
		"calls_window": callsWindow,
		"success_rate": successRate,
		"revenue_window": revenue,
		"top_models":    topModels,
		"channel_health": health,
	})
}

// adminListInvocations：GET /api/admin/invocations —— 全局调用日志（不受 org 隔离）。
// 筛选：status / model / channel_id / org_id / since / until（YYYY-MM-DD）。分页。
// admin 视图回显平台内部字段（org/channel/provider/上游 id），普通 /api/usage 仍隐藏这些。
func (s *Server) adminListInvocations(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := rdb.Model(&invocation.Invocation{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("model"); v != "" {
		q = q.Where("model = ?", v)
	}
	if v := c.Query("channel_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			q = q.Where("channel_id = ?", id)
		}
	}
	if v := c.Query("org_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			q = q.Where("org_id = ?", id)
		}
	}
	if v := c.Query("since"); v != "" {
		q = q.Where("created_at >= ?", v)
	}
	if v := c.Query("until"); v != "" {
		q = q.Where("created_at < ?", v+" 23:59:59")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "invocation query failed")
		return
	}
	var rows []invocation.Invocation
	if err := q.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "invocation query failed")
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{
			"id": r.ID, "created_at": r.CreatedAt, "request_id": r.RequestID,
			"org_id": r.OrgID, "model": r.Model,
			"channel_id": r.ChannelID, "provider": r.Provider,
			"status": r.Status, "status_code": r.StatusCode, "is_stream": r.IsStream,
			"attempts": r.Attempts, "prompt_tokens": r.PromptTokens, "completion_tokens": r.CompletionTokens,
			"cost_usd": r.CostUSD, "usage_source": r.UsageSource,
			"frt_ms": r.FRTMs, "duration_ms": r.DurationMs,
			"upstream_request_id": r.UpstreamRequestID, "error_brief": r.ErrorBrief,
		})
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "page_size": pageSize, "items": out})
}
