package server

// §3.8 批次 2（kling/vidu）真机 smoke——env 门控（无 key 自动 skip，CI 零影响）。
// 知识移植自 New API，代码/单测已就绪但字段是参考推导；这些 smoke 是真机验证入口，
// 真实 key 一到即一条命令验证全链路。KEY 只走环境变量，绝不落文件。
//
//	# Kling（凭证格式 access_key|secret_key）：
//	LAIAI_SMOKE_KLING_KEY='ak|sk' [LAIAI_SMOKE_KLING_URL=...] [LAIAI_SMOKE_KLING_MODEL=kling-v1] \
//	  go test ./internal/server/ -run TestRealKlingTaskSmoke -count=1 -v -timeout 12m
//	# Vidu：
//	LAIAI_SMOKE_VIDU_KEY='...' [LAIAI_SMOKE_VIDU_URL=...] [LAIAI_SMOKE_VIDU_MODEL=viduq1] \
//	  go test ./internal/server/ -run TestRealViduTaskSmoke -count=1 -v -timeout 12m

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

// runRealTaskSmoke：通过本平台提交真任务→轮询到终态→断言计费闭环。共用骨架。
func runRealTaskSmoke(t *testing.T, provider, baseURL, key, model string, submitParams map[string]any) {
	t.Helper()
	srv, db := setupRelayServer(t)
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": provider + "-real", "provider": provider, "base_url": baseURL,
		"api_key": key, "models": []string{model},
	})
	if w.Code != 201 {
		t.Fatalf("create %s channel = %d %v", provider, w.Code, out)
	}
	// 伪 token 约定：1M token = 1 厂商单位；此处 $1/unit 便于观察
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "0", "output_usd_per_1m": "1",
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	svc := task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	apiKey, orgID := seedUserWithCredit(t, srv, provider+"-smoke@example.com", "100")

	body := map[string]any{"model": model,
		"content": []map[string]any{{"type": "text", "text": "a red apple rolls slowly on a wooden table"}}}
	for k, v := range submitParams {
		body[k] = v
	}
	w, submitOut := srv.do(t, "POST", "/v1/video/generations", apiKey, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("%s submit = %d %v", provider, w.Code, submitOut)
	}
	taskID := submitOut["id"].(string)
	t.Logf("[%s] submitted real task %s", provider, taskID)

	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsPositive() {
		t.Fatalf("no hold after submit: %s", acc.HeldAmount)
	}

	deadline := time.Now().Add(10 * time.Minute)
	var final map[string]any
	for time.Now().Before(deadline) {
		svc.PollOnce(context.Background())
		w, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
		if w.Code != 200 {
			t.Fatalf("get = %d %v", w.Code, got)
		}
		if st, _ := got["status"].(string); st == task.StatusSucceeded || st == task.StatusFailed {
			final = got
			break
		}
		time.Sleep(10 * time.Second)
	}
	if final == nil {
		t.Fatal("task did not reach terminal state within deadline")
	}
	if final["status"] != task.StatusSucceeded {
		t.Fatalf("[%s] task failed: %v", provider, final)
	}
	if url, _ := final["result_url"].(string); url == "" {
		t.Fatalf("[%s] succeeded but no result_url", provider)
	}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("[%s] hold not released: %s", provider, acc.HeldAmount)
	}
	cost := decimal.RequireFromString("100").Sub(acc.PostedBalance)
	t.Logf("[%s] succeeded: cost=$%s tokens=%v result=%.50s...", provider, cost, final["completion_tokens"], final["result_url"])
}

func TestRealKlingTaskSmoke(t *testing.T) {
	key := os.Getenv("LAIAI_SMOKE_KLING_KEY")
	if key == "" {
		t.Skip("LAIAI_SMOKE_KLING_KEY not set (format access_key|secret_key) — skipping")
	}
	url := os.Getenv("LAIAI_SMOKE_KLING_URL")
	if url == "" {
		url = "https://api-beijing.klingai.com"
	}
	model := os.Getenv("LAIAI_SMOKE_KLING_MODEL")
	if model == "" {
		model = "kling-v1"
	}
	runRealTaskSmoke(t, "kling", url, key, model, map[string]any{"mode": "std", "duration": "5"})
}

func TestRealViduTaskSmoke(t *testing.T) {
	key := os.Getenv("LAIAI_SMOKE_VIDU_KEY")
	if key == "" {
		t.Skip("LAIAI_SMOKE_VIDU_KEY not set — skipping")
	}
	url := os.Getenv("LAIAI_SMOKE_VIDU_URL")
	if url == "" {
		url = "https://api.vidu.cn"
	}
	model := os.Getenv("LAIAI_SMOKE_VIDU_MODEL")
	if model == "" {
		model = "viduq1"
	}
	runRealTaskSmoke(t, "vidu", url, key, model, map[string]any{"duration": float64(5), "resolution": "1080p"})
}
