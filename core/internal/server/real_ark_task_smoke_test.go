package server

// 真机任务链路 smoke（§3.8 fixture 纪律的平台级环节）：通过本平台 /v1/video/generations
// 提交真实 Doubao Seedance 任务 → 轮询到出片 → 断言计费闭环（hold 清零、按真实 usage 结算）。
// env 门控：未设 LAIAI_SMOKE_ARK_KEY 时自动 skip（CI 零影响）。KEY 只走环境变量，绝不落文件。
//
//	LAIAI_SMOKE_ARK_KEY=ark-... [LAIAI_SMOKE_ARK_URL=https://ark.cn-beijing.volces.com/api/v3]
//	[LAIAI_SMOKE_ARK_MODEL=doubao-seedance-2-0-260128] \
//	go test ./internal/server/ -run TestRealArkTaskSmoke -count=1 -v -timeout 10m

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
)

func TestRealArkTaskSmoke(t *testing.T) {
	arkKey := os.Getenv("LAIAI_SMOKE_ARK_KEY")
	if arkKey == "" {
		t.Skip("LAIAI_SMOKE_ARK_KEY not set — skipping real ARK task smoke")
	}
	arkURL := os.Getenv("LAIAI_SMOKE_ARK_URL")
	if arkURL == "" {
		arkURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	model := os.Getenv("LAIAI_SMOKE_ARK_MODEL")
	if model == "" {
		model = "doubao-seedance-2-0-260128"
	}

	srv, db := setupRelayServer(t)
	// doubao 档案（2026-07-05 新增）：国内方舟端点 + volcengine 任务家族回退
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ark-real", "provider": "doubao", "base_url": arkURL,
		"api_key": arkKey, "models": []string{model},
	})
	if w.Code != 201 {
		t.Fatalf("create doubao channel = %d %v", w.Code, out)
	}
	// 真实定价口径（豆包 seedance 视频 token 约 ¥0.015/千token ≈ $2.1/1M；此处用 $2/1M 近似）
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "0", "output_usd_per_1m": "2",
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	svc := task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	apiKey, orgID := seedUserWithCredit(t, srv, "ark-smoke@example.com", "10")

	// 最小成本真任务：4s / 16:9 / 默认 720p / 无音频 / 无水印（真机标定 ≈87k tokens）
	w, submitOut := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model":   model,
		"content": []map[string]any{{"type": "text", "text": "a red apple rolls slowly on a wooden table, soft daylight"}},
		"duration": 4, "ratio": "16:9", "generate_audio": false, "watermark": false,
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("real submit = %d %v", w.Code, submitOut)
	}
	taskID := submitOut["id"].(string)
	t.Logf("submitted real task %s", taskID)

	// hold 生效（4s 无显式 resolution → 1080p 保守基准 252,720 tokens × $2/1M ≈ $0.505）
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsPositive() {
		t.Fatalf("no hold after real submit: held=%s", acc.HeldAmount)
	}

	// 轮询到终态（真实出片 2-5 分钟）
	deadline := time.Now().Add(8 * time.Minute)
	var final map[string]any
	for time.Now().Before(deadline) {
		svc.PollOnce(context.Background())
		w, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
		if w.Code != 200 {
			t.Fatalf("get task = %d %v", w.Code, got)
		}
		st, _ := got["status"].(string)
		if st == task.StatusSucceeded || st == task.StatusFailed {
			final = got
			break
		}
		time.Sleep(10 * time.Second)
	}
	if final == nil {
		t.Fatal("task did not reach terminal state within deadline")
	}
	if final["status"] != task.StatusSucceeded {
		t.Fatalf("real task failed: %v", final)
	}
	if url, _ := final["result_url"].(string); url == "" {
		t.Fatal("succeeded but no result_url")
	}
	tokens, _ := final["completion_tokens"].(float64)
	if tokens <= 0 {
		t.Fatalf("no real usage captured: %v", final["completion_tokens"])
	}
	// 计费闭环：hold 清零、cost>0、余额减少 = cost
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("hold not released after settle: %s", acc.HeldAmount)
	}
	cost := decimal.RequireFromString("10").Sub(acc.PostedBalance)
	if !cost.IsPositive() {
		t.Fatalf("no cost charged: balance=%s", acc.PostedBalance)
	}
	t.Logf("real task succeeded: tokens=%v cost=$%s result_url=%.60s...", tokens, cost, final["result_url"])
}
