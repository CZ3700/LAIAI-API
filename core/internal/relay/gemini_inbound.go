package relay

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

var _ gin.ResponseWriter = (*geminiInboundWriter)(nil)

// HandleGeminiNative 处理 Google Gemini 原生入站端点：
// POST /v1beta/models/:modelAction（实际通配路径 /v1beta/models/*action，支持 :generateContent 与 :streamGenerateContent）。
// 认证支持 x-goog-api-key 请求头、?key= 查询参数或 Authorization: Bearer，均验证平台 API Key 后走统一 Relay 主链路。
func (r *Relay) HandleGeminiNative(c *gin.Context) {
	action := strings.TrimPrefix(c.Param("action"), "/")
	model, op, ok := strings.Cut(action, ":")
	if !ok || strings.TrimSpace(model) == "" {
		geminiErr(c, http.StatusBadRequest, "INVALID_ARGUMENT", "expected /v1beta/models/{model}:generateContent or :streamGenerateContent")
		return
	}
	var stream bool
	switch op {
	case "generateContent":
		stream = false
	case "streamGenerateContent":
		stream = true
	default:
		geminiErr(c, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported Gemini action :"+op)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		geminiErr(c, http.StatusBadRequest, "INVALID_ARGUMENT", "cannot read request body")
		return
	}
	openaiBody, cerr := geminiToOpenAIRequest(model, stream, raw)
	if cerr != nil {
		geminiErr(c, http.StatusBadRequest, "INVALID_ARGUMENT", cerr.Error())
		return
	}

	// 桥接鉴权头到标准 Authorization: Bearer，复用 HandleChatCompletions 全套校验/计费/路由/记录。
	if c.GetHeader("Authorization") == "" {
		apiKey := cmp.Or(strings.TrimSpace(c.GetHeader("x-goog-api-key")), strings.TrimSpace(c.Query("key")))
		if apiKey != "" {
			c.Request.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	c.Request.Body = io.NopCloser(bytes.NewReader(openaiBody))
	c.Request.ContentLength = int64(len(openaiBody))
	c.Request.Header.Set("Content-Type", "application/json")

	orig := c.Writer
	gw := &geminiInboundWriter{ResponseWriter: orig, stream: stream, model: model}
	c.Writer = gw
	c.Set(ingressProtocolKey, "gemini")
	r.HandleChatCompletions(c)
	c.Writer = orig
	gw.finalize()
}

func geminiErr(c *gin.Context, status int, grpcStatus, msg string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": msg,
			"status":  grpcStatus,
		},
	})
}

func geminiGRPCStatusFor(httpStatus int) string {
	switch httpStatus {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusPaymentRequired:
		return "FAILED_PRECONDITION"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}

func geminiToOpenAIRequest(model string, stream bool, raw []byte) ([]byte, error) {
	var in map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
	}
	if in == nil {
		in = map[string]any{}
	}

	msgs := []map[string]any{}
	if sys, ok := in["systemInstruction"].(map[string]any); ok {
		if txt := joinGeminiPartsText(sys["parts"]); txt != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": txt})
		}
	}
	if contents, ok := in["contents"].([]any); ok {
		for _, item := range contents {
			cm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role, _ := cm["role"].(string)
			switch role {
			case "model":
				role = "assistant"
			case "":
				role = "user"
			}
			parts, _ := cm["parts"].([]any)
			if msg := convertGeminiPartsToOpenAIMessage(role, parts); msg != nil {
				msgs = append(msgs, msg)
			}
		}
	}

	out := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   stream,
	}
	if gen, ok := in["generationConfig"].(map[string]any); ok {
		if v, exists := gen["maxOutputTokens"]; exists {
			out["max_tokens"] = v
		}
		if v, exists := gen["temperature"]; exists {
			out["temperature"] = v
		}
		if v, exists := gen["topP"]; exists {
			out["top_p"] = v
		}
		if tc, exists := gen["thinkingConfig"]; exists {
			out["thinkingConfig"] = tc
		}
		if mime, _ := gen["responseMimeType"].(string); mime == "application/json" {
			if schema, hasSchema := gen["responseSchema"]; hasSchema {
				out["response_format"] = map[string]any{
					"type":        "json_schema",
					"json_schema": map[string]any{"name": "response", "schema": schema},
				}
			} else {
				out["response_format"] = map[string]any{"type": "json_object"}
			}
		}
	}
	return json.Marshal(out)
}

func joinGeminiPartsText(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, p := range arr {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := pm["text"].(string); t != "" {
			sb.WriteString(t)
		}
	}
	return sb.String()
}

func convertGeminiPartsToOpenAIMessage(role string, parts []any) map[string]any {
	textParts := []string{}
	contentBlocks := []map[string]any{}
	toolCalls := []map[string]any{}

	for idx, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := pm["text"].(string); t != "" {
			textParts = append(textParts, t)
			contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": t})
		}
		if ind, ok := pm["inlineData"].(map[string]any); ok {
			mime, _ := ind["mimeType"].(string)
			data, _ := ind["data"].(string)
			if mime != "" && data != "" {
				contentBlocks = append(contentBlocks, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + mime + ";base64," + data},
				})
			}
		}
		if fc, ok := pm["functionCall"].(map[string]any); ok {
			name, _ := fc["name"].(string)
			argsBytes, err := json.Marshal(fc["args"])
			if err != nil || len(argsBytes) == 0 || string(argsBytes) == "null" {
				argsBytes = []byte("{}")
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":       "call_gemini_in_" + strconv.Itoa(idx),
				"type":     "function",
				"function": map[string]any{"name": name, "arguments": string(argsBytes)},
			})
		}
	}

	msg := map[string]any{"role": role}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	if len(contentBlocks) > len(textParts) {
		msg["content"] = contentBlocks
	} else {
		msg["content"] = strings.Join(textParts, "\n")
	}
	return msg
}

type geminiInboundWriter struct {
	gin.ResponseWriter
	stream     bool
	model      string
	status     int
	sseStarted bool
	buf        bytes.Buffer
	sseBuf     bytes.Buffer
	finalized  bool
}

func (w *geminiInboundWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
func (w *geminiInboundWriter) Written() bool { return w.sseStarted }
func (w *geminiInboundWriter) Size() int     { return w.buf.Len() }
func (w *geminiInboundWriter) WriteHeaderNow() {
	if w.stream && w.Status() == http.StatusOK && !w.sseStarted {
		w.sseStarted = true
		w.ResponseWriter.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.ResponseWriter.WriteHeader(http.StatusOK)
	}
}

func (w *geminiInboundWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	if w.stream && code == http.StatusOK && !w.sseStarted {
		w.sseStarted = true
		w.ResponseWriter.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *geminiInboundWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 || !w.stream {
		return w.buf.Write(p)
	}
	w.sseBuf.Write(p)
	w.drainSSE()
	return len(p), nil
}

func (w *geminiInboundWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *geminiInboundWriter) Flush() {
	if w.stream {
		w.drainSSE()
	}
	w.ResponseWriter.Flush()
}

func (w *geminiInboundWriter) drainSSE() {
	for {
		line, err := w.sseBuf.ReadString('\n')
		if err != nil {
			if line != "" {
				rest := line + w.sseBuf.String()
				w.sseBuf.Reset()
				w.sseBuf.WriteString(rest)
			}
			return
		}
		trimmed := strings.TrimSpace(line)
		data, ok := strings.CutPrefix(trimmed, "data: ")
		if !ok || data == "[DONE]" || data == "" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *usage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		parts := []gin.H{}
		var finishReason string
		if len(chunk.Choices) > 0 {
			ch := chunk.Choices[0]
			if ch.Delta.ReasoningContent != "" {
				parts = append(parts, gin.H{"text": ch.Delta.ReasoningContent, "thought": true})
			}
			if ch.Delta.Content != "" {
				parts = append(parts, gin.H{"text": ch.Delta.Content})
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				finishReason = mapOpenAIFinishToGemini(*ch.FinishReason)
			}
		}
		// 跳过纯 role 宣告等无有效载荷帧，避免下发空 parts 触发严格 SDK 报错
		if len(parts) == 0 && finishReason == "" && chunk.Usage == nil {
			continue
		}
		cand := gin.H{
			"index":   0,
			"content": gin.H{"role": "model", "parts": parts},
		}
		if finishReason != "" {
			cand["finishReason"] = finishReason
		}
		out := gin.H{
			"modelVersion": w.model,
			"candidates":   []gin.H{cand},
		}
		if chunk.Usage != nil {
			out["usageMetadata"] = buildGeminiUsageMetadata(*chunk.Usage)
		}
		if b, err := json.Marshal(out); err == nil {
			_, _ = w.ResponseWriter.Write([]byte("data: " + string(b) + "\n\n"))
		}
	}
}

func (w *geminiInboundWriter) finalize() {
	if w.finalized {
		return
	}
	w.finalized = true
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 {
		msg := strings.TrimSpace(w.buf.String())
		var parsed struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(w.buf.Bytes(), &parsed) == nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		w.ResponseWriter.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(w.status)
		b, _ := json.Marshal(gin.H{
			"error": gin.H{
				"code":    w.status,
				"message": msg,
				"status":  geminiGRPCStatusFor(w.status),
			},
		})
		_, _ = w.ResponseWriter.Write(b)
		return
	}
	if w.stream {
		w.drainSSE()
		w.ResponseWriter.Flush()
		return
	}

	raw := w.buf.Bytes()
	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		w.ResponseWriter.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(w.status)
		_, _ = w.ResponseWriter.Write(raw)
		return
	}
	parts := []gin.H{}
	finishReason := "STOP"
	if len(parsed.Choices) > 0 {
		ch := parsed.Choices[0]
		if ch.Message.ReasoningContent != "" {
			parts = append(parts, gin.H{"text": ch.Message.ReasoningContent, "thought": true})
		}
		if ch.Message.Content != "" {
			parts = append(parts, gin.H{"text": ch.Message.Content})
		}
		for _, tc := range ch.Message.ToolCalls {
			var args any = map[string]any{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			parts = append(parts, gin.H{
				"functionCall": gin.H{"name": tc.Function.Name, "args": args},
			})
		}
		if ch.FinishReason != "" {
			finishReason = mapOpenAIFinishToGemini(ch.FinishReason)
		}
	}
	resp := gin.H{
		"modelVersion": cmp.Or(parsed.Model, w.model),
		"candidates": []gin.H{{
			"index":        0,
			"content":      gin.H{"role": "model", "parts": parts},
			"finishReason": finishReason,
		}},
		"usageMetadata": buildGeminiUsageMetadata(parsed.Usage),
	}
	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	w.ResponseWriter.WriteHeader(http.StatusOK)
	b, _ := json.Marshal(resp)
	_, _ = w.ResponseWriter.Write(b)
}

func mapOpenAIFinishToGemini(reason string) string {
	switch reason {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}

func buildGeminiUsageMetadata(u usage) gin.H {
	return gin.H{
		"promptTokenCount":        u.PromptTokens,
		"candidatesTokenCount":    u.CompletionTokens,
		"totalTokenCount":         u.PromptTokens + u.CompletionTokens,
		"cachedContentTokenCount": u.PromptTokensDetails.CachedTokens,
		"thoughtsTokenCount":      u.CompletionTokensDetails.ReasoningTokens,
	}
}
