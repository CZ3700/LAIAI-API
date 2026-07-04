package task

// §3.8 批次 3（hailuo/wan-ali/suno）adaptor 测试。fixture 为 New API 参考实现推导（字段/状态按其
// 生产解析结构体，非我方真机捕获——真机验证走 real_batch3_task_smoke_test.go，env 门控）。

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/relay"
)

// mockJSONServer：path→(status,body) 的 JSON mock 上游（二跳取件测试用）。
func mockJSONServer(t *testing.T, route func(path string) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, body := route(r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	return srv
}

func chBase(url string) *relay.Channel { return &relay.Channel{BaseURL: url} }

func readTaskBody(t *testing.T, req *http.Request) map[string]any { return readJSONBody(t, req) }

// —— hailuo ——

func TestHailuoParseSubmit(t *testing.T) {
	ad := hailuoTask{}
	// base_resp.status_code==0 成功
	id, err := ad.ParseSubmit(fixtureResp(200, `{"task_id":"h-1","base_resp":{"status_code":0,"status_msg":"success"}}`))
	if err != nil || id != "h-1" {
		t.Fatalf("submit ok = %q err=%v", id, err)
	}
	// 双层错误：HTTP 200 但 base_resp.status_code!=0 → 类型化拒绝（1026 内容敏感）
	_, err = ad.ParseSubmit(fixtureResp(200, `{"base_resp":{"status_code":1026,"status_msg":"content sensitive"}}`))
	var rej *UpstreamRejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Message, "sensitive") {
		t.Fatalf("base_resp fail = %v, want typed rejection", err)
	}
}

func TestHailuoParseQuery(t *testing.T) {
	ad := hailuoTask{}
	// Success → succeeded + file_id 进 ResultRef（无 URL，走二跳）
	r, err := ad.ParseQuery(fixtureResp(200, `{"status":"Success","file_id":"file-99","base_resp":{"status_code":0}}`))
	if err != nil || r.Status != StatusSucceeded {
		t.Fatalf("success = %+v err=%v", r, err)
	}
	if r.ResultRef != "file-99" || r.ResultURL != "" {
		t.Fatalf("want ResultRef=file-99 ResultURL empty, got ref=%q url=%q", r.ResultRef, r.ResultURL)
	}
	// 方言：Fail（非 Failed）、Queueing（双 e）
	for st, want := range map[string]string{
		"Preparing": StatusRunning, "Queueing": StatusRunning, "Processing": StatusRunning,
		"Fail": StatusFailed, "WeirdUnknown": StatusRunning,
	} {
		r, _ := ad.ParseQuery(fixtureResp(200, `{"status":"`+st+`","base_resp":{"status_code":0}}`))
		if r.Status != want {
			t.Fatalf("status %q → %q, want %q", st, r.Status, want)
		}
	}
	// 查询期 base_resp 非零 = 传输层错误 → 返回 error（本轮重试，绝不终态失败仍在生成的任务）
	if _, err := ad.ParseQuery(fixtureResp(200, `{"status":"","base_resp":{"status_code":1002,"status_msg":"rate limit"}}`)); err == nil {
		t.Fatal("poll-time base_resp!=0 must return error (retry), not terminal-fail")
	}
}

func TestHailuoFetchResultURL(t *testing.T) {
	ad := hailuoTask{}
	up := mockJSONServer(t, func(path string) (int, string) {
		if strings.Contains(path, "/v1/files/retrieve") && strings.Contains(path, "file_id=file-99") {
			return 200, `{"file":{"download_url":"https://cdn.minimax/v.mp4"},"base_resp":{"status_code":0}}`
		}
		return 404, `{}`
	})
	defer up.Close()
	url, err := ad.FetchResultURL(t.Context(), up.Client(), chBase(up.URL), "k", "file-99")
	if err != nil || url != "https://cdn.minimax/v.mp4" {
		t.Fatalf("fetch = %q err=%v", url, err)
	}
	// 取件 base_resp 失败 → error（pollOne 会本轮重试，不误判任务失败）
	up2 := mockJSONServer(t, func(string) (int, string) {
		return 200, `{"base_resp":{"status_code":1008,"status_msg":"insufficient"}}`
	})
	defer up2.Close()
	if _, err := ad.FetchResultURL(t.Context(), up2.Client(), chBase(up2.URL), "k", "f"); err == nil {
		t.Fatal("retrieve base_resp fail must error")
	}
}

func TestHailuoEstimate(t *testing.T) {
	ad := hailuoTask{}
	// 720P 6s 基准
	base := ad.EstimateOutputTokens("MiniMax-Hailuo-02", nil, map[string]any{"resolution": "720P"})
	// 1080P 更贵
	if hi := ad.EstimateOutputTokens("m", nil, map[string]any{"resolution": "1080P"}); hi <= base {
		t.Fatalf("1080P est %d <= 720P %d", hi, base)
	}
	// duration=10 更贵
	if lo := ad.EstimateOutputTokens("m", nil, map[string]any{"resolution": "720P", "duration": float64(10)}); lo <= base {
		t.Fatalf("10s est %d <= 6s %d", lo, base)
	}
}

// —— wan/ali ——

func TestAliParseSubmit(t *testing.T) {
	ad := aliTask{}
	// 顶层 code 空串成功 → output.task_id
	id, err := ad.ParseSubmit(fixtureResp(200, `{"output":{"task_id":"a-1","task_status":"PENDING"},"request_id":"r1"}`))
	if err != nil || id != "a-1" {
		t.Fatalf("submit = %q err=%v", id, err)
	}
	// 顶层 code 非空 → 请求级错误
	_, err = ad.ParseSubmit(fixtureResp(200, `{"code":"InvalidParameter","message":"bad size"}`))
	var rej *UpstreamRejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Message, "bad size") {
		t.Fatalf("code err = %v, want typed rejection", err)
	}
}

func TestAliParseQuery(t *testing.T) {
	ad := aliTask{}
	// SUCCEEDED（非 SUCCESS）→ video_url 直链
	r, err := ad.ParseQuery(fixtureResp(200, `{"output":{"task_status":"SUCCEEDED","video_url":"https://oss/v.mp4"}}`))
	if err != nil || r.Status != StatusSucceeded || r.ResultURL != "https://oss/v.mp4" {
		t.Fatalf("succeeded = %+v err=%v", r, err)
	}
	// 方言：CANCELED 单 L、UNKNOWN 合法终态都归失败
	for st, want := range map[string]string{
		"PENDING": StatusQueued, "RUNNING": StatusRunning, "SUCCEEDED": StatusSucceeded,
		"FAILED": StatusFailed, "CANCELED": StatusFailed, "UNKNOWN": StatusFailed, "weird": StatusRunning,
	} {
		r, _ := ad.ParseQuery(fixtureResp(200, `{"output":{"task_status":"`+st+`"}}`))
		if r.Status != want {
			t.Fatalf("status %q → %q, want %q", st, r.Status, want)
		}
	}
	// 失败原因：顶层 message 优先于 output.message
	r, _ = ad.ParseQuery(fixtureResp(200, `{"message":"top err","output":{"task_status":"FAILED","message":"out err"}}`))
	if r.FailReason != "top err" {
		t.Fatalf("fail reason = %q, want top-level priority", r.FailReason)
	}
}

func TestAliSubmitAsyncHeaderAndNesting(t *testing.T) {
	ad := aliTask{}
	req, err := ad.SubmitRequest(t.Context(), chBase("https://dashscope.aliyuncs.com"), "k",
		SubmitInput{Model: "wan2.5-t2v-preview",
			Content: []any{map[string]any{"type": "text", "text": "a cat"}},
			Params:  map[string]any{"resolution": "1080P", "duration": float64(5)}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-DashScope-Async") != "enable" {
		t.Fatal("missing X-DashScope-Async: enable")
	}
	body := readTaskBody(t, req)
	// prompt 进 input 段，resolution/duration 进 parameters 段
	input, _ := body["input"].(map[string]any)
	if input["prompt"] != "a cat" {
		t.Fatalf("prompt not in input: %v", body)
	}
	params, _ := body["parameters"].(map[string]any)
	if params["resolution"] != "1080P" {
		t.Fatalf("resolution not in parameters: %v", body)
	}
}

// —— suno ——

func TestSunoAction(t *testing.T) {
	if sunoAction("suno_music") != "MUSIC" || sunoAction("suno_lyrics") != "LYRICS" {
		t.Fatal("action derivation")
	}
	if sunoAction("weird") != "MUSIC" {
		t.Fatal("non-suno model → MUSIC default")
	}
}

func TestSunoParseSubmit(t *testing.T) {
	ad := sunoTask{}
	// envelope code=="success"（小写），data=裸 task id
	id, err := ad.ParseSubmit(fixtureResp(200, `{"code":"success","message":"","data":"suno-task-1"}`))
	if err != nil || id != "suno-task-1" {
		t.Fatalf("submit = %q err=%v", id, err)
	}
	_, err = ad.ParseSubmit(fixtureResp(200, `{"code":"invalid_action","message":"bad action","data":""}`))
	var rej *UpstreamRejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("code!=success = %v, want rejection", err)
	}
}

func TestSunoParseQuery(t *testing.T) {
	ad := sunoTask{}
	// 批量 fetch 响应取第一条；SUCCESS → audio_url
	r, err := ad.ParseQuery(fixtureResp(200, `{"code":"success","data":[
		{"task_id":"t1","status":"SUCCESS","data":[{"audio_url":"https://cdn.suno/a.mp3"},{"audio_url":"b"}]}]}`))
	if err != nil || r.Status != StatusSucceeded || r.ResultURL != "https://cdn.suno/a.mp3" {
		t.Fatalf("success = %+v err=%v", r, err)
	}
	// 大小写双方言归一：大写 QUEUED / 小写 queueing / processing
	for st, want := range map[string]string{
		"SUBMITTED": StatusQueued, "queueing": StatusQueued, "IN_PROGRESS": StatusRunning,
		"processing": StatusRunning, "SUCCESS": StatusSucceeded, "failed": StatusFailed, "FAILURE": StatusFailed,
	} {
		r, _ := ad.ParseQuery(fixtureResp(200, `{"data":[{"status":"`+st+`"}]}`))
		if r.Status != want {
			t.Fatalf("suno status %q → %q, want %q", st, r.Status, want)
		}
	}
	// fail_reason 非空即失败（OR 条件，即使 status 未到失败态）
	r, _ = ad.ParseQuery(fixtureResp(200, `{"data":[{"status":"SUBMITTED","fail_reason":"policy"}]}`))
	if r.Status != StatusFailed || r.FailReason != "policy" {
		t.Fatalf("fail_reason OR = %+v, want failed", r)
	}
	// 空 data → 本轮无回执，running 重试
	r, _ = ad.ParseQuery(fixtureResp(200, `{"code":"success","data":[]}`))
	if r.Status != StatusRunning {
		t.Fatalf("empty data → %q, want running retry", r.Status)
	}
}

// 注册表：批次 3 三家已登记。
func TestBatch3Registered(t *testing.T) {
	checks := map[string]any{"hailuo": hailuoTask{}, "wan": aliTask{}, "ali": aliTask{}, "suno": sunoTask{}}
	for code, want := range checks {
		ad, err := adaptorFor(code)
		if err != nil {
			t.Fatalf("adaptorFor(%q) err=%v", code, err)
		}
		if _, ok := ad.(ResultFetcher); code == "hailuo" && !ok {
			t.Fatalf("hailuo must implement ResultFetcher")
		}
		_ = want
	}
}
