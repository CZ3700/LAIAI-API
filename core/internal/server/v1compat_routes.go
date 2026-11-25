package server

import (
	"cmp"
	"context"
	"crypto/rand"
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
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"github.com/laiai/laiai-next/core/internal/server/v1compat"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) registerV1CompatRoutes(r *gin.Engine) {
	v1 := r.Group("/api/v1")
	v1.Use(bodyLimit(controlPlaneBodyLimit))

	// ── 公开免登录端点 ────────────────────────
	v1.GET("/auth/init-status", s.v1InitStatus)
	v1.POST("/auth/init-admin", s.authRateLimit(), s.v1InitAdmin)
	v1.POST("/auth/login", s.authRateLimit(), s.v1Login)
	v1.POST("/auth/2fa", s.authRateLimit(), s.v1TwofaLogin)
	v1.POST("/auth/register", s.authRateLimit(), s.register)
	v1.POST("/auth/register-email", s.authRateLimit(), s.register)
	v1.POST("/auth/send-code", s.authRateLimit(), s.v1SendCode)
	v1.GET("/auth/oauth", s.getOAuthProviders)
	v1.GET("/auth/oauth/:provider/start", s.authRateLimit(), s.oauthStart)
	v1.GET("/auth/oauth/:provider/callback", s.authRateLimit(), s.oauthCallback)

	v1.GET("/settings", s.v1PublicSettings)
	v1.GET("/plugins/active", s.v1ActivePlugins)
	v1.GET("/announcements/public", s.v1PublicAnnouncements)
	v1.GET("/marketplace/public", s.v1MarketplacePublic)
	v1.GET("/plugins/docs-api/public/tree", s.v1DocsPublicTree)
	v1.GET("/plugins/docs-api/public/docs/:id", s.v1DocsPublicDetail)

	// ── 已登录用户端点 ────────────────────────
	authed := v1.Group("", s.authRequired(), s.requireActiveUser())
	{
		authed.GET("/console/bootstrap", s.v1ConsoleBootstrap)
		authed.GET("/user/profile", s.v1GetProfile)
		authed.PUT("/user/profile", s.v1UpdateProfile)
		authed.POST("/user/password", s.changePassword)

		org := authed.Group("", s.requireActiveMembership())
		{
			org.GET("/dashboard/stats", s.v1DashboardStats)
			org.GET("/tokens", s.v1ListTokens)
			org.POST("/tokens", s.requireStepUpForKeys(), s.v1CreateToken)
			org.PUT("/tokens/:id", s.v1UpdateToken)
			org.DELETE("/tokens/:id", s.v1DeleteToken)
			org.POST("/tokens/:id/reveal", s.v1ForbidTokenReveal)

			org.GET("/logs", s.v1ListLogs)
			org.GET("/logs/:id/detail", s.v1LogDetail)
			org.GET("/task_logs", s.v1ListTaskLogs)
			org.GET("/task_logs/:id/detail", s.v1TaskLogDetail)
			org.POST("/task_logs/:id/sync", s.v1SyncTaskLog)
			org.POST("/task_logs/:id/cancel", s.v1CancelTaskLog)

			org.GET("/user/wallet", s.v1UserWallet)
			org.GET("/user/recharge_records", s.v1UserRechargeRecords)
		}
	}

	// ── 管理端 /api/v1/* 桥接 ──────────────────
	admin := v1.Group("", s.adminRequired(identity.PlatRoleAdmin))
	{
		admin.GET("/model-providers", s.v1ModelProviders)
		admin.GET("/model-types", s.v1ModelTypes)

		admin.GET("/channel-categories", s.v1ListChannelCategories)
		admin.POST("/channel-categories", s.adminCreateChannelCategory)
		admin.PUT("/channel-categories/:id", s.adminUpdateChannelCategory)
		admin.DELETE("/channel-categories/:id", s.adminDeleteChannelCategory)

		admin.GET("/billing-rules", s.v1ListBillingRules)
		admin.POST("/billing-rules", s.adminCreateBillingRule)
		admin.PUT("/billing-rules/:id", s.adminUpdateBillingRule)
		admin.DELETE("/billing-rules/:id", s.adminDeleteBillingRule)

		admin.GET("/forward-rules", s.v1ListForwardRules)
		admin.POST("/forward-rules", s.adminCreateForwardRule)
		admin.PUT("/forward-rules/:id", s.adminUpdateForwardRule)
		admin.DELETE("/forward-rules/:id", s.adminDeleteForwardRule)

		admin.GET("/models", s.v1AdminListModels)
		admin.POST("/models", s.v1AdminUpsertModel)
		admin.PUT("/models/:id", s.v1AdminUpsertModel)
		admin.DELETE("/models/:id", s.v1AdminDeleteModel)

		admin.GET("/announcements", s.v1AdminListAnnouncements)
		admin.POST("/announcements", s.v1AdminCreateAnnouncement)
		admin.PUT("/announcements/:id", s.v1AdminUpdateAnnouncement)
		admin.DELETE("/announcements/:id", s.v1AdminDeleteAnnouncement)

		admin.GET("/plugins/docs-api/docs", s.v1AdminListDocs)
		admin.POST("/plugins/docs-api/docs", s.v1AdminCreateDoc)
		admin.GET("/plugins/docs-api/docs/:id", s.v1AdminGetDoc)
		admin.PUT("/plugins/docs-api/docs/:id", s.v1AdminUpdateDoc)
		admin.DELETE("/plugins/docs-api/docs/:id", s.v1AdminDeleteDoc)
		admin.PUT("/plugins/docs-api/docs/:id/default-expanded", s.v1AdminSetDocExpanded)
		admin.POST("/plugins/docs-api/docs/import-default", s.v1AdminImportDefaultDocs)
		admin.POST("/plugins/docs-api/docs/clear-all", s.v1AdminClearAllDocs)
	}
}

func (s *Server) buildV1UserDTO(user *identity.User, orgID uint64, fallbackRole ...string) gin.H {
	role := user.Role
	if len(fallbackRole) > 0 && fallbackRole[0] != "" {
		role = fallbackRole[0]
	}
	if role == "" {
		role = identity.PlatRoleUser
	}
	// Map superadmin/admin uniformly to role='admin' for Console UI route guards; sub-admin flag distinguishes privilege.
	uiRole := "user"
	var adminGroupID *uint64
	if identity.RoleRank(role) >= identity.RoleRank(identity.PlatRoleAdmin) {
		uiRole = "admin"
		if role != identity.PlatRoleSuperadmin {
			g := uint64(1)
			adminGroupID = &g
		}
	}
	posted, _, _ := ledger.OrgBalances(s.db, orgID, ledger.AccountUserCredit)
	displayName := cmp.Or(strings.TrimSpace(user.DisplayName), user.Email)
	return gin.H{
		"id":                         user.ID,
		"user_id":                    user.ID,
		"uid":                        strconv.FormatUint(user.ID, 10),
		"username":                   displayName,
		"display_name":               user.DisplayName,
		"email":                      user.Email,
		"org_id":                     orgID,
		"role":                       uiRole,
		"plat_role":                  role,
		"admin_group_id":             adminGroupID,
		"user_group":                 "default",
		"is_active":                  user.Status == identity.StatusActive,
		"quota":                      posted,
		"balance":                    posted,
		"display_currency":           normalizeCurrency(user.DisplayCurrency),
		"currency_display_available": s.currencyDisplayAvailable(user.ID, orgID),
		"totp_enabled":               user.TOTPEnabled != 0,
	}
}

func (s *Server) v1InitStatus(c *gin.Context) {
	var userCount int64
	_ = s.db.WithContext(c.Request.Context()).Model(&identity.User{}).Count(&userCount).Error
	c.JSON(http.StatusOK, gin.H{
		"initialized": userCount > 0 || len(s.adminToken) > 0,
		"has_users":   userCount > 0,
	})
}

func (s *Server) v1InitAdmin(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		Email       string `json:"email" binding:"required"`
		Password    string `json:"password" binding:"required"`
		DisplayName string `json:"display_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "email and password are required")
		return
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&identity.User{}).Count(&count).Error; err != nil {
		fail(c, http.StatusInternalServerError, "init check failed")
		return
	}
	if count > 0 {
		fail(c, http.StatusConflict, "platform is already initialized")
		return
	}
	var user *identity.User
	var org *identity.Organization
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rerr error
		user, org, rerr = identity.Register(tx, identity.RegisterInput{
			Email:         req.Email,
			Password:      req.Password,
			DisplayName:   cmp.Or(strings.TrimSpace(req.DisplayName), "Administrator"),
			EmailVerified: true,
		})
		if rerr != nil {
			return rerr
		}
		return tx.Model(&identity.User{}).Where("id = ?", user.ID).Update("role", identity.PlatRoleSuperadmin).Error
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	user.Role = identity.PlatRoleSuperadmin
	token, claims, err := identity.IssueSessionToken(s.secret, user.ID, org.ID, sessionTTL, s.now())
	if err != nil {
		fail(c, http.StatusInternalServerError, "token issue failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"token":      token,
		"expires_at": claims.Exp,
		"org_id":     org.ID,
		"user":       s.buildV1UserDTO(user, org.ID),
	})
}

func (s *Server) v1Login(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "email and password are required")
		return
	}
	identifier := cmp.Or(strings.TrimSpace(req.Email), strings.TrimSpace(req.Username))
	if identifier == "" {
		fail(c, http.StatusBadRequest, "email and password are required")
		return
	}
	ctx := c.Request.Context()
	user, err := identity.FindUserByEmail(s.db.WithContext(ctx), identifier)
	if err != nil {
		identity.VerifyDummyPassword(req.Password)
		fail(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if vErr := identity.VerifyPassword(req.Password, user.PasswordHash); vErr != nil || user.Status != identity.StatusActive {
		fail(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if user.TOTPEnabled != 0 {
		c.JSON(http.StatusOK, gin.H{
			"requires_2fa": true,
			"token":        s.make2FAToken(user.ID),
		})
		return
	}
	m := &identity.OrgMember{}
	if err := s.db.WithContext(ctx).Where("user_id = ? AND role = ?", user.ID, identity.RoleOwner).
		Order("id").First(m).Error; err != nil {
		fail(c, http.StatusInternalServerError, "no organization")
		return
	}
	token, claims, err := identity.IssueSessionToken(s.secret, user.ID, m.OrgID, sessionTTL, s.now())
	if err != nil {
		fail(c, http.StatusInternalServerError, "token issue failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token":      token,
		"expires_at": claims.Exp,
		"org_id":     m.OrgID,
		"user":       s.buildV1UserDTO(user, m.OrgID),
	})
}

func (s *Server) v1TwofaLogin(c *gin.Context) {
	// Bridge to the exact 2FA login verification flow by capturing twofaVerifyLogin output if needed,
	// or verifying directly with the same claims/user helper.
	s.twofaVerifyLogin(c)
}

func (s *Server) v1SendCode(c *gin.Context) {
	s.sendVerification(c)
}

func (s *Server) v1PublicSettings(c *gin.Context) {
	_, cny := s.effectiveCNYPerUSD()
	currencyEnabled := s.getBoolSetting("currency_display_enabled")
	currencySymbol := "$"
	if currencyEnabled {
		currencySymbol = "¥"
	}
	c.JSON(http.StatusOK, gin.H{
		"site": gin.H{
			"name":                cmp.Or(s.getSetting("site_name"), "LAIAI"),
			"title":               cmp.Or(s.getSetting("site_name"), "LAIAI AI Gateway"),
			"admin_path":          cmp.Or(s.getSetting("admin_path"), "admin"),
			"enable_multilingual": true,
			"enable_theme_toggle": true,
			"default_theme":       "light",
			"default_language":    "zh",
			"supported_languages": []string{"zh", "en", "zh-TW"},
			"default_timezone":    "Asia/Shanghai",
			"footer_text":         s.getSetting("footer_text"),
			"support_email":       s.getSetting("support_email"),
			"maintenance":         s.getBoolSetting("maintenance_mode"),
		},
		"login": gin.H{
			"enable_username_login": true,
			"enable_email_login":    true,
			"enable_google_login":   s.getSetting("google_client_id") != "",
			"enable_github_login":   s.getSetting("github_client_id") != "",
		},
		"registration": gin.H{
			"enable_registration":        s.getBoolSetting("register_enabled"),
			"enable_email_registration":  s.getBoolSetting("register_enabled"),
			"email_verification_enabled": s.getBoolSetting("email_verification_enabled"),
			"require_stepup_for_keys":    s.getBoolSetting("require_stepup_for_keys"),
		},
		"currency": gin.H{
			"currency_display_enabled": currencyEnabled,
			"cny_per_usd":              cny,
			"currency_symbol":          currencySymbol,
		},
		"agreement": gin.H{
			"tos_mode":     "page",
			"privacy_mode": "page",
		},
		"notification": gin.H{
			"site_notification_enabled": true,
			"low_balance_threshold":     10,
		},
	})
}

func (s *Server) v1ActivePlugins(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"active_plugins": []gin.H{
			{
				"name":           "model_marketplace",
				"is_enabled":     true,
				"allowed_levels": "all",
				"mp_allow_guest": true,
			},
			{
				"name":           "docs_api",
				"is_enabled":     true,
				"allowed_levels": "all",
				"mp_allow_guest": true,
			},
		},
	})
}

type v1Announcement struct {
	ID              uint64 `json:"id"`
	Title           string `json:"title"`
	Content         string `json:"content"`
	IsPinned        int8   `json:"is_pinned"`
	IsActive        int8   `json:"is_active"`
	IsPopup         int8   `json:"is_popup"`
	SortOrder       int    `json:"sort_order"`
	DisplayTimeMode string `json:"display_time_mode,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func (s *Server) loadAnnouncements(ctx context.Context) []v1Announcement {
	raw := strings.TrimSpace(s.getSetting("announcements_json"))
	items := []v1Announcement{}
	if raw != "" && raw != "[]" {
		_ = json.Unmarshal([]byte(raw), &items)
	}
	if len(items) == 0 {
		if single := strings.TrimSpace(s.getSetting("announcement")); single != "" {
			now := s.now().UTC().Format(time.RFC3339)
			items = append(items, v1Announcement{
				ID:        1,
				Title:     "平台公告",
				Content:   single,
				IsPinned:  1,
				IsActive:  1,
				CreatedAt: now,
				UpdatedAt: now,
			})
		}
	}
	return items
}

func (s *Server) saveAnnouncements(ctx context.Context, items []v1Announcement) error {
	b, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Save(&Setting{
		Name:  "announcements_json",
		Value: string(b),
	}).Error
}

func (s *Server) v1PublicAnnouncements(c *gin.Context) {
	all := s.loadAnnouncements(c.Request.Context())
	active := make([]v1Announcement, 0, len(all))
	for _, a := range all {
		if a.IsActive == 1 {
			active = append(active, a)
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": active})
}

func (s *Server) v1ConsoleBootstrap(c *gin.Context) {
	claims := mustClaims(c)
	ctx := c.Request.Context()
	user := &identity.User{}
	if err := s.db.WithContext(ctx).First(user, claims.UserID).Error; err != nil {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	all := s.loadAnnouncements(ctx)
	active := make([]v1Announcement, 0, len(all))
	for _, a := range all {
		if a.IsActive == 1 {
			active = append(active, a)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"user":          s.buildV1UserDTO(user, claims.OrgID),
		"announcements": active,
		"active_plugins": []gin.H{
			{"name": "model_marketplace", "is_enabled": true, "allowed_levels": "all", "mp_allow_guest": true},
			{"name": "docs_api", "is_enabled": true, "allowed_levels": "all", "mp_allow_guest": true},
		},
	})
}

func (s *Server) v1GetProfile(c *gin.Context) {
	claims := mustClaims(c)
	user := &identity.User{}
	if err := s.db.WithContext(c.Request.Context()).First(user, claims.UserID).Error; err != nil {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	c.JSON(http.StatusOK, s.buildV1UserDTO(user, claims.OrgID))
}

func (s *Server) v1UpdateProfile(c *gin.Context) {
	claims := mustClaims(c)
	var req struct {
		DisplayName     *string `json:"display_name"`
		Username        *string `json:"username"`
		DisplayCurrency *string `json:"display_currency"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]any{}
	if req.DisplayName != nil {
		updates["display_name"] = strings.TrimSpace(*req.DisplayName)
	} else if req.Username != nil {
		updates["display_name"] = strings.TrimSpace(*req.Username)
	}
	if req.DisplayCurrency != nil {
		updates["display_currency"] = normalizeCurrency(*req.DisplayCurrency)
	}
	if len(updates) > 0 {
		if err := s.db.WithContext(c.Request.Context()).Model(&identity.User{}).Where("id = ?", claims.UserID).Updates(updates).Error; err != nil {
			fail(c, http.StatusInternalServerError, "update profile failed")
			return
		}
	}
	s.v1GetProfile(c)
}

func (s *Server) v1MarketplacePublic(c *gin.Context) {
	res, err := v1compat.BuildPublicMarketplace(c.Request.Context(), s.readOnly())
	if err != nil {
		fail(c, http.StatusInternalServerError, "load marketplace failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, res)
}

func (s *Server) v1DocsPublicTree(c *gin.Context) {
	tree, err := v1compat.ListDocTree(c.Request.Context(), s.db, c.Query("lang"), true)
	if err != nil {
		fail(c, http.StatusInternalServerError, "load doc tree failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"tree": tree})
}

func (s *Server) v1DocsPublicDetail(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	doc, err := v1compat.GetDocDetail(c.Request.Context(), s.db, id, c.Query("lang"), false, true)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "document not found")
			return
		}
		fail(c, http.StatusInternalServerError, "load document failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"doc": doc})
}

func (s *Server) v1DashboardStats(c *gin.Context) {
	ctx := c.Request.Context()
	claims := mustClaims(c)
	rdb := s.readOnly().WithContext(ctx)

	isAdminScope := strings.EqualFold(c.Query("scope"), "platform")
	if isAdminScope {
		if _, role, _, ok := s.resolveAdmin(c); !ok || identity.RoleRank(role) < identity.RoleRank(identity.PlatRoleAdmin) {
			fail(c, http.StatusForbidden, "admin access required")
			return
		}
	}

	days := 14
	if d, err := strconv.Atoi(c.Query("days")); err == nil && d >= 1 && d <= 90 {
		days = d
	}
	now := s.now().UTC()
	sinceTime := now.AddDate(0, 0, -days+1)
	sinceDate := sinceTime.Format("2006-01-02")

	var posted, held decimal.Decimal
	if isAdminScope {
		_ = rdb.Model(&ledger.Account{}).Where("type = ?", ledger.AccountUserCredit).
			Select("COALESCE(SUM(posted_balance), 0)").Scan(&posted).Error
		_ = rdb.Model(&ledger.Account{}).Where("type = ?", ledger.AccountUserCredit).
			Select("COALESCE(SUM(held_balance), 0)").Scan(&held).Error
	} else {
		posted, held, _ = ledger.OrgBalances(s.db.WithContext(ctx), claims.OrgID, ledger.AccountUserCredit)
	}

	iq := rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", sinceDate)
	if !isAdminScope {
		iq = iq.Where("org_id = ?", claims.OrgID)
	}
	var invRows []invocation.Invocation
	if err := iq.Select("model, status, prompt_tokens, completion_tokens, cost_usd, frt_ms, duration_ms, created_at").Find(&invRows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "dashboard stats failed")
		return
	}

	totalCost := decimal.Zero
	var totalReq, successReq, totalTokens int64
	dailyCost := make(map[string]decimal.Decimal, days)
	dailyReq := make(map[string]int64, days)
	modelCost := make(map[string]decimal.Decimal)
	modelReq := make(map[string]int64)

	for _, row := range invRows {
		totalReq++
		if row.Status == invocation.StatusSuccess {
			successReq++
		}
		totalTokens += row.PromptTokens + row.CompletionTokens
		totalCost = totalCost.Add(row.CostUSD)
		day := row.CreatedAt.UTC().Format("2006-01-02")
		dailyCost[day] = dailyCost[day].Add(row.CostUSD)
		dailyReq[day]++
		modelCost[row.Model] = modelCost[row.Model].Add(row.CostUSD)
		modelReq[row.Model]++
	}

	trend := make([]gin.H, 0, days)
	for i := range days {
		d := sinceTime.AddDate(0, 0, i).Format("2006-01-02")
		trend = append(trend, gin.H{
			"date":     d,
			"cost_usd": dailyCost[d],
			"requests": dailyReq[d],
		})
	}

	type modelStat struct {
		Model    string          `json:"model"`
		Requests int64           `json:"requests"`
		CostUSD  decimal.Decimal `json:"cost_usd"`
	}
	dist := make([]modelStat, 0, len(modelReq))
	for m, cnt := range modelReq {
		dist = append(dist, modelStat{Model: m, Requests: cnt, CostUSD: modelCost[m]})
	}
	slices.SortFunc(dist, func(a, b modelStat) int {
		if c := b.CostUSD.Cmp(a.CostUSD); c != 0 {
			return c
		}
		return cmp.Compare(b.Requests, a.Requests)
	})
	if len(dist) > 10 {
		dist = dist[:10]
	}

	resp := gin.H{
		"scope":              map[bool]string{true: "platform", false: "org"}[isAdminScope],
		"balance_usd":        posted,
		"held_usd":           held,
		"total_requests":     totalReq,
		"success_requests":   successReq,
		"total_tokens":       totalTokens,
		"total_cost_usd":     totalCost,
		"daily_trend":        trend,
		"model_distribution": dist,
	}
	if isAdminScope {
		var userCount, orgCount, chCount int64
		_ = rdb.Table("users").Count(&userCount).Error
		_ = rdb.Table("organizations").Count(&orgCount).Error
		_ = rdb.Table("channels").Where("status = 1").Count(&chCount).Error
		resp["user_count"] = userCount
		resp["org_count"] = orgCount
		resp["active_channel_count"] = chCount
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) formatV1Token(k identity.APIKey) gin.H {
	models := []string{}
	if strings.TrimSpace(k.ModelLimits) != "" {
		for item := range strings.SplitSeq(k.ModelLimits, ",") {
			if m := strings.TrimSpace(item); m != "" {
				models = append(models, m)
			}
		}
	}
	quotaLimit := -1.0
	if k.QuotaLimit.IsPositive() {
		quotaLimit, _ = k.QuotaLimit.Float64()
	}
	return gin.H{
		"id":             k.ID,
		"name":           k.Name,
		"key_prefix":     k.Prefix,
		"masked_key":     k.Prefix + "••••••••••••••••",
		"is_active":      k.Status,
		"status":         k.Status,
		"scopes":         k.Scopes,
		"quota_limit":    k.QuotaLimit,
		"quota_limit_f":  quotaLimit,
		"used_amount":    k.UsedAmount,
		"allowed_models": models,
		"allowed_ips":    k.AllowIPs,
		"rpm_limit":      k.RPMLimit,
		"route_policy":   k.RoutePolicy,
		"expires_at":     k.ExpiresAt,
	}
}

func (s *Server) v1ListTokens(c *gin.Context) {
	claims := mustClaims(c)
	var keys []identity.APIKey
	if err := s.db.WithContext(c.Request.Context()).Where("org_id = ?", claims.OrgID).Order("id DESC").Find(&keys).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list tokens failed")
		return
	}
	out := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.formatV1Token(k))
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (s *Server) v1CreateToken(c *gin.Context) {
	claims := mustClaims(c)
	var req struct {
		Name             string          `json:"name"`
		Scopes           string          `json:"scopes"`
		ExpiresInDays    int             `json:"expires_in_days"`
		QuotaLimit       decimal.Decimal `json:"quota_limit"`
		AllowedModels    []string        `json:"allowed_models"`
		ModelLimits      []string        `json:"model_limits"`
		AllowedIPs       string          `json:"allowed_ips"`
		AllowIPs         string          `json:"allow_ips"`
		RPMLimit         int             `json:"rpm_limit"`
		RoutePolicy      string          `json:"route_policy"`
		EnableVideoAsync bool            `json:"enable_video_async"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.QuotaLimit.IsNegative() {
		req.QuotaLimit = decimal.Zero
	}
	if req.RPMLimit < 0 {
		fail(c, http.StatusBadRequest, "rpm_limit must be non-negative")
		return
	}
	if !relay.ValidRoutePolicy(req.RoutePolicy) {
		fail(c, http.StatusBadRequest, "route_policy must be one of: order, latency, throughput, price")
		return
	}
	models := req.ModelLimits
	if len(models) == 0 {
		models = req.AllowedModels
	}
	allowIPs := cmp.Or(strings.TrimSpace(req.AllowIPs), strings.TrimSpace(req.AllowedIPs))
	var expires *time.Time
	if req.ExpiresInDays > 0 {
		t := s.now().AddDate(0, 0, req.ExpiresInDays)
		expires = &t
	}
	requestID := rand.Text()
	var plaintext string
	var issued *identity.APIKey
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var org identity.Organization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", claims.OrgID).Take(&org).Error; err != nil {
			return err
		}
		if err := identity.VerifyActiveMembership(tx, claims.UserID, claims.OrgID); err != nil {
			return err
		}
		effectiveScopes := req.Scopes
		effectiveModels := joinTrim(models)
		group, err := routinggroup.DefaultKeyGroupTx(tx, claims.OrgID)
		if err != nil {
			return err
		}
		var mode *string
		if group != nil {
			if effectiveModels != "" {
				return identity.ErrModelAccessLimits
			}
			inherit := identity.ModelAccessInheritGroup
			mode = &inherit
			effectiveScopes = "video:read video:write"
		}
		ent, err := identity.LoadEntitlements(tx, claims.OrgID)
		if err != nil {
			return err
		}
		if videoWriteScope(effectiveScopes) {
			consent := req.EnableVideoAsync && group == nil
			if err := s.ensureKeyVideoAccess(tx, claims.OrgID, claims.UserID, consent, ent); err != nil {
				return err
			}
		}
		uid := claims.UserID
		plaintext, issued, err = identity.IssueKey(tx, identity.IssueKeyInput{
			OrgID: claims.OrgID, UserID: &uid,
			Name: req.Name, Scopes: effectiveScopes, ExpiresAt: expires,
			QuotaLimit: req.QuotaLimit, ModelLimits: effectiveModels, AllowIPs: allowIPs,
			RPMLimit: req.RPMLimit, RoutePolicy: req.RoutePolicy, MaxKeys: ent.MaxAPIKeys,
			ModelAccessMode: mode, RoutingGroupID: group,
			PolicyAudit: routinggroup.Audit{ActorUserID: claims.UserID, RequestID: requestID},
		})
		return err
	})
	if replyKeyVideoError(c, err) {
		return
	}
	if errors.Is(err, identity.ErrKeyLimit) {
		fail(c, http.StatusForbidden, "api key limit reached for this organization")
		return
	}
	if errors.Is(err, identity.ErrModelAccessLimits) {
		fail(c, http.StatusBadRequest, "model whitelist is inherited from routing group")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "create token failed")
		return
	}
	out := s.formatV1Token(*issued)
	out["key"] = plaintext // 红线 7：明文仅创建时返回一次
	c.JSON(http.StatusCreated, out)
}

func (s *Server) v1UpdateToken(c *gin.Context) {
	claims := mustClaims(c)
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Name          *string          `json:"name"`
		IsActive      *int8            `json:"is_active"`
		QuotaLimit    *decimal.Decimal `json:"quota_limit"`
		AllowedModels *[]string        `json:"allowed_models"`
		AllowIPs      *string          `json:"allow_ips"`
		AllowedIPs    *string          `json:"allowed_ips"`
		RPMLimit      *int             `json:"rpm_limit"`
		RoutePolicy   *string          `json:"route_policy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	var k identity.APIKey
	if err := s.db.WithContext(ctx).Where("id = ? AND org_id = ?", id, claims.OrgID).First(&k).Error; err != nil {
		fail(c, http.StatusNotFound, "token not found")
		return
	}
	updates := map[string]any{}
	if req.Name != nil {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.IsActive != nil {
		if *req.IsActive == 0 {
			updates["status"] = 0
		} else {
			updates["status"] = 1
		}
	}
	if req.QuotaLimit != nil {
		q := *req.QuotaLimit
		if q.IsNegative() {
			q = decimal.Zero
		}
		updates["quota_limit"] = q
	}
	if req.AllowedModels != nil {
		updates["model_limits"] = joinTrim(*req.AllowedModels)
	}
	if req.AllowIPs != nil {
		updates["allow_ips"] = strings.TrimSpace(*req.AllowIPs)
	} else if req.AllowedIPs != nil {
		updates["allow_ips"] = strings.TrimSpace(*req.AllowedIPs)
	}
	if req.RPMLimit != nil && *req.RPMLimit >= 0 {
		updates["rpm_limit"] = *req.RPMLimit
	}
	if req.RoutePolicy != nil {
		if !relay.ValidRoutePolicy(*req.RoutePolicy) {
			fail(c, http.StatusBadRequest, "route_policy must be one of: order, latency, throughput, price")
			return
		}
		updates["route_policy"] = *req.RoutePolicy
	}
	if len(updates) > 0 {
		if err := s.db.WithContext(ctx).Model(&identity.APIKey{}).Where("id = ? AND org_id = ?", id, claims.OrgID).Updates(updates).Error; err != nil {
			fail(c, http.StatusInternalServerError, "update token failed")
			return
		}
	}
	_ = s.db.WithContext(ctx).Where("id = ? AND org_id = ?", id, claims.OrgID).First(&k).Error
	c.JSON(http.StatusOK, s.formatV1Token(k))
}

func (s *Server) v1DeleteToken(c *gin.Context) {
	claims := mustClaims(c)
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var k identity.APIKey
	if err := s.db.WithContext(ctx).Where("id = ? AND org_id = ?", id, claims.OrgID).First(&k).Error; err != nil {
		fail(c, http.StatusNotFound, "token not found")
		return
	}
	if k.Status != 0 {
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return identity.RevokeKeyWithAudit(tx, k.ID, claims.OrgID, routinggroup.Audit{ActorUserID: claims.UserID, RequestID: rand.Text()})
		}); err != nil {
			fail(c, http.StatusInternalServerError, "revoke token failed")
			return
		}
		c.JSON(http.StatusOK, gin.H{"revoked": k.ID})
		return
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return identity.PurgeRevokedKeyTx(tx, claims.OrgID, k.ID, videobilling.HasKeySnapshotReferenceTx)
	})
	if errors.Is(err, identity.ErrKeyPurgeReferenced) {
		fail(c, http.StatusConflict, "key is retained for video billing history; revocation remains effective")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete token failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": k.ID})
}

// v1ForbidTokenReveal 严格执行平台红线 7：API Key 仅存哈希，绝不允许二次明文查看。
func (s *Server) v1ForbidTokenReveal(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error": "plaintext API keys are shown only once at creation time and cannot be revealed again",
		"code":  "token_reveal_forbidden",
	})
}

func (s *Server) v1ListLogs(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := false
	targetOrgID := claims.OrgID
	if c.Query("admin") == "1" {
		if _, role, _, ok := s.resolveAdmin(c); ok && identity.RoleRank(role) >= identity.RoleRank(identity.PlatRoleAdmin) {
			isAdmin = true
			targetOrgID = 0
			if raw := strings.TrimSpace(c.Query("org_id")); raw != "" {
				if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
					targetOrgID = parsed
				}
			}
		} else {
			fail(c, http.StatusForbidden, "admin access required")
			return
		}
	}

	page, _ := strconv.Atoi(cmp.Or(c.Query("page"), "1"))
	page = max(1, page)
	pageSize, _ := strconv.Atoi(cmp.Or(c.Query("per_page"), c.Query("page_size"), "20"))
	if pageSize < 1 {
		pageSize = 20
	}
	pageSize = min(100, pageSize)

	rdb := s.readOnly().WithContext(c.Request.Context())
	q := rdb.Model(&invocation.Invocation{})
	if targetOrgID > 0 {
		q = q.Where("org_id = ?", targetOrgID)
	}
	if m := strings.TrimSpace(c.Query("model")); m != "" {
		q = q.Where("model = ?", m)
	}
	if st := strings.TrimSpace(c.Query("status")); st != "" {
		if st == "failed" {
			q = q.Where("status <> ?", invocation.StatusSuccess)
		} else {
			q = q.Where("status = ?", st)
		}
	}
	if mod := strings.TrimSpace(c.Query("modality")); mod != "" {
		q = q.Where("modality = ?", mod)
	}
	if kw := strings.TrimSpace(cmp.Or(c.Query("q"), c.Query("search_keyword"))); kw != "" {
		pat := "%" + escapeSQLLike(kw) + "%"
		q = q.Where(`request_id LIKE ? ESCAPE '\' OR upstream_request_id LIKE ? ESCAPE '\' OR model LIKE ? ESCAPE '\'`, pat, pat, pat)
	}

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

	out := make([]gin.H, 0, len(rows))
	for _, inv := range rows {
		entry := gin.H{
			"id":                inv.ID,
			"request_id":        inv.RequestID,
			"api_key_id":        inv.APIKeyID,
			"model":             inv.Model,
			"modality":          inv.Modality,
			"protocol":          inv.Protocol,
			"status":            inv.Status,
			"status_code":       inv.StatusCode,
			"is_stream":         inv.IsStream,
			"prompt_tokens":     inv.PromptTokens,
			"completion_tokens": inv.CompletionTokens,
			"cached_tokens":     inv.CachedTokens,
			"reasoning_tokens":  inv.ReasoningTokens,
			"cost_usd":          inv.CostUSD,
			"usage_source":      inv.UsageSource,
			"frt_ms":            inv.FRTMs,
			"duration_ms":       inv.DurationMs,
			"finish_reason":     inv.FinishReason,
			"error_brief":       inv.ErrorBrief,
			"forward_rule_eid":  inv.ForwardRuleEID,
			"billing_rule_pid":  inv.BillingRulePID,
			"created_at":        inv.CreatedAt,
		}
		if isAdmin {
			entry["org_id"] = inv.OrgID
			entry["user_id"] = inv.UserID
			entry["channel_id"] = inv.ChannelID
			entry["provider"] = inv.Provider
			entry["upstream_cost_ratio"] = inv.UpstreamCostRatio
			entry["upstream_request_id"] = inv.UpstreamRequestID
		}
		out = append(out, entry)
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": total, "page": page, "per_page": pageSize})
}

func (s *Server) v1LogDetail(c *gin.Context) {
	claims := mustClaims(c)
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	isAdmin := false
	if _, role, _, okAdmin := s.resolveAdmin(c); okAdmin && identity.RoleRank(role) >= identity.RoleRank(identity.PlatRoleAdmin) {
		isAdmin = true
	}
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
	out := gin.H{
		"id":                  inv.ID,
		"request_id":          inv.RequestID,
		"upstream_request_id": inv.UpstreamRequestID,
		"model":               inv.Model,
		"modality":            inv.Modality,
		"protocol":            inv.Protocol,
		"status":              inv.Status,
		"status_code":         inv.StatusCode,
		"is_stream":           inv.IsStream,
		"prompt_tokens":       inv.PromptTokens,
		"completion_tokens":   inv.CompletionTokens,
		"cached_tokens":       inv.CachedTokens,
		"reasoning_tokens":    inv.ReasoningTokens,
		"cost_usd":            inv.CostUSD,
		"usage_source":        inv.UsageSource,
		"frt_ms":              inv.FRTMs,
		"duration_ms":         inv.DurationMs,
		"finish_reason":       inv.FinishReason,
		"upstream_error_code": inv.UpstreamErrorCode,
		"error_brief":         inv.ErrorBrief,
		"client_ip":           inv.ClientIP,
		"user_agent":          inv.UserAgent,
		"forward_rule_eid":    inv.ForwardRuleEID,
		"billing_rule_pid":    inv.BillingRulePID,
		"created_at":          inv.CreatedAt,
	}
	if isAdmin {
		out["org_id"] = inv.OrgID
		out["channel_id"] = inv.ChannelID
		out["provider"] = inv.Provider
		out["upstream_cost_ratio"] = inv.UpstreamCostRatio
	}
	c.JSON(http.StatusOK, out)
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
	entry := gin.H{
		"id":               t.ID,
		"task_id":          t.TaskID,
		"api_key_id":       t.APIKeyID,
		"model":            t.Model,
		"action_type":      "视频",
		"status":           t.Status,
		"fail_reason":      t.FailReason,
		"result_url":       t.ResultURL,
		"preview_urls":     previews,
		"est_tokens":       t.EstTokens,
		"actual_tokens":    t.ActualTokens,
		"hold_amount_usd":  t.HoldAmountUSD,
		"cost_usd":         t.CostUSD,
		"forward_rule_eid": t.ForwardRuleEID,
		"billing_rule_pid": t.BillingRulePID,
		"billing_detail":   t.BillingDetail,
		"features_json":    t.FeaturesJSON,
		"time_multiplier":  t.TimeMultiplier,
		"duration_ms":      durMs,
		"submitted_at":     t.SubmittedAt,
		"started_at":       t.StartedAt,
		"finished_at":      t.FinishedAt,
		"created_at":       t.SubmittedAt,
	}
	if isAdmin {
		entry["org_id"] = t.OrgID
		entry["user_id"] = t.UserID
		entry["channel_id"] = t.ChannelID
		entry["provider"] = t.Provider
		entry["upstream_task_id"] = t.UpstreamTaskID
	}
	return entry
}

func (s *Server) v1ListTaskLogs(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := false
	targetOrgID := claims.OrgID
	if c.Query("admin") == "1" {
		if _, role, _, ok := s.resolveAdmin(c); ok && identity.RoleRank(role) >= identity.RoleRank(identity.PlatRoleAdmin) {
			isAdmin = true
			targetOrgID = 0
			if raw := strings.TrimSpace(c.Query("org_id")); raw != "" {
				if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
					targetOrgID = parsed
				}
			}
		} else {
			fail(c, http.StatusForbidden, "admin access required")
			return
		}
	}
	page, _ := strconv.Atoi(cmp.Or(c.Query("page"), "1"))
	page = max(1, page)
	pageSize, _ := strconv.Atoi(cmp.Or(c.Query("per_page"), c.Query("page_size"), "20"))
	if pageSize < 1 {
		pageSize = 20
	}
	pageSize = min(100, pageSize)

	rdb := s.readOnly().WithContext(c.Request.Context())
	q := rdb.Model(&task.Task{})
	if targetOrgID > 0 {
		q = q.Where("org_id = ?", targetOrgID)
	}
	if m := strings.TrimSpace(c.Query("model")); m != "" {
		q = q.Where("model = ?", m)
	}
	if st := strings.TrimSpace(c.Query("status")); st != "" {
		q = q.Where("status = ?", st)
	}
	if kw := strings.TrimSpace(cmp.Or(c.Query("q"), c.Query("search_keyword"))); kw != "" {
		pat := "%" + escapeSQLLike(kw) + "%"
		q = q.Where(`task_id LIKE ? ESCAPE '\' OR upstream_task_id LIKE ? ESCAPE '\' OR model LIKE ? ESCAPE '\'`, pat, pat, pat)
	}

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
	c.JSON(http.StatusOK, gin.H{"data": out, "total": total, "page": page, "per_page": pageSize})
}

func (s *Server) v1TaskLogDetail(c *gin.Context) {
	claims := mustClaims(c)
	isAdmin := false
	if _, role, _, ok := s.resolveAdmin(c); ok && identity.RoleRank(role) >= identity.RoleRank(identity.PlatRoleAdmin) {
		isAdmin = true
	}
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
	isAdmin := false
	if _, role, _, ok := s.resolveAdmin(c); ok && identity.RoleRank(role) >= identity.RoleRank(identity.PlatRoleAdmin) {
		isAdmin = true
	}
	t, err := s.taskService.SyncTask(c.Request.Context(), claims.OrgID, c.Param("id"), isAdmin)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "sync task failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "task synced",
		"task":    s.formatV1TaskRow(*t, isAdmin),
	})
}

func (s *Server) v1CancelTaskLog(c *gin.Context) {
	if s.taskService == nil {
		fail(c, http.StatusServiceUnavailable, "task service unavailable")
		return
	}
	claims := mustClaims(c)
	isAdmin := false
	if _, role, _, ok := s.resolveAdmin(c); ok && identity.RoleRank(role) >= identity.RoleRank(identity.PlatRoleAdmin) {
		isAdmin = true
	}
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
	c.JSON(http.StatusOK, gin.H{
		"message": "task cancelled and hold refunded",
		"task":    s.formatV1TaskRow(*t, isAdmin),
	})
}

func (s *Server) v1UserWallet(c *gin.Context) {
	claims := mustClaims(c)
	ctx := c.Request.Context()
	posted, held, err := ledger.OrgBalances(s.db.WithContext(ctx), claims.OrgID, ledger.AccountUserCredit)
	if err != nil {
		fail(c, http.StatusInternalServerError, "wallet query failed")
		return
	}
	var totalRecharged decimal.Decimal
	_ = s.readOnly().WithContext(ctx).Model(&TopupOrder{}).
		Where("org_id = ? AND status = ?", claims.OrgID, topupPaid).
		Select("COALESCE(SUM(amount_usd), 0)").Scan(&totalRecharged).Error
	var totalConsumed decimal.Decimal
	_ = s.readOnly().WithContext(ctx).Model(&invocation.Invocation{}).
		Where("org_id = ?", claims.OrgID).
		Select("COALESCE(SUM(cost_usd), 0)").Scan(&totalConsumed).Error

	c.JSON(http.StatusOK, gin.H{
		"balance":         posted,
		"held":            held,
		"total_recharged": totalRecharged,
		"total_consumed":  totalConsumed,
	})
}

func (s *Server) v1UserRechargeRecords(c *gin.Context) {
	claims := mustClaims(c)
	var rows []TopupOrder
	if err := s.readOnly().WithContext(c.Request.Context()).Where("org_id = ?", claims.OrgID).Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query recharge records failed")
		return
	}
	if rows == nil {
		rows = []TopupOrder{}
	}
	c.JSON(http.StatusOK, rows)
}
