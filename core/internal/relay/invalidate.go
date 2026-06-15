package relay

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

// 跨实例缓存失效（WS-2 多实例一致性）：admin 改价/改渠道/改倍率、或 relay 自动禁用渠道时，
// 本进程同步失效本地缓存 + 向 Redis 发布一条失效消息；所有 core 实例订阅同一 channel 并失效。
// TTL 是兜底——即使没配 Redis（单实例）或 Redis 抖动，本地失效仍即时生效、跨实例最多 stale 一个 TTL。

const cacheInvalidateChannel = "laiai:cache:invalidate"

const (
	KindAll     = "all"     // 全部失效
	KindPrice   = "price"   // key = model（空 = 全部价目）
	KindRatio   = "ratio"   // 倍率（key 忽略，整表清——改 group/ratio 影响面广）
	KindChannel = "channel" // 渠道快照（key 忽略）
)

type invMsg struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// applyInvalidate 只失效本进程缓存（订阅者收到消息时调，绝不再发布——防自激环）。
func (r *Relay) applyInvalidate(kind, key string) {
	switch kind {
	case KindPrice:
		if key == "" {
			r.prices.invalidateAll()
		} else {
			r.prices.invalidate(key)
		}
	case KindRatio:
		r.ratios.invalidateAll()
	case KindChannel:
		r.channels.invalidate()
	case KindAll:
		r.prices.invalidateAll()
		r.ratios.invalidateAll()
		r.channels.invalidate()
	}
}

// broadcastInvalidate 本地同步失效 + 跨实例发布。供 relay 内部（自动禁用）与 server admin 写后调用。
// 单实例无 Redis：本地失效即时生效（修掉"改价要等一个 TTL"的回归）；多实例：发布让其它实例失效
// （本进程会收到自己的发布而重复 apply，幂等无害）。发布失败仅靠 TTL 兜底，绝不阻塞调用方。
func (r *Relay) broadcastInvalidate(kind, key string) {
	r.applyInvalidate(kind, key) // 本地即时（不依赖 Redis）
	if r.invRedis == nil {
		return
	}
	b, _ := json.Marshal(invMsg{Kind: kind, Key: key})
	if err := r.invRedis.Publish(context.Background(), cacheInvalidateChannel, b).Err(); err != nil {
		r.Log.Warn("cache invalidate publish failed (TTL fallback)", "kind", kind, "err", err)
	}
}

// SubscribeInvalidations 接入 Redis 跨实例失效（cmd/core 在配置 Redis 时调用）。
// 起一个订阅 goroutine，收到消息只 applyInvalidate（不再发布）。Redis 不可用不 panic（TTL 兜底）。
func (r *Relay) SubscribeInvalidations(cli *redis.Client) {
	if cli == nil {
		return
	}
	r.invRedis = cli
	r.invStop = make(chan struct{})
	sub := cli.Subscribe(context.Background(), cacheInvalidateChannel)
	go func() {
		ch := sub.Channel()
		for {
			select {
			case <-r.invStop:
				_ = sub.Close()
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				var msg invMsg
				if json.Unmarshal([]byte(m.Payload), &msg) != nil {
					continue
				}
				r.applyInvalidate(msg.Kind, msg.Key)
			}
		}
	}()
}

// StopInvalidations 优雅停机时关闭订阅 goroutine（可选）。
func (r *Relay) StopInvalidations() {
	if r.invStop != nil {
		close(r.invStop)
		r.invStop = nil
	}
}

// InvalidateCache 供 server admin 包在写后调用（本地 + 跨实例广播）。
func (r *Relay) InvalidateCache(kind, key string) { r.broadcastInvalidate(kind, key) }
