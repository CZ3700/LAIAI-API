package relay

// (model×channel) 统计底座（设计 §4.4 策略化选路 A3；指令 §5 / roadmap A2+A3）。
//
// 近 1h invocations 的每 (model, channel) 聚合：calls / 成功率 / p50 / p95 / avg_frt。
// 形态：进程内缓存 + 60s TTL 惰性刷新——单条 SQL 拉窗口内最近 chanStatsMaxRows 行采样，
// 内存聚合。零迁移、零热路径查询（TTL 内纯 map 读）；多副本各自聚合（本地视角，
// 与健康分 EWMA 同哲学）。刷新失败 fail-open：保留旧缓存并推进时钟（绝不打爆 DB、
// 绝不让策略路径产生新的失败模式——无数据时策略自动回落 order）。

import (
	"sort"
	"time"
)

const (
	chanStatsTTL       = 60 * time.Second
	chanStatsWindow    = time.Hour
	chanStatsMaxRows   = 20000 // 采样上限（与 usagePerfMaxRows 同哲学：够统计、不拖库）
	chanStatsMinSample = 10    // 低于此样本视为无数据：策略排序时排后并退回加权随机
)

// ChanStat：单 (model, channel) 的窗口聚合。
type ChanStat struct {
	Calls     int
	Succeeded int
	P50Ms     int
	P95Ms     int
	AvgFRTMs  int
}

// SuccessRate ∈ [0,1]；无调用返回 0。
func (s *ChanStat) SuccessRate() float64 {
	if s == nil || s.Calls == 0 {
		return 0
	}
	return float64(s.Succeeded) / float64(s.Calls)
}

// Sampled 返回样本量是否足以参与策略排序（<10 视为冷渠道无数据）。
func (s *ChanStat) Sampled() bool { return s != nil && s.Calls >= chanStatsMinSample }

// channelStats：读取聚合缓存（TTL 内直接返回；过期则刷新）。返回 map 只读，绝不原地改。
func (r *Relay) channelStats() map[string]map[uint64]*ChanStat {
	now := r.Now()
	r.chanStatsMu.Lock()
	if now.Sub(r.chanStatsAt) < chanStatsTTL && r.chanStatsMap != nil {
		m := r.chanStatsMap
		r.chanStatsMu.Unlock()
		return m
	}
	// 先推进时间戳再锁外刷新：并发请求 TTL 内只有一个真正刷库，其余拿旧缓存
	r.chanStatsAt = now
	old := r.chanStatsMap
	r.chanStatsMu.Unlock()

	fresh, err := aggregateChanStats(r, now)
	if err != nil {
		r.Log.Warn("chan stats refresh failed, keeping stale", "err", err)
		return old // fail-open：旧缓存（可能 nil→策略回落 order）
	}
	r.chanStatsMu.Lock()
	r.chanStatsMap = fresh
	r.chanStatsMu.Unlock()
	return fresh
}

// statFor：单点查询（策略排序用）。缓存未建/无该组合 → nil（调用方按无数据处置）。
func (r *Relay) statFor(stats map[string]map[uint64]*ChanStat, model string, channelID uint64) *ChanStat {
	if stats == nil {
		return nil
	}
	if byCh, ok := stats[model]; ok {
		return byCh[channelID]
	}
	return nil
}

// aggregateChanStats：拉窗口采样行并聚合。独立函数便于测试（传入伪造 Relay.DB）。
func aggregateChanStats(r *Relay, now time.Time) (map[string]map[uint64]*ChanStat, error) {
	var rows []struct {
		Model      string `gorm:"column:model"`
		ChannelID  uint64 `gorm:"column:channel_id"`
		Status     string `gorm:"column:status"`
		DurationMs int    `gorm:"column:duration_ms"`
		FRTMs      int    `gorm:"column:frt_ms"`
	}
	since := now.Add(-chanStatsWindow)
	if err := r.DB.Table("invocations").
		Select("model, channel_id, status, duration_ms, frt_ms").
		Where("created_at >= ? AND channel_id > 0", since).
		Order("id DESC").Limit(chanStatsMaxRows).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	type acc struct {
		stat ChanStat
		durs []int
		frt  int64
	}
	accs := map[string]map[uint64]*acc{}
	for i := range rows {
		row := &rows[i]
		byCh := accs[row.Model]
		if byCh == nil {
			byCh = map[uint64]*acc{}
			accs[row.Model] = byCh
		}
		a := byCh[row.ChannelID]
		if a == nil {
			a = &acc{}
			byCh[row.ChannelID] = a
		}
		a.stat.Calls++
		if row.Status == "success" {
			a.stat.Succeeded++
			// 分位只算成功请求（失败的时延是错误路径的时延，混入会污染 latency 排序）
			if row.DurationMs > 0 {
				a.durs = append(a.durs, row.DurationMs)
			}
			a.frt += int64(row.FRTMs)
		}
	}
	out := make(map[string]map[uint64]*ChanStat, len(accs))
	for model, byCh := range accs {
		m := make(map[uint64]*ChanStat, len(byCh))
		for chID, a := range byCh {
			if n := len(a.durs); n > 0 {
				sort.Ints(a.durs)
				a.stat.P50Ms = a.durs[pctIdx(50, n)]
				a.stat.P95Ms = a.durs[pctIdx(95, n)]
			}
			if a.stat.Succeeded > 0 {
				a.stat.AvgFRTMs = int(a.frt / int64(a.stat.Succeeded))
			}
			s := a.stat
			m[chID] = &s
		}
		out[model] = m
	}
	return out, nil
}

// pctIdx：与 server.percentiles 同口径（idx = ceil(p/100*n)-1，封顶 [0, n-1]）。
func pctIdx(p, n int) int {
	idx := (p*n + 99) / 100 // ceil(p*n/100)
	if idx > 0 {
		idx--
	}
	if idx >= n {
		idx = n - 1
	}
	return idx
}
