package relay

// 请求级 provider preference（设计 §4.4）：解析/剥除/过滤/order 重排。

import "testing"

func TestParseProviderPrefStripsAndParses(t *testing.T) {
	req := map[string]any{
		"model": "m",
		"provider": map[string]any{
			"order":  []any{"OpenAI", " groq "},
			"ignore": []any{"anthropic"},
		},
	}
	p := parseProviderPref(req)
	if p == nil {
		t.Fatal("want pref")
	}
	if _, has := req["provider"]; has {
		t.Fatal("provider 字段必须从转发体剥除（上游不认识）")
	}
	if len(p.order) != 2 || p.order[0] != "openai" || p.order[1] != "groq" {
		t.Fatalf("order = %v", p.order)
	}
	if !p.ignore["anthropic"] {
		t.Fatalf("ignore = %v", p.ignore)
	}

	// 非对象形态：也要剥除，且返回 nil（零行为变化）
	req2 := map[string]any{"provider": "openai"}
	if parseProviderPref(req2) != nil {
		t.Fatal("非对象形态应返回 nil")
	}
	if _, has := req2["provider"]; has {
		t.Fatal("非对象形态也必须剥除")
	}
	// 无字段
	if parseProviderPref(map[string]any{"model": "m"}) != nil {
		t.Fatal("无 provider 字段应返回 nil")
	}
	// 三键全空
	if parseProviderPref(map[string]any{"provider": map[string]any{"order": []any{}}}) != nil {
		t.Fatal("全空对象应返回 nil")
	}
}

func TestProviderPrefAllowsAndFilter(t *testing.T) {
	p := &providerPref{only: map[string]bool{"openai": true, "groq": true}, ignore: map[string]bool{"groq": true}}
	if !p.allows("OpenAI") {
		t.Fatal("only 命中（大小写不敏感）应放行")
	}
	if p.allows("groq") {
		t.Fatal("ignore 优先于 only")
	}
	if p.allows("anthropic") {
		t.Fatal("only 未列出应拒绝")
	}
	var nilPref *providerPref
	if !nilPref.allows("anything") {
		t.Fatal("nil pref 恒放行")
	}

	snap := []Channel{
		{ID: 1, Provider: "openai"},
		{ID: 2, Provider: "groq"},
		{ID: 3, Provider: "anthropic"},
	}
	got := p.filterChannels(snap)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("filter = %v", got)
	}
	if len(nilPref.filterChannels(snap)) != 3 {
		t.Fatal("nil pref 零过滤")
	}
}

func TestPickByPrefOrder(t *testing.T) {
	p := &providerPref{order: []string{"groq", "openai"}}
	tier := []*Channel{
		{ID: 1, Provider: "openai", Weight: 1},
		{ID: 2, Provider: "groq", Weight: 1},
		{ID: 3, Provider: "anthropic", Weight: 1},
	}
	for i := 0; i < 20; i++ {
		ch := pickByPrefOrder(tier, p, nil)
		if ch == nil || ch.ID != 2 {
			t.Fatalf("order 首选 groq，got %v", ch)
		}
	}
	// groq 被 exclude 后（tier 里没有它）→ openai 接棒
	ch := pickByPrefOrder(tier[:1], p, nil)
	if ch == nil || ch.ID != 1 {
		t.Fatalf("次序接棒 openai，got %v", ch)
	}
	// 全是未列出 provider → 同秩子集加权随机（不为 nil）
	if ch := pickByPrefOrder(tier[2:], p, nil); ch == nil || ch.ID != 3 {
		t.Fatalf("未列出者兜底，got %v", ch)
	}
}
