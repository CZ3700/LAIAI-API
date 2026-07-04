package task

// Vidu adaptor 测试。fixture 为 New API 参考实现推导（字段/状态按其生产解析结构体，非我方真机捕获——
// 真机验证走 TestRealViduTaskSmoke，env 门控）。

import (
	"context"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/relay"
)

// 提交路径按图片数量/显式 action 分叉。
func TestViduSubmitPath(t *testing.T) {
	cases := map[string]struct {
		params map[string]any
		want   string
	}{
		"text (0 img)":       {map[string]any{}, "/text2video"},
		"img (1)":            {map[string]any{"images": []any{"a"}}, "/img2video"},
		"start-end (2)":      {map[string]any{"images": []any{"a", "b"}}, "/start-end2video"},
		"reference (3)":      {map[string]any{"images": []any{"a", "b", "c"}}, "/reference2video"},
		"explicit action":    {map[string]any{"action": "img2video", "images": []any{}}, "/img2video"},
		"invalid action→img": {map[string]any{"action": "bogus", "images": []any{"a"}}, "/img2video"},
	}
	for name, c := range cases {
		if got := viduSubmitPath(c.params); got != c.want {
			t.Fatalf("%s: path = %s, want %s", name, got, c.want)
		}
	}
}

// ParseSubmit：task_id 提取；state=="failed" 是同步失败（ParseSubmit 阶段拦截）；400 类型化。
func TestViduParseSubmit(t *testing.T) {
	ad := viduTask{}
	id, err := ad.ParseSubmit(fixtureResp(200, `{"task_id":"vidu-task-1","state":"created"}`))
	if err != nil || id != "vidu-task-1" {
		t.Fatalf("submit ok = %q err=%v", id, err)
	}
	// 时序陷阱：提交响应 state=failed 即同步失败
	if _, err := ad.ParseSubmit(fixtureResp(200, `{"state":"failed"}`)); err == nil {
		t.Fatal("state=failed must error at ParseSubmit (not wait for poll)")
	}
	// 400 → 类型化拒绝
	_, err = ad.ParseSubmit(fixtureResp(400, `{"err_code":"invalid_param"}`))
	if _, ok := err.(*UpstreamRejectedError); !ok {
		t.Fatalf("400 = %v, want UpstreamRejectedError", err)
	}
}

// ParseQuery：success→credits 结算 + creations[0].url；方言 queueing/failed；未知兜底 running。
func TestViduParseQuery(t *testing.T) {
	ad := viduTask{}
	// 成功：credits=7 → 7M 伪 token
	r, err := ad.ParseQuery(fixtureResp(200, `{"state":"success","credits":7,
		"creations":[{"id":"c1","url":"https://cdn.vidu/v.mp4","cover_url":"x"}]}`))
	if err != nil || r.Status != StatusSucceeded {
		t.Fatalf("success = %+v err=%v", r, err)
	}
	if r.ResultURL != "https://cdn.vidu/v.mp4" {
		t.Fatalf("url = %q", r.ResultURL)
	}
	if r.CompletionTokens != 7_000_000 {
		t.Fatalf("tokens = %d, want 7000000 (7 credits×1M)", r.CompletionTokens)
	}
	// 状态方言归一：queueing（双 e）、failed（非 failure）
	for st, want := range map[string]string{
		"created": StatusQueued, "queueing": StatusQueued, "processing": StatusRunning,
		"failed": StatusFailed, "mystery": StatusRunning,
	} {
		r, _ := ad.ParseQuery(fixtureResp(200, `{"state":"`+st+`","err_code":"E1"}`))
		if r.Status != want {
			t.Fatalf("state %q → %q, want %q", st, r.Status, want)
		}
	}
	// 失败带 err_code（无 message）
	r, _ = ad.ParseQuery(fixtureResp(200, `{"state":"failed","err_code":"content_policy"}`))
	if r.FailReason != "content_policy" {
		t.Fatalf("fail reason = %q", r.FailReason)
	}
	// credits 缺失 → CompletionTokens=0（走估算）
	r, _ = ad.ParseQuery(fixtureResp(200, `{"state":"success","creations":[{"url":"u"}]}`))
	if r.CompletionTokens != 0 {
		t.Fatalf("no credits tokens = %d, want 0", r.CompletionTokens)
	}
}

// EstimateOutputTokens：duration×resolution 上界（基准 5s/1080p=8 credits，对抗 review 放宽）；未知/畸形取最高档。
func TestViduEstimate(t *testing.T) {
	ad := viduTask{}
	base := ad.EstimateOutputTokens("viduq1", nil, map[string]any{"duration": float64(5), "resolution": "1080p"})
	if base != 8_000_000 {
		t.Fatalf("5s/1080p est = %d, want 8M", base)
	}
	// 8s 更贵
	if e := ad.EstimateOutputTokens("m", nil, map[string]any{"duration": float64(8), "resolution": "1080p"}); e <= base {
		t.Fatalf("8s est = %d, want > 5s %d", e, base)
	}
	// 未知分辨率 → 上界（2× → 16M）
	if e := ad.EstimateOutputTokens("m", nil, map[string]any{"duration": float64(5), "resolution": "4k"}); e != 16_000_000 {
		t.Fatalf("4k est = %d, want 16M (未知档上界)", e)
	}
	// 畸形 duration（字符串）→ 40 credits 上界
	if e := ad.EstimateOutputTokens("m", nil, map[string]any{"duration": "10"}); e != 40_000_000 {
		t.Fatalf("string duration est = %d, want cap 40M", e)
	}
}

// SubmitRequest：prompt/images 从 content 提取进上游体；reference2video 模型归一（对抗 review）。
func TestViduSubmitCarriesContent(t *testing.T) {
	ad := viduTask{}
	// 纯文本 → text2video + prompt
	req, err := ad.SubmitRequest(context.Background(),
		&relay.Channel{BaseURL: "https://api.vidu.cn"}, "k",
		SubmitInput{Model: "viduq1", Content: []any{map[string]any{"type": "text", "text": "a dog"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(req.URL.Path, "/text2video") {
		t.Fatalf("text path = %s", req.URL.Path)
	}
	if body := readJSONBody(t, req); body["prompt"] != "a dog" {
		t.Fatalf("prompt not carried: %v", body["prompt"])
	}
	// 3 图 content → reference2video + 模型归一 viduq2-pro → viduq2
	req, _ = ad.SubmitRequest(context.Background(),
		&relay.Channel{BaseURL: "https://api.vidu.cn"}, "k",
		SubmitInput{Model: "viduq2-pro", Content: []any{
			map[string]any{"type": "text", "text": "ref"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "a"}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "b"}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "c"}},
		}})
	if !strings.HasSuffix(req.URL.Path, "/reference2video") {
		t.Fatalf("3-img path = %s, want reference2video", req.URL.Path)
	}
	body := readJSONBody(t, req)
	if body["model"] != "viduq2" {
		t.Fatalf("model = %v, want viduq2 (归一去 -pro)", body["model"])
	}
	if imgs, _ := body["images"].([]any); len(imgs) != 3 {
		t.Fatalf("images not carried: %v", body["images"])
	}
}

// taskContentParts：文本拼接 + 图片提取 + 大小写变体键容忍（验修残留补锁）。
func TestTaskContentParts(t *testing.T) {
	prompt, images := taskContentParts([]any{
		map[string]any{"type": "text", "text": "line one"},
		map[string]any{"type": "text", "text": "line two"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://img/1.png"}},
		map[string]any{"type": "image_url", "Image_URL": map[string]any{"URL": "https://img/2.png"}}, // 变体键
		map[string]any{"type": "image", "Image": "https://img/3.png"},                                // 扁平变体
	})
	if prompt != "line one\nline two" {
		t.Fatalf("prompt = %q", prompt)
	}
	if len(images) != 3 || images[0] != "https://img/1.png" || images[2] != "https://img/3.png" {
		t.Fatalf("images = %v (变体键/扁平 image 未提取)", images)
	}
	// 空/非 map 项容忍
	if p, im := taskContentParts([]any{"bare string", 42, nil}); p != "" || len(im) != 0 {
		t.Fatalf("non-map items: prompt=%q images=%v", p, im)
	}
}

// 注册表：kling/vidu 已登记，可解析到各自 adaptor。
func TestBatch2AdaptorRegistered(t *testing.T) {
	if ad, err := adaptorFor("kling"); err != nil || func() bool { _, ok := ad.(klingTask); return !ok }() {
		t.Fatalf("kling adaptor: %T err=%v", ad, err)
	}
	if ad, err := adaptorFor("vidu"); err != nil {
		t.Fatalf("vidu adaptor err=%v", err)
	} else if _, ok := ad.(viduTask); !ok {
		t.Fatalf("vidu adaptor = %T", ad)
	}
	// 凭证错误信息不含内容（红线 7）——kling 凭证格式错误面
	if _, _, err := klingCredential("secret-should-not-appear"); err == nil ||
		strings.Contains(err.Error(), "secret-should-not-appear") {
		t.Fatalf("kling credential error hygiene: %v", err)
	}
}
