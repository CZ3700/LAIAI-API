package task

// ark 兼容聚合网关（MaaS 站）方言适配测试。fixture 按用户提供的真实网关 OpenAPI 文档
// （maas.stringx.top，Seedance 2.0，2026-07）的响应示例逐字构造——与 ark 官方的两点差异：
//   ① 创建 200 返回**完整任务对象**（含 id/status/usage 全量字段），非 ark 的裸 {"id"}；
//   ② 错误信封是**顶层平铺形** {"error":"ValidationError","errorCode":10001,"message":...}，
//      error 是字符串（错误类名）——按 ark 对象形解析会静默得空消息（本次修复的根因）。
// 提交/查询路径与 ark 同（base_url 填 https://<host>/api/v3 即可复用 volcengine 适配器）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/relay"
)

// 创建成功（HTTP 200）：完整任务对象（网关文档示例形态；ark 只回 {"id"}——两形都必须可解）
const maasSubmitOKFixture = `{"id":"task_019db400-0000-7000-8000-000000000001","model":"seedance-2.0","status":"queued","created_at":1712664960,"updated_at":1712664960,"service_tier":"default","execution_expires_after":172800,"generate_audio":false,"draft":false,"priority":0}`

// 创建失败（HTTP 400）：平铺错误信封——error 是字符串类名，解释在顶层 message
const maasSubmitBadReqFixture = `{"error":"ValidationError","errorCode":10001,"message":"request body is invalid","requestId":"req_01hxy8v7m9n0pqrstuvwxyza1","detail":{}}`

// 查询成功（HTTP 200）：content.video_url + last_frame_url/expiredAt 扩展 + usage
const maasQuerySucceededFixture = `{"id":"task_019db400-0000-7000-8000-000000000001","model":"seedance-2.0","status":"succeeded","content":{"video_url":"https://cdn.example.com/output.mp4","last_frame_url":"https://cdn.example.com/last-frame.png","expiredAt":"2026-06-04T10:02:00Z"},"usage":{"completion_tokens":88,"total_tokens":88},"created_at":1712664960,"updated_at":1712664990,"seed":42,"resolution":"720p","ratio":"16:9","duration":5,"framespersecond":24,"safety_identifier":"tenant-1:user-1","service_tier":"default","status_detail":null,"priority":0,"draft":false,"generate_audio":false,"execution_expires_after":172800}`

// 查询失败（HTTP 200 终态 failed）：error 为对象形（网关任务对象内的 error 与 ark 同形）
const maasQueryFailedFixture = `{"id":"task_019db400-0000-7000-8000-000000000001","model":"seedance-2.0","status":"failed","error":{"code":"upstream_failed","message":"seedance task failed upstream"},"created_at":1712664960,"updated_at":1712664990}`

func TestArkCompatGatewaySubmit(t *testing.T) {
	// 创建 200 完整对象 → id 正常提取（ParseSubmit 只读 id，全量字段无害）
	id, err := volcengineTask{}.ParseSubmit(fixtureResp(200, maasSubmitOKFixture))
	if err != nil || id != "task_019db400-0000-7000-8000-000000000001" {
		t.Fatalf("submit id = %q err=%v", id, err)
	}

	// 创建 400 平铺信封 → UpstreamRejectedError 且 message 非空（修复回归：此前按 ark
	// 对象形解析得空串，客户端只看到"上游拒绝了参数:"后面没内容）
	_, err = volcengineTask{}.ParseSubmit(fixtureResp(400, maasSubmitBadReqFixture))
	var rej *UpstreamRejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("400 应为 UpstreamRejectedError, got %v", err)
	}
	if !strings.Contains(rej.Message, "request body is invalid") || !strings.Contains(rej.Message, "ValidationError") {
		t.Fatalf("平铺信封的解释必须透出（类名+message），got %q", rej.Message)
	}
}

// ark 官方对象形信封零回归（真机 fixture 同款形态仍走老路径）。
func TestArkObjectEnvelopeRegression(t *testing.T) {
	_, err := volcengineTask{}.ParseSubmit(fixtureResp(400, arkErrBadDurationFixture))
	var rej *UpstreamRejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Message, "duration") {
		t.Fatalf("ark 对象形信封回归：%v", err)
	}
}

func TestArkCompatGatewayQuery(t *testing.T) {
	// 成功：video_url + usage.completion_tokens 提取
	q, err := volcengineTask{}.ParseQuery(fixtureResp(200, maasQuerySucceededFixture))
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != StatusSucceeded || q.ResultURL != "https://cdn.example.com/output.mp4" || q.CompletionTokens != 88 {
		t.Fatalf("succeeded query = %+v", q)
	}
	// 失败：error.message 进 FailReason
	q, err = volcengineTask{}.ParseQuery(fixtureResp(200, maasQueryFailedFixture))
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != StatusFailed || !strings.Contains(q.FailReason, "seedance task failed upstream") {
		t.Fatalf("failed query = %+v", q)
	}
}

// arkErrMsg 双信封单测：对象形/平铺形/坏体。
func TestArkErrMsgDualEnvelope(t *testing.T) {
	if m := arkErrMsg([]byte(`{"error":{"code":"InvalidParameter","message":"the duration is not valid"}}`)); m != "the duration is not valid" {
		t.Fatalf("对象形 = %q", m)
	}
	if m := arkErrMsg([]byte(maasSubmitBadReqFixture)); m != "ValidationError: request body is invalid" {
		t.Fatalf("平铺形 = %q", m)
	}
	if m := arkErrMsg([]byte(`not json`)); m != "" {
		t.Fatalf("坏体应空 = %q", m)
	}
	if m := arkErrMsg([]byte(`{"message":"only top-level message"}`)); m != "only top-level message" {
		t.Fatalf("仅顶层 message = %q", m)
	}
}

// 端到端 URL 构造：base_url 填 https://<host>/api/v3 时提交/查询打到网关文档声明的路径。
func TestArkCompatGatewayURLs(t *testing.T) {
	ch := &relay.Channel{Provider: "doubao", BaseURL: "https://maas.example.top/api/v3"}
	req, err := volcengineTask{}.SubmitRequest(context.Background(), ch, "sk-x", SubmitInput{
		Model: "seedance-2.0", Content: []any{map[string]any{"type": "text", "text": "a cat"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://maas.example.top/api/v3/contents/generations/tasks" {
		t.Fatalf("submit url = %s", req.URL.String())
	}
	if req.Header.Get("Authorization") != "Bearer sk-x" {
		t.Fatalf("auth = %q", req.Header.Get("Authorization"))
	}
	qreq, err := volcengineTask{}.QueryRequest(context.Background(), ch, "sk-x", "task_x")
	if err != nil {
		t.Fatal(err)
	}
	if qreq.URL.String() != "https://maas.example.top/api/v3/contents/generations/tasks/task_x" {
		t.Fatalf("query url = %s", qreq.URL.String())
	}
}
