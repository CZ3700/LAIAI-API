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
	// params 是 model/content 之外的顶层任务参数——估算必须按 duration/resolution 感知
	// （§3.8：参数抬高产出必须同步抬高预冻结，否则 capture>hold 负余额）。
	EstimateOutputTokens(model string, content []any, params map[string]any) int64
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

// taskAdaptors：任务型（异步：提交→轮询→取产物）厂商注册表。
//
// 新增一个视频/任务厂商 = 三步（与 chat 适配器同纪律）：
//  1. 新文件 internal/task/<vendor>.go 定义 `type <vendor>Task struct{}` 并实现 Adaptor 的 5 个方法
//     （EstimateOutputTokens / SubmitRequest / ParseSubmit / QueryRequest / ParseQuery），把厂商
//     的提交/查询请求与响应**双向归一**到本包的 SubmitInput 与 QueryResult（Status* 常量）。
//  2. 在下面注册表登记 "<provider-code>": <vendor>Task{}（或在 providers.go 给该 provider 档案
//     设 Adapter 家族名，走下面的家族回退）。
//  3. 🚨 红线：用**真实上游 raw response** 写 fixture 测试一次再上线——绝不凭文档臆造字段
//     （否则重蹈 epay 方言事故）。提交/查询响应的 task id、status 枚举、产物 URL、usage 字段
//     都必须对真实 API 校验。
//
// 计费：任务按 usage（如视频 token / 时长）结算，复用 model_prices 定价 + hold→capture→overage。
var taskAdaptors = map[string]Adaptor{
	"volcengine": volcengineTask{},
	"modelark":   volcengineTask{},
}

// adaptorFor：先按 provider code 精确查注册表；未命中再按 provider 档案的 Adapter 家族回退一次
// （如自定义渠道 code 映射到某视频家族）。都查不到返回 ErrNoTaskAdaptor。
func adaptorFor(provider string) (Adaptor, error) {
	if ad, ok := taskAdaptors[provider]; ok {
		return ad, nil
	}
	if p, ok := relay.ProfileFor(provider); ok {
		if ad, ok := taskAdaptors[p.Adapter]; ok {
			return ad, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNoTaskAdaptor, provider)
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

// contentHasDirectiveHint：prompt 内嵌参数指令探测（验修 INCOMPLETE 封堵，覆盖三类绕过）：
// 扫描每个 content 项的**全部顶层字符串值**（含 "Text" 等大小写变体键、纯字符串项），
// 匹配半角 "--" 与全角 "－－"；未知项形态保守按含指令处理（识别失败绝不低估）。
// 不深入嵌套对象——image_url.url 等 URL 位于嵌套 map，含 "--" 的无害 URL 不误伤 i2v。
func contentHasDirectiveHint(content []any) bool {
	for _, item := range content {
		switch it := item.(type) {
		case string:
			if hasDirectiveMarker(it) {
				return true
			}
		case map[string]any:
			for _, v := range it {
				if s, ok := v.(string); ok && hasDirectiveMarker(s) {
					return true
				}
			}
		default:
			return true // 未知形态：保守按含指令（上游多半会拒绝，误伤只是多冻结）
		}
	}
	return false
}

func hasDirectiveMarker(s string) bool {
	return strings.Contains(s, "--") || strings.Contains(s, "－－")
}

// seedanceResolutionPixels：分辨率档位 → 像素数（ModelArk 视频 token 公式的宽×高因子）。
var seedanceResolutionPixels = map[string]int64{
	"480p":  864 * 480,
	"720p":  1280 * 720,
	"1080p": 1920 * 1080,
	"2k":    2560 * 1440,
	"4k":    3840 * 2160,
}

// EstimateOutputTokens：duration/resolution/fps 感知的 hold 上界（§3.8 "按 duration、resolution 预扣"）。
// ModelArk 视频 token 口径：tokens ≈ 宽×高×fps×时长/1024。真机标定（2026-07-05，doubao-seedance-2-0
// 真实任务）：4s/16:9/默认档实测 completion_tokens=87300，回显 resolution=720p、framespersecond=24
// ——公式值 86400 偏差 ~1%。上浮 30% 边际（公式舍入 + generate_audio 音轨附加项）；真实成本由
// 上游 usage 结算校正，差额自动解冻。
//
// 保守纪律（对抗 review 两条 CONFIRMED 资金洞的封堵，绝不低估）：
//   - prompt 文本可内嵌参数指令（Seedance 方言 --resolution/--rs、--duration/--dur 等），能绕过
//     顶层参数抬高真实产出 → 文本含 "--" 一律按 estCap 估（误伤只是多冻结，结算即解冻）。
//   - 无显式 resolution 时基准取 1080p 而非真机默认 720p：家族内其他模型（seedance-1-0-pro 等）
//     默认 1080p，720p 基准会被击穿；1080p 基准统一覆盖（对 2.0 裸请求约 2.9× 过冻结，可接受）。
//   - 畸形类型（字符串 duration/非字符串 resolution/非数值 fps）与未知 resolution 档位 → 上界估。
func (volcengineTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	const estCap = 4_000_000 // 上限防御：超大参数不至于瞬时冻结天量资金（4K 15s ≈2.9M×1.3 留满）
	// prompt 内嵌指令探测：含指令标记 → 指令可把 res/duration 抬到模型上限，按 estCap。
	if contentHasDirectiveHint(content) {
		return estCap
	}
	duration := 5.0 // 上游默认 5s（家族一致文档口径）
	if v, exists := params["duration"]; exists {
		f, ok := v.(float64)
		if !ok || f <= 0 || f > 3600 {
			return estCap
		}
		duration = f
	}
	fps := 24.0 // 真机回显 framespersecond=24
	if v, exists := params["fps"]; exists {
		f, ok := v.(float64)
		if !ok || f <= 0 || f > 240 {
			return estCap
		}
		fps = f
	}
	if v, exists := params["framespersecond"]; exists {
		f, ok := v.(float64)
		if !ok || f <= 0 || f > 240 {
			return estCap
		}
		fps = f
	}
	pixels := seedanceResolutionPixels["1080p"] // 无显式参数的保守基准（见上）
	if v, exists := params["resolution"]; exists {
		r, ok := v.(string)
		if !ok {
			return estCap
		}
		px, known := seedanceResolutionPixels[strings.ToLower(r)]
		if !known {
			px = seedanceResolutionPixels["4k"]
		}
		pixels = px
	}
	est := float64(pixels) * fps * duration / 1024 * 1.3
	if est < 100_000 { // 下限兜底：超小任务不至于 hold 过小
		est = 100_000
	}
	if est > estCap {
		est = estCap
	}
	return int64(est)
}

func (volcengineTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	// 薄透传：客户端直接说 ModelArk 方言。content 数组 + 顶层任务参数（generate_audio/ratio/
	// duration/watermark/resolution 等，Seedance 2.0 真机验证 2026-07-05）原样透传，不做二次发明。
	// model/content 由平台字段权威覆盖（防 Params 注入同名键顶掉准入校验过的值）。
	payload := make(map[string]any, len(in.Params)+2)
	for k, v := range in.Params {
		payload[k] = v
	}
	payload["model"] = in.Model
	payload["content"] = in.Content
	body, err := json.Marshal(payload)
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
		// 上游 400 = 客户端自己的任务参数问题（参数透传后的常态错误面，对抗 review CONFIRMED）：
		// 带上游解释返回类型化错误 → handler 转 400 给客户端（与 chat 透传上游错误同性质）。
		// 401/403/404 等是渠道配置/key 问题，仍走通用 502（真机 fixture：400=InvalidParameter）。
		if resp.StatusCode == http.StatusBadRequest {
			var parsed struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &parsed)
			return "", &UpstreamRejectedError{StatusCode: resp.StatusCode, Message: parsed.Error.Message}
		}
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
	// ⚠️ failed/cancelled 腿的形态是文档推导（succeeded/running/error 响应均已真机验证 2026-07-05；
	// 终态失败无法安全按需触发——运行中任务不可删除[真机 409]，违规内容不做）。
	// "canceled" 单 L 拼写按别名收编：方言漂移落 default→running 会拖到 30min 超时清扫才退款。
	case "failed", "cancelled", "canceled":
		out.Status = StatusFailed
	default:
		// 未知状态保守按 running 处理（下轮再看），不误杀任务
		out.Status = StatusRunning
	}
	return out, nil
}
