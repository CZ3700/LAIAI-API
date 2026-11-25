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
	v1.GET("/auth/admin/init-status", s.v1InitStatus)
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
			org.GET("/dashboard", s.v1TkeDashboard)
			org.GET("/dashboard/stats", s.v1DashboardStats)
			org.GET("/dashboard/models_30d", s.v1Models30d)
			org.GET("/metrics/live", s.v1LiveMetrics)

			org.GET("/tokens", s.v1ListTokens)
			org.POST("/tokens", s.requireStepUpForKeys(), s.v1CreateToken)
			org.PUT("/tokens/:id", s.v1UpdateToken)
			org.DELETE("/tokens/:id", s.v1DeleteToken)
			org.POST("/tokens/:id/reveal", s.v1ForbidTokenReveal)

			org.GET("/logs", s.v1ListLogs)
			org.GET("/logs/export", s.v1ExportLogs)
			org.GET("/logs/:id/detail", s.v1LogDetail)
			org.GET("/task_logs", s.v1ListTaskLogs)
			org.GET("/task_logs/export", s.v1ExportTaskLogs)
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
		admin.POST("/model-providers", s.v1SaveModelProvider)
		admin.PUT("/model-providers/:id", s.v1SaveModelProvider)
		admin.DELETE("/model-providers/:id", s.v1DeleteModelProvider)

		admin.GET("/model-api-providers", s.v1ModelAPIProviders)
		admin.POST("/model-api-providers", s.v1SaveModelAPIProvider)
		admin.PUT("/model-api-providers/:id", s.v1SaveModelAPIProvider)
		admin.DELETE("/model-api-providers/:id", s.v1DeleteModelAPIProvider)

		admin.GET("/model-types", s.v1ModelTypes)
		admin.POST("/model-types", s.v1SaveModelType)
		admin.PUT("/model-types/:id", s.v1SaveModelType)
		admin.DELETE("/model-types/:id", s.v1DeleteModelType)

		admin.GET("/classifications/stats", s.v1ClassificationsStats)

		admin.GET("/channel-categories", s.v1ListChannelCategories)
		admin.POST("/channel-categories", s.adminCreateChannelCategory)
		admin.PUT("/channel-categories/:id", s.adminUpdateChannelCategory)
		admin.DELETE("/channel-categories/:id", s.adminDeleteChannelCategory)

		admin.GET("/channels", s.v1ListChannels)
		admin.POST("/channels", s.v1CreateChannel)
		admin.PUT("/channels/:id", s.v1UpdateChannel)
		admin.DELETE("/channels/:id", s.v1DeleteChannel)
		admin.POST("/channels/:id/test", s.adminTestChannel)
		admin.GET("/channels/:id/meltdown", s.v1ChannelMeltdownStatus)
		admin.POST("/channels/:id/meltdown/reset", s.v1ChannelMeltdownReset)
		admin.POST("/channels/:id/quota/reset", s.v1ChannelQuotaReset)

		admin.GET("/channel-configs", s.v1ListChannelConfigs)
		admin.POST("/channel-configs", s.v1CreateChannelConfig)
		admin.PUT("/channel-configs/:id", s.v1UpdateChannelConfig)
		admin.DELETE("/channel-configs/:id", s.v1DeleteChannelConfig)
		admin.POST("/channel-configs/:id/copy", s.v1CopyChannelConfig)
		admin.GET("/channel-configs/:id/sync-logs", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"data": []any{}}) })
		admin.POST("/channel-configs/:id/sync-rate", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"synced": true}) })
		admin.POST("/channel-configs/upstream-groups", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"groups": []any{}}) })
		admin.GET("/channel-configs/providers", s.v1ChannelConfigProviders)
		admin.POST("/channel-configs/providers", s.v1SaveChannelConfigProvider)
		admin.PUT("/channel-configs/providers/:id", s.v1SaveChannelConfigProvider)
		admin.DELETE("/channel-configs/providers/:id", s.v1SaveChannelConfigProvider)
		admin.GET("/upstreams", s.v1ListUpstreams)
		admin.GET("/user_levels", s.v1ListUserLevels)
		admin.PUT("/user_levels/:id", s.v1UpdateUserLevel)

		admin.GET("/billing-rules", s.v1ListBillingRules)
		admin.POST("/billing-rules", s.adminCreateBillingRule)
		admin.PUT("/billing-rules/:id", s.adminUpdateBillingRule)
		admin.DELETE("/billing-rules/:id", s.adminDeleteBillingRule)
		admin.POST("/billing-rules/:id/restore-default", s.v1RestoreDefaultBillingRule)

		admin.GET("/forward-rules", s.v1ListForwardRules)
		admin.POST("/forward-rules", s.adminCreateForwardRule)
		admin.PUT("/forward-rules/:id", s.adminUpdateForwardRule)
		admin.DELETE("/forward-rules/:id", s.adminDeleteForwardRule)

		admin.GET("/models", s.v1AdminListModels)
		admin.POST("/models", s.v1AdminUpsertModel)
		admin.PUT("/models/:id", s.v1AdminUpsertModel)
		admin.PUT("/models/library/:id", s.v1AdminUpsertModel)
		admin.DELETE("/models/:id", s.v1AdminDeleteModel)
		admin.POST("/models/:id/relist", func(c *gin.Context) { s.v1ModelSetListed(c, 1) })
		admin.POST("/models/:id/unlist", func(c *gin.Context) { s.v1ModelSetListed(c, 0) })
		admin.POST("/models/library/:id/publish", func(c *gin.Context) { s.v1ModelSetListed(c, 1) })
		admin.POST("/models/:id/copy", s.v1ModelCopy)

		admin.GET("/finance/recharge_types", s.v1FinanceRechargeTypes)
		admin.GET("/finance/recharges", s.v1FinanceRecharges)
		admin.GET("/finance/orders", s.v1FinanceOrders)
		admin.POST("/finance/orders/:out_trade_no/query-remote", s.v1FinanceOrderAction)
		admin.POST("/finance/orders/:out_trade_no/confirm-credit", s.requireSuperadmin(), s.v1FinanceOrderAction)
		admin.POST("/finance/orders/:out_trade_no/mark-settled", s.requireSuperadmin(), s.v1FinanceOrderAction)
		admin.GET("/finance/daily-stats", s.v1FinanceDailyStats)

		admin.GET("/settings/full", s.v1AdminGetFullSettings)
		admin.POST("/settings", s.requireSuperadmin(), s.v1AdminPostFullSettings)
		admin.POST("/settings/full", s.requireSuperadmin(), s.v1AdminPostFullSettings)
		admin.GET("/settings/currency/fx", s.v1SettingsCurrencyFX)
		admin.POST("/settings/currency/fx/refresh", s.v1SettingsCurrencyFX)
		admin.GET("/settings/database/info", s.v1SettingsDatabaseInfo)
		admin.POST("/settings/database/verify", s.v1SettingsDatabaseVerify)
		admin.POST("/settings/cron/trigger-maintenance", s.v1SettingsTriggerMaintenance)
		admin.POST("/settings/usage-stats/sync", s.v1SettingsUsageStatsSync)
		admin.GET("/system/about", s.v1SystemAbout)
		admin.GET("/plugins", s.v1AdminListPlugins)
		admin.GET("/plugins/high_availability_channel/ha-config", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"enabled": false, "items": []any{}})
		})

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
	full := s.buildFullSettingsPayload()
	site, _ := full["site"].(map[string]any)
	reg, _ := full["registration"].(map[string]any)
	login, _ := full["login"].(map[string]any)
	cur, _ := full["currency"].(map[string]any)
	_, cny := s.effectiveCNYPerUSD()
	if cur != nil {
		cur["cny_per_usd"] = cny
		cur["currency_display_enabled"] = s.getBoolSetting("currency_display_enabled")
	}
	c.JSON(http.StatusOK, gin.H{
		"site":         site,
		"login":        login,
		"registration": reg,
		"currency":     cur,
		"agreement":    full["agreement"],
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
	ctx := c.Request.Context()
	_ = v1compat.EnsurePresetRules(ctx, s.db)
	res, err := v1compat.BuildPublicMarketplace(ctx, s.readOnly())
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
