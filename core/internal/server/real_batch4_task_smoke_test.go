package server

// §3.8 批次 4（sora/veo）真机 smoke——env 门控（无 key 自动 skip）。KEY 只走环境变量，绝不落文件。
//	LAIAI_SMOKE_SORA_KEY / LAIAI_SMOKE_VEO_KEY (+ _URL/_MODEL)
// 这两家产物经平台代理，smoke 提交→轮询→断言 result_url 是平台代理路径 + 拉一次 /content 有流。

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
)

func runProxyTaskSmoke(t *testing.T, provider, baseURL, key, model string, params map[string]any) {
	t.Helper()
	srv, db := setupRelayServer(t)
	if w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": provider + "-real", "provider": provider, "base_url": baseURL,
		"api_key": key, "models": []string{model},
	}); w.Code != 201 {
		t.Fatalf("create %s channel = %d %v", provider, w.Code, out)
	}
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "0", "output_usd_per_1m": "0.05",
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	svc := task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	apiKey, _ := seedUserWithCredit(t, srv, provider+"-smoke@example.com", "100")

	body := map[string]any{"model": model,
		"content": []map[string]any{{"type": "text", "text": "a red apple rolls slowly on a wooden table"}}}
	for k, v := range params {
		body[k] = v
	}
	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("%s submit = %d %v", provider, w.Code, out)
	}
	taskID := out["id"].(string)
	t.Logf("[%s] submitted %s", provider, taskID)

	deadline := time.Now().Add(10 * time.Minute)
	var final map[string]any
	for time.Now().Before(deadline) {
		svc.PollOnce(context.Background())
		_, got := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
		if st, _ := got["status"].(string); st == task.StatusSucceeded || st == task.StatusFailed {
			final = got
			break
		}
		time.Sleep(10 * time.Second)
	}
	if final == nil {
		t.Fatal("task did not reach terminal state")
	}
	if final["status"] != task.StatusSucceeded {
		t.Fatalf("[%s] failed: %v", provider, final)
	}
	rurl, _ := final["result_url"].(string)
	if !strings.HasSuffix(rurl, "/content") {
		t.Fatalf("[%s] result_url = %q, want proxy /content path", provider, rurl)
	}
	// 拉产物流
	rec := srv.doRaw(t, "GET", rurl, apiKey)
	if rec.Code != 200 || rec.Body.Len() == 0 {
		t.Fatalf("[%s] content proxy = %d len=%d", provider, rec.Code, rec.Body.Len())
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(1), ledger.AccountUserCredit).First(acc)
	t.Logf("[%s] succeeded: content %d bytes, ct=%s", provider, rec.Body.Len(), rec.Header().Get("Content-Type"))
}

func TestRealSoraTaskSmoke(t *testing.T) {
	key := os.Getenv("LAIAI_SMOKE_SORA_KEY")
	if key == "" {
		t.Skip("LAIAI_SMOKE_SORA_KEY not set — skipping")
	}
	url := os.Getenv("LAIAI_SMOKE_SORA_URL")
	if url == "" {
		url = "https://api.openai.com/v1"
	}
	model := os.Getenv("LAIAI_SMOKE_SORA_MODEL")
	if model == "" {
		model = "sora-2"
	}
	runProxyTaskSmoke(t, "sora", url, key, model, map[string]any{"seconds": "4", "size": "720x1280"})
}

func TestRealVeoTaskSmoke(t *testing.T) {
	key := os.Getenv("LAIAI_SMOKE_VEO_KEY")
	if key == "" {
		t.Skip("LAIAI_SMOKE_VEO_KEY not set — skipping")
	}
	url := os.Getenv("LAIAI_SMOKE_VEO_URL")
	if url == "" {
		url = "https://generativelanguage.googleapis.com/v1beta"
	}
	model := os.Getenv("LAIAI_SMOKE_VEO_MODEL")
	if model == "" {
		model = "veo-3.1-generate-preview"
	}
	runProxyTaskSmoke(t, "veo", url, key, model, map[string]any{"duration": 8})
}
