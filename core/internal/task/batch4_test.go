package task

// §3.8 批次 4（sora/veo）adaptor 测试。fixture 为 New API 参考实现推导（非我方真机捕获——
// 真机验证走 real_batch4_task_smoke_test.go，env 门控）。

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// —— sora ——

func TestSoraParseSubmit(t *testing.T) {
	ad := soraTask{}
	// id 主字段
	id, err := ad.ParseSubmit(fixtureResp(200, `{"id":"video_abc","object":"video","status":"queued","seconds":"4","size":"720x1280"}`))
	if err != nil || id != "video_abc" {
		t.Fatalf("submit = %q err=%v", id, err)
	}
	// task_id 兜底（转售网关方言）
	id, err = ad.ParseSubmit(fixtureResp(200, `{"task_id":"video_alt","status":"queued"}`))
	if err != nil || id != "video_alt" {
		t.Fatalf("task_id fallback = %q err=%v", id, err)
	}
	// 400 → 类型化拒绝带 error.message
	_, err = ad.ParseSubmit(fixtureResp(400, `{"error":{"message":"invalid size for sora-2","code":"invalid_request"}}`))
	var rej *UpstreamRejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Message, "invalid size") {
		t.Fatalf("400 = %v, want typed rejection", err)
	}
}

func TestSoraParseQuery(t *testing.T) {
	ad := soraTask{}
	// completed：ResultRef=上游 id（产物走代理）+ A 类结算 seconds×sizeRatio
	r, err := ad.ParseQuery(fixtureResp(200, `{"id":"video_abc","status":"completed","seconds":"8","size":"1792x1024"}`))
	if err != nil || r.Status != StatusSucceeded {
		t.Fatalf("completed = %+v err=%v", r, err)
	}
	if r.ResultRef != "video_abc" || r.ResultURL != "" {
		t.Fatalf("want ResultRef=video_abc（走代理），got ref=%q url=%q", r.ResultRef, r.ResultURL)
	}
	if want := int64(8 * 1.666667 * 1_000_000); r.CompletionTokens != want {
		t.Fatalf("settle tokens = %d, want %d (8s×1.667)", r.CompletionTokens, want)
	}
	// 状态方言：pending/processing/cancelled（转售网关）+ 官方四态
	for st, want := range map[string]string{
		"queued": StatusQueued, "pending": StatusQueued,
		"processing": StatusRunning, "in_progress": StatusRunning,
		"failed": StatusFailed, "cancelled": StatusFailed, "weird": StatusRunning,
	} {
		r, _ := ad.ParseQuery(fixtureResp(200, `{"id":"v","status":"`+st+`"}`))
		if r.Status != want {
			t.Fatalf("status %q → %q, want %q", st, r.Status, want)
		}
	}
	// 查询期 429 限流 → error（下轮重试，非终态）
	if _, err := ad.ParseQuery(fixtureResp(429, `{"error":{"message":"rate limited","code":"429"}}`)); err == nil {
		t.Fatal("429 must be transient error, not terminal")
	}
	// failed 带 error.message
	r, _ = ad.ParseQuery(fixtureResp(200, `{"id":"v","status":"failed","error":{"message":"content policy"}}`))
	if r.FailReason != "content policy" {
		t.Fatalf("fail reason = %q", r.FailReason)
	}
}

func TestSoraEstimateAndSubmitBody(t *testing.T) {
	ad := soraTask{}
	// 4s 720 = 4M；8s 1792 档 = 13.33M
	if e := ad.EstimateOutputTokens("sora-2", nil, map[string]any{"seconds": "4", "size": "720x1280"}); e != 4_000_000 {
		t.Fatalf("4s est = %d", e)
	}
	if e := ad.EstimateOutputTokens("sora-2-pro", nil, map[string]any{"seconds": "8", "size": "1792x1024"}); e != int64(8*1.666667*1_000_000) {
		t.Fatalf("8s pro est = %d", e)
	}
	// 畸形 seconds → 上界
	if e := ad.EstimateOutputTokens("m", nil, map[string]any{"seconds": "abc"}); e != 30_000_000 {
		t.Fatalf("malformed est = %d", e)
	}
	// SubmitRequest：prompt 从 content、seconds 字符串化、duration 数字换算、input_reference
	req, err := ad.SubmitRequest(t.Context(), chBase("https://api.openai.com/v1"), "sk-x",
		SubmitInput{Model: "sora-2", Content: []any{
			map[string]any{"type": "text", "text": "a fox"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://img/ref.png"}},
		}, Params: map[string]any{"duration": float64(8)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(req.URL.Path, "/videos") {
		t.Fatalf("url = %s", req.URL)
	}
	body := readJSONBody(t, req)
	if body["prompt"] != "a fox" || body["input_reference"] != "https://img/ref.png" {
		t.Fatalf("prompt/input_reference: %v", body)
	}
	if body["seconds"] != "8" { // duration(数字)→seconds(字符串) 方言换算
		t.Fatalf("seconds = %v (want string \"8\")", body["seconds"])
	}
	if _, hasDur := body["duration"]; hasDur {
		t.Fatal("duration must be converted away")
	}
}

// —— veo ——

func TestVeoParseSubmitAndQueryURL(t *testing.T) {
	ad := veoTask{}
	id, err := ad.ParseSubmit(fixtureResp(200, `{"name":"models/veo-3.1-generate-preview/operations/op123"}`))
	if err != nil || id != "models/veo-3.1-generate-preview/operations/op123" {
		t.Fatalf("submit = %q err=%v", id, err)
	}
	// 查询 URL 直接拼完整 operation name
	req, err := ad.QueryRequest(t.Context(), chBase("https://generativelanguage.googleapis.com/v1beta"), "gk", id)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://generativelanguage.googleapis.com/v1beta/models/veo-3.1-generate-preview/operations/op123" {
		t.Fatalf("query url = %s", req.URL)
	}
	if req.Header.Get("x-goog-api-key") != "gk" {
		t.Fatal("missing x-goog-api-key")
	}
}

func TestVeoParseQueryLRO(t *testing.T) {
	ad := veoTask{}
	// done=false → running
	r, _ := ad.ParseQuery(fixtureResp(200, `{"done":false}`))
	if r.Status != StatusRunning {
		t.Fatalf("done=false → %q", r.Status)
	}
	// error 优先于 done（done=false 也失败）
	r, _ = ad.ParseQuery(fixtureResp(200, `{"done":false,"error":{"message":"quota exceeded"}}`))
	if r.Status != StatusFailed || r.FailReason != "quota exceeded" {
		t.Fatalf("error-first = %+v", r)
	}
	// 5 种 URI 方言逐一验证 → ResultRef（绝不进 ResultURL——带 key）
	dialects := map[string]string{
		"新版 generatedVideos": `{"done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"https://files/g1?key=SECRET"}}]}}}`,
		"旧版 generatedSamples": `{"done":true,"response":{"generateVideoResponse":{"generatedSamples":[{"video":{"uri":"https://files/g2"}}]}}}`,
		"videos 数组":          `{"done":true,"response":{"videos":[{"uri":"https://files/g3"}]}}`,
		"video 字符串":          `{"done":true,"response":{"video":"https://files/g4"}}`,
		"顶层 uri":             `{"done":true,"uri":"https://files/g6"}`,
	}
	for name, body := range dialects {
		r, err := ad.ParseQuery(fixtureResp(200, body))
		if err != nil || r.Status != StatusSucceeded || r.ResultRef == "" {
			t.Fatalf("dialect %s: %+v err=%v", name, r, err)
		}
		if r.ResultURL != "" {
			t.Fatalf("dialect %s: URI 泄漏进 ResultURL（带 key，必须走代理）", name)
		}
	}
	// RAI 内容过滤空成功 → failed（不空转到超时）
	r, _ = ad.ParseQuery(fixtureResp(200, `{"done":true,"response":{"generateVideoResponse":{"raiMediaFilteredCount":1}}}`))
	if r.Status != StatusFailed || !strings.Contains(r.FailReason, "filtered") {
		t.Fatalf("RAI filtered = %+v, want failed", r)
	}
}

func TestVeoEstimate(t *testing.T) {
	ad := veoTask{}
	// 默认 8s 720p = 8M
	if e := ad.EstimateOutputTokens("veo-3.1-generate-preview", nil, nil); e != 8_000_000 {
		t.Fatalf("default est = %d", e)
	}
	// 4k 按模型分档
	if e := ad.EstimateOutputTokens("veo-3.1-generate-preview", nil, map[string]any{"duration": float64(8), "resolution": "4k"}); e != int64(8*1.5*1_000_000) {
		t.Fatalf("veo-3.1 4k est = %d", e)
	}
	if e := ad.EstimateOutputTokens("veo-3.1-fast-generate-preview", nil, map[string]any{"duration": float64(8), "resolution": "4K"}); e != int64(8*2.333333*1_000_000) {
		t.Fatalf("veo-3.1-fast 4k est = %d", e)
	}
	// 对抗 review CONFIRMED：数字型 seconds 必须正确识别（此前判畸形→30M 上界 3.75× 多收）
	if e := ad.EstimateOutputTokens("veo-3.1-generate-preview", nil, map[string]any{"seconds": float64(8)}); e != 8_000_000 {
		t.Fatalf("numeric seconds est = %d, want 8M (不得判畸形上界)", e)
	}
	// 字符串 seconds 仍支持
	if e := ad.EstimateOutputTokens("m", nil, map[string]any{"seconds": "8"}); e != 8_000_000 {
		t.Fatalf("string seconds est = %d", e)
	}
	// 真畸形（带单位）→ 上界
	if e := ad.EstimateOutputTokens("m", nil, map[string]any{"seconds": "8s"}); e != 30_000_000 {
		t.Fatalf("malformed seconds est = %d, want cap", e)
	}
}

// 对抗 review CONFIRMED：sampleCount 恒 1——客户端传 sampleCount:4 不得顶掉（否则 4 视频按 1 计费）；
// 数字 seconds 进 durationSeconds 且不透传原 key。
func TestVeoSubmitPinsSampleCountAndSeconds(t *testing.T) {
	ad := veoTask{}
	req, err := ad.SubmitRequest(t.Context(), chBase("https://gl.googleapis.com/v1beta"), "gk",
		SubmitInput{Model: "veo-3.1-generate-preview",
			Content: []any{map[string]any{"type": "text", "text": "a fox"}},
			Params:  map[string]any{"sampleCount": float64(4), "seconds": float64(8)}})
	if err != nil {
		t.Fatal(err)
	}
	body := readJSONBody(t, req)
	params, _ := body["parameters"].(map[string]any)
	if params["sampleCount"] != float64(1) {
		t.Fatalf("sampleCount = %v, want 1 (客户端 4 不得顶掉——少收红线)", params["sampleCount"])
	}
	if params["durationSeconds"] != float64(8) {
		t.Fatalf("durationSeconds = %v, want 8 (数字 seconds 换算)", params["durationSeconds"])
	}
	if _, leaked := params["seconds"]; leaked {
		t.Fatal("原 seconds key 不得透传")
	}
}

// ContentStreamer：sora bearer / veo x-goog-api-key 取流 + Content-Type 透传。
func TestBatch4ContentStreamers(t *testing.T) {
	// sora
	up := mockJSONServer(t, func(path string) (int, string) {
		if strings.Contains(path, "/videos/video_abc/content") {
			return 200, "FAKE_MP4_BYTES"
		}
		return 404, `{}`
	})
	defer up.Close()
	rc, _, err := soraTask{}.StreamResult(t.Context(), up.Client(), chBase(up.URL), "sk", "video_abc")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "FAKE_MP4_BYTES" {
		t.Fatalf("sora stream = %q", b)
	}
	// veo：URI 直接是绝对地址，带 x-goog-api-key
	up2 := mockJSONServer(t, func(string) (int, string) { return 200, "VEO_BYTES" })
	defer up2.Close()
	rc, _, err = veoTask{}.StreamResult(t.Context(), up2.Client(), chBase(""), "gk", up2.URL+"/files/dl")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(rc)
	rc.Close()
	if string(b) != "VEO_BYTES" {
		t.Fatalf("veo stream = %q", b)
	}
}

func TestBatch4Registered(t *testing.T) {
	for _, code := range []string{"sora", "veo"} {
		ad, err := adaptorFor(code)
		if err != nil {
			t.Fatalf("adaptorFor(%q): %v", code, err)
		}
		if _, ok := ad.(ContentStreamer); !ok {
			t.Fatalf("%s must implement ContentStreamer", code)
		}
	}
}
