package task

// Vidu（生数科技）视频生成任务 adaptor。
// 知识移植自 New API 参考实现 relay/channel/task/vidu/adaptor.go（字段/状态/边角按其生产解析结构体
// 移植——非我方真机捕获，真机验证走 env 门控 smoke）。
// 鉴权：trivial——Authorization: "Token " + key（注意前缀是 Token 非 Bearer，用 Bearer 必 401）。
// 计费体制：A 类——查询响应带 credits（上游实耗点数），ParseQuery 用它精确结算；
//          EstimateOutputTokens 是上界（duration×resolution 表；未知取最高档）。
// 伪 token 约定：1,000,000 伪 token = 1 个 vidu credit；output_usd_per_1m = 每 credit 美元价。

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

type viduTask struct{}

// action 编码进 upstreamTaskID：vidu 查询是 GET /tasks/{id}/creations，不依赖 action，
// 但提交 URL 按 action 分叉（text2video/img2video/start-end2video/reference2video）。查询不需 action，
// 故不编码（裸 task id）。
func (viduTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	// vidu credits 与 duration×resolution 相关；New API 无档表（靠模型名区分价）。保守上界：
	// duration（默认 5s）线性 + 1080p 基准。未知/畸形 → 最高档。1 credit ≈ 5s/1080p 基准单位。
	dur := 5.0
	switch v := params["duration"].(type) {
	case float64:
		if v > 0 {
			dur = v
		}
	case string:
		return 40_000_000 // 畸形类型：最高档上界（40 credits，绝不低估）
	}
	resFactor := 1.0
	if r, ok := params["resolution"].(string); ok {
		switch strings.ToLower(r) {
		case "360p", "480p", "512":
			resFactor = 0.5
		case "720p":
			resFactor = 0.75
		case "1080p":
			resFactor = 1.0
		default:
			resFactor = 2.0 // 未知档（可能 2k/4k）：上界
		}
	}
	// 基准放宽（对抗 review CONFIRMED：hold 须真上界，credits 无先验封顶）：5s/1080p 基准取
	// 8 credits——真实 viduq1/viduq2 单次渲染普遍数 credits，配合 settle 的 cap-at-hold 双保险
	// （task.go 已封顶不超 hold），既避系统性少收又绝不透支。真机 smoke 待标定。
	credits := dur / 5.0 * resFactor * 8
	if credits < 4 {
		credits = 4
	}
	if credits > 40 {
		credits = 40
	}
	return int64(credits * 1_000_000)
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// viduSubmitPath：按图片数量/显式 action 分叉到 4 个端点（薄透传，action 由 Params/images 判定）。
func viduSubmitPath(params map[string]any) string {
	if a, ok := params["action"].(string); ok && a != "" {
		switch a {
		case "text2video", "img2video", "start-end2video", "reference2video":
			return "/" + a
		}
	}
	n := 0
	if imgs, ok := params["images"].([]any); ok {
		n = len(imgs)
	}
	switch {
	case n == 0:
		return "/text2video"
	case n == 1:
		return "/img2video"
	case n == 2:
		return "/start-end2video"
	default:
		return "/reference2video"
	}
}

func (viduTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	// prompt/images 从 OpenAI content 数组提取（对抗 review CONFIRMED：此前只读 Params 丢 prompt）；
	// Params 显式值优先覆盖。
	prompt, images := taskContentParts(in.Content)
	payload := map[string]any{}
	if prompt != "" {
		payload["prompt"] = prompt
	}
	if len(images) > 0 {
		payload["images"] = images
	}
	for k, v := range in.Params {
		payload[k] = v
	}
	// action/路由：Params 显式 action 优先，否则按图片数量（含 content 提取的）判定。
	pathParams := map[string]any{}
	for k, v := range in.Params {
		pathParams[k] = v
	}
	if _, ok := pathParams["images"]; !ok && len(images) > 0 {
		pathParams["images"] = toAnySlice(images)
	}
	path := viduSubmitPath(pathParams)
	model := in.Model
	// reference2video 只接受裸 viduq2（去 -pro/-turbo 后缀，对标 New API，对抗 review LOW）
	if path == "/reference2video" && strings.Contains(model, "viduq2") {
		model = "viduq2"
	}
	payload["model"] = model
	delete(payload, "action") // action 只用于选路径，不进上游体
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(ch.BaseURL, "/") + "/ent/v2" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+credential) // 方言：Token 非 Bearer
	return req, nil
}

// viduSubmitResp：提交响应（task_id + state；state=="failed" 是同步失败）。
type viduSubmitResp struct {
	TaskID string `json:"task_id"`
	State  string `json:"state"`
}

func (viduTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusBadRequest {
			return "", &UpstreamRejectedError{StatusCode: resp.StatusCode, Message: clipReason(string(raw), 300)}
		}
		return "", fmt.Errorf("vidu: submit status %d", resp.StatusCode)
	}
	var parsed viduSubmitResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("vidu: submit parse: %w", err)
	}
	// 时序陷阱：提交响应自带 state，failed 即同步失败——ParseSubmit 阶段拦截触发 release，不等轮询。
	if parsed.State == "failed" {
		return "", fmt.Errorf("vidu: upstream rejected submit (state=failed)")
	}
	if parsed.TaskID == "" {
		return "", fmt.Errorf("vidu: submit response missing task id")
	}
	return parsed.TaskID, nil
}

func (viduTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	url := strings.TrimRight(ch.BaseURL, "/") + "/ent/v2/tasks/" + upstreamTaskID + "/creations"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+credential)
	return req, nil
}

// viduQueryResp：查询响应。state ∈ {created,queueing,processing,success,failed}（queueing 双 e）。
type viduQueryResp struct {
	State     string `json:"state"`
	ErrCode   string `json:"err_code"`
	Credits   int64  `json:"credits"` // 上游实耗点数（New API 弃用，我们用于 A 类结算）
	Creations []struct {
		URL string `json:"url"`
	} `json:"creations"`
}

func (viduTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("vidu: query status %d", resp.StatusCode)
	}
	var parsed viduQueryResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("vidu: query parse: %w", err)
	}
	out := QueryResult{}
	// 方言：queueing 双 e；终态 failed（非 failure）；全小写。
	switch parsed.State {
	case "created", "queueing":
		out.Status = StatusQueued
	case "processing":
		out.Status = StatusRunning
	case "success":
		out.Status = StatusSucceeded
		if len(parsed.Creations) > 0 {
			out.ResultURL = parsed.Creations[0].URL // 多产物只取 [0]（cover_url 丢弃）
		}
		if parsed.Credits > 0 { // A 类结算：credits × 1M 伪 token
			out.CompletionTokens = parsed.Credits * 1_000_000
		}
	case "failed":
		out.Status = StatusFailed
		out.FailReason = parsed.ErrCode // 失败只有 err_code 码，无 message
	default:
		out.Status = StatusRunning // 未知状态兜底，绝不误杀
	}
	return out, nil
}
