package invocation

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestUpsertDailyStatsAccumulatesAcrossFlushes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	err = db.Exec(`CREATE TABLE usage_daily_stats (
		stat_date TEXT NOT NULL,
		org_id INTEGER NOT NULL,
		model TEXT NOT NULL,
		request_count INTEGER NOT NULL DEFAULT 0,
		success_count INTEGER NOT NULL DEFAULT 0,
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		reasoning_tokens INTEGER NOT NULL DEFAULT 0,
		cost_usd NUMERIC NOT NULL DEFAULT 0,
		PRIMARY KEY (stat_date, org_id, model)
	)`).Error
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	ts := time.Date(2026, 11, 25, 14, 30, 0, 0, time.UTC)
	batch1 := []*Invocation{
		{
			OrgID:            1,
			Model:            "gpt-5",
			Status:           StatusSuccess,
			PromptTokens:     100,
			CompletionTokens: 50,
			CachedTokens:     20,
			ReasoningTokens:  10,
			CostUSD:          decimal.RequireFromString("0.0125"),
			CreatedAt:        ts,
		},
		{
			OrgID:     1,
			Model:     "gpt-5",
			Status:    StatusUpstreamError,
			CostUSD:   decimal.Zero,
			CreatedAt: ts.Add(time.Minute),
		},
	}
	if err := UpsertDailyStats(db, batch1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	batch2 := []*Invocation{
		{
			OrgID:            1,
			Model:            "gpt-5",
			Status:           StatusSuccess,
			PromptTokens:     40,
			CompletionTokens: 10,
			CostUSD:          decimal.RequireFromString("0.0075"),
			CreatedAt:        ts.Add(2 * time.Hour),
		},
	}
	if err := UpsertDailyStats(db, batch2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var got UsageDailyStat
	if err := db.Where("stat_date = ? AND org_id = ? AND model = ?", "2026-11-25", 1, "gpt-5").Take(&got).Error; err != nil {
		t.Fatalf("query stat: %v", err)
	}
	if got.RequestCount != 3 || got.SuccessCount != 2 {
		t.Fatalf("counts = (%d, %d), want (3, 2)", got.RequestCount, got.SuccessCount)
	}
	if got.PromptTokens != 140 || got.CompletionTokens != 60 || got.CachedTokens != 20 || got.ReasoningTokens != 10 {
		t.Fatalf("tokens mismatch: %+v", got)
	}
	if got.CostUSD.StringFixed(4) != "0.0200" {
		t.Fatalf("cost_usd = %s, want 0.0200", got.CostUSD.String())
	}
}
