// Package config：env-only 配置加载（设计 §4 移植旧平台 MustLoad 模式）。
// 红线（禁止移植黑名单 #8 的反向）：关键密钥缺失 = 拒绝启动，绝不静默生成随机值。
package config

import (
	"fmt"
	"os"

	"github.com/laiai/laiai-next/core/internal/crypto"
)

type Config struct {
	DSN        string          // LAIAI_DSN，必填
	ListenAddr string          // LAIAI_LISTEN_ADDR，默认 :8080
	AuthSecret []byte          // LAIAI_AUTH_SECRET，必填且 >= 32 字符（session token HMAC）
	AdminToken []byte          // LAIAI_ADMIN_TOKEN，可选；设置则须 >= 32 字符，未设置 admin API 整组禁用
	Keyring    *crypto.Keyring // LAIAI_DATA_KEYS + LAIAI_DATA_KEY_ACTIVE，可选；未设置则渠道管理/relay 禁用
	RedisAddr  string          // LAIAI_REDIS_ADDR，可选（M2 起 asynq/限流需要）
}

// Load 读取并校验配置；任何关键项非法返回 error（调用方应直接退出）。
func Load() (*Config, error) {
	c := &Config{
		DSN:        os.Getenv("LAIAI_DSN"),
		ListenAddr: getenvDefault("LAIAI_LISTEN_ADDR", ":8080"),
		RedisAddr:  os.Getenv("LAIAI_REDIS_ADDR"),
	}
	if c.DSN == "" {
		return nil, fmt.Errorf("config: LAIAI_DSN is required")
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
	return c, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
