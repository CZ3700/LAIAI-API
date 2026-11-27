package invocation

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestDailyStatsSyncContentErrorAndArchivePipeline(t *testing.T) {
	t.Parallel()
	db := setup(t)
	ctx := context.Background()
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC)

	seed := []Invocation{
		{
			OrgID: 1, APIKeyID: 10, RequestID: "old-ok", Model: "gpt-4o",
			Status: StatusSuccess, StatusCode: http.StatusOK,
			PromptTokens: 100, CompletionTokens: 50, CostUSD: decimal.RequireFromString("0.5"),
			ErrorBrief: "stale detail", UserAgent: "ua", Referer: "ref",
			CreatedAt: now.AddDate(0, 0, -40),
		},
		{
			OrgID: 1, APIKeyID: 10, RequestID: "old-err", Model: "gpt-4o",
			Status: StatusUpstreamError, StatusCode: http.StatusBadGateway,
			PromptTokens: 10, CompletionTokens: 0, CostUSD: decimal.Zero,
			ErrorBrief: "bad gateway",
			CreatedAt: now.AddDate(0, 0, -40),
		},
		{
			OrgID: 1, APIKeyID: 10, RequestID: "recent-ok", Model: "gpt-4o",
			Status: StatusSuccess, StatusCode: http.StatusOK,
			PromptTokens: 200, CompletionTokens: 80, CostUSD: decimal.RequireFromString("0.8"),
			CreatedAt: now.Add(-24 * time.Hour),
		},
	}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed invocations: %v", err)
	}

	// Step 1: backfill / sync daily stats
	upserted, err := BackfillDailyStatsOnStartup(ctx, db, loc, now)
	if err != nil || upserted == 0 {
		t.Fatalf("backfill = (%d, %v), want >0 rows", upserted, err)
	}

	// Step 2: clean heavy detail fields older than 30 days
	cleaned, err := CleanupInvocationContent(ctx, db, now.AddDate(0, 0, -30), 100)
	if err != nil || cleaned != 2 {
		t.Fatalf("CleanupInvocationContent = (%d, %v), want 2", cleaned, err)
	}
	var refreshed Invocation
	if err := db.Where("request_id = ?", "old-ok").First(&refreshed).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.UserAgent != "" || refreshed.ErrorBrief != "" {
		t.Fatalf("expected detail fields cleared, got ua=%q brief=%q", refreshed.UserAgent, refreshed.ErrorBrief)
	}

	// Step 3: archive rows older than (7 + 2) days into invocations_archive
	archived, err := ArchiveOldInvocations(ctx, db, loc, now, 7, 100)
	if err != nil || archived != 2 {
		t.Fatalf("ArchiveOldInvocations = (%d, %v), want 2", archived, err)
	}
	var hotCnt, archCnt int64
	_ = db.Model(&Invocation{}).Count(&hotCnt).Error
	_ = db.Model(&InvocationArchive{}).Count(&archCnt).Error
	if hotCnt != 1 || archCnt != 2 {
		t.Fatalf("counts after archive: hot=%d arch=%d, want 1/2", hotCnt, archCnt)
	}

	// Step 4: cleanup error logs in both invocations and invocations_archive
	hotDel, archDel, err := CleanupExpiredErrorLogs(ctx, db, now.AddDate(0, 0, -30), 100)
	if err != nil || hotDel != 0 || archDel != 1 {
		t.Fatalf("CleanupExpiredErrorLogs = (hot=%d, arch=%d, err=%v), want 0/1", hotDel, archDel, err)
	}
}
