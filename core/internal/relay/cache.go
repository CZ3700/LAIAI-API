package relay

import (
	"sync"
	"time"
)

// 进程内 TTL 缓存（WS-2 热读缓存）：把 ratelimit.go 的单值 5s-TTL 范式推广为按 key 的并发安全表。
// 只缓存"极少变、读极热"的运营配置（价目/倍率/活跃渠道快照），不缓存任何资金可变字段
// （used_amount/quota/balance 永远走 DB 原子写）。失效一致性：本地写后同步 invalidate + 跨实例
// Redis pub/sub（见 invalidate.go）；TTL 是兜底（Redis 挂也最多 stale 一个 TTL）。
//
// 🚨 clobber 竞态防御（对抗评审命中）：load 在锁外执行（不持锁查 DB，避免阻塞全表命中），
// 但若 load 期间发生 invalidate，则那次 load 的结果是"失效前的旧值"，绝不能写回缓存——
// 否则违反"失效即时生效"承诺。用单调 generation：load 前记 gen，写回时若 gen 已变（期间有过
// invalidate）则丢弃结果不缓存（调用方仍拿到本次 load 的值，但缓存留空，下次重新 load 拿最新）。

type ttlEntry[V any] struct {
	val V
	at  time.Time
}

type ttlCache[K comparable, V any] struct {
	mu       sync.Mutex
	ttl      time.Duration
	now      func() time.Time
	entries  map[K]ttlEntry[V]
	gen      uint64 // 每次 invalidate/invalidateAll 自增；getOrLoad 据此防 clobber
	lastEvic time.Time
}

func newTTLCache[K comparable, V any](ttl time.Duration, now func() time.Time) *ttlCache[K, V] {
	if now == nil {
		now = time.Now
	}
	return &ttlCache[K, V]{ttl: ttl, now: now, entries: map[K]ttlEntry[V]{}}
}

// getOrLoad 命中且未过期 → 返回缓存；否则锁外 load，再据 generation 决定是否写回。
// load 失败不缓存（错误绝不钉住一个 TTL）。nil 接收者（未经 New 构造的测试实例）退化为直接 load。
func (c *ttlCache[K, V]) getOrLoad(key K, load func() (V, error)) (V, error) {
	if c == nil {
		return load()
	}
	now := c.now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Sub(e.at) < c.ttl {
		v := e.val
		c.mu.Unlock()
		return v, nil
	}
	genAtStart := c.gen
	c.mu.Unlock()

	v, err := load()
	if err != nil {
		var zero V
		return zero, err
	}

	c.mu.Lock()
	if c.gen == genAtStart { // load 期间无 invalidate → 安全写回
		c.entries[key] = ttlEntry[V]{val: v, at: now}
		// 惰性驱逐：每 ttl×10 清一次过期项，防 map 无界增长（仿 ratelimit rateLastEvic）。
		if now.Sub(c.lastEvic) > c.ttl*10 {
			for k, e := range c.entries {
				if now.Sub(e.at) >= c.ttl {
					delete(c.entries, k)
				}
			}
			c.lastEvic = now
		}
	}
	c.mu.Unlock()
	return v, nil
}

func (c *ttlCache[K, V]) invalidate(key K) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, key)
	c.gen++
	c.mu.Unlock()
}

func (c *ttlCache[K, V]) invalidateAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = map[K]ttlEntry[V]{}
	c.gen++
	c.mu.Unlock()
}

// snapCache：无键单值快照（给 status=1 渠道全表用）。同 generation clobber 防御。
type snapCache[V any] struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	val  V
	at   time.Time
	have bool
	gen  uint64
}

func newSnapCache[V any](ttl time.Duration, now func() time.Time) *snapCache[V] {
	if now == nil {
		now = time.Now
	}
	return &snapCache[V]{ttl: ttl, now: now}
}

func (c *snapCache[V]) getOrLoad(load func() (V, error)) (V, error) {
	if c == nil {
		return load()
	}
	now := c.now()
	c.mu.Lock()
	if c.have && now.Sub(c.at) < c.ttl {
		v := c.val
		c.mu.Unlock()
		return v, nil
	}
	genAtStart := c.gen
	c.mu.Unlock()

	v, err := load()
	if err != nil {
		var zero V
		return zero, err
	}

	c.mu.Lock()
	if c.gen == genAtStart {
		c.val, c.at, c.have = v, now, true
	}
	c.mu.Unlock()
	return v, nil
}

func (c *snapCache[V]) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.have = false
	c.gen++
	c.mu.Unlock()
}
