package task

// MiniMax Hailuo 视频生成任务 adaptor。
// 知识移植自 New API 参考实现 relay/channel/task/hailuo/adaptor.go（字段/状态/双层错误协议/二跳取件
// 按其生产解析结构体移植——非我方真机捕获，真机验证走 env 门控 smoke）。
// 鉴权：trivial bearer（提交/查询/取件同一 key）。
// 计费体制：B 类——查询响应无 usage/duration 回执，无法事后按实结算；EstimateOutputTokens 是
//          最终收费（duration/resolution 感知，绝不上界多收，畸形/未知参数按档默认而非放大）。
// 产物二跳（接口缺口②）：查询 succeeded 只给 file_id，须再 GET /v1/files/retrieve 换 download_url。

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

type hailuoTask struct{}

// 编译期断言：hailuo 实现 ResultFetcher（file_id→download_url 二跳）。
var _ ResultFetcher = hailuoTask{}

// hailuoResolutionUnits：分辨率档 → 计费单位系数（480/512P 基准 1）。B 类：估算即价，不上浮。
var hailuoResolutionUnits = map[string]float64{
	"512p": 1, "768p": 1.5, "720p": 1.5, "1080p": 2.5,
}

// EstimateOutputTokens：duration(6/10) × resolution 档感知（B 类=最终收费）。
// New API 对 hailuo 是平价（不分档），此处按 MiniMax 真实价随档浮动补上系数；
// 1,000,000 伪 token = 1 计费单位；未知参数按模型默认档（不放大，B 类不上界估）。
func (hailuoTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	units := 1.0
	if r, ok := params["resolution"].(string); ok {
		if u, known := hailuoResolutionUnits[strings.ToLower(r)]; known {
			units = u
		}
	}
	// duration=10 约为 6s 的 ~1.6×（MiniMax 计价近似）
	if d, ok := params["duration"].(float64); ok && d >= 10 {
		units *= 1.6
	}
	return int64(units * 1_000_000)
}

func (hailuoTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	prompt, images := taskContentParts(in.Content)
	payload := map[string]any{}
	if prompt != "" {
		payload["prompt"] = prompt
	}
	if len(images) > 0 {
		payload["first_frame_image"] = images[0] // 图生视频首帧；尾帧/主体参考走 Params 显式传
	}
	for k, v := range in.Params {
		payload[k] = v
	}
	payload["model"] = in.Model
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(ch.BaseURL, "/") + "/v1/video_generation"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

// hailuoBaseResp：双层错误协议——HTTP 200 也可能业务失败，唯一真值是 base_resp.status_code==0。
type hailuoBaseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

func (hailuoTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("hailuo: submit status %d", resp.StatusCode)
	}
	var parsed struct {
		TaskID   string         `json:"task_id"`
		BaseResp hailuoBaseResp `json:"base_resp"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("hailuo: submit parse: %w", err)
	}
	// base_resp.status_code 才是真值（HTTP 200 也可能失败）；参数/敏感类按客户端问题转 400。
	if parsed.BaseResp.StatusCode != 0 {
		return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest, Message: parsed.BaseResp.StatusMsg}
	}
	if parsed.TaskID == "" {
		return "", fmt.Errorf("hailuo: submit response missing task id")
	}
	return parsed.TaskID, nil
}

func (hailuoTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	// task_id 走 query string（不是 JSON body），GET
	url := strings.TrimRight(ch.BaseURL, "/") + "/v1/query/video_generation?task_id=" + upstreamTaskID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

func (hailuoTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("hailuo: query status %d", resp.StatusCode)
	}
	var parsed struct {
		Status   string         `json:"status"`
		FileID   string         `json:"file_id"`
		BaseResp hailuoBaseResp `json:"base_resp"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("hailuo: query parse: %w", err)
	}
	// 查询期 base_resp 非零是 API 传输层错误（限流 1002/内部 1013/鉴权等），**非任务失败**
	// （对抗 review CONFIRMED：此前一律判终态失败，会把仍在生成的任务误杀退款）——返回 error 让
	// pollOne 本轮重试，绝不据此终态失败。任务失败只经 status=="Fail"；真·渠道故障由 sweepTimedOut
	// 30min 兜底超时退款。提交期 base_resp 仍按拒绝处理（ParseSubmit，那是同步准入不是在途任务）。
	if parsed.BaseResp.StatusCode != 0 {
		return QueryResult{}, fmt.Errorf("hailuo: query base_resp %d", parsed.BaseResp.StatusCode)
	}
	out := QueryResult{}
	// 方言：Fail（非 Failed）、Queueing（双 e）；CamelCase。
	switch parsed.Status {
	case "Preparing", "Queueing", "Processing":
		out.Status = StatusRunning
	case "Success":
		out.Status = StatusSucceeded
		out.ResultRef = parsed.FileID // 只给 file_id，产物 URL 走二跳 FetchResultURL
	case "Fail":
		out.Status = StatusFailed
		out.FailReason = "task failed"
	default:
		out.Status = StatusRunning // 未知状态兜底，绝不误杀
	}
	return out, nil
}

// FetchResultURL：二跳取件——GET /v1/files/retrieve?file_id= 换 download_url（接口缺口②）。
// 用框架传入的 client（带超时），绝不用 http.DefaultClient（无超时=挂起风险）。
func (hailuoTask) FetchResultURL(ctx context.Context, client *http.Client, ch *relay.Channel, credential, fileID string) (string, error) {
	url := strings.TrimRight(ch.BaseURL, "/") + "/v1/files/retrieve?file_id=" + fileID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("hailuo: retrieve status %d", resp.StatusCode)
	}
	var parsed struct {
		File struct {
			DownloadURL string `json:"download_url"`
		} `json:"file"`
		BaseResp hailuoBaseResp `json:"base_resp"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("hailuo: retrieve parse: %w", err)
	}
	if parsed.BaseResp.StatusCode != 0 {
		return "", fmt.Errorf("hailuo: retrieve base_resp %d", parsed.BaseResp.StatusCode)
	}
	return parsed.File.DownloadURL, nil
}
