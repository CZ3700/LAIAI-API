package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Anthropic 原生入站端点（POST /anthropic/v1/messages）——让 Claude Code / Anthropic SDK
// 把 base 指到 /anthropic 即可用本网关。
//
// 设计（双 agent 对抗后定，零资金红线风险）：
//   - 复用**完全不改动**的 HandleChatCompletions（auth/定价/hold/quota/换渠道重试/capture 全套）。
//   - ingress：Anthropic Messages 请求体 → OpenAI canonical 请求体，替换 c.Request.Body。
//   - egress：用同步转换的 ResponseWriter 包住 c.Writer——适配器照常把 OpenAI 输出写给"客户端"，
//     包装器在**同一 goroutine 内**实时把它转成 Anthropic 格式写给真实客户端（无 goroutine/无 io.Pipe，
//     capture 仍内联在 HandleChatCompletions 末尾跑）。
//   - 关键安全性质：计费只依赖上游 usage（适配器 pipe 抽取），与本包装器写什么给客户端**无关**——
//     即便转换有 bug 也只是客户端展示问题，绝不影响入金/扣费（fund-path 一字未改）。
//
// 字段映射是 anthropic.go（出站 OpenAI↔Anthropic 适配器）的精确逆向，common 字段往返无损。
//
// 已知有损（OpenAI canonical 表达不了，或上游适配器本身会丢——非本端点 bug）：
//   - system 块上的 cache_control：出站适配器把 system 折叠成纯字符串（anthropic.go:105-107），
//     故系统提示的 prompt-caching 标记无法透到上游。消息级（user/assistant）text/image 的 cache_control 已保留。
//   - stop_sequence（具体命中的停止串）：OpenAI 不回报，egress 恒为 null。
//   - metadata / top_k / service_tier 等 Anthropic 专属字段：ingress 丢弃（管道不读）。
// 以上均不影响计费（计费只认上游 usage），仅是 Anthropic 专属语义的取舍。

// HandleAnthropicMessages 处理 /anthropic/v1/messages。
func (r *Relay) HandleAnthropicMessages(c *gin.Context) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		anthropicErr(c, http.StatusBadRequest, "invalid_request_error", "cannot read request body")
		return
	}
	openaiBody, stream, cerr := anthropicToOpenAIRequest(raw)
	if cerr != nil {
		anthropicErr(c, http.StatusBadRequest, "invalid_request_error", cerr.Error())
		return
	}
	// 替换请求体为 OpenAI canonical，供未改动的 HandleChatCompletions 读取。
	c.Request.Body = io.NopCloser(bytes.NewReader(openaiBody))
	c.Request.ContentLength = int64(len(openaiBody))
	c.Request.Header.Set("Content-Type", "application/json")

	// 鉴权头桥接（丝滑度批 A）：原生 Anthropic 客户端（Claude Code / Anthropic SDK 默认
	// ANTHROPIC_API_KEY）走 x-api-key 头，而下游 HandleChatCompletions 只认 Authorization: Bearer。
	// 无 Authorization 时用 x-api-key 补上——否则"把 Anthropic 客户端 base 指过来即用"对最常见
	// 用法直接 401。ANTHROPIC_AUTH_TOKEN（Bearer）用户不受影响（Authorization 已在则不覆盖）。
	if c.GetHeader("Authorization") == "" {
		if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
			c.Request.Header.Set("Authorization", "Bearer "+k)
		}
	}

	orig := c.Writer
	aw := &anthropicWriter{ResponseWriter: orig, stream: stream}
	c.Writer = aw
	c.Set(ingressProtocolKey, "anthropic") // 请求日志真实入站方言（排障闭环 0045）
	r.HandleChatCompletions(c)
	c.Writer = orig
	aw.finalize()
}

// anthropicErr 写 Anthropic 形态错误包络（type/error.type/message）。
func anthropicErr(c *gin.Context, status int, errType, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"type": "error", "error": gin.H{"type": errType, "message": msg}})
}

// anthropicErrTypeFor 把 HTTP 状态映射到 Anthropic error.type。
func anthropicErrTypeFor(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusPaymentRequired: // 402 无 Anthropic 对应，归类 invalid_request
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// ════════════════════════ INGRESS：Anthropic 请求 → OpenAI canonical ════════════════════════

// anthropicToOpenAIRequest 把 Anthropic Messages 请求转成 OpenAI chat 请求体。返回 (openaiBody, stream, err)。
// 精确逆向 anthropic.go buildRequest / anthropicContent / tool_choice 映射。
func anthropicToOpenAIRequest(raw []byte) ([]byte, bool, error) {
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false, fmt.Errorf("invalid json")
	}
	out := map[string]any{}
	model, _ := in["model"].(string)
	if model == "" {
		return nil, false, fmt.Errorf("model is required")
	}
	out["model"] = model
	// max_tokens：Anthropic 必填 → OpenAI max_tokens。
	if mt, ok := toInt(in["max_tokens"]); ok && mt > 0 {
		out["max_tokens"] = mt
	} else {
		return nil, false, fmt.Errorf("max_tokens is required")
	}
	stream, _ := in["stream"].(bool)
	if stream {
		out["stream"] = true
	}
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	if ss, ok := in["stop_sequences"].([]any); ok && len(ss) > 0 {
		out["stop"] = ss
	}
	if th, ok := in["thinking"]; ok {
		out["thinking"] = th // 管道原生处理（hold 口径一致）
	}

	var messages []map[string]any
	// system：string 或 text-block 数组 → 顶层 system 消息（join \n\n，逆 anthropic.go:105-107）。
	if sysText := flattenAnthropicSystem(in["system"]); sysText != "" {
		messages = append(messages, map[string]any{"role": "system", "content": sysText})
	}
	// messages：user/assistant，content 为 string 或块数组。
	rawMsgs, _ := in["messages"].([]any)
	for _, rm := range rawMsgs {
		m, ok := rm.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		content := m["content"]
		// string content：原样。
		if s, ok := content.(string); ok {
			messages = append(messages, map[string]any{"role": role, "content": s})
			continue
		}
		blocks, _ := content.([]any)
		if role == "assistant" {
			messages = append(messages, anthropicAssistantToOpenAI(blocks)...)
			continue
		}
		// user（及未知角色）：可能含 text/image/tool_result 块。tool_result 拆成独立 role:tool 消息
		// （逆 anthropic.go:51-63 的"tool 合并进 user"），其余 text/image 合成一条 user 消息。
		messages = append(messages, anthropicUserToOpenAI(role, blocks)...)
	}
	if len(messages) == 0 {
		return nil, false, fmt.Errorf("no messages")
	}
	out["messages"] = messages

	// tools：{name,description,input_schema} → {type:function,function:{name,description,parameters}}。
	if tools, ok := in["tools"].([]any); ok && len(tools) > 0 {
		var ots []map[string]any
		for _, rt := range tools {
			t, ok := rt.(map[string]any)
			if !ok || t["name"] == nil {
				continue
			}
			fn := map[string]any{"name": t["name"], "parameters": t["input_schema"]}
			if d, ok := t["description"]; ok {
				fn["description"] = d
			}
			ots = append(ots, map[string]any{"type": "function", "function": fn})
		}
		if len(ots) > 0 {
			out["tools"] = ots
		}
	}
	// tool_choice：{type:auto|any|tool,name} → "auto"|"required"|{type:function,function:{name}}（逆 :131-145）。
	if tc, ok := in["tool_choice"].(map[string]any); ok {
		switch tc["type"] {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "none":
			out["tool_choice"] = "none"
		case "tool":
			if name, ok := tc["name"].(string); ok && name != "" {
				out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": name}}
			}
		}
	}

	body, err := json.Marshal(out)
	return body, stream, err
}

// flattenAnthropicSystem：system 为 string 直接返回；为 text-block 数组则 join \n\n。
func flattenAnthropicSystem(sys any) string {
	switch s := sys.(type) {
	case string:
		return s
	case []any:
		var parts []string
		for _, raw := range s {
			if b, ok := raw.(map[string]any); ok {
				if t, ok := b["text"].(string); ok && t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// anthropicAssistantToOpenAI：assistant 块数组（text + tool_use）→ 一条 OpenAI assistant 消息。
func anthropicAssistantToOpenAI(blocks []any) []map[string]any {
	var textParts []string
	var toolCalls []map[string]any
	var contentParts []any // 多模态/带 cache_control 时用
	hasImage := false
	forceParts := false // 任一块带 cache_control → 用 parts 形态保留缓存标记
	for _, raw := range blocks {
		b, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch b["type"] {
		case "text":
			if t, ok := b["text"].(string); ok {
				textParts = append(textParts, t)
				tp := map[string]any{"type": "text", "text": t}
				if cc, ok := b["cache_control"]; ok {
					tp["cache_control"] = cc
					forceParts = true
				}
				contentParts = append(contentParts, tp)
			}
		case "image":
			if part := anthropicImageToOpenAI(b); part != nil {
				contentParts = append(contentParts, part)
				hasImage = true
			}
		case "tool_use":
			input := b["input"]
			if input == nil {
				input = map[string]any{}
			}
			args, _ := json.Marshal(input)
			toolCalls = append(toolCalls, map[string]any{
				"id": b["id"], "type": "function",
				"function": map[string]any{"name": b["name"], "arguments": string(args)},
			})
		}
	}
	msg := map[string]any{"role": "assistant"}
	switch {
	case hasImage || forceParts:
		msg["content"] = contentParts
	case len(textParts) > 0:
		msg["content"] = strings.Join(textParts, "")
	default:
		msg["content"] = nil // 纯工具调用：OpenAI 惯例 content=null
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	return []map[string]any{msg}
}

// anthropicUserToOpenAI：user 块数组 → tool_result 拆成 role:tool 消息 + 其余合成一条 user 消息（保序）。
func anthropicUserToOpenAI(role string, blocks []any) []map[string]any {
	var out []map[string]any
	var userParts []any
	flushUser := func() {
		if len(userParts) == 0 {
			return
		}
		if len(userParts) == 1 {
			// 单纯文本块塌缩为字符串；但带 cache_control 的不塌缩（否则丢缓存标记）。
			if tp, ok := userParts[0].(map[string]any); ok && tp["type"] == "text" && tp["cache_control"] == nil {
				out = append(out, map[string]any{"role": role, "content": tp["text"]})
				userParts = nil
				return
			}
		}
		out = append(out, map[string]any{"role": role, "content": userParts})
		userParts = nil
	}
	for _, raw := range blocks {
		b, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch b["type"] {
		case "text":
			tp := map[string]any{"type": "text", "text": b["text"]}
			if cc, ok := b["cache_control"]; ok {
				tp["cache_control"] = cc
			}
			userParts = append(userParts, tp)
		case "image":
			if part := anthropicImageToOpenAI(b); part != nil {
				userParts = append(userParts, part)
			}
		case "tool_result":
			flushUser() // 保序：先把已累积的 user 内容刷出
			out = append(out, map[string]any{
				"role": "tool", "tool_call_id": b["tool_use_id"],
				"content": flattenToolResultContent(b["content"]),
			})
		}
	}
	flushUser()
	return out
}

// anthropicImageToOpenAI：Anthropic image 块 → OpenAI image_url 部件（逆 anthropicContent:229-246）。
func anthropicImageToOpenAI(b map[string]any) map[string]any {
	src, ok := b["source"].(map[string]any)
	if !ok {
		return nil
	}
	var url string
	switch src["type"] {
	case "base64":
		mt, _ := src["media_type"].(string)
		data, _ := src["data"].(string)
		if mt == "" || data == "" {
			return nil
		}
		url = "data:" + mt + ";base64," + data
	case "url":
		url, _ = src["url"].(string)
	}
	if url == "" {
		return nil
	}
	part := map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
	if cc, ok := b["cache_control"]; ok { // 透传 prompt caching 标记（buildRequest 会重新挂到上游 Anthropic 块）
		part["cache_control"] = cc
	}
	return part
}

// flattenToolResultContent：tool_result.content（string 或块数组）→ OpenAI tool 消息 content。
func flattenToolResultContent(content any) any {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []any
		var text []string
		hasImage := false
		for _, raw := range c {
			b, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch b["type"] {
			case "text":
				if t, ok := b["text"].(string); ok {
					text = append(text, t)
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			case "image":
				if part := anthropicImageToOpenAI(b); part != nil {
					parts = append(parts, part)
					hasImage = true
				}
			}
		}
		if hasImage {
			return parts
		}
		return strings.Join(text, "")
	case nil:
		return ""
	default: // number/bool/object 等：序列化为字符串，不丢内容（对抗评审 MEDIUM-1）
		b, _ := json.Marshal(content)
		return string(b)
	}
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// ════════════════════════ EGRESS：同步转换 ResponseWriter ════════════════════════

// anthropicWriter 包住真实 gin.ResponseWriter，把适配器写出的 OpenAI 响应实时转成 Anthropic 格式。
// 嵌入原 writer：Header/Hijack/CloseNotify/Pusher 等直接透传；只覆写 body 写入与状态相关方法。
type anthropicWriter struct {
	gin.ResponseWriter // orig
	stream             bool
	status             int
	body               bytes.Buffer // error / 非流模式累积 OpenAI body
	// SSE 模式：
	sseStarted bool         // 已向 orig 写过 SSE 头
	pending    bytes.Buffer // 未处理完的 OpenAI SSE 字节
	st         anthropicSSE // SSE 状态机
}

func (w *anthropicWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
func (w *anthropicWriter) Written() bool                     { return w.sseStarted }
func (w *anthropicWriter) Size() int                         { return w.body.Len() }
func (w *anthropicWriter) WriteHeader(code int)              { w.status = code }
func (w *anthropicWriter) WriteHeaderNow()                   {}
func (w *anthropicWriter) Write(b []byte) (int, error)       { return w.ingest(b) }
func (w *anthropicWriter) WriteString(s string) (int, error) { return w.ingest([]byte(s)) }

func (w *anthropicWriter) Flush() {
	if w.isStreamSuccess() {
		w.pumpSSE()
		w.ResponseWriter.Flush()
	}
}

func (w *anthropicWriter) isStreamSuccess() bool {
	return w.stream && w.Status() < 400
}

// ingest：所有 body 写入入口。错误模式/非流模式累积到 body（finalize 转换）；流式成功模式喂 SSE 解析。
func (w *anthropicWriter) ingest(b []byte) (int, error) {
	if w.Status() >= 400 {
		return w.body.Write(b) // 错误：缓冲，finalize 转 Anthropic error 包络
	}
	if w.isStreamSuccess() {
		w.startSSE()
		n, _ := w.pending.Write(b)
		w.pumpSSE()
		return n, nil
	}
	return w.body.Write(b) // 非流成功：缓冲，finalize 转 Anthropic Messages JSON
}

// startSSE：首次流式写入前，把状态码+头（适配器已设 text/event-stream + X-Request-Id 到 orig.Header）刷给 orig。
func (w *anthropicWriter) startSSE() {
	if w.sseStarted {
		return
	}
	w.sseStarted = true
	w.ResponseWriter.WriteHeader(http.StatusOK)
}

// pumpSSE：从 pending 提取完整行，逐个 OpenAI chunk 转 Anthropic 事件写给 orig；留尾部不完整行。
func (w *anthropicWriter) pumpSSE() {
	for {
		line, err := w.pending.ReadString('\n')
		if err != nil { // 没有完整行了：把读出的残片塞回
			w.pending.Reset()
			w.pending.WriteString(line)
			return
		}
		data, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: ")
		if !ok || data == "" {
			continue // event:/空行/非 data 行忽略
		}
		if data == "[DONE]" {
			w.st.finishStream(w.ResponseWriter)
			continue
		}
		// 适配器在流截断时注入 OpenAI error 帧（emitStreamError: {"error":{...,"type":"upstream_error"}}) + [DONE]。
		// 必须转成 Anthropic error 事件并**标记 stopped 禁止**后续 [DONE] 伪造干净 message_stop——
		// 截断绝不能被掩盖成成功响应（对抗评审 HIGH）。
		if msg, ok := openaiErrorFrame(data); ok {
			w.st.emitError(w.ResponseWriter, msg)
			continue
		}
		w.st.handleChunk(w.ResponseWriter, data)
	}
}

// openaiErrorFrame 识别 emitStreamError 注入的 OpenAI 形态 error 帧（顶层 error 对象）。
func openaiErrorFrame(data string) (string, bool) {
	var e struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(data), &e) != nil || e.Error == nil {
		return "", false
	}
	if e.Error.Message == "" {
		return "upstream error", true
	}
	return e.Error.Message, true
}

// finalize：HandleChatCompletions 返回后收尾。
func (w *anthropicWriter) finalize() {
	switch {
	case w.Status() >= 400:
		w.writeErrorEnvelope()
	case w.stream:
		w.startSSE() // 兜底：流式但一字未写（理论不至）
		w.pumpSSE()
		if !w.st.stopped { // 流被截断（无 [DONE]）：补 Anthropic error 事件，绝不伪造干净 message_stop
			writeSSE(w.ResponseWriter, "error", gin.H{"type": "error",
				"error": gin.H{"type": "api_error", "message": "upstream stream ended abnormally"}})
			w.ResponseWriter.Flush()
		}
	default:
		w.writeNonStreamJSON()
	}
}

// writeErrorEnvelope：把缓冲的 OpenAI/laiai 错误 JSON 转成 Anthropic error 包络写给 orig。
func (w *anthropicWriter) writeErrorEnvelope() {
	msg := "request failed"
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(w.body.Bytes(), &parsed) == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	out, _ := json.Marshal(gin.H{"type": "error",
		"error": gin.H{"type": anthropicErrTypeFor(w.Status()), "message": msg}})
	h := w.ResponseWriter.Header()
	h.Set("Content-Type", "application/json")
	// 适配器经 c.Data 写 OpenAI body 时已在真实 header 上钉了旧 Content-Length；转换后长度不同，
	// 不改正会让 net/http 按旧长度截断 Anthropic body（对抗评审 CRITICAL）。重设为转换后长度。
	h.Set("Content-Length", strconv.Itoa(len(out)))
	w.ResponseWriter.WriteHeader(w.Status())
	_, _ = w.ResponseWriter.Write(out)
}

// writeNonStreamJSON：把缓冲的 OpenAI chat-completion JSON 转成 Anthropic Messages JSON 写给 orig。
func (w *anthropicWriter) writeNonStreamJSON() {
	out, ok := openaiRespToAnthropic(w.body.Bytes())
	h := w.ResponseWriter.Header()
	h.Set("Content-Type", "application/json")
	if !ok { // 解析失败：不向 Anthropic 客户端泄露 OpenAI JSON，回 Anthropic api_error
		errBody, _ := json.Marshal(gin.H{"type": "error",
			"error": gin.H{"type": "api_error", "message": "upstream returned an unparseable response"}})
		h.Set("Content-Length", strconv.Itoa(len(errBody)))
		w.ResponseWriter.WriteHeader(http.StatusBadGateway)
		_, _ = w.ResponseWriter.Write(errBody)
		return
	}
	// 重设 Content-Length 为转换后 Anthropic body 长度（适配器 c.Data 钉的旧 OpenAI 长度会截断，CRITICAL）。
	h.Set("Content-Length", strconv.Itoa(len(out)))
	w.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = w.ResponseWriter.Write(out)
}

// openaiRespToAnthropic：OpenAI chat-completion → Anthropic Messages 响应 JSON（逆 anthropic.go pipeJSON）。
func openaiRespToAnthropic(raw []byte) ([]byte, bool) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"` // 思考内容（批 A 保真闭环）
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"` // adapter 扩展键（写缓存拆回）
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Choices) == 0 {
		return nil, false
	}
	ch := resp.Choices[0]
	var content []any
	// 思考内容还原（批 A 保真闭环）：reasoning_content 作为可见 text 块前置——原生 Anthropic 客户端
	// 无标准 reasoning 字段，用 text 块保证思考不静默丢（恢复批 A 前可见性）。不用 thinking 块：
	// 那需要有效 signature 才能多轮回传，往返 signature 保真是独立专项。
	if ch.Message.ReasoningContent != "" {
		content = append(content, map[string]any{"type": "text", "text": ch.Message.ReasoningContent})
	}
	if ch.Message.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": ch.Message.Content})
	}
	for _, tc := range ch.Message.ToolCalls {
		var input any = map[string]any{}
		if tc.Function.Arguments != "" {
			if json.Unmarshal([]byte(tc.Function.Arguments), &input) != nil {
				input = map[string]any{}
			}
		}
		content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input})
	}
	if content == nil {
		content = []any{}
	}
	cached, cacheWrite := resp.Usage.PromptTokensDetails.CachedTokens, resp.Usage.CacheCreationInputTokens
	inputTokens := resp.Usage.PromptTokens - cached - cacheWrite // Anthropic input_tokens=非缓存部分（逆 promptTotal）
	if inputTokens < 0 {
		inputTokens = 0
	}
	out := map[string]any{
		"id": resp.ID, "type": "message", "role": "assistant", "model": resp.Model,
		"content": content, "stop_reason": invStopReason(ch.FinishReason), "stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens": inputTokens, "output_tokens": resp.Usage.CompletionTokens,
			"cache_read_input_tokens": cached, "cache_creation_input_tokens": cacheWrite,
		},
	}
	b, err := json.Marshal(out)
	return b, err == nil
}

// invStopReason：OpenAI finish_reason → Anthropic stop_reason（逆 anthropic.go mapStopReason）。
func invStopReason(r string) string {
	switch r {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default: // stop / 空 → end_turn
		return "end_turn"
	}
}

// ════════════════════════ EGRESS SSE 状态机：OpenAI chunk 流 → Anthropic 事件流 ════════════════════════

type anthropicSSE struct {
	started   bool
	id        string
	model     string
	textOpen  bool
	textIndex int
	toolBlock  map[int]int // OpenAI tool index → Anthropic content-block index
	nextIndex  int
	inTok      int64
	cachedTok  int64
	cacheWrTok int64 // 写缓存（adapter 扩展键 cache_creation_input_tokens 拆回）
	outTok     int64
	finish     string
	stopped    bool
}

type sseFlusher interface {
	io.Writer
	Flush()
}

// handleChunk：处理一个 OpenAI chunk（data 行的 JSON），按需 emit Anthropic 事件。
func (s *anthropicSSE) handleChunk(w sseFlusher, data string) {
	if s.stopped {
		return
	}
	var ck struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"` // 思考增量（批 A 保真闭环）
				ToolCalls        []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"` // adapter 扩展键
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &ck) != nil {
		return
	}
	if ck.ID != "" {
		s.id = ck.ID
	}
	if ck.Model != "" {
		s.model = ck.Model
	}
	if !s.started {
		s.start(w)
	}
	if ck.Usage != nil {
		if ck.Usage.PromptTokens > 0 {
			s.inTok = ck.Usage.PromptTokens
		}
		if ck.Usage.CompletionTokens > 0 {
			s.outTok = ck.Usage.CompletionTokens
		}
		if ck.Usage.PromptTokensDetails.CachedTokens > 0 {
			s.cachedTok = ck.Usage.PromptTokensDetails.CachedTokens
		}
		if ck.Usage.CacheCreationInputTokens > 0 {
			s.cacheWrTok = ck.Usage.CacheCreationInputTokens
		}
	}
	for _, choice := range ck.Choices {
		// 思考增量还原（批 A 保真闭环）：作为可见 text_delta 流出（原生 Anthropic 无标准 reasoning
		// 增量字段；用 text 避免 thinking 块的 signature 往返问题，恢复批 A 前可见性）。
		if choice.Delta.ReasoningContent != "" {
			if !s.textOpen {
				s.openText(w)
			}
			writeSSE(w, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": s.textIndex,
				"delta": map[string]any{"type": "text_delta", "text": choice.Delta.ReasoningContent},
			})
		}
		// 文本增量
		if choice.Delta.Content != "" {
			if !s.textOpen {
				s.openText(w)
			}
			writeSSE(w, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": s.textIndex,
				"delta": map[string]any{"type": "text_delta", "text": choice.Delta.Content},
			})
		}
		// 工具调用增量
		for _, tc := range choice.Delta.ToolCalls {
			bi, exists := s.toolBlock[tc.Index]
			if !exists { // 首次见此工具：开 tool_use 块
				if s.textOpen { // 先闭文本块（Anthropic 块需严格闭合）
					s.closeBlock(w, s.textIndex)
					s.textOpen = false
				}
				bi = s.nextIndex
				s.nextIndex++
				s.toolBlock[tc.Index] = bi
				writeSSE(w, "content_block_start", map[string]any{
					"type": "content_block_start", "index": bi,
					"content_block": map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": map[string]any{}},
				})
			}
			if tc.Function.Arguments != "" {
				writeSSE(w, "content_block_delta", map[string]any{
					"type": "content_block_delta", "index": bi,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
				})
			}
		}
		if choice.FinishReason != "" {
			s.finish = choice.FinishReason
		}
	}
	w.Flush()
}

func (s *anthropicSSE) start(w sseFlusher) {
	s.started = true
	s.toolBlock = map[int]int{}
	if s.id == "" {
		s.id = "msg_" + newRequestID()
	}
	writeSSE(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": s.id, "type": "message", "role": "assistant", "model": s.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			// OpenAI 仅末帧给 usage；input 先占位 0，message_delta 里据末帧 usage 校正。
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
		},
	})
}

func (s *anthropicSSE) openText(w sseFlusher) {
	s.textOpen = true
	s.textIndex = s.nextIndex
	s.nextIndex++
	writeSSE(w, "content_block_start", map[string]any{
		"type": "content_block_start", "index": s.textIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (s *anthropicSSE) closeBlock(w sseFlusher, index int) {
	writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
}

// finishStream：收到 [DONE] → 闭合所有开块 + message_delta（stop_reason+usage）+ message_stop。
func (s *anthropicSSE) finishStream(w sseFlusher) {
	if s.stopped {
		return
	}
	if !s.started {
		s.start(w) // 空流兜底
	}
	if s.textOpen {
		s.closeBlock(w, s.textIndex)
		s.textOpen = false
	}
	for _, bi := range s.toolBlock {
		s.closeBlock(w, bi)
	}
	s.toolBlock = map[int]int{}
	inputTokens := s.inTok - s.cachedTok - s.cacheWrTok
	if inputTokens < 0 {
		inputTokens = 0
	}
	writeSSE(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": invStopReason(s.finish), "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": s.outTok,
			"cache_read_input_tokens": s.cachedTok, "cache_creation_input_tokens": s.cacheWrTok},
	})
	writeSSE(w, "message_stop", map[string]any{"type": "message_stop"})
	w.Flush()
	s.stopped = true
}

// emitError：流中途上游错误 → 发 Anthropic error 事件，标记 stopped（禁止再发 message_stop/重复 error）。
func (s *anthropicSSE) emitError(w sseFlusher, msg string) {
	if s.stopped {
		return
	}
	if !s.started { // 还没发过 message_start 也要先起头，客户端才认这是一个消息流
		s.start(w)
	}
	writeSSE(w, "error", map[string]any{"type": "error",
		"error": map[string]any{"type": "api_error", "message": msg}})
	w.Flush()
	s.stopped = true
}

// writeSSE：写一条 Anthropic SSE 事件（event: 行 + data: 行）。
func writeSSE(w io.Writer, event string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "event: "+event+"\ndata: "+string(raw)+"\n\n")
}
