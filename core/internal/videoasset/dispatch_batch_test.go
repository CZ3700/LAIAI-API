package videoasset

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestDispatchPerTickBoundsOfficialCalls 验证素材域官方调用总闸：
// pending op 超过 DispatchPerTick 时一次 ProcessOnce 恰好只消费该上限，
// 其余留在队列（对齐火山 Premium 素材域 QPM 300 的配额纪律）。
func TestDispatchPerTickBoundsOfficialCalls(t *testing.T) {
	s, db, now, provider := setup(t)
	s.options.DispatchPerTick = 5
	ctx := context.Background()
	group := readyGroup(t, s, "dispatch-batch-group")
	for i := 0; i < 12; i++ {
		pid := "official-asset-" + string(rune('a'+i))
		asset := Asset{ID: newTestAssetID(i), OrgID: 1, GroupID: group.ID, MediaType: "image", Status: StatusProcessing, ReviewState: ReviewClear, ProviderID: &pid, RowVersion: 1, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
		if err := db.Create(&asset).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := provider.calls["GetAsset"]
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var enqueued int64
	if err := db.Model(&Operation{}).Where("action = ? AND org_id = ?", "GetAsset", 1).Count(&enqueued).Error; err != nil || enqueued != 12 {
		t.Fatalf("all 12 assets must be enqueued: %d %v", enqueued, err)
	}
	var dispatched int64
	if err := db.Model(&Operation{}).Where("action = ? AND org_id = ? AND status <> ?", "GetAsset", 1, "pending").Count(&dispatched).Error; err != nil {
		t.Fatal(err)
	}
	if dispatched != 5 {
		t.Fatalf("dispatch batch must be capped at DispatchPerTick=5, got %d", dispatched)
	}
	var remaining int64
	if err := db.Model(&Operation{}).Where("action = ? AND org_id = ? AND status = ?", "GetAsset", 1, "pending").Count(&remaining).Error; err != nil || remaining != 7 {
		t.Fatalf("remaining pending must be 7: %d %v", remaining, err)
	}
	if provider.calls["GetAsset"]-before != 5 {
		t.Fatalf("official GetAsset calls must equal the cap: delta=%d", provider.calls["GetAsset"]-before)
	}
}

func newTestAssetID(i int) string { return fmt.Sprintf("asset_dispatchbatch%03d", i) }
