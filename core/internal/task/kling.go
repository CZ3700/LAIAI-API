package task

// Kling（快手可灵）视频生成任务 adaptor。
// 知识移植自 New API 参考实现 relay/channel/task/kling/adaptor.go（字段映射/状态枚举/计费通道
// 均按其生产验证过的解析结构体移植——非我方真机捕获，真机验证走 env 门控 smoke）。
// 鉴权：JWT-HS256（access_key|secret_key 现签，纯 stdlib，无云 SDK）。
// 计费体制：A 类——查询响应带 final_unit_deduction（上游实际扣费单位），ParseQuery 用它精确结算；
//          EstimateOutputTokens 是上界（mode×duration 表；未知取最高档，capture>hold 是红线）。
// 伪 token 约定：1,000,000 伪 token = 1 个 kling 计费单位；output_usd_per_1m = 每单位美元价。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/relay"
)

type klingTask struct{}

// —— action 编码进 upstreamTaskID（接口缺口①的约定，设计 §4.4）——
// kling 查询 URL 依赖提交时的 action（image2video/text2video），而 QueryRequest 只拿 task id；
// 故 ParseSubmit 返回 "<action>:<task_id>"，QueryRequest 解回。落库的 upstream_task_id 自带路由。
const (
	klingActionImage2Video = "image2video"
	klingActionText2Video  = "text2video"
)

func klingEncodeTaskID(action, id string) string { return action + ":" + id }

func klingDecodeTaskID(composite string) (action, id string) {
	if i := strings.IndexByte(composite, ':'); i >= 0 {
		return composite[:i], composite[i+1:]
	}
	return klingActionImage2Video, composite // 兼容裸 id（历史/异常）：按默认 action
}

// klingJWT：HS256 JWT（claims iss=ak / exp=now+30min / nbf=now-5s；header 显式 typ=JWT）。
// 纯 stdlib HMAC-SHA256——不引 golang-jwt。now 显式入参供测试固定。
func klingJWT(ak, sk string, now time.Time) (string, error) {
	if ak == "" || sk == "" {
		return "", fmt.Errorf("kling: empty access_key/secret_key")
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iss": ak,
		"exp": now.Add(30 * time.Minute).Unix(),
		"nbf": now.Add(-5 * time.Second).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + b64(claims)
	mac := hmac.New(sha256.New, []byte(sk))
	mac.Write([]byte(signingInput))
	return signingInput + "." + b64(mac.Sum(nil)), nil
}

// klingCredential：拆 access_key|secret_key。错误信息绝不回显凭证内容（红线 7）。
func klingCredential(credential string) (ak, sk string, err error) {
	parts := strings.Split(credential, "|")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("kling: credential must be access_key|secret_key")
	}
	ak, sk = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if ak == "" || sk == "" {
		return "", "", fmt.Errorf("kling: access_key/secret_key must be non-empty")
	}
	return ak, sk, nil
}

func klingBearer(credential string, now time.Time) (string, error) {
	ak, sk, err := klingCredential(credential)
	if err != nil {
		return "", err
	}
	return klingJWT(ak, sk, now)
}

// seedanceResolutionPixels 之外，kling 的计费档：mode(std|pro) × duration(5|10s) 的单位数上界表。
// New API 无此表（它靠 final_unit_deduction 事后结算）；LAIAI 提交时须预估 hold 上界——按官方计价
// pro≈2×std、10s≈2×5s，以 std/5s=1 单位为基。未知 mode/duration 取最高档（pro/10s=4）。
func klingEstimateUnits(params map[string]any) float64 {
	mode := "std"
	if m, ok := params["mode"].(string); ok && m != "" {
		mode = strings.ToLower(m)
	}
	const maxUnits = 16.0 // 最高档 pro×长时长（4×2×2）
	// duration 可能是字符串 "5"/"10"（kling 方言）或数字
	dur := 5.0
	switch v := params["duration"].(type) {
	case string:
		if f, e := strconv.ParseFloat(v, 64); e == nil && f > 0 {
			dur = f
		} else {
			return maxUnits // 畸形：取最高档（A 类上界安全，绝不低估）
		}
	case float64:
		if v > 0 {
			dur = v
		}
	}
	// 基准放宽（对抗 review CONFIRMED：hold 须真上界，final_unit_deduction 无先验封顶）：
	// std/5s 基准取 4 单位而非 1——真实 kling 单任务扣费单位普遍 >1，配合 settle 的 cap-at-hold
	// 双保险（task.go settleSuccess 已封顶不超 hold），既避系统性少收又绝不透支。真机 smoke 待标定。
	units := 4.0
	if mode == "pro" {
		units *= 2
	} else if mode != "std" {
		units *= 2 // 未知 mode：按 pro 上界
	}
	if dur > 5 { // 5<dur（含 10s）按更长档上界
		units *= 2
	}
	return units
}

func (klingTask) EstimateOutputTokens(model string, content []any, params map[string]any) int64 {
	return int64(klingEstimateUnits(params) * 1_000_000)
}

// klingSubmitBody：请求体。prompt/image 从 OpenAI content 数组提取（对抗 review CONFIRMED：
// 此前只读 Params 导致 prompt 丢失），Params 显式值优先覆盖（power-user 逃生阀），
// 平台补默认值 + model/model_name 双写。
func klingSubmitBody(in SubmitInput) ([]byte, string) {
	prompt, images := taskContentParts(in.Content)
	payload := map[string]any{}
	if prompt != "" {
		payload["prompt"] = prompt
	}
	if len(images) > 0 {
		payload["image"] = images[0] // kling 单图（首帧）；多图/尾帧走 Params 显式传
	}
	for k, v := range in.Params { // Params 覆盖 content 提取值
		payload[k] = v
	}
	model := in.Model
	payload["model_name"] = model // 官方认 model_name
	payload["model"] = model      // 部分兼容上游只认 model（双写同值）
	if _, ok := payload["mode"]; !ok {
		payload["mode"] = "std"
	}
	if _, ok := payload["duration"]; !ok {
		payload["duration"] = "5" // kling 方言：duration 是字符串
	}
	// action：有 image（或 image_tail）→ image2video，否则 text2video（与查询 URL 分支一致）
	action := klingActionText2Video
	if s, _ := payload["image"].(string); s != "" {
		action = klingActionImage2Video
	} else if s, _ := payload["image_tail"].(string); s != "" {
		action = klingActionImage2Video
	}
	body, _ := json.Marshal(payload)
	return body, action
}

func (klingTask) SubmitRequest(ctx context.Context, ch *relay.Channel, credential string, in SubmitInput) (*http.Request, error) {
	token, err := klingBearer(credential, time.Now())
	if err != nil {
		return nil, err
	}
	body, action := klingSubmitBody(in)
	url := strings.TrimRight(ch.BaseURL, "/") + "/v1/videos/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	_ = action // action 已编码进 URL 末段；ParseSubmit 从 resp.Request.URL 还原（不发内部头给上游）
	return req, nil
}

// klingActionFromPath：从提交 URL 末段还原 action（/v1/videos/<action>）。
func klingActionFromPath(path string) string {
	if strings.HasSuffix(path, "/"+klingActionText2Video) {
		return klingActionText2Video
	}
	return klingActionImage2Video // 默认与 SubmitRequest 一致
}

// klingSubmitResp：提交响应（code==0 成功，data.task_id）。
type klingSubmitResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		TaskID string `json:"task_id"`
	} `json:"data"`
}

func (klingTask) ParseSubmit(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("kling: submit status %d", resp.StatusCode)
	}
	var parsed klingSubmitResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("kling: submit parse: %w", err)
	}
	// HTTP 200 但 code!=0 是业务失败。仅参数/内容类判客户端短路 failover；
	// 余额/限流/鉴权/资源包等渠道侧故障走换渠道（对抗 review 修）。
	if parsed.Code != 0 {
		if submitErrorIsClientFault(parsed.Message) {
			return "", &UpstreamRejectedError{StatusCode: http.StatusBadRequest, Message: parsed.Message}
		}
		return "", fmt.Errorf("kling: submit failed upstream (code=%d)", parsed.Code)
	}
	if parsed.Data.TaskID == "" {
		return "", fmt.Errorf("kling: submit response missing task id")
	}
	// action 从提交请求 URL 末段还原（resp.Request 是发出的请求，URL 已含 /image2video|/text2video）
	action := klingActionImage2Video
	if resp.Request != nil && resp.Request.URL != nil {
		action = klingActionFromPath(resp.Request.URL.Path)
	}
	return klingEncodeTaskID(action, parsed.Data.TaskID), nil
}

func (klingTask) QueryRequest(ctx context.Context, ch *relay.Channel, credential, upstreamTaskID string) (*http.Request, error) {
	token, err := klingBearer(credential, time.Now())
	if err != nil {
		return nil, err
	}
	action, id := klingDecodeTaskID(upstreamTaskID)
	url := strings.TrimRight(ch.BaseURL, "/") + "/v1/videos/" + action + "/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

// klingQueryResp：查询响应。task_status ∈ {submitted,processing,succeed,failed}（注意 succeed 无 ed）。
type klingQueryResp struct {
	Code int `json:"code"`
	Data struct {
		TaskStatus    string `json:"task_status"`
		TaskStatusMsg string `json:"task_status_msg"`
		TaskResult    struct {
			Videos []struct {
				URL      string `json:"url"`
				Duration string `json:"duration"`
			} `json:"videos"`
		} `json:"task_result"`
		FinalUnitDeduction string `json:"final_unit_deduction"` // 字符串浮点，上游实耗单位数
	} `json:"data"`
}

func (klingTask) ParseQuery(resp *http.Response) (QueryResult, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QueryResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueryResult{}, fmt.Errorf("kling: query status %d", resp.StatusCode)
	}
	var parsed klingQueryResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("kling: query parse: %w", err)
	}
	out := QueryResult{FailReason: parsed.Data.TaskStatusMsg}
	// 方言：成功态字面量是 "succeed"（非 succeeded/success），全小写；无 queued 态。
	switch parsed.Data.TaskStatus {
	case "submitted":
		out.Status = StatusQueued
	case "processing":
		out.Status = StatusRunning
	case "succeed":
		out.Status = StatusSucceeded
		if len(parsed.Data.TaskResult.Videos) > 0 {
			out.ResultURL = parsed.Data.TaskResult.Videos[0].URL // 多产物只取 [0]
		}
		// A 类结算：final_unit_deduction（字符串浮点）→ ceil → ×1M 伪 token；解析失败回 0 走估算
		if f, e := strconv.ParseFloat(parsed.Data.FinalUnitDeduction, 64); e == nil && f > 0 {
			out.CompletionTokens = int64(math.Ceil(f)) * 1_000_000
		}
	case "failed":
		out.Status = StatusFailed
	default:
		out.Status = StatusRunning // 未知状态兜底（有意偏离 New API 的硬报错，绝不误杀）
	}
	return out, nil
}
