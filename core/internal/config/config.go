// Package config：env-only 配置加载（设计 §4 移植旧平台 MustLoad 模式）。
// 红线（禁止移植黑名单 #8 的反向）：关键密钥缺失 = 拒绝启动，绝不静默生成随机值。
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/laiai/laiai-next/core/internal/crypto"
)

type Config struct {
	DSN                       string          // LAIAI_DSN，必填
	ListenAddr                string          // LAIAI_LISTEN_ADDR，默认 :8080
	AuthSecret                []byte          // LAIAI_AUTH_SECRET，必填且 >= 32 字符（session token HMAC）
	AdminToken                []byte          // LAIAI_ADMIN_TOKEN，可选；设置则须 >= 32 字符，未设置 admin API 整组禁用
	Keyring                   *crypto.Keyring // LAIAI_DATA_KEYS + LAIAI_DATA_KEY_ACTIVE，可选；未设置则渠道管理/relay 禁用
	VideoHMACSecret           []byte
	VideoResponseInboxEnabled bool // LAIAI_VIDEO_RESPONSE_INBOX, default false; only controls new attempt registration
	VideoPortraitCallbackURL  string
	VideoPortraitReturnURL    string
	VideoMediaRoot            string
	VideoMediaPublicBaseURL   string
	VideoMediaSigningKey      []byte
	VideoMediaFFProbePath     string
	VideoMediaScanCommand     []string
	RedisAddr                 string // LAIAI_REDIS_ADDR，可选（跨实例限流需要；空=退化进程内限流）
	RedisPassword             string // LAIAI_REDIS_PASSWORD，可选（compose 的 redis 启用 requirepass）
	// 规模化（读副本 + 连接池可调，扩容旋钮）：
	// LAIAI_DSN_REPLICAS：逗号分隔的只读副本 DSN，空=无副本（所有读走主库，行为不变）。
	//   🚨 只把分析/用量历史/排行/公开价目这类「非金钱、容延迟」的只读路由到副本；余额/quota/hold
	//   等金钱判定读**永远走主库**（复制延迟会致超发）——见 server.readDB 的使用约束。
	ReplicaDSNs    []string // LAIAI_DSN_REPLICAS
	DBMaxOpenConns int      // LAIAI_DB_MAX_OPEN_CONNS，默认 50（每实例；扩容旋钮）
	DBMaxIdleConns int      // LAIAI_DB_MAX_IDLE_CONNS，默认 10
	// LAIAI_INVOCATION_RETENTION_DAYS：Invocation Store 保留天数（设计 §6.1 默认 30；0 = 关闭清理）
	InvocationRetentionDays int
	// LAIAI_TRUSTED_PROXIES（指令 P1-2 生产边缘 IP 信任链）：逗号分隔的可信反代 CIDR/IP。
	// 未设置 = 私网段默认（容器拓扑：core 仅经同网 Caddy 可达）；"none" = 不信任任何代理头
	// （core 直接对公网时用，X-Forwarded-For 一律忽略）。ClientIP 是 keyGate IP 白名单 /
	// 认证限流 / 审计 IP 的共同口径，信任链错配 = 三者同时失真。
	TrustedProxies []string
	// SMTP（0014，可选）：全空 = 邮件退化为日志，不真实发送
	SMTPHost     string // LAIAI_SMTP_HOST
	SMTPPort     int    // LAIAI_SMTP_PORT，默认 587
	SMTPUsername string // LAIAI_SMTP_USERNAME
	SMTPPassword string // LAIAI_SMTP_PASSWORD
	SMTPFrom     string // LAIAI_SMTP_FROM，默认取 username
}

// Load 读取并校验配置；任何关键项非法返回 error（调用方应直接退出）。
func Load() (*Config, error) {
	c := &Config{
		DSN:           os.Getenv("LAIAI_DSN"),
		ListenAddr:    getenvDefault("LAIAI_LISTEN_ADDR", ":8080"),
		RedisAddr:     os.Getenv("LAIAI_REDIS_ADDR"),
		RedisPassword: os.Getenv("LAIAI_REDIS_PASSWORD"),
	}
	if c.DSN == "" {
		return nil, fmt.Errorf("config: LAIAI_DSN is required")
	}
	// 读副本 + 连接池（规模化旋钮，扩容时只改 env）。
	for _, dsn := range strings.Split(os.Getenv("LAIAI_DSN_REPLICAS"), ",") {
		if s := strings.TrimSpace(dsn); s != "" {
			c.ReplicaDSNs = append(c.ReplicaDSNs, s)
		}
	}
	var perr error
	switch os.Getenv("LAIAI_VIDEO_RESPONSE_INBOX") {
	case "", "false":
		c.VideoResponseInboxEnabled = false
	case "true":
		c.VideoResponseInboxEnabled = true
	default:
		return nil, fmt.Errorf("config: LAIAI_VIDEO_RESPONSE_INBOX must be exactly true or false when set")
	}
	// open conns 最小 1：database/sql 中 0 = 无上限，高负载会打爆 MySQL max_connections（与旧默认相反）。
	if c.DBMaxOpenConns, perr = getenvInt("LAIAI_DB_MAX_OPEN_CONNS", 50, 1); perr != nil {
		return nil, perr
	}
	// idle conns 允许 0：显式设 0 可避免复用被 MySQL wait_timeout 杀掉的陈旧连接。
	if c.DBMaxIdleConns, perr = getenvInt("LAIAI_DB_MAX_IDLE_CONNS", 10, 0); perr != nil {
		return nil, perr
	}
	// MySQL DSN 必须带 parseTime=true：否则 DATETIME 列扫成 []byte，ledger/analytics 静默损坏。
	// 失败要响——宁可启动即报错，不要上线后资金流水悄悄读不出。
	if strings.Contains(c.DSN, "@tcp(") && !strings.Contains(c.DSN, "parseTime=true") {
		return nil, fmt.Errorf("config: MySQL LAIAI_DSN must include parseTime=true (DATETIME scanning; ledger/analytics break silently without it)")
	}
	secret := os.Getenv("LAIAI_AUTH_SECRET")
	if len(secret) < 32 {
		return nil, fmt.Errorf("config: LAIAI_AUTH_SECRET is required and must be >= 32 chars (refusing to auto-generate: tokens must survive restarts)")
	}
	c.AuthSecret = []byte(secret)

	if admin := os.Getenv("LAIAI_ADMIN_TOKEN"); admin != "" {
		if len(admin) < 32 {
			return nil, fmt.Errorf("config: LAIAI_ADMIN_TOKEN must be >= 32 chars when set")
		}
		c.AdminToken = []byte(admin)
	}

	c.InvocationRetentionDays = 30
	if v := os.Getenv("LAIAI_INVOCATION_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("config: LAIAI_INVOCATION_RETENTION_DAYS must be a non-negative integer")
		}
		c.InvocationRetentionDays = n
	}

	// 可信反代（P1-2）：启动即校验 CIDR/IP 合法性——信任链配错宁可启动失败，不要上线后
	// IP 白名单/限流/审计三处口径静默失真。"none" 原样传给 server 层解释（不信任任何代理头）。
	for _, p := range strings.Split(os.Getenv("LAIAI_TRUSTED_PROXIES"), ",") {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		if s != "none" {
			if _, _, err := net.ParseCIDR(s); err != nil && net.ParseIP(s) == nil {
				return nil, fmt.Errorf("config: LAIAI_TRUSTED_PROXIES entry %q is not a valid IP or CIDR", s)
			}
		}
		c.TrustedProxies = append(c.TrustedProxies, s)
	}
	// "none" 只能单独出现（对抗 review）：混填 "none,10.0.0.0/8" 语义矛盾，若放行会在运行期
	// 被 gin 当无效 CIDR 静默降级为「不信任任何代理头」——Caddy 拓扑下 IP 白名单全体 403。
	if len(c.TrustedProxies) > 1 {
		for _, p := range c.TrustedProxies {
			if p == "none" {
				return nil, fmt.Errorf("config: LAIAI_TRUSTED_PROXIES value \"none\" must be used alone, not mixed with CIDR entries")
			}
		}
	}

	if spec := os.Getenv("LAIAI_DATA_KEYS"); spec != "" {
		active := os.Getenv("LAIAI_DATA_KEY_ACTIVE")
		if active == "" {
			return nil, fmt.Errorf("config: LAIAI_DATA_KEY_ACTIVE is required when LAIAI_DATA_KEYS is set")
		}
		kr, err := crypto.NewKeyring(spec, active)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		c.Keyring = kr
	}

	c.SMTPHost = os.Getenv("LAIAI_SMTP_HOST")
	if v := os.Getenv("LAIAI_VIDEO_HMAC_SECRET"); v != "" {
		if len(v) < 32 || c.Keyring == nil {
			return nil, fmt.Errorf("config: video gateway requires a separate HMAC secret of 32+ bytes and the data keyring")
		}
		c.VideoHMACSecret = []byte(v)
	}
	c.VideoPortraitCallbackURL = os.Getenv("LAIAI_VIDEO_PORTRAIT_CALLBACK_URL")
	c.VideoPortraitReturnURL = os.Getenv("LAIAI_VIDEO_PORTRAIT_RETURN_URL")
	if err := c.loadVideoMedia(); err != nil {
		return nil, err
	}
	c.SMTPPort = 587
	if v := os.Getenv("LAIAI_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.SMTPPort = n
		}
	}
	c.SMTPUsername = os.Getenv("LAIAI_SMTP_USERNAME")
	c.SMTPPassword = os.Getenv("LAIAI_SMTP_PASSWORD")
	c.SMTPFrom = os.Getenv("LAIAI_SMTP_FROM")
	return c, nil
}

// Media upload is optional, but a partial configuration must never silently
// accept unscanned files or publish unsigned download URLs.
func (c *Config) loadVideoMedia() error {
	c.VideoMediaRoot = os.Getenv("LAIAI_VIDEO_MEDIA_ROOT")
	c.VideoMediaPublicBaseURL = os.Getenv("LAIAI_VIDEO_MEDIA_PUBLIC_BASE_URL")
	c.VideoMediaSigningKey = []byte(os.Getenv("LAIAI_VIDEO_MEDIA_SIGNING_KEY"))
	c.VideoMediaFFProbePath = os.Getenv("LAIAI_VIDEO_MEDIA_FFPROBE_PATH")
	scan := os.Getenv("LAIAI_VIDEO_MEDIA_SCAN_COMMAND")
	if c.VideoMediaRoot == "" && c.VideoMediaPublicBaseURL == "" && len(c.VideoMediaSigningKey) == 0 && c.VideoMediaFFProbePath == "" && scan == "" {
		return nil
	}
	u, err := url.Parse(c.VideoMediaPublicBaseURL)
	if len(c.VideoHMACSecret) == 0 || !filepath.IsAbs(c.VideoMediaRoot) || len(c.VideoMediaSigningKey) < 32 || c.VideoMediaFFProbePath == "" || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != "/api/video/media-source" {
		return fmt.Errorf("config: video media requires the video gateway, a private absolute root, signing key of 32+ bytes, ffprobe and an HTTPS /api/video/media-source base URL")
	}
	if err := json.Unmarshal([]byte(scan), &c.VideoMediaScanCommand); err != nil || len(c.VideoMediaScanCommand) == 0 || strings.TrimSpace(c.VideoMediaScanCommand[0]) == "" {
		return fmt.Errorf("config: LAIAI_VIDEO_MEDIA_SCAN_COMMAND must be a non-empty JSON array of executable and arguments")
	}
	for _, arg := range c.VideoMediaScanCommand {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("config: video media scanner arguments must not contain NUL")
		}
	}
	return nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getenvInt 解析整数 env（连接池旋钮）。区分「未设置」与「设了非法值」：未设置用 def；设了就必须是
// ≥min 的合法整数，否则 fail-loud 报错而非静默回退——与 RETENTION_DAYS 等必填项纪律一致，不让运维
// 误以为生效的配置被悄悄吞掉。min 让调用方区分「允许 0」(idle) 与「必须 ≥1」(open，0=无上限有害)。
func getenvInt(key string, def, min int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		return 0, fmt.Errorf("config: %s must be an integer >= %d", key, min)
	}
	return n, nil
}
