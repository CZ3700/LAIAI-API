package server

// 真实上游主链路 smoke（env 门控，CI 默认 skip）：用真实 OpenAI 兼容上游打穿
// 完整 relay 链路——认证→keyGate→hold→picker→adapter 转发→真实 usage capture→
// invocation 落库。这是 launch checklist「渠道真机 smoke」的可重复执行形式。
//
// 运行方式（KEY 只走环境变量，绝不落仓库文件）：
//   LAIAI_SMOKE_UPSTREAM_URL=https://host/v1 \
//   LAIAI_SMOKE_UPSTREAM_KEY=sk-... \
//   LAIAI_SMOKE_UPSTREAM_MODEL=some-model \
//   go test ./internal/server/ -run TestRealUpstreamSmoke -count=1 -v

import (
	"os"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

func TestRealUpstreamSmoke(t *testing.T) {
	baseURL := os.Getenv("LAIAI_SMOKE_UPSTREAM_URL")
	key := os.Getenv("LAIAI_SMOKE_UPSTREAM_KEY")
	model := os.Getenv("LAIAI_SMOKE_UPSTREAM_MODEL")
	if baseURL == "" || key == "" || model == "" {
		t.Skip("real upstream smoke skipped: set LAIAI_SMOKE_UPSTREAM_{URL,KEY,MODEL} to run")
	}

	srv, db := setupRelayServer(t)
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "smoke-real", "provider": "openai", "base_url": baseURL,
		"api_key": key, "models": []string{model},
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "1", "output_usd_per_1m": "4",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, orgID := seedUserWithCredit(t, srv, "smoke-real@example.com", "5")

	// 非流式：真实上游往返 + usage 计费
	w, out = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": model, "max_tokens": 32,
		"messages": []map[string]any{{"role": "user", "content": "Reply with exactly: OK"}},
	})
	if w.Code != 200 {
		t.Fatalf("real chat = %d %s", w.Code, w.Body.String())
	}
	usage, _ := out["usage"].(map[string]any)
	if usage == nil || usage["completion_tokens"].(float64) <= 0 {
		t.Fatalf("no real usage returned: %v", out)
	}
	t.Logf("real upstream ok: model=%s prompt=%v completion=%v", model, usage["prompt_tokens"], usage["completion_tokens"])

	// 资金链路：capture 已发生（hold 清零 + 余额被扣真实小额）
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held not released after settle: %s", acc.HeldAmount)
	}
	if !acc.PostedBalance.LessThan(decimal.RequireFromString("5")) || acc.PostedBalance.LessThan(decimal.RequireFromString("4.9")) {
		t.Fatalf("balance after real call = %s, want tiny real deduction from 5", acc.PostedBalance)
	}

	// Invocation Store：真实调用进请求日志
	inv := waitInvocation(t, db, "model = ? AND status = ?", model, "success")
	if inv.CompletionTokens <= 0 || !inv.CostUSD.IsPositive() {
		t.Fatalf("invocation not billed: %+v", inv)
	}

	// 流式：SSE 管道 + 末帧 usage
	w2, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": model, "max_tokens": 32, "stream": true,
		"messages": []map[string]any{{"role": "user", "content": "Reply with exactly: OK"}},
	})
	if w2.Code != 200 || !strings.Contains(w2.Body.String(), "data:") {
		t.Fatalf("real stream = %d (body head: %.200s)", w2.Code, w2.Body.String())
	}
	t.Log("real upstream stream ok")
}

// 入站 /v1/responses 打真实 chat 上游（阶段二 §3.2）：Responses SDK 形态请求 → 真实 chat 上游 →
// Responses 响应/事件流。env 门控（同 TestRealUpstreamSmoke）。
func TestRealResponsesInboundSmoke(t *testing.T) {
	baseURL := os.Getenv("LAIAI_SMOKE_UPSTREAM_URL")
	key := os.Getenv("LAIAI_SMOKE_UPSTREAM_KEY")
	model := os.Getenv("LAIAI_SMOKE_UPSTREAM_MODEL")
	if baseURL == "" || key == "" || model == "" {
		t.Skip("real responses smoke skipped: set LAIAI_SMOKE_UPSTREAM_{URL,KEY,MODEL} to run")
	}
	srv, db := setupRelayServer(t)
	if w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "smoke-resp", "provider": "openai", "base_url": baseURL,
		"api_key": key, "models": []string{model},
	}); w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "1", "output_usd_per_1m": "4",
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, orgID := seedUserWithCredit(t, srv, "smoke-resp@example.com", "5")

	// 非流式 Responses 请求（instructions + input 数组形态，真实 SDK 用法）
	w, out := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model":             model,
		"instructions":      "You reply tersely.",
		"input":             []map[string]any{{"role": "user", "content": "Reply with exactly: OK"}},
		"max_output_tokens": 32,
	})
	if w.Code != 200 {
		t.Fatalf("real responses = %d %s", w.Code, w.Body.String())
	}
	if out["object"] != "response" || out["status"] != "completed" {
		t.Fatalf("not responses shape: %v", out)
	}
	usage := out["usage"].(map[string]any)
	if usage["output_tokens"].(float64) <= 0 {
		t.Fatalf("no real usage: %v", usage)
	}
	t.Logf("real responses inbound ok: input=%v output=%v", usage["input_tokens"], usage["output_tokens"])
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsZero() || !acc.PostedBalance.LessThan(decimal.RequireFromString("5")) {
		t.Fatalf("billing: held=%s posted=%s", acc.HeldAmount, acc.PostedBalance)
	}

	// 流式 Responses：事件序列 + 无 [DONE]
	w2, _ := srv.do(t, "POST", "/v1/responses", apiKey, map[string]any{
		"model": model, "stream": true, "input": "Reply with exactly: OK", "max_output_tokens": 32,
	})
	body := w2.Body.String()
	if w2.Code != 200 || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("real responses stream = %d (head: %.300s)", w2.Code, body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("responses stream must not contain [DONE]")
	}
	t.Log("real responses inbound stream ok")
}

// 出站 openai-responses adapter 打真实 Responses 上游（阶段二 §3.2）：chat 客户端请求 →
// 转 Responses 打真实 /v1/responses 上游 → Responses 响应/流转回 chat。
// env：LAIAI_SMOKE_RESPONSES_{URL,KEY,MODEL}（MODEL 必须是上游支持 /v1/responses 的模型）。
func TestRealResponsesOutboundSmoke(t *testing.T) {
	baseURL := os.Getenv("LAIAI_SMOKE_RESPONSES_URL")
	key := os.Getenv("LAIAI_SMOKE_RESPONSES_KEY")
	model := os.Getenv("LAIAI_SMOKE_RESPONSES_MODEL")
	if baseURL == "" || key == "" || model == "" {
		t.Skip("real responses-outbound smoke skipped: set LAIAI_SMOKE_RESPONSES_{URL,KEY,MODEL} to run")
	}
	srv, db := setupRelayServer(t)
	if w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "smoke-resp-out", "provider": "openai-responses", "base_url": baseURL,
		"api_key": key, "models": []string{model},
	}); w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "1", "output_usd_per_1m": "4",
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, orgID := seedUserWithCredit(t, srv, "smoke-respout@example.com", "5")

	// chat 客户端调用 → 内部转 Responses → 真实上游 → 转回 chat.completion
	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": model, "max_tokens": 32,
		"messages": []map[string]any{
			{"role": "system", "content": "You reply tersely."},
			{"role": "user", "content": "Reply with exactly: OK"},
		},
	})
	if w.Code != 200 {
		t.Fatalf("chat via real responses = %d %s", w.Code, w.Body.String())
	}
	if out["object"] != "chat.completion" {
		t.Fatalf("not chat.completion: %v", out)
	}
	usage := out["usage"].(map[string]any)
	if usage["completion_tokens"].(float64) <= 0 {
		t.Fatalf("no usage: %v", usage)
	}
	t.Logf("real responses outbound ok: prompt=%v completion=%v", usage["prompt_tokens"], usage["completion_tokens"])
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.IsZero() || !acc.PostedBalance.LessThan(decimal.RequireFromString("5")) {
		t.Fatalf("billing: held=%s posted=%s", acc.HeldAmount, acc.PostedBalance)
	}

	// 流式：chat stream → 真实 Responses 事件流 → chat chunk + [DONE]
	w2, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": model, "stream": true, "max_tokens": 32,
		"messages": []map[string]any{{"role": "user", "content": "Reply with exactly: OK"}},
	})
	body := w2.Body.String()
	if w2.Code != 200 || !strings.Contains(body, "chat.completion.chunk") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("real responses outbound stream = %d (head: %.300s)", w2.Code, body)
	}
	t.Log("real responses outbound stream ok")
}
