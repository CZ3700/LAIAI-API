package videoasset

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

// portraitGatewayTestRouter mounts RegisterRoutes with the parent middleware
// contract the video package provides: video_key + video_request_id context and
// the video_error key captured back for assertions.
func portraitGatewayTestRouter(s *Service) (*gin.Engine, *string) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	captured := ""
	group := router.Group("/v1/video")
	group.Use(func(c *gin.Context) {
		uid := uint64(2)
		c.Set("video_key", &identity.APIKey{ID: 3, OrgID: 1, UserID: &uid, Scopes: "video:read video:write"})
		c.Set("video_request_id", "req-portrait-http")
		c.Next()
		captured = c.GetString("video_error")
	})
	s.RegisterRoutes(group)
	return router, &captured
}

func portraitPost(router *gin.Engine, body, idempotency string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/video/portrait-authorizations", strings.NewReader(body))
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// 平台回调未配置必须报 503 provider_unavailable（可重试、指向平台），绝不能
// 伪装成 400 invalid_request 指责客户请求——2026-10-28 两个生产 400 的根因。
func TestPortraitUnconfiguredIsProviderUnavailable(t *testing.T) {
	s, _, _, _ := setup(t)
	s.options.PlatformCallbackURL = ""
	s.options.ResolveCallback = nil
	s.options.ResolveCallbackByID = nil
	router, captured := portraitGatewayTestRouter(s)
	rec := portraitPost(router, `{"callback_url":"https://customer.example/return","name":"本人素材授权","description":"视频生成用途"}`, "idem-portrait-1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured gateway returned %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"provider_unavailable"`) || !strings.Contains(body, `"retryable":true`) {
		t.Fatalf("envelope must be provider_unavailable/retryable: %s", body)
	}
	if !strings.Contains(body, "not configured") {
		t.Fatalf("message must name the platform gap: %s", body)
	}
	if *captured != "provider_unavailable" {
		t.Fatalf("video_error not captured for request log, got %q", *captured)
	}
}

// 缺 Idempotency-Key 的 400 必须点名缺失的头，不再笼统 "The request is invalid."
func TestPortraitMissingIdempotencyKeyNamesHeader(t *testing.T) {
	s, _, _, _ := setup(t)
	router, captured := portraitGatewayTestRouter(s)
	rec := portraitPost(router, `{"callback_url":"https://customer.example/return"}`, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key returned %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Idempotency-Key header is required") {
		t.Fatalf("message must name the header: %s", rec.Body.String())
	}
	if *captured != "invalid_request" {
		t.Fatalf("video_error not captured, got %q", *captured)
	}
}

// callback_url 未预登记（客户拿示例 URL 直接打）必须点名登记要求。
func TestPortraitUnregisteredCallbackNamesRegistration(t *testing.T) {
	s, _, _, _ := setup(t)
	router, captured := portraitGatewayTestRouter(s)
	rec := portraitPost(router, `{"callback_url":"https://client.example.invalid/portrait-complete","name":"本人素材授权","description":"视频生成用途"}`, "idem-portrait-2")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unregistered callback returned %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"invalid_request"`) || !strings.Contains(body, "registered for this organization") {
		t.Fatalf("message must name registration requirement: %s", body)
	}
	if *captured != "invalid_request" {
		t.Fatalf("video_error not captured, got %q", *captured)
	}
}

func TestPortraitFailedHTTPReadReturnsSafeProviderDiagnostics(t *testing.T) {
	s, _, _, provider := setup(t)
	provider.denyPortrait = true
	router, _ := portraitGatewayTestRouter(s)
	created := portraitPost(router, `{"callback_url":"https://customer.example/return","name":"本人素材授权"}`, "idem-portrait-denied")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create returned %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Result struct {
			SessionID string `json:"SessionId"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil || envelope.Result.SessionID == "" {
		t.Fatalf("missing session id: %s (%v)", created.Body.String(), err)
	}
	if err := s.ProcessOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/video/portrait-authorizations/"+envelope.Result.SessionID, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get failed portrait returned %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"Status":"failed"`, `"code":"provider_permission_denied"`, `"ProviderHttpStatus":403`, `"UpstreamRequestId":"permission-denied-request"`, `"CreateRequestId":"req-portrait-http"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s from safe diagnostics: %s", want, body)
		}
	}
	if strings.Contains(body, "raw provider detail") || strings.Contains(body, "fixture-secret") || strings.Contains(body, "h5-fixture-secret") {
		t.Fatal("raw upstream text or credentials leaked through portrait response")
	}
}

// 已配置 + 已登记回调 + 幂等键齐全的请求应进入正常受理（202 creating），
// 确保错误分流没有破坏合法路径。
func TestPortraitConfiguredHappyPathStillAccepted(t *testing.T) {
	s, _, _, _ := setup(t)
	router, _ := portraitGatewayTestRouter(s)
	rec := portraitPost(router, `{"callback_url":"https://customer.example/return","name":"本人素材授权","description":"视频生成用途"}`, "idem-portrait-3")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("valid create returned %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"Status":"creating"`) {
		t.Fatalf("expected creating session: %s", rec.Body.String())
	}
}
