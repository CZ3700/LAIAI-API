package relay

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// applyInvalidate 的 kind→cache 路由（WS-2 多实例一致性核心，此前无测）：
// 断言目标缓存被清（下次 getOrLoad 重 load）、非目标缓存不被误清。误路由会让改价/改渠道后
// 缓存 stale 至多一个 TTL（计费可见性回归）。
func TestApplyInvalidateRouting(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	r := &Relay{
		channels: newSnapCache[[]Channel](8*time.Second, clock),
		prices:   newTTLCache[string, *ModelPrice](45*time.Second, clock),
		ratios:   newTTLCache[ratioKey, decimal.Decimal](45*time.Second, clock),
	}

	var priceLoads, ratioLoads, chanLoads int
	heat := func() {
		r.prices.getOrLoad("m1", func() (*ModelPrice, error) { priceLoads++; return &ModelPrice{}, nil })
		r.prices.getOrLoad("m2", func() (*ModelPrice, error) { priceLoads++; return &ModelPrice{}, nil })
		r.ratios.getOrLoad(ratioKey{1, "m1"}, func() (decimal.Decimal, error) { ratioLoads++; return decimal.NewFromInt(1), nil })
		r.channels.getOrLoad(func() ([]Channel, error) { chanLoads++; return nil, nil })
	}
	// reload helpers re-trigger load only on miss
	getPrice := func(k string) {
		r.prices.getOrLoad(k, func() (*ModelPrice, error) { priceLoads++; return &ModelPrice{}, nil })
	}
	getRatio := func() {
		r.ratios.getOrLoad(ratioKey{1, "m1"}, func() (decimal.Decimal, error) { ratioLoads++; return decimal.NewFromInt(1), nil })
	}
	getChan := func() { r.channels.getOrLoad(func() ([]Channel, error) { chanLoads++; return nil, nil }) }

	heat()
	if priceLoads != 2 || ratioLoads != 1 || chanLoads != 1 {
		t.Fatalf("warmup loads p=%d r=%d c=%d, want 2/1/1", priceLoads, ratioLoads, chanLoads)
	}

	// KindPrice 单 key：只清 m1，m2 仍命中，ratios/channels 不受影响
	r.applyInvalidate(KindPrice, "m1")
	getPrice("m1") // reload → priceLoads 3
	getPrice("m2") // cached → 不变
	getRatio()     // cached
	getChan()      // cached
	if priceLoads != 3 || ratioLoads != 1 || chanLoads != 1 {
		t.Fatalf("after KindPrice m1: p=%d r=%d c=%d, want 3/1/1 (只清 m1)", priceLoads, ratioLoads, chanLoads)
	}

	// KindRatio：清 ratios，不动 prices/channels
	r.applyInvalidate(KindRatio, "")
	getRatio() // reload → 2
	getPrice("m1")
	getChan()
	if ratioLoads != 2 || priceLoads != 3 || chanLoads != 1 {
		t.Fatalf("after KindRatio: r=%d p=%d c=%d, want 2/3/1", ratioLoads, priceLoads, chanLoads)
	}

	// KindChannel：清 channels，不动 prices/ratios
	r.applyInvalidate(KindChannel, "")
	getChan() // reload → 2
	getPrice("m1")
	getRatio()
	if chanLoads != 2 || priceLoads != 3 || ratioLoads != 2 {
		t.Fatalf("after KindChannel: c=%d p=%d r=%d, want 2/3/2", chanLoads, priceLoads, ratioLoads)
	}

	// KindAll：三者全清
	r.applyInvalidate(KindAll, "")
	getPrice("m1")
	getRatio()
	getChan()
	if priceLoads != 4 || ratioLoads != 3 || chanLoads != 3 {
		t.Fatalf("after KindAll: p=%d r=%d c=%d, want 4/3/3", priceLoads, ratioLoads, chanLoads)
	}
}
