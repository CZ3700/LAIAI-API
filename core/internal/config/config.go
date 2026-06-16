// Package config：env-only 配置加载（设计 §4 移植旧平台 MustLoad 模式）。
// 红线（禁止移植黑名单 #8 的反向）：关键密钥缺失 = 拒绝启动，绝不静默生成随机值。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/laiai/laiai-next/core/internal/crypto"
)

type Config struct {
	DSN           string          // LAIAI_DSN，必填
	ListenAddr    string          // LAIAI_LISTEN_ADDR，默认 :8080
	AuthSecret    []byte          // LAIAI_AUTH_SECRET，必填且 >= 32 字符（session token HMAC）
	AdminToken    []byte          // LAIAI_ADMIN_TOKEN，可选；设置则须 >= 32 字符，未设置 admin API 整组禁用
	Keyring       *crypto.Keyring // LAIAI_DATA_KEYS + LAIAI_DATA_KEY_ACTIVE，可选；未设置则渠道管理/relay 禁用
	RedisAddr     string          // LAIAI_REDIS_ADDR，可选（跨实例限流需要；空=退化进程内限流）
	RedisPassword string          // LAIAI_REDIS_PASSWORD，可选（compose 的 redis 启用 requirepass）
	// 规模化（读副本 + 连接池可调，扩容旋钮）：
	// LAIAI_DSN_REPLICAS：逗号分隔的只读副本 DSN，空=无副本（所有读走主库，行为不变）。
	//   🚨 只把分析/用量历史/排行/公开价目这类「非金钱、容延迟」的只读路由到副本；余额/quota/hold
	//   等金钱判定读**永远走主库**（复制延迟会致超发）——见 server.readDB 的使用约束。
	ReplicaDSNs    []string // LAIAI_DSN_REPLICAS
	DBMaxOpenConns int      // LAIAI_DB_MAX_OPEN_CONNS，默认 50（每实例；扩容旋钮）
	DBMaxIdleConns int      // LAIAI_DB_MAX_IDLE_CONNS，默认 10
	// LAIAI_INVOCATION_RETENTION_DAYS：Invocation Store 保留天数（设计 §6.1 默认 30；0 = 关闭清理）
	InvocationRetentionDays int
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
	c.DBMaxOpenConns = getenvInt("LAIAI_DB_MAX_OPEN_CONNS", 50)
	c.DBMaxIdleConns = getenvInt("LAIAI_DB_MAX_IDLE_CONNS", 10)
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

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
