package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// 认证端点限流（审查 H1）：进程内 per-IP token bucket。
// 单进程部署下足够；M2 引入 Redis 后可换分布式实现。
// 配额：每 IP 平均 1 次/秒、突发 5——足够正常重试，挡住暴力破解。
type ipLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*limiterEntry
	r        rate.Limit
	burst    int
	lastEvic time.Time
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(r rate.Limit, burst int) *ipLimiter {
	return &ipLimiter{buckets: map[string]*limiterEntry{}, r: r, burst: burst, lastEvic: time.Now()}
}

func (l *ipLimiter) allow(ip string, now time.Time) bool {
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
