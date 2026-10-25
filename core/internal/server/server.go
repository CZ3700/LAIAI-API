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
	"github.com/laiai/laiai-next/core/internal/notify"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

type Options struct {
	AuthSecret       []byte               // session token HMAC（必填）
	AdminToken       []byte               // X-Admin-Token；空 = admin API 整组禁用
	Keyring          *crypto.Keyring      // 渠道凭证信封加密；nil = 渠道管理禁用
	Recorder         *invocation.Recorder // 调用记录；nil = relay 不记录
	TaskService      *task.Service        // 异步任务服务；nil = 按默认构造（main 传入以共享轮询实例）
	VideoService     *video.Service
	VideoMediaSource http.Handler // Signed, short-lived media fetches for the provider.
	// AuthRateBurst：认证端点 per-IP 突发上限；0 = 生产默认 5。测试设大值绕开。
	AuthRateBurst int
	Mailer        mailer.Mailer // 邮件发送；nil = 邮件功能禁用（验证端点 503）
	Log           *slog.Logger
	// RedisRateLimiter：接入 Redis 跨实例限流（多实例部署用）；nil = relay 退化进程内限流。
	RedisRateLimiter *redis.Client
	// ReadDB：只读副本句柄（规模化）；nil = 用主库。🚨 只供「非金钱、容延迟」的分析/用量/排行/
	// 公开价目只读；余额/quota/hold 等金钱判定读永远用主 db（复制延迟致超发，资金红线）。
	ReadDB *gorm.DB
	// InvocationRetentionDays：调用日志保留天数的「启动期默认」（env 注入）。
	// 运营可用 ext_settings.log_retention_days 热覆盖；该值仅作未配置时的回退。0 = 永久保留。
	InvocationRetentionDays int
	// TrustedProxies（指令 P1-2）：可信反代 CIDR/IP。空 = 私网段默认；["none"] = 不信任任何代理头。
	// ClientIP 是 keyGate IP 白名单/认证限流/审计 IP 的共同口径。
	TrustedProxies []string
}

type Server struct {
	db                  *gorm.DB
	readDB              *gorm.DB // 只读副本（规模化）；nil 时 readOnly() 回退主 db。仅非金钱分析读用。
	secret              []byte
	adminToken          []byte
	keyring             *crypto.Keyring
	authLimiter         *ipLimiter
	diagLimiter         *ipLimiter         // per-user Key 检测配额（§4.8）——以 userID 为键，抗 IP 轮换
	twofaLimiter        *ipLimiter         // 2FA 二阶段 per-account 限流（防中间票窗口内暴力猜 TOTP）
	relay               *relay.Relay       // 渠道一键测试用；nil = 测试端点 503
	mailer              mailer.Mailer      // 邮件发送；nil = 邮件功能禁用
	notify              *notify.Dispatcher // 统一通知层（§4.9）；nil = 通知禁用
	video               *video.Service
	log                 *slog.Logger
	now                 func() time.Time    // 可注入时钟，测试用
	invRetentionDefault int                 // 调用日志保留天数的启动期默认（ext_settings 未配置时回退）
	invocationQueries   invocationQueryGate // v1 metadata queries: global 4, subject 2, zero wait.
	routingGroupReads   routingAdminReadGate
}

// readOnly 返回只读副本句柄（规模化：分析/用量/排行/公开价目等重读卸载到副本）；未配副本回退主库。
// 🚨 资金红线：仅用于「非金钱、容延迟」只读。余额/quota/hold 预检/任何写永远用 s.db（复制延迟致超发）。
func (s *Server) readOnly() *gorm.DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
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
		db: db, readDB: opts.ReadDB, secret: opts.AuthSecret,
		video:      opts.VideoService,
		adminToken: opts.AdminToken, keyring: opts.Keyring,
		authLimiter:         newIPLimiter(rate.Every(time.Second), authBurst),
		twofaLimiter:        newIPLimiter(rate.Every(time.Second), authBurst), // 与 auth 同档（生产默认 5）；测试设大值绕开
		diagLimiter:         newIPLimiter(rate.Every(30*time.Second), 20),     // per-user 诊断配额（§4.8 对抗 review）：突发 20 后 2/min，抗 IP 轮换的账号侧封顶
		log:                 log,
		now:                 time.Now,
		invRetentionDefault: opts.InvocationRetentionDays,
	}
	// 邮件走「settings 热配优先 + env 回退」包装：admin 后台填 SMTP 即生效，无需重启/改 env。
	s.mailer = &settingsMailer{s: s, env: opts.Mailer}
	s.authLimiter.useRedis(opts.RedisRateLimiter)  // 多实例：auth 限流跨副本聚合（nil 退化进程内）
	s.twofaLimiter.useRedis(opts.RedisRateLimiter) // 同上：2FA per-account 限流跨副本聚合
	s.diagLimiter.useRedis(opts.RedisRateLimiter)  // 同上：诊断 per-user 配额跨副本聚合
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// 可信反代（指令 P1-2）：gin 默认信任一切来源的 X-Forwarded-For——直连 core 的请求可伪造
	// 客户端 IP，绕过 keyGate IP 白名单/认证限流/审计口径。显式收窄：
	//   未配置 → 私网段默认（容器拓扑：core 仅经同网 Caddy 可达，Caddy 会重写不可信 XFF）；
	//   "none" → 不信任任何代理头（core 直接对公网时用）；
	//   其余  → 运营显式给出的 CIDR/IP 列表（CN2/CF 前置拓扑照 runbook 配）。
	trusted := opts.TrustedProxies
	switch {
	case len(trusted) == 1 && trusted[0] == "none":
		trusted = nil
	case len(trusted) == 0:
		trusted = []string{"127.0.0.1/32", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}
	}
	if err := r.SetTrustedProxies(trusted); err != nil {
		// config.Load 已做启动校验；此处仅兜底直连构造（测试）传坏值
		log.Error("invalid trusted proxies, refusing all proxy headers", "err", err)
		_ = r.SetTrustedProxies(nil)
	}

	// relay 实例先建（admin 渠道测试 + 数据面共用）；keyring 缺失则两者一并禁用
	var rl *relay.Relay
	if opts.Keyring != nil {
		rl = relay.New(db, opts.Keyring, opts.Recorder, log.With("component", "relay"))
		rl.UseRedisRateLimiter(opts.RedisRateLimiter)    // 非 nil 才接入；nil 退化进程内限流
		rl.SubscribeInvalidations(opts.RedisRateLimiter) // 跨实例热读缓存失效（同一 Redis 客户端复用）
		s.relay = rl
	}

	// 统一通知层（§4.9）：邮件复用 settingsMailer；出站 webhook 走 guarded client（SSRF 纪律，
	// 恒不放行私网，与 BYOK 同档）。Emit 是 best-effort，keyring 缺失时 webhook 通道不可用（邮件仍可）。
	s.notify = &notify.Dispatcher{
		DB: db, Keyring: opts.Keyring, Log: log.With("component", "notify"),
		Mail:       s.mailer.Send,
		MailActive: s.mailer.Active,
		HTTP:       relay.NewGuardedClient(10*time.Second, false),
		Now:        s.now,
	}

	r.GET("/healthz", s.healthz)
	if opts.VideoMediaSource != nil {
		r.GET("/api/video/media-source/*path", gin.WrapH(opts.VideoMediaSource))
		r.HEAD("/api/video/media-source/*path", gin.WrapH(opts.VideoMediaSource))
	}
	// Streaming media has a per-type limit in its handler. Keep the ordinary
	// JSON API's 1 MiB limit and all session/membership checks intact.
	mediaOrg := r.Group("/api", s.authRequired(), s.requireActiveUser(), s.requireActiveMembership())
	var videoConsoleOrg *gin.RouterGroup

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
		api.POST("/auth/telegram", s.authRateLimit(), s.telegramLogin)                // Telegram Login Widget（gap P1-C）
		api.GET("/pricing", s.getPricing)                                             // 公开价目（营销站/模型市场同源）
		api.GET("/config", s.getConfig)                                               // 公开运营配置（站名/注册开关/公告/法务）
		api.GET("/status", s.publicStatus)                                            // 公开状态页（粗粒度组件健康，M3）
		api.POST("/payment/callback/:provider", s.paymentCallback)                    // 支付 webhook（验签入金，无需登录）

		// authed：会话令牌 + 用户 active（P0-1：封禁用户旧令牌即时失效）。
		authed := api.Group("", s.authRequired(), s.requireActiveUser())
		{
			// —— 账户级（仅需用户 active）：个人资料 / 2FA / 列我的组织 / 切换组织 ——
			// 被移出当前 org 的有效用户仍可在此列出并切换到其它有效 org（switchOrg 校验目标 membership）。
			authed.GET("/me", s.me)
			authed.PATCH("/me", s.updateProfile)
			authed.PUT("/me/preferences", s.updatePreferences)
			authed.POST("/me/password", s.changePassword)
			authed.POST("/me/2fa/setup", s.twofaSetup)
			authed.POST("/me/2fa/enable", s.twofaEnable)
			authed.POST("/me/2fa/disable", s.twofaDisable)
			authed.POST("/me/2fa/backup-codes", s.twofaRegenBackup)  // 重置备用码（gap #3）
			authed.POST("/me/verify", s.authRateLimit(), s.meVerify) // 二次验证换 step-up 令牌（gap #4）；限流防暴力猜密码/TOTP
			authed.GET("/orgs", s.listOrgs)
			authed.POST("/orgs", s.createOrg)
			authed.POST("/auth/switch", s.switchOrg)
			authed.POST("/diagnostics/check", s.authRateLimit(), s.checkKey) // Key 检测工具站（§4.8）；限流防拿平台当扫描器/SSRF 跳板
			// 团队邀请（被邀请人侧，0029）：未入队前不能 org-scoped，故挂 authed。
			authed.GET("/invitations", s.listMyInvitations)              // 我收到的待确认邀请
			authed.POST("/invitations/:id/accept", s.acceptInvitation)   // 接受 → 入队
			authed.POST("/invitations/:id/decline", s.declineInvitation) // 拒绝

			// —— org-scoped（P0-1：还需 org active + 当前会话用户仍是该 org 成员）——
			org := authed.Group("", s.requireActiveMembership())
			videoConsoleOrg = org
			{
				s.registerFundActivities(org)
				org.POST("/keys", s.requireStepUpForKeys(), s.createKey) // 受开关保护：开启则需 step-up
				org.GET("/keys", s.listKeys)
				org.GET("/keys/video-access", s.keyVideoAccess)
				org.DELETE("/keys/:id", s.revokeKey)
				org.DELETE("/keys/:id/purge", s.deleteKey)
				org.GET("/usage", s.getUsage)
				org.GET("/usage/export", s.getUsageExport) // 调用明细 CSV 自助导出（Latency/Usage 页）
				org.GET("/usage/summary", s.getUsageSummary)
				org.GET("/usage/by-model", s.getUsageByModel)
				org.GET("/usage/by-key", s.getUsageByKey)         // 仪表盘密钥排行榜
				org.GET("/usage/perf-stats", s.getUsagePerfStats) // 延迟/成功率 服务端分位聚合（Latency 页）
				org.GET("/balance", s.getBalance)
				org.GET("/ledger", s.getLedger)
				org.GET("/payment/methods", s.getPaymentMethods)
				org.POST("/payment/create", s.createTopup)
				org.GET("/payment/orders", s.listTopups)
				org.GET("/payment/recharge-stats", s.getRechargeStats) // 仪表盘本月/近7日充值聚合
				org.GET("/affiliate", s.getAffiliate)
				org.GET("/org/balance-alert", s.getBalanceAlert)     // 余额低预警阈值（0042）
				org.PUT("/org/balance-alert", s.putBalanceAlert)     // 仅 owner/admin
				org.GET("/org/notify-settings", s.getNotifySettings) // 统一通知订阅（§4.9 Notify）
				org.PUT("/org/notify-settings", s.putNotifySettings) // 仅 owner/admin
				org.GET("/org/byok-channels", s.listByokChannels)    // BYOK 自带上游 Key（0044）
				org.POST("/org/byok-channels", s.createByokChannel)
				org.PATCH("/org/byok-channels/:id", s.patchByokChannel)
				org.DELETE("/org/byok-channels/:id", s.deleteByokChannel)
				org.GET("/org/members", s.listMembers)
				org.POST("/org/members", s.inviteMember) // 发邀请（pending），对方确认后才入队
				org.DELETE("/org/members/:user_id", s.removeMember)
				org.GET("/org/invitations", s.listOrgInvitations)         // 本团队待确认邀请（管理者）
				org.DELETE("/org/invitations/:id", s.revokeOrgInvitation) // 撤销待确认邀请（管理者）
			}
		}

		admin := api.Group("/admin", s.adminRequired(identity.PlatRoleAdmin))
		{
			// —— admin 级（运营）：渠道 / 模型定价 / 查看 ——（对标 NewAPI AdminAuth）
			admin.POST("/channels", s.adminCreateChannel)
			admin.GET("/channels", s.adminListChannels)
			admin.PUT("/channels/:id", s.adminUpdateChannel)
			admin.POST("/channels/:id/disable", s.adminDisableChannel) // 手动禁用（status→0）
			admin.POST("/channels/:id/enable", s.adminEnableChannel)   // 重新启用恢复（→1）
			admin.DELETE("/channels/:id", s.adminDeleteChannel)        // 真正删除渠道行
			admin.POST("/channels/:id/test", s.adminTestChannel)
			admin.POST("/channels/probe", s.adminProbeChannel)
			admin.POST("/diagnostics/check", s.adminCheckKey) // Key 检测丰富指纹（§4.8 v1.1，运维检测上游质量）
			// 渠道多 key 管理（0034 Multi-key）
			admin.GET("/channels/:id/credentials", s.adminListChannelKeys)
			admin.POST("/channels/:id/credentials", s.adminAddChannelKey)
			admin.POST("/channels/:id/credentials/:cid/disable", s.adminDisableChannelKey)
			admin.POST("/channels/:id/credentials/:cid/enable", s.adminEnableChannelKey)
			admin.DELETE("/channels/:id/credentials/:cid", s.adminDeleteChannelKey)
			admin.GET("/channels/:id/fetch-models", s.adminFetchChannelModels) // 自动拉模型+缺失检测（gap P2-D）
			// 上游模型同步动作（0037 §3.10）：一键上货/下架/忽略
			admin.POST("/channels/:id/apply-models", s.adminApplyChannelModels)
			admin.POST("/channels/:id/mark-removed", s.adminMarkChannelModelsRemoved)
			admin.POST("/channels/:id/ignore-models", s.adminIgnoreChannelModels)
			admin.POST("/model-prices", s.adminUpsertPrice)
			admin.GET("/model-prices", s.adminListPrices)
			admin.POST("/model-meta", s.adminUpsertModelMeta) // 模型元数据 upsert（gap P2-D）
			admin.GET("/model-catalog", s.adminModelCatalog)
			admin.GET("/pricing-catalog", s.adminPricingCatalog)
			admin.GET("/providers", s.adminListProviders)
			admin.GET("/orgs", s.adminListOrgs)
			admin.GET("/overview", s.adminOverview)
			admin.GET("/analytics", s.adminAnalytics)
			admin.GET("/channel-health", s.adminChannelHealth)          // M2 渠道健康看板
			admin.GET("/model-channel-stats", s.adminModelChannelStats) // (model×channel) 交叉看板（§5）
			admin.GET("/audit-logs", s.adminListAuditLogs)
			admin.GET("/users", s.adminListUsers)
			admin.POST("/users/:id/manage", s.adminManageUser)           // 含 targetRole 守卫（不能动同级/更高）
			admin.POST("/users/:id/remark", s.adminSetUserRemark)        // 运营备注（gap P2-B，含等级守卫）
			admin.GET("/usage/by-user", s.adminUsageByUser)              // 全平台按用户聚合用量（gap P2-B）
			admin.GET("/usage/by-user/export", s.adminUsageByUserExport) // 同上 CSV 导出
			admin.GET("/log-stats", s.adminLogStats)                     // 日志量 + 保留期一览（gap P2-C）

			// —— superadmin 级（资金 / 系统设置 / 定价策略 / 授权）——（对标 NewAPI RootAuth）
			su := admin.Group("", s.requireSuperadmin())
			{
				s.registerRoutingGroupsAdmin(su)
				s.registerVideoConfiguration(su)
				s.registerArkBillingAdmin(su)
				s.registerArkAssetAdmin(su)
				s.registerArkAssetsConsole(videoConsoleOrg, su, mediaOrg)
				s.registerArkPricingConsole(su)
				s.registerArkVerification(su)
				s.registerArkVariantPricePlans(su)
				s.registerArkOfficialPriceCatalog(su)
				su.GET("/video/models/:model/pricing/reference-candidates", s.getArkReferenceCandidates)
				s.registerArkRequestLogs(videoConsoleOrg, su)
				su.GET("/video/worker-observability", s.getArkWorkerObservability)
				// 跨租户全平台调用明细（org_id/user_id/model/cost）：限 superadmin（P2-3）
				su.GET("/invocations", s.adminListInvocations)
				su.POST("/credits", s.adminGrantCredit) // 授信入金（动钱）
				su.GET("/settings", s.adminGetSettings) // 系统设置
				su.PUT("/settings", s.adminSetSettings)
				su.GET("/group-ratios", s.adminListGroupRatios) // 差异化定价倍率
				su.POST("/group-ratios", s.adminUpsertGroupRatio)
				su.DELETE("/group-ratios/:name", s.adminDeleteGroupRatio)
				su.GET("/model-group-ratios", s.adminListModelGroupRatios) // 二层 (分组,模型) 倍率（gap P2-F）
				su.POST("/model-group-ratios", s.adminUpsertModelGroupRatio)
				su.DELETE("/model-group-ratios", s.adminDeleteModelGroupRatio)
				su.PUT("/orgs/:id/group", s.adminSetOrgGroup)            // 指派计费分组（定价）
				su.PUT("/orgs/:id/entitlements", s.adminSetEntitlements) // P0-2 功能调配
				su.PUT("/orgs/:id/shards", s.adminSetOrgShards)          // 规模化：账户分片数（巨客户写吞吐）
				su.POST("/users/:id/role", s.adminSetUserRole)           // 授角色
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
		// 客户端可观测性头透传（DevEx 承诺）：x-trace-id / x-user-id / x-agent-name 原样回显到响应头，
		// 客户端据此把自己的 trace 与我方 X-Request-Id 对齐。中间件挂在 relay 路由组，不动各 handler。
		rg := r.Group("", echoTraceHeaders())
		rg.GET("/v1/models", s.listPricedModels)
		rg.POST("/v1/chat/completions", rl.HandleChatCompletions)
		// Anthropic 原生入站（Claude Code / Anthropic SDK 把 base 指到 /anthropic 即用）：
		// ingress 转 OpenAI canonical → 复用未改动的 HandleChatCompletions 全套计费/路由 → egress 转回 Anthropic。
		rg.POST("/anthropic/v1/messages", rl.HandleAnthropicMessages)
		// OpenAI Responses 原生入站（Responses SDK / Codex 把 base 指到本网关即用任意 chat 上游）：
		// ingress 转 OpenAI canonical → 复用未改动 HandleChatCompletions → egress 转回 Responses 形态（§3.2）。
		rg.POST("/v1/responses", rl.HandleResponses)
		rg.POST("/v1/embeddings", rl.HandleEmbeddings)
		rg.POST("/v1/rerank", rl.HandleRerank) // 重排序（WS-1，RAG；Jina/SiliconFlow/Cohere/Qwen）
		rg.POST("/v1/images/generations", rl.HandleImages)
		rg.POST("/v1/images/edits", rl.HandleImageEdits)                 // 图生图编辑（D1，multipart，按张计价）
		rg.POST("/v1/images/variations", rl.HandleImageVariations)       // 图像变体（D1，multipart）
		rg.POST("/v1/audio/speech", rl.HandleAudioSpeech)                // TTS（gap P2-F，按字符计价）
		rg.POST("/v1/audio/transcriptions", rl.HandleAudioTranscription) // STT（gap P2-F，按次固定价）
		rg.POST("/v1/audio/translations", rl.HandleAudioTranslation)     // STT 翻译到英文（D1，同转写计价）
		rg.GET("/v1/realtime", rl.HandleRealtime)                        // Realtime WS 网关（§3.9；OpenAI/Azure，token 实时计费）

		// 异步任务（Seedance 类视频生成，设计 §4.4 Task API）
		ts := opts.TaskService
		if ts == nil {
			ts = task.NewService(db, opts.Keyring, log.With("component", "task"))
		}
		if ts.Gate == nil {
			ts.Gate = rl // 准入统一（P0-1）：与 chat 同一实例——同一 keyGate 语义、同一限流窗口
		}
		if ts.Rec == nil {
			ts.Rec = opts.Recorder // 任务终态进 Invocation Store（请求日志可见视频消费）
		}
		if ts.Notify == nil {
			ts.Notify = s.notify // 任务完成/失败回调（§4.9 Notify）；best-effort，绝不影响结算
		}
		// 数据面体上限（P0-2）：与 relay 各端点 io.LimitReader 同一上限，超限 413
		vg := r.Group("", bodyLimit(relay.MaxRequestBodySize))
		if opts.VideoService != nil {
			if opts.VideoService.Gate == nil {
				opts.VideoService.Gate = rl
			}
			opts.VideoService.RegisterRoutes(vg, ts)
		} else {
			vg.POST("/v1/video/generations", video.DisabledOfficialCreate(ts.HandleCreate))
			vg.GET("/v1/video/generations/:id", ts.HandleGet)
			vg.GET("/v1/video/generations/:id/content", ts.HandleContent)
		}
		// Midjourney（mjproxy 网关方言，丝滑度批 C-4）：MJ 客户端生态标准协议；task 骨架复用
		vg.POST("/mj/submit/:action", ts.HandleMJSubmit)
		vg.GET("/mj/task/:id/fetch", ts.HandleMJFetch)
		vg.POST("/mj/task/list-by-condition", ts.HandleMJList)
		vg.GET("/mj/image/:id", ts.HandleMJImage)
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
