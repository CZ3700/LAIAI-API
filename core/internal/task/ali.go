package task

// 阿里 DashScope 通义万相（wan/wanx）视频生成任务 adaptor。
// 知识移植自 New API 参考实现 relay/channel/task/ali/adaptor.go（字段/状态/双错误通道/计费系数表
// 按其生产解析结构体移植——非我方真机捕获，真机验证走 env 门控 smoke）。
// 鉴权：trivial bearer + 提交必带 X-DashScope-Async: enable（异步开关）。
// 计费体制：B 类——查询响应 usage.duration 不含分辨率因子，无法自包含结算；EstimateOutputTokens
//          是最终收费（duration × resolution 系数，移植自 New API ali EstimateBilling 的 ratio 表）。
// 产物：查询响应 output.video_url 直链（阿里 OSS，无需二跳/代理）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/laiai/laiai-next/core/internal/relay"
)

type aliTask struct{}

// aliResolutionUnits：分辨率档 → 计费单位（480P 基准 1；移植 New API ali 的 resolution-<RES> 因子近似）。
var aliResolutionUnits = map[string]float64{"480p": 1, "720p": 2, "1080p": 5}

// EstimateOutputTokens：duration(秒) × resolution 档（B 类=最终收费）。
// 1,000,000 伪 token = 1 计费单位；未知分辨率按最低档（B 类不上界估，避免多收）。
func (aliTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	seconds := 5.0
	if d, ok := params["duration"].(float64); ok && d > 0 {
		seconds = d
	}
	resUnits := 1.0
	if r, ok := params["resolution"].(string); ok {
		if u, known := aliResolutionUnits[strings.ToLower(r)]; known {
			resUnits = u
		}
	}
	units := seconds / 5.0 * resUnits
	if units < 1 {
		units = 1
	}
	return int64(units * 1_000_000)
}

func (aliTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	prompt, images := taskContentParts(in.Content)
	// 阿里请求体是嵌套 {model, input:{prompt, img_url?}, parameters:{...}}。
	input := map[string]any{}
	if prompt != "" {
		input["prompt"] = prompt
	}
	if len(images) > 0 {
		input["img_url"] = images[0] // 图生视频首帧；首尾帧走 Params 显式传
	}
	parameters := map[string]any{}
	for k, v := range in.Params {
		switch k {
		case "prompt", "img_url", "first_frame_url", "last_frame_url", "negative_prompt", "audio_url", "template":
			input[k] = v // input 段字段
		default:
			parameters[k] = v // 其余（size/resolution/duration/watermark/seed…）进 parameters
		}
	}
	payload := map[string]any{"model": in.Model, "input": input}
	if len(parameters) > 0 {
		payload["parameters"] = parameters
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(ch.BaseURL, "/") + "/api/v1/services/aigc/video-generation/video-synthesis"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("X-DashScope-Async", "enable") // 异步硬开关（不设则非异步语义）
	return req, nil
}

// aliResp：提交/查询共用外壳。顶层 code 非空=请求级错误；output.task_status=任务态。
type aliResp struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Output  struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		VideoURL   string `json:"video_url"`
		Code       string `json:"code"`    // 任务级错误码
		Message    string `json:"message"` // 任务级错误消息
	} `json:"output"`
}

func (aliTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusBadRequest {
			return "", &UpstreamRejectedError{StatusCode: resp.StatusCode, Message: clipReason(string(raw), 300)}
		}
		return "", fmt.Errorf("ali: submit status %d", resp.StatusCode)
	}
	var parsed aliResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("ali: submit parse: %w", err)
	}
	// 成功判别：顶层 code 为空串（非空即请求级错误）。仅参数/内容类判客户端短路 failover；
	// Throttling/InvalidApiKey/Quota 等渠道侧故障走换渠道（对抗 review 修）。
	if parsed.Code != "" {
		if submitErrorIsClientFault(parsed.Code + " " + parsed.Message) {
			return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest, Message: parsed.Message}
		}
		return "", fmt.Errorf("ali: submit failed upstream (code=%s)", parsed.Code)
	}
	if parsed.Output.TaskID == "" {
		return "", fmt.Errorf("ali: submit response missing task id")
	}
	return parsed.Output.TaskID, nil
}

func (aliTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	url := strings.TrimRight(ch.BaseURL, "/") + "/api/v1/tasks/" + upstreamTaskID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential) // 查询无需 Async 头
	return req, nil
}

func (aliTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("ali: query status %d", resp.StatusCode)
	}
	var parsed aliResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("ali: query parse: %w", err)
	}
	out := QueryResult{}
	// 方言：成功态 SUCCEEDED（非 SUCCESS）；CANCELED 单 L；UNKNOWN 是合法上游终态（过期/查无）按失败。
	switch parsed.Output.TaskStatus {
	case "PENDING":
		out.Status = StatusQueued
	case "RUNNING":
		out.Status = StatusRunning
	case "SUCCEEDED":
		out.Status = StatusSucceeded
		out.ResultURL = parsed.Output.VideoURL // OSS 直链，无需二跳
	case "FAILED", "CANCELED", "UNKNOWN":
		out.Status = StatusFailed
		// 失败原因：顶层 message 优先于 output.message
		if parsed.Message != "" {
			out.FailReason = parsed.Message
		} else {
			out.FailReason = parsed.Output.Message
		}
	default:
		out.Status = StatusRunning // 未识别状态兜底（与 New API 防御性归入进行中同哲学）
	}
	return out, nil
}
