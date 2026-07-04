package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// openaiResponsesAdapter：上游只支持 OpenAI Responses API（/v1/responses）时的适配器
// （如 Codex 系模型、只暴露 Responses 的聚合网关）。对内仍是 OpenAI chat 契约：
//   - buildRequest：chat 请求 → Responses 请求，POST <base>/responses。
//   - pipeJSON/pipeSSE：Responses 响应/事件流 → chat 完成/chunk 流（+ [DONE]）。
//
// 运营侧把"只支持 Responses 的上游"渠道 provider 标为 openai-responses 家族即完成
// "平台自动选择转换链"（指令 §3.2 验收：只配 Responses 上游时 chat 客户端仍能用）。
// 与入站 /v1/responses 是精确镜像：入站是 Responses客户端→chat上游，出站是 chat客户端→Responses上游。
type openaiResponsesAdapter struct{}

func (openaiResponsesAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	body, err := json.Marshal(chatToResponsesRequest(req, stream))
	if err != nil {
		return nil, err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(ch.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+credential)
	return upReq, nil
}

// chatToResponsesRequest：OpenAI chat 请求 → Responses 请求。
func chatToResponsesRequest(req map[string]any, stream bool) map[string]any {
	out := map[string]any{"model": req["model"], "store": false}
	if stream {
		out["stream"] = true
	}
	// max_tokens / max_completion_tokens → max_output_tokens（取较大者）
	var maxOut float64
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		if v, ok := req[k].(float64); ok && v > maxOut {
			maxOut = v
		}
	}
	if maxOut > 0 {
		out["max_output_tokens"] = maxOut
	}
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}
	// reasoning_effort → reasoning:{effort, summary:"auto"}
	if eff, ok := req["reasoning_effort"].(string); ok && eff != "" {
		out["reasoning"] = map[string]any{"effort": eff, "summary": "auto"}
	}
	// response_format → text.format（json_schema 展平 name/schema/strict 进 format 顶层）
	if rf, ok := req["response_format"].(map[string]any); ok {
		if tf := chatResponseFormatToTextFormat(rf); tf != nil {
			out["text"] = map[string]any{"format": tf}
		}
	}

	// messages → instructions（system/developer）+ input 数组
	var instructions []string
	input := make([]any, 0, 4)
	msgs, _ := req["messages"].([]any)
	for _, rm := range msgs {
		m, ok := rm.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "system", "developer":
			if s := chatMessageText(m["content"]); s != "" {
				instructions = append(instructions, s)
			}
		case "tool", "function":
			callID, _ := m["tool_call_id"].(string)
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": callID,
				"output": chatMessageText(m["content"]),
			})
		case "assistant":
			if content := chatContentToResponsesParts(m["content"], "output_text"); content != nil {
				input = append(input, map[string]any{"role": "assistant", "content": content})
			}
			// tool_calls → function_call items（紧跟 assistant 消息，保序）
			if tcs, ok := m["tool_calls"].([]any); ok {
				for _, rtc := range tcs {
					tc, ok := rtc.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tc["function"].(map[string]any)
					if fn == nil {
						continue
					}
					args, _ := fn["arguments"].(string)
					if args == "" {
						args = "{}"
					}
					input = append(input, map[string]any{
						"type": "function_call", "call_id": tc["id"],
						"name": fn["name"], "arguments": args,
					})
				}
			}
		default: // user
			if content := chatContentToResponsesParts(m["content"], "input_text"); content != nil {
				input = append(input, map[string]any{"role": role, "content": content})
			}
		}
	}
	if len(instructions) > 0 {
		out["instructions"] = strings.Join(instructions, "\n\n")
	}
	out["input"] = input

	// tools：chat 嵌套 → Responses 扁平
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		var rts []map[string]any
		for _, rt := range tools {
			t, ok := rt.(map[string]any)
			if !ok || t["type"] != "function" {
				continue
			}
			fn, _ := t["function"].(map[string]any)
			if fn == nil {
				continue
			}
			flat := map[string]any{"type": "function", "name": fn["name"]}
			if d, ok := fn["description"]; ok {
				flat["description"] = d
			}
			if p, ok := fn["parameters"]; ok {
				flat["parameters"] = p
			}
			rts = append(rts, flat)
		}
		if len(rts) > 0 {
			out["tools"] = rts
		}
	}
	// tool_choice：字符串直传；{type:function,function:{name}} → {type:function,name}
	switch tc := req["tool_choice"].(type) {
	case string:
		out["tool_choice"] = tc
	case map[string]any:
		if tc["type"] == "function" {
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok && name != "" {
					out["tool_choice"] = map[string]any{"type": "function", "name": name}
				}
			}
		}
	}
	return out
}

// chatResponseFormatToTextFormat：chat response_format → Responses text.format。
func chatResponseFormatToTextFormat(rf map[string]any) map[string]any {
	rtype, _ := rf["type"].(string)
	switch rtype {
	case "json_schema":
		out := map[string]any{"type": "json_schema"}
		if js, ok := rf["json_schema"].(map[string]any); ok {
			for _, k := range []string{"name", "schema", "strict", "description"} {
				if v, ok := js[k]; ok {
					out[k] = v
				}
			}
		}
		return out
	case "json_object", "text":
		return map[string]any{"type": rtype}
	}
	return nil
}

// chatMessageText：chat message.content（string | parts）→ 纯文本（拼接 text parts）。
func chatMessageText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, raw := range c {
			if p, ok := raw.(map[string]any); ok {
				if t, ok := p["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

// chatContentToResponsesParts：chat message.content → Responses content parts（textType=input_text|output_text）。
// 纯文本返回 [{type:textType,text}]；含图像返回混合 parts；nil content 返回 nil（跳过该消息）。
func chatContentToResponsesParts(content any, textType string) []any {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []any{map[string]any{"type": textType, "text": c}}
	case []any:
		var parts []any
		for _, raw := range c {
			p, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch p["type"] {
			case "text":
				parts = append(parts, map[string]any{"type": textType, "text": p["text"]})
			case "image_url":
				if iu, ok := p["image_url"].(map[string]any); ok {
					img := map[string]any{"type": "input_image", "image_url": iu["url"]}
					if d, ok := iu["detail"].(string); ok && d != "" {
						img["detail"] = d
					}
					parts = append(parts, img)
				}
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return parts
	}
	return nil
}

// ════════════════════════ 非流式：Responses 响应 → chat 完成 ════════════════════════

func (openaiResponsesAdapter) pipeJSON(c *gin.Context, body io.Reader) (usage, int, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return usage{}, 0, fmt.Errorf("%w: %v", errStreamReadFail, err)
	}
	var resp struct {
		ID                string          `json:"id"`
		Created           int64           `json:"created_at"`
		Model             string          `json:"model"`
		Output            []responsesItem `json:"output"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage *struct {
			InputTokens        int64 `json:"input_tokens"`
			OutputTokens       int64 `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		c.Data(http.StatusOK, "application/json", raw)
		return usage{}, len(raw), nil
	}

	var text strings.Builder
	var toolCalls []gin.H
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		case "function_call":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			if item.Name == "" {
				continue // 无名工具调用跳过（防污染 chat tool_calls）
			}
			args := item.Arguments
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, gin.H{
				"id": callID, "type": "function",
				"function": gin.H{"name": item.Name, "arguments": args},
			})
		}
	}
	message := gin.H{"role": "assistant", "content": text.String()}
	finish := "stop"
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		finish = "tool_calls"
		if text.Len() == 0 {
			message["content"] = nil
		}
	} else if resp.IncompleteDetails != nil { // 截断（工具调用优先，故在 else 分支）
		switch resp.IncompleteDetails.Reason {
		case "max_output_tokens":
			finish = "length"
		case "content_filter":
			finish = "content_filter"
		}
	}

	var u usage
	if resp.Usage != nil {
		u.PromptTokens = resp.Usage.InputTokens
		u.CompletionTokens = resp.Usage.OutputTokens
		u.PromptTokensDetails.CachedTokens = resp.Usage.InputTokensDetails.CachedTokens
	}
	chatID := "chatcmpl-" + strings.TrimPrefix(resp.ID, "resp_")
	c.JSON(http.StatusOK, gin.H{
		"id": chatID, "object": "chat.completion", "created": resp.Created, "model": resp.Model,
		"choices": []gin.H{{"index": 0, "message": message, "finish_reason": finish}},
		"usage": gin.H{
			"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
			"total_tokens":          u.PromptTokens + u.CompletionTokens,
			"prompt_tokens_details": gin.H{"cached_tokens": u.PromptTokensDetails.CachedTokens},
		},
	})
	return u, len(raw), nil
}

type responsesItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// ════════════════════════ 流式：Responses 事件流 → chat chunk 流 ════════════════════════

func (openaiResponsesAdapter) pipeSSE(c *gin.Context, body io.Reader) (usage, int, error) {
	sseHeaders(c)
	w := c.Writer

	var u usage
	outBytes := 0
	emit := func(payload any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		outBytes += len(raw)
		_, _ = w.WriteString("data: " + string(raw) + "\n\n")
		w.Flush()
	}

	sentRole := false
	finishReason := "stop"
	toolByItem := map[string]int{} // Responses function_call item_id → chat tool_calls index
	nextToolIdx := 0
	streamModel := ""
	streamID := ""
	failMsg := "" // 非空 = 上游发过 response.failed（记 upstream_error，不合成正常收尾）

	err := pumpLines(c.Request.Context(), body, func(line string) {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "" {
			return // event: 行/空行忽略（事件类型靠 JSON type 字段判定）
		}
		var ev struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			ItemID   string `json:"item_id"`
			Item     *responsesItem `json:"item"`
			Response *struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage *struct {
					InputTokens        int64 `json:"input_tokens"`
					OutputTokens       int64 `json:"output_tokens"`
					InputTokensDetails struct {
						CachedTokens int64 `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return
		}
		if ev.Response != nil {
			if ev.Response.ID != "" {
				streamID = "chatcmpl-" + strings.TrimPrefix(ev.Response.ID, "resp_")
			}
			if ev.Response.Model != "" {
				streamModel = ev.Response.Model
			}
		}
		roleChunk := func() {
			if sentRole {
				return
			}
			sentRole = true
			emit(gin.H{"id": streamID, "object": "chat.completion.chunk", "model": streamModel,
				"choices": []gin.H{{"index": 0, "delta": gin.H{"role": "assistant"}}}})
		}

		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				roleChunk()
				emit(gin.H{"id": streamID, "object": "chat.completion.chunk", "model": streamModel,
					"choices": []gin.H{{"index": 0, "delta": gin.H{"content": ev.Delta}}}})
			}
		case "response.reasoning_summary_text.delta":
			if ev.Delta != "" {
				roleChunk()
				emit(gin.H{"id": streamID, "object": "chat.completion.chunk", "model": streamModel,
					"choices": []gin.H{{"index": 0, "delta": gin.H{"reasoning_content": ev.Delta}}}})
			}
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" && ev.Item.Name != "" {
				roleChunk()
				idx := nextToolIdx
				nextToolIdx++
				toolByItem[ev.Item.ID] = idx
				callID := ev.Item.CallID
				if callID == "" {
					callID = ev.Item.ID
				}
				finishReason = "tool_calls"
				emit(gin.H{"id": streamID, "object": "chat.completion.chunk", "model": streamModel,
					"choices": []gin.H{{"index": 0, "delta": gin.H{"tool_calls": []gin.H{{
						"index": idx, "id": callID, "type": "function",
						"function": gin.H{"name": ev.Item.Name, "arguments": ""},
					}}}}}})
			}
		case "response.function_call_arguments.delta":
			if ev.Delta != "" {
				idx, ok := toolByItem[ev.ItemID]
				if !ok {
					idx = 0
				}
				emit(gin.H{"id": streamID, "object": "chat.completion.chunk", "model": streamModel,
					"choices": []gin.H{{"index": 0, "delta": gin.H{"tool_calls": []gin.H{{
						"index": idx, "function": gin.H{"arguments": ev.Delta},
					}}}}}})
			}
		case "response.completed", "response.incomplete":
			// 两者都是终态且都携带上游权威 usage——incomplete（命中 max_output_tokens/内容过滤）
			// 的 usage 同样必须采信，否则推理模型截断响应会漏读 reasoning tokens 按估算少收（收入泄漏）。
			if ev.Response != nil && ev.Response.Usage != nil {
				u.PromptTokens = ev.Response.Usage.InputTokens
				u.CompletionTokens = ev.Response.Usage.OutputTokens
				u.PromptTokensDetails.CachedTokens = ev.Response.Usage.InputTokensDetails.CachedTokens
			}
			// incomplete → chat finish_reason 映射（工具调用优先，不覆盖）
			if ev.Type == "response.incomplete" && finishReason != "tool_calls" && ev.Response != nil && ev.Response.IncompleteDetails != nil {
				switch ev.Response.IncompleteDetails.Reason {
				case "max_output_tokens":
					finishReason = "length"
				case "content_filter":
					finishReason = "content_filter"
				}
			}
		case "response.failed":
			msg := "upstream reported failure"
			if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
				msg = ev.Response.Error.Message
			}
			emitStreamError(w, msg) // 写 error 帧 + [DONE]
			failMsg = msg
		}
	})
	if err != nil { // 截断流：发 error 帧，不可计 success
		emitStreamError(w, "upstream stream ended abnormally")
		return u, outBytes, err
	}
	if failMsg != "" { // 上游显式失败：error 帧+[DONE] 已发，绝不再合成正常收尾；记 upstream_error
		return u, outBytes, fmt.Errorf("%w: %s", errUpstreamReported, failMsg)
	}

	// Responses 流无 [DONE]（靠 EOF）；chat 客户端依赖收尾帧 + [DONE]，由我们合成。
	emit(gin.H{"id": streamID, "object": "chat.completion.chunk", "model": streamModel,
		"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": finishReason}},
		"usage": gin.H{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
			"total_tokens": u.PromptTokens + u.CompletionTokens}})
	_, _ = w.WriteString("data: [DONE]\n\n")
	w.Flush()
	return u, outBytes, nil
}
