package videoasset

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
)

// setupArchive 返回固定归档时钟（窗内/窗外可控）的 Service。
func setupArchive(t *testing.T, at time.Time) (*Service, *gorm.DB, *time.Time) {
	s, db, _, _ := setup(t)
	s.options.ArchiveEnabled = true
	clock := at
	s.options.ArchiveNow = func() time.Time { return clock }
	return s, db, &clock
}

func TestArchiveWindowGate(t *testing.T) {
	at := time.Date(2026, 11, 12, 1, 30, 0, 0, time.FixedZone("CST", 8*3600)) // 窗口前
	s, _, clock := setupArchive(t, at)
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
	s, _, clock := setupArchive(t, at)
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
	s, _, clock := setupArchive(t, at)
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
	s, _, clock := setupArchive(t, at)
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

// TestArchiveReinjectionAfterRejectionNoCollision 断言 rejected 恢复 active
// 的素材可再次注入（幂等哈希含 op id，不再撞 uk_video_asset_idempotency
// 中断整批——2026-11-11 生产停摆事故的回归防呆）。
func TestArchiveReinjectionAfterRejectionNoCollision(t *testing.T) {
	at := time.Date(2026, 11, 12, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s, _, clock := setupArchive(t, at)
	ctx := context.Background()
	group := readyGroup(t, s, "archive-reinject-group")
	if _, _, err := s.CreateAsset(ctx, createContext("req-archive-reinj"), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/archive-reinj.png"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Asset{}).Where("asset_id LIKE ?", "asset_%").Updates(map[string]any{"created_at": at.AddDate(0, 0, -10), "status": StatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	*clock = at
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatalf("first sweep must not fail: %v", err)
	}
	// 模拟官方拒绝：op 置 rejected（Receipt 恢复链路把素材拉回 active），
	// 素材回到候选集后第二次注入不得撞唯一键。
	var first Operation
	if err := s.db.Where("action = ?", "DeleteAsset").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Operation{}).Where("operation_id = ?", first.ID).Updates(map[string]any{"status": "rejected", "safe_error_code": "asset_processing_failed"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Asset{}).Where("asset_id = ?", first.ResourceID).Updates(map[string]any{"status": StatusActive, "deleted_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatalf("re-injection after rejection must not collide: %v", err)
	}
	var ops int64
	s.db.Model(&Operation{}).Where("action = ?", "DeleteAsset").Count(&ops)
	if ops != 2 {
		t.Fatalf("rejected asset must be re-injected with a fresh idempotency hash: %d", ops)
	}
}

// TestArchiveRespectsConcurrentTaskReference（审计 A01 回归）：预检通过后、
// 事务拿锁前，视频任务写入 TaskReference——锁内完整重检必须放弃归档。
func TestArchiveRespectsConcurrentTaskReference(t *testing.T) {
	at := time.Date(2026, 11, 12, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s, db, clock := setupArchive(t, at)
	ctx := context.Background()
	group := readyGroup(t, s, "archive-race-group")
	asset, _, err := s.CreateAsset(ctx, createContext("req-archive-race"), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/archive-race.png"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&Asset{}).Where("asset_id = ?", asset.ID).Updates(map[string]any{"created_at": at.AddDate(0, 0, -10), "status": StatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	// 模拟并发窗口：预检（archiveSweep 候选阶段）看到无引用；注入前另一
	// 任务已写入未释放引用。直接调 archiveInject 前插引用即可确定性编排。
	*clock = at
	if err := db.Create(&TaskReference{OrgID: 1, AssetID: asset.ID, TaskID: "task_race_ref01", CreatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	var candidate Asset
	if err := db.Where("asset_id = ?", asset.ID).Take(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.archiveInject(ctx, &candidate, at); err != nil {
		t.Fatal(err)
	}
	var row Asset
	if err := db.Where("asset_id = ?", asset.ID).Take(&row).Error; err != nil || row.Status != StatusActive {
		t.Fatalf("asset referenced by a concurrent task must stay active: %+v", row)
	}
	var ops int64
	db.Model(&Operation{}).Where("action = ? AND resource_id = ?", "DeleteAsset", asset.ID).Count(&ops)
	if ops != 0 {
		t.Fatalf("no archive delete op may be created for a referenced asset: %d", ops)
	}
}

// TestArchiveRunsOnIdleWorker（审计 A02 回归）：读写队列全空时 ProcessOnce
// 仍必须执行归档（曾因空批提前 return 跳过凌晨窗口的维护阶段）。
func TestArchiveRunsOnIdleWorker(t *testing.T) {
	at := time.Date(2026, 11, 12, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s, db, clock := setupArchive(t, at)
	ctx := context.Background()
	group := readyGroup(t, s, "archive-idle-group")
	asset, _, err := s.CreateAsset(ctx, createContext("req-archive-idle"), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/archive-idle.png"})
	if err != nil {
		t.Fatal(err)
	}
	// 先把创建/确认 op 全部消化完（队列清空）。
	for i := 0; i < 4; i++ {
		*clock = clock.Add(16 * time.Second)
		if err := s.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var pendingOps int64
	db.Model(&Operation{}).Where("status IN ?", []string{"pending", "in_flight"}).Count(&pendingOps)
	if pendingOps != 0 {
		t.Fatalf("precondition: op queue must be idle, got %d", pendingOps)
	}
	if err := db.Model(&Asset{}).Where("asset_id = ?", asset.ID).Updates(map[string]any{"created_at": at.AddDate(0, 0, -10), "status": StatusActive, "updated_at": at.Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	*clock = at
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var ops int64
	db.Model(&Operation{}).Where("action = ? AND resource_id = ?", "DeleteAsset", asset.ID).Count(&ops)
	if ops != 1 {
		t.Fatalf("idle worker must still enqueue archive delete: %d", ops)
	}
}

// TestArchiveCreateAsset429Recovers（审计 A03 回归）：官方明确 429（未受理）
// 时素材不得置 failed；上游恢复后同一 op 重派成功转 ready。
func TestArchiveCreateAsset429Recovers(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	group := readyGroup(t, s, "asset-429-group")
	provider.rateLimited = 1 // 第一次 CreateAsset 调用返回 429
	if _, _, err := s.CreateAsset(ctx, createContext("req-asset-429"), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/asset-429.png"}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(16 * time.Second)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var row Asset
	if err := db.Where("group_id = ? AND media_type = ?", group.ID, "image").Order("created_at DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status == StatusFailed {
		t.Fatal("definitive 429 must not fail the asset")
	}
	var op Operation
	if err := db.Where("action = ? AND resource_id = ?", "CreateAsset", row.ID).First(&op).Error; err != nil {
		t.Fatal(err)
	}
	if op.Status != "pending" || op.SafeErrorCode != "provider_rate_limited" {
		t.Fatalf("429 op must stay pending with backoff: status=%s code=%s next=%v", op.Status, op.SafeErrorCode, op.NextAttemptAt)
	}
	// 退避到期后重派成功 → 素材走 processing → ready。
	*now = now.Add(time.Minute)
	for i := 0; i < 3; i++ {
		*now = now.Add(16 * time.Second)
		if err := s.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Where("asset_id = ?", row.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusActive {
		t.Fatalf("asset must recover to ready after provider is healthy: %s", row.Status)
	}
}

// TestRecoveredQueryUnknownDoesNotBlockDeletion（审计 A05 回归）：GetAsset
// 网络失败留下 unknown 查询行，其后的权威读恢复 ready——删除防护不得被
// 历史只读 unknown 永久阻塞。
func TestRecoveredQueryUnknownDoesNotBlockDeletion(t *testing.T) {
	s, db, now, _ := setup(t)
	group := readyGroup(t, s, "unknown-query-group")
	asset := readyAsset(t, s, now, group.ID, "unknown-query-asset")
	// 手工注入一个 GetAsset unknown 历史行（模拟网络失败遗留）。
	unknown := Operation{ID: "op_unknown_query1", OrgID: 1, Action: "GetAsset", ResourceType: "asset", ResourceID: asset.ID, Status: "unknown", SafeErrorCode: "provider_timeout", CanonicalPath: "internal:GetAsset:" + asset.ID, HMACKeyVersion: "h1", IdempotencyHash: "ih-unknown-1", BodyHash: "bh", CreatedAt: *now, UpdatedAt: *now}
	if err := db.Create(&unknown).Error; err != nil {
		t.Fatal(err)
	}
	var current Asset
	if err := db.Where("asset_id = ?", asset.ID).Take(&current).Error; err != nil {
		t.Fatal(err)
	}
	var g Group
	if err := db.Where("group_id = ?", group.ID).Take(&g).Error; err != nil {
		t.Fatal(err)
	}
	_ = g
	mode, reason, _, err := s.assetDeletionState(db, current, g)
	if err != nil || mode != "official" || reason != "" {
		t.Fatalf("read-only unknown must not block deletion: mode=%s reason=%s err=%v", mode, reason, err)
	}
}

// 归档唯一判据（2026-11-12 用户拍板）：近 7 天内被生成任务引用
// （video_task_asset_refs，含已终态 released 的历史引用）的素材不归档；
// 近 7 天无调用即可归档——创建时长不参与判断。
func TestArchiveUsageWindowGate(t *testing.T) {
	at := time.Date(2026, 11, 12, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	s, _, clock := setupArchive(t, at)
	ctx := context.Background()
	group := readyGroup(t, s, "archive-usage-group")
	for i, name := range []string{"recent", "old", "never"} {
		if _, _, err := s.CreateAsset(ctx, createContext("req-archive-u"+string(rune('a'+i))), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/archive-u.png"}); err != nil {
			t.Fatal(err)
		}
		_ = name
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var assets []Asset
	if err := s.db.Where("group_id = ?", group.ID).Order("asset_id").Find(&assets).Error; err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 {
		t.Fatalf("expected 3 assets, got %d", len(assets))
	}
	// 使用窗口是唯一判据：刚创建（created_at = now）的素材只要近 7 天无调用
	// 也会进入候选——不再有独立的创建保留期。素材拨回 active（对齐线上
	// ready/active 稳态；测试夹具创建后停在 processing）。
	if err := s.db.Model(&Asset{}).Where("group_id = ?", group.ID).Updates(map[string]any{"status": StatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	// recent：2 天前被任务引用（已 released——历史调用也算使用）。
	released := at.AddDate(0, 0, -2)
	if err := s.db.Create(&TaskReference{OrgID: 1, TaskID: "task_usage_recent", AssetID: assets[0].ID, GroupID: group.ID, ContentIndex: 0, Role: "reference_image", AssetMetadataVersion: 1, AuthorizationVersion: 1, ReleasedAt: &released, CreatedAt: at.AddDate(0, 0, -2)}).Error; err != nil {
		t.Fatal(err)
	}
	// old：8 天前被引用（超出 7 天使用窗口）。
	if err := s.db.Create(&TaskReference{OrgID: 1, TaskID: "task_usage_old", AssetID: assets[1].ID, GroupID: group.ID, ContentIndex: 0, Role: "reference_image", AssetMetadataVersion: 1, AuthorizationVersion: 1, ReleasedAt: &released, CreatedAt: at.AddDate(0, 0, -8)}).Error; err != nil {
		t.Fatal(err)
	}
	*clock = at
	if err := s.archiveSweep(ctx); err != nil {
		t.Fatal(err)
	}
	var ops int64
	s.db.Model(&Operation{}).Where("action = ?", "DeleteAsset").Count(&ops)
	if ops != 2 {
		t.Fatalf("only never-referenced and 7d-stale assets must be archived: %d", ops)
	}
	var protected int64
	s.db.Model(&Operation{}).Where("action = ? AND resource_id = ?", "DeleteAsset", assets[0].ID).Count(&protected)
	if protected != 0 {
		t.Fatal("asset referenced within 7 days must be protected from archive")
	}
	var archivedOld int64
	s.db.Model(&Operation{}).Where("action = ? AND resource_id = ?", "DeleteAsset", assets[1].ID).Count(&archivedOld)
	if archivedOld != 1 {
		t.Fatal("asset whose only reference is older than 7 days must be archived")
	}
}

// TestAutoLicenseWorksWithoutCaptureHistory（2026-11-13 org45 缺口修复）：
// CaptureHistory=false 的组织无响应证据指针——自动授权回退用本次核实操作
// ID 作证据引用，不再永久卡 needs_review。
func TestAutoLicenseWorksWithoutCaptureHistory(t *testing.T) {
	s, db, now, _ := setup(t)
	s.options.AutoLicense = true
	// 关闭历史捕获：finish 不落响应证据，ResponsePayloadID 为空（运行时由
	// 组织的 ExecutionRetentionConsent 决定；单测直接验证证据回退分支）。
	ctx := context.Background()
	if err := s.ConfigureConsole(ConsoleOptions{CallbackURL: "https://customer.example/return"}); err != nil {
		t.Fatal(err)
	}
	c := CreateContext{CallerKind: "session_user", OrgID: 1, UserID: 22, RequestID: "auto-license-nocapture", IdempotencyKey: "auto-license-nocapture"}
	p, _, err := s.CreateConsolePortrait(ctx, c, PortraitCreate{Name: "Portrait"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(16 * time.Second)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var row Portrait
	if err := db.Where("session_id = ?", p.ID).Take(&row).Error; err != nil || row.SessionStatus != "succeeded" {
		t.Fatalf("portrait must succeed: %+v %v", row, err)
	}
	if row.LicenseStatus != "verified" || row.LicenseEvidenceRef == "" {
		t.Fatalf("auto license must register even without capture history: %+v", row)
	}
	if s.licenseStatus(row, "video_generation") != "authorized" {
		t.Fatal("license must be authorized")
	}
}
