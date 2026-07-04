package task

// 真机 fixture 测试（红线 4 泛化：绝不凭文档臆造字段）。以下 raw response 全部于 2026-07-05
// 用真实 doubao-seedance-2-0-260128 任务在 ark.cn-beijing.volces.com 捕获（成品 URL 的
// TOS 临时签名 query 已脱敏截断；task id / request id 无敏感性保留原样）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/relay"
)

// —— 真机 raw fixtures ——

// 提交成功（HTTP 200）
const arkSubmitOKFixture = `{"id":"cgt-20260705030718-2lkj4"}`

// 查询：生成中（HTTP 200）——注意无 content/usage/error 字段，且回显任务参数
const arkQueryRunningFixture = `{"id":"cgt-20260705030718-2lkj4","model":"doubao-seedance-2-0-260128","status":"running","created_at":1783192038,"updated_at":1783192038,"service_tier":"default","execution_expires_after":172800,"generate_audio":false,"draft":false,"priority":0}`

// 查询：成功（HTTP 200）——4s/16:9/默认档实测 completion_tokens=87300，回显 resolution=720p/fps=24
const arkQuerySucceededFixture = `{"id":"cgt-20260705030718-2lkj4","model":"doubao-seedance-2-0-260128","status":"succeeded","content":{"video_url":"https://ark-acg-cn-beijing.tos-cn-beijing.volces.com/doubao-seedance-2-0/02178319203885700000000000000000000ffffac181416f7b23e.mp4?X-Tos-Algorithm=TOS4-HMAC-SHA256&X-Tos-Credential=REDACTED&X-Tos-Signature=REDACTED"},"usage":{"completion_tokens":87300,"total_tokens":87300},"created_at":1783192038,"updated_at":1783192284,"seed":91074,"resolution":"720p","ratio":"16:9","duration":4,"framespersecond":24,"service_tier":"default","execution_expires_after":172800,"generate_audio":false,"draft":false,"priority":0}`

// 提交错误：模型不存在（HTTP 404）
const arkErrBadModelFixture = `{"error":{"code":"InvalidEndpointOrModel.NotFound","message":"The model or endpoint not-a-real-model does not exist or you do not have access to it. Request id: 021783192005106474f70d1995dfb4f017267c950669b0a6c52d4","param":"","type":"Not Found"}}`

// 提交错误：参数非法（HTTP 400，duration=1 越界）
const arkErrBadDurationFixture = `{"error":{"code":"InvalidParameter","message":"the parameter duration specified in the request is not valid for model doubao-seedance-2-0 in t2v Request id: 0217831920222791759647e7543ede3d3e5e601fa3b6a5b2c41bd","param":"","type":"BadRequest"}}`

// 提交错误：引用图资源不存在（HTTP 400，param 字段带定位）——图片在提交时同步校验
const arkErrBadImageFixture = `{"error":{"code":"InvalidParameter","message":"The parameter ` + "`content[1].image_url`" + ` specified in the request is not valid: resource not found. Request id: 0217831942871692b35cfdb86d4e4b324c5d7f5a75557a2ca365c","param":"content[1].image_url","type":"BadRequest"}}`

func fixtureResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func TestVolcengineParseSubmitRealFixtures(t *testing.T) {
	ad := volcengineTask{}
	// 真机提交成功响应 → 抽出 task id
	id, err := ad.ParseSubmit(fixtureResp(200, arkSubmitOKFixture))
	if err != nil || id != "cgt-20260705030718-2lkj4" {
		t.Fatalf("ParseSubmit ok fixture: id=%q err=%v", id, err)
	}
	// 真机 404（模型不存在）→ 报错不留任务
	if _, err := ad.ParseSubmit(fixtureResp(404, arkErrBadModelFixture)); err == nil {
		t.Fatal("ParseSubmit must fail on 404 error fixture")
	}
	// 真机 400（参数越界）→ 类型化拒绝错误，携带上游解释（handler 转 400 给客户端）
	_, err = ad.ParseSubmit(fixtureResp(400, arkErrBadDurationFixture))
	var rej *UpstreamRejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Message, "duration") {
		t.Fatalf("ParseSubmit 400 = %v, want UpstreamRejectedError with upstream message", err)
	}
	// 真机 400（引用图 404，param 字段带定位）→ 同样类型化
	_, err = ad.ParseSubmit(fixtureResp(400, arkErrBadImageFixture))
	if !errors.As(err, &rej) || !strings.Contains(rej.Message, "image_url") {
		t.Fatalf("ParseSubmit badimg = %v, want UpstreamRejectedError", err)
	}
}

func TestVolcengineParseQueryRealFixtures(t *testing.T) {
	ad := volcengineTask{}
	// running：无 content/usage 字段 → 归一 StatusRunning，零值不炸
	r, err := ad.ParseQuery(fixtureResp(200, arkQueryRunningFixture))
	if err != nil || r.Status != StatusRunning || r.ResultURL != "" || r.CompletionTokens != 0 {
		t.Fatalf("running fixture: %+v err=%v", r, err)
	}
	// succeeded：真实 usage 87300 + video_url
	r, err = ad.ParseQuery(fixtureResp(200, arkQuerySucceededFixture))
	if err != nil || r.Status != StatusSucceeded {
		t.Fatalf("succeeded fixture: %+v err=%v", r, err)
	}
	if r.CompletionTokens != 87300 {
		t.Fatalf("completion_tokens = %d, want 87300 (real usage)", r.CompletionTokens)
	}
	if !strings.HasPrefix(r.ResultURL, "https://ark-acg-cn-beijing.tos-cn-beijing.volces.com/") {
		t.Fatalf("result url = %q", r.ResultURL)
	}
	// 上游 5xx → 报错（下轮重试），不误判状态
	if _, err := ad.ParseQuery(fixtureResp(500, `{"error":{"message":"internal"}}`)); err == nil {
		t.Fatal("ParseQuery must fail on 5xx")
	}
}

// SubmitRequest：Seedance 2.0 顶层任务参数透传（generate_audio/ratio/duration/watermark），
// model/content 由平台字段权威覆盖（Params 注入同名键不得顶掉准入校验过的值）。
func TestVolcengineSubmitRequestParamPassthrough(t *testing.T) {
	ad := volcengineTask{}
	ch := &relay.Channel{BaseURL: "https://ark.cn-beijing.volces.com/api/v3"}
	req, err := ad.SubmitRequest(context.Background(), ch, "k", SubmitInput{
		Model:   "doubao-seedance-2-0-260128",
		Content: []any{map[string]any{"type": "text", "text": "hi"}},
		Params: map[string]any{
			"generate_audio": true, "ratio": "16:9", "duration": float64(11), "watermark": false,
			"model": "injected-model", // 恶意注入：必须被平台 model 覆盖
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks" {
		t.Fatalf("submit url = %s", req.URL)
	}
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "doubao-seedance-2-0-260128" {
		t.Fatalf("model = %v — Params 注入顶掉了平台 model", body["model"])
	}
	if body["generate_audio"] != true || body["ratio"] != "16:9" ||
		body["duration"] != float64(11) || body["watermark"] != false {
		t.Fatalf("params not passed through: %v", body)
	}
}

// EstimateOutputTokens：真机标定回归——公式与实测偏差、参数感知缩放、floor/cap、保守化封堵。
func TestVolcengineEstimateCalibration(t *testing.T) {
	ad := volcengineTask{}
	cleanContent := []any{map[string]any{"type": "text", "text": "a red apple rolls slowly"}}
	// 显式 720p 4s（真机任务口径）：实测 87300，估算必须覆盖（上界）且贴近（<2×）
	est := ad.EstimateOutputTokens("doubao-seedance-2-0-260128", cleanContent,
		map[string]any{"duration": float64(4), "ratio": "16:9", "resolution": "720p"})
	if est < 87300 {
		t.Fatalf("est %d < real usage 87300 — capture>hold 负余额风险", est)
	}
	if est > 87300*2 {
		t.Fatalf("explicit 720p est %d > 2× real usage — 过度冻结", est)
	}
	// 无显式 resolution（真机默认 720p 出片 87300）：基准按 1080p 保守覆盖
	// （家族内 seedance-1-0-pro 默认 1080p + prompt 指令可达 1080p——对抗 review CONFIRMED 封堵）
	est = ad.EstimateOutputTokens("m", cleanContent, map[string]any{"duration": float64(4)})
	if est != 252_720 { // 1080p×24×4/1024×1.3
		t.Fatalf("no-resolution est = %d, want 252720 (1080p 保守基准)", est)
	}
	// 无参数默认（5s，1080p 基准）= 315,900
	if est := ad.EstimateOutputTokens("m", cleanContent, nil); est != 315_900 {
		t.Fatalf("default est = %d, want 315900", est)
	}
	// 1080p 11s（用户实际用例口径）必须超过旧固定值 500k（旧实现会 capture>hold）
	est = ad.EstimateOutputTokens("m", cleanContent, map[string]any{"duration": float64(11), "resolution": "1080p"})
	if est <= 500_000 {
		t.Fatalf("1080p 11s est = %d, want > 500k (旧固定上界不足)", est)
	}
	// prompt 内嵌指令（--resolution/--dur 方言可绕过顶层参数）→ 一律上界估（对抗 review CONFIRMED）。
	// 验修 INCOMPLETE 封堵的三类绕过一并锁死："Text" 变体键、纯字符串项、全角 －－。
	for name, dc := range map[string][]any{
		"text key":       {map[string]any{"type": "text", "text": "epic drone flight --resolution 1080p --duration 10"}},
		"Text variant":   {map[string]any{"type": "text", "Text": "flight --resolution 4k --duration 15"}},
		"bare string":    {"a scene --dur 12"},
		"fullwidth dash": {map[string]any{"type": "text", "text": "一段风景 －－resolution 1080p"}},
		"unknown shape":  {float64(42)},
	} {
		if est := ad.EstimateOutputTokens("m", dc, nil); est != 4_000_000 {
			t.Fatalf("directive detection %q: est = %d, want cap (指令/未知形态绝不低估)", name, est)
		}
	}
	// 嵌套 URL 含 "--" 不误伤（image_url.url 在嵌套 map，不参与扫描）
	i2v := []any{
		map[string]any{"type": "text", "text": "animate this"},
		map[string]any{"type": "image_url", "role": "reference_image",
			"image_url": map[string]any{"url": "https://cdn.example.com/co--op.jpg"}},
	}
	if est := ad.EstimateOutputTokens("m", i2v, nil); est == 4_000_000 {
		t.Fatal("nested URL with -- must not trigger directive cap (i2v 误伤)")
	}
	// fps 参数纳入因子（响应回显 framespersecond，若上游接受更高 fps 不得低估）
	base := ad.EstimateOutputTokens("m", cleanContent, map[string]any{"duration": float64(4), "resolution": "720p"})
	doubled := ad.EstimateOutputTokens("m", cleanContent, map[string]any{"duration": float64(4), "resolution": "720p", "fps": float64(48)})
	if doubled != base*2 {
		t.Fatalf("fps 48 est = %d, want 2× base %d", doubled, base)
	}
	// floor：超小任务不至于 hold 过小
	if est := ad.EstimateOutputTokens("m", cleanContent, map[string]any{"duration": float64(4), "resolution": "480p"}); est != 100_000 {
		t.Fatalf("floor est = %d, want 100000", est)
	}
	// cap：天量 duration 不至于冻结无界资金
	if est := ad.EstimateOutputTokens("m", cleanContent, map[string]any{"duration": float64(99999), "resolution": "4k"}); est != 4_000_000 {
		t.Fatalf("cap est = %d, want 4000000", est)
	}
	// 畸形参数保守化（资金红线）：字符串 duration（上游可能宽容接受）→ 上界估，绝不按默认低估
	if est := ad.EstimateOutputTokens("m", cleanContent, map[string]any{"duration": "11"}); est != 4_000_000 {
		t.Fatalf("string duration est = %d, want cap 4000000 (低估=capture>hold)", est)
	}
	// 未知 resolution 档位 → 按 4K 上界估（识别失败绝不低估）
	est = ad.EstimateOutputTokens("m", cleanContent, map[string]any{"duration": float64(5), "resolution": "2160p"})
	want4k5s := int64(float64(3840*2160) * 24 * 5 / 1024 * 1.3)
	if est != want4k5s {
		t.Fatalf("unknown resolution est = %d, want 4k-basis %d", est, want4k5s)
	}
}

// ParseQuery 终态别名："canceled" 单 L 拼写不得落 default→running（方言漂移会拖 30min 超时）。
func TestVolcengineParseQueryCanceledAlias(t *testing.T) {
	ad := volcengineTask{}
	for _, st := range []string{"failed", "cancelled", "canceled"} {
		r, err := ad.ParseQuery(fixtureResp(200, `{"id":"cgt-x","status":"`+st+`","error":{"message":"boom"}}`))
		if err != nil || r.Status != StatusFailed {
			t.Fatalf("status %q → %+v (err %v), want StatusFailed", st, r, err)
		}
	}
}
