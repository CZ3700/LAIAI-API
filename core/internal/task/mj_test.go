package task

// Midjourney adaptor 单测：提交请求形态/提交响应码/查询状态映射。
// fixture 按 midjourney-proxy 方言 + NewAPI 生产解析移植（真机验证待 mjproxy 上游）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/relay"
)

func TestMJSubmitRequest(t *testing.T) {
	ch := &relay.Channel{Provider: "midjourney", BaseURL: "https://mjgw.example"}
	in := SubmitInput{Model: "mj_imagine", Params: map[string]any{
		"prompt": "a cat --v 6", "base64Array": []any{},
	}}
	req, err := midjourneyTask{}.SubmitRequest(context.Background(), ch, "mj-secret", in)
	if err != nil {
		t.Fatalf("submit request: %v", err)
	}
	if req.URL.String() != "https://mjgw.example/mj/submit/imagine" {
		t.Fatalf("url = %s", req.URL.String())
	}
	if req.Header.Get("mj-api-secret") != "mj-secret" {
		t.Fatalf("auth header = %v", req.Header)
	}
	body, _ := io.ReadAll(req.Body)
	var b map[string]any
	_ = json.Unmarshal(body, &b)
	if b["prompt"] != "a cat --v 6" {
		t.Fatalf("params 透传缺失: %s", body)
	}
	// 未知 action 模型拒绝
	if _, err := (midjourneyTask{}).SubmitRequest(context.Background(), ch, "s", SubmitInput{Model: "mj_hack"}); err == nil {
		t.Fatal("unknown action must error")
	}
}

func TestMJParseSubmit(t *testing.T) {
	mk := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
	}
	// code 1/21/22 都算提交成功（有任务在跑）
	for _, code := range []string{"1", "21", "22"} {
		id, err := (midjourneyTask{}).ParseSubmit(mk(200, `{"code":`+code+`,"description":"ok","result":"1720000001"}`))
		if err != nil || id != "1720000001" {
			t.Fatalf("code=%s: id=%q err=%v", code, id, err)
		}
	}
	// 24 敏感词 / 23 队列满：无任务，报错带描述
	if _, err := (midjourneyTask{}).ParseSubmit(mk(200, `{"code":24,"description":"May contain sensitive words"}`)); err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("code 24 must error with description: %v", err)
	}
	// 非 200
	if _, err := (midjourneyTask{}).ParseSubmit(mk(502, `bad gateway`)); err == nil {
		t.Fatal("non-200 must error")
	}
	// result 为数字形态也接受（mjproxy 有实现回 number）
	id, err := (midjourneyTask{}).ParseSubmit(mk(200, `{"code":1,"result":1720000002}`))
	if err != nil || id != "1720000002" {
		t.Fatalf("numeric result: id=%q err=%v", id, err)
	}
}

func TestMJParseQuery(t *testing.T) {
	mk := func(body string) *http.Response {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}
	}
	q, err := (midjourneyTask{}).ParseQuery(mk(`{"id":"x","status":"SUCCESS","progress":"100%","imageUrl":"https://cdn.discordapp.com/a.png"}`))
	if err != nil || q.Status != StatusSucceeded || q.ResultURL != "https://cdn.discordapp.com/a.png" {
		t.Fatalf("SUCCESS: %+v err=%v", q, err)
	}
	q, _ = (midjourneyTask{}).ParseQuery(mk(`{"status":"FAILURE","failReason":"banned prompt"}`))
	if q.Status != StatusFailed || !strings.Contains(q.FailReason, "banned") {
		t.Fatalf("FAILURE: %+v", q)
	}
	q, _ = (midjourneyTask{}).ParseQuery(mk(`{"status":"CANCEL"}`))
	if q.Status != StatusFailed {
		t.Fatalf("CANCEL should map failed: %+v", q)
	}
	for _, st := range []string{"NOT_START", "SUBMITTED", "IN_PROGRESS", "MODAL", ""} {
		q, _ = (midjourneyTask{}).ParseQuery(mk(`{"status":"` + st + `"}`))
		if q.Status != StatusRunning {
			t.Fatalf("%q should map running: %+v", st, q)
		}
	}
	// describe 类：SUCCESS 无 imageUrl 也算成功（产出是文本，fetch 透传体携带）
	q, err = (midjourneyTask{}).ParseQuery(mk(`{"status":"SUCCESS","properties":{"finalPrompt":"a described prompt"}}`))
	if err != nil || q.Status != StatusSucceeded {
		t.Fatalf("SUCCESS without image: %+v err=%v", q, err)
	}
}
