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
	if mt := requestedMaxOutput(req); mt > 0 { // 同识别 max_tokens 与 max_completion_tokens（与 anthropic 口径一致）
		gen["maxOutputTokens"] = float64(mt)
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
					// Gemini responseSchema 只收 OpenAPI 3.0 子集，带 JSON-Schema 专有关键字
					// （additionalProperties/title/$schema…）会 400；而 OpenAI strict 规范强制每个
					// object 带 additionalProperties:false——必须递归剥离（对标 NewAPI）。
					gen["responseSchema"] = sanitizeGeminiSchema(schema, 0)
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
				parts := geminiContentParts(m["content"]) // string 或多模态历史 → text/inlineData 部件
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
				// string 或多模态数组（text/image_url 的 data:URI→inlineData；远程 http 图 Gemini 原生
				// 不收 → 跳过、走兼容渠道）。assistant 历史复用同一 geminiContentParts，转换对称。
				if parts := geminiContentParts(m["content"]); len(parts) > 0 {
					contents = append(contents, map[string]any{"role": "user", "parts": parts})
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

// geminiContentParts 把 OpenAI 消息 content（string 或多模态数组）统一转成 Gemini parts，供
// user 与 assistant 分支复用（转换对称）：text → {text}；image_url 的 data:base64 URI → {inlineData}。
// 远程 http(s) 图 Gemini 原生不收任意 URL（仅 GCS/File API），跳过——这类图走 OpenAI 兼容渠道透传。
func geminiContentParts(content any) []map[string]any {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []map[string]any{{"text": c}}
	case []any:
		var parts []map[string]any
		for _, rp := range c {
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
					parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}})
				}
			}
		}
		return parts
	}
	return nil
}

// sanitizeGeminiSchema 深拷贝并递归剥离 Gemini responseSchema 不接受的 JSON-Schema 专有关键字。
// Gemini 只收 OpenAPI 3.0 Schema 子集；带 additionalProperties/title/$schema 等会 400 INVALID_ARGUMENT。
// 深拷贝（不原地改客户端传入的 map，透传场景下原 req 可能复用）；限深防递归炸栈。对标 NewAPI
// removeAdditionalPropertiesWithDepth。
func sanitizeGeminiSchema(v any, depth int) any {
	if depth > 64 {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			switch k {
			case "additionalProperties", "title", "$schema", "$id", "unevaluatedProperties", "patternProperties":
				continue // Gemini OpenAPI 子集不接受 → 剥离
			default:
				out[k] = sanitizeGeminiSchema(val, depth+1) // 递归处理 properties/items/anyOf/oneOf/allOf 等
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = sanitizeGeminiSchema(e, depth+1)
		}
		return out
	default:
		return v
	}
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
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`      // 思考 token：Google 按 output 计费且**不含**在 candidates 内
}

// completionTotal：计费口径的输出 token = candidates + thoughts。
// Gemini 思考模型（thinkingConfig）的思考 token 由 Google 按 output 价实扣但与
// candidatesTokenCount 分列——只取 candidates 会系统性少收（思考密集时漏 ~95% 输出成本）。
// 负值各自钳 0（对抗 review：异常上游不得拉低计费或把 actual 算成负）。
func (g geminiUsage) completionTotal() int64 {
	c, t := g.CandidatesTokenCount, g.ThoughtsTokenCount
	if c < 0 {
		c = 0
	}
	if t < 0 {
		t = 0
	}
	return c + t
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
	toolIdx := 0 // 只数 tool_use（与 pipeSSE 的 toolCount 同口径），避免按 parts 下标编号致流/非流 id 不一致
	for _, p := range cand.Content.Parts {
		if p.Text != "" {
			text.WriteString(p.Text)
		}
		if p.FunctionCall != nil {
			args := string(p.FunctionCall.Args)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, gin.H{
				"id":       fmt.Sprintf("call_gemini_%d", toolIdx), // Gemini 无 call id，合成稳定 id
				"type":     "function",
				"function": gin.H{"name": p.FunctionCall.Name, "arguments": args},
			})
			toolIdx++
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
			"prompt_tokens":             resp.UsageMetadata.PromptTokenCount,
			"completion_tokens":         resp.UsageMetadata.completionTotal(),
			"total_tokens":              resp.UsageMetadata.PromptTokenCount + resp.UsageMetadata.completionTotal(),
			"prompt_tokens_details":     gin.H{"cached_tokens": resp.UsageMetadata.CachedContentTokenCount},
			"completion_tokens_details": gin.H{"reasoning_tokens": resp.UsageMetadata.ThoughtsTokenCount},
		},
	})
	u := usage{PromptTokens: resp.UsageMetadata.PromptTokenCount, CompletionTokens: resp.UsageMetadata.completionTotal(),
		FinishReason: mapGeminiFinishReason(cand.FinishReason, len(toolCalls) > 0)}
	u.PromptTokensDetails.CachedTokens = resp.UsageMetadata.CachedContentTokenCount // 缓存折扣计费（P2-9）
	u.CompletionTokensDetails.ReasoningTokens = resp.UsageMetadata.ThoughtsTokenCount
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
			u.CompletionTokens = chunk.UsageMetadata.completionTotal()                       // 累计值，取最后一次；含 thoughts（思考 token 计费）
			u.PromptTokensDetails.CachedTokens = chunk.UsageMetadata.CachedContentTokenCount // 缓存折扣（P2-9）
			u.CompletionTokensDetails.ReasoningTokens = chunk.UsageMetadata.ThoughtsTokenCount
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
			"total_tokens":              u.PromptTokens + u.CompletionTokens,
			"prompt_tokens_details":     gin.H{"cached_tokens": u.PromptTokensDetails.CachedTokens},
			"completion_tokens_details": gin.H{"reasoning_tokens": u.CompletionTokensDetails.ReasoningTokens}}})
	_, _ = w.WriteString("data: [DONE]\n\n")
	w.Flush()
	u.FinishReason = finishReason // 排障闭环（0045）
	return u, outBytes, nil
}
