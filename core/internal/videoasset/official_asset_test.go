package videoasset

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

// TestOfficialAssetResponseFormats 验证素材组与素材端点返回火山官方 ResponseMetadata + Result 格式
func TestOfficialAssetResponseFormats(t *testing.T) {
	s, _, _, _ := setup(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/v1/video")
	group.Use(func(c *gin.Context) {
		uid := uint64(2)
		c.Set("video_key", &identity.APIKey{ID: 3, OrgID: 1, UserID: &uid, Scopes: "video:read video:write"})
		c.Set("video_request_id", "req-official-asset")
		c.Next()
	})
	s.RegisterRoutes(group)

	// 1. 创建虚拟素材组 (CreateAssetGroup) -> 火山官方 ResponseMetadata + Result
	req := httptest.NewRequest(http.MethodPost, "/v1/video/asset-groups", strings.NewReader(`{"kind":"virtual","name":"测试素材组"}`))
	req.Header.Set("Idempotency-Key", "idem-group-001")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create group returned %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"Action":"CreateAssetGroup"`) || !strings.Contains(body, `"Service":"ark"`) {
		t.Fatalf("missing official ResponseMetadata: %s", body)
	}
	if !strings.Contains(body, `"Result":{"Id":"ag_`) || !strings.Contains(body, `"Name":"测试素材组"`) {
		t.Fatalf("missing official Result fields: %s", body)
	}

	// 2. 查询素材组 (ListAssetGroups)
	listReq := httptest.NewRequest(http.MethodGet, "/v1/video/asset-groups", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list groups returned %d: %s", listRec.Code, listRec.Body.String())
	}
	listBody := listRec.Body.String()
	if !strings.Contains(listBody, `"Action":"ListAssetGroups"`) || !strings.Contains(listBody, `"Items":`) {
		t.Fatalf("missing ListAssetGroups format: %s", listBody)
	}
}
