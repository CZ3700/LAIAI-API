package relay

// 策略化选路（Provider preference，设计 §4.4；指令 §5 / roadmap A2+A4，对标 OpenRouter
// provider preferences）。策略只改「priority 层内的选择方式」，绝不跨层（硬分层是运营语义）；
// 失败仍走既有 exclude 换渠道 + attempt 跨层降级——确定性排序下 exclude 自然使次优接棒。
//
// 策略语义：
//   order（默认/空） = 现状层内 (weight+smoothing)×健康分 加权随机，行为逐字不变
//   latency          = 层内按近 1h p50 升序确定性取最优
//   throughput       = 层内按成功率降序、p50 升序 tie-break
//   price            = 当前单一平台定价下渠道无差价 → 等价 order（枚举保留，BYOK/渠道差价时接真数据）
// 样本 <chanStatsMinSample 的渠道视为无数据：排后不排除；层内全无数据 → 回落加权随机
// （冷渠道不因无数据被饿死，统计缓存失败也绝不产生新的请求失败模式——fail-open）。

import (
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

// 路由策略枚举（api_keys.route_policy 与 X-LAIAI-Route 头共用）。空串 = order 默认路径。
const (
	RoutePolicyLatency    = "latency"
	RoutePolicyThroughput = "throughput"
)

// ValidRoutePolicy：写入侧白名单（keys API 校验用）。order/price 存原值（语义=默认路径）。
func ValidRoutePolicy(p string) bool {
	switch p {
	case "", "order", "latency", "throughput", "price":
		return true
	}
	return false
}

// resolveRoutePolicy：每请求策略求解——X-LAIAI-Route 头 > key 级默认 > 平台默认 order。
// 未知值静默回落 order（宽容向后兼容，设计 §4.4 注记）；order/price 归一为空串（默认路径）。
func resolveRoutePolicy(c *gin.Context, key *identity.APIKey) string {
	p := strings.ToLower(strings.TrimSpace(c.GetHeader("X-LAIAI-Route")))
	if p == "" {
		p = strings.ToLower(strings.TrimSpace(key.RoutePolicy))
	}
	switch p {
	case RoutePolicyLatency, RoutePolicyThroughput:
		return p
	}
	return "" // order/price/未知 → 默认加权随机路径
}

// pickSnapshotPolicy：策略感知的快照选路。policy 为空走 pickFromSnapshot 原路径（逐字不变）。
func (r *Relay) pickSnapshotPolicy(channels []Channel, model string, attempt int, exclude map[uint64]bool, policy string) (*Channel, error) {
	if policy == "" {
		return pickFromSnapshot(channels, model, attempt, exclude, r.healthFactor)
	}
	tier, err := tierForAttempt(channels, model, attempt, exclude)
	if err != nil {
		return nil, err
	}
	if len(tier) == 1 {
		return tier[0], nil
	}
	if ch := pickByPolicy(tier, r.channelStats(), model, policy); ch != nil {
		return ch, nil
	}
	// 层内全无统计数据/未知策略 → 回落加权随机（fail-open）
	return weightedPick(tier, r.healthFactor), nil
}

// pickByPolicy：层内策略排序取最优。纯函数（stats 显式传入）便于单测。
// 返回 nil = 无可排序依据（全员无数据），调用方回落默认路径。
func pickByPolicy(tier []*Channel, stats map[string]map[uint64]*ChanStat, model, policy string) *Channel {
	if stats == nil {
		return nil
	}
	byCh := stats[model]
	if byCh == nil {
		return nil
	}
	type cand struct {
		ch *Channel
		st *ChanStat
	}
	sampled := make([]cand, 0, len(tier))
	for _, ch := range tier {
		if st := byCh[ch.ID]; st.Sampled() {
			sampled = append(sampled, cand{ch, st})
		}
	}
	if len(sampled) == 0 {
		return nil
	}
	switch policy {
	case RoutePolicyLatency:
		sort.Slice(sampled, func(i, j int) bool {
			if sampled[i].st.P50Ms != sampled[j].st.P50Ms {
				return sampled[i].st.P50Ms < sampled[j].st.P50Ms
			}
			return sampled[i].ch.ID < sampled[j].ch.ID // 稳定 tie-break
		})
	case RoutePolicyThroughput:
		sort.Slice(sampled, func(i, j int) bool {
			ri, rj := sampled[i].st.SuccessRate(), sampled[j].st.SuccessRate()
			if ri != rj {
				return ri > rj
			}
			if sampled[i].st.P50Ms != sampled[j].st.P50Ms {
				return sampled[i].st.P50Ms < sampled[j].st.P50Ms
			}
			return sampled[i].ch.ID < sampled[j].ch.ID
		})
	default:
		return nil
	}
	return sampled[0].ch
}
