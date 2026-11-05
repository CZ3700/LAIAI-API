package video

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

// 回归（2026-11-04）：AV 参考链路端到端受理。URL 模式的视频参考资产没有实测
// 时长（平台不下载不探测），修复前 InputDurationMS=0 直接 ErrCapability（422
// unsupported_capability 无 param），音频/视频参考生成从未成功过。修复后：
// 绑定的视频资产缺实测时长 → HasUnverifiedVideo → 按配置的
// MaxInputVideoDurationMS 兜底计费与校验（与裸 HTTPS 输入视频同款）。
func TestMySQLVideoReferenceCreateWithUnmeasuredAsset(t *testing.T) {
	f := mysqlFixture(t)
	scope, _ := assetTestSetup(t, f)
	// 资产夹具：虚拟组 + 已分配 provider 的视频资产，但无实测时长。
	group, asset := assetTestSeed(t, f, scope, videoasset.KindVirtual)
	must(t, f.s.DB.Model(&videoasset.Asset{}).Where("asset_id = ?", asset.ID).
		Update("media_type", "video").Error)
	// 定价单元格：复制 720p 规则改 720p/input1（同分辨率，只换输入维度），
	// 并发布配套的最小用量证据（input_video 规则必须带版本化下限）。
	var rule videobilling.PricingRule
	must(t, f.s.DB.Where("provider_scope_id = ?", scope.ID).First(&rule).Error)
	rule.ID = 0
	rule.RuleID, _ = randomID("")
	rule.InputVideo = true
	rule.MinimumRuleVersion = "fixture-" + rule.RuleID
	must(t, f.s.DB.Create(&rule).Error)
	cells := []videobilling.MinimumRuleCell{{Resolution: "720p", Ratio: "16:9", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 1000000}}
	for duration := 4; duration <= 15; duration++ {
		if duration == 5 {
			continue
		}
		cells = append(cells, videobilling.MinimumRuleCell{Resolution: "720p", Ratio: "16:9", OutputDurationMS: int64(duration) * 1000, FPS: 24, MinimumTokens: 1000000})
	}
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		_, err := videobilling.PublishMinimumTx(tx, videobilling.MinimumRuleSet{Model: ark.Model20, Version: rule.MinimumRuleVersion, SourceURL: "https://docs.volcengine.com/fixture-only", EvidenceHash: strings.Repeat("a", 64), ObservedAt: f.now, VerifiedAt: f.now, VerifiedBy: 77}, cells)
		return err
	}))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/video/generations", f.s.Middleware(), f.s.HandleCreate)
	body := `{"model":"` + ark.Model20 + `","content":[{"type":"text","text":"视频背景改成沙漠"},{"type":"video_url","video_url":{"url":"asset://` + asset.ID + `"},"role":"reference_video"}],"resolution":"720p","duration":5}`
	call := func(idem string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.plain)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idem)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	w := call("av-ref-idem-1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("video-reference create = %d %s, want 202", w.Code, w.Body.String())
	}
	// 官方 Submit 形状：受理体仅 {"id": "task_..."}，状态在单查/任务表。
	if !strings.HasPrefix(strings.TrimSpace(w.Body.String()), `{"id":"task_`) {
		t.Fatalf("accepted body not official submit shape: %s", w.Body.String())
	}
	// 第二单（不同幂等键，确认链路可重复受理）
	if w := call("av-ref-idem-2"); w.Code != http.StatusAccepted {
		t.Fatalf("second create = %d, want 202", w.Code)
	}
	var accepted int64
	f.s.DB.Model(&task.Task{}).Where("org_id = ? AND model = ? AND status = ?", group.OrgID, ark.Model20, "accepted").Count(&accepted)
	if accepted < 2 {
		t.Fatalf("accepted tasks = %d, want >= 2", accepted)
	}
}
