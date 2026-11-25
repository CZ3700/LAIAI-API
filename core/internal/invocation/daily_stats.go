package invocation

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UsageDailyStat stores aggregated invocation counters per UTC day, org, and model.
type UsageDailyStat struct {
	StatDate         string          `gorm:"primaryKey;column:stat_date"`
	OrgID            uint64          `gorm:"primaryKey;column:org_id"`
	Model            string          `gorm:"primaryKey;column:model"`
	RequestCount     int64           `gorm:"column:request_count"`
	SuccessCount     int64           `gorm:"column:success_count"`
	PromptTokens     int64           `gorm:"column:prompt_tokens"`
	CompletionTokens int64           `gorm:"column:completion_tokens"`
	CachedTokens     int64           `gorm:"column:cached_tokens"`
	ReasoningTokens  int64           `gorm:"column:reasoning_tokens"`
	CostUSD          decimal.Decimal `gorm:"column:cost_usd"`
}

func (UsageDailyStat) TableName() string { return "usage_daily_stats" }

type dailyStatKey struct {
	date  string
	orgID uint64
	model string
}

// AggregateDailyStats folds a batch of invocations into sorted daily rollup rows.
func AggregateDailyStats(batch []*Invocation) []UsageDailyStat {
	if len(batch) == 0 {
		return nil
	}
	acc := make(map[dailyStatKey]*UsageDailyStat, len(batch))
	for _, inv := range batch {
		if inv == nil {
			continue
		}
		ts := inv.CreatedAt
		if ts.IsZero() {
			ts = time.Now().UTC()
		} else {
			ts = ts.UTC()
		}
		key := dailyStatKey{
			date:  ts.Format("2006-01-02"),
			orgID: inv.OrgID,
			model: inv.Model,
		}
		row, ok := acc[key]
		if !ok {
			row = &UsageDailyStat{
				StatDate: key.date,
				OrgID:    key.orgID,
				Model:    key.model,
			}
			acc[key] = row
		}
		row.RequestCount++
		if inv.Status == StatusSuccess {
			row.SuccessCount++
		}
		row.PromptTokens += inv.PromptTokens
		row.CompletionTokens += inv.CompletionTokens
		row.CachedTokens += inv.CachedTokens
		row.ReasoningTokens += inv.ReasoningTokens
		row.CostUSD = row.CostUSD.Add(inv.CostUSD)
	}
	if len(acc) == 0 {
		return nil
	}
	out := make([]UsageDailyStat, 0, len(acc))
	for _, row := range acc {
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b UsageDailyStat) int {
		if c := cmp.Compare(a.StatDate, b.StatDate); c != 0 {
			return c
		}
		if c := cmp.Compare(a.OrgID, b.OrgID); c != 0 {
			return c
		}
		return cmp.Compare(a.Model, b.Model)
	})
	return out
}

// UpsertDailyStats accumulates daily rollups atomically across PostgreSQL and SQLite.
func UpsertDailyStats(db *gorm.DB, batch []*Invocation) error {
	rows := AggregateDailyStats(batch)
	if len(rows) == 0 || db == nil {
		return nil
	}
	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "stat_date"}, {Name: "org_id"}, {Name: "model"}},
		DoUpdates: clause.Assignments(map[string]any{
			"request_count":     gorm.Expr("usage_daily_stats.request_count + excluded.request_count"),
			"success_count":     gorm.Expr("usage_daily_stats.success_count + excluded.success_count"),
			"prompt_tokens":     gorm.Expr("usage_daily_stats.prompt_tokens + excluded.prompt_tokens"),
			"completion_tokens": gorm.Expr("usage_daily_stats.completion_tokens + excluded.completion_tokens"),
			"cached_tokens":     gorm.Expr("usage_daily_stats.cached_tokens + excluded.cached_tokens"),
			"reasoning_tokens":  gorm.Expr("usage_daily_stats.reasoning_tokens + excluded.reasoning_tokens"),
			"cost_usd":          gorm.Expr("usage_daily_stats.cost_usd + excluded.cost_usd"),
		}),
	}).Create(&rows).Error
	if err != nil {
		return fmt.Errorf("invocation: upsert daily stats: %w", err)
	}
	return nil
}

func isMissingDailyStatsTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "does not exist")
}
