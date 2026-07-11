package relay

// Replicate 生图适配（丝滑度批 C）。协议：POST /v1/models/{model}/predictions（官方模型路径），
// 请求 {"input":{...}}，响应为 prediction 对象（异步任务）。
//   - Prefer: wait=60 让上游最多同步等 60s（flux/sdxl 常规 2~20s 内完）
//   - 超窗未终态 → 平台侧轮询 GET /v1/predictions/{id} 兜底（冷启动/大图可到分钟级）
//   - succeeded → output（string 或 []string 的图 URL）转 OpenAI images {data:[{url|b64_json}]}
//   - failed/canceled → 上游错误（释放 hold，不收费——图像按张计费，失败=0 张）
//
// 入参转换哲学（丝滑优先）：除 OpenAI 路由/计费/形态字段外**全部透传进 input**——客户端可直接
// 带 Replicate 原生参数（seed/guidance/output_format/aspect_ratio/num_inference_steps…），
// 平台不做白名单拦截。n>1 才发 num_outputs（flux-1.1-pro 等单图 schema 会拒未知/超限字段）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// replicateExcludedKeys：不进 input 的 OpenAI 专有字段。model/n/size/response_format 已转换语义；
// quality/style 等是 dall-e/gpt-image 专有旋钮，Replicate 模型 schema 不识别（严格校验会 422）。
// num_outputs/num_images/batch_size 是**张数旋钮**，必须走 n（计费口径）——直接透传会绕过
// 按张 hold/结算：n=1 冻 1 张、input.num_outputs=4 实拿 4 张只收 1 张（对抗 review HIGH，资金红线）。
var replicateExcludedKeys = map[string]bool{
	"model": true, "n": true, "size": true, "response_format": true,
	"stream": true, "user": true, "partial_images": true, "quality": true,
	"style": true, "background": true, "moderation": true, "output_compression": true,
	"num_outputs": true, "num_images": true, "batch_size": true,
}

// replicateAspectRatio：OpenAI size → Replicate aspect_ratio 枚举（flux 系）。
// 未知尺寸不发（用模型默认），绝不发非法枚举招 422。
var replicateAspectRatio = map[string]string{
	"256x256": "1:1", "512x512": "1:1", "1024x1024": "1:1",
	"1536x1024": "3:2", "1024x1536": "2:3",
	"1792x1024": "16:9", "1024x1792": "9:16",
}

// 轮询参数（测试可缩短）。预算对齐 upstreamTimeout 量级但留 HTTP 往返裕度。
var (
	replicatePollInterval = 2 * time.Second
	replicatePollBudget   = 180 * time.Second
)

// buildReplicateImagesRequest：OpenAI images/generations body → Replicate prediction 请求。
func buildReplicateImagesRequest(ctx context.Context, ch *Channel, credential string, rawBody []byte, upModel string) (*http.Request, error) {
	var req map[string]any
	if err := json.Unmarshal(rawBody, &req); err != nil {
		return nil, fmt.Errorf("replicate: bad request body: %w", err)
	}
	input := map[string]any{}
	for k, v := range req {
		if !replicateExcludedKeys[k] {
			input[k] = v // prompt + 客户端自带的 Replicate 原生参数全透传
		}
	}
	if n, ok := req["n"].(float64); ok && n > 1 {
		input["num_outputs"] = n
	}
	if size, ok := req["size"].(string); ok && size != "" {
		if _, has := input["aspect_ratio"]; !has {
			if ar, ok := replicateAspectRatio[size]; ok {
				input["aspect_ratio"] = ar
			}
		}
	}
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(ch.BaseURL, "/")
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/v1/models/"+upModel+"/predictions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+credential)
	upReq.Header.Set("Prefer", "wait=60") // 上游同步等待窗（最大 60）；超窗回 processing，平台轮询兜底
	return upReq, nil
}

// replicatePrediction：prediction 对象子集。
type replicatePrediction struct {
	ID     string `json:"id"`
	Status string `json:"status"` // starting|processing|succeeded|failed|canceled
	Output any    `json:"output"` // string | []string（图 URL）
	Error  any    `json:"error"`
}

func (p *replicatePrediction) terminal() bool {
	switch p.Status {
	case "succeeded", "failed", "canceled":
		return true
	}
	return false
}

// outputURLs：output 字段 → URL 列表（string 或 []any）。
func (p *replicatePrediction) outputURLs() []string {
	switch o := p.Output.(type) {
	case string:
		if o != "" {
			return []string{o}
		}
	case []any:
		var urls []string
		for _, v := range o {
			if s, ok := v.(string); ok && s != "" {
				urls = append(urls, s)
			}
		}
		return urls
	}
	return nil
}

// replicateFinalizeImages：prediction 原始响应 → OpenAI images JSON。
// 未终态时轮询 GET {base}/v1/predictions/{id}（pollClient=主转发同款 clientFor——URL 由
// 我方从 base_url 构造，不信 prediction.urls）；wantB64 时经 dlClient（**必须 SSRF guarded**，
// 图 URL 来自上游响应体，veo 产物代理同款先例）下载转 base64。
// maxImages = 计费口径 n：交付张数封顶到 n（与 settle 钳制对齐——交付即所收，杜绝
// 未知张数别名旋钮"多拿少付"，对抗 review HIGH 的纵深）。
// 瞬时轮询失败（网络错/429/5xx/坏体）在预算内容错重试（对抗 review MEDIUM——180s ~90 次
// GET 零容错会让一次抖动击杀已付费的成功生成）；仅确定性 4xx 与预算耗尽终止。
// 返回错误 = 上游失败：调用方释放 hold、不收费。
func replicateFinalizeImages(ctx context.Context, pollClient, dlClient *http.Client, base, credential string, raw []byte, wantB64 bool, maxImages int, now func() time.Time) ([]byte, error) {
	var p replicatePrediction
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("replicate: response not a prediction object")
	}
	deadline := now().Add(replicatePollBudget)
	var lastPollErr error
	for !p.terminal() {
		if p.ID == "" {
			return nil, fmt.Errorf("replicate: non-terminal prediction without id")
		}
		if now().After(deadline) {
			return nil, fmt.Errorf("replicate: prediction %s not finished within poll budget (last poll err: %v)", p.ID, lastPollErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(replicatePollInterval):
		}
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
			strings.TrimRight(base, "/")+"/v1/predictions/"+p.ID, nil)
		if err != nil {
			return nil, err
		}
		pollReq.Header.Set("Authorization", "Bearer "+credential)
		resp, err := pollClient.Do(pollReq)
		if err != nil {
			lastPollErr = err // 瞬时网络错误：预算内下一轮再试
			continue
		}
		pollBody, rerr := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodySize))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastPollErr = fmt.Errorf("poll HTTP %d", resp.StatusCode) // 瞬时限流/5xx：重试
			continue
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("replicate: poll HTTP %d", resp.StatusCode) // 确定性 4xx（401/403/404）：终止
		case rerr != nil:
			lastPollErr = fmt.Errorf("poll body read: %w", rerr)
			continue
		}
		if err := json.Unmarshal(pollBody, &p); err != nil {
			lastPollErr = fmt.Errorf("poll body not a prediction object")
			continue
		}
	}
	if p.Status != "succeeded" {
		msg := ""
		if p.Error != nil {
			msg = fmt.Sprintf("%v", p.Error)
		}
		return nil, fmt.Errorf("replicate: prediction %s: %s", p.Status, gcpTruncate(msg, 300))
	}
	urls := p.outputURLs()
	if len(urls) == 0 {
		return nil, fmt.Errorf("replicate: succeeded but empty output")
	}
	if maxImages > 0 && len(urls) > maxImages {
		urls = urls[:maxImages] // 交付 = 计费口径（settle 也钳到 n，两边对齐）
	}
	data := make([]map[string]any, 0, len(urls))
	for _, u := range urls {
		if wantB64 {
			b64, err := replicateDownloadB64(ctx, dlClient, u)
			if err != nil { // 已成功生成的图因单次下载抖动全盘 502 太脆：重试一次
				b64, err = replicateDownloadB64(ctx, dlClient, u)
			}
			if err != nil {
				return nil, err
			}
			data = append(data, map[string]any{"b64_json": b64})
		} else {
			data = append(data, map[string]any{"url": u})
		}
	}
	return json.Marshal(map[string]any{"created": now().Unix(), "data": data})
}

// replicateDownloadB64：下载产物图转 base64（response_format=b64_json 保真——replicate.delivery
// 链接会过期，b64 语义是"给我字节"）。单图 32MB 上限；client 必须带 SSRF 守卫。
func replicateDownloadB64(ctx context.Context, client *http.Client, imgURL string) (string, error) {
	if !strings.HasPrefix(imgURL, "https://") && !strings.HasPrefix(imgURL, "http://") {
		return "", fmt.Errorf("replicate: non-http output url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("replicate: image download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("replicate: image download HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", fmt.Errorf("replicate: image download read failed: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
