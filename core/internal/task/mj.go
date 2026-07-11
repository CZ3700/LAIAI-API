package task

// Midjourney（mjproxy 网关方言）task adaptor（丝滑度批 C-4）。
//
// MJ 无官方 API——生态标准是 midjourney-proxy 网关方言，客户端（chatgpt-web-midjourney-proxy/
// lobe-chat 等）与聚合平台（NewAPI）都说这套协议：
//   提交: POST {base}/mj/submit/{action}   action ∈ imagine|change|blend|describe|shorten|modal
//         鉴权 mj-api-secret 头；响应 {"code":1|21|22,"description":...,"result":"<mj task id>"}
//         （1=成功 21=已存在 22=排队中 都携带 result；23=队列满 24=敏感词 4=参数错 无任务）
//   查询: GET {base}/mj/task/{id}/fetch → {"id","status":"SUBMITTED|IN_PROGRESS|SUCCESS|FAILURE",
//         "progress":"100%","imageUrl","failReason","buttons":[...]}
//
// 平台侧建模：每个 action 是一个 catalog "模型"（mj_imagine/mj_change/...），PerCallUSD 按次
// 定价（0048 快照）；任务骨架（hold→poll→settle/release）全复用。对外方言路由见 mj_handlers.go。
//
// ⚠️ fixture 纪律：形态按 midjourney-proxy 文档 + NewAPI 生产解析移植；真机验证待 mjproxy 上游。

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

// mjActions：受理的 action 白名单（路由 :action 校验 + 模型名派生）。
var mjActions = map[string]bool{
	"imagine": true, "change": true, "blend": true,
	"describe": true, "shorten": true, "modal": true,
}

// mjModelPrefix：action → catalog 模型名（mj_imagine 等，admin 按 PerCallUSD 定价）。
const mjModelPrefix = "mj_"

type midjourneyTask struct{}

// EstimateOutputTokens：MJ 按次计价（PerCallUSD），token 恒 0——hold 金额由价格快照直接承载。
func (midjourneyTask) EstimateOutputTokens(string, []any, map[string]any) int64 { return 0 }

// SubmitRequest：POST {base}/mj/submit/{action}，body = 客户端参数原样透传（taskId 已由
// handler 换成上游 id）。鉴权 mj-api-secret（mjproxy 方言，非 Bearer）。
func (midjourneyTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	action := strings.TrimPrefix(in.Model, mjModelPrefix)
	if !mjActions[action] {
		return nil, fmt.Errorf("midjourney: unknown action model %q", in.Model)
	}
	body, err := json.Marshal(in.Params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(ch.BaseURL, "/")+"/mj/submit/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("mj-api-secret", credential)
	return req, nil
}

// mjSubmitResp：mjproxy 提交响应。
type mjSubmitResp struct {
	Code        int    `json:"code"`
	Description string `json:"description"`
	Result      any    `json:"result"` // 通常 string 的 MJ 任务 id；describe/shorten 也同形
}

func (midjourneyTask) ParseSubmit(resp *http.Response) (string, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("midjourney: read submit response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("midjourney: submit HTTP %d", resp.StatusCode)
	}
	var r mjSubmitResp
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("midjourney: submit response not json")
	}
	switch r.Code {
	case 1, 21, 22: // 成功/已存在/排队中——都有任务在跑
		var id string
		switch v := r.Result.(type) {
		case string:
			id = v
		case float64: // 部分 mjproxy 实现回数字 id——不能走 %v（科学计数法毁 id）
			id = strconv.FormatFloat(v, 'f', -1, 64)
		}
		if id == "" {
			return "", fmt.Errorf("midjourney: submit ok but empty result id")
		}
		return id, nil
	case 4, 24:
		// 参数错/敏感词 = 确定性客户端问题：UpstreamRejectedError 短路故障转移（换网关也一样拒），
		// description 经 handler 的 rejected 分支带给客户端（丝滑：把上游解释给用户，不吞成泛化错误）。
		return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest,
			Message: fmt.Sprintf("code=%d: %s", r.Code, clipReason(r.Description, 200))}
	default:
		// 23 队列满等：渠道侧瞬时状态，普通错误 → 允许换渠道重试。
		return "", fmt.Errorf("midjourney: submit rejected code=%d: %s", r.Code, clipReason(r.Description, 200))
	}
}

// QueryRequest：GET {base}/mj/task/{id}/fetch。
func (midjourneyTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential string, upstreamTaskID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(ch.BaseURL, "/")+"/mj/task/"+upstreamTaskID+"/fetch", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("mj-api-secret", credential)
	return req, nil
}

// mjFetchResp：mjproxy 任务对象子集（轮询判终态用；对外 fetch 透传完整体，见 mj_handlers）。
type mjFetchResp struct {
	Status     string `json:"status"` // NOT_START|SUBMITTED|MODAL|IN_PROGRESS|FAILURE|SUCCESS|CANCEL
	ImageURL   string `json:"imageUrl"`
	FailReason string `json:"failReason"`
	Properties struct {
		FinalPrompt string `json:"finalPrompt"`
	} `json:"properties"`
}

func (midjourneyTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return QueryResult{}, fmt.Errorf("midjourney: read fetch response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return QueryResult{}, fmt.Errorf("midjourney: fetch HTTP %d", resp.StatusCode)
	}
	var r mjFetchResp
	if err := json.Unmarshal(body, &r); err != nil {
		return QueryResult{}, fmt.Errorf("midjourney: fetch response not json")
	}
	switch r.Status {
	case "SUCCESS":
		// describe/shorten 无图（产出在 properties/prompt 文本）：ResultURL 允许为空，
		// 客户端从 /mj/task/:id/fetch 的透传体拿全量字段。
		return QueryResult{Status: StatusSucceeded, ResultURL: r.ImageURL}, nil
	case "FAILURE", "CANCEL":
		return QueryResult{Status: StatusFailed, FailReason: clipReason(r.FailReason, 300)}, nil
	default: // NOT_START/SUBMITTED/MODAL/IN_PROGRESS/空——继续轮询
		return QueryResult{Status: StatusRunning}, nil
	}
}
