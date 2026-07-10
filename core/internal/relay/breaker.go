package relay

// 渠道熔断器（顶级审计修 2026-07 排名 2）：连续可靠性失败（网络不可达/5xx）达阈值 →
// 熔断 open，选路硬摘除（此前只有健康分降权 floor 0.1，死渠道长期仍吃 ~10% 流量、
// 每请求先在它身上烧一次重试）。冷却期满转半开：放行**一个**真实请求探活——成功即闭合
// 恢复全量，失败重新 open。与 auto-disable（运营开关，鉴权/配额类硬故障禁 key/渠道，
// 默认关=人工管理）职责互补：熔断是**自愈的临时**摘除，不落库、重启清零，不违背
// "渠道由人工管理"的运营约定。
//
// 降级保证：若某模型的全部候选渠道都被熔断挡住，选路会清除熔断排除并绕过熔断继续
// （breakerPicker.reopen）——熔断绝不把可用性降到比无熔断更差。
//
// 判定口径（与审计一致，只认渠道自身可靠性）：
//   - 失败：网络不可达（Do 错误，含客户端超时）、上游 5xx（含 529/overloaded）
//   - 成功：收到任何 <500 的上游响应（400/401/429 说明渠道活着，是分类器的事）

import "time"

const (
	breakerThreshold   = 5                // 连续可靠性失败 N 次 → open
	breakerCooldown    = 30 * time.Second // open → 半开 的冷却
	breakerProbeExpiry = 60 * time.Second // 半开探活无结果视为丢失，允许再探（防探活名额泄漏）
)

type breakerState struct {
	fails     int       // 连续可靠性失败计数（closed 态累加）
	openUntil time.Time // 零值=closed；未来=open；过去=可转半开
	probing   bool      // 半开探活名额已被占用
	probeAt   time.Time // 探活认领时间（超 breakerProbeExpiry 视为丢失）
}

// breakerAllow：选路前询问渠道是否放行。closed → true；open 冷却中 → false；
// 冷却期满 → 认领半开探活名额（同一时刻只放一个），已被认领 → false。
func (r *Relay) breakerAllow(channelID uint64) bool {
	now := r.Now()
	r.breakerMu.Lock()
	defer r.breakerMu.Unlock()
	st, ok := r.breakerStates[channelID]
	if !ok || st.openUntil.IsZero() {
		return true // closed（或从未失败）
	}
	if now.Before(st.openUntil) {
		return false // open 冷却中
	}
	// 半开：认领探活名额
	if st.probing && now.Sub(st.probeAt) < breakerProbeExpiry {
		return false // 名额已被在途探活占用
	}
	st.probing, st.probeAt = true, now
	return true
}

// breakerNote：喂一次结果。ok=true（收到 <500 响应）→ 闭合清零；
// ok=false（不可达/5xx）→ 计数，达阈值或半开探活失败 → open 进入/重回冷却。
func (r *Relay) breakerNote(channelID uint64, ok bool) {
	now := r.Now()
	r.breakerMu.Lock()
	defer r.breakerMu.Unlock()
	if r.breakerStates == nil {
		r.breakerStates = map[uint64]*breakerState{} // 懒初始化（兼容不经 New 构造的测试实例）
	}
	if ok {
		delete(r.breakerStates, channelID) // 任何一次成功 → 全清（closed）
		return
	}
	st := r.breakerStates[channelID]
	if st == nil {
		st = &breakerState{}
		r.breakerStates[channelID] = st
	}
	if !st.openUntil.IsZero() && !now.Before(st.openUntil.Add(-breakerCooldown)) {
		// open/半开期间的失败（探活失败或残余在途请求失败）→ 重回冷却
		st.openUntil, st.probing = now.Add(breakerCooldown), false
		return
	}
	st.fails++
	if st.fails >= breakerThreshold {
		st.openUntil, st.probing = now.Add(breakerCooldown), false
	}
}

// breakerPicker：换渠道重试循环里的熔断闸封装（chat 主循环与 forwardWithFailover 共用）。
// admit 拒绝时把渠道记入 exclude（调用方 attempt-- 后 continue，不烧重试预算）；
// 当 pick 因排除耗尽失败且确有渠道是被熔断挡住的，reopen 清除这些排除并转入 bypass
// （之后 admit 恒放行）——见文件头"降级保证"。
type breakerPicker struct {
	r       *Relay
	bypass  bool
	skipped []uint64
}

func (r *Relay) newBreakerPicker() *breakerPicker { return &breakerPicker{r: r} }

// admit：ch 放行返回 true；被熔断挡住则记入 exclude 并返回 false。
func (p *breakerPicker) admit(channelID uint64, exclude map[uint64]bool) bool {
	if p.bypass || p.r.breakerAllow(channelID) {
		return true
	}
	exclude[channelID] = true
	p.skipped = append(p.skipped, channelID)
	return false
}

// reopen：pick 失败时的降级出口。曾有渠道被熔断挡住 → 清除它们的排除、转 bypass，
// 返回 true（调用方重试一次 pick）；否则 false（真的无渠道）。
func (p *breakerPicker) reopen(exclude map[uint64]bool) bool {
	if len(p.skipped) == 0 {
		return false
	}
	for _, id := range p.skipped {
		delete(exclude, id)
	}
	p.skipped = nil
	p.bypass = true
	p.r.Log.Warn("all candidate channels circuit-open — bypassing breaker (degraded)")
	return true
}
