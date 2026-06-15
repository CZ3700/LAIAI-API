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

// anthropicAdapter：协议转换第一例——对外 OpenAI 格式 ↔ 上游 Anthropic Messages API。
// 转换要点：system 消息提升为顶层 system；max_tokens 必填（Anthropic 强制）；
// stop→stop_sequences；usage 字段 input_tokens/output_tokens ↔ prompt/completion_tokens；
// 流式：Anthropic 事件流（message_start/content_block_delta/message_delta/message_stop）
// 重组为 OpenAI chunk 流 + [DONE]。
type anthropicAdapter struct{}

const anthropicVersion = "2023-06-01"

func (anthropicAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	out := map[string]any{"model": req["model"]}

	// Anthropic 强制 max_tokens；同时识别 max_completion_tokens（与 hold 口径一致，P0-2）
	maxTokens := float64(defaultMaxTokens)
	if v := requestedMaxOutput(req); v > 0 {
		maxTokens = float64(v)
	}
	out["max_tokens"] = maxTokens

	// 消息转换：system 提升；assistant tool_calls → tool_use 块；
	// role=tool → tool_result 块，且必须附在 user 消息上（知识库 §5 边角 6：
	// 连续 tool 结果合并进同一条 user 消息，孤立 tool_result 会被 Claude 拒绝）。
	var systemParts []string
	var messages []map[string]any
	if rawMsgs, ok := req["messages"].([]any); ok {
		for _, rm := range rawMsgs {
			m, ok := rm.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			switch role {
			case "system":
				if s, ok := m["content"].(string); ok {
					systemParts = append(systemParts, s)
				}
			case "tool":
				block := map[string]any{
					"type":        "tool_result",
					"tool_use_id": m["tool_call_id"],
					"content":     m["content"],
				}
				if n := len(messages); n > 0 && messages[n-1]["role"] == "user" {
					if blocks, ok := messages[n-1]["content"].([]any); ok {
						messages[n-1]["content"] = append(blocks, block)
						continue
					}
				}
				messages = append(messages, map[string]any{"role": "user", "content": []any{block}})
			case "assistant":
				tcs, _ := m["tool_calls"].([]any)
				if len(tcs) == 0 {
					messages = append(messages, map[string]any{"role": role, "content": m["content"]})
					continue
				}
				var blocks []any
				if s, ok := m["content"].(string); ok && s != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": s})
				}
				for _, rtc := range tcs {
					tc, ok := rtc.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tc["function"].(map[string]any)
					if fn == nil {
						continue
					}
					var input any = map[string]any{}
					if args, ok := fn["arguments"].(string); ok && args != "" {
						if json.Unmarshal([]byte(args), &input) != nil {
							input = map[string]any{} // 历史里的坏 JSON 不致命：给空入参
						}
					}
					blocks = append(blocks, map[string]any{
						"type": "tool_use", "id": tc["id"], "name": fn["name"], "input": input,
					})
				}
				messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
			default:
				messages = append(messages, map[string]any{"role": role, "content": m["content"]})
			}
		}
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("anthropic: no user/assistant messages")
	}
	out["messages"] = messages
	if len(systemParts) > 0 {
		out["system"] = strings.Join(systemParts, "\n\n")
	}

	// 工具定义与 tool_choice（OpenAI → Claude 形态）
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		var ctools []map[string]any
		for _, rt := range tools {
			t, ok := rt.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := t["function"].(map[string]any)
			if !ok {
				continue
			}
			ct := map[string]any{"name": fn["name"], "input_schema": fn["parameters"]}
			if d, ok := fn["description"]; ok {
				ct["description"] = d
			}
			ctools = append(ctools, ct)
		}
		if len(ctools) > 0 {
			out["tools"] = ctools
		}
	}
	switch tc := req["tool_choice"].(type) {
	case string:
		switch tc {
		case "auto":
			out["tool_choice"] = map[string]any{"type": "auto"}
		case "required":
			out["tool_choice"] = map[string]any{"type": "any"}
		case "none":
			delete(out, "tools") // Claude 无 none 语义：撤掉工具定义等效
		}
	case map[string]any:
		if fn, ok := tc["function"].(map[string]any); ok {
			out["tool_choice"] = map[string]any{"type": "tool", "name": fn["name"]}
		}
	}

	for from, to := range map[string]string{"temperature": "temperature", "top_p": "top_p"} {
		if v, ok := req[from]; ok {
			out[to] = v
		}
	}
	if stop, ok := req["stop"]; ok {
		switch s := stop.(type) {
		case string:
			out["stop_sequences"] = []string{s}
		case []any:
			out["stop_sequences"] = s
		}
	}
	if stream {
		out["stream"] = true
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(ch.BaseURL, "/")+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("x-api-key", credential)
	upReq.Header.Set("anthropic-version", anthropicVersion)
	return upReq, nil
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`     // 命中缓存的输入（折扣价）
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"` // 写缓存的输入（按输入价计，宁少不多）
}

// promptTotal：Anthropic 的 input_tokens 不含缓存读/写部分，总输入需相加（否则少计 prompt）。
func (u anthropicUsage) promptTotal() int64 {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

func mapStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter" // 知识库 §5：Claude 拒绝 → OpenAI content_filter
	default: // end_turn / stop_sequence
		return "stop"
	}
}

func (anthropicAdapter) pipeJSON(c *gin.Context, body io.Reader) (usage, int, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return usage{}, 0, fmt.Errorf("%w: %v", errStreamReadFail, err)
	}
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string         `json:"stop_reason"`
		Usage      anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		// 解析失败原样透传（不丢上游信息），usage 走兜底估算
		c.Data(http.StatusOK, "application/json", raw)
		return usage{}, len(raw), nil
	}
	var text strings.Builder
	var toolCalls []gin.H
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, gin.H{
				"id":   block.ID,
				"type": "function",
				"function": gin.H{
					"name":      block.Name,
					"arguments": args, // OpenAI 形态：JSON 字符串而非对象
				},
			})
		}
	}
	message := gin.H{"role": "assistant", "content": text.String()}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if text.Len() == 0 {
			message["content"] = nil // OpenAI 惯例：纯工具调用时 content 为 null
		}
	}
	converted := gin.H{
		"id":     resp.ID,
		"object": "chat.completion",
		"model":  resp.Model,
		"choices": []gin.H{{
			"index":         0,
			"message":       message,
			"finish_reason": mapStopReason(resp.StopReason),
		}},
		"usage": gin.H{
			"prompt_tokens":     resp.Usage.promptTotal(),
			"completion_tokens": resp.Usage.OutputTokens,
			"total_tokens":      resp.Usage.promptTotal() + resp.Usage.OutputTokens,
			"prompt_tokens_details": gin.H{"cached_tokens": resp.Usage.CacheReadInputTokens},
		},
	}
	c.JSON(http.StatusOK, converted)
	u := usage{PromptTokens: resp.Usage.promptTotal(), CompletionTokens: resp.Usage.OutputTokens}
	u.PromptTokensDetails.CachedTokens = resp.Usage.CacheReadInputTokens // 缓存读按折扣价计费（P2-9）
	return u, len(raw), nil
}

func (anthropicAdapter) pipeSSE(c *gin.Context, body io.Reader) (usage, int, error) {
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

	finishReason := "stop"
	// 并发 tool calls 状态机（知识库 §5 边角 1，NewAPI rc.10 同款问题）：
	// Claude 的 content block index 含 text 块，OpenAI 的 tool_calls index 只数工具——
	// 必须维护 block index → tool index 映射，多工具并行时各自累积 arguments。
	toolIdxByBlock := map[int]int{}
	toolCount := 0
	err := pumpLines(c.Request.Context(), body, func(line string) {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			return // event: 行与空行不需要（data JSON 自带 type）
		}
		var ev struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage anthropicUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return
		}
		switch ev.Type {
		case "message_start":
			u.PromptTokens = ev.Message.Usage.promptTotal()
			u.PromptTokensDetails.CachedTokens = ev.Message.Usage.CacheReadInputTokens // 缓存读折扣计费（P2-9）
			emit(gin.H{"object": "chat.completion.chunk",
				"choices": []gin.H{{"index": 0, "delta": gin.H{"role": "assistant"}}}})
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				toolIdx := toolCount
				toolCount++
				toolIdxByBlock[ev.Index] = toolIdx
				emit(gin.H{"object": "chat.completion.chunk",
					"choices": []gin.H{{"index": 0, "delta": gin.H{"tool_calls": []gin.H{{
						"index": toolIdx, "id": ev.ContentBlock.ID, "type": "function",
						"function": gin.H{"name": ev.ContentBlock.Name, "arguments": ""},
					}}}}}})
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					emit(gin.H{"object": "chat.completion.chunk",
						"choices": []gin.H{{"index": 0, "delta": gin.H{"content": ev.Delta.Text}}}})
				}
			case "input_json_delta":
				if toolIdx, ok := toolIdxByBlock[ev.Index]; ok && ev.Delta.PartialJSON != "" {
					emit(gin.H{"object": "chat.completion.chunk",
						"choices": []gin.H{{"index": 0, "delta": gin.H{"tool_calls": []gin.H{{
							"index": toolIdx, "function": gin.H{"arguments": ev.Delta.PartialJSON},
						}}}}}})
				}
			}
		case "message_delta":
			if ev.Usage.OutputTokens > 0 {
				u.CompletionTokens = ev.Usage.OutputTokens
			}
			if ev.Delta.StopReason != "" {
				finishReason = mapStopReason(ev.Delta.StopReason)
			}
		case "message_stop":
			emit(gin.H{"object": "chat.completion.chunk",
				"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": finishReason}},
				"usage": gin.H{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
					"total_tokens": u.PromptTokens + u.CompletionTokens}})
			_, _ = w.WriteString("data: [DONE]\n\n")
			w.Flush()
		}
	})
	if err != nil { // 截断流（无 message_stop）：补 error 帧，不可计 success
		emitStreamError(w, "upstream stream ended abnormally")
	}
	return u, outBytes, err
}
