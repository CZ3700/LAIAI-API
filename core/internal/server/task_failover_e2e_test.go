package server

// 任务提交换渠道故障转移 e2e（顶级审计修 2026-07 排名 1）：
// 高优先渠道不可达 → 自动改钉到低优先活渠道提交成功；全部候选耗尽 → hold 全退 + 任务 failed。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
)

// deadURL：立即关闭的监听地址——connection refused（确定性可重试失败）。
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func TestTaskSubmitFailoverToSecondChannel(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)

	// 高优先死渠道（attempt 0 必选它）+ 低优先活渠道
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ark-dead", "provider": "volcengine", "base_url": deadURL(t),
		"api_key": "sk-upstream-secret", "models": []string{"seedance-test"}, "priority": 10,
	})
	if w.Code != 201 {
		t.Fatalf("create dead channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ark-live", "provider": "volcengine", "base_url": ark.srv.URL,
		"api_key": "sk-upstream-secret", "models": []string{"seedance-test"}, "priority": 1,
	})
	if w.Code != 201 {
		t.Fatalf("create live channel = %d %v", w.Code, out)
	}
	var liveCh relay.Channel
	if err := db.Where("name = ?", "ark-live").First(&liveCh).Error; err != nil {
		t.Fatalf("live channel lookup: %v", err)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "seedance-test", "input_usd_per_1m": "0", "output_usd_per_1m": "10",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, orgID := seedUserWithCredit(t, srv, "failover@example.com", "10")

	taskID := submitVideoTask(t, srv, apiKey) // 内部断言 202 queued

	// 任务必须改钉到活渠道，且活渠道收到了提交
	tk := &task.Task{}
	if err := db.Where("task_id = ?", taskID).First(tk).Error; err != nil {
		t.Fatalf("task row: %v", err)
	}
	if tk.ChannelID != liveCh.ID {
		t.Fatalf("task pinned to channel %d, want live channel %d (failover)", tk.ChannelID, liveCh.ID)
	}
	if ark.submits.Load() != 1 {
		t.Fatalf("live upstream submits = %d, want 1", ark.submits.Load())
	}
	if tk.UpstreamTaskID == "" {
		t.Fatal("upstream_task_id not backfilled after failover submit")
	}
	// hold 仍在（任务活着），资金未泄漏
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsPositive() {
		t.Fatalf("held after failover submit = %s, want positive", acc.HeldAmount)
	}
}

func TestTaskSubmitAllChannelsExhausted(t *testing.T) {
	srv, db := setupRelayServer(t)

	// 两个渠道全死
	for i, url := range []string{deadURL(t), deadURL(t)} {
		w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
			"name": fmt.Sprintf("ark-dead-%d", i), "provider": "volcengine", "base_url": url,
			"api_key": "sk-upstream-secret", "models": []string{"seedance-test"}, "priority": 10 - i,
		})
		if w.Code != 201 {
			t.Fatalf("create channel %d = %d %v", i, w.Code, out)
		}
	}
	w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "seedance-test", "input_usd_per_1m": "0", "output_usd_per_1m": "10",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, orgID := seedUserWithCredit(t, srv, "exhaust@example.com", "10")

	w, out = srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model":   "seedance-test",
		"content": []map[string]any{{"type": "text", "text": "a cat surfing"}},
	})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("all-dead submit = %d %v, want 502", w.Code, out)
	}

	// hold 全退（资金红线）+ 任务终态 failed
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held after exhausted submit = %s, want 0 (release)", acc.HeldAmount)
	}
	tk := &task.Task{}
	if err := db.Order("id DESC").First(tk).Error; err != nil {
		t.Fatalf("task row: %v", err)
	}
	if tk.Status != task.StatusFailed {
		t.Fatalf("task status = %s, want failed", tk.Status)
	}
}
