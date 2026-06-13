package relay

import (
	"testing"

	"github.com/shopspring/decimal"
)

// 缓存命中输入按折扣价计；未配缓存价则回退 input 全价（不少收）。
func TestCostCachedTokens(t *testing.T) {
	p := &ModelPrice{
		InputUSDPer1M:       decimal.RequireFromString("10"), // $10/1M 输入
		OutputUSDPer1M:      decimal.RequireFromString("30"),
		CachedInputUSDPer1M: decimal.RequireFromString("1"), // 缓存 0.1×
	}
	// 1000 输入(其中 800 命中缓存) + 500 输出
	// = 200×10/1M + 800×1/1M + 500×30/1M = 0.002 + 0.0008 + 0.015 = 0.0178
	got := p.Cost(1000, 800, 500)
	want := decimal.RequireFromString("0.0178")
	if !got.Equal(want) {
		t.Fatalf("cached cost = %s, want %s", got, want)
	}

	// 未配缓存价 → 缓存 token 按 input 全价（向后兼容，等于旧行为）
	p2 := &ModelPrice{InputUSDPer1M: decimal.RequireFromString("10"), OutputUSDPer1M: decimal.RequireFromString("30")}
	// 1000×10/1M + 500×30/1M = 0.01 + 0.015 = 0.025（缓存不打折）
	if got := p2.Cost(1000, 800, 500); !got.Equal(decimal.RequireFromString("0.025")) {
		t.Fatalf("no-cache-price cost = %s, want 0.025", got)
	}

	// 防御：cached > input 钳到 input
	if got := p.Cost(100, 999, 0); !got.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("clamp cost = %s, want 0.0001 (100×cached)", got)
	}
}

// usage.Cached 钳到 PromptTokens 内。
func TestUsageCached(t *testing.T) {
	u := usage{PromptTokens: 100}
	u.PromptTokensDetails.CachedTokens = 80
	if u.Cached() != 80 {
		t.Fatalf("Cached = %d, want 80", u.Cached())
	}
	u.PromptTokensDetails.CachedTokens = 999 // 异常：超过 prompt
	if u.Cached() != 100 {
		t.Fatalf("Cached clamp = %d, want 100", u.Cached())
	}
}
