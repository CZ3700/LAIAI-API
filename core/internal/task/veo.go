package task

// Google Veo（Gemini API LRO）视频生成任务 adaptor。
// 知识移植自 New API 参考实现 relay/channel/task/gemini/adaptor.go + video_proxy_gemini.go
// （LRO 状态推导/5 种产物 URI 方言/RAI 过滤/计费系数按其生产代码移植——非我方真机捕获）。
// 鉴权：trivial——x-goog-api-key 头（提交/轮询/产物下载同一 key）。
// 计费体制：B 类——LRO 响应无用量字段；EstimateOutputTokens 是最终收费
//          （seconds × resolutionRatio，系数表移植自参考实现 billing.go）。
// 伪 token 约定：1M 伪 token = 1 秒 × 720p 档；output_usd_per_1m = 720p 档每秒美元价。
// 产物：URI 指向 files 下载端点，**带渠道 key 且会过期——绝不直接吐给用户（=泄露凭证）**，
//      经平台代理端点下发（ContentStreamer 用 x-goog-api-key 头取流）。
// 本批支持文生视频（prompt）；图生（base64 内嵌）与 referenceImages 留后续。

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

type veoTask struct{}

var _ ContentStreamer = veoTask{} // 编译期断言：veo 产物经平台代理下发

// veoResolutionRatio：resolution × 模型 → 计费倍率（720p/1080p=1；4k 按模型分档，
// 移植自参考实现 billing.go:123-138 的 Vertex 官方定价 video+audio 档）。
func veoResolutionRatio(model, resolution string) float64 {
	if strings.ToLower(resolution) != "4k" {
		return 1.0 // 720p/1080p 同价档
	}
	switch {
	case strings.Contains(model, "veo-3.1-fast"):
		return 2.333333
	case strings.Contains(model, "veo-3.1"):
		return 1.5
	}
	return 2.333333 // 未知模型的 4K：按高档（B 类此处宁高——4K 是显式参数非默认，且防低收）
}

// veoSeconds：时长回退链 durationSeconds > duration(数字) > seconds(字符串) > 默认 8。
func veoSeconds(params map[string]any) (float64, bool) {
	if v, ok := params["durationSeconds"].(float64); ok && v > 0 {
		return v, true
	}
	if v, ok := params["duration"].(float64); ok && v > 0 {
		return v, true
	}
	if v, exists := params["seconds"]; exists {
		switch s := v.(type) {
		case string: // sora 风格字符串 "8"
			if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
				return f, true
			}
		case float64: // JSON number 8（对抗 review CONFIRMED：此前只收字符串→数字被判畸形上界多收）
			if s > 0 {
				return s, true
			}
		}
		return 0, false // 畸形（含带单位的 "8s"）
	}
	return 8, true // 上游默认 8s
}

func (veoTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	sec, ok := veoSeconds(params)
	if !ok {
		return 30_000_000 // 畸形参数上界（B 类畸形本应拒绝；上游会校验，多冻结结算即退）
	}
	res, _ := params["resolution"].(string)
	return int64(sec * veoResolutionRatio(model, res) * 1_000_000)
}

func (veoTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	prompt, _ := taskContentParts(in.Content)
	instance := map[string]any{}
	if prompt != "" {
		instance["prompt"] = prompt
	}
	parameters := map[string]any{}
	for k, v := range in.Params {
		switch k {
		case "prompt", "image":
			instance[k] = v // instance 段字段（image 须 {bytesBase64Encoded,mimeType} 形态，显式透传）
		case "duration", "seconds": // 统一时长方言 → durationSeconds（不透传原 key）
			if sec, ok := veoSeconds(in.Params); ok {
				parameters["durationSeconds"] = int(sec)
			}
		case "resolution":
			if s, ok := v.(string); ok {
				parameters[k] = strings.ToLower(s) // 上游只认小写 720p/1080p/4k
			}
		default:
			parameters[k] = v
		}
	}
	// sampleCount 恒 1（对抗 review CONFIRMED）：在透传之后强制覆盖——否则客户端传 sampleCount:4
	// 会经 default 分支顶掉，上游出 4 条视频但计费只按 1 条（4× 少收）。计费模型假设单产物。
	parameters["sampleCount"] = 1
	payload := map[string]any{"instances": []any{instance}, "parameters": parameters}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// base_url 含版本段（档案默认 https://generativelanguage.googleapis.com/v1beta）
	url := strings.TrimRight(ch.BaseURL, "/") + "/models/" + in.Model + ":predictLongRunning"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-goog-api-key", credential)
	return req, nil
}

func (veoTask) ParseSubmit(resp *http.Response) (string, error) {
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
		return "", fmt.Errorf("veo: submit status %d", resp.StatusCode)
	}
	var parsed struct {
		Name string `json:"name"` // LRO operation name（models/{m}/operations/{id}，含斜杠）
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("veo: submit parse: %w", err)
	}
	if parsed.Name == "" {
		return "", fmt.Errorf("veo: submit response missing operation name")
	}
	return parsed.Name, nil // 完整 operation name 即任务 id（天然自路由）
}

func (veoTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	// 查询 URL 直接拼完整 operation name（含 models/.../operations/... 路径）
	url := strings.TrimRight(ch.BaseURL, "/") + "/" + strings.TrimLeft(upstreamTaskID, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-goog-api-key", credential)
	return req, nil
}

// veoLRO：LRO 响应。无状态枚举——done:bool + error 推导；产物 URI 5 种方言全兜底。
type veoLRO struct {
	Done  bool `json:"done"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Response struct {
		GenerateVideoResponse struct {
			GeneratedVideos []struct {
				Video struct {
					URI string `json:"uri"`
				} `json:"video"`
			} `json:"generatedVideos"`
			GeneratedSamples []struct { // 旧版字段名方言
				Video struct {
					URI string `json:"uri"`
				} `json:"video"`
			} `json:"generatedSamples"`
			RaiMediaFilteredCount int `json:"raiMediaFilteredCount"`
		} `json:"generateVideoResponse"`
		Videos []struct {
			URI string `json:"uri"`
		} `json:"videos"`
		Video string `json:"video"`
		URI   string `json:"uri"`
	} `json:"response"`
	URI string `json:"uri"` // 顶层方言
}

// veoExtractURI：5 种产物方言逐一兜底（新→旧）。
func (l *veoLRO) extractURI() string {
	gv := l.Response.GenerateVideoResponse
	if len(gv.GeneratedVideos) > 0 && gv.GeneratedVideos[0].Video.URI != "" {
		return gv.GeneratedVideos[0].Video.URI
	}
	if len(gv.GeneratedSamples) > 0 && gv.GeneratedSamples[0].Video.URI != "" {
		return gv.GeneratedSamples[0].Video.URI
	}
	if len(l.Response.Videos) > 0 && l.Response.Videos[0].URI != "" {
		return l.Response.Videos[0].URI
	}
	if l.Response.Video != "" {
		return l.Response.Video
	}
	if l.Response.URI != "" {
		return l.Response.URI
	}
	return l.URI
}

func (veoTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("veo: query status %d", resp.StatusCode)
	}
	var parsed veoLRO
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("veo: query parse: %w", err)
	}
	// 状态推导（LRO 无枚举）：error.message 非空即失败（优先于 done）；done=false 进行中。
	if parsed.Error != nil && parsed.Error.Message != "" {
		return QueryResult{Status: StatusFailed, FailReason: parsed.Error.Message}, nil
	}
	if !parsed.Done {
		return QueryResult{Status: StatusRunning}, nil
	}
	uri := parsed.extractURI()
	if uri == "" {
		// done=true 但无产物：RAI 内容过滤的"空成功"按失败处理（否则空 URL 重试到超时才退款）
		if parsed.Response.GenerateVideoResponse.RaiMediaFilteredCount > 0 {
			return QueryResult{Status: StatusFailed, FailReason: "content filtered by safety system"}, nil
		}
		// 无过滤计数的无 URI done：方言未覆盖？保守重试（框架空 URL 守卫 + 超时兜底）
		return QueryResult{Status: StatusSucceeded}, nil
	}
	// URI 带渠道 key 且会过期——进 ResultRef 走平台代理，绝不直接暴露
	return QueryResult{Status: StatusSucceeded, ResultRef: uri}, nil
}

// StreamResult：用 x-goog-api-key 头取产物流（URI 裸访问 403）。Content-Type 透传。
// 错误绝不回显 uri：URI 带渠道 key（含 GCS 预签名 query token），*url.Error 会带出完整 URL（红线 7）。
func (veoTask) StreamResult(ctx context.Context, client *http.Client, ch *relay.Channel, credential, uri string) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, "", fmt.Errorf("veo: bad content uri")
	}
	req.Header.Set("x-goog-api-key", credential)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("veo: content fetch failed") // 不带 err（*url.Error 含带 key 的 URL）
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("veo: content status %d", resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}
