package server

// 丝滑度批 B-2 渠道禁用建议 e2e：硬故障 → 记建议（不自动禁）→ admin 可见 → 成功清建议。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/laiai/laiai-next/core/internal/relay"
)

func TestChannelDisableSuggestion(t *testing.T) {
	srv, db := setupRelayServer(t)
	// 硬故障上游（余额耗尽）+ 健康上游，同模型两渠道
	broke := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`)
	}))
	defer broke.Close()
	// 高优先坏渠道 + 低优先健康渠道
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "broke", "provider": "openai", "base_url": broke.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"}, "priority": 10,
	})
	if w.Code != 201 {
		t.Fatalf("create broke = %d %v", w.Code, out)
	}
	up := mockUpstreamJSON(t, 1000, 500)
	defer up.Close()
	w, out = srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "good", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"}, "priority": 1,
	})
	if w.Code != 201 {
		t.Fatalf("create good = %d %v", w.Code, out)
	}
	w, _ = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "1", "output_usd_per_1m": "1",
	})
	apiKey, _ := seedUserWithCredit(t, srv, "sd@example.com", "10")

	// 请求：坏渠道余额耗尽(400 硬故障)→ 换渠道到健康渠道成功（B-1/B-2）
	w, out = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chat = %d %v (硬故障应换渠道成功)", w.Code, out)
	}

	// 坏渠道：status 仍启用（**不自动禁用**）+ 有禁用建议
	var brokeCh relay.Channel
	waitFor(t, func() bool {
		db.Where("name = ?", "broke").First(&brokeCh)
		return brokeCh.SuggestDisableReason != ""
	}, true)
	if brokeCh.SuggestDisableReason == "" {
		t.Fatal("broke channel should get a disable suggestion")
	}
	if brokeCh.Status != 1 {
		t.Fatalf("broke channel status = %d, want 1 (不自动禁用)", brokeCh.Status)
	}

	// admin 列表显示建议
	w, listOut := srv.doAdmin(t, "GET", "/api/admin/channels", nil)
	if w.Code != 200 {
		t.Fatalf("list = %d", w.Code)
	}
	chans := listOut["channels"].([]any)
	var found bool
	for _, c := range chans {
		m := c.(map[string]any)
		if m["name"] == "broke" {
			if s, _ := m["suggest_disable_reason"].(string); s == "" {
				t.Fatalf("broke channel suggest_disable_reason empty in admin list")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("broke channel not in admin list")
	}
}
