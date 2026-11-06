package videoasset

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

// TestGetAssetSourceURLExtension 验证公开 GetAsset 信封的 SourceURL 平台扩展字段：
// 素材可用时实时返回火山临时地址；不可用时不出上游调用；上游校验失败不影响主体响应。
func TestGetAssetSourceURLExtension(t *testing.T) {
	s, db, now, provider := setup(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/v1/video")
	group.Use(func(c *gin.Context) {
		uid := uint64(2)
		c.Set("video_key", &identity.APIKey{ID: 3, OrgID: 1, UserID: &uid, Scopes: "video:read"})
		c.Set("video_request_id", "req-asset-source-url")
		c.Next()
	})
	s.RegisterRoutes(group)

	g := readyGroup(t, s, "source-url-group")
	a := readyAsset(t, s, now, g.ID, "source-url-asset")
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/video/assets/"+a.ID, nil))
		return rec
	}

	// 1. 可用素材：SourceURL 返回火山临时地址，URL 仍是 asset:// 引用。
	rec := get()
	if rec.Code != http.StatusOK {
		t.Fatalf("get asset returned %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"SourceURL":"https://private.example/`) {
		t.Fatalf("usable asset missing upstream SourceURL: %s", body)
	}
	if !strings.Contains(body, `"URL":"asset://`+a.ID+`"`) {
		t.Fatalf("URL reference replaced: %s", body)
	}

	// 2. 素材不可用（非 active）：不触发上游调用，SourceURL 留空。
	if err := db.Model(&Asset{}).Where("asset_id = ?", a.ID).Update("status", StatusDeleting).Error; err != nil {
		t.Fatal(err)
	}
	before := provider.calls["GetAsset"]
	rec = get()
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"SourceURL":"https://`) {
		t.Fatalf("unusable asset should return 200 with empty SourceURL: %d %s", rec.Code, rec.Body.String())
	}
	if provider.calls["GetAsset"] != before {
		t.Fatal("unusable asset still called provider")
	}
	if err := db.Model(&Asset{}).Where("asset_id = ?", a.ID).Update("status", StatusActive).Error; err != nil {
		t.Fatal(err)
	}

	// 3. 上游校验失败（项目不匹配）：主体仍 200，SourceURL 留空。
	provider.project = "different-project"
	rec = get()
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"SourceURL":"https://`) {
		t.Fatalf("upstream mismatch should keep 200 with empty SourceURL: %d %s", rec.Code, rec.Body.String())
	}
	provider.project = testScope().Project
}
