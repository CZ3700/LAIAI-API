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

// geminiAdapter：Google Gemini 原生协议（generateContent）双向转换（知识库 §5.3）。
// 要点：messages→contents/parts（assistant→model 角色）、system→systemInstruction、
// usageMetadata→usage、finishReason 映射（SAFETY/RECITATION→content_filter）、
// 流式是 SSE 但 chunk 形态与 OpenAI 完全不同（candidates/parts），且无 [DONE]——由我们收尾合成。
type geminiAdapter struct{}

func mapGeminiFinishReason(r string, hasToolCall bool) string {
	if hasToolCall {
		return "tool_calls"
	}
	switch r {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	default: // STOP / FINISH_REASON_UNSPECIFIED / OTHER
		return "stop"
	}
}

func (geminiAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	model, _ := req["model"].(string)
	out := map[string]any{}

	// generationConfig
	gen := map[string]any{}
	if mt, ok := req["max_tokens"].(float64); ok && mt > 0 {
		gen["maxOutputTokens"] = mt
	}
	if v, ok := req["temperature"]; ok {
		gen["temperature"] = v
	}
	if v, ok := req["top_p"]; ok {
		gen["topP"] = v
	}
	if len(gen) > 0 {
		out["generationConfig"] = gen
	}

	// messages → contents/parts；system 提升为 systemInstruction
	var systemParts []string
	var contents []map[string]any
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
			case "assistant":
				parts := []map[string]any{}
				if s, ok := m["content"].(string); ok && s != "" {
					parts = append(parts, map[string]any{"text": s})
				}
				if tcs, ok := m["tool_calls"].([]any); ok {
					for _, rtc := range tcs {
						tc, _ := rtc.(map[string]any)
						if tc == nil {
							continue
						}
						fn, _ := tc["function"].(map[string]any)
						if fn == nil {
							continue
						}
						var args any = map[string]any{}
						if s, ok := fn["arguments"].(string); ok && s != "" {
							_ = json.Unmarshal([]byte(s), &args)
						}
						parts = append(parts, map[string]any{
							"functionCall": map[string]any{"name": fn["name"], "args": args},
						})
					}
				}
				if len(parts) > 0 {
					contents = append(contents, map[string]any{"role": "model", "parts": parts})
				}
			case "tool":
				// functionResponse 需要函数名；OpenAI tool 消息可带 name，缺失时退化为文本部件
				name, _ := m["name"].(string)
				if name == "" {
					if s, ok := m["content"].(string); ok {
						contents = append(contents, map[string]any{"role": "user",
							"parts": []map[string]any{{"text": "[tool result] " + s}}})
					}
					continue
				}
				contents = append(contents, map[string]any{"role": "user",
					"parts": []map[string]any{{"functionResponse": map[string]any{
						"name": name, "response": map[string]any{"content": m["content"]},
					}}}})
			default: // user
				switch content := m["content"].(type) {
				case string:
					contents = append(contents, map[string]any{"role": "user",
						"parts": []map[string]any{{"text": content}}})
				case []any:
					// 多模态数组：M2 仅取 text 部件（图片 inlineData 留待 vision 专项）
					parts := []map[string]any{}
					for _, rp := range content {
						if p, ok := rp.(map[string]any); ok && p["type"] == "text" {
							if s, ok := p["text"].(string); ok {
								parts = append(parts, map[string]any{"text": s})
							}
						}
					}
					if len(parts) > 0 {
						contents = append(contents, map[string]any{"role": "user", "parts": parts})
					}
				}
			}
		}
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("gemini: no user/assistant messages")
	}
	out["contents"] = contents
	if len(systemParts) > 0 {
		out["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": strings.Join(systemParts, "\n\n")}},
		}
	}

	// tools / tool_choice → functionDeclarations / functionCallingConfig（知识库 §5.3）
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		var decls []map[string]any
		for _, rt := range tools {
			t, _ := rt.(map[string]any)
			if t == nil {
				continue
			}
			fn, _ := t["function"].(map[string]any)
			if fn == nil {
				continue
			}
			decls = append(decls, map[string]any{
				"name": fn["name"], "description": fn["description"], "parameters": fn["parameters"],
			})
		}
		if len(decls) > 0 {
			out["tools"] = []map[string]any{{"functionDeclarations": decls}}
		}
	}
	switch tc := req["tool_choice"].(type) {
	case string:
		mode := map[string]string{"auto": "AUTO", "required": "ANY", "none": "NONE"}[tc]
		if mode != "" {
			out["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": mode}}
		}
	case map[string]any:
		if fn, ok := tc["function"].(map[string]any); ok {
			out["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{
				"mode": "ANY", "allowedFunctionNames": []any{fn["name"]},
			}}
		}
	}

	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(ch.BaseURL, "/")+"/models/"+model+":"+action, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("x-goog-api-key", credential)
	return upReq, nil
}

type geminiUsage struct {
	PromptTokenCount     int64 `json:"promptTokenCount"`
	CandidatesTokenCount int64 `json:"candidatesTokenCount"`
}

type geminiCandidate struct {
	Content struct {
		Parts []struct {
			Text         string `json:"text"`
			FunctionCall *struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			} `json:"functionCall"`
		} `json:"parts"`
	} `json:"content"`
	FinishReason string `json:"finishReason"`
}

func (geminiAdapter) pipeJSON(c *gin.Context, body io.Reader) (usage, int) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return usage{}, 0
	}
	var resp struct {
		Candidates    []geminiCandidate `json:"candidates"`
		UsageMetadata geminiUsage       `json:"usageMetadata"`
		ModelVersion  string            `json:"modelVersion"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.Candidates) == 0 {
		c.Data(http.StatusOK, "application/json", raw)
		return usage{}, len(raw)
	}
	cand := resp.Candidates[0]
	var text strings.Builder
	var toolCalls []gin.H
	for i, p := range cand.Content.Parts {
		if p.Text != "" {
			text.WriteString(p.Text)
		}
		if p.FunctionCall != nil {
			args := string(p.FunctionCall.Args)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, gin.H{
				"id":   fmt.Sprintf("call_gemini_%d", i), // Gemini 无 call id，合成稳定 id
				"type": "function",
				"function": gin.H{"name": p.FunctionCall.Name, "arguments": args},
			})
		}
	}
	message := gin.H{"role": "assistant", "content": text.String()}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if text.Len() == 0 {
			message["content"] = nil
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"id":     "chatcmpl-gemini",
		"object": "chat.completion",
		"model":  resp.ModelVersion,
		"choices": []gin.H{{
			"index":         0,
			"message":       message,
			"finish_reason": mapGeminiFinishReason(cand.FinishReason, len(toolCalls) > 0),
		}},
		"usage": gin.H{
			"prompt_tokens":     resp.UsageMetadata.PromptTokenCount,
			"completion_tokens": resp.UsageMetadata.CandidatesTokenCount,
			"total_tokens":      resp.UsageMetadata.PromptTokenCount + resp.UsageMetadata.CandidatesTokenCount,
		},
	})
	return usage{PromptTokens: resp.UsageMetadata.PromptTokenCount,
		CompletionTokens: resp.UsageMetadata.CandidatesTokenCount}, len(raw)
}

func (geminiAdapter) pipeSSE(c *gin.Context, body io.Reader) (usage, int) {
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
	sentRole := false
	toolCount := 0
	pumpLines(c.Request.Context(), body, func(line string) {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			return
		}
		var chunk struct {
			Candidates    []geminiCandidate `json:"candidates"`
			UsageMetadata *geminiUsage      `json:"usageMetadata"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return
		}
		if chunk.UsageMetadata != nil {
			u.PromptTokens = chunk.UsageMetadata.PromptTokenCount
			u.CompletionTokens = chunk.UsageMetadata.CandidatesTokenCount // 累计值，取最后一次
		}
		if len(chunk.Candidates) == 0 {
			return
		}
		if !sentRole {
			sentRole = true
			emit(gin.H{"object": "chat.completion.chunk",
				"choices": []gin.H{{"index": 0, "delta": gin.H{"role": "assistant"}}}})
		}
		cand := chunk.Candidates[0]
		for _, p := range cand.Content.Parts {
			if p.Text != "" {
				emit(gin.H{"object": "chat.completion.chunk",
					"choices": []gin.H{{"index": 0, "delta": gin.H{"content": p.Text}}}})
			}
			if p.FunctionCall != nil {
				args := string(p.FunctionCall.Args)
				if args == "" {
					args = "{}"
				}
				emit(gin.H{"object": "chat.completion.chunk",
					"choices": []gin.H{{"index": 0, "delta": gin.H{"tool_calls": []gin.H{{
						"index": toolCount, "id": fmt.Sprintf("call_gemini_%d", toolCount),
						"type": "function", "function": gin.H{"name": p.FunctionCall.Name, "arguments": args},
					}}}}}})
				toolCount++
				finishReason = "tool_calls"
			}
		}
		if cand.FinishReason != "" && finishReason != "tool_calls" {
			finishReason = mapGeminiFinishReason(cand.FinishReason, false)
		}
	})

	// Gemini 流无 [DONE]：上游 EOF 后由我们合成收尾帧（OpenAI 客户端依赖它）
	emit(gin.H{"object": "chat.completion.chunk",
		"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": finishReason}},
		"usage": gin.H{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
			"total_tokens": u.PromptTokens + u.CompletionTokens}})
	_, _ = w.WriteString("data: [DONE]\n\n")
	w.Flush()
	return u, outBytes
}
