package task

// Suno 音乐/歌词生成任务 adaptor（经开源 Suno-API 网关方言，非 suno.com 官方）。
// 知识移植自 New API 参考实现 relay/channel/task/suno/adaptor.go（字段/状态双方言/批量查询
// 按其生产解析结构体移植——非我方真机捕获，真机验证走 env 门控 smoke）。
// 鉴权：trivial bearer。计费体制：B 类——纯按次固定价（模型名 suno_music/suno_lyrics 承载价）。
// 产物：查询响应 data 内 clip 数组的 audio_url（多产物取第一首为主产物）。
// ⚠️ 本批完整支持 suno_music（音频 URL 产物）；suno_lyrics 的产物是**文本歌词**不是可下载 URL，
// 当前 ParseQuery 只抽 audio_url → LYRICS 任务会走 pollOne 的"空 URL 重试"直到 30min 超时退款
// （资金安全：退款不多收）。歌词文本落地/结算需 result 模型支持文本产物，留后续。

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

type sunoTask struct{}

// EstimateOutputTokens：按次固定价（B 类）。1,000,000 伪 token = 1 次调用；模型名承载价差。
func (sunoTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	return 1_000_000
}

// sunoAction：从模型名 suno_music/suno_lyrics 推导上游 ACTION（大写 MUSIC/LYRICS 入 URL）。
func sunoAction(model string) string {
	a := strings.ToUpper(strings.TrimPrefix(strings.ToLower(model), "suno_"))
	if a != "LYRICS" {
		a = "MUSIC" // 默认音乐（含非法值兜底）
	}
	return a
}

func (s sunoTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	prompt, _ := taskContentParts(in.Content)
	action := sunoAction(in.Model)
	payload := map[string]any{}
	if prompt != "" {
		// MUSIC：content 文本作灵感描述；LYRICS：作自定义歌词。都落 prompt/gpt_description_prompt。
		if action == "LYRICS" {
			payload["prompt"] = prompt
		} else {
			payload["gpt_description_prompt"] = prompt
		}
	}
	for k, v := range in.Params { // Params 覆盖（title/tags/mv/make_instrumental/prompt 等）
		payload[k] = v
	}
	if action == "MUSIC" {
		if _, ok := payload["mv"]; !ok {
			payload["mv"] = "chirp-v3-0" // MUSIC mv 缺省
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(ch.BaseURL, "/") + "/suno/submit/" + action // ACTION 大写入 URL
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

// sunoEnvelope：提交/查询共用外壳 {code:string, message, data:T}。成功码是小写 "success"。
func (sunoTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("suno: submit status %d", resp.StatusCode)
	}
	var parsed struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"` // 提交 data = 裸 task id 字符串
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("suno: submit parse: %w", err)
	}
	if parsed.Code != "success" {
		return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest, Message: parsed.Message}
	}
	if parsed.Data == "" {
		return "", fmt.Errorf("suno: submit response missing task id")
	}
	return parsed.Data, nil
}

// QueryRequest：批量 fetch 退化为单元素 ids 数组（POST /suno/fetch {ids:[id]}）。
func (sunoTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	body, _ := json.Marshal(map[string]any{"ids": []string{upstreamTaskID}})
	url := strings.TrimRight(ch.BaseURL, "/") + "/suno/fetch"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

// sunoNormalizeStatus：大小写不敏感 + 双方言归一（大写 MJ 风格 vs 小写 suno 原生）。
func sunoNormalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "submitted", "queued", "queueing", "not_start":
		return StatusQueued
	case "in_progress", "processing", "streaming":
		return StatusRunning
	case "success", "complete":
		return StatusSucceeded
	case "failure", "failed", "error":
		return StatusFailed
	default:
		return StatusRunning // 未知/空态兜底，绝不误杀
	}
}

func (sunoTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("suno: query status %d", resp.StatusCode)
	}
	// 批量 fetch 响应：{code, data:[ {task_id, status, fail_reason, data:[clips]} ]}——取第一条。
	var parsed struct {
		Data []struct {
			Status     string `json:"status"`
			FailReason string `json:"fail_reason"`
			Data       []struct {
				AudioURL string `json:"audio_url"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("suno: query parse: %w", err)
	}
	if len(parsed.Data) == 0 {
		return QueryResult{Status: StatusRunning}, nil // 本轮无回执，下轮重试
	}
	item := parsed.Data[0]
	out := QueryResult{FailReason: item.FailReason}
	// 失败判定是 OR：fail_reason 非空即失败（即使 status 未到失败态）。
	if item.FailReason != "" {
		out.Status = StatusFailed
		return out, nil
	}
	out.Status = sunoNormalizeStatus(item.Status)
	if out.Status == StatusSucceeded && len(item.Data) > 0 {
		out.ResultURL = item.Data[0].AudioURL // 多首取第一首为主产物
	}
	return out, nil
}
