package invocation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DailySyncTracker prevents duplicate automatic daily stats runs on the same local date.
type DailySyncTracker struct {
	lastYMD atomic.Int32
}

var defaultDailyTracker DailySyncTracker

func localYMD(t time.Time) int32 {
	return int32(t.Year()*10000 + int(t.Month())*100 + t.Day())
}

// SyncDailyStatsAt runs an idempotent 3-day rolling usage rollup up through today in loc.
func SyncDailyStatsAt(ctx context.Context, db *gorm.DB, loc *time.Location, now time.Time, force bool, tracker *DailySyncTracker) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if tracker == nil {
		tracker = &defaultDailyTracker
	}
	loc = resolveSyncLoc(loc)
	localNow := now.In(loc)
	todayNum := localYMD(localNow)
	if !force && tracker.lastYMD.Load() == todayNum {
		return 0, nil
	}
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -3)
	end := today.AddDate(0, 0, 1)
	rows, err := SyncDailyStatsRange(ctx, db, loc, start, end)
	if err != nil {
		return rows, err
	}
	tracker.lastYMD.Store(todayNum)
	return rows, nil
}

// BackfillDailyStatsOnStartup backfills missing historical days in usage_daily_stats once on leader startup.
func BackfillDailyStatsOnStartup(ctx context.Context, db *gorm.DB, loc *time.Location, now time.Time) (int64, error) {
	if db == nil {
		return 0, nil
	}
	loc = resolveSyncLoc(loc)
	localNow := now.In(loc)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	yesterday := today.AddDate(0, 0, -1)

	var latest UsageDailyStat
	err := db.WithContext(ctx).Order("stat_date DESC").Limit(1).Take(&latest).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		if isMissingDailyStatsTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("invocation: query latest stat_date: %w", err)
	}

	var startDay time.Time
	if err == nil && latest.StatDate != "" {
		if latest.StatDate >= yesterday.Format("2006-01-02") {
			return 0, nil
		}
		parsed, perr := time.ParseInLocation("2006-01-02", latest.StatDate, loc)
		if perr == nil {
			startDay = parsed.AddDate(0, 0, -1)
		}
	}
	if startDay.IsZero() {
		earliest, hasMin, merr := earliestInvocationTime(ctx, db)
		if merr != nil || !hasMin {
			return 0, merr
		}
		el := earliest.In(loc)
		startDay = time.Date(el.Year(), el.Month(), el.Day(), 0, 0, 0, 0, loc)
	}
	return SyncDailyStatsRange(ctx, db, loc, startDay, today.AddDate(0, 0, 1))
}

// ManualSyncUsageStats parses optional YYYY-MM-DD start/end bounds and recalculates daily rollups.
func ManualSyncUsageStats(ctx context.Context, db *gorm.DB, loc *time.Location, now time.Time, startStr, endStr string) (int64, error) {
	loc = resolveSyncLoc(loc)
	localNow := now.In(loc)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -30)
	end := today
	if s := parseSyncDay(startStr, loc); !s.IsZero() {
		start = s
	}
	if e := parseSyncDay(endStr, loc); !e.IsZero() {
		end = e
	}
	if end.Before(start) {
		end = start
	}
	return SyncDailyStatsRange(ctx, db, loc, start, end.AddDate(0, 0, 1))
}

// SyncDailyStatsRange recomputes per-day rollups idempotently for local calendar days [startDay, endDay).
func SyncDailyStatsRange(ctx context.Context, db *gorm.DB, loc *time.Location, startDay, endDay time.Time) (int64, error) {
	if db == nil {
		return 0, nil
	}
	loc = resolveSyncLoc(loc)
	start := time.Date(startDay.In(loc).Year(), startDay.In(loc).Month(), startDay.In(loc).Day(), 0, 0, 0, 0, loc)
	end := time.Date(endDay.In(loc).Year(), endDay.In(loc).Month(), endDay.In(loc).Day(), 0, 0, 0, 0, loc)
	if !start.Before(end) {
		return 0, nil
	}

	earliest, hasMin, err := earliestInvocationTime(ctx, db)
	if err != nil {
		return 0, err
	}
	var totalUpserted int64
	for cur := start; cur.Before(end); cur = cur.AddDate(0, 0, 1) {
		if ctx.Err() != nil {
			return totalUpserted, ctx.Err()
		}
		dayEnd := cur.AddDate(0, 0, 1)
		// Never overwrite historical stats with zero if raw hot rows for that day were already archived.
		if !hasMin || !dayEnd.After(earliest) {
			continue
		}
		n, err := syncSingleDayStats(ctx, db, cur, cur.UTC(), dayEnd.UTC())
		if err != nil {
			return totalUpserted, err
		}
		totalUpserted += n
		if dayEnd.Before(end) {
			if err := sleepWithContext(ctx, 20*time.Millisecond); err != nil {
				return totalUpserted, err
			}
		}
	}
	return totalUpserted, nil
}

func syncSingleDayStats(ctx context.Context, db *gorm.DB, localDay time.Time, startUTC, endUTC time.Time) (int64, error) {
	type aggRow struct {
		OrgID            uint64          `gorm:"column:org_id"`
		Model            string          `gorm:"column:model"`
		RequestCount     int64           `gorm:"column:request_count"`
		SuccessCount     int64           `gorm:"column:success_count"`
		PromptTokens     int64           `gorm:"column:prompt_tokens"`
		CompletionTokens int64           `gorm:"column:completion_tokens"`
		CachedTokens     int64           `gorm:"column:cached_tokens"`
		ReasoningTokens  int64           `gorm:"column:reasoning_tokens"`
		CostUSD          decimal.Decimal `gorm:"column:cost_usd"`
	}
	var aggs []aggRow
	err := db.WithContext(ctx).Model(&Invocation{}).
		Select(`org_id, model, COUNT(*) AS request_count,
			SUM(CASE WHEN status = ? THEN 1 ELSE 0 END) AS success_count,
			COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
			COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
			COALESCE(SUM(cost_usd), 0) AS cost_usd`, StatusSuccess).
		Where("created_at >= ? AND created_at < ?", startUTC, endUTC).
		Group("org_id, model").
		Scan(&aggs).Error
	if err != nil {
		return 0, fmt.Errorf("invocation: aggregate day %s: %w", localDay.Format("2006-01-02"), err)
	}
	if len(aggs) == 0 {
		return 0, nil
	}
	statDate := localDay.Format("2006-01-02")
	stats := make([]UsageDailyStat, 0, len(aggs))
	for _, r := range aggs {
		stats = append(stats, UsageDailyStat{
			StatDate:         statDate,
			OrgID:            r.OrgID,
			Model:            r.Model,
			RequestCount:     r.RequestCount,
			SuccessCount:     r.SuccessCount,
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			CachedTokens:     r.CachedTokens,
			ReasoningTokens:  r.ReasoningTokens,
			CostUSD:          r.CostUSD,
		})
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "stat_date"}, {Name: "org_id"}, {Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"request_count", "success_count", "prompt_tokens",
			"completion_tokens", "cached_tokens", "reasoning_tokens", "cost_usd",
		}),
	}).Create(&stats).Error; err != nil {
		return 0, fmt.Errorf("invocation: sync upsert day %s: %w", statDate, err)
	}
	return int64(len(stats)), nil
}

func earliestInvocationTime(ctx context.Context, db *gorm.DB) (time.Time, bool, error) {
	var first Invocation
	err := db.WithContext(ctx).Select("created_at").Order("created_at ASC, id ASC").Limit(1).Take(&first).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("invocation: query earliest invocation: %w", err)
	}
	return first.CreatedAt.UTC(), true, nil
}

func parseSyncDay(raw string, loc *time.Location) time.Time {
	v := cmp.Or(raw, "")
	if len(v) > 10 {
		v = v[:10]
	}
	t, err := time.ParseInLocation("2006-01-02", v, loc)
	if err != nil {
		return time.Time{}
	}
	return t
}

func resolveSyncLoc(loc *time.Location) *time.Location {
	if loc != nil {
		return loc
	}
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}
