// Package leader：Redis SETNX+TTL 续租的 leader 租约（WS-3 多实例）。
//
// 让 cmd/core 的后台 loop（task 轮询 / 日志保留 / stuck-hold sweep / ledger drift / topup reconcile）
// 只在 leader 实例跑，省掉多副本重复的上游 API 调用与重复对账日志。
//
// 🚨 这是「优化」非「正确性依赖」：所有改钱/改状态路径的正确性已下沉到 DB（FOR UPDATE/条件
// UPDATE 行锁 + CAS WHERE status + ledger 幂等键 topup:/task:/affiliate:），双 leader 脑裂或租约
// 逻辑 bug 也绝不会重复入账或破坏 A/B 恒等式。故租约一律 **fail-open**：拿不准/Redis 抖动/未配
// Redis 时一律视为 leader（退化为多副本并跑，仍正确）——绝不能因租约故障导致 job 永不跑。
package leader

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Store：租约底层存储抽象（便于测试注入，不依赖 miniredis）。
// acquireOrRenew 原子地"我持有则续期 / 空闲则抢占 / 他人持有则失败"，返回是否由本实例持有。
type Store interface {
	acquireOrRenew(ctx context.Context, key, token string, ttl time.Duration) (held bool, err error)
}

type Lease struct {
	store Store
	key   string
	token string
	ttl   time.Duration
	log   *slog.Logger

	mu   sync.RWMutex
	held bool
}

// New 构造租约。store=nil（未配 Redis / 单实例）→ 永远是 leader。token 必须每实例唯一。
func New(store Store, key, token string, ttl time.Duration, log *slog.Logger) *Lease {
	if log == nil {
		log = slog.Default()
	}
	l := &Lease{store: store, key: key, token: token, ttl: ttl, log: log}
	if store == nil {
		l.held = true // 单实例：恒为 leader
	}
	return l
}

// IsLeader 返回当前实例是否应执行 leader-only 工作。
// store=nil → 恒 true。否则反映最近一次续租结果（fail-open：刷新出错时保持上次/抢占语义见 refresh）。
func (l *Lease) IsLeader() bool {
	if l.store == nil {
		return true
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.held
}

// refresh 执行一次 acquireOrRenew，更新 held。Redis 出错 → fail-open 置 held=true（宁可多跑不可不跑）。
func (l *Lease) refresh(ctx context.Context) {
	if l.store == nil {
		return
	}
	held, err := l.store.acquireOrRenew(ctx, l.key, l.token, l.ttl)
	if err != nil {
		l.log.Warn("leader lease refresh error, failing open (run as leader)", "key", l.key, "err", err)
		held = true // fail-open：故障期退化为本实例也跑（DB 幂等兜正确性）
	}
	l.mu.Lock()
	wasHeld := l.held
	l.held = held
	l.mu.Unlock()
	if held && !wasHeld {
		l.log.Info("acquired leader lease", "key", l.key)
	} else if !held && wasHeld {
		l.log.Info("lost leader lease (another instance leads)", "key", l.key)
	}
}

// Run 启动续租循环（阻塞到 ctx 取消）。续租间隔 < ttl/2，确保 leader 不因单次抖动丢租约。
// 调用方通常 `go lease.Run(ctx)`。store=nil 时立即返回（无需续租）。
func (l *Lease) Run(ctx context.Context) {
	if l.store == nil {
		return
	}
	l.refresh(ctx) // 启动即尝试抢占，避免首个间隔的空窗
	interval := l.ttl / 3
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.refresh(ctx)
		}
	}
}
