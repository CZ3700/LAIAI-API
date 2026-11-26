package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/relay"
)

func buildAdapterInputMap(in SubmitInput) map[string]any {
	raw := map[string]any{}
	if len(in.RawBody) > 0 {
		raw = maps.Clone(in.RawBody)
	}
	for k, v := range in.Params {
		if _, exists := raw[k]; !exists {
			raw[k] = v
		}
	}
	if r, ok := in.Params["resolution"]; ok && r != nil {
		raw["resolution"] = r
	}
	if len(in.Content) > 0 {
		if _, exists := raw["content"]; !exists {
			raw["content"] = in.Content
		}
		if _, hasPrompt := raw["prompt"]; !hasPrompt {
			if p, imgs := taskContentParts(in.Content); p != "" || len(imgs) > 0 {
				if p != "" {
					raw["prompt"] = p
				}
				if len(imgs) > 0 && raw["images"] == nil {
					raw["images"] = toAnySlice(imgs)
				}
			}
		}
	}
	return raw
}

// ── 1. 可灵 Kling 3.0 / Omni (`target_type: "kling_video"`) ─────────────────────────

type klingV3Task struct{}

var _ ForwardAwareAdaptor = klingV3Task{}

func (klingV3Task) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	return (klingTask{}).EstimateOutputTokens(model, content, params)
}

func (klingV3Task) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	return (klingV3Task{}).SubmitRequestWithForward(ctx, ch, nil, credential, in)
}

func (klingV3Task) SubmitRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	in SubmitInput,
) (*http.Request, error) {
	bearer, err := forward.ResolveKlingBearer(credential, time.Now())
	if err != nil {
		return nil, err
	}
	upstreamModel := ch.UpstreamModel(in.Model)
	raw := buildAdapterInputMap(in)
	upPath := "/text-to-video/${model}"
	if fwd != nil && fwd.UpstreamPath != "" {
		upPath = fwd.UpstreamPath
	}
	built := forward.BuildKlingV3VideoBody(raw, upPath)
	dynPath := forward.ResolveKlingDynamicPath(upPath, upstreamModel, built)
	expanded := forward.ExpandUpstreamPath(dynPath, upstreamModel, "")
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, expanded)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(built)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	return req, nil
}

func (klingV3Task) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusBadRequest {
			return "", &UpstreamRejectedError{StatusCode: resp.StatusCode, Message: clipReason(string(raw), 300)}
		}
		return "", fmt.Errorf("kling_video: submit status %d", resp.StatusCode)
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		TaskID  string `json:"task_id"`
		ID      string `json:"id"`
		Data    struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("kling_video: submit parse: %w", err)
	}
	if parsed.Code != 0 {
		if submitErrorIsClientFault(parsed.Message) {
			return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest, Message: parsed.Message}
		}
		return "", fmt.Errorf("kling_video: submit failed upstream (code=%d)", parsed.Code)
	}
	id := firstNonEmptyStr(parsed.Data.ID, parsed.Data.TaskID, parsed.TaskID, parsed.ID)
	if id == "" {
		return "", fmt.Errorf("kling_video: submit response missing task id")
	}
	return id, nil
}

func (klingV3Task) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	return (klingV3Task{}).QueryRequestWithForward(ctx, ch, nil, credential, upstreamTaskID)
}

func (klingV3Task) QueryRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	upstreamTaskID string,
) (*http.Request, error) {
	bearer, err := forward.ResolveKlingBearer(credential, time.Now())
	if err != nil {
		return nil, err
	}
	pollPath := "/tasks?task_ids=" + upstreamTaskID
	if fwd != nil && fwd.PollPath != "" {
		pollPath = forward.ExpandUpstreamPath(fwd.PollPath, "", upstreamTaskID)
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, pollPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	return req, nil
}

func (klingV3Task) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("kling_video: query status %d", resp.StatusCode)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return QueryResult{}, fmt.Errorf("kling_video: query parse: %w", err)
	}

	var item map[string]any
	if dataArr, ok := root["data"].([]any); ok && len(dataArr) > 0 {
		item, _ = dataArr[0].(map[string]any)
	} else if tasksArr, ok := root["tasks"].([]any); ok && len(tasksArr) > 0 {
		item, _ = tasksArr[0].(map[string]any)
	} else if dataObj, ok := root["data"].(map[string]any); ok {
		item = dataObj
	} else {
		item = root
	}
	if item == nil {
		return QueryResult{Status: StatusRunning}, nil
	}

	rawStatus := strings.ToLower(firstNonEmptyStr(strField(item, "status"), strField(item, "task_status")))
	msg := firstNonEmptyStr(strField(item, "message"), strField(item, "task_status_msg"))
	out := QueryResult{FailReason: msg}
	switch rawStatus {
	case "submitted", "queued", "pending":
		out.Status = StatusQueued
	case "processing", "running", "in_progress":
		out.Status = StatusRunning
	case "succeed", "succeeded", "completed", "success":
		out.Status = StatusSucceeded
		if outputs, ok := item["outputs"].([]any); ok && len(outputs) > 0 {
			if first, ok := outputs[0].(map[string]any); ok {
				out.ResultURL = strField(first, "url")
			}
		}
		if out.ResultURL == "" {
			if tr, ok := item["task_result"].(map[string]any); ok {
				if vids, ok := tr["videos"].([]any); ok && len(vids) > 0 {
					if v0, ok := vids[0].(map[string]any); ok {
						out.ResultURL = strField(v0, "url")
					}
				}
			}
		}
		if fStr := strField(item, "final_unit_deduction"); fStr != "" {
			if f, err := strconv.ParseFloat(fStr, 64); err == nil && f > 0 {
				out.CompletionTokens = int64(f * 1_000_000)
			}
		}
	case "failed", "cancelled", "canceled":
		out.Status = StatusFailed
	default:
		out.Status = StatusRunning
	}
	return out, nil
}

// ── 2. MiniMax V2 (`target_type: "minimax_video"`) ─────────────────────────

type minimaxV2Task struct{}

var (
	_ ForwardAwareAdaptor = minimaxV2Task{}
	_ ResultFetcher       = minimaxV2Task{}
)

func (minimaxV2Task) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	return (hailuoTask{}).EstimateOutputTokens(model, content, params)
}

func (minimaxV2Task) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	return (minimaxV2Task{}).SubmitRequestWithForward(ctx, ch, nil, credential, in)
}

func (minimaxV2Task) SubmitRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	in SubmitInput,
) (*http.Request, error) {
	upstreamModel := ch.UpstreamModel(in.Model)
	raw := buildAdapterInputMap(in)
	payload := forward.BuildMiniMaxVideoBody(upstreamModel, raw)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	upPath := "/v2/video_generation"
	if fwd != nil && fwd.UpstreamPath != "" {
		upPath = forward.ExpandUpstreamPath(fwd.UpstreamPath, upstreamModel, "")
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, upPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	authType := forward.AuthBearer
	if fwd != nil && fwd.AuthType != "" {
		authType = fwd.AuthType
	}
	forward.ApplyAuth(req, authType, credential)
	return req, nil
}

func (minimaxV2Task) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusBadRequest {
			return "", &UpstreamRejectedError{StatusCode: resp.StatusCode, Message: clipReason(string(raw), 300)}
		}
		return "", fmt.Errorf("minimax_video: submit status %d", resp.StatusCode)
	}
	var parsed struct {
		TaskID   string         `json:"task_id"`
		ID       string         `json:"id"`
		BaseResp hailuoBaseResp `json:"base_resp"`
		Task     struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("minimax_video: submit parse: %w", err)
	}
	if parsed.BaseResp.StatusCode != 0 {
		if submitErrorIsClientFault(parsed.BaseResp.StatusMsg) {
			return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest, Message: parsed.BaseResp.StatusMsg}
		}
		return "", fmt.Errorf("minimax_video: submit failed upstream (code=%d)", parsed.BaseResp.StatusCode)
	}
	id := firstNonEmptyStr(parsed.TaskID, parsed.Task.ID, parsed.ID)
	if id == "" {
		return "", fmt.Errorf("minimax_video: submit response missing task id")
	}
	return id, nil
}

func (minimaxV2Task) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	return (minimaxV2Task{}).QueryRequestWithForward(ctx, ch, nil, credential, upstreamTaskID)
}

func (minimaxV2Task) QueryRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	upstreamTaskID string,
) (*http.Request, error) {
	pollPath := "/v2/query/video_generation/" + upstreamTaskID
	if fwd != nil && fwd.PollPath != "" {
		pollPath = forward.ExpandUpstreamPath(fwd.PollPath, "", upstreamTaskID)
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, pollPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	authType := forward.AuthBearer
	if fwd != nil && fwd.AuthType != "" {
		authType = fwd.AuthType
	}
	forward.ApplyAuth(req, authType, credential)
	return req, nil
}

func (minimaxV2Task) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("minimax_video: query status %d", resp.StatusCode)
	}
	var parsed struct {
		Status   string         `json:"status"`
		FileID   string         `json:"file_id"`
		BaseResp hailuoBaseResp `json:"base_resp"`
		Task     *struct {
			Status  string `json:"status"`
			Content struct {
				URL          string `json:"url"`
				VideoURL     string `json:"video_url"`
				LastFrameURL string `json:"last_frame_url"`
			} `json:"content"`
			Usage struct {
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("minimax_video: query parse: %w", err)
	}
	if parsed.BaseResp.StatusCode != 0 {
		return QueryResult{}, fmt.Errorf("minimax_video: query base_resp %d", parsed.BaseResp.StatusCode)
	}

	if parsed.Task != nil {
		st := strings.ToLower(parsed.Task.Status)
		out := QueryResult{
			FailReason:   parsed.Task.Error.Message,
			LastFrameURL: parsed.Task.Content.LastFrameURL,
		}
		switch st {
		case "queued", "pending":
			out.Status = StatusQueued
		case "running", "processing", "in_progress":
			out.Status = StatusRunning
		case "succeeded", "completed", "success":
			out.Status = StatusSucceeded
			out.ResultURL = firstNonEmptyStr(parsed.Task.Content.URL, parsed.Task.Content.VideoURL)
			out.CompletionTokens = parsed.Task.Usage.CompletionTokens
			if out.CompletionTokens <= 0 {
				out.CompletionTokens = parsed.Task.Usage.TotalTokens
			}
		case "failed", "cancelled", "canceled", "fail":
			out.Status = StatusFailed
		default:
			out.Status = StatusRunning
		}
		return out, nil
	}

	// V1 hailuo fallback envelope
	out := QueryResult{}
	switch parsed.Status {
	case "Preparing", "Queueing", "Processing":
		out.Status = StatusRunning
	case "Success":
		out.Status = StatusSucceeded
		out.ResultRef = parsed.FileID
	case "Fail":
		out.Status = StatusFailed
		out.FailReason = "task failed"
	default:
		out.Status = StatusRunning
	}
	return out, nil
}

func (minimaxV2Task) FetchResultURL(ctx context.Context, client *http.Client, ch *relay.Channel, credential, fileID string) (string, error) {
	return (hailuoTask{}).FetchResultURL(ctx, client, ch, credential, fileID)
}

// ── 3. 即梦 AI (`target_type: "jimeng_video"`) ─────────────────────────

type jimengVideoTask struct{}

var _ ForwardAwareAdaptor = jimengVideoTask{}

func (jimengVideoTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	return (volcengineTask{}).EstimateOutputTokens(model, content, params)
}

func encodeJimengTaskID(reqKey, taskID string) string {
	return reqKey + ":" + taskID
}

func decodeJimengTaskID(composite string) (reqKey, taskID string) {
	if rk, tid, ok := strings.Cut(composite, ":"); ok && rk != "" {
		return rk, tid
	}
	return "dreamina_ic_generate_video_v2", composite
}

func (jimengVideoTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	return (jimengVideoTask{}).SubmitRequestWithForward(ctx, ch, nil, credential, in)
}

func (jimengVideoTask) SubmitRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	in SubmitInput,
) (*http.Request, error) {
	ak, sk, _, ok := forward.SplitAKSK(credential)
	if !ok {
		return nil, fmt.Errorf("jimeng_video: credential must be access_key:secret_key")
	}
	upstreamModel := ch.UpstreamModel(in.Model)
	raw := buildAdapterInputMap(in)
	payload := forward.BuildJimengVideoBody(upstreamModel, raw)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	basePath := "/"
	if fwd != nil && fwd.UpstreamPath != "" {
		basePath = fwd.UpstreamPath
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, basePath+"?Action=CVSync2AsyncSubmitTask&Version=2022-08-31")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := forward.SignVolcengineV4(req, ak, sk, "cv", "cn-north-1", body, time.Now()); err != nil {
		return nil, err
	}
	return req, nil
}

func (jimengVideoTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusBadRequest {
			return "", &UpstreamRejectedError{StatusCode: resp.StatusCode, Message: clipReason(string(raw), 300)}
		}
		return "", fmt.Errorf("jimeng_video: submit status %d", resp.StatusCode)
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("jimeng_video: submit parse: %w", err)
	}
	if parsed.Code != 10000 {
		if submitErrorIsClientFault(parsed.Message) {
			return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest, Message: parsed.Message}
		}
		return "", fmt.Errorf("jimeng_video: submit failed upstream (code=%d)", parsed.Code)
	}
	if parsed.Data.TaskID == "" {
		return "", fmt.Errorf("jimeng_video: submit response missing task_id")
	}
	reqKey := "dreamina_ic_generate_video_v2"
	if resp.Request != nil && resp.Request.GetBody != nil {
		if rc, gerr := resp.Request.GetBody(); gerr == nil {
			defer rc.Close()
			var reqObj map[string]any
			if json.NewDecoder(rc).Decode(&reqObj) == nil {
				if rk, ok := reqObj["req_key"].(string); ok && rk != "" {
					reqKey = rk
				}
			}
		}
	}
	return encodeJimengTaskID(reqKey, parsed.Data.TaskID), nil
}

func (jimengVideoTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	return (jimengVideoTask{}).QueryRequestWithForward(ctx, ch, nil, credential, upstreamTaskID)
}

func (jimengVideoTask) QueryRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	upstreamTaskID string,
) (*http.Request, error) {
	ak, sk, _, ok := forward.SplitAKSK(credential)
	if !ok {
		return nil, fmt.Errorf("jimeng_video: credential must be access_key:secret_key")
	}
	reqKey, tid := decodeJimengTaskID(upstreamTaskID)
	payload, err := json.Marshal(map[string]any{
		"req_key":  reqKey,
		"task_id":  tid,
		"req_json": `{"return_url":true}`,
	})
	if err != nil {
		return nil, err
	}
	basePath := "/"
	if fwd != nil && fwd.UpstreamPath != "" {
		basePath = fwd.UpstreamPath
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, basePath+"?Action=CVSync2AsyncGetResult&Version=2022-08-31")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err := forward.SignVolcengineV4(req, ak, sk, "cv", "cn-north-1", payload, time.Now()); err != nil {
		return nil, err
	}
	return req, nil
}

func (jimengVideoTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("jimeng_video: query status %d", resp.StatusCode)
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Status   string   `json:"status"`
			VideoURL string   `json:"video_url"`
			URLs     []string `json:"image_urls"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("jimeng_video: query parse: %w", err)
	}
	if parsed.Code != 10000 {
		return QueryResult{}, fmt.Errorf("jimeng_video: query code=%d", parsed.Code)
	}
	out := QueryResult{FailReason: parsed.Message}
	switch strings.ToLower(parsed.Data.Status) {
	case "in_queue", "queued":
		out.Status = StatusQueued
	case "generating", "processing", "running":
		out.Status = StatusRunning
	case "done", "succeeded", "success":
		out.Status = StatusSucceeded
		out.ResultURL = parsed.Data.VideoURL
		if out.ResultURL == "" && len(parsed.Data.URLs) > 0 {
			out.ResultURL = parsed.Data.URLs[0]
		}
	case "not_found", "expired", "failed":
		out.Status = StatusFailed
		if out.FailReason == "" {
			out.FailReason = "jimeng task " + parsed.Data.Status
		}
	default:
		out.Status = StatusRunning
	}
	return out, nil
}

// ── 4. 腾讯云点播 VOD AIGC (`target_type: "tencent_vod_video"`) ─────────────────────────

type tencentVodVideoTask struct{}

var _ ForwardAwareAdaptor = tencentVodVideoTask{}

func (tencentVodVideoTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	return (volcengineTask{}).EstimateOutputTokens(model, content, params)
}

func (tencentVodVideoTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	return (tencentVodVideoTask{}).SubmitRequestWithForward(ctx, ch, nil, credential, in)
}

func (tencentVodVideoTask) SubmitRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	in SubmitInput,
) (*http.Request, error) {
	sid, skey, subAppID, err := forward.ParseTencentVODCredential(credential)
	if err != nil {
		return nil, err
	}
	upstreamModel := ch.UpstreamModel(in.Model)
	raw := buildAdapterInputMap(in)
	payload := forward.BuildTencentVODVideoBody(upstreamModel, subAppID, raw)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	upPath := "/"
	if fwd != nil && fwd.UpstreamPath != "" {
		upPath = fwd.UpstreamPath
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, upPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := forward.SignTencentTC3(req, sid, skey, "vod", "CreateAigcVideoTask", "2018-07-17", "", body, time.Now()); err != nil {
		return nil, err
	}
	return req, nil
}

func (tencentVodVideoTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("tencent_vod_video: submit status %d", resp.StatusCode)
	}
	var parsed struct {
		Response struct {
			TaskId string `json:"TaskId"`
			Error  *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("tencent_vod_video: submit parse: %w", err)
	}
	if parsed.Response.Error != nil {
		errMsg := parsed.Response.Error.Code + ": " + parsed.Response.Error.Message
		if submitErrorIsClientFault(errMsg) {
			return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest, Message: parsed.Response.Error.Message}
		}
		return "", fmt.Errorf("tencent_vod_video: submit error (%s)", parsed.Response.Error.Code)
	}
	if parsed.Response.TaskId == "" {
		return "", fmt.Errorf("tencent_vod_video: submit response missing TaskId")
	}
	return parsed.Response.TaskId, nil
}

func (tencentVodVideoTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	return (tencentVodVideoTask{}).QueryRequestWithForward(ctx, ch, nil, credential, upstreamTaskID)
}

func (tencentVodVideoTask) QueryRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	upstreamTaskID string,
) (*http.Request, error) {
	sid, skey, subAppID, err := forward.ParseTencentVODCredential(credential)
	if err != nil {
		return nil, err
	}
	qMap := map[string]any{"TaskId": upstreamTaskID}
	if subAppID > 0 {
		qMap["SubAppId"] = subAppID
	}
	body, err := json.Marshal(qMap)
	if err != nil {
		return nil, err
	}
	upPath := "/"
	if fwd != nil && fwd.UpstreamPath != "" {
		upPath = fwd.UpstreamPath
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, upPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := forward.SignTencentTC3(req, sid, skey, "vod", "DescribeTaskDetail", "2018-07-17", "", body, time.Now()); err != nil {
		return nil, err
	}
	return req, nil
}

func (tencentVodVideoTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("tencent_vod_video: query status %d", resp.StatusCode)
	}
	var parsed struct {
		Response struct {
			Status        string `json:"Status"`
			AigcVideoTask *struct {
				Status  string `json:"Status"`
				ErrCode int    `json:"ErrCode"`
				Message string `json:"Message"`
				Output  struct {
					FileInfos []struct {
						FileUrl string `json:"FileUrl"`
					} `json:"FileInfos"`
				} `json:"Output"`
			} `json:"AigcVideoTask"`
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("tencent_vod_video: query parse: %w", err)
	}
	if parsed.Response.Error != nil {
		return QueryResult{}, fmt.Errorf("tencent_vod_video: query error (%s)", parsed.Response.Error.Code)
	}
	st := strings.ToUpper(parsed.Response.Status)
	out := QueryResult{}
	switch st {
	case "WAITING":
		out.Status = StatusQueued
	case "PROCESSING":
		out.Status = StatusRunning
	case "FINISH":
		taskInfo := parsed.Response.AigcVideoTask
		if taskInfo == nil || taskInfo.ErrCode != 0 {
			out.Status = StatusFailed
			if taskInfo != nil {
				out.FailReason = taskInfo.Message
			}
			return out, nil
		}
		out.Status = StatusSucceeded
		if len(taskInfo.Output.FileInfos) > 0 {
			out.ResultURL = taskInfo.Output.FileInfos[0].FileUrl
		}
	default:
		out.Status = StatusRunning
	}
	return out, nil
}

// ── 5. 火山 MediaKit 视频画质增强 (`target_type: "volcengine_media_enhance"`) ─────────

type volcMediaEnhanceTask struct{}

var _ ForwardAwareAdaptor = volcMediaEnhanceTask{}

func (volcMediaEnhanceTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	return 100_000
}

func (volcMediaEnhanceTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	return (volcMediaEnhanceTask{}).SubmitRequestWithForward(ctx, ch, nil, credential, in)
}

func (volcMediaEnhanceTask) SubmitRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	in SubmitInput,
) (*http.Request, error) {
	upstreamModel := ch.UpstreamModel(in.Model)
	raw := buildAdapterInputMap(in)
	payload := forward.BuildVolcMediaEnhanceBody(upstreamModel, raw)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	upPath := "/api/v1/tools/enhance-video"
	if fwd != nil && fwd.UpstreamPath != "" {
		upPath = forward.ExpandUpstreamPath(fwd.UpstreamPath, upstreamModel, "")
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, upPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	authType := forward.AuthBearer
	if fwd != nil && fwd.AuthType != "" {
		authType = fwd.AuthType
	}
	forward.ApplyAuth(req, authType, credential)
	return req, nil
}

func (volcMediaEnhanceTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusBadRequest {
			return "", &UpstreamRejectedError{StatusCode: resp.StatusCode, Message: clipReason(string(raw), 300)}
		}
		return "", fmt.Errorf("volc_enhance: submit status %d", resp.StatusCode)
	}
	var parsed struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("volc_enhance: submit parse: %w", err)
	}
	id := firstNonEmptyStr(parsed.TaskID, parsed.ID)
	if id == "" {
		return "", fmt.Errorf("volc_enhance: submit missing task_id")
	}
	return id, nil
}

func (volcMediaEnhanceTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	return (volcMediaEnhanceTask{}).QueryRequestWithForward(ctx, ch, nil, credential, upstreamTaskID)
}

func (volcMediaEnhanceTask) QueryRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	upstreamTaskID string,
) (*http.Request, error) {
	pollPath := "/api/v1/tasks/" + upstreamTaskID
	if fwd != nil && fwd.PollPath != "" {
		pollPath = forward.ExpandUpstreamPath(fwd.PollPath, "", upstreamTaskID)
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, pollPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	authType := forward.AuthBearer
	if fwd != nil && fwd.AuthType != "" {
		authType = fwd.AuthType
	}
	forward.ApplyAuth(req, authType, credential)
	return req, nil
}

func (volcMediaEnhanceTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("volc_enhance: query status %d", resp.StatusCode)
	}
	var parsed struct {
		Status   string `json:"status"`
		VideoURL string `json:"video_url"`
		Error    string `json:"error"`
		Result   struct {
			VideoURL string `json:"video_url"`
		} `json:"result"`
		Content struct {
			VideoURL string `json:"video_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("volc_enhance: query parse: %w", err)
	}
	out := QueryResult{FailReason: parsed.Error}
	switch strings.ToLower(parsed.Status) {
	case "pending", "queued":
		out.Status = StatusQueued
	case "processing", "running", "in_progress":
		out.Status = StatusRunning
	case "success", "succeeded", "completed":
		out.Status = StatusSucceeded
		out.ResultURL = firstNonEmptyStr(parsed.Result.VideoURL, parsed.Content.VideoURL, parsed.VideoURL)
	case "failed", "cancelled", "canceled":
		out.Status = StatusFailed
		if out.FailReason == "" {
			out.FailReason = "media enhance failed"
		}
	default:
		out.Status = StatusRunning
	}
	return out, nil
}

// ── 6. 标准 OpenAI 异步视频网关 (`target_type: "openai"`) ─────────────────────────

type openaiVideoTask struct{}

var _ ForwardAwareAdaptor = openaiVideoTask{}

func (openaiVideoTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	return (volcengineTask{}).EstimateOutputTokens(model, content, params)
}

func (openaiVideoTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	return (openaiVideoTask{}).SubmitRequestWithForward(ctx, ch, nil, credential, in)
}

func (openaiVideoTask) SubmitRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	in SubmitInput,
) (*http.Request, error) {
	payload := buildAdapterInputMap(in)
	payload["model"] = ch.UpstreamModel(in.Model)
	if len(in.Content) > 0 {
		payload["content"] = in.Content
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	upPath := "/v1/video/generations"
	if fwd != nil && fwd.UpstreamPath != "" {
		upPath = forward.ExpandUpstreamPath(fwd.UpstreamPath, ch.UpstreamModel(in.Model), "")
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, upPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	authType := forward.AuthBearer
	if fwd != nil && fwd.AuthType != "" {
		authType = fwd.AuthType
	}
	forward.ApplyAuth(req, authType, credential)
	return req, nil
}

func (openaiVideoTask) ParseSubmit(resp *http.Response) (string, error) {
	return (volcengineTask{}).ParseSubmit(resp)
}

func (openaiVideoTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	return (openaiVideoTask{}).QueryRequestWithForward(ctx, ch, nil, credential, upstreamTaskID)
}

func (openaiVideoTask) QueryRequestWithForward(
	ctx context.Context,
	ch *relay.Channel,
	fwd *forward.ResolvedForward,
	credential string,
	upstreamTaskID string,
) (*http.Request, error) {
	pollPath := "/v1/video/generations/" + upstreamTaskID
	if fwd != nil && fwd.PollPath != "" {
		pollPath = forward.ExpandUpstreamPath(fwd.PollPath, "", upstreamTaskID)
	}
	parsedURL, err := forward.JoinUpstreamURL(ch.BaseURL, pollPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, err
	}
	authType := forward.AuthBearer
	if fwd != nil && fwd.AuthType != "" {
		authType = fwd.AuthType
	}
	forward.ApplyAuth(req, authType, credential)
	return req, nil
}

func (openaiVideoTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("openai_video: query status %d", resp.StatusCode)
	}
	var parsed struct {
		Status    string `json:"status"`
		ResultURL string `json:"result_url"`
		URL       string `json:"url"`
		Data      []struct {
			URL string `json:"url"`
		} `json:"data"`
		Content struct {
			VideoURL string `json:"video_url"`
		} `json:"content"`
		Usage struct {
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		FailReason string `json:"fail_reason"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("openai_video: query parse: %w", err)
	}
	out := QueryResult{
		FailReason:       firstNonEmptyStr(parsed.FailReason, parsed.Error.Message),
		CompletionTokens: parsed.Usage.CompletionTokens,
	}
	if out.CompletionTokens <= 0 {
		out.CompletionTokens = parsed.Usage.TotalTokens
	}
	switch strings.ToLower(parsed.Status) {
	case "queued", "pending":
		out.Status = StatusQueued
	case "running", "processing", "in_progress":
		out.Status = StatusRunning
	case "completed", "succeeded", "success":
		out.Status = StatusSucceeded
		var dataURL string
		if len(parsed.Data) > 0 {
			dataURL = parsed.Data[0].URL
		}
		out.ResultURL = firstNonEmptyStr(parsed.ResultURL, dataURL, parsed.Content.VideoURL, parsed.URL)
	case "failed", "cancelled", "canceled":
		out.Status = StatusFailed
	default:
		out.Status = StatusRunning
	}
	return out, nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func strField(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
