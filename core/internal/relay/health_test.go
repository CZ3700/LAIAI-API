package relay

import "testing"

// 健康分 EWMA 的确定性行为（M2 收尾）：未知=满分、失败衰减、成功恢复、floor 钳制。
func TestChannelHealthEWMA(t *testing.T) {
	r := &Relay{}

	// 未知渠道 = 满分（乐观默认：先给正常流量）
	if f := r.healthFactor(1); f != 1.0 {
		t.Fatalf("unknown channel factor = %v, want 1.0", f)
	}

	// 单次失败：1.0 → 0.3*0 + 0.7*1 = 0.7（灵敏但不过激）
	r.observe(1, false)
	if f := r.healthFactor(1); f < 0.69 || f > 0.71 {
		t.Fatalf("single-failure factor = %v, want ~0.7", f)
	}

	// 连续失败 → 被 floor 钳住，但永不归零（保留恢复机会）
	for i := 0; i < 50; i++ {
		r.observe(1, false)
	}
	if f := r.healthFactor(1); f != healthFloor {
		t.Fatalf("after many failures factor = %v, want floor %v", f, healthFloor)
	}

	// 连续成功 → 恢复到接近满分
	for i := 0; i < 50; i++ {
		r.observe(1, true)
	}
	if f := r.healthFactor(1); f < 0.99 {
		t.Fatalf("after recovery factor = %v, want ~1.0", f)
	}
}

// 健康分作层内权重乘子：不健康渠道流量被大幅压低，但永不被硬排除（floor 保活）。
func TestHealthWeightedPick(t *testing.T) {
	a := &Channel{ID: 1, Weight: 1}
	b := &Channel{ID: 2, Weight: 1}
	// b 触底（factor=floor），a 满分
	health := func(id uint64) float64 {
		if id == 2 {
			return healthFloor
		}
		return 1.0
	}
	seen := map[uint64]int{}
	const N = 4000
	for i := 0; i < N; i++ {
		seen[weightedPick([]*Channel{a, b}, health).ID]++
	}
	// 低均值 → smoothing=100；有效权重 a=101, b=101*0.1≈10.1 → b 约 9%
	if seen[2] == 0 {
		t.Fatal("floored channel never picked — must stay reachable for recovery")
	}
	if seen[1] <= seen[2] {
		t.Fatalf("healthy channel must dominate: a=%d b=%d", seen[1], seen[2])
	}
	if frac := float64(seen[2]) / N; frac > 0.25 {
		t.Fatalf("unhealthy channel got %.1f%% traffic, want heavily reduced", frac*100)
	}
}
