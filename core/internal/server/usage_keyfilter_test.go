package server

// Request Logs 按 API key 过滤（指令 0-14 天「Request Logs 补 API key filter」）：
// /api/usage 的 api_key_id 服务端过滤——分页与 total 必须反映过滤结果（非前端本地隐藏）。

import (
	"net/http"
	"testing"

	"github.com/laiai/laiai-next/core/internal/invocation"
)

func TestUsageAPIKeyFilter(t *testing.T) {
	srv, db := setupRelayServer(t)
	token, orgID := seedUserSession(t, srv, "keyfilter@example.com")

	for _, row := range []invocation.Invocation{
		{OrgID: uint64(orgID), APIKeyID: 111, RequestID: "r-key-111", Model: "m1", Status: invocation.StatusSuccess},
		{OrgID: uint64(orgID), APIKeyID: 222, RequestID: "r-key-222", Model: "m1", Status: invocation.StatusSuccess},
		{OrgID: uint64(orgID) + 999, APIKeyID: 111, RequestID: "r-other-org", Model: "m1", Status: invocation.StatusSuccess},
	} {
		r := row
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("seed invocation: %v", err)
		}
	}

	// 按 key 过滤：只回本 org 该 key 的行，total 同步
	w, out := srv.do(t, "GET", "/api/usage?api_key_id=111", token, nil)
	if w.Code != 200 || out["total"].(float64) != 1 {
		t.Fatalf("filtered = %d %v, want total 1", w.Code, out)
	}
	items := out["items"].([]any)
	if items[0].(map[string]any)["request_id"] != "r-key-111" {
		t.Fatalf("filtered row = %v", items[0])
	}

	// 不过滤：本 org 两行（跨 org 行绝不可见——org_id 谓词红线）
	w, out = srv.do(t, "GET", "/api/usage", token, nil)
	if w.Code != 200 || out["total"].(float64) != 2 {
		t.Fatalf("unfiltered = %d %v, want total 2", w.Code, out)
	}

	// 跨 org 的 key id 过滤 = 空集（不越权、不报错）
	w, out = srv.do(t, "GET", "/api/usage?api_key_id=333", token, nil)
	if w.Code != 200 || out["total"].(float64) != 0 {
		t.Fatalf("cross-org key filter = %d %v, want empty", w.Code, out)
	}

	// 非整数 → 400
	if w, _ := srv.do(t, "GET", "/api/usage?api_key_id=abc", token, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad api_key_id = %d, want 400", w.Code)
	}
}
