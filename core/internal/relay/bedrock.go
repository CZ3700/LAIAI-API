package relay

// AWS Bedrock 适配器（丝滑度批 C-1，知识移植自 AWS 文档；非真机——env 门控 smoke 待真机）。
//
// 设计选择（与 NewAPI 分歧，更优）：
//   - 用 **Converse API**（/model/<id>/converse[-stream]）——统一 Claude/Llama/Titan/Mistral/Nova
//     全模型家族的请求/响应格式；NewAPI 用 InvokeModel + 每家不同的 body（Claude 格式），零散。
//   - 手写 **SigV4** 签名（sigv4.go，纯 stdlib）——不引 aws-sdk-go-v2 巨型依赖。
//
// 凭证格式：`ak|sk|region` 或 `ak|sk|region|session_token`（临时凭证）。
// model = Bedrock modelId（如 anthropic.claude-3-5-sonnet-20241022-v2:0 / meta.llama3-1-70b-instruct-v1:0）。

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// respModelKey：relay 在 c.Set 里存对外 model 名，供不含 model 的上游响应（bedrock Converse）回显用。
const respModelKey = "laiai_resp_model"

type bedrockAdapter struct{}

// parseBedrockCred：ak|sk|region[|session_token]。
func parseBedrockCred(credential string) (ak, sk, region, token string, err error) {
	parts := strings.Split(credential, "|")
	if len(parts) < 3 {
		return "", "", "", "", fmt.Errorf("bedrock credential must be ak|sk|region[|token]")
	}
	ak, sk, region = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	if len(parts) >= 4 {
		token = strings.TrimSpace(parts[3])
	}
	if ak == "" || sk == "" || region == "" {
		return "", "", "", "", fmt.Errorf("bedrock credential fields must be non-empty")
	}
	return ak, sk, region, token, nil
}

func (bedrockAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	ak, sk, region, token, err := parseBedrockCred(credential)
	if err != nil {
		return nil, err
	}
	// req["model"] 已由 relay 经 ch.UpstreamModel 映射成上游名（relay.go），直接用——
	// 不再二次 UpstreamModel（对抗 review：与其它 adapter 对齐，避免链式映射跳变）。
	modelID := reqModel(req)

	// OpenAI messages → Converse messages（content 数组 {text}）+ system 提顶层。
	var messages []map[string]any
	var system []map[string]any
	if rawMsgs, ok := req["messages"].([]any); ok {
		for _, rm := range rawMsgs {
			m, ok := rm.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			text := bedrockContentText(m["content"])
			switch role {
			case "system":
				if text != "" {
					system = append(system, map[string]any{"text": text})
				}
			case "assistant", "user":
				messages = append(messages, map[string]any{
					"role":    role,
					"content": []any{map[string]any{"text": text}},
				})
			default: // tool/其他角色降级为 user 可见文本（不做工具往返，v1 薄透传）
				if text != "" {
					messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]any{"text": text}}})
				}
			}
		}
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("bedrock: no user/assistant messages")
	}

	body := map[string]any{"messages": messages}
	if len(system) > 0 {
		body["system"] = system
	}
	inf := map[string]any{}
	if v := requestedMaxOutput(req); v > 0 {
		inf["maxTokens"] = v
	} else {
		inf["maxTokens"] = defaultMaxTokens // Bedrock 必须给 maxTokens
	}
	if v, ok := req["temperature"].(float64); ok {
		inf["temperature"] = v
	}
	if v, ok := req["top_p"].(float64); ok {
		inf["topP"] = v
	}
	if stop := bedrockStopSequences(req["stop"]); len(stop) > 0 {
		inf["stopSequences"] = stop
	}
	body["inferenceConfig"] = inf

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	op := "converse"
	if stream {
		op = "converse-stream"
	}
	u := &url.URL{
		Scheme: "https",
		Host:   "bedrock-runtime." + region + ".amazonaws.com",
		Path:   "/model/" + modelID + "/" + op,
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	if stream {
		upReq.Header.Set("Accept", "application/vnd.amazon.eventstream")
	}
	// SigV4 签名（service=bedrock）——就地设 Authorization/X-Amz-Date/(Security-Token)
	sigV4Sign(upReq, "bedrock", region, ak, sk, token, raw, time.Now())
	return upReq, nil
}

// bedrockContentText：OpenAI content（string 或多模态数组）→ 纯文本（v1 只取 text 部件；
// 图像多模态留后续——Converse 支持 image 块，但需 base64+format 转换）。
func bedrockContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok {
						b.WriteString(t)
					}
				}
			}
		}
		return b.String()
	}
	return ""
}

func bedrockStopSequences(stop any) []string {
	switch s := stop.(type) {
	case string:
		if s != "" {
			return []string{s}
		}
	case []any:
		var out []string
		for _, v := range s {
			if str, ok := v.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// reqModel：取请求 model 字段（buildRequest 前已由 relay 覆盖成上游名前的对外名，这里再经 UpstreamModel）。
func reqModel(req map[string]any) string {
	m, _ := req["model"].(string)
	return m
}

// mapBedrockStopReason：Converse stopReason → OpenAI finish_reason。
func mapBedrockStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "content_filtered", "guardrail_intervened":
		return "content_filter"
	default: // end_turn / stop_sequence
		return "stop"
	}
}

// bedrockConverseResp：Converse 非流响应形态。
type bedrockConverseResp struct {
	Output struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	} `json:"usage"`
}

func (bedrockAdapter) pipeJSON(c *gin.Context, body io.Reader) (usage, int, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return usage{}, 0, fmt.Errorf("%w: %v", errStreamReadFail, err)
	}
	var resp bedrockConverseResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.Data(http.StatusOK, "application/json", raw) // 解析失败原样透传
		return usage{}, len(raw), nil
	}
	var text strings.Builder
	for _, blk := range resp.Output.Message.Content {
		text.WriteString(blk.Text)
	}
	finish := mapBedrockStopReason(resp.StopReason)
	c.JSON(http.StatusOK, gin.H{
		"id":     "chatcmpl-bedrock",
		"object": "chat.completion",
		"model":  c.GetString(respModelKey), // 对外 model 名（relay c.Set；Converse 响应本身无 model）
		"choices": []gin.H{{
			"index":         0,
			"message":       gin.H{"role": "assistant", "content": text.String()},
			"finish_reason": finish,
		}},
		"usage": gin.H{
			"prompt_tokens":     resp.Usage.InputTokens,
			"completion_tokens": resp.Usage.OutputTokens,
			"total_tokens":      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	})
	return usage{PromptTokens: resp.Usage.InputTokens, CompletionTokens: resp.Usage.OutputTokens, FinishReason: finish}, len(raw), nil
}

func (bedrockAdapter) pipeSSE(c *gin.Context, body io.Reader) (usage, int, error) {
	sseHeaders(c)
	w := c.Writer
	var u usage
	outBytes := 0
	emit := func(payload any) {
		b, err := json.Marshal(payload)
		if err != nil {
			return
		}
		outBytes += len(b)
		_, _ = w.WriteString("data: " + string(b) + "\n\n")
		w.Flush()
	}
	emit(gin.H{"object": "chat.completion.chunk",
		"choices": []gin.H{{"index": 0, "delta": gin.H{"role": "assistant"}}}})

	finish := "stop"
	err := decodeAWSEventStream(c.Request.Context(), body, func(eventType string, payload []byte) {
		switch eventType {
		case "contentBlockDelta":
			var ev struct {
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
			}
			if json.Unmarshal(payload, &ev) == nil && ev.Delta.Text != "" {
				emit(gin.H{"object": "chat.completion.chunk",
					"choices": []gin.H{{"index": 0, "delta": gin.H{"content": ev.Delta.Text}}}})
			}
		case "messageStop":
			var ev struct {
				StopReason string `json:"stopReason"`
			}
			if json.Unmarshal(payload, &ev) == nil && ev.StopReason != "" {
				finish = mapBedrockStopReason(ev.StopReason)
			}
		case "metadata":
			var ev struct {
				Usage struct {
					InputTokens  int64 `json:"inputTokens"`
					OutputTokens int64 `json:"outputTokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(payload, &ev) == nil {
				u.PromptTokens = ev.Usage.InputTokens
				u.CompletionTokens = ev.Usage.OutputTokens
			}
		}
	})
	if err != nil {
		emitStreamError(w, "upstream stream ended abnormally")
		return u, outBytes, err
	}
	emit(gin.H{"object": "chat.completion.chunk",
		"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": finish}},
		"usage": gin.H{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
			"total_tokens": u.PromptTokens + u.CompletionTokens}})
	_, _ = w.WriteString("data: [DONE]\n\n")
	w.Flush()
	u.FinishReason = finish
	return u, outBytes, nil
}

// decodeAWSEventStream：解析 AWS event stream 二进制帧（converse-stream 用）。
// 帧格式：[totalLen u32][headersLen u32][preludeCRC u32][headers][payload][msgCRC u32]（大端）。
// headers 为 [nameLen u8][name][valueType u8][valueLen u16][value]... —— 我们只取 :event-type
// （valueType=7 字符串）判事件类型，payload 为 JSON。回调 (eventType, payloadJSON)。
func decodeAWSEventStream(ctx context.Context, body io.Reader, onEvent func(eventType string, payload []byte)) error {
	br := body
	prelude := make([]byte, 12)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := io.ReadFull(br, prelude); err != nil {
			if err == io.EOF {
				return nil // 干净帧边界结束（io.ReadFull 只在 0 字节时返 EOF）
			}
			// ErrUnexpectedEOF（读到 1-11 字节后断）= 真截断，不可当完整响应（对抗 review low）
			return err
		}
		totalLen := binary.BigEndian.Uint32(prelude[0:4])
		headersLen := binary.BigEndian.Uint32(prelude[4:8])
		if totalLen < 16 || totalLen > 16<<20 { // 帧合理上界，防坏数据分配巨块
			return fmt.Errorf("bedrock event stream: bad frame length %d", totalLen)
		}
		rest := make([]byte, totalLen-12) // headers + payload + msgCRC(4)
		if _, err := io.ReadFull(br, rest); err != nil {
			return err
		}
		if int(headersLen)+4 > len(rest) {
			return fmt.Errorf("bedrock event stream: bad headers length")
		}
		headers := rest[:headersLen]
		payload := rest[headersLen : len(rest)-4] // 去掉尾部 msgCRC
		eventType, messageType := awsStreamHeaders(headers)
		// 异常/错误帧（:message-type=exception|error，无 :event-type）→ 返回错误，触发 pipeSSE
		// 的 emitStreamError，把上游限流/模型错误如实透出（对抗 review medium：否则截断伪装成功 + usage=0）。
		if messageType == "exception" || messageType == "error" {
			return fmt.Errorf("%w: bedrock stream %s", errUpstreamReported, messageType)
		}
		if eventType != "" && len(payload) > 0 {
			onEvent(eventType, payload)
		}
	}
}

// awsStreamHeaders：从 event stream headers 提取 :event-type 与 :message-type 的字符串值。
// AWS 帧头：[nameLen u8][name][valueType u8][按类型的值]。数据帧带 :event-type + :message-type=event；
// 异常帧带 :message-type=exception + :exception-type（无 :event-type）；错误帧 :message-type=error。
func awsStreamHeaders(headers []byte) (eventType, messageType string) {
	i := 0
	for i < len(headers) {
		nameLen := int(headers[i])
		i++
		if i+nameLen > len(headers) {
			break
		}
		name := string(headers[i : i+nameLen])
		i += nameLen
		if i >= len(headers) {
			break
		}
		valueType := headers[i]
		i++
		switch valueType {
		case 7: // 字符串：[len u16][bytes]
			if i+2 > len(headers) {
				return
			}
			vLen := int(binary.BigEndian.Uint16(headers[i : i+2]))
			i += 2
			if i+vLen > len(headers) {
				return
			}
			val := string(headers[i : i+vLen])
			i += vLen
			switch name {
			case ":event-type":
				eventType = val
			case ":message-type":
				messageType = val
			}
		default:
			// 其它头类型（bool/byte/short/int/long/bytes/timestamp/uuid）——converse-stream 的
			// : 前缀元头都是字符串(7)；遇到非 7 说明进入非关心头或解析越界，止损。
			return
		}
	}
	return
}
