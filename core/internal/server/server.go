// Package server：core 控制面 HTTP 层（Gin）。
// 业务逻辑一律在 internal/{identity,ledger,relay,...}，handler 只做参数与编排。
package server

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/mailer"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

type Options struct {
	AuthSecret  []byte               // session token HMAC（必填）
	AdminToken  []byte               // X-Admin-Token；空 = admin API 整组禁用
	Keyring     *crypto.Keyring      // 渠道凭证信封加密；nil = 渠道管理禁用
	Recorder    *invocation.Recorder // 调用记录；nil = relay 不记录
	TaskService *task.Service        // 异步任务服务；nil = 按默认构造（main 传入以共享轮询实例）
	// AuthRateBurst：认证端点 per-IP 突发上限；0 = 生产默认 5。测试设大值绕开。
	AuthRateBurst int
	Mailer        mailer.Mailer // 邮件发送；nil = 邮件功能禁用（验证端点 503）
	Log           *slog.Logger
}

type Server struct {
	db          *gorm.DB
	secret      []byte
	adminToken  []byte
	keyring     *crypto.Keyring
	authLimiter *ipLimiter
	relay       *relay.Relay     // 渠道一键测试用；nil = 测试端点 503
	mailer      mailer.Mailer    // 邮件发送；nil = 邮件功能禁用
	log         *slog.Logger
	now         func() time.Time // 可注入时钟，测试用
}

// Log 返回服务器 logger（payment/audit 等用）。
func (s *Server) Log() *slog.Logger {
	if s.log == nil {
		return slog.Default()
	}
	return s.log
}

const controlPlaneBodyLimit = 1 << 20 // 1MB：控制面 JSON 足够（审查 M2）

// New 构建 HTTP handler。后台 job（对账等）需 *Server 句柄时用 NewWithServer。
func New(db *gorm.DB, opts Options) *gin.Engine {
	r, _ := NewWithServer(db, opts)
	return r
}

// NewWithServer 同 New，但额外返回 *Server，供 main.go 启动后台 job（ReconcileTopups 等）。
func NewWithServer(db *gorm.DB, opts Options) (*gin.Engine, *Server) {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	authBurst := opts.AuthRateBurst
	if authBurst == 0 {
		authBurst = 5 // 生产默认：5 突发 + 1/秒补充
	}
	s := &Server{
		db: db, secret: opts.AuthSecret,
		adminToken: opts.AdminToken, keyring: opts.Keyring,
		authLimiter: newIPLimiter(rate.Every(time.Second), authBurst),
		mailer:      opts.Mailer,
		log:         log,
		now:         time.Now,
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// relay 实例先建（admin 渠道测试 + 数据面共用）；keyring 缺失则两者一并禁用
	var rl *relay.Relay
	if opts.Keyring != nil {
		rl = relay.New(db, opts.Keyring, opts.Recorder, log.With("component", "relay"))
		s.relay = rl
	}

	r.GET("/healthz", s.healthz)

	api := r.Group("/api")
	api.Use(bodyLimit(controlPlaneBodyLimit)) // 控制面体上限（审查 M2）
	{
		// 认证端点限流（审查 H1）：挡暴力破解 + Argon2 内存放大
		api.POST("/auth/register", s.authRateLimit(), s.register)
		api.POST("/auth/login", s.authRateLimit(), s.login)
		api.POST("/auth/send-verification", s.authRateLimit(), s.sendVerification)
		api.POST("/auth/2fa", s.authRateLimit(), s.twofaVerifyLogin) // 登录二阶段
		api.GET("/auth/oauth", s.getOAuthProviders)                  // 已配置的 OAuth 提供方
		api.GET("/auth/oauth/:provider/start", s.authRateLimit(), s.oauthStart)
		api.GET("/auth/oauth/:provider/callback", s.authRateLimit(), s.oauthCallback) // 限流：callback 触发外联 token 交换 + 建号
		api.POST("/auth/telegram", s.authRateLimit(), s.telegramLogin) // Telegram Login Widget（gap P1-C）
		api.GET("/pricing", s.getPricing) // 公开价目（营销站/模型市场同源）
		api.GET("/config", s.getConfig)       // 公开运营配置（站名/注册开关/公告/法务）
		api.GET("/rankings", s.getRankings)   // 公开模型/厂商调用量排行（营销/SEO）
		api.GET("/status", s.publicStatus)    // 公开状态页（粗粒度组件健康，M3）
		api.POST("/payment/callback/:provider", s.paymentCallback) // 支付 webhook（验签入金，无需登录）
		api.POST("/payment/x402/settle/:order_no", s.x402Settle)   // x402 链上结算入账（§5.2，默认禁用）

		authed := api.Group("", s.authRequired())
		{
			authed.GET("/me", s.me)
			authed.PATCH("/me", s.updateProfile)
			authed.POST("/me/password", s.changePassword)
			authed.POST("/me/2fa/setup", s.twofaSetup)
			authed.POST("/me/2fa/enable", s.twofaEnable)
			authed.POST("/me/2fa/disable", s.twofaDisable)
			authed.POST("/me/2fa/backup-codes", s.twofaRegenBackup) // 重置备用码（gap #3）
			authed.POST("/me/verify", s.authRateLimit(), s.meVerify)           // 二次验证换 step-up 令牌（gap #4）；限流防暴力猜密码/TOTP
			authed.POST("/keys", s.requireStepUpForKeys(), s.createKey)         // 受开关保护：开启则需 step-up
			authed.GET("/keys", s.listKeys)
			authed.DELETE("/keys/:id", s.revokeKey)
			authed.DELETE("/keys/:id/purge", s.deleteKey)
			authed.GET("/usage", s.getUsage)
			authed.GET("/usage/summary", s.getUsageSummary)
			authed.GET("/usage/by-model", s.getUsageByModel)
			authed.GET("/usage/by-model-daily", s.getUsageByModelDaily)
			authed.GET("/usage/perf", s.getUsagePerf)
			authed.GET("/balance", s.getBalance)
			authed.GET("/ledger", s.getLedger)
			authed.GET("/payment/methods", s.getPaymentMethods)
			authed.POST("/payment/create", s.createTopup)
			authed.GET("/payment/orders", s.listTopups)
			authed.GET("/affiliate", s.getAffiliate)
			authed.GET("/orgs", s.listOrgs)
			authed.POST("/orgs", s.createOrg)
			authed.POST("/auth/switch", s.switchOrg)
			authed.GET("/org/members", s.listMembers)
			authed.POST("/org/members", s.addMember)
			authed.DELETE("/org/members/:user_id", s.removeMember)
		}

		admin := api.Group("/admin", s.adminRequired(identity.PlatRoleAdmin))
		{
			// —— admin 级（运营）：渠道 / 模型定价 / 查看 ——（对标 NewAPI AdminAuth）
			admin.POST("/channels", s.adminCreateChannel)
			admin.GET("/channels", s.adminListChannels)
			admin.PUT("/channels/:id", s.adminUpdateChannel)
			admin.DELETE("/channels/:id", s.adminDisableChannel)
			admin.POST("/channels/:id/test", s.adminTestChannel)
			admin.POST("/channels/probe", s.adminProbeChannel)
			admin.POST("/model-prices", s.adminUpsertPrice)
			admin.GET("/model-prices", s.adminListPrices)
			admin.GET("/model-catalog", s.adminModelCatalog)
			admin.GET("/providers", s.adminListProviders)
			admin.GET("/orgs", s.adminListOrgs)
			admin.GET("/overview", s.adminOverview)
			admin.GET("/analytics", s.adminAnalytics)
			admin.GET("/channel-health", s.adminChannelHealth) // M2 渠道健康看板
			admin.GET("/invocations", s.adminListInvocations)
			admin.GET("/audit-logs", s.adminListAuditLogs)
			admin.GET("/users", s.adminListUsers)
			admin.POST("/users/:id/manage", s.adminManageUser) // 含 targetRole 守卫（不能动同级/更高）

			// —— superadmin 级（资金 / 系统设置 / 定价策略 / 授权）——（对标 NewAPI RootAuth）
			su := admin.Group("", s.requireSuperadmin())
			{
				su.POST("/credits", s.adminGrantCredit)             // 授信入金（动钱）
				su.GET("/settings", s.adminGetSettings)             // 系统设置
				su.PUT("/settings", s.adminSetSettings)
				su.GET("/group-ratios", s.adminListGroupRatios)     // 差异化定价倍率
				su.POST("/group-ratios", s.adminUpsertGroupRatio)
				su.DELETE("/group-ratios/:name", s.adminDeleteGroupRatio)
				su.PUT("/orgs/:id/group", s.adminSetOrgGroup)       // 指派计费分组（定价）
				su.PUT("/orgs/:id/entitlements", s.adminSetEntitlements) // P0-2 功能调配
				su.POST("/users/:id/role", s.adminSetUserRole)      // 授角色
				su.GET("/payment-config", s.adminPaymentConfig)     // 支付配置（含密钥引用）
				su.GET("/payment-channels", s.adminListPaymentChannels)
				su.POST("/payment-channels", s.adminUpsertPaymentChannel)
				su.DELETE("/payment-channels/:id", s.adminDeletePaymentChannel)
				su.POST("/payment-channels/:id/test", s.adminTestPaymentChannel)
				su.GET("/payment-channels/route-preview", s.adminPaymentRoutePreview)
			}
		}
	}

	// Relay 数据面（M1 暂驻 core 进程，M2/M3 拆独立二进制）
	if rl != nil {
		r.GET("/v1/models", rl.HandleListModels)
		r.POST("/v1/chat/completions", rl.HandleChatCompletions)
		r.POST("/v1/embeddings", rl.HandleEmbeddings)
		r.POST("/v1/images/generations", rl.HandleImages)

		// 异步任务（Seedance 类视频生成，设计 §4.4 Task API）
		ts := opts.TaskService
		if ts == nil {
			ts = task.NewService(db, opts.Keyring, log.With("component", "task"))
		}
		r.POST("/v1/video/generations", ts.HandleCreate)
		r.GET("/v1/video/generations/:id", ts.HandleGet)
	}

	return r, s
}

func (s *Server) healthz(c *gin.Context) {
	sqlDB, err := s.db.DB()
	if err == nil {
		err = sqlDB.Ping()
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "db": "down"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func fail(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": msg})
}
