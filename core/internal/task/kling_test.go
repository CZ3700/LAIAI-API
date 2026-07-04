package task

// Kling adaptor 测试。fixture 为 New API 参考实现推导（字段/状态按其生产解析结构体，非我方真机捕获——
// 真机验证走 TestRealKlingTaskSmoke，env 门控）。JWT 向量用固定时间自校验。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/relay"
)

// readJSONBody：读回请求体 JSON（测试断言上游提交体用）。
func readJSONBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(req.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, raw)
	}
	return m
}

func TestKlingJWTVector(t *testing.T) {
	// 固定时间自校验：解出的 header/claims/签名必须与 HS256(ak,sk) 一致。
	now := time.Unix(1_700_000_000, 0)
	tok, err := klingJWT("my-ak", "my-sk", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt must have 3 parts, got %d", len(parts))
	}
	// header
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(hdr) != `{"alg":"HS256","typ":"JWT"}` {
		t.Fatalf("header = %s", hdr)
	}
	// claims：iss + exp(now+30min) + nbf(now-5s)
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "my-ak" {
		t.Fatalf("iss = %v", claims["iss"])
	}
	if int64(claims["exp"].(float64)) != now.Add(30*time.Minute).Unix() {
		t.Fatalf("exp = %v", claims["exp"])
	}
	if int64(claims["nbf"].(float64)) != now.Add(-5*time.Second).Unix() {
		t.Fatalf("nbf = %v", claims["nbf"])
	}
	// 签名自校验
	mac := hmac.New(sha256.New, []byte("my-sk"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Fatal("signature mismatch")
	}
}

func TestKlingCredentialSplit(t *testing.T) {
	ak, sk, err := klingCredential("access-123 | secret-456")
	if err != nil || ak != "access-123" || sk != "secret-456" {
		t.Fatalf("split = %q,%q err=%v", ak, sk, err)
	}
	// 错误信息绝不回显凭证（红线 7）
	for _, bad := range []string{"onlyone", "a|b|c", "|", "a|", "|b"} {
		_, _, err := klingCredential(bad)
		if err == nil {
			t.Fatalf("credential %q must fail", bad)
		}
		if strings.Contains(err.Error(), bad) && bad != "" {
			t.Fatalf("error leaks credential content: %v", err)
		}
	}
}

func TestKlingActionEncoding(t *testing.T) {
	if got := klingEncodeTaskID("text2video", "cgt-1"); got != "text2video:cgt-1" {
		t.Fatalf("encode = %s", got)
	}
	a, id := klingDecodeTaskID("image2video:cgt-2")
	if a != "image2video" || id != "cgt-2" {
		t.Fatalf("decode = %s,%s", a, id)
	}
	// 裸 id（历史/异常）→ 默认 image2video
	a, id = klingDecodeTaskID("cgt-bare")
	if a != klingActionImage2Video || id != "cgt-bare" {
		t.Fatalf("bare decode = %s,%s", a, id)
	}
	if klingActionFromPath("/v1/videos/text2video") != "text2video" {
		t.Fatal("path→action text2video")
	}
	if klingActionFromPath("/v1/videos/image2video") != "image2video" {
		t.Fatal("path→action image2video")
	}
}

// ParseSubmit：code==0 成功 + action 从 resp.Request.URL 编码进 task id；code!=0 类型化拒绝。
func TestKlingParseSubmit(t *testing.T) {
	ad := klingTask{}
	// 成功（模拟经 text2video 提交）
	resp := fixtureResp(200, `{"code":0,"message":"SUCCEED","data":{"task_id":"cgt-kling-1"}}`)
	resp.Request = &http.Request{URL: mustURL(t, "https://api.klingai.com/v1/videos/text2video")}
	id, err := ad.ParseSubmit(resp)
	if err != nil || id != "text2video:cgt-kling-1" {
		t.Fatalf("submit ok = %q err=%v", id, err)
	}
	// 业务失败 code!=0 → UpstreamRejectedError 带 message
	_, err = ad.ParseSubmit(fixtureResp(200, `{"code":1200,"message":"invalid model_name","data":{}}`))
	var rej *UpstreamRejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Message, "invalid model_name") {
		t.Fatalf("code!=0 = %v, want typed rejection", err)
	}
}

// ParseQuery：succeed→final_unit_deduction 结算 + videos[0].url；各状态归一；未知兜底 running。
func TestKlingParseQuery(t *testing.T) {
	ad := klingTask{}
	// 成功：final_unit_deduction "3.5" → ceil(3.5)=4 → 4M 伪 token
	r, err := ad.ParseQuery(fixtureResp(200, `{"code":0,"data":{"task_status":"succeed",
		"task_result":{"videos":[{"url":"https://cdn.kling/v.mp4","duration":"5"}]},
		"final_unit_deduction":"3.5"}}`))
	if err != nil || r.Status != StatusSucceeded {
		t.Fatalf("succeed = %+v err=%v", r, err)
	}
	if r.ResultURL != "https://cdn.kling/v.mp4" {
		t.Fatalf("result url = %q", r.ResultURL)
	}
	if r.CompletionTokens != 4_000_000 {
		t.Fatalf("tokens = %d, want 4000000 (ceil(3.5)×1M)", r.CompletionTokens)
	}
	// 状态归一（方言：succeed 无 ed）
	for st, want := range map[string]string{
		"submitted": StatusQueued, "processing": StatusRunning,
		"failed": StatusFailed, "weird-unknown": StatusRunning,
	} {
		r, _ := ad.ParseQuery(fixtureResp(200, `{"code":0,"data":{"task_status":"`+st+`","task_status_msg":"m"}}`))
		if r.Status != want {
			t.Fatalf("status %q → %q, want %q", st, r.Status, want)
		}
	}
	// final_unit_deduction 解析失败 → CompletionTokens=0（走估算，不报错）
	r, _ = ad.ParseQuery(fixtureResp(200, `{"code":0,"data":{"task_status":"succeed","final_unit_deduction":"bad"}}`))
	if r.CompletionTokens != 0 {
		t.Fatalf("bad deduction tokens = %d, want 0", r.CompletionTokens)
	}
}

// EstimateOutputTokens：mode×duration 上界表（基准 std/5s=4 单位，对抗 review 放宽）；未知/畸形取最高档。
func TestKlingEstimate(t *testing.T) {
	ad := klingTask{}
	cases := map[string]struct {
		params map[string]any
		want   int64
	}{
		"std 5s":        {map[string]any{"mode": "std", "duration": "5"}, 4_000_000},
		"pro 5s":        {map[string]any{"mode": "pro", "duration": "5"}, 8_000_000},
		"std 10s":       {map[string]any{"mode": "std", "duration": "10"}, 8_000_000},
		"pro 10s":       {map[string]any{"mode": "pro", "duration": "10"}, 16_000_000},
		"default":       {nil, 4_000_000},
		"unknown mode":  {map[string]any{"mode": "ultra"}, 8_000_000},
		"malformed dur": {map[string]any{"duration": "abc"}, 16_000_000},
	}
	for name, c := range cases {
		if got := ad.EstimateOutputTokens("kling-v1", nil, c.params); got != c.want {
			t.Fatalf("%s: est = %d, want %d", name, got, c.want)
		}
	}
}

// SubmitRequest：prompt/image 从 OpenAI content 数组提取进上游体（对抗 review CONFIRMED 回归）。
func TestKlingSubmitCarriesContent(t *testing.T) {
	ad := klingTask{}
	// 纯文本 → text2video + prompt
	req, err := ad.SubmitRequest(context.Background(),
		&relay.Channel{BaseURL: "https://api.klingai.com"}, "ak|sk",
		SubmitInput{Model: "kling-v1", Content: []any{
			map[string]any{"type": "text", "text": "a red apple"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(req.URL.Path, "/text2video") {
		t.Fatalf("no-image → %s, want text2video", req.URL.Path)
	}
	body := readJSONBody(t, req)
	if body["prompt"] != "a red apple" {
		t.Fatalf("prompt not carried from content: %v", body["prompt"])
	}
	// 带图 → image2video + image
	req, _ = ad.SubmitRequest(context.Background(),
		&relay.Channel{BaseURL: "https://api.klingai.com"}, "ak|sk",
		SubmitInput{Model: "kling-v1", Content: []any{
			map[string]any{"type": "text", "text": "animate"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://img/x.png"}},
		}})
	if !strings.HasSuffix(req.URL.Path, "/image2video") {
		t.Fatalf("with-image → %s, want image2video", req.URL.Path)
	}
	body = readJSONBody(t, req)
	if body["image"] != "https://img/x.png" || body["prompt"] != "animate" {
		t.Fatalf("image/prompt not carried: %v", body)
	}
}

// —— 测试辅助 ——

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
