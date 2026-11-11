package videoasset

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 归档：用户素材用完即弃，创建超过保留期（默认 3 天）的素材每日凌晨窗口内
// 分批注入 DeleteAsset op——复用既有删除链路（能力门控、在途引用保护、
// 官方限流节拍、异步删除确认、tombstone 软删），官方侧同步释放存储。
// 窗口/限量/保留期/豁免全部 env 可配（见 Options）。

// archiveDailyCount 统计本地时区"今日 00:00"以来已注入的归档删除 op 数，
// 作为当日限量的轻查询（免迁移，internal 前缀与用户删除路径隔离）。
// action 条件走 idx_video_asset_operation_resource 索引，避免 LIKE 全表扫。
func (s *Service) archiveDailyCount(dayStart time.Time) int64 {
	var n int64
	s.db.Model(&Operation{}).Where("action = ? AND created_at >= ?", "DeleteAsset", dayStart).Count(&n)
	return n
}

// archiveInWindow 判定归档时钟是否落在配置窗口内，返回本地时区当日 00:00。
func (s *Service) archiveInWindow(now time.Time) (bool, time.Time) {
	loc := s.options.ArchiveLocation
	local := now.In(loc)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	start, okStart := parseArchiveClock(s.options.ArchiveWindowStart)
	end, okEnd := parseArchiveClock(s.options.ArchiveWindowEnd)
	if !okStart || !okEnd || end <= start {
		return false, dayStart
	}
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= start && minutes < end, dayStart
}

func parseArchiveClock(v string) (int, bool) {
	parts := strings.Split(v, ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// archiveSweep 是每 tick 的归档入口：窗口/限量不满足时零成本返回；窗口内
// 选取候选素材、逐一过 assetDeletionState 防护后直插 DeleteAsset op
// （仿 enqueueQuery 的 internal 域直插，不走用户幂等路径）。
func (s *Service) archiveSweep(ctx context.Context) error {
	now := s.options.ArchiveNow()
	inWindow, dayStart := s.archiveInWindow(now)
	if !inWindow {
		return nil
	}
	if s.archiveDailyCount(dayStart) >= int64(s.options.ArchiveDailyLimit) {
		return nil
	}
	remaining := int64(s.options.ArchiveDailyLimit) - s.archiveDailyCount(dayStart)
	batch := int64(s.options.ArchiveBatch)
	if remaining < batch {
		batch = remaining
	}
	q := s.db.WithContext(ctx).
		Table("video_assets AS a").
		Joins("JOIN video_asset_groups g ON g.org_id = a.org_id AND g.group_id = a.group_id").
		Where("a.status = ? AND a.provider_id IS NOT NULL AND a.deleted_at IS NULL",
			StatusActive)
	if s.options.ArchiveExemptRealGroups {
		q = q.Where("g.kind <> ?", KindReal)
	}
	// 归档判据（2026-11-12 用户拍板，替代"3 天保留期 + 7 天使用保护"双条件）：
	// 只看近 N 天内是否被生成任务引用——有调用不归档，无调用可归档。
	// 引用以 video_task_asset_refs 行为准（任务受理时写入，含已终态任务——
	// released 的历史引用也算使用）。NOT EXISTS 命中既有
	// idx_video_asset_refs(org_id,asset_id,released_at) 前缀，每批 ≤25 次
	// 索引查找，无新迁移。SELECT 与注入之间的新引用由 archiveInject 内
	// assetDeletionState 的在途引用保护兜底。
	if s.options.ArchiveUsageWindowDays > 0 {
		usageCutoff := now.UTC().AddDate(0, 0, -s.options.ArchiveUsageWindowDays)
		q = q.Where("NOT EXISTS (SELECT 1 FROM video_task_asset_refs r WHERE r.org_id = a.org_id AND r.asset_id = a.asset_id AND r.created_at >= ?)", usageCutoff)
	}
	var candidates []Asset
	if err := q.Select("a.*").Order("a.created_at, a.asset_id").Limit(int(batch)).Find(&candidates).Error; err != nil {
		return ErrStorage
	}
	// 单素材注入失败不中断整批（与派发池同语义）：一个坏候选不应阻塞
	// 同批其余素材的归档。
	var firstErr error
	for i := range candidates {
		if err := s.archiveInject(ctx, &candidates[i], now); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// archiveInject 对单个素材完成防护与注入；任何防护不满足都静默跳过
// （rejected 恢复 active 的素材次日窗口自然重试）。
func (s *Service) archiveInject(ctx context.Context, asset *Asset, now time.Time) error {
	var group Group
	if err := s.db.WithContext(ctx).Where("org_id = ? AND group_id = ?", asset.OrgID, asset.GroupID).Take(&group).Error; err != nil {
		return nil
	}
	// 复用既有删除防护链：operation_pending 防重、任务引用未释放、组状态、
	// provider/scope 匹配任一不满足即跳过。
	mode, reason, _, err := s.assetDeletionState(s.db.WithContext(ctx), *asset, group)
	if err != nil || mode != "official" || reason != "" {
		return nil
	}
	runtime, err := s.resolve(ctx, asset.OrgID, Scope{})
	if err != nil || !runtime.ExecutionRetentionConsent {
		return nil
	}
	opID, err := newID("op_")
	if err != nil {
		return ErrStorage
	}
	requestID, err := newID("ar_")
	if err != nil {
		return ErrStorage
	}
	body := bodyJSON(map[string]any{"Id": *asset.ProviderID, "ProjectName": group.Project})
	nowMillisValue := nowMillis(s)
	prepared, err := s.evidence.Prepare(invocation.VideoPayloadInput{OrgID: asset.OrgID, RequestID: requestID, AttemptID: opID, Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeExecution, SchemaVersion: 1, Body: body, ExpiresAt: nowMillisValue.Add(24 * time.Hour), CaptureEnabled: true})
	if err != nil {
		return ErrInvalid
	}
	receipt := bodyJSON(map[string]any{"prior_status": StatusActive, "prior_review_state": ReviewClear})
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, asset.OrgID); err != nil {
			return err
		}
		// 锁内完整重检（TOCTOU 关闭）：上方事务外的 assetDeletionState 只是
		// 省事务的预检——预检通过后、本事务拿锁前，视频任务可能在同一租户锁
		// 下写入 TaskReference 引用本素材。锁内必须重跑完整防护（任务引用、
		// 待决操作、组状态），否则已受理任务的依赖素材会进入官方删除流程。
		var current Asset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND asset_id = ? AND deleted_at IS NULL", asset.OrgID, asset.ID).Take(&current).Error; err != nil {
			return nil
		}
		var lockedGroup Group
		if err := tx.Where("org_id = ? AND group_id = ?", asset.OrgID, asset.GroupID).Take(&lockedGroup).Error; err != nil {
			return nil
		}
		mode, reason, _, err := s.assetDeletionState(tx, current, lockedGroup)
		if err != nil || mode != "official" || reason != "" {
			return nil
		}
		// 乐观锁复核仍为 active 并置 deleting（对齐用户删除路径的受理语义，
		// 官方确认链路 WHERE status='deleting' 才能命中）。
		res := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ? AND status = ? AND deleted_at IS NULL", asset.OrgID, asset.ID, StatusActive).Updates(map[string]any{"status": StatusDeleting, "updated_at": nowMillisValue})
		if res.Error != nil {
			return ErrStorage
		}
		if res.RowsAffected == 0 {
			return nil
		}
		row := Operation{
			ID: opID, OrgID: asset.OrgID, Action: "DeleteAsset", ResourceType: "asset", ResourceID: asset.ID,
			RequestID: requestID, CanonicalPath: "internal:DeleteAsset:" + asset.ID,
			HMACKeyVersion: s.options.ActiveHMACKey,
			// 幂等哈希必须含本次 op id：同素材 rejected 恢复 active 后会再次
			// 进入候选，固定哈希会撞 uk_video_asset_idempotency 唯一键并中断
			// 整批 sweep（2026-11-11 生产事故：两个 rejected 素材把归档永久
			// 停摆）。重注入防重由上方 pending/in_flight/unknown 查询承担。
			IdempotencyHash: s.digest(s.options.ActiveHMACKey, "internal", asset.OrgID, "DeleteAsset", opID),
			BodyHash:        s.digest(s.options.ActiveHMACKey, "body", asset.OrgID, "DeleteAsset", string(body)),
			ScopeID:         group.ScopeID, ScopeVersion: group.ScopeVersion, AccountID: group.AccountID,
			Region: group.Region, Project: group.Project,
			Status: "pending", PayloadID: prepared.PayloadID(),
			Receipt:   string(receipt),
			CreatedAt: nowMillisValue, UpdatedAt: nowMillisValue, NextAttemptAt: nowMillisValue,
		}
		if err := tx.Create(&row).Error; err != nil {
			// 唯一键冲突 = 另一 worker 同时注入同一素材：跳过而非中断整批。
			if strings.Contains(err.Error(), "Duplicate entry") || strings.Contains(err.Error(), "1062") {
				return nil
			}
			return ErrStorage
		}
		if _, err := s.evidence.StageTx(tx, prepared); err != nil {
			return ErrStorage
		}
		if err := inheritOwnerTx(tx, asset.OrgID, "asset", asset.ID, "operation", opID); err != nil {
			return err
		}
		return s.event(tx, asset.OrgID, "asset", asset.ID, opID, "archive_intent", prepared.PayloadID(), "archive")
	})
}
