package task

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

// Adaptor：任务型厂商适配器（对应知识库 §6.2 的 TaskAdaptor 契约精简版）。
// 与 chat adapter 分离：任务是"提交/查询"双请求模式，无流式。
type Adaptor interface {
	// EstimateOutputTokens 给 hold 预冻结的输出 token 上界估算（计费快照配套）。
	EstimateOutputTokens(model string, content []any) int64
	SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error)
	ParseSubmit(resp *http.Response) (upstreamTaskID string, err error)
	QueryRequest(ctx context.Context, ch *relay.Channel, credential string, upstreamTaskID string) (*http.Request, error)
	ParseQuery(resp *http.Response) (QueryResult, error)
}

type QueryResult struct {
	Status           string // 归一化到本包 Status* 常量
	ResultURL        string
	CompletionTokens int64
	FailReason       string
}

func adaptorFor(provider string) (Adaptor, error) {
	switch provider {
	case "volcengine", "modelark":
		return volcengineTask{}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrNoTaskAdaptor, provider)
	}
}

// volcengineTask：BytePlus ModelArk 内容生成任务 API（Seedance 视频生成走此协议）。
//
//	提交: POST {base}/contents/generations/tasks  {"model":..., "content":[...]}
//	查询: GET  {base}/contents/generations/tasks/{id}
//	响应: {"id":..., "status":"queued|running|succeeded|failed|cancelled",
//	       "content":{"video_url":...}, "usage":{"completion_tokens":N}, "error":{...}}
//
// ⚠️ 形态依据 ModelArk contents-generations API 文档与 NewAPI volcengine 实现；
// M2 服务器部署时必须用真实 API 抽样验证一次（fixture 纪律的"真实 raw response"环节）。
// Seedance 按 video token 计费（usage.completion_tokens），与文本同口径，
// 故复用 model_prices 的 output_usd_per_1m 定价。
type volcengineTask struct{}

// EstimateOutputTokens：保守上界。Seedance 视频 token 与 时长×分辨率 相关，
// 提交侧（薄透传）不解析参数，取固定上界 500k（≈ 高档位单任务）；
// 真实成本由上游 usage 在结算时校正——hold 是预授权，差额自动解冻/overage 补差。
func (volcengineTask) EstimateOutputTokens(model string, content []any) int64 {
	return 500_000
}

func (volcengineTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	body, err := json.Marshal(map[string]any{
		"model":   in.Model,
		"content": in.Content, // 薄透传：客户端直接说 ModelArk content 方言，不做二次参数发明
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(ch.BaseURL, "/")+"/contents/generations/tasks", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

func (volcengineTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("task: upstream submit status %d", resp.StatusCode)
	}
	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.ID == "" {
		return "", fmt.Errorf("task: upstream submit response missing task id")
	}
	return parsed.ID, nil
}

func (volcengineTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(ch.BaseURL, "/")+"/contents/generations/tasks/"+upstreamTaskID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

func (volcengineTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("task: upstream query status %d", resp.StatusCode)
	}
	var parsed struct {
		Status  string `json:"status"`
		Content struct {
			VideoURL string `json:"video_url"`
		} `json:"content"`
		Usage struct {
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("task: upstream query parse: %w", err)
	}
	out := QueryResult{
		ResultURL:        parsed.Content.VideoURL,
		CompletionTokens: parsed.Usage.CompletionTokens,
		FailReason:       parsed.Error.Message,
	}
	switch parsed.Status {
	case "queued":
		out.Status = StatusQueued
	case "running":
		out.Status = StatusRunning
	case "succeeded":
		out.Status = StatusSucceeded
	case "failed", "cancelled":
		out.Status = StatusFailed
	default:
		// 未知状态保守按 running 处理（下轮再看），不误杀任务
		out.Status = StatusRunning
	}
	return out, nil
}
