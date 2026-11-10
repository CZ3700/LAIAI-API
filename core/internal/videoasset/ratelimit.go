package videoasset

import (
	"context"
	"sync"
	"time"
)

// assetsTick 是素材域后台作业（video_jobs "assets"）的名义节拍。逐 tick 配额
// 由官方限流间隔推导：quota = assetsTick / interval。
const assetsTick = 5 * time.Second

// officialActionRates 是火山私域素材库各接口的官方限流 → 单请求最小间隔
// （interval = 1/QPS；CreateAsset 按 QPM 300 → 200ms）。口径出处：火山控制台
// 《虚拟人/私域素材库指南（预览）》限流说明，2026-11-10 核实：
//
//	GetAsset 100 QPS；ListAssets/ListAssetGroups/GetAssetGroup/
//	UpdateAsset/UpdateAssetGroup/DeleteAsset 10 QPS；DeleteAssetGroup 5 QPS；
//	CreateAsset 最大 QPM 300。
//
// CreateVisualValidateSession / GetVisualValidateResult 官方未列出限流，
// 保守按 10 QPS 档，待官方联调核实后修正。官方口径变化时改本表发版。
var officialActionRates = map[string]time.Duration{
	"GetAsset":                    10 * time.Millisecond,
	"ListAssetGroups":             100 * time.Millisecond,
	"ListAssets":                  100 * time.Millisecond,
	"GetAssetGroup":               100 * time.Millisecond,
	"CreateAssetGroup":            100 * time.Millisecond, // 官方未单列，保守按 10 QPS 档
	"UpdateAsset":                 100 * time.Millisecond,
	"UpdateAssetGroup":            100 * time.Millisecond,
	"DeleteAsset":                 100 * time.Millisecond,
	"DeleteAssetGroup":            200 * time.Millisecond,
	"CreateAsset":                 200 * time.Millisecond,
	"CreateVisualValidateSession": 100 * time.Millisecond,
	"GetVisualValidateResult":     100 * time.Millisecond,
}

// assetActions 是素材域全部官方动作清单（完备性防呆）：派发循环按本清单
// 遍历而非直接 range 限流表，防止表漏键导致该动作的 op 永不派发
// （2026-11-11 曾因表漏 CreateAssetGroup 使虚拟组注册整链路停摆）。
var assetActions = []string{
	"CreateAssetGroup", "CreateAsset", "UpdateAsset", "UpdateAssetGroup",
	"DeleteAsset", "DeleteAssetGroup", "GetAsset", "GetAssetGroup",
	"ListAssets", "ListAssetGroups",
	"CreateVisualValidateSession", "GetVisualValidateResult",
}

const fallbackActionRate = 200 * time.Millisecond

func actionRate(action string) time.Duration {
	if interval, ok := officialActionRates[action]; ok {
		return interval
	}
	return fallbackActionRate
}

// tickQuota 是单 tick 内某 action 允许派发的官方调用上限，由官方限流间隔推导。
func tickQuota(action string) int {
	return int(assetsTick / actionRate(action))
}

// writeAction 判定官方调用是否写类：读写分池使官方吞吐承诺两侧都拿满
// （注册洪峰时查询确认不得挤占 CreateAsset 的 QPM 通道，反之亦然）。
func writeAction(action string) bool {
	switch action {
	case "CreateAsset", "CreateAssetGroup", "UpdateAsset", "UpdateAssetGroup",
		"DeleteAsset", "DeleteAssetGroup", "CreateVisualValidateSession":
		return true
	}
	return false
}

// rateLimiter 把同 action 的官方调用平滑到单请求间隔（全局节拍，锁内记账、
// 锁外等待）。等待尊重 ctx 取消，与 mainJobs 的优雅关闭联动。
type rateLimiter struct {
	mu   sync.Mutex
	next map[string]time.Time
	now  func() time.Time
}

func newRateLimiter(now func() time.Time) *rateLimiter {
	return &rateLimiter{next: map[string]time.Time{}, now: now}
}

// reserve 返回本次调用需要等待的时长（0=立即可发），并把全局节拍推进一格。
// 并发调用同 action 时按锁序获得连续槽位：第 i 个等待 i×interval，
// 从而把一批调用摊成均匀间隔而非瞬时突发（QPS 是每秒滑动窗口）。
func (l *rateLimiter) reserve(action string) time.Duration {
	interval := actionRate(action)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	start := l.next[action]
	if start.Before(now) {
		start = now
	}
	l.next[action] = start.Add(interval)
	return start.Sub(now)
}

// sleepCtx 等待 delay 或 ctx 取消。
func sleepCtx(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
