package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

const v1LogCleanupSettingKey = "log_cleanup_settings"

// LogCleanupSettings mirrors tkeapi's log retention and staggered daily maintenance schedule.
type LogCleanupSettings struct {
	LogRetentionDays      int `json:"log_retention_days"`
	LogRowRetentionDays   int `json:"log_row_retention_days"`
	ErrorLogRetentionDays int `json:"error_log_retention_days"`

	DailyStatsHour     int `json:"daily_stats_hour"`
	DailyStatsMinute   int `json:"daily_stats_minute"`
	CleanHour          int `json:"clean_hour"`
	CleanMinute        int `json:"clean_minute"`
	ArchiveHour        int `json:"archive_hour"`
	ArchiveMinute      int `json:"archive_minute"`
	ErrorCleanHour     int `json:"error_clean_hour"`
	ErrorCleanMinute   int `json:"error_clean_minute"`
	StorageCleanHour   int `json:"storage_clean_hour"`
	StorageCleanMinute int `json:"storage_clean_minute"`
	MaintenanceHour    int `json:"maintenance_hour"`
	MaintenanceMinute  int `json:"maintenance_minute"`
}

// MaintenanceSummary summarizes rows affected by a daily maintenance pass.
type MaintenanceSummary struct {
	DailyStatsRows    int64 `json:"daily_stats_rows"`
	ContentCleaned    int64 `json:"content_cleaned"`
	InvErrorsDeleted  int64 `json:"inv_errors_deleted"`
	ArchErrorsDeleted int64 `json:"arch_errors_deleted"`
	TaskErrorsDeleted int64 `json:"task_errors_deleted"`
	ArchivedRows      int64 `json:"archived_rows"`
	ArkAssetsDeleted  int64 `json:"ark_assets_deleted"`
	SyncLogsDeleted   int64 `json:"sync_logs_deleted"`
	OutboxPurged      int64 `json:"outbox_purged"`
}

func defaultLogCleanupSettings(invDefault int) LogCleanupSettings {
	ret := invDefault
	if ret <= 0 {
		ret = 30
	}
	return LogCleanupSettings{
		LogRetentionDays:      ret,
		LogRowRetentionDays:   0,
		ErrorLogRetentionDays: 0,
		DailyStatsHour:        1,
		DailyStatsMinute:      0,
		CleanHour:             2,
		CleanMinute:           30,
		ArchiveHour:           3,
		ArchiveMinute:         30,
		ErrorCleanHour:        4,
		ErrorCleanMinute:      0,
		StorageCleanHour:      4,
		StorageCleanMinute:    30,
		MaintenanceHour:       3,
		MaintenanceMinute:     0,
	}
}

func clampHM(h, m int) (int, int) {
	if h < 0 {
		h = 0
	}
	if m < 0 {
		m = 0
	}
	return h % 24, m % 60
}

// SafeDailyStatsHM returns bounds-checked (hour, minute) for DailyStatsSync.
func (c LogCleanupSettings) SafeDailyStatsHM() (int, int) {
	return clampHM(c.DailyStatsHour, c.DailyStatsMinute)
}

// SafeCleanHM returns bounds-checked (hour, minute) for LogContentCleanup.
func (c LogCleanupSettings) SafeCleanHM() (int, int) {
	return clampHM(c.CleanHour, c.CleanMinute)
}

// SafeArchiveHM returns bounds-checked (hour, minute) for LogRowArchive.
func (c LogCleanupSettings) SafeArchiveHM() (int, int) {
	return clampHM(c.ArchiveHour, c.ArchiveMinute)
}

// SafeErrorCleanHM returns bounds-checked (hour, minute) for ErrorLogsCleanup.
func (c LogCleanupSettings) SafeErrorCleanHM() (int, int) {
	return clampHM(c.ErrorCleanHour, c.ErrorCleanMinute)
}

// SafeStorageCleanHM returns bounds-checked (hour, minute) for StorageAndPluginCleanup.
func (c LogCleanupSettings) SafeStorageCleanHM() (int, int) {
	return clampHM(c.StorageCleanHour, c.StorageCleanMinute)
}

// LoadLogCleanupSettings loads persisted maintenance/retention config with defaults and ext_settings fallback.
func (s *Server) LoadLogCleanupSettings() LogCleanupSettings {
	cfg := defaultLogCleanupSettings(s.invRetentionDefault)
	if raw := strings.TrimSpace(s.getSetting(v1LogCleanupSettingKey)); raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	} else if raw := strings.TrimSpace(s.getSetting(v1FullSettingsKey)); raw != "" {
		var full struct {
			LogCleanup *LogCleanupSettings `json:"log_cleanup"`
		}
		if json.Unmarshal([]byte(raw), &full) == nil && full.LogCleanup != nil {
			cfg = *full.LogCleanup
		}
	}
	if s.getSetting("log_retention_days") != "" {
		cfg.LogRetentionDays = s.retentionDaysSetting("log_retention_days", cfg.LogRetentionDays)
	}
	if s.getSetting("log_row_retention_days") != "" {
		cfg.LogRowRetentionDays = s.retentionDaysSetting("log_row_retention_days", cfg.LogRowRetentionDays)
	}
	if s.getSetting("error_log_retention_days") != "" {
		cfg.ErrorLogRetentionDays = s.retentionDaysSetting("error_log_retention_days", cfg.ErrorLogRetentionDays)
	}
	return cfg
}

// SiteLocation returns the configured site display timezone, falling back to Asia/Shanghai.
func (s *Server) SiteLocation() *time.Location {
	if raw := strings.TrimSpace(s.getSetting(v1FullSettingsKey)); raw != "" {
		var saved struct {
			Site struct {
				DefaultTimezone string `json:"default_timezone"`
			} `json:"site"`
		}
		if json.Unmarshal([]byte(raw), &saved) == nil && strings.TrimSpace(saved.Site.DefaultTimezone) != "" {
			if loc, err := time.LoadLocation(strings.TrimSpace(saved.Site.DefaultTimezone)); err == nil {
				return loc
			}
		}
	}
	return s.siteLocation()
}

// ScheduleChangedChan returns a channel closed whenever site timezone or daily cron schedules change.
func (s *Server) ScheduleChangedChan() <-chan struct{} {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	if s.scheduleCh == nil {
		s.scheduleCh = make(chan struct{})
	}
	return s.scheduleCh
}

func (s *Server) notifyScheduleChanged() {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	if s.scheduleCh != nil {
		close(s.scheduleCh)
	}
	s.scheduleCh = make(chan struct{})
}

// SyncDailyUsageStats updates usage_daily_stats for the rolling last 3 local days through today.
func (s *Server) SyncDailyUsageStats(ctx context.Context, force bool) (int64, error) {
	return invocation.SyncDailyStatsAt(ctx, s.db, s.siteLocation(), s.now(), force, &s.dailySyncTracker)
}

// BackfillDailyUsageStats backfills missing historical usage_daily_stats rows on leader startup.
func (s *Server) BackfillDailyUsageStats(ctx context.Context) (int64, error) {
	return invocation.BackfillDailyStatsOnStartup(ctx, s.db, s.siteLocation(), s.now())
}

// ManualSyncUsageStats recalculates usage_daily_stats across [startDate, endDate] inclusive.
func (s *Server) ManualSyncUsageStats(ctx context.Context, startDate, endDate string) (int64, error) {
	return invocation.ManualSyncUsageStats(ctx, s.db, s.siteLocation(), s.now(), startDate, endDate)
}

// CleanupLogContent strips heavy detail payloads/texts older than days while keeping rows for billing stats.
func (s *Server) CleanupLogContent(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := s.now().UTC().AddDate(0, 0, -days)
	cleaned, err := invocation.CleanupInvocationContent(ctx, s.db, cutoff, 5000)
	if err != nil {
		return cleaned, err
	}
	nTasks, err := cleanupSettledTaskDetails(ctx, s.db, cutoff)
	if err != nil {
		return cleaned, err
	}
	cleaned += nTasks
	nArk, err := cleanupArkRawResponses(ctx, s.db, cutoff)
	if err != nil {
		return cleaned, err
	}
	return cleaned + nArk, nil
}

func cleanupSettledTaskDetails(ctx context.Context, db *gorm.DB, cutoff time.Time) (int64, error) {
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		var ids []uint64
		err := db.WithContext(ctx).Table("tasks").
			Where("finished_at IS NOT NULL AND finished_at < ? AND (request_meta_json <> '{}' OR billing_detail <> '' OR fail_reason <> '')", cutoff).
			Order("id ASC").Limit(2000).Pluck("id", &ids).Error
		if err != nil {
			if isMissingTableErr(err) {
				return total, nil
			}
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := db.WithContext(ctx).Table("tasks").Where("id IN ?", ids).Updates(map[string]any{
			"request_meta_json": "{}",
			"billing_detail":    "",
			"fail_reason":       "",
		})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < 2000 {
			return total, nil
		}
	}
}

func cleanupArkRawResponses(ctx context.Context, db *gorm.DB, cutoff time.Time) (int64, error) {
	res := db.WithContext(ctx).Model(&ark.VideoTask{}).
		Where("synced_at < ? AND raw_response <> '{}' AND raw_response <> ''", cutoff).
		Update("raw_response", "{}")
	if res.Error != nil {
		if isMissingTableErr(res.Error) {
			return 0, nil
		}
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// CleanupErrorLogs deletes settled expired error rows across invocations, invocations_archive, and tasks.
func (s *Server) CleanupErrorLogs(ctx context.Context, days int) (invHot, invArch, taskErr int64, err error) {
	if days <= 0 {
		return 0, 0, 0, nil
	}
	cutoff := s.now().UTC().AddDate(0, 0, -days)
	invHot, invArch, err = invocation.CleanupExpiredErrorLogs(ctx, s.db, cutoff, 5000)
	if err != nil {
		return invHot, invArch, 0, err
	}
	for _, st := range []string{task.StatusFailed, task.StatusCancelled} {
		n, terr := purgeRowsOlderThan(s.db.WithContext(ctx), "tasks", "id", "finished_at", cutoff, 1000, st)
		if terr != nil && !isMissingTableErr(terr) {
			return invHot, invArch, taskErr, terr
		}
		taskErr += n
	}
	return invHot, invArch, taskErr, nil
}

// ArchiveOldLogs moves cold invocations older than (rowDays + 2) days into invocations_archive.
func (s *Server) ArchiveOldLogs(ctx context.Context, rowDays int) (int64, error) {
	return invocation.ArchiveOldInvocations(ctx, s.db, s.siteLocation(), s.now(), rowDays, 2000)
}

// CleanupStorageAndPluginLogs cleans expired Ark assets, channel sync logs, notify outbox, and configured audit/task retention.
func (s *Server) CleanupStorageAndPluginLogs(ctx context.Context) (arkDeleted, syncLogsDeleted, outboxPurged int64, err error) {
	if n, aerr := s.CleanupExpiredArkAssets(ctx); aerr != nil {
		if !isMissingTableErr(aerr) {
			return 0, 0, 0, aerr
		}
	} else {
		arkDeleted = int64(n)
	}
	syncCutoff := s.now().UTC().AddDate(0, 0, -30)
	if n, serr := purgeRowsOlderThan(s.db.WithContext(ctx), "channel_config_sync_logs", "id", "created_at", syncCutoff, 1000, ""); serr != nil {
		if !isMissingTableErr(serr) {
			return arkDeleted, 0, 0, serr
		}
	} else {
		syncLogsDeleted = n
	}
	if n, oerr := s.PurgeNotifyOutbox(ctx); oerr != nil {
		if !isMissingTableErr(oerr) {
			return arkDeleted, syncLogsDeleted, 0, oerr
		}
	} else {
		outboxPurged = n
	}
	if d := s.retentionDaysSetting("audit_log_retention_days", 0); d > 0 {
		cutoffMs := s.now().UTC().AddDate(0, 0, -d).UnixMilli()
		if _, aerr := purgeAuditLogsOlderThan(s.db.WithContext(ctx), cutoffMs, 1000); aerr != nil && !isMissingTableErr(aerr) {
			return arkDeleted, syncLogsDeleted, outboxPurged, aerr
		}
	}
	if d := s.retentionDaysSetting("task_log_retention_days", 30); d > 0 {
		cutoff := s.now().UTC().AddDate(0, 0, -d)
		for _, status := range []string{task.StatusSucceeded, task.StatusFailed, task.StatusCancelled} {
			if _, terr := purgeRowsOlderThan(s.db.WithContext(ctx), "tasks", "id", "finished_at", cutoff, 1000, status); terr != nil && !isMissingTableErr(terr) {
				return arkDeleted, syncLogsDeleted, outboxPurged, terr
			}
		}
	}
	return arkDeleted, syncLogsDeleted, outboxPurged, nil
}

// RunDailyMaintenance executes the full tkeapi-aligned system maintenance pipeline in safe order:
// 1. Force daily stats rollup -> 2. Clean log detail fields -> 3. Delete expired errors ->
// 4. Archive cold invocation rows -> 5. Clean temporary storage, sync logs & outbox.
func (s *Server) RunDailyMaintenance(ctx context.Context) (MaintenanceSummary, bool, error) {
	var sum MaintenanceSummary
	if !s.maintenanceRunning.CompareAndSwap(false, true) {
		return sum, true, nil
	}
	defer s.maintenanceRunning.Store(false)

	var firstErr error
	recordErr := func(step string, err error) {
		if err == nil {
			return
		}
		s.Log().Warn("maintenance step failed", "step", step, "err", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	var err error
	sum.DailyStatsRows, err = s.SyncDailyUsageStats(ctx, true)
	recordErr("daily_stats_sync", err)

	cfg := s.LoadLogCleanupSettings()

	sum.ContentCleaned, err = s.CleanupLogContent(ctx, cfg.LogRetentionDays)
	recordErr("log_content_cleanup", err)

	sum.InvErrorsDeleted, sum.ArchErrorsDeleted, sum.TaskErrorsDeleted, err = s.CleanupErrorLogs(ctx, cfg.ErrorLogRetentionDays)
	recordErr("error_logs_cleanup", err)

	sum.ArchivedRows, err = s.ArchiveOldLogs(ctx, cfg.LogRowRetentionDays)
	recordErr("log_row_archive", err)

	sum.ArkAssetsDeleted, sum.SyncLogsDeleted, sum.OutboxPurged, err = s.CleanupStorageAndPluginLogs(ctx)
	recordErr("storage_and_plugin_cleanup", err)

	return sum, false, firstErr
}

func isMissingTableErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return errors.Is(err, gorm.ErrRecordNotFound) ||
		strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "does not exist")
}
