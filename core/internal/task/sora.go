package task

// OpenAI Sora 视频生成任务 adaptor（行业标准 /v1/videos 形态）。
// 知识移植自 New API 参考实现 relay/channel/task/sora/adaptor.go（字段/状态方言/双 id/content 端点
// 按其生产解析结构体移植——非我方真机捕获，真机验证走 env 门控 smoke）。
// 鉴权：trivial bearer（提交/查询/取产物同一 key）。
// 计费体制：A 类——查询响应回显 seconds(字符串)+size，自包含结算 seconds×sizeRatio；
//          估算同公式（回显==请求参数时 settle==hold，异常回显由框架 cap-at-hold 兜底）。
// 伪 token 约定：1M 伪 token = 1 秒 × 720 档；output_usd_per_1m = 720 档每秒美元价。
// 产物：查询响应【无】产物 URL——completed 后须 GET /v1/videos/{id}/content 拿视频二进制，
//      经平台代理端点下发（ContentStreamer；ResultRef=上游 video id）。
// 本批支持 JSON 提交（文生 + input_reference URL 图生）；multipart 文件上传与 remix 留后续。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/laiai/laiai-next/core/internal/relay"
)

type soraTask struct{}

// 编译期断言：sora 实现产物流代理（不走二跳 URL——产物是二进制流）。
var _ ContentStreamer = soraTask{}

// soraSizeRatio：size → 计费倍率（720 档基准 1；1792 档仅 sora-2-pro）。
func soraSizeRatio(size string) float64 {
	switch size {
	case "", "720x1280", "1280x720":
		return 1.0
	case "1792x1024", "1024x1792":
		return 1.666667
	}
	return 1.666667 // 未知 size：按高档（A 类上界安全；上游会校验合法性）
}

// soraSeconds：seconds 双字段方言——字符串 "4"/"8"/"12"（sora 原生）或数字 duration。
func soraSeconds(params map[string]any) (float64, bool) {
	if v, exists := params["seconds"]; exists {
		switch s := v.(type) {
		case string:
			if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
				return f, true
			}
			return 0, false // 畸形字符串
		case float64:
			if s > 0 {
				return s, true
			}
		}
		return 0, false
	}
	if d, ok := params["duration"].(float64); ok && d > 0 {
		return d, true
	}
	return 4, true // 上游默认 4s
}

func (soraTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	sec, ok := soraSeconds(params)
	if !ok {
		return 30_000_000 // 畸形参数：上界（12s×1.67 留满，A 类差额自动解冻）
	}
	size, _ := params["size"].(string)
	est := sec * soraSizeRatio(size) * 1_000_000
	if est > 30_000_000 {
		est = 30_000_000
	}
	return int64(est)
}

func (soraTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	prompt, images := taskContentParts(in.Content)
	payload := map[string]any{}
	if prompt != "" {
		payload["prompt"] = prompt
	}
	if len(images) > 0 {
		payload["input_reference"] = images[0] // sora 特有字段名（≈其他厂商 images[0]）
	}
	for k, v := range in.Params {
		payload[k] = v
	}
	payload["model"] = in.Model // 平台权威覆盖
	if _, ok := payload["seconds"]; !ok {
		if d, ok2 := payload["duration"]; ok2 { // duration(数字) → seconds(字符串) 方言换算
			if f, ok3 := d.(float64); ok3 {
				payload["seconds"] = strconv.Itoa(int(f))
				delete(payload, "duration")
			}
		} else {
			payload["seconds"] = "4"
		}
	}
	if _, ok := payload["size"]; !ok {
		payload["size"] = "720x1280"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(ch.BaseURL, "/") + "/videos" // base 已含 /v1（openai 档案惯例）
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

// soraVideoObj：提交/查询同构响应（OpenAI video 对象）。id 主字段、task_id 兜底（转售网关方言）。
type soraVideoObj struct {
	ID      string `json:"id"`
	AltID   string `json:"task_id"`
	Status  string `json:"status"`
	Seconds string `json:"seconds"` // 字符串型数字（方言）
	Size    string `json:"size"`
	Error   *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (v *soraVideoObj) taskID() string {
	if v.ID != "" {
		return v.ID
	}
	return v.AltID
}

func (soraTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusBadRequest {
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &e)
			return "", &UpstreamRejectedError{StatusCode: resp.StatusCode, Message: e.Error.Message}
		}
		return "", fmt.Errorf("sora: submit status %d", resp.StatusCode)
	}
	var parsed soraVideoObj
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("sora: submit parse: %w", err)
	}
	if parsed.taskID() == "" {
		return "", fmt.Errorf("sora: submit response missing video id")
	}
	return parsed.taskID(), nil
}

func (soraTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	url := strings.TrimRight(ch.BaseURL, "/") + "/videos/" + upstreamTaskID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

func (soraTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	// 429 限流是查询期瞬时态（对标 New API 特判）：非终态，error 让 pollOne 下轮重试
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("sora: query status %d", resp.StatusCode)
	}
	var parsed soraVideoObj
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("sora: query parse: %w", err)
	}
	out := QueryResult{}
	// 方言：官方 queued/in_progress/completed/failed 之外须兼容 pending/processing/cancelled（转售网关）
	switch parsed.Status {
	case "queued", "pending":
		out.Status = StatusQueued
	case "processing", "in_progress":
		out.Status = StatusRunning
	case "completed":
		out.Status = StatusSucceeded
		out.ResultRef = parsed.taskID() // 产物无 URL——经平台代理端点取 /content 二进制流
		// A 类结算：回显 seconds×sizeRatio（正常==请求参数→settle==hold；异常回显 cap-at-hold 兜底）
		if sec, e := strconv.ParseFloat(parsed.Seconds, 64); e == nil && sec > 0 {
			out.CompletionTokens = int64(sec * soraSizeRatio(parsed.Size) * 1_000_000)
		}
	case "failed", "cancelled":
		out.Status = StatusFailed
		if parsed.Error != nil {
			out.FailReason = parsed.Error.Message
		}
		if out.FailReason == "" {
			out.FailReason = "task failed"
		}
	default:
		out.Status = StatusRunning // 未知状态兜底，绝不误杀
	}
	return out, nil
}

// StreamResult：产物二进制流代理——GET /v1/videos/{id}/content（Bearer），Content-Type 透传。
func (soraTask) StreamResult(ctx context.Context, client *http.Client, ch *relay.Channel, credential, videoID string) (io.ReadCloser, string, error) {
	url := strings.TrimRight(ch.BaseURL, "/") + "/videos/" + videoID + "/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("sora: bad content url")
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("sora: content fetch failed") // 不带 err（一致纪律；凭证在 Bearer 头）
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("sora: content status %d", resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}
