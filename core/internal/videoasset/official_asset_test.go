package videoasset

import (
	"context"
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
	if !strings.Contains(body, `"RequestId":"req-official-asset"`) {
		t.Fatalf("official envelope must echo the platform RequestId: %s", body)
	}
	if !strings.Contains(body, `"Result":{"Id":"ag_`) || !strings.Contains(body, `"Name":"测试素材组"`) {
		t.Fatalf("missing official Result fields: %s", body)
	}
	groupID := extractQuoted(body, `"Result":{"Id":"`, `"`)
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("settle group: %v", err)
	}

	// 2. 查询素材组 (ListAssetGroups / GetAssetGroup)
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

	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/v1/video/asset-groups/"+groupID, nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get group returned %d: %s", getRec.Code, getRec.Body.String())
	}
	getBody := getRec.Body.String()
	if !strings.Contains(getBody, `"Action":"GetAssetGroup"`) || !strings.Contains(getBody, `"AssetCount":0`) {
		t.Fatalf("missing GetAssetGroup format: %s", getBody)
	}

	// 3. 注册素材 (CreateAsset) 与查询素材 (GetAsset)
	auth := CreateContext{OrgID: 1, KeyID: 3, UserID: 2, RequestID: "req-official-asset", IdempotencyKey: "idem-asset-001"}
	asset, _, err := s.CreateAsset(context.Background(), auth, AssetCreate{GroupID: groupID, URL: "https://media.example/registered-image.png", MediaType: "image", Name: "产品图"})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	getAssetRec := httptest.NewRecorder()
	router.ServeHTTP(getAssetRec, httptest.NewRequest(http.MethodGet, "/v1/video/assets/"+asset.ID, nil))
	if getAssetRec.Code != http.StatusOK {
		t.Fatalf("get asset returned %d: %s", getAssetRec.Code, getAssetRec.Body.String())
	}
	assetBody := getAssetRec.Body.String()
	if !strings.Contains(assetBody, `"Action":"GetAsset"`) || !strings.Contains(assetBody, `"AssetType":"image"`) || !strings.Contains(assetBody, `"GroupId":"`+groupID+`"`) {
		t.Fatalf("missing GetAsset official format: %s", assetBody)
	}
}

// extractQuoted 从 body 中截取 start 之后到 stop 的片段（仅用于测试 fixture ID 提取）。
func extractQuoted(body, start, stop string) string {
	idx := strings.Index(body, start)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(start):]
	end := strings.Index(rest, stop)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
