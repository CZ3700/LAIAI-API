package server

import (
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 平台管理 API（M1 内部验证阶段）：X-Admin-Token 门禁（常数时间比较）。
// M3 Console 落地后升级为正式 root RBAC；资金类操作的审计 = journal 本身（kind=grant 全留痕）。

// resolveAdmin：解析「带有效身份」的访问者（RBAC，0020）。只解析身份与角色，不做门槛判定
//
//	—— 门槛交给 adminRequired(min)，这样 min 参数对所有层级（support/admin/superadmin）都生效。
//	优先级：1) X-Admin-Token 命中 → root 破玻璃（视同 superadmin）；2) Bearer session 有效且用户在职 → 该用户身份与角色。
//
// 返回 (审计标签, 角色, userID, 是否有有效身份)。
func (s *Server) resolveAdmin(c *gin.Context) (label, role string, uid uint64, ok bool) {
	if len(s.adminToken) > 0 {
		got := c.GetHeader("X-Admin-Token")
		if subtle.ConstantTimeCompare([]byte(got), s.adminToken) == 1 {
			return "root-token", identity.PlatRoleSuperadmin, 0, true
		}
	}
	h := c.GetHeader("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		claims, err := identity.VerifySessionToken(s.secret, strings.TrimPrefix(h, "Bearer "), s.now())
		if err == nil {
			var u identity.User
			if e := s.db.WithContext(c.Request.Context()).
				Select("id, role, status").First(&u, claims.UserID).Error; e == nil &&
				u.Status == identity.StatusActive {
				r := u.Role
				if r == "" {
					r = identity.PlatRoleUser
				}
				// 审计标签只存 user#id(role)，不含 email（P2-4：审计日志禁 PII，红线 8）
				return fmt.Sprintf("user#%d(%s)", u.ID, r), r, u.ID, true
			}
		}
	}
	return "", "", 0, false
}

// adminRequired：后台门禁，要求访问者角色 ≥ min。
//
//	无有效身份 → 401；有身份但权限不足 → 403。通过后把身份写进 context（审计/二次校验用）。
func (s *Server) adminRequired(min string) gin.HandlerFunc {
	return func(c *gin.Context) {
		label, role, uid, ok := s.resolveAdmin(c)
		if !ok {
			fail(c, http.StatusUnauthorized, "admin access required")
			return
		}
		if identity.RoleRank(role) < identity.RoleRank(min) {
			fail(c, http.StatusForbidden, "insufficient role")
			return
		}
		c.Set("actor_label", label)
		c.Set("actor_role", role)
		c.Set("actor_id", uid)
		c.Next()
	}
}

// requireSuperadmin：在 adminRequired 之后追加，限定仅 superadmin（角色管理等高危操作）。
func (s *Server) requireSuperadmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("actor_role")
		if r, _ := role.(string); identity.RoleRank(r) < identity.RoleRank(identity.PlatRoleSuperadmin) {
			fail(c, http.StatusForbidden, "superadmin role required")
			return
		}
		c.Next()
	}
}

type grantReq struct {
	OrgID          uint64 `json:"org_id" binding:"required"`
	Mode           string `json:"mode"` // add | subtract | override；空=add（向后兼容）
	AmountUSD      string `json:"amount_usd" binding:"required"`
	Reason         string `json:"reason" binding:"required"`
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
}

var errBadAdjustMode = errors.New("invalid adjust mode")

// adminGrantCredit：授信入账（红线 2：幂等键由调用方提供，UNIQUE 兜底）。
func (s *Server) adminGrantCredit(c *gin.Context) {
	var req grantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "org_id, amount_usd, reason, idempotency_key required")
		return
	}
	amount, err := decimal.NewFromString(req.AmountUSD)
	if err != nil || amount.IsNegative() {
		fail(c, http.StatusBadRequest, "amount_usd must be a non-negative decimal string")
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "add" // 向后兼容：旧调用方不传 mode 即为授信
	}
	if (mode == "add" || mode == "subtract") && !amount.IsPositive() {
		fail(c, http.StatusBadRequest, "amount_usd must be positive for add/subtract")
		return
	}

	// 三档调账，全部走 ledger 事务（红线：余额永不直接 UPDATE；扣减由 Post 的可用余额守卫兜底）：
	//   add=授信(+) / subtract=扣减(-) / override=订正到目标值(delta=目标-当前，正补负扣)
	var entry *ledger.JournalEntry
	var applied, noop bool
	var balanceAfter decimal.Decimal
	err = s.db.Transaction(func(tx *gorm.DB) error {
		acc, err := ledger.EnsureAccount(tx, req.OrgID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		var delta decimal.Decimal
		var kind, refType string
		switch mode {
		case "add":
			delta, kind, refType = amount, ledger.KindGrant, "admin_grant"
		case "subtract":
			delta, kind, refType = amount.Neg(), ledger.KindDebit, "admin_debit"
		case "override":
			// P2-12 严格：行锁读余额再算 delta=目标-当前，并持锁到事务结束，
			// 确保 Post 看到同一余额 → 订正精确落到目标（旧实现用非锁读，并发会订偏）。
			locked, lerr := ledger.LockAccount(tx, acc.ID)
			if lerr != nil {
				return lerr
			}
			delta = amount.Sub(locked.PostedBalance)
			if delta.IsZero() {
				noop, balanceAfter = true, locked.PostedBalance
				return nil // 已等于目标，无需过账
			}
			if delta.IsNegative() {
				kind, refType = ledger.KindDebit, "admin_override"
			} else {
				kind, refType = ledger.KindGrant, "admin_override"
			}
		default:
			return errBadAdjustMode
		}
		entry, applied, err = ledger.Post(tx, ledger.PostInput{
			IdempotencyKey: "admin_adjust:" + req.IdempotencyKey,
			AccountID:      acc.ID,
			Kind:           kind,
			Delta:          delta,
			RefType:        refType,
			Reason:         req.Reason,
		})
		if err == nil {
			balanceAfter = entry.BalanceAfter
		}
		return err
	})
	switch {
	case errors.Is(err, errBadAdjustMode):
		fail(c, http.StatusBadRequest, "mode must be add, subtract or override")
		return
	case errors.Is(err, ledger.ErrInsufficientBalance):
		fail(c, http.StatusConflict, "insufficient available balance for this adjustment")
		return
	case err != nil:
		fail(c, http.StatusInternalServerError, "adjustment failed")
		return
	}
	if !applied && !noop {
		s.writeAudit(c, "credit_"+mode, strconv.FormatUint(req.OrgID, 10), mode+" "+req.AmountUSD+" USD: "+req.Reason)
	}
	c.JSON(http.StatusOK, gin.H{
		"balance_after": balanceAfter, "already_applied": applied, "noop": noop,
	})
}

type channelReq struct {
	Name               string            `json:"name" binding:"required"`
	Provider           string            `json:"provider" binding:"required"`
	BaseURL            string            `json:"base_url"` // 缺省取厂商档案默认端点（providers.go）
	APIKey             string            `json:"api_key" binding:"required"`
	Models             []string          `json:"models" binding:"required"`
	Weight             int               `json:"weight"`
	Priority           int               `json:"priority"`             // 大者先用；重试降层
	MaxConcurrentTasks int               `json:"max_concurrent_tasks"` // 任务并发闸，默认 3
	ModelMapping       map[string]string `json:"model_mapping"`        // 对外名→上游名（可选）
	Proxy              string            `json:"proxy"`                // 渠道级出网代理（0019，可选）
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

// invalidateCache：admin 写后失效 relay 热读缓存（本地 + 跨实例 Redis 广播）。
// relay 缺失（keyring 未配的测试路径）时安全 no-op。WS-2 多实例一致性 + 单实例改配即时生效。
func (s *Server) invalidateCache(kind, key string) {
	if s.relay != nil {
		s.relay.InvalidateCache(kind, key)
	}
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
		BaseURL:       strings.TrimRight(req.BaseURL, "/"),
		CredentialEnc: enc, Models: strings.Join(req.Models, ","),
		Weight: weight, Priority: req.Priority,
		MaxConcurrentTasks: maxTasks, StrictResponse: 1,
		ModelMapping: mapping, Proxy: strings.TrimSpace(req.Proxy), Status: 1,
	}
	if err := s.db.Create(ch).Error; err != nil {
		fail(c, http.StatusInternalServerError, "channel create failed")
		return
	}
	s.writeAudit(c, "channel_create", strconv.FormatUint(ch.ID, 10), "created channel "+ch.Name+" ("+ch.Provider+")")
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusCreated, gin.H{"id": ch.ID, "name": ch.Name})
}

type updateChannelReq struct {
	BaseURL        *string            `json:"base_url"`
	APIKey         *string            `json:"api_key"` // 省略=不改凭证
	Models         []string           `json:"models"`
	Weight         *int               `json:"weight"`
	Priority       *int               `json:"priority"`
	StrictResponse *bool              `json:"strict_response"`
	ModelMapping   *map[string]string `json:"model_mapping"` // 指针区分"不改"与"清空"
	Proxy          *string            `json:"proxy"`         // 指针区分"不改"与"清空代理"
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
	if req.Proxy != nil {
		updates["proxy"] = strings.TrimSpace(*req.Proxy)
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
	s.writeAudit(c, "channel_update", strconv.FormatUint(id, 10), "channel config updated")
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusOK, gin.H{"updated": id})
}

func (s *Server) adminListChannels(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	var channels []relay.Channel
	if err := rdb.Order("id").Find(&channels).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	// 累计调用数 + 质量（14 天窗口，与 overview 同口径）
	since := s.now().UTC().AddDate(0, 0, -13).Format("2006-01-02")
	calls := s.channelCallCounts(rdb, since)
	quality := s.channelQualityStats(rdb, since)
	out := make([]gin.H, 0, len(channels))
	for _, ch := range channels {
		modelCount := 0
		if ch.Models != "" {
			modelCount = len(strings.Split(ch.Models, ","))
		}
		total := calls[ch.ID]
		q := quality[ch.ID]
		successRate := 1.0 // 无调用视作健康（不误报红）
		if total > 0 {
			successRate = float64(q.Succeeded) / float64(total)
		}
		out = append(out, gin.H{ // 凭证永不回显，连密文都不给
			"id": ch.ID, "name": ch.Name, "provider": ch.Provider,
			"base_url": ch.BaseURL, "models": ch.Models, "model_count": modelCount,
			"weight": ch.Weight, "priority": ch.Priority,
			"strict_response": ch.EnforcesStrictResponse(),
			"model_mapping":   ch.ModelMapping, "calls": total, "status": ch.Status,
			"success_rate": successRate, "avg_latency_ms": q.AvgDuration, "errors": q.Errors,
			"proxy": ch.Proxy,
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
	s.writeAudit(c, "channel_disable", strconv.FormatUint(id, 10), "channel disabled")
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusOK, gin.H{"disabled": id})
}

type priceReq struct {
	Model                string `json:"model" binding:"required"`
	InputUSDPer1M        string `json:"input_usd_per_1m" binding:"required"`
	OutputUSDPer1M       string `json:"output_usd_per_1m" binding:"required"`
	ImageUSD             string `json:"image_usd"`                  // 每张图 USD；图像模型必填，其他模型省略
	CachedInputUSDPer1M  string `json:"cached_input_usd_per_1m"`    // 缓存命中输入价（0010，省略=按 input 全价）
	PerCallUSD           string `json:"per_call_usd"`               // 按次固定价（0027，gap P2-F）
	TTSPer1KChars        string `json:"tts_usd_per_1k_chars"`       // TTS 每千字符 USD（0027）
	TranscriptionPerCall string `json:"transcription_usd_per_call"` // 转录每次固定价 USD（0027）
}

// firstErr：返回首个非 nil 错误。
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// parseOptNonNegDecimal：解析可选的非负十进制串。空串 → 0；非法/负 → 报错。
func parseOptNonNegDecimal(s, field string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	v, err := decimal.NewFromString(s)
	if err != nil || v.IsNegative() {
		return decimal.Zero, errors.New(field + " must be a non-negative decimal string")
	}
	return v, nil
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
	cachedIn := decimal.Zero
	if req.CachedInputUSDPer1M != "" {
		var err error
		if cachedIn, err = decimal.NewFromString(req.CachedInputUSDPer1M); err != nil || cachedIn.IsNegative() {
			fail(c, http.StatusBadRequest, "cached_input_usd_per_1m must be a non-negative decimal string")
			return
		}
	}
	perCall, e1 := parseOptNonNegDecimal(req.PerCallUSD, "per_call_usd")
	tts, e2 := parseOptNonNegDecimal(req.TTSPer1KChars, "tts_usd_per_1k_chars")
	transcribe, e3 := parseOptNonNegDecimal(req.TranscriptionPerCall, "transcription_usd_per_call")
	if e1 != nil || e2 != nil || e3 != nil {
		fail(c, http.StatusBadRequest, firstErr(e1, e2, e3).Error())
		return
	}
	p := &relay.ModelPrice{
		Model: req.Model, InputUSDPer1M: in, OutputUSDPer1M: out, ImageUSD: img, CachedInputUSDPer1M: cachedIn,
		PerCallUSD: perCall, TTSPer1KChars: tts, TranscriptionPerCall: transcribe,
	}
	err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"input_usd_per_1m", "output_usd_per_1m", "image_usd", "cached_input_usd_per_1m",
			"per_call_usd", "tts_usd_per_1k_chars", "transcription_usd_per_call",
		}),
	}).Create(p).Error
	if err != nil {
		fail(c, http.StatusInternalServerError, "price upsert failed")
		return
	}
	s.writeAudit(c, "price_upsert", p.Model, "set price in="+req.InputUSDPer1M+" out="+req.OutputUSDPer1M+" /1M USD")
	s.invalidateCache(relay.KindPrice, p.Model)
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
		Group         string          `json:"group"`
		OwnerEmail    string          `json:"owner_email"`
		Members       int64           `json:"members"`
		PostedBalance decimal.Decimal `json:"balance"`
		HeldAmount    decimal.Decimal `json:"held"`
		Calls         int64           `json:"calls"`       // 生涯调用数
		Spend         decimal.Decimal `json:"spend"`       // 生涯消耗
		LastActive    string          `json:"last_active"` // 最近一次调用时间（空=从未调用）
		// Entitlements（P0-2 功能调配）：无配置行 → COALESCE 回平台默认（全开/不限）。
		FeatureImages bool `json:"feature_images"`
		FeatureVideo  bool `json:"feature_video"`
		MaxMembers    int  `json:"max_members"`
		MaxAPIKeys    int  `json:"max_api_keys"`
	}
	// 活跃度子查询独立聚合（每 org 一行），避免与 members 子查询交叉相乘
	err := s.db.Raw(`
		SELECT o.id, o.name, o.kind, o.status, o.group_name AS ` + "`group`" + `,
		       COALESCE(u.email, '') AS owner_email,
		       (SELECT COUNT(*) FROM org_members m WHERE m.org_id = o.id) AS members,
		       COALESCE(a.posted_balance, 0) AS posted_balance,
		       COALESCE(a.held_amount, 0) AS held_amount,
		       COALESCE(iv.calls, 0) AS calls,
		       COALESCE(iv.spend, 0) AS spend,
		       COALESCE(iv.last_active, '') AS last_active,
		       COALESCE(e.feature_images, 1) AS feature_images,
		       COALESCE(e.feature_video, 1) AS feature_video,
		       COALESCE(e.max_members, 0) AS max_members,
		       COALESCE(e.max_api_keys, 0) AS max_api_keys
		FROM organizations o
		LEFT JOIN org_members om ON om.org_id = o.id AND om.role = 'owner'
		LEFT JOIN users u ON u.id = om.user_id
		LEFT JOIN ledger_accounts a ON a.org_id = o.id AND a.type = 'user_credit'
		LEFT JOIN org_entitlements e ON e.org_id = o.id
		LEFT JOIN (
		    SELECT org_id, COUNT(*) AS calls,
		           COALESCE(SUM(cost_usd), 0) AS spend, MAX(created_at) AS last_active
		    FROM invocations GROUP BY org_id
		) iv ON iv.org_id = o.id
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

// chQuality：渠道窗口质量（成功数 / 平均时延 / 错误数）。
type chQuality struct {
	Succeeded   int64
	AvgDuration float64
	Errors      int64
}

// channelQualityStats：各渠道窗口内质量聚合（channel_id → chQuality）。
// 与 channelCallCounts 同 since 口径，便于用 calls 当分母算成功率。
func (s *Server) channelQualityStats(rdb *gorm.DB, since string) map[uint64]chQuality {
	type row struct {
		ChannelID   uint64
		Succeeded   int64
		AvgDuration float64
		Errors      int64
	}
	var rows []row
	rdb.Model(&invocation.Invocation{}).
		Select("channel_id, "+
			"SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS succeeded, "+
			"COALESCE(AVG(duration_ms), 0) AS avg_duration, "+
			"SUM(CASE WHEN status <> 'success' THEN 1 ELSE 0 END) AS errors").
		Where("created_at >= ? AND channel_id > 0", since).
		Group("channel_id").Scan(&rows)
	out := make(map[uint64]chQuality, len(rows))
	for _, r := range rows {
		out[r.ChannelID] = chQuality{Succeeded: r.Succeeded, AvgDuration: r.AvgDuration, Errors: r.Errors}
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
		"window_days":    windowDays,
		"channels":       gin.H{"total": total, "active": active, "disabled": disabled, "auto_disabled": autoDisabled},
		"calls_today":    callsToday,
		"calls_window":   callsWindow,
		"success_rate":   successRate,
		"revenue_window": revenue,
		"top_models":     topModels,
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
		// 超管视图：回显全部落库元数据字段（含 user_id / api_key_id）。
		// 普通 /api/usage 仍按 json tag 隐藏平台内部字段。
		// 注（P1-4）：本表是**元数据-only** 存储，不落 request/response payload——
		// 故不回显 payload_object_key（当前恒为空、指向不存在的对象，回显即"超承诺"）。
		out = append(out, gin.H{
			"id": r.ID, "created_at": r.CreatedAt, "request_id": r.RequestID,
			"org_id": r.OrgID, "user_id": r.UserID, "api_key_id": r.APIKeyID, "model": r.Model,
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

// adminProbeChannel：POST /api/admin/channels/probe {api_key, base_url}
// 上货前自检：试出正确 base_url 格式 + 拉可调模型列表。不落库不计费。
func (s *Server) adminProbeChannel(c *gin.Context) {
	if s.relay == nil {
		fail(c, http.StatusServiceUnavailable, "relay not configured")
		return
	}
	var req struct {
		APIKey   string `json:"api_key" binding:"required"`
		BaseURL  string `json:"base_url" binding:"required"`
		Provider string `json:"provider"` // 可选；决定 /models 鉴权与解析口径（P2-10），空=openai 兼容
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "api_key and base_url required")
		return
	}
	c.JSON(http.StatusOK, s.relay.ProbeChannel(c.Request.Context(), req.APIKey, req.BaseURL, req.Provider))
}

// —— 分组倍率（0011，差异化定价） ——

type GroupRatio struct {
	Name  string          `gorm:"primaryKey;column:name" json:"name"`
	Ratio decimal.Decimal `gorm:"column:ratio" json:"ratio"`
}

func (GroupRatio) TableName() string { return "group_ratios" }

// adminListGroupRatios：GET /api/admin/group-ratios
func (s *Server) adminListGroupRatios(c *gin.Context) {
	var rows []GroupRatio
	if err := s.db.Order("name").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"groups": rows})
}

// adminUpsertGroupRatio：POST /api/admin/group-ratios {name, ratio}
func (s *Server) adminUpsertGroupRatio(c *gin.Context) {
	var req struct {
		Name  string `json:"name" binding:"required"`
		Ratio string `json:"ratio" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name and ratio required")
		return
	}
	ratio, err := decimal.NewFromString(req.Ratio)
	if err != nil || ratio.IsNegative() {
		fail(c, http.StatusBadRequest, "ratio must be a non-negative decimal string")
		return
	}
	g := &GroupRatio{Name: req.Name, Ratio: ratio}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"ratio"}),
	}).Create(g).Error; err != nil {
		fail(c, http.StatusInternalServerError, "upsert failed")
		return
	}
	s.writeAudit(c, "group_ratio_set", g.Name, "ratio = "+req.Ratio)
	s.invalidateCache(relay.KindRatio, "")
	c.JSON(http.StatusOK, gin.H{"name": g.Name})
}

// adminDeleteGroupRatio：DELETE /api/admin/group-ratios/:name（default 不可删）
func (s *Server) adminDeleteGroupRatio(c *gin.Context) {
	name := c.Param("name")
	if name == "default" {
		fail(c, http.StatusBadRequest, "the default group cannot be deleted")
		return
	}
	if err := s.db.Where("name = ?", name).Delete(&GroupRatio{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "delete failed")
		return
	}
	s.writeAudit(c, "group_ratio_delete", name, "group ratio deleted")
	s.invalidateCache(relay.KindRatio, "")
	c.JSON(http.StatusOK, gin.H{"deleted": name})
}

// —— 二层分组倍率（0027，gap P2-F）：(分组, 模型) → 倍率，叠加在分组倍率之上 ——

type ModelGroupRatio struct {
	GroupName string          `gorm:"primaryKey;column:group_name" json:"group_name"`
	Model     string          `gorm:"primaryKey;column:model" json:"model"`
	Ratio     decimal.Decimal `gorm:"column:ratio" json:"ratio"`
}

func (ModelGroupRatio) TableName() string { return "model_group_ratios" }

// adminListModelGroupRatios：GET /api/admin/model-group-ratios
func (s *Server) adminListModelGroupRatios(c *gin.Context) {
	var rows []ModelGroupRatio
	if err := s.db.Order("group_name, model").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// adminUpsertModelGroupRatio：POST /api/admin/model-group-ratios {group_name, model, ratio}
func (s *Server) adminUpsertModelGroupRatio(c *gin.Context) {
	var req struct {
		GroupName string `json:"group_name" binding:"required"`
		Model     string `json:"model" binding:"required"`
		Ratio     string `json:"ratio" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "group_name, model and ratio required")
		return
	}
	ratio, err := decimal.NewFromString(req.Ratio)
	if err != nil || ratio.IsNegative() {
		fail(c, http.StatusBadRequest, "ratio must be a non-negative decimal string")
		return
	}
	g := &ModelGroupRatio{GroupName: req.GroupName, Model: req.Model, Ratio: ratio}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "group_name"}, {Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{"ratio"}),
	}).Create(g).Error; err != nil {
		fail(c, http.StatusInternalServerError, "upsert failed")
		return
	}
	s.writeAudit(c, "model_group_ratio_set", req.GroupName+"/"+req.Model, "ratio = "+req.Ratio)
	s.invalidateCache(relay.KindRatio, "")
	c.JSON(http.StatusOK, gin.H{"group_name": req.GroupName, "model": req.Model})
}

// adminDeleteModelGroupRatio：DELETE /api/admin/model-group-ratios?group_name=&model=
func (s *Server) adminDeleteModelGroupRatio(c *gin.Context) {
	groupName, model := c.Query("group_name"), c.Query("model")
	if groupName == "" || model == "" {
		fail(c, http.StatusBadRequest, "group_name and model query params required")
		return
	}
	if err := s.db.Where("group_name = ? AND model = ?", groupName, model).Delete(&ModelGroupRatio{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "delete failed")
		return
	}
	s.writeAudit(c, "model_group_ratio_delete", groupName+"/"+model, "deleted")
	s.invalidateCache(relay.KindRatio, "")
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// adminSetOrgGroup：PUT /api/admin/orgs/:id/group {group}
func (s *Server) adminSetOrgGroup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid org id")
		return
	}
	var req struct {
		Group string `json:"group" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "group required")
		return
	}
	res := s.db.Model(&identity.Organization{}).Where("id = ?", id).Update("group_name", req.Group)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "org not found")
		return
	}
	s.writeAudit(c, "org_group_set", strconv.FormatUint(id, 10), "group → "+req.Group)
	s.invalidateCache(relay.KindRatio, "") // 改 org 分组影响该 org 所有 (org,model) 倍率
	c.JSON(http.StatusOK, gin.H{"id": id, "group": req.Group})
}

// adminSetEntitlements：PUT /api/admin/orgs/:id/entitlements —— 超管按 org 分配功能与上限（P0-2）。
// 整行覆盖语义：请求体即新配置；缺省字段按零值落库（feature=关、上限=0/不限）。
func (s *Server) adminSetEntitlements(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid org id")
		return
	}
	var req struct {
		FeatureImages bool `json:"feature_images"`
		FeatureVideo  bool `json:"feature_video"`
		MaxMembers    int  `json:"max_members"`
		MaxAPIKeys    int  `json:"max_api_keys"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MaxMembers < 0 || req.MaxAPIKeys < 0 {
		fail(c, http.StatusBadRequest, "limits must be non-negative (0 = unlimited)")
		return
	}
	// org 必须存在（避免给不存在的 org 写孤儿配置）。
	var exists int64
	if err := s.db.Model(&identity.Organization{}).Where("id = ?", id).Count(&exists).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query failed")
		return
	}
	if exists == 0 {
		fail(c, http.StatusNotFound, "org not found")
		return
	}
	ent := identity.Entitlements{
		OrgID:         id,
		FeatureImages: req.FeatureImages,
		FeatureVideo:  req.FeatureVideo,
		MaxMembers:    req.MaxMembers,
		MaxAPIKeys:    req.MaxAPIKeys,
	}
	if err := identity.UpsertEntitlements(s.db, ent); err != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	s.writeAudit(c, "org_entitlements_set", strconv.FormatUint(id, 10),
		fmt.Sprintf("images=%t video=%t max_members=%d max_keys=%d",
			ent.FeatureImages, ent.FeatureVideo, ent.MaxMembers, ent.MaxAPIKeys))
	c.JSON(http.StatusOK, gin.H{
		"org_id": id, "feature_images": ent.FeatureImages, "feature_video": ent.FeatureVideo,
		"max_members": ent.MaxMembers, "max_api_keys": ent.MaxAPIKeys,
	})
}

// adminListUsers：GET /api/admin/users?page=&q= —— 用户清单（含状态/注册时间，运营视图）。
func (s *Server) adminListUsers(c *gin.Context) {
	page := 1
	if n, err := strconv.Atoi(c.Query("page")); err == nil && n > 0 {
		page = n
	}
	const pageSize = 30
	rdb := s.db.WithContext(c.Request.Context())

	where, args := "", []any{}
	if kw := c.Query("q"); kw != "" {
		like := "%" + kw + "%"
		where = " WHERE u.email LIKE ? OR u.display_name LIKE ?"
		args = append(args, like, like)
	}
	var total int64
	rdb.Raw("SELECT COUNT(*) FROM users u"+where, args...).Scan(&total)

	// 每个用户带出其个人组织(kind=0)与余额——授信入账（加余额）的 ledger 目标。
	// 相关子查询锁定唯一个人组织，避免 owner 多组织 JOIN 扇出导致行重复/分页错乱。
	var rows []struct {
		ID           uint64          `json:"id"`
		Email        string          `json:"email"`
		DisplayName  string          `json:"display_name"`
		Status       int8            `json:"status"`
		Role         string          `json:"role"` // 平台角色（0020 RBAC）
		OrgID        uint64          `json:"org_id"`
		Balance      decimal.Decimal `json:"balance"`
		Group        string          `json:"group"`          // 个人组织计费分组（=层级/tier）
		Invited      int64           `json:"invited"`        // 该用户邀请的组织数
		Earned       decimal.Decimal `json:"earned"`         // 累计返佣（affiliate ledger）
		InviterOrgID uint64          `json:"inviter_org_id"` // 邀请来源组织（0=无）
		AdminRemark  string          `json:"admin_remark"`   // 运营备注（0025，gap P2-B）
	}
	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rdb.Raw(`
		SELECT u.id, u.email, u.display_name, u.status, u.role, u.admin_remark,
		       COALESCE(o.id, 0) AS org_id,
		       COALESCE(a.posted_balance, 0) AS balance,
		       COALESCE(o.group_name, 'default') AS `+"`group`"+`,
		       COALESCE(o.inviter_org_id, 0) AS inviter_org_id,
		       (SELECT COUNT(*) FROM organizations oi WHERE oi.inviter_org_id = o.id) AS invited,
		       (SELECT COALESCE(SUM(j.delta), 0) FROM ledger_journal j
		           JOIN ledger_accounts la ON la.id = j.account_id
		           WHERE la.org_id = o.id AND j.ref_type = 'affiliate') AS earned
		FROM users u
		LEFT JOIN organizations o ON o.id = (
		    SELECT om.org_id FROM org_members om
		    JOIN organizations oo ON oo.id = om.org_id
		    WHERE om.user_id = u.id AND om.role = 'owner' AND oo.kind = 0
		    ORDER BY om.org_id LIMIT 1)
		LEFT JOIN ledger_accounts a ON a.org_id = o.id AND a.type = 'user_credit'`+
		where+` ORDER BY u.id DESC LIMIT ? OFFSET ?`, pageArgs...).Scan(&rows)

	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "page_size": pageSize, "items": rows})
}

// adminManageUser：POST /api/admin/users/:id/manage {action: ban|unban}
func (s *Server) adminManageUser(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}
	var req struct {
		Action string `json:"action" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "action required")
		return
	}
	var status int8
	switch req.Action {
	case "ban":
		status = identity.StatusDisabled
	case "unban":
		status = identity.StatusActive
	default:
		fail(c, http.StatusBadRequest, "action must be ban or unban")
		return
	}
	// 等级守卫（对标 NewAPI）：不能封禁同级/更高角色（admin 动不了 admin/superadmin）；
	// 也不能封禁自己。root-token(actor_id=0) 视作 superadmin，不受自我保护限制。
	var target identity.User
	if e := s.db.Select("id, role").First(&target, id).Error; e != nil {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	if aid, _ := c.Get("actor_id"); aid != nil {
		if v, _ := aid.(uint64); v != 0 && v == id {
			fail(c, http.StatusBadRequest, "cannot ban yourself")
			return
		}
	}
	if !identity.CanManageUser(c.GetString("actor_role"), target.Role) {
		fail(c, http.StatusForbidden, "cannot manage a user of equal or higher role")
		return
	}
	res := s.db.Model(&identity.User{}).Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	s.writeAudit(c, "user_"+req.Action, strconv.FormatUint(id, 10), "user "+req.Action)
	c.JSON(http.StatusOK, gin.H{"id": id, "status": status})
}

// adminSetUserRemark：POST /api/admin/users/:id/remark {remark} —— 设运营备注（gap P2-B）。
// 等级守卫：不能给同级/更高角色加备注（与 ban 同口径，防越权标注上级）。备注长度上限 500。
func (s *Server) adminSetUserRemark(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}
	var req struct {
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "remark required")
		return
	}
	if len([]rune(req.Remark)) > 500 {
		fail(c, http.StatusBadRequest, "remark too long (max 500 chars)")
		return
	}
	var target identity.User
	if e := s.db.Select("id, role").First(&target, id).Error; e != nil {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	if !identity.CanManageUser(c.GetString("actor_role"), target.Role) {
		fail(c, http.StatusForbidden, "cannot annotate a user of equal or higher role")
		return
	}
	if e := s.db.Model(&identity.User{}).Where("id = ?", id).Update("admin_remark", req.Remark).Error; e != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	s.writeAudit(c, "user_remark", strconv.FormatUint(id, 10), "set admin remark")
	c.JSON(http.StatusOK, gin.H{"id": id, "admin_remark": req.Remark})
}

// adminUsageByUser：GET /api/admin/usage/by-user?days=N&limit=M —— 全平台近 N 天按用户聚合用量
// （调用数/成功数/token/费用），按费用降序。运营看「谁在烧钱 / 谁是大客户」。user_id 为空的调用
// （无成员归属）归入 user_id=0 一行。DATE()/时间串比较两库同义（见 usage.go 注释）。
func (s *Server) adminUsageByUser(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 365 {
		days = 30
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows := s.usageByUserRows(c, days, limit)
	c.JSON(http.StatusOK, gin.H{"days": days, "items": rows})
}

type usageByUserRow struct {
	UserID           uint64          `json:"user_id"`
	Email            string          `json:"email"`
	Calls            int64           `json:"calls"`
	Succeeded        int64           `json:"succeeded"`
	PromptTokens     int64           `json:"prompt_tokens"`
	CompletionTokens int64           `json:"completion_tokens"`
	CostUSD          decimal.Decimal `json:"cost_usd"`
}

// usageByUserRows：usage-by-user 聚合（JSON 与 CSV 导出共用同一口径，避免两处漂移）。
func (s *Server) usageByUserRows(c *gin.Context, days, limit int) []usageByUserRow {
	since := s.now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	rows := []usageByUserRow{} // 非 nil：空结果序列化为 [] 而非 null（前端按数组消费）
	s.db.WithContext(c.Request.Context()).Raw(`
		SELECT COALESCE(i.user_id, 0) AS user_id,
		       COALESCE(u.email, '') AS email,
		       COUNT(*) AS calls,
		       SUM(CASE WHEN i.status = 'success' THEN 1 ELSE 0 END) AS succeeded,
		       COALESCE(SUM(i.prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(i.completion_tokens), 0) AS completion_tokens,
		       COALESCE(SUM(i.cost_usd), 0) AS cost_usd
		FROM invocations i
		LEFT JOIN users u ON u.id = i.user_id
		WHERE i.created_at >= ?
		GROUP BY COALESCE(i.user_id, 0), COALESCE(u.email, '')
		ORDER BY cost_usd DESC
		LIMIT ?`, since, limit).Scan(&rows)
	return rows
}

// adminUsageByUserExport：GET /api/admin/usage/by-user/export?days=N —— 同上聚合的 CSV 下载（gap P2-B）。
// 与 JSON 同口径（usageByUserRows）。CSV 注入防护：以 =,+,-,@ 起首的字段加前导单引号。
func (s *Server) adminUsageByUserExport(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 365 {
		days = 30
	}
	rows := s.usageByUserRows(c, days, 1000)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="usage_by_user.csv"`)
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"user_id", "email", "calls", "succeeded", "prompt_tokens", "completion_tokens", "cost_usd"})
	for _, r := range rows {
		_ = w.Write([]string{
			strconv.FormatUint(r.UserID, 10), csvSafe(r.Email),
			strconv.FormatInt(r.Calls, 10), strconv.FormatInt(r.Succeeded, 10),
			strconv.FormatInt(r.PromptTokens, 10), strconv.FormatInt(r.CompletionTokens, 10),
			r.CostUSD.String(),
		})
	}
	w.Flush()
	s.writeAudit(c, "usage_export", "", "exported usage-by-user CSV")
}

// csvSafe：防 CSV 公式注入——电子表格会把以 = + - @ 起首的单元格当公式执行。
// 给这类字段加前导单引号（被表格视作文本）。encoding/csv 已处理逗号/引号/换行的转义。
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// adminSetUserRole：POST /api/admin/users/:id/role {role} —— 授角色（仅 superadmin，0020 RBAC）。
func (s *Server) adminSetUserRole(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "role required")
		return
	}
	if !identity.ValidRole(req.Role) {
		fail(c, http.StatusBadRequest, "invalid role (user|support|admin|superadmin)")
		return
	}
	// 自我保护：实名 actor 不能改自己角色（防误降级锁死/自抬权）；root-token(id=0) 不受限。
	if v, ok := c.Get("actor_id"); ok {
		if aid, _ := v.(uint64); aid != 0 && aid == id {
			fail(c, http.StatusBadRequest, "cannot change your own role")
			return
		}
	}
	res := s.db.Model(&identity.User{}).Where("id = ?", id).Update("role", req.Role)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	s.writeAudit(c, "user_role_set", strconv.FormatUint(id, 10), "role → "+req.Role)
	c.JSON(http.StatusOK, gin.H{"id": id, "role": req.Role})
}
