package relay

// 策略化选路单测（设计 §4.4 A2/A4）：层内排序语义、无数据回落、resolveRoutePolicy 优先级。

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

func chans(ids ...uint64) []*Channel {
	out := make([]*Channel, len(ids))
	for i, id := range ids {
		out[i] = &Channel{ID: id, Weight: 10}
	}
	return out
}

func TestPickByPolicyLatency(t *testing.T) {
	tier := chans(1, 2, 3)
	stats := map[string]map[uint64]*ChanStat{
		"m": {
			1: {Calls: 50, Succeeded: 50, P50Ms: 800},
			2: {Calls: 50, Succeeded: 40, P50Ms: 200}, // 最快（成功率低不影响 latency 策略）
			3: {Calls: 5, Succeeded: 5, P50Ms: 10},    // 样本不足：不参与排序
		},
	}
	if got := pickByPolicy(tier, stats, "m", RoutePolicyLatency); got == nil || got.ID != 2 {
		t.Fatalf("latency pick = %v, want ch2", got)
	}
}

func TestPickByPolicyThroughput(t *testing.T) {
	tier := chans(1, 2, 3)
	stats := map[string]map[uint64]*ChanStat{
		"m": {
			1: {Calls: 100, Succeeded: 90, P50Ms: 100},  // 0.90
			2: {Calls: 100, Succeeded: 99, P50Ms: 900},  // 0.99 最高
			3: {Calls: 100, Succeeded: 99, P50Ms: 100},  // 0.99 且更快 → tie-break 胜
		},
	}
	if got := pickByPolicy(tier, stats, "m", RoutePolicyThroughput); got == nil || got.ID != 3 {
		t.Fatalf("throughput pick = %v, want ch3 (tie-break by p50)", got)
	}
}

func TestPickByPolicyNoDataFallback(t *testing.T) {
	tier := chans(1, 2)
	// 无该模型统计 → nil（调用方回落加权随机）
	if got := pickByPolicy(tier, map[string]map[uint64]*ChanStat{}, "m", RoutePolicyLatency); got != nil {
		t.Fatalf("no-stats pick = %v, want nil", got)
	}
	// 全员样本不足 → nil
	stats := map[string]map[uint64]*ChanStat{"m": {1: {Calls: 3}, 2: {Calls: 2}}}
	if got := pickByPolicy(tier, stats, "m", RoutePolicyLatency); got != nil {
		t.Fatalf("undersampled pick = %v, want nil", got)
	}
	// nil stats map → nil
	if got := pickByPolicy(tier, nil, "m", RoutePolicyLatency); got != nil {
		t.Fatalf("nil-stats pick = %v, want nil", got)
	}
}

// exclude 接棒：最优被排除后确定性取次优（A4 降级链语义）。
func TestPolicyExcludeHandsOver(t *testing.T) {
	all := []Channel{{ID: 1, Weight: 10, Models: "m", Status: 1}, {ID: 2, Weight: 10, Models: "m", Status: 1}}
	stats := map[string]map[uint64]*ChanStat{
		"m": {1: {Calls: 20, Succeeded: 20, P50Ms: 100}, 2: {Calls: 20, Succeeded: 20, P50Ms: 500}},
	}
	tier, err := tierForAttempt(all, "m", 0, map[uint64]bool{1: true}, 0) // 最快的 ch1 已失败被排除
	if err != nil {
		t.Fatal(err)
	}
	if got := pickByPolicy(tier, stats, "m", RoutePolicyLatency); got == nil || got.ID != 2 {
		t.Fatalf("exclude handover = %v, want ch2", got)
	}
}

// BYOK 隔离红线（0044）：他人 BYOK 结构性不可见；自有 BYOK 构成虚拟顶层；orgID=0 只平台。
func TestTierForAttemptByokIsolation(t *testing.T) {
	chs := []Channel{
		{ID: 1, Models: "m", Priority: 5, Weight: 10},    // 平台渠道
		{ID: 2, Models: "m", OwnerOrgID: 42, Weight: 10}, // org42 自有
		{ID: 3, Models: "m", OwnerOrgID: 99, Weight: 10}, // 他人自有
	}
	// org42：attempt0 = 自有虚拟顶层（只含 ch2）
	tier, err := tierForAttempt(chs, "m", 0, nil, 42)
	if err != nil || len(tier) != 1 || tier[0].ID != 2 {
		t.Fatalf("org42 attempt0 = %v, want [ch2]", tier)
	}
	// org42：attempt1 降级到平台层（绝不含任何 BYOK）
	tier, _ = tierForAttempt(chs, "m", 1, nil, 42)
	if len(tier) != 1 || tier[0].ID != 1 {
		t.Fatalf("org42 attempt1 = %v, want [ch1 platform]", tier)
	}
	// 无 BYOK 的 org7：只见平台，ch2/ch3 结构性不存在
	tier, _ = tierForAttempt(chs, "m", 0, nil, 7)
	if len(tier) != 1 || tier[0].ID != 1 {
		t.Fatalf("org7 = %v, want [ch1]", tier)
	}
	// orgID=0（task/realtime）：只平台
	tier, _ = tierForAttempt(chs, "m", 0, nil, 0)
	if len(tier) != 1 || tier[0].ID != 1 {
		t.Fatalf("orgID=0 = %v, want [ch1]", tier)
	}
	// 自有渠道被 exclude 后 attempt0 直接是平台层（own 层为空不占位）
	tier, _ = tierForAttempt(chs, "m", 0, map[uint64]bool{2: true}, 42)
	if len(tier) != 1 || tier[0].ID != 1 {
		t.Fatalf("org42 excluded-own = %v, want [ch1]", tier)
	}
}

func TestResolveRoutePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mk := func(header string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if header != "" {
			c.Request.Header.Set("X-LAIAI-Route", header)
		}
		return c
	}
	key := &identity.APIKey{RoutePolicy: "throughput"}
	// 头优先于 key 默认
	if p := resolveRoutePolicy(mk("latency"), key); p != RoutePolicyLatency {
		t.Fatalf("header precedence = %q", p)
	}
	// 无头 → key 默认
	if p := resolveRoutePolicy(mk(""), key); p != RoutePolicyThroughput {
		t.Fatalf("key default = %q", p)
	}
	// 未知值静默回落 order（空串）
	if p := resolveRoutePolicy(mk("fastest-please"), key); p != "" {
		t.Fatalf("unknown header = %q, want ''", p)
	}
	// price/order 归一默认路径
	if p := resolveRoutePolicy(mk("price"), key); p != "" {
		t.Fatalf("price = %q, want '' (等价 order)", p)
	}
	if p := resolveRoutePolicy(mk(""), &identity.APIKey{}); p != "" {
		t.Fatalf("no policy anywhere = %q, want ''", p)
	}
}