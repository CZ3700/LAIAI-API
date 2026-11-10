package videoasset

import (
	"context"
	"testing"
	"time"
)

// setupArchive 返回固定归档时钟（窗内/窗外可控）的 Service。
func setupArchive(t *testing.T, at time.Time) (*Service, *time.Time) {
	s, _, _, _ := setup(t)
	s.options.ArchiveEnabled = true
	clock := at
	s.options.ArchiveNow = func() time.Time { return clock }
	return s, &clock
}

func TestArchiveWindowGate(t *testing.T) {
	at := time.Date(2026, 11, 12, 1, 30, 0, 0, time.FixedZone("CST", 8*3600)) // 窗口前
	s, clock := setupArchive(t, at)
	ctx := context.Background()
	group := readyGroup(t, s, "archive-window-group")
	if _, _, err := s.CreateAsset(ctx, createContext("req-archive-w"), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/archive.png"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	*clock = at.AddDate(0, 0, -10) // 素材创建时间拨到 10 天前
	params := map[string]any{"created_at": at.AddDate(0, 0, -10), "status": StatusActive}
	if err := s.db.Model(&Asset{}).Where("group_id = ?", group.ID).Updates(params).Error; err != nil {
		t.Fatal(err)
	}
	// 窗口外：不注入。
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatal(err)
	}
	var ops int64
	s.db.Model(&Operation{}).Where("action = ?", "DeleteAsset").Count(&ops)
	if ops != 0 {
		t.Fatalf("outside window must not inject: %d", ops)
	}
	// 进入窗口：注入。
	*clock = time.Date(2026, 11, 12, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatal(err)
	}
	s.db.Model(&Operation{}).Where("action = ?", "DeleteAsset").Count(&ops)
	if ops != 1 {
		t.Fatalf("inside window must inject exactly one: %d", ops)
	}
}

func TestArchiveExemptsRealGroups(t *testing.T) {
	at := time.Date(2026, 11, 12, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s, clock := setupArchive(t, at)
	ctx := context.Background()
	group := readyGroup(t, s, "archive-virtual-group")
	if _, _, err := s.CreateAsset(ctx, createContext("req-archive-v"), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/archive-v.png"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"created_at": at.AddDate(0, 0, -10), "status": StatusActive}
	if err := s.db.Model(&Asset{}).Where("group_id = ?", group.ID).Updates(params).Error; err != nil {
		t.Fatal(err)
	}
	// 虚拟组素材正常注入。
	*clock = at
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatal(err)
	}
	var ops int64
	s.db.Model(&Operation{}).Where("action = ?", "DeleteAsset").Count(&ops)
	if ops != 1 {
		t.Fatalf("virtual group asset must be archived: %d", ops)
	}
	// 真人组豁免：组 kind 改 real 后，即便素材超期也不再注入（授权肖像是
	// 长期资产）。已注入的 pending op 依旧防重跳过。
	if err := s.db.Model(&Group{}).Where("group_id = ?", group.ID).Update("kind", KindReal).Error; err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatal(err)
	}
	s.db.Model(&Operation{}).Where("action = ?", "DeleteAsset").Count(&ops)
	if ops != 1 {
		t.Fatalf("real-group exemption must not inject more: %d", ops)
	}
}

func TestArchiveDailyLimit(t *testing.T) {
	at := time.Date(2026, 11, 12, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s, clock := setupArchive(t, at)
	s.options.ArchiveDailyLimit = 2
	s.options.ArchiveBatch = 5
	ctx := context.Background()
	group := readyGroup(t, s, "archive-limit-group")
	for i := 0; i < 3; i++ {
		if _, _, err := s.CreateAsset(ctx, createContext("req-archive-l"+string(rune('a'+i))), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/archive-l.png"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"created_at": at.AddDate(0, 0, -10), "status": StatusActive}
	if err := s.db.Model(&Asset{}).Where("group_id = ?", group.ID).Updates(params).Error; err != nil {
		t.Fatal(err)
	}
	*clock = at
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatal(err)
	}
	var ops int64
	s.db.Model(&Operation{}).Where("action = ?", "DeleteAsset").Count(&ops)
	if ops != 2 {
		t.Fatalf("daily limit must cap injections: %d", ops)
	}
	// 跨日重置：次日窗口继续。
	*clock = at.AddDate(0, 0, 1)
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatal(err)
	}
	s.db.Model(&Operation{}).Where("action = ?", "DeleteAsset").Count(&ops)
	if ops != 3 {
		t.Fatalf("next day must resume: %d", ops)
	}
}

func TestArchiveEndToEndDeletesAsset(t *testing.T) {
	at := time.Date(2026, 11, 12, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s, clock := setupArchive(t, at)
	ctx := context.Background()
	group := readyGroup(t, s, "archive-e2e-group")
	asset, _, err := s.CreateAsset(ctx, createContext("req-archive-e2e"), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/archive-e2e.png"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Asset{}).Where("asset_id = ?", asset.ID).Updates(map[string]any{"created_at": at.AddDate(0, 0, -10), "status": StatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	*clock = at
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatal(err)
	}
	var row Asset
	if err := s.db.Where("asset_id = ?", asset.ID).Take(&row).Error; err != nil || row.Status != StatusDeleting {
		t.Fatalf("archived asset must enter deleting on acceptance: %+v", row)
	}
	// 归档 op 派发并确认：官方删除受理 → 既有链路置 deleting；随后删除确认
	//（delete_asset_confirmed 事件）置 deleted+deleted_at。
	*clock = clock.Add(16 * time.Second)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var op Operation
	if err := s.db.Where("action = ? AND resource_id = ?", "DeleteAsset", asset.ID).First(&op).Error; err != nil {
		t.Fatal(err)
	}
	if op.Status != "confirmed" && op.Status != "in_flight" {
		t.Fatalf("archive op must dispatch: %s %s", op.Status, op.SafeErrorCode)
	}
	// 推进时间让官方确认轮询跑完（delete confirmed 链路）。
	for i := 0; i < 3; i++ {
		*clock = clock.Add(16 * time.Second)
		if err := s.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Where("asset_id = ?", asset.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusDeleted && row.DeletedAt == nil {
		t.Fatalf("archived asset must end deleted with deleted_at: %+v", row)
	}
}
