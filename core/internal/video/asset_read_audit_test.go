package video

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIsAssetReadRoute(t *testing.T) {
	for _, tc := range []struct {
		method, route string
		want          bool
	}{{"GET", "/v1/video/assets/:asset_id", true}, {"GET", "/v1/video/assets", true},
		{"GET", "/v1/video/asset-groups", true}, {"GET", "/v1/video/portrait-authorizations/:session_id", true},
		{"POST", "/v1/video/assets", false}, {"DELETE", "/v1/video/assets/:asset_id", false},
		{"GET", "/v1/video/generations", false}, {"GET", "/v1/video/generations/:id/content", false},
		{"POST", "/v1/video/portrait-authorizations", false}} {
		if got := isAssetReadRoute(tc.method, tc.route); got != tc.want {
			t.Fatalf("%s %s = %v, want %v", tc.method, tc.route, got, tc.want)
		}
	}
}

// 素材状态轮询（GET 2xx）不落审计行；失败（4xx/5xx）整行落库；非素材路由不受影响。
func TestMySQLAssetReadAuditSkip(t *testing.T) {
	f := mysqlFixture(t)
	// ark_test 是共享库：先清掉本路由的历史行，断言只看本次请求。
	f.s.DB.Exec("DELETE FROM api_request_logs WHERE route IN (?, ?)", "/v1/video/assets/:asset_id", "/v1/video/generations")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/video/assets/:asset_id", f.s.Middleware(), func(c *gin.Context) {
		if strings.Contains(c.Param("asset_id"), "fail") {
			respondError(c, ErrNotFound)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.GET("/v1/video/generations", f.s.Middleware(), f.s.HandleList)
	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+f.plain)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := call("/v1/video/assets/asset_ok123"); w.Code != 200 {
		t.Fatalf("ok asset = %d, want 200", w.Code)
	}
	var okRows, failRows, genRows int64
	f.s.DB.Table("api_request_logs").Where("route = ?", "/v1/video/assets/:asset_id").Count(&okRows)
	if okRows != 0 {
		t.Fatalf("successful asset poll left %d audit rows, want 0", okRows)
	}
	if w := call("/v1/video/assets/asset_fail123"); w.Code != 404 {
		t.Fatalf("failing asset = %d, want 404", w.Code)
	}
	f.s.DB.Table("api_request_logs").Where("route = ? AND http_status = 404", "/v1/video/assets/:asset_id").Count(&failRows)
	if failRows != 1 {
		t.Fatalf("failed asset rows = %d, want exactly 1 complete row", failRows)
	}
	if w := call("/v1/video/generations?limit=1"); w.Code != 200 {
		t.Fatalf("task list = %d, want 200", w.Code)
	}
	f.s.DB.Table("api_request_logs").Where("route = ?", "/v1/video/generations").Count(&genRows)
	if genRows != 1 {
		t.Fatalf("non-asset route rows = %d, want 1 (skip must not leak to task routes)", genRows)
	}
}
