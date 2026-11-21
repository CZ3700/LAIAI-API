package videoasset

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

func rpcActionPost(router *gin.Engine, action, body, idempotency string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/video/?Action="+action+"&Version=2024-01-01", strings.NewReader(body))
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestRPCPortraitPassthroughFullFlow（2026-11-18 火山透传对齐）：Action RPC 形态
// 的创建/核实全链。创建同步返回火山三字段并落 pending+指纹；查询等待期返回空
// GroupId，就绪后复用认证即授权联动（real 组 + 自动许可）；幂等键重放不重复创建。
func TestRPCPortraitPassthroughFullFlow(t *testing.T) {
	s, db, now, _ := setup(t)
	*now = now.Add(750 * time.Microsecond)
	const upstreamToken = "20260102030405A12BCDEF0123456789"
	s.options.AutoLicense = true
	s.options.AutoLicenseValidity = 365 * 24 * time.Hour
	s.options.MaxLicenseAge = 90 * 24 * time.Hour
	stages := 0
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		action := req.URL.Query().Get("Action")
		if action == "CreateVisualValidateSession" {
			stages++
			return syncReply(req, map[string]any{"BytedToken": upstreamToken, "H5Link": "https://h5.example/rpc-auth", "CallbackURL": "https://customer.example/return"}), nil
		}
		if action == "GetVisualValidateResult" {
			var input struct{ BytedToken string }
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil || input.BytedToken != upstreamToken {
				t.Errorf("provider query must receive the original token, got %q (%v)", input.BytedToken, err)
			}
			stages++
			groupID := ""
			if stages >= 3 { // 第二次核实：官方侧已就绪
				groupID = "group-rpc-test-001"
			}
			return syncReply(req, map[string]any{"GroupId": groupID}), nil
		}
		return groupErrorReply(req, 500, "InternalError", nil), nil
	})})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		uid := uint64(2)
		c.Set("video_key", &identity.APIKey{ID: 3, OrgID: 1, UserID: &uid, Scopes: "video:read video:write"})
		c.Set("video_request_id", "req-rpc-test")
		c.Next()
	})
	s.RegisterRoutes(router.Group("/v1/video"))

	rec := rpcActionPost(router, rpcActionCreateSession, `{"CallbackURL":"https://customer.example/return"}`, "idem-rpc-create")
	if rec.Code != http.StatusOK {
		t.Fatalf("create returned %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Result 严格火山原生三字段（SessionId 不进响应体，在 X-Session-Id 头）。
	for _, want := range []string{`"BytedToken":"` + upstreamToken + `"`, `"H5Link":"https://h5.example/rpc-auth"`, `"CallbackURL":"https://customer.example/return"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("create result missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "SessionId") {
		t.Fatalf("create result must not contain SessionId (volcano-native shape): %s", body)
	}
	if rec.Header().Get("X-Session-Id") == "" {
		t.Fatal("X-Session-Id header expected for platform-side management")
	}
	var p Portrait
	if err := db.Where("idempotency_key = ?", "idem-rpc-create").Take(&p).Error; err != nil || p.SessionStatus != "pending" || p.BytedTokenHash == "" {
		t.Fatalf("pending session with token hash expected: %+v %v", p, err)
	}

	// 幂等重放：同键返回同会话，不再触达官方。
	before := stages
	rec = rpcActionPost(router, rpcActionCreateSession, `{"CallbackURL":"https://customer.example/return"}`, "idem-rpc-create")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Session-Id") == "" {
		t.Fatalf("replay returned %d %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != body || rec.Header().Get("X-Session-Id") != p.ID {
		t.Fatalf("replay must return the original provider token, link and session ID: %s", rec.Body.String())
	}
	if stages != before {
		t.Fatal("replay must not hit the provider again")
	}

	// 核实·等待期：空 GroupId。
	queryBody := `{"BytedToken":"` + upstreamToken + `"}`
	rec = rpcActionPost(router, rpcActionGetResult, queryBody, "idem-rpc-check-1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"GroupId":""`) {
		t.Fatalf("waiting query returned %d %s", rec.Code, rec.Body.String())
	}

	// 核实·就绪：建组 + 自动许可。响应体严格火山原生（仅 GroupId，无 SessionId）。
	rec = rpcActionPost(router, rpcActionGetResult, queryBody, "idem-rpc-check-2")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"GroupId":"group-rpc-test-001"`) || strings.Contains(rec.Body.String(), "SessionId") {
		t.Fatalf("ready query returned %d %s", rec.Code, rec.Body.String())
	}
	var portrait Portrait
	db.Where("session_id = ?", p.ID).Take(&portrait)
	var allGroups []Group
	if err := db.Find(&allGroups).Error; err != nil {
		t.Fatal(err)
	}
	var g Group
	if err := db.Where("provider_id = ?", "group-rpc-test-001").Take(&g).Error; err != nil || g.Kind != KindReal || g.Status != StatusActive {
		t.Fatalf("real group expected: %+v %v", g, err)
	}
	if portrait.SessionStatus != "succeeded" || portrait.GroupID == nil || portrait.LicenseStatus != "verified" {
		t.Fatalf("identity/license end state wrong: %+v", portrait)
	}
	before = stages
	rec = rpcActionPost(router, rpcActionGetResult, queryBody, "idem-rpc-check-3")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"GroupId":"group-rpc-test-001"`) || stages != before {
		t.Fatalf("verified session must keep returning the provider group ID: %d %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/video/asset-groups/group-rpc-test-001?cascade=false", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "authorization_active") {
		t.Fatalf("DELETE must resolve the provider group ID and check the license: %d %s", rec.Code, rec.Body.String())
	}

	before = stages
	rec = rpcActionPost(router, rpcActionCreateSession, `{"CallbackURL":"https://customer.example/return"}`, "idem-rpc-create")
	if rec.Code != http.StatusConflict || stages != before {
		t.Fatalf("completed session must report a conflict without creating a new one: %d %s", rec.Code, rec.Body.String())
	}

	// 不支持的 Action / Version。
	if rec := rpcActionPost(router, "Unknown", `{}`, "x"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"InvalidAction"`) {
		t.Fatalf("unknown action returned %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/video/?Action=CreateVisualValidateSession&Version=2020-01-01", strings.NewReader(`{"CallbackURL":"https://customer.example/return"}`))
	req.Header.Set("Idempotency-Key", "x")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"InvalidParameter"`) {
		t.Fatalf("bad version returned %d %s", rec.Code, rec.Body.String())
	}
}

// TestRPCCreateProviderRejectedSurfacesCleanly（2026-11-18 生产 503 回归）：
// 官方拒绝（非 accepted）时会话置 failed 且响应 409 安全信封——此前 rejected
// 分支写了 portrait 表不存在的 safe_error_code 列（1054）→ ErrStorage →
// 503 provider_unavailable，失败原因被吞。
func TestRPCCreateProviderRejectedSurfacesCleanly(t *testing.T) {
	s, db, _, _ := setup(t)
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		return groupErrorReply(req, 403, "AccessDenied", nil), nil
	})})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		uid := uint64(2)
		c.Set("video_key", &identity.APIKey{ID: 3, OrgID: 1, UserID: &uid, Scopes: "video:read video:write"})
		c.Set("video_request_id", "req-rpc-reject")
		c.Next()
	})
	s.RegisterRoutes(router.Group("/v1/video"))

	rec := rpcActionPost(router, rpcActionCreateSession, `{"CallbackURL":"https://customer.example/return"}`, "idem-rpc-reject")
	if rec.Code != http.StatusConflict {
		t.Fatalf("provider rejection returned %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"Code":"`) {
		t.Fatalf("rejection envelope must carry the safe error: %s", rec.Body.String())
	}
	var p Portrait
	if err := db.Where("idempotency_key = ?", "idem-rpc-reject").Take(&p).Error; err != nil || p.SessionStatus != "failed" {
		t.Fatalf("session must be failed: %+v %v", p, err)
	}
	var events int64
	db.Model(&Event{}).Where("org_id = ? AND resource_id = ? AND kind = ?", 1, p.ID, "rpc_session_failed").Count(&events)
	if events != 1 {
		t.Fatalf("rejection audit event expected, got %d", events)
	}
}

// TestRPCCreateInvalidCallbackURLNamesField（2026-11-19 客户反馈）：少斜杠等
// URL 笔误必须点名 CallbackURL 字段并给出正确示例——泛化的 invalid_request
// 让客户无法自查（实测案例 https:/www.baidu.com）。
func TestRPCCreateInvalidCallbackURLNamesField(t *testing.T) {
	s, _, _, _ := setup(t)
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid callback URL must not reach the provider")
		return nil, nil
	})})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		uid := uint64(2)
		c.Set("video_key", &identity.APIKey{ID: 3, OrgID: 1, UserID: &uid, Scopes: "video:read video:write"})
		c.Set("video_request_id", "req-rpc-badurl")
		c.Next()
	})
	s.RegisterRoutes(router.Group("/v1/video"))

	rec := rpcActionPost(router, rpcActionCreateSession, `{"CallbackURL":"https:/www.baidu.com"}`, "idem-rpc-badurl")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed URL returned %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CallbackURL must be a valid HTTPS URL") {
		t.Fatalf("error must name CallbackURL with guidance: %s", rec.Body.String())
	}
}
