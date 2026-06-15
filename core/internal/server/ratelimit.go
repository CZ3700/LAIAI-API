package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// 认证端点限流（审查 H1）：per-IP 防暴力破解。
// 多实例（WS-3）：配 Redis 时走跨实例固定窗口（否则 N 副本各一份桶 → 有效阈值被实例数稀释 N 倍，
// 这是唯一带安全语义的多实例缺口）。未配 Redis / Redis 出错 → 退化进程内 token bucket（仍有保护，
// 不 fail-open 全放）。配额：每 IP 平均 1 次/秒、突发 burst（默认 5）。
type ipLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*limiterEntry
	r        rate.Limit
	burst    int
	lastEvic time.Time

	rdb       *redis.Client // 跨实例固定窗口；nil/出错 → 退化进程内桶
	windowSec int           // Redis 固定窗口秒数（= burst，使 limit/window ≈ 1/s 与桶语义一致）
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(r rate.Limit, burst int) *ipLimiter {
	return &ipLimiter{buckets: map[string]*limiterEntry{}, r: r, burst: burst, lastEvic: time.Now()}
}

// useRedis 接入跨实例限流（多实例部署）。窗口 = burst 秒、窗口内上限 = burst，
// 等效 1/s 平均 + burst 突发（与进程内 token bucket 同量级），跨副本聚合计数。
func (l *ipLimiter) useRedis(cli *redis.Client) {
	if cli != nil {
		l.rdb = cli
		l.windowSec = l.burst
		if l.windowSec < 1 {
			l.windowSec = 1
		}
	}
}

func (l *ipLimiter) allow(ip string, now time.Time) bool {
	// 跨实例路径：固定窗口（key 含窗口纪元，过期即清）。Redis 出错退化进程内桶（不放空门）。
	if l.rdb != nil {
		win := now.Unix() / int64(l.windowSec)
		key := fmt.Sprintf("authrl:%s:%d", ip, win)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		pipe := l.rdb.Pipeline()
		n := pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, time.Duration(l.windowSec*2)*time.Second)
		if _, err := pipe.Exec(ctx); err == nil {
			return n.Val() <= int64(l.burst)
		}
		// Redis 抖动：退化到进程内桶（仍挡暴力破解，不像业务限流那样 fail-open）
	}
	return l.allowLocal(ip, now)
}

func (l *ipLimiter) allowLocal(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 惰性驱逐：每 10 分钟清掉 30 分钟未见的 IP，防 map 无界增长
	if now.Sub(l.lastEvic) > 10*time.Minute {
		for k, e := range l.buckets {
			if now.Sub(e.seen) > 30*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastEvic = now
	}
	e := l.buckets[ip]
	if e == nil {
		e = &limiterEntry{lim: rate.NewLimiter(l.r, l.burst)}
		l.buckets[ip] = e
	}
	e.seen = now
	return e.lim.Allow()
}

// authRateLimit 中间件：超限返回 429。用 gin ClientIP（信任 Caddy 设置的 X-Forwarded-For，
// 见 Caddyfile trusted_proxies 说明）。
func (s *Server) authRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.authLimiter.allow(c.ClientIP(), s.now()) {
			c.Header("Retry-After", "1")
			fail(c, http.StatusTooManyRequests, "too many requests, slow down")
			return
		}
		c.Next()
	}
}

// bodyLimit 中间件：控制面请求体上限（审查 M2，防内存放大 DoS）。
func bodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
