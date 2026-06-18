package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// —— 运营设置（0013，运行时可配项） ——

type Setting struct {
	Name  string `gorm:"primaryKey;column:name" json:"name"`
	Value string `gorm:"column:value" json:"value"`
}

func (Setting) TableName() string { return "ext_settings" }

// 已知设置键（公开/admin 共用），含默认值。adminSetSettings 只接受这里登记的键，
// 所以「前端表单/引擎用到的每个键」都必须在此登记，否则保存会被当未知键拒绝。
var settingDefaults = map[string]string{
	// —— 通用 / 门面 ——
	"site_name":        "LAIAI",
	"announcement":     "",
	"terms_of_service": "",
	"privacy_policy":   "",
	"support_email":    "",
	"footer_text":      "",
	"maintenance_mode": "false",
	// —— 注册 / 登录 ——
	"register_enabled":           "true",
	"email_verification_enabled": "true", // 默认开启：注册必须验证真实邮箱（绑定邮箱供后续推送，用户要求）。须配 SMTP 才能发码，否则注册 fail-closed。
	"new_user_grant_usd":         "0",    // 注册即赠送额度（USD，0=不送）；register 流程消费
	// 邮箱准入策略（gap P2-E，checkEmailDomainPolicy 消费；作用于注册/发码/OAuth）
	"email_domain_whitelist": "",      // 逗号分隔允许域名；空=不限制
	"email_domain_blocklist": "",      // 逗号分隔禁止域名（挡一次性邮箱）；空=不禁
	"email_alias_block":      "false", // 拒绝带 +别名 的邮箱
	// —— SMTP（admin 后台热配，settingsMailer 消费；改了即生效无需重启/env）——
	"smtp_host":     "",      // SMTP 服务器地址（空=回退 env / logMailer）
	"smtp_port":     "",      // 端口（空=按 SSL 取 465/587 默认）
	"smtp_username": "",      // SMTP 账户
	"smtp_password": "",      // 访问凭证（secret：加密落库 + 掩码回显）
	"smtp_from":     "",      // 发件人邮箱（空=取 username）
	"smtp_ssl":      "false", // true=隐式 TLS(465)；false=STARTTLS(587)
	// —— 支付（引擎 engineFor 消费；密钥服务端存，前端永不回显明文）——
	"stripe_secret_key":     "",
	"stripe_webhook_secret": "",
	"epay_pid":              "",
	"epay_key":              "",
	"epay_api_url":          "",
	"cny_per_usd":           "",  // 空=默认 7.3
	"min_topup_usd":         "1", // 最低充值额（USD）；createTopup 消费
	// 充值阶梯优惠（gap P1-D）：JSON 数组 [{"min_usd":"50","rate":"0.05"},...]，空=无优惠。
	// 入账时取「amount≥min_usd 的最高档 min_usd」对应 rate，bonus=amount×rate（KindGrant 单列入账）。
	"recharge_bonus_tiers": "",
	// —— 邀请 ——
	// 注：返佣比例（affiliate_rate）已彻底移除——本站不提供「按充值额给邀请人返佣」功能（旧平台迁移残留）。
	// 被邀请赠额（gap P1-D）：携有效邀请码注册时给「被邀请人」一次性赠额（USD，0=不送）。
	// 防薅羊毛：开启时应同时开 email_verification_enabled（与 new_user_grant_usd 同口径）。
	"invite_grant_usd": "0",
	// —— 渠道治理 ——
	"channel_auto_disable":      "false", // 默认手动管理渠道开关（用户要求）；置 true 才启用"连续失败自动禁用"
	"channel_disable_threshold": "1",     // 连续失败次数阈值（1=判定性错误即禁用，与 relay 默认一致）
	// —— 模型级限流（gap #2，relay allowModelRate 消费）——
	"model_rate_limit_enabled": "false", // 开关
	"model_rate_limit_rpm":     "0",     // 每 (org, model) 每分钟请求上限；0=不限
	// —— 二次验证（gap #4，requireStepUpForKeys 消费）——
	"require_stepup_for_keys": "false", // 开启则创建 API Key 需 step-up 再认证（默认关，零摩擦）
	// —— 旧日志清理（gap P2-C，RunLogRetention 每小时消费；0/空=永久保留）——
	"log_retention_days":       "",  // 调用日志保留天数；空=回退到 env 默认（INVOCATION_RETENTION_DAYS）
	"audit_log_retention_days": "0", // 审计日志保留天数；0=永久保留（审计默认不删，合规优先）
	// —— OAuth（oauthStart/Callback 消费）——
	"github_client_id":     "",
	"github_client_secret": "",
	"google_client_id":     "",
	"google_client_secret": "",
}

// secretSettingKeys：值为敏感凭据的设置键（P1-3）——落库信封加密、内部读时解密、
// admin API 只回显掩码、PUT 收到掩码/空＝保留原值（write-only）。epay_pid/client_id/
// *_api_url 是标识/地址非密钥，不在此列。
var secretSettingKeys = map[string]bool{
	"stripe_secret_key":     true,
	"stripe_webhook_secret": true,
	"epay_key":              true,
	"github_client_secret":  true,
	"google_client_secret":  true,
	"discord_client_secret": true,
	"oidc_client_secret":    true,
	"smtp_password":         true, // SMTP 访问凭证：加密落库 + 掩码回显（admin 后台热配）
}

// settingEncPrefix：加密落库的 secret 值前缀，getSetting 据此判定是否解密。
// 兼容历史明文（无前缀按明文返回，下次保存自动升级为密文）。
const settingEncPrefix = "enc:v1:"

// secretMask：admin GET 对「已配置」secret 的占位回显；PUT 收到此值视为「不改」。
const secretMask = "••••••••"

func (s *Server) getSetting(name string) string {
	var row Setting
	if err := s.db.Where("name = ?", name).First(&row).Error; err == nil {
		return s.decodeSettingValue(name, row.Value)
	}
	return settingDefaults[name]
}

// decodeSettingValue：secret 键若为密文（带前缀）则解密供内部消费；历史明文原样返回。
// 解密失败/无密钥环时返回 ""（绝不向调用方泄露密文），并告警。
func (s *Server) decodeSettingValue(name, val string) string {
	if val == "" || !secretSettingKeys[name] || !strings.HasPrefix(val, settingEncPrefix) {
		return val
	}
	if s.keyring == nil {
		s.Log().Error("secret setting present but keyring unavailable", "name", name)
		return ""
	}
	plain, err := s.keyring.Decrypt(strings.TrimPrefix(val, settingEncPrefix))
	if err != nil {
		s.Log().Error("secret setting decrypt failed", "name", name, "err", err)
		return ""
	}
	return plain
}

func (s *Server) getBoolSetting(name string) bool {
	return s.getSetting(name) == "true"
}

// adminGetSettings：GET /api/admin/settings —— 全部已知设置（落库值优先，否则默认）。
func (s *Server) adminGetSettings(c *gin.Context) {
	var rows []Setting
	s.db.Find(&rows)
	stored := make(map[string]string, len(rows))
	for _, r := range rows {
		stored[r.Name] = r.Value
	}
	out := make(map[string]string, len(settingDefaults))
	for k, def := range settingDefaults {
		v, ok := stored[k]
		if !ok {
			v = def
		}
		// 红线（P1-3）：secret 值永不回显——已配置回掩码、未配置回空，绝不下发明文/密文。
		if secretSettingKeys[k] {
			if v != "" {
				v = secretMask
			}
		}
		out[k] = v
	}
	c.JSON(http.StatusOK, gin.H{"settings": out})
}

// adminSetSettings：PUT /api/admin/settings {name: value, ...} —— 只接受已知键。
func (s *Server) adminSetSettings(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updated := 0
	for name, value := range req {
		if _, known := settingDefaults[name]; !known {
			fail(c, http.StatusBadRequest, "unknown setting: "+name)
			return
		}
		if secretSettingKeys[name] {
			// write-only（P1-3）：空或掩码＝保留原值（前端表单回显的是掩码，回传不应清空）。
			if value == "" || value == secretMask {
				continue
			}
			if s.keyring == nil {
				fail(c, http.StatusServiceUnavailable, "LAIAI_DATA_KEYS not configured; cannot store secret")
				return
			}
			enc, err := s.keyring.Encrypt(value)
			if err != nil {
				fail(c, http.StatusInternalServerError, "secret encrypt failed")
				return
			}
			value = settingEncPrefix + enc // 落库即密文，明文绝不入 ext_settings
		}
		if err := s.db.Save(&Setting{Name: name, Value: value}).Error; err != nil {
			fail(c, http.StatusInternalServerError, "save failed")
			return
		}
		updated++
	}
	s.writeAudit(c, "settings_update", "", "updated platform settings")
	c.JSON(http.StatusOK, gin.H{"updated": updated})
}

// getConfig：GET /api/config —— 公开运营配置（无需登录；前端门面/注册页用）。
func (s *Server) getConfig(c *gin.Context) {
	cny := s.getSetting("cny_per_usd")
	if cny == "" {
		cny = "7.3" // 与 settingDefaults 注释一致的回退（前端 ≈¥ 展示用）
	}
	c.JSON(http.StatusOK, gin.H{
		"cny_per_usd":                cny, // 前端把 USD 金额换算 ≈¥ 展示（运营可在设置热配）
		"site_name":                  s.getSetting("site_name"),
		"register_enabled":           s.getBoolSetting("register_enabled"),
		"email_verification_enabled": s.getBoolSetting("email_verification_enabled"),
		"maintenance":                s.getBoolSetting("maintenance_mode"),
		"announcement":               s.getSetting("announcement"),
		"terms_of_service":           s.getSetting("terms_of_service"),
		"privacy_policy":             s.getSetting("privacy_policy"),
		"support_email":              s.getSetting("support_email"),
		"footer_text":                s.getSetting("footer_text"),
	})
}

// —— 审计日志（0013，admin 操作留痕） ——

type AuditLog struct {
	ID        uint64 `gorm:"primaryKey" json:"id"`
	Actor     string `gorm:"column:actor" json:"actor"`
	Action    string `gorm:"column:action" json:"action"`
	Target    string `gorm:"column:target" json:"target"`
	Detail    string `gorm:"column:detail" json:"detail"`
	IP        string `gorm:"column:ip" json:"ip"`
	CreatedAt int64  `gorm:"column:created_at" json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }

// writeAudit 记录一条 admin 操作（尽力而为，失败只忽略不阻断业务）。
// actor 取自 adminRequired 解析进 context 的身份标签（具体用户或 root-token，0020 RBAC）。
func (s *Server) writeAudit(c *gin.Context, action, target, detail string) {
	actor := "admin"
	if v, ok := c.Get("actor_label"); ok {
		if label, ok := v.(string); ok && label != "" {
			actor = label
		}
	}
	_ = s.db.Create(&AuditLog{
		Actor:     actor,
		Action:    action,
		Target:    target,
		Detail:    detail,
		IP:        c.ClientIP(),
		CreatedAt: s.now().UnixMilli(),
	}).Error
}

// adminListAuditLogs：GET /api/admin/audit-logs?page=&action= —— 分页倒序。
func (s *Server) adminListAuditLogs(c *gin.Context) {
	page := 1
	if n, err := strconv.Atoi(c.Query("page")); err == nil && n > 0 {
		page = n
	}
	const pageSize = 30
	q := s.db.Model(&AuditLog{})
	if a := c.Query("action"); a != "" {
		q = q.Where("action = ?", a)
	}
	var total int64
	q.Count(&total)
	var rows []AuditLog
	q.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows)
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "page_size": pageSize, "items": rows})
}
