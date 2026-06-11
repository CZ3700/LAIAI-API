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

	// Anthropic 强制 max_tokens
	maxTokens := float64(defaultMaxTokens)
	if mt, ok := req["max_tokens"].(float64); ok && mt > 0 {
		maxTokens = mt
	}
	out["max_tokens"] = maxTokens

	// system 消息提升 + 其余 role 直传
	var systemParts []string
	var messages []map[string]any
	if rawMsgs, ok := req["messages"].([]any); ok {
		for _, rm := range rawMsgs {
			m, ok := rm.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			if role == "system" {
				if s, ok := m["content"].(string); ok {
					systemParts = append(systemParts, s)
				}
				continue
			}
			messages = append(messages, map[string]any{"role": role, "content": m["content"]})
		}
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("anthropic: no user/assistant messages")
	}
	out["messages"] = messages
	if len(systemParts) > 0 {
		out["system"] = strings.Join(systemParts, "\n\n")
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
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
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

func (anthropicAdapter) pipeJSON(c *gin.Context, body io.Reader) (usage, int) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return usage{}, 0
	}
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string         `json:"stop_reason"`
		Usage      anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		// 解析失败原样透传（不丢上游信息），usage 走兜底估算
		c.Data(http.StatusOK, "application/json", raw)
		return usage{}, len(raw)
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	converted := gin.H{
		"id":     resp.ID,
		"object": "chat.completion",
		"model":  resp.Model,
		"choices": []gin.H{{
			"index":         0,
			"message":       gin.H{"role": "assistant", "content": text.String()},
			"finish_reason": mapStopReason(resp.StopReason),
		}},
		"usage": gin.H{
			"prompt_tokens":     resp.Usage.InputTokens,
			"completion_tokens": resp.Usage.OutputTokens,
			"total_tokens":      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
	c.JSON(http.StatusOK, converted)
	return usage{PromptTokens: resp.Usage.InputTokens, CompletionTokens: resp.Usage.OutputTokens}, len(raw)
}

func (anthropicAdapter) pipeSSE(c *gin.Context, body io.Reader) (usage, int) {
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
	pumpLines(c.Request.Context(), body, func(line string) {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			return // event: 行与空行不需要（data JSON 自带 type）
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage anthropicUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return
		}
		switch ev.Type {
		case "message_start":
			u.PromptTokens = ev.Message.Usage.InputTokens
			emit(gin.H{"object": "chat.completion.chunk",
				"choices": []gin.H{{"index": 0, "delta": gin.H{"role": "assistant"}}}})
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				emit(gin.H{"object": "chat.completion.chunk",
					"choices": []gin.H{{"index": 0, "delta": gin.H{"content": ev.Delta.Text}}}})
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
	return u, outBytes
}
