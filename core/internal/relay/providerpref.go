package relay

// 请求级 provider preference（设计 §4.4，2026-07-11；顶级审计修排名 6，对标 OpenRouter
// `provider` 对象）：客户端按请求钉/避上游厂商。
//   - 请求形态：body 顶层 provider: {order:[..], only:[..], ignore:[..]}，转发前剥除。
//   - 匹配键 = 渠道 provider（adapter 家族名，小写）——渠道名是运营内部信息不外露。
//   - only/ignore = 候选集硬过滤（snapshot 级，tier 结构在过滤后集合上构建）；
//     order = 层内确定性重排（列表内按序优先，同秩子集内仍走健康分加权随机保住负载均衡），
//     显式 order 优先于 X-LAIAI-Route 策略（更具体的意图赢）。
//   - 纯选路决策不碰 ledger；过滤后无渠道 = 与"模型无渠道"同一失败面。

import "strings"

type providerPref struct {
	order  []string
	only   map[string]bool
	ignore map[string]bool
}

// parseProviderPref：从 chat 请求体解析 provider 对象并**剥除**（上游不认识该字段）。
// 无字段/形态不合法/三键全空 → nil（零行为变化）。
func parseProviderPref(req map[string]any) *providerPref {
	raw, has := req["provider"]
	if !has {
		return nil
	}
	delete(req, "provider") // 无论形态是否合法都要剥除
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	p := &providerPref{}
	if arr, ok := obj["order"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				p.order = append(p.order, normProvider(s))
			}
		}
	}
	p.only = strSetOf(obj["only"])
	p.ignore = strSetOf(obj["ignore"])
	if len(p.order) == 0 && len(p.only) == 0 && len(p.ignore) == 0 {
		return nil
	}
	return p
}

func strSetOf(v any) map[string]bool {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, e := range arr {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			set[normProvider(s)] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func normProvider(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// allows：only/ignore 过滤判定。nil pref 恒放行。
func (p *providerPref) allows(provider string) bool {
	if p == nil {
		return true
	}
	pv := normProvider(provider)
	if p.ignore[pv] {
		return false
	}
	if len(p.only) > 0 && !p.only[pv] {
		return false
	}
	return true
}

// filterChannels：snapshot 预过滤（不改原快照，Channel 值拷贝）。无过滤键时零拷贝返回原 slice。
func (p *providerPref) filterChannels(snap []Channel) []Channel {
	if p == nil || (len(p.only) == 0 && len(p.ignore) == 0) {
		return snap
	}
	out := make([]Channel, 0, len(snap))
	for _, ch := range snap {
		if p.allows(ch.Provider) {
			out = append(out, ch)
		}
	}
	return out
}

// rank：order 序（未列出 = len(order)，排最后）。
func (p *providerPref) rank(provider string) int {
	pv := normProvider(provider)
	for i, o := range p.order {
		if o == pv {
			return i
		}
	}
	return len(p.order)
}

// pickByPrefOrder：层内按 order 秩取最优子集，同秩子集内健康分加权随机（保留负载均衡）。
func pickByPrefOrder(tier []*Channel, pref *providerPref, health func(uint64) float64) *Channel {
	best := pref.rank("") + 1 // len(order)+1 = 比"未列出"更差的哨兵
	var sub []*Channel
	for _, ch := range tier {
		r := pref.rank(ch.Provider)
		if r < best {
			best, sub = r, sub[:0]
		}
		if r == best {
			sub = append(sub, ch)
		}
	}
	if len(sub) == 0 {
		return nil
	}
	return weightedPick(sub, health)
}
