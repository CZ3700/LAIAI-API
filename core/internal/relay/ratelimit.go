package relay

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// 模型级限流（gap #2，对标 NewAPI ModelRequestRateLimit）：每 (org, model) 固定 60s 窗口计数。
// 接入 Redis（rate 非 nil）→ 跨实例原子计数（设计 §4.4 限流红线）；未配 Redis → 退化进程内。
// 策略从 ext_settings 读，5s 缓存避免每请求打库。0/关闭 = 不限。
// **fail-open**：Redis 任何错误都放行（限流是保护、不是收费闸；基础设施抖动绝不挡付费流量）。

// rateCounter：跨实例固定窗口计数器抽象（便于测试注入）。incr 在 key 上原子自增并设过期，
// 返回自增后的计数。
type rateCounter interface {
	incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
}

// redisRateCounter：go-redis 实现。INCR + EXPIRE（key 含窗口纪元，过期仅为清理）。
type redisRateCounter struct{ cli *redis.Client }

func (c redisRateCounter) incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := c.cli.Pipeline()
	n := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return n.Val(), nil
}

// UseRedisRateLimiter 接入 Redis 跨实例限流（cmd/core 在 LAIAI_REDIS_ADDR 配置时调用）。
func (r *Relay) UseRedisRateLimiter(cli *redis.Client) {
	if cli != nil {
		r.rate = redisRateCounter{cli: cli}
	}
}

type rateWindow struct {
	start time.Time
	count int
}

// modelRatePolicy：返回 (是否启用, 每分钟上限)。带 5s 缓存。
func (r *Relay) modelRatePolicy() (bool, int) {
	now := r.Now()
	r.ratePolMu.Lock()
	if now.Sub(r.ratePolAt) < 5*time.Second {
		en, rpm := r.rateEnabled, r.rateRPM
		r.ratePolMu.Unlock()
		return en, rpm
	}
	r.ratePolMu.Unlock()

	enabled, rpm := false, 0
	var rows []struct{ Name, Value string }
	r.DB.Table("ext_settings").Where("name IN ?",
		[]string{"model_rate_limit_enabled", "model_rate_limit_rpm"}).Find(&rows)
	for _, row := range rows {
		switch row.Name {
		case "model_rate_limit_enabled":
			enabled = row.Value == "true"
		case "model_rate_limit_rpm":
			if n, err := strconv.Atoi(row.Value); err == nil && n > 0 {
				rpm = n
			}
		}
	}
	r.ratePolMu.Lock()
	r.rateEnabled, r.rateRPM, r.ratePolAt = enabled, rpm, now
	r.ratePolMu.Unlock()
	return enabled, rpm
}

// allowModelRate：(org, model) 在当前 60s 窗口内是否还可放行。未启用/RPM=0 一律放行。
func (r *Relay) allowModelRate(orgID uint64, model string) (ok bool, rpm int) {
	enabled, limit := r.modelRatePolicy()
	if !enabled || limit <= 0 {
		return true, 0
	}
	now := r.Now()

	// 跨实例路径：Redis 固定窗口（key 含窗口纪元，天然每分钟重置）。
	if r.rate != nil {
		window := now.Unix() / 60
		key := fmt.Sprintf("rl:m:%d:%s:%d", orgID, model, window)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		n, err := r.rate.incr(ctx, key, 2*time.Minute) // 2m 过期 > 窗口，安全清理
		if err != nil {
			// fail-open：Redis 抖动绝不挡流量（P2-2 用户体验红线），放行并告警
			r.Log.Warn("rate limiter redis error, failing open", "org", orgID, "model", model, "err", err)
			return true, limit
		}
		return n <= int64(limit), limit
	}

	// 单实例退化路径：进程内固定窗口
	key := fmt.Sprintf("%d:%s", orgID, model)
	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	// 惰性驱逐：每 5 分钟清掉 2 分钟前的旧窗口，防 map 无界增长
	if now.Sub(r.rateLastEvic) > 5*time.Minute {
		for k, w := range r.rateWindows {
			if now.Sub(w.start) > 2*time.Minute {
				delete(r.rateWindows, k)
			}
		}
		r.rateLastEvic = now
	}
	w := r.rateWindows[key]
	if w == nil || now.Sub(w.start) >= time.Minute {
		r.rateWindows[key] = &rateWindow{start: now, count: 1}
		return true, limit
	}
	if w.count >= limit {
		return false, limit
	}
	w.count++
	return true, limit
}
