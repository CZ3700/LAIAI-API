package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
)

func TestRunDailyMaintenanceAndSettingsAPI(t *testing.T) {
	f, db, _, _, _ := setupStage2Server(t)
	now := time.Now().UTC()

	// Seed settled invocations (1 old error, 1 old success, 1 recent success) + settled failed task.
	_ = db.Create([]invocation.Invocation{
		{
			OrgID: 1, APIKeyID: 1, RequestID: "maint-old-ok", Model: "gpt-4o",
			Status: invocation.StatusSuccess, StatusCode: http.StatusOK,
			PromptTokens: 100, CompletionTokens: 40, CostUSD: decimal.RequireFromString("0.25"),
			UserAgent: "ua-old", ErrorBrief: "", CreatedAt: now.AddDate(0, 0, -45),
		},
		{
			OrgID: 1, APIKeyID: 1, RequestID: "maint-old-err", Model: "gpt-4o",
			Status: invocation.StatusUpstreamError, StatusCode: http.StatusBadGateway,
			ErrorBrief: "upstream failed", CreatedAt: now.AddDate(0, 0, -45),
		},
		{
			OrgID: 1, APIKeyID: 1, RequestID: "maint-fresh-ok", Model: "gpt-4o",
			Status: invocation.StatusSuccess, StatusCode: http.StatusOK,
			PromptTokens: 20, CompletionTokens: 10, CostUSD: decimal.RequireFromString("0.05"),
			CreatedAt: now.Add(-2 * time.Hour),
		},
	}).Error
	oldFinished := now.AddDate(0, 0, -40)
	_ = db.Create(&task.Task{
		TaskID: "maint-task-err", OrgID: 1, APIKeyID: 1, Provider: "volcengine",
		Model: "doubao-seedance-2-0", Status: task.StatusFailed, FailReason: "timeout",
		SubmittedAt: oldFinished.Add(-time.Minute), FinishedAt: &oldFinished,
	}).Error

	// Save retention & schedule config via full settings API.
	wSave := adminDo(t, f, http.MethodPost, "/api/v1/settings/full", map[string]any{
		"log_cleanup": map[string]any{
			"log_retention_days":       30,
			"log_row_retention_days":   7,
			"error_log_retention_days": 30,
			"daily_stats_hour":         1,
			"daily_stats_minute":       15,
			"clean_hour":               2,
			"clean_minute":             45,
			"archive_hour":             3,
			"archive_minute":           30,
			"error_clean_hour":         4,
			"error_clean_minute":       0,
			"storage_clean_hour":       4,
			"storage_clean_minute":     30,
		},
		"relay": map[string]any{
			"model_rate_limit_enabled": true,
			"model_rate_limit_rpm":     "120",
			"model_rate_limit_tpm":     "50000",
		},
	}, true)
	if wSave.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/settings/full = %d body=%s", wSave.Code, wSave.Body.String())
	}
	wGet := adminDo(t, f, http.MethodGet, "/api/v1/settings/full", nil, true)
	if wGet.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings/full = %d", wGet.Code)
	}
	var gotCfg map[string]any
	_ = json.Unmarshal(wGet.Body.Bytes(), &gotCfg)
	lc := gotCfg["log_cleanup"].(map[string]any)
	if int(lc["daily_stats_minute"].(float64)) != 15 || int(lc["error_log_retention_days"].(float64)) != 30 {
		t.Fatalf("unexpected saved log_cleanup: %+v", lc)
	}

	// Execute deterministic full maintenance pass.
	sum, skipped, err := f.srv.RunDailyMaintenance(context.Background())
	if err != nil || skipped {
		t.Fatalf("RunDailyMaintenance = (skipped=%v, err=%v)", skipped, err)
	}
	if sum.DailyStatsRows == 0 || sum.ContentCleaned == 0 || sum.TaskErrorsDeleted == 0 || sum.ArchivedRows == 0 {
		t.Fatalf("unexpected maintenance summary (expected stats, cleanup, task err, archive > 0): %+v", sum)
	}

	// Verify API triggers return 200.
	wTrig := adminDo(t, f, http.MethodPost, "/api/v1/settings/cron/trigger-maintenance", nil, true)
	if wTrig.Code != http.StatusOK {
		t.Fatalf("trigger-maintenance expected 200, got %d body=%s", wTrig.Code, wTrig.Body.String())
	}
	wSync := adminDo(t, f, http.MethodPost, "/api/v1/settings/usage-stats/sync?start_date=2026-11-01&end_date=2026-11-27", nil, true)
	if wSync.Code != http.StatusOK {
		t.Fatalf("usage-stats sync expected 200, got %d body=%s", wSync.Code, wSync.Body.String())
	}
}
