package server

// 0040 modality 模态标 + usage 端点过滤参数扩展的行为测试：
//   - /api/usage?modality= 服务端过滤（图像日志页数据源）+ request_id 精确锚点查询
//   - 真实 relay 调用端到端打标（handler 打 Modality → Recorder 落库 → API 可筛）
//   - /api/usage/summary?granularity=hour 近 24h 小时分桶（sqlite 方言分支）
//   - summary/by-model 的 api_key_id 过滤（Usage 页 Key 维度）

import (
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
)

func TestUsageModalityAndRequestIDFilter(t *testing.T) {
	srv, db := setupRelayServer(t)
	token, orgID := seedUserSession(t, srv, "modality@example.com")

	for _, row := range []invocation.Invocation{
		{OrgID: uint64(orgID), APIKeyID: 1, RequestID: "r-img-1", Model: "dall-e-3", Modality: invocation.ModalityImage, Status: invocation.StatusSuccess},
		{OrgID: uint64(orgID), APIKeyID: 1, RequestID: "r-img-2", Model: "dall-e-3", Modality: invocation.ModalityImage, Status: invocation.StatusUpstreamError},
		{OrgID: uint64(orgID), APIKeyID: 1, RequestID: "r-chat-1", Model: "gpt-x", Modality: invocation.ModalityChat, Status: invocation.StatusSuccess},
		{OrgID: uint64(orgID) + 999, APIKeyID: 1, RequestID: "r-img-other-org", Model: "dall-e-3", Modality: invocation.ModalityImage, Status: invocation.StatusSuccess},
	} {
		r := row
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("seed invocation: %v", err)
		}
	}

	// modality=image：只回本 org 的两行图像调用（跨 org 行不可见——org_id 谓词红线）
	w, out := srv.do(t, "GET", "/api/usage?modality=image", token, nil)
	if w.Code != 200 || out["total"].(float64) != 2 {
		t.Fatalf("modality=image = %d %v, want total 2", w.Code, out)
	}
	// modality + status 组合过滤在服务端完成（分页 total 正确性同 keyfilter 纪律）
	w, out = srv.do(t, "GET", "/api/usage?modality=image&status=failed", token, nil)
	if w.Code != 200 || out["total"].(float64) != 1 {
		t.Fatalf("modality+status = %d %v, want total 1", w.Code, out)
	}
	// request_id 精确锚点（uk_inv_request）：工单场景一键定位
	w, out = srv.do(t, "GET", "/api/usage?request_id=r-chat-1", token, nil)
	if w.Code != 200 || out["total"].(float64) != 1 {
		t.Fatalf("request_id filter = %d %v, want total 1", w.Code, out)
	}
	items := out["items"].([]any)
	if items[0].(map[string]any)["modality"] != "chat" {
		t.Fatalf("modality not exposed in response: %v", items[0])
	}
}

// 真实 relay 链路端到端：chat 调用落库的行带 modality=chat，且 /api/usage?modality= 能筛到。
func TestUsageModalityStampedE2E(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 5, 7)
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "modstamp@example.com", "10")
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "modstamp@example.com", "password": "longenough1"})
	token := login["token"].(string)

	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chat call = %d", w.Code)
	}
	inv := waitInvocation(t, db, "model = ? AND status = ?", "test-model", invocation.StatusSuccess)
	if inv.Modality != invocation.ModalityChat {
		t.Fatalf("stamped modality = %q, want chat", inv.Modality)
	}
	w, out := srv.do(t, "GET", "/api/usage?modality=chat", token, nil)
	if w.Code != 200 || out["total"].(float64) < 1 {
		t.Fatalf("modality=chat after e2e = %d %v, want ≥1", w.Code, out)
	}
}

func TestUsageSummaryHourGranularity(t *testing.T) {
	srv, db := setupRelayServer(t)
	token, orgID := seedUserSession(t, srv, "hourly@example.com")

	for _, rid := range []string{"r-h1", "r-h2"} {
		if err := db.Create(&invocation.Invocation{
			OrgID: uint64(orgID), APIKeyID: 1, RequestID: rid, Model: "m", Status: invocation.StatusSuccess,
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// 30 小时前的行：不落入近 24h 窗口
	old := invocation.Invocation{OrgID: uint64(orgID), APIKeyID: 1, RequestID: "r-h-old", Model: "m", Status: invocation.StatusSuccess}
	if err := db.Create(&old).Error; err != nil {
		t.Fatalf("seed old: %v", err)
	}
	db.Exec("UPDATE invocations SET created_at = ? WHERE request_id = 'r-h-old'",
		time.Now().UTC().Add(-30*time.Hour).Format("2006-01-02 15:04:05"))

	w, out := srv.do(t, "GET", "/api/usage/summary?granularity=hour", token, nil)
	if w.Code != 200 || out["granularity"] != "hour" {
		t.Fatalf("hour summary = %d %v", w.Code, out)
	}
	hourLabel := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:00$`)
	var calls float64
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if !hourLabel.MatchString(m["day"].(string)) {
			t.Fatalf("bucket label %q not hour-shaped", m["day"])
		}
		calls += m["calls"].(float64)
	}
	if calls != 2 {
		t.Fatalf("hour window calls = %v, want 2 (old row excluded)", calls)
	}

	// 未知 granularity 静默回落 day（与 days 钳制同风格）
	w, out = srv.do(t, "GET", "/api/usage/summary?granularity=nope", token, nil)
	if w.Code != 200 || out["granularity"] != "day" {
		t.Fatalf("unknown granularity = %d %v, want fallback day", w.Code, out)
	}
}

func TestUsageAggregatesKeyFilter(t *testing.T) {
	srv, db := setupRelayServer(t)
	token, orgID := seedUserSession(t, srv, "aggkey@example.com")

	for _, row := range []invocation.Invocation{
		{OrgID: uint64(orgID), APIKeyID: 111, RequestID: "r-agg-1", Model: "m1", Status: invocation.StatusSuccess, PromptTokens: 10},
		{OrgID: uint64(orgID), APIKeyID: 111, RequestID: "r-agg-2", Model: "m2", Status: invocation.StatusSuccess, PromptTokens: 20},
		{OrgID: uint64(orgID), APIKeyID: 222, RequestID: "r-agg-3", Model: "m1", Status: invocation.StatusSuccess, PromptTokens: 40},
	} {
		r := row
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// by-model 按 key 过滤：只聚合 key 111 的行（m1=1 次、m2=1 次）
	w, out := srv.do(t, "GET", "/api/usage/by-model?days=7&api_key_id=111", token, nil)
	if w.Code != 200 {
		t.Fatalf("by-model keyed = %d", w.Code)
	}
	items := out["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("by-model keyed rows = %d, want 2", len(items))
	}
	for _, it := range items {
		if it.(map[string]any)["calls"].(float64) != 1 {
			t.Fatalf("by-model keyed calls = %v, want 1 each", it)
		}
	}

	// summary 按 key 过滤：calls 总和只含 key 111 的 2 行
	w, out = srv.do(t, "GET", "/api/usage/summary?days=7&api_key_id=111", token, nil)
	if w.Code != 200 {
		t.Fatalf("summary keyed = %d", w.Code)
	}
	var calls, tokens float64
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		calls += m["calls"].(float64)
		tokens += m["prompt_tokens"].(float64)
	}
	if calls != 2 || tokens != 30 {
		t.Fatalf("summary keyed calls=%v tokens=%v, want 2/30", calls, tokens)
	}

	// 非整数 → 400（两个端点同规）
	if w, _ := srv.do(t, "GET", "/api/usage/summary?api_key_id=abc", token, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("summary bad key id = %d, want 400", w.Code)
	}
	if w, _ := srv.do(t, "GET", "/api/usage/by-model?api_key_id=abc", token, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("by-model bad key id = %d, want 400", w.Code)
	}
}
