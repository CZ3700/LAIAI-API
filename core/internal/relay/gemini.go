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
	// 结构化输出（response_format → Gemini generationConfig，原 OpenAI 透传渠道才支持的能力补到原生 Gemini）：
	//   {type:"json_object"} → responseMimeType=application/json
	//   {type:"json_schema", json_schema:{schema}} → + responseSchema（OpenAPI 子集）
	if rf, ok := req["response_format"].(map[string]any); ok {
		switch rf["type"] {
		case "json_object":
			gen["responseMimeType"] = "application/json"
		case "json_schema":
			gen["responseMimeType"] = "application/json"
			if js, ok := rf["json_schema"].(map[string]any); ok {
				if schema, ok := js["schema"]; ok {
					gen["responseSchema"] = schema
				}
			}
		}
	}
	// 扩展思考：客户端显式提供 Gemini 原生 thinkingConfig 时透传（OpenAI reasoning_effort 不透传，
	// Gemini 不识别该顶层字段）。
	if tc, ok := req["thinkingConfig"].(map[string]any); ok {
		gen["thinkingConfig"] = tc
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
					// 多模态数组：text → text 部件；image_url 的 data: URI → inlineData（base64 内联）。
					// 远程 http(s) 图 Gemini 原生不收任意 URL（仅 GCS/File API），此处跳过——
					// 这类图走 OpenAI 兼容渠道可直接透传（D1：原生通道视觉输入丝滑）。
					parts := []map[string]any{}
					for _, rp := range content {
						p, ok := rp.(map[string]any)
						if !ok {
							continue
						}
						switch p["type"] {
						case "text":
							if s, ok := p["text"].(string); ok {
								parts = append(parts, map[string]any{"text": s})
							}
						case "image_url":
							iu, _ := p["image_url"].(map[string]any)
							url, _ := iu["url"].(string)
							if mime, data, ok := parseDataURI(url); ok {
								parts = append(parts, map[string]any{
									"inlineData": map[string]any{"mimeType": mime, "data": data},
								})
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

// parseDataURI 解析 `data:<mime>;base64,<data>`，用于把 OpenAI image_url 内联图转成 Gemini inlineData。
// 仅接受 base64 形态（视觉图的常态）；非 data: 或非 base64 返回 ok=false。
func parseDataURI(s string) (mime, data string, ok bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", "", false
	}
	rest := s[len("data:"):]
	const marker = ";base64,"
	i := strings.Index(rest, marker)
	if i < 0 {
		return "", "", false
	}
	mime, data = rest[:i], rest[i+len(marker):]
	if mime == "" || data == "" {
		return "", "", false
	}
	return mime, data, true
}

type geminiUsage struct {
	PromptTokenCount        int64 `json:"promptTokenCount"` // 含缓存部分（Gemini 口径，总输入）
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"` // promptTokenCount 的缓存子集（折扣价）
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

func (geminiAdapter) pipeJSON(c *gin.Context, body io.Reader) (usage, int, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return usage{}, 0, fmt.Errorf("%w: %v", errStreamReadFail, err)
	}
	var resp struct {
		Candidates    []geminiCandidate `json:"candidates"`
		UsageMetadata geminiUsage       `json:"usageMetadata"`
		ModelVersion  string            `json:"modelVersion"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.Candidates) == 0 {
		c.Data(http.StatusOK, "application/json", raw)
		return usage{}, len(raw), nil
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
				"id":       fmt.Sprintf("call_gemini_%d", i), // Gemini 无 call id，合成稳定 id
				"type":     "function",
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
			"prompt_tokens":         resp.UsageMetadata.PromptTokenCount,
			"completion_tokens":     resp.UsageMetadata.CandidatesTokenCount,
			"total_tokens":          resp.UsageMetadata.PromptTokenCount + resp.UsageMetadata.CandidatesTokenCount,
			"prompt_tokens_details": gin.H{"cached_tokens": resp.UsageMetadata.CachedContentTokenCount},
		},
	})
	u := usage{PromptTokens: resp.UsageMetadata.PromptTokenCount, CompletionTokens: resp.UsageMetadata.CandidatesTokenCount}
	u.PromptTokensDetails.CachedTokens = resp.UsageMetadata.CachedContentTokenCount // 缓存折扣计费（P2-9）
	return u, len(raw), nil
}

func (geminiAdapter) pipeSSE(c *gin.Context, body io.Reader) (usage, int, error) {
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
	err := pumpLines(c.Request.Context(), body, func(line string) {
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
			u.CompletionTokens = chunk.UsageMetadata.CandidatesTokenCount                    // 累计值，取最后一次
			u.PromptTokensDetails.CachedTokens = chunk.UsageMetadata.CachedContentTokenCount // 缓存折扣（P2-9）
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
	if err != nil { // 截断流：发 error 帧而非合成"正常收尾"帧，不可计 success
		emitStreamError(w, "upstream stream ended abnormally")
		return u, outBytes, err
	}

	// Gemini 流无 [DONE]：上游 EOF 后由我们合成收尾帧（OpenAI 客户端依赖它）
	emit(gin.H{"object": "chat.completion.chunk",
		"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": finishReason}},
		"usage": gin.H{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
			"total_tokens": u.PromptTokens + u.CompletionTokens}})
	_, _ = w.WriteString("data: [DONE]\n\n")
	w.Flush()
	return u, outBytes, nil
}
