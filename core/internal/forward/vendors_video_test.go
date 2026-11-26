package forward

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDomesticVendorVideoBuilders(t *testing.T) {
	t.Run("volcengine multimodal role override and draft task", func(t *testing.T) {
		body := map[string]any{
			"prompt": "cat playing guitar",
			"images": []any{
				map[string]any{"url": "https://img.test/a.png", "role": "first_frame"},
			},
			"videos":     []any{"https://vid.test/ref.mp4"},
			"duration":   8,
			"ratio":      "16:9",
			"web_search": true,
		}
		built := BuildVolcengineVideoBody("doubao-seedance-2-5", body)
		if built["model"] != "doubao-seedance-2-5" {
			t.Fatalf("model = %v", built["model"])
		}
		content, ok := built["content"].([]any)
		if !ok || len(content) != 3 {
			t.Fatalf("expected 3 content entries, got %+v", built["content"])
		}
		imgEntry := content[1].(map[string]any)
		if imgEntry["role"] != "reference_image" {
			t.Fatalf("multimodal image role must be forced to reference_image, got %v", imgEntry["role"])
		}
		if _, hasTools := built["tools"]; !hasTools {
			t.Fatal("expected web_search to populate tools")
		}

		draftBuilt := BuildVolcengineVideoBody("doubao-seedance-2-5", map[string]any{
			"draft_task_id": "cgt-upstream-draft-1",
			"resolution":    "1080p",
			"duration":      10,
			"ratio":         "16:9",
		})
		draftContent := draftBuilt["content"].([]any)
		if len(draftContent) != 1 || draftContent[0].(map[string]any)["type"] != "draft_task" {
			t.Fatalf("expected draft_task item, got %+v", draftContent)
		}
		if _, exists := draftBuilt["duration"]; exists {
			t.Fatal("draft_task stage 2 must omit duration/ratio/seed")
		}
	})

	t.Run("kling v3 omni builder and dynamic path", func(t *testing.T) {
		built := BuildKlingV3VideoBody(map[string]any{
			"prompt": "smooth camera orbit",
			"images": []any{"https://img.test/1.png", "https://img.test/2.png"},
			"videos": []any{"https://vid.test/motion.mp4"},
		}, "/text-to-video/kling-v3")
		contents := built["contents"].([]any)
		if len(contents) != 4 {
			t.Fatalf("expected prompt + 2 images + 1 video, got %d", len(contents))
		}
		dynPath := ResolveKlingDynamicPath("/text-to-video/${model}", "kling-v3", built)
		if dynPath != "/omni-video/kling-v3" {
			t.Fatalf("expected /omni-video/kling-v3, got %s", dynPath)
		}
	})

	t.Run("vidu role order and dynamic path", func(t *testing.T) {
		reqBody := map[string]any{
			"prompt": "bloom",
			"images": []any{
				map[string]any{"url": "https://img.test/end.png", "role": "last_frame"},
				map[string]any{"url": "https://img.test/start.png", "role": "first_frame"},
			},
			"generate_audio": true,
			"service_tier":   "flex",
			"ratio":          "16:9",
		}
		built := BuildViduVideoBody("viduq3-pro", reqBody)
		imgs := built["images"].([]string)
		if len(imgs) != 2 || imgs[0] != "https://img.test/start.png" || imgs[1] != "https://img.test/end.png" {
			t.Fatalf("vidu must order first_frame before last_frame, got %v", imgs)
		}
		if built["audio"] != true || built["off_peak"] != true || built["aspect_ratio"] != "16:9" {
			t.Fatalf("vidu mapped params incorrect: %+v", built)
		}
		path := ResolveViduDynamicPath("/ent/v2/img2video", "/v1/video/generations", reqBody)
		if path != "/ent/v2/start-end2video" {
			t.Fatalf("expected /ent/v2/start-end2video, got %s", path)
		}
	})

	t.Run("dashscope wan and happyhorse", func(t *testing.T) {
		built := BuildDashScopeVideoBody("happyhorse-1-0", map[string]any{
			"prompt":     "horse running",
			"videos":     []any{"https://vid.test/src.mp4"},
			"resolution": "1080p",
		})
		input := built["input"].(map[string]any)
		media := input["media"].([]any)
		if media[0].(map[string]any)["type"] != "video" {
			t.Fatalf("happyhorse video type should be 'video', got %v", media[0])
		}
		params := built["parameters"].(map[string]any)
		if params["resolution"] != "1080P" {
			t.Fatalf("expected uppercase resolution 1080P, got %v", params["resolution"])
		}
	})

	t.Run("minimax jimeng and tencent vod", func(t *testing.T) {
		mm := BuildMiniMaxVideoBody("MiniMax-H3", map[string]any{
			"prompt": "ocean waves", "watermark": true, "duration": 6,
		})
		if mm["aigc_watermark"] != true {
			t.Fatalf("expected aigc_watermark=true, got %+v", mm)
		}

		jm := BuildJimengVideoBody("dreamina_ic_generate_video_v2", map[string]any{
			"prompt": "sunrise", "duration": 10, "ratio": "16:9",
			"images": []any{"data:image/png;base64,iVBORw0KGgoAAA"},
		})
		if jm["req_key"] != "dreamina_ic_generate_video_v2" || jm["frames"] != 241 {
			t.Fatalf("unexpected jimeng payload: %+v", jm)
		}
		b64s := jm["binary_data_base64"].([]string)
		if len(b64s) != 1 || b64s[0] != "iVBORw0KGgoAAA" {
			t.Fatalf("expected stripped data uri base64, got %v", b64s)
		}

		tc := BuildTencentVODVideoBody("Kling@2.5", 130001, map[string]any{
			"prompt":         "dragon dance",
			"images":         []any{"https://img.test/f1.png"},
			"duration":       5,
			"resolution":     "1080p",
			"generate_audio": true,
		})
		if tc["ModelName"] != "Kling" || tc["ModelVersion"] != "2.5" || tc["SubAppId"] != uint64(130001) {
			t.Fatalf("unexpected tencent vod header fields: %+v", tc)
		}
	})
}

func TestSignersAndCascadeHelpers(t *testing.T) {
	now := time.Date(2026, 11, 26, 1, 2, 3, 0, time.UTC)

	t.Run("volc v4 and tencent tc3 and kling jwt", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://visual.volcengineapi.com/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31", nil)
		if err := SignVolcengineV4(req, "ak123", "sk456", "cv", "cn-north-1", []byte(`{"req_key":"k"}`), now); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(req.Header.Get("Authorization"), "HMAC-SHA256 Credential=ak123/20261126/cn-north-1/cv/request") {
			t.Fatalf("unexpected volc authorization: %s", req.Header.Get("Authorization"))
		}

		tcReq, _ := http.NewRequest(http.MethodPost, "https://vod.tencentcloudapi.com/", nil)
		if err := SignTencentTC3(tcReq, "sid1", "skey2", "vod", "CreateAigcVideoTask", "2018-07-17", "", []byte(`{}`), now); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tcReq.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=sid1/2026-11-26/vod/tc3_request") {
			t.Fatalf("unexpected tc3 authorization: %s", tcReq.Header.Get("Authorization"))
		}
		if tcReq.Header.Get("X-TC-Action") != "CreateAigcVideoTask" {
			t.Fatalf("missing X-TC-Action")
		}

		jwtTok, err := ResolveKlingBearer("ak:sk", now)
		if err != nil || strings.Count(jwtTok, ".") != 2 {
			t.Fatalf("expected 3-part jwt for ak:sk, got %q err=%v", jwtTok, err)
		}
		rawKey, err := ResolveKlingBearer("kling-api-key-v3", now)
		if err != nil || rawKey != "kling-api-key-v3" {
			t.Fatalf("expected passthrough v3 key, got %q err=%v", rawKey, err)
		}
	})

	t.Run("cascade helpers", func(t *testing.T) {
		fwd := &ResolvedForward{
			IsCascade: true,
			ResMul:    map[string]float64{"480p": 1.5, "720p": 2.15, "1080p": 2.25},
		}
		if mul := fwd.LookupResMul("1080P"); mul != 2.25 {
			t.Fatalf("LookupResMul = %v, want 2.25", mul)
		}
		if fwd.IsResEnhanceEnabled("480p") {
			t.Fatal("480p should skip S2 by default")
		}
		if !fwd.IsResEnhanceEnabled("1080p") {
			t.Fatal("1080p should enable S2")
		}
		if fwd.CascadeResolveBase("1080p") != "720p" {
			t.Fatalf("CascadeResolveBase(1080p) = %s", fwd.CascadeResolveBase("1080p"))
		}
		mode, path, model := fwd.ResolveVolcEnhanceTarget("1080p")
		if mode != "standard" || path != "/api/v1/tools/enhance-video" || model != "vve-sd" {
			t.Fatalf("unexpected enhance target: %s %s %s", mode, path, model)
		}
	})
}
