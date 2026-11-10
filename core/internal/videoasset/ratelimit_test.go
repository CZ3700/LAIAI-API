package videoasset

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestOfficialActionRatesMatchProviderQuotas 断言限流表与火山官方口径一致
// （私域素材库指南，2026-11-10 核实）。官方口径变化时本测试是第一道闸。
func TestOfficialActionRatesMatchProviderQuotas(t *testing.T) {
	quotas := map[string]time.Duration{
		"GetAsset":                    10 * time.Millisecond,  // 100 QPS
		"ListAssetGroups":             100 * time.Millisecond, // 10 QPS
		"ListAssets":                  100 * time.Millisecond, // 10 QPS
		"GetAssetGroup":               100 * time.Millisecond, // 10 QPS
		"CreateAssetGroup":            100 * time.Millisecond, // 未单列，保守
		"UpdateAsset":                 100 * time.Millisecond, // 10 QPS
		"UpdateAssetGroup":            100 * time.Millisecond, // 10 QPS
		"DeleteAsset":                 100 * time.Millisecond, // 10 QPS
		"DeleteAssetGroup":            200 * time.Millisecond, // 5 QPS
		"CreateAsset":                 200 * time.Millisecond, // QPM 300
		"CreateVisualValidateSession": 100 * time.Millisecond, // 未核实，保守
		"GetVisualValidateResult":     100 * time.Millisecond, // 未核实，保守
	}
	if len(quotas) != len(officialActionRates) {
		t.Fatalf("rate table must cover exactly the documented actions: table=%d documented=%d", len(officialActionRates), len(quotas))
	}
	for action, want := range quotas {
		if got := officialActionRates[action]; got != want {
			t.Errorf("%s interval = %v, want %v", action, got, want)
		}
	}
	// 完备性：动作清单里的每个 action 都必须有限流档位，且清单本身覆盖全表
	//（2026-11-11 表漏 CreateAssetGroup 曾使虚拟组注册整链路停摆）。
	if len(assetActions) != len(officialActionRates) {
		t.Fatalf("assetActions must cover the whole rate table: actions=%d table=%d", len(assetActions), len(officialActionRates))
	}
	for _, action := range assetActions {
		if _, ok := officialActionRates[action]; !ok {
			t.Errorf("assetAction %q missing from officialActionRates", action)
		}
	}
	expectedQuotas := map[string]int{
		"GetAsset":         500, // 100 QPS × 5s tick
		"GetAssetGroup":    50,  // 10 QPS
		"DeleteAssetGroup": 25,  // 5 QPS
		"CreateAsset":      25,  // QPM 300
	}
	for action, want := range expectedQuotas {
		if got := tickQuota(action); got != want {
			t.Errorf("%s tickQuota = %d, want %d", action, got, want)
		}
	}
	for _, action := range []string{"CreateAsset", "CreateAssetGroup", "UpdateAsset", "UpdateAssetGroup", "DeleteAsset", "DeleteAssetGroup", "CreateVisualValidateSession"} {
		if !writeAction(action) {
			t.Errorf("%s must be classified as a write action", action)
		}
	}
	for _, action := range []string{"GetAsset", "GetAssetGroup", "GetVisualValidateResult", "ListAssets", "ListAssetGroups"} {
		if writeAction(action) {
			t.Errorf("%s must not be classified as a write action", action)
		}
	}
}

// TestRateLimiterReservesUniformCadence 断言同 action 连续 reserve 形成均匀节拍：
// delay 序列 0 → interval → 2×interval（官方 QPS 是每秒滑动窗口，均匀节拍不突发）。
func TestRateLimiterReservesUniformCadence(t *testing.T) {
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	l := newRateLimiter(func() time.Time { return at })
	for i, want := range []time.Duration{0, 200 * time.Millisecond, 400 * time.Millisecond} {
		if got := l.reserve("CreateAsset"); got != want {
			t.Fatalf("reserve #%d delay = %v, want %v", i+1, got, want)
		}
	}
	// 不同 action 相互独立。
	if got := l.reserve("GetAsset"); got != 0 {
		t.Fatalf("independent action delay = %v, want 0", got)
	}
}

// TestRateLimiterReserveAfterIdleDoesNotBurst 断言空闲后恢复：next 落后于当前
// 时间时立即放行（不累积旧额度、不突发补偿）。
func TestRateLimiterReserveAfterIdleDoesNotBurst(t *testing.T) {
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	clock := at
	l := newRateLimiter(func() time.Time { return clock })
	if got := l.reserve("GetAsset"); got != 0 {
		t.Fatalf("first reserve delay = %v", got)
	}
	clock = clock.Add(time.Hour) // 长时间空闲后
	if got := l.reserve("GetAsset"); got != 0 {
		t.Fatalf("reserve after idle must not owe delay: %v", got)
	}
}

// TestDispatchRespectsContextCancellation 断言优雅关闭：已取消的 tick 立即
// 以错误返回，不再发起官方调用（mainJobs 停机时 periodic 的 ctx 贯穿到派发）。
func TestDispatchRespectsContextCancellation(t *testing.T) {
	s, db, now, provider := setup(t)
	s.options.QueryConcurrency = 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消：停机中的 tick 不应产生任何官方调用
	group := readyGroup(t, s, "dispatch-cancel-group")
	for i := 0; i < 3; i++ {
		pid := "official-asset-c" + string(rune('a'+i))
		asset := Asset{ID: newTestAssetID(200 + i), OrgID: 1, GroupID: group.ID, MediaType: "image", Status: StatusProcessing, ReviewState: ReviewClear, ProviderID: &pid, RowVersion: 1, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
		if err := db.Create(&asset).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ProcessOnce(ctx); err == nil {
		t.Fatal("dispatch on a cancelled context must surface an error")
	}
	if provider.calls["GetAsset"] > 1 {
		t.Fatalf("at most one official call may pass after cancellation: %d", provider.calls["GetAsset"])
	}
}

func newTestAssetID(i int) string {
	return "asset_ratelimit" + string(rune('a'+i%26)) + fmt.Sprintf("%03d", i)
}

// TestWriteAndQueryPoolsIndependent 断言读写分池：同 tick 内写批吃满自己的
// 配额（CreateAsset 25/tick）时不挤占查询确认（GetAsset 独立池照常派发），
// 反之亦然——官方吞吐承诺两侧都拿满。
func TestWriteAndQueryPoolsIndependent(t *testing.T) {
	s, db, now, _ := setup(t)
	ctx := context.Background()
	_ = readyGroup(t, s, "pools-independent-group")
	// 30 个 CreateAsset 写 op（超出 25/tick 写闸）。
	for i := 0; i < 30; i++ {
		op := Operation{ID: newIDForTest("opw", i), OrgID: 1, Action: "CreateAsset", ResourceType: "asset", ResourceID: newTestAssetID(300 + i), Status: "pending", RequestID: newIDForTest("req", i), CanonicalPath: "/v1/video/assets", HMACKeyVersion: "h1", IdempotencyHash: newIDForTest("ih", i), BodyHash: "bh", ScopeID: "scope1", ScopeVersion: 1, AccountID: "account1", Region: "cn-beijing", Project: "project1", Fence: 0, SafeErrorCode: "", Receipt: "", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
		if err := db.Create(&op).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 5 个 GetAsset 查询 op。
	for i := 0; i < 5; i++ {
		op := Operation{ID: newIDForTest("opq", i), OrgID: 1, Action: "GetAsset", ResourceType: "asset", ResourceID: newTestAssetID(400 + i), Status: "pending", RequestID: newIDForTest("reqq", i), CanonicalPath: "/v1/video/assets/x", HMACKeyVersion: "h1", IdempotencyHash: newIDForTest("ihq", i), BodyHash: "bh", ScopeID: "scope1", ScopeVersion: 1, AccountID: "account1", Region: "cn-beijing", Project: "project1", Fence: 0, SafeErrorCode: "", Receipt: "", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
		if err := db.Create(&op).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var dispatchedWrites, dispatchedQueries int64
	db.Model(&Operation{}).Where("action = ? AND org_id = ? AND status <> ?", "CreateAsset", 1, "pending").Count(&dispatchedWrites)
	db.Model(&Operation{}).Where("action = ? AND org_id = ? AND status <> ?", "GetAsset", 1, "pending").Count(&dispatchedQueries)
	if dispatchedWrites != 25 {
		t.Fatalf("write pool must cap at 25/tick: %d", dispatchedWrites)
	}
	if dispatchedQueries != 5 {
		t.Fatalf("query pool must dispatch all 5 regardless of write pressure: %d", dispatchedQueries)
	}
}

func newIDForTest(prefix string, i int) string {
	return prefix + "_ratelimit" + string(rune('a'+i%26)) + fmt.Sprintf("%03d", i)
}
