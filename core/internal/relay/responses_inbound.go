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

// OpenAI Responses API 入站端点（POST /v1/responses）——让 OpenAI Responses SDK / Codex 类客户端
// 把 base 指到本网关即可用任意 chat 上游（商用迭代指令 §3.2）。
//
// 架构与 anthropic_inbound.go 同款（零资金红线风险、经对抗评审）：
//   - 复用**完全不改动**的 HandleChatCompletions（auth/admission/定价/hold/quota/换渠道重试/capture 全套）。
//   - ingress：Responses 请求体 → OpenAI canonical chat 请求体，替换 c.Request.Body。
//   - egress：同步转换 ResponseWriter 把 chat 输出实时转成 Responses 形态（非流式 JSON / 流式事件序列）。
//   - 计费只依赖上游 usage（适配器 pipe 抽取），与本包装器写什么给客户端**无关**——转换 bug 只影响展示。
//
// 无状态边界（诚实拒绝，不静默降级）：
//   - previous_response_id / conversation：本平台不存 Responses 会话状态 → 400（无法续接）。
//   - store：忽略（我们从不持久化；响应不可事后 GET，但单次调用正常——软降级，不拒绝默认调用）。
//   - 托管内建工具（web_search 等）：ingress 丢弃 tools 中的非 function 项（chat 上游不支持），不伪造调用。
//
// 参考库（new-api）已知 bug，本实现规避：incomplete_details 官方字段是 reason 非 reasoning；
// Responses usage 有独立的 output_tokens_details.reasoning_tokens（不与 chat 共享 struct）。

// HandleResponses 处理 POST /v1/responses。
func (r *Relay) HandleResponses(c *gin.Context) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		responsesErr(c, http.StatusBadRequest, "invalid_request_error", "cannot read request body")
		return
	}
	openaiBody, stream, cerr := responsesToOpenAIRequest(raw)
	if cerr != nil {
		responsesErr(c, http.StatusBadRequest, "invalid_request_error", cerr.Error())
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(openaiBody))
	c.Request.ContentLength = int64(len(openaiBody))
	c.Request.Header.Set("Content-Type", "application/json")

	orig := c.Writer
	rw := &responsesWriter{ResponseWriter: orig, stream: stream}
	c.Writer = rw
	r.HandleChatCompletions(c)
	c.Writer = orig
	rw.finalize()
}

// responsesErr 写 Responses 形态错误包络（{error:{type,message,code,param}}）。
func responsesErr(c *gin.Context, status int, errType, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": errType, "message": msg, "code": nil, "param": nil}})
}

// responsesErrTypeFor 把 HTTP 状态映射到 Responses/OpenAI error.type。
func responsesErrTypeFor(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusPaymentRequired:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "invalid_request_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

// ════════════════════════ INGRESS：Responses 请求 → OpenAI canonical chat ════════════════════════

// responsesToOpenAIRequest 把 Responses 请求转成 OpenAI chat 请求体。返回 (openaiBody, stream, err)。
func responsesToOpenAIRequest(raw []byte) ([]byte, bool, error) {
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, false, fmt.Errorf("invalid json")
	}
	// 无状态边界：无法续接已存储会话/模板，诚实拒绝（不静默丢上下文）。
	if s, _ := in["previous_response_id"].(string); s != "" {
		return nil, false, fmt.Errorf("previous_response_id is not supported: this gateway is stateless, resend full input")
	}
	if in["conversation"] != nil {
		return nil, false, fmt.Errorf("conversation is not supported: this gateway is stateless")
	}
	// prompt 模板（服务端存储的 prompt.id）同样是服务端状态 → 拒绝（裸字符串 prompt 不带 id 不拦）。
	if p, ok := in["prompt"].(map[string]any); ok && p["id"] != nil {
		return nil, false, fmt.Errorf("prompt templates are not supported: this gateway is stateless")
	}

	out := map[string]any{}
	model, _ := in["model"].(string)
	if model == "" {
		return nil, false, fmt.Errorf("model is required")
	}
	out["model"] = model

	stream, _ := in["stream"].(bool)
	if stream {
		out["stream"] = true
	}
	// max_output_tokens → max_tokens
	if mt, ok := toInt(in["max_output_tokens"]); ok && mt > 0 {
		out["max_tokens"] = mt
	}
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	// reasoning.effort → chat reasoning_effort（canonical OpenAI 字段；上游不识别则由 param_override 处理）
	if rz, ok := in["reasoning"].(map[string]any); ok {
		if eff, ok := rz["effort"].(string); ok && eff != "" {
			out["reasoning_effort"] = eff
		}
	}

	// input（string | 数组）+ instructions → messages
	messages := make([]map[string]any, 0, 4)
	if instr := flattenResponsesInstructions(in["instructions"]); instr != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instr})
	}
	msgs, ierr := responsesInputToMessages(in["input"])
	if ierr != nil {
		return nil, false, ierr
	}
	messages = append(messages, msgs...)
	if len(messages) == 0 {
		return nil, false, fmt.Errorf("input is required")
	}
	out["messages"] = messages

	// tools：Responses 扁平 {type:function,name,description,parameters} → chat 嵌套 {type:function,function:{...}}。
	// 非 function 项（web_search_preview/file_search 等托管工具）chat 上游不支持 → 丢弃（不伪造）。
	if tools, ok := in["tools"].([]any); ok && len(tools) > 0 {
		var ots []map[string]any
		for _, rt := range tools {
			t, ok := rt.(map[string]any)
			if !ok || t["type"] != "function" || t["name"] == nil {
				continue
			}
			fn := map[string]any{"name": t["name"]}
			if p, ok := t["parameters"]; ok {
				fn["parameters"] = p
			}
			if d, ok := t["description"]; ok {
				fn["description"] = d
			}
			ots = append(ots, map[string]any{"type": "function", "function": fn})
		}
		if len(ots) > 0 {
			out["tools"] = ots
		}
	}
	// tool_choice：字符串（auto/none/required）直传；{type:function,name} → {type:function,function:{name}}。
	switch tc := in["tool_choice"].(type) {
	case string:
		out["tool_choice"] = tc
	case map[string]any:
		if tc["type"] == "function" {
			if name, ok := tc["name"].(string); ok && name != "" {
				out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": name}}
			}
		}
	}
	// text.format → response_format（json_schema 需把 name/schema/strict 收进 json_schema 子对象）。
	if text, ok := in["text"].(map[string]any); ok {
		if rf := responsesTextFormatToResponseFormat(text["format"]); rf != nil {
			out["response_format"] = rf
		}
	}

	body, err := json.Marshal(out)
	return body, stream, err
}

// flattenResponsesInstructions：instructions 为 string 直接返回，其余（null/对象）返回空。
func flattenResponsesInstructions(instr any) string {
	if s, ok := instr.(string); ok {
		return s
	}
	return ""
}

// responsesInputToMessages：Responses input（string | item 数组）→ OpenAI messages。
func responsesInputToMessages(input any) ([]map[string]any, error) {
	switch v := input.(type) {
	case nil:
		return nil, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return []map[string]any{{"role": "user", "content": v}}, nil
	case []any:
		var out []map[string]any
		// 并行工具调用：Responses 把一个助手回合的多个 function_call 作为相邻 item 回传。
		// 必须合并进**同一条** assistant 消息的 tool_calls 数组——逐个拆成独立 assistant 消息
		// 会让紧随的 tool 结果消息找不到完整的 tool_calls 归属，上游 chat 400。
		var pendingTools []map[string]any
		flushTools := func() {
			if len(pendingTools) == 0 {
				return
			}
			// 若上一条已是无 tool_calls 的 assistant 消息（其文本回合），挂到它上（chat 允许 content+tool_calls）
			if n := len(out); n > 0 && out[n-1]["role"] == "assistant" {
				if _, has := out[n-1]["tool_calls"]; !has {
					out[n-1]["tool_calls"] = pendingTools
					pendingTools = nil
					return
				}
			}
			out = append(out, map[string]any{"role": "assistant", "content": nil, "tool_calls": pendingTools})
			pendingTools = nil
		}
		for _, raw := range v {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch item["type"] {
			case "function_call": // 助手侧工具调用 → 累积进当前 assistant.tool_calls
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				args, _ := item["arguments"].(string)
				if args == "" {
					args = "{}"
				}
				pendingTools = append(pendingTools, map[string]any{
					"id": callID, "type": "function",
					"function": map[string]any{"name": name, "arguments": args},
				})
			case "function_call_output": // 工具结果 → role:tool（先刷出累积的工具调用）
				flushTools()
				callID, _ := item["call_id"].(string)
				out = append(out, map[string]any{
					"role": "tool", "tool_call_id": callID,
					"content": stringifyResponsesOutput(item["output"]),
				})
			case "item_reference": // 引用已存储的历史 item = 服务端状态 → 拒绝（无状态边界）
				return nil, fmt.Errorf("input item_reference is not supported: this gateway is stateless, resend full input")
			default: // message（含无 type 但有 role 的项）
				flushTools()
				role, _ := item["role"].(string)
				if role == "" {
					role = "user"
				}
				content := responsesContentToOpenAI(item["content"])
				if content == nil {
					continue
				}
				out = append(out, map[string]any{"role": role, "content": content})
			}
		}
		flushTools()
		return out, nil
	default:
		return nil, fmt.Errorf("input must be a string or an array")
	}
}

// responsesContentToOpenAI：message.content（string | part 数组）→ OpenAI content（string 或 parts）。
// input_text→text、input_image→image_url、output_text（历史 assistant 回合）→text。
func responsesContentToOpenAI(content any) any {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []any
		var textOnly []string
		onlyText := true
		for _, raw := range c {
			p, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch p["type"] {
			case "input_text", "output_text", "text":
				if t, ok := p["text"].(string); ok {
					textOnly = append(textOnly, t)
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			case "input_image":
				if url := responsesImageURL(p["image_url"]); url != "" {
					onlyText = false
					ip := map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
					if d, ok := p["detail"].(string); ok && d != "" {
						ip["image_url"].(map[string]any)["detail"] = d
					}
					parts = append(parts, ip)
				}
			}
		}
		if len(parts) == 0 {
			return nil
		}
		if onlyText { // 纯文本塌缩为字符串（与 OpenAI chat 惯例一致）
			return strings.Join(textOnly, "")
		}
		return parts
	}
	return nil
}

// responsesImageURL：input_image.image_url 为裸字符串或 {url:string}。
func responsesImageURL(v any) string {
	switch u := v.(type) {
	case string:
		return u
	case map[string]any:
		s, _ := u["url"].(string)
		return s
	}
	return ""
}

// stringifyResponsesOutput：function_call_output.output（string 或结构）→ tool 消息 content 字符串。
func stringifyResponsesOutput(v any) string {
	switch o := v.(type) {
	case string:
		return o
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// responsesTextFormatToResponseFormat：Responses text.format → OpenAI chat response_format。
func responsesTextFormatToResponseFormat(format any) map[string]any {
	f, ok := format.(map[string]any)
	if !ok {
		return nil
	}
	ftype, _ := f["type"].(string)
	switch ftype {
	case "json_schema":
		js := map[string]any{}
		for _, k := range []string{"name", "schema", "strict", "description"} {
			if v, ok := f[k]; ok {
				js[k] = v
			}
		}
		return map[string]any{"type": "json_schema", "json_schema": js}
	case "json_object", "text":
		return map[string]any{"type": ftype}
	}
	return nil
}

// ════════════════════════ EGRESS：同步转换 ResponseWriter ════════════════════════

// responsesWriter 包住真实 gin.ResponseWriter，把适配器写出的 OpenAI 响应实时转成 Responses 格式。
type responsesWriter struct {
	gin.ResponseWriter
	stream     bool
	status     int
	body       bytes.Buffer // error / 非流模式累积 OpenAI body
	sseStarted bool
	pending    bytes.Buffer
	st         responsesSSE
}

func (w *responsesWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
func (w *responsesWriter) Written() bool                     { return w.sseStarted }
func (w *responsesWriter) Size() int                         { return w.body.Len() }
func (w *responsesWriter) WriteHeader(code int)              { w.status = code }
func (w *responsesWriter) WriteHeaderNow()                   {}
func (w *responsesWriter) Write(b []byte) (int, error)       { return w.ingest(b) }
func (w *responsesWriter) WriteString(s string) (int, error) { return w.ingest([]byte(s)) }

func (w *responsesWriter) Flush() {
	if w.isStreamSuccess() {
		w.pumpSSE()
		w.ResponseWriter.Flush()
	}
}

func (w *responsesWriter) isStreamSuccess() bool { return w.stream && w.Status() < 400 }

func (w *responsesWriter) ingest(b []byte) (int, error) {
	if w.Status() >= 400 {
		return w.body.Write(b)
	}
	if w.isStreamSuccess() {
		w.startSSE()
		n, _ := w.pending.Write(b)
		w.pumpSSE()
		return n, nil
	}
	return w.body.Write(b)
}

func (w *responsesWriter) startSSE() {
	if w.sseStarted {
		return
	}
	w.sseStarted = true
	w.ResponseWriter.WriteHeader(http.StatusOK)
}

// pumpSSE：从 pending 提取完整行，逐个 OpenAI chunk 转 Responses 事件；留尾部不完整行。
func (w *responsesWriter) pumpSSE() {
	for {
		line, err := w.pending.ReadString('\n')
		if err != nil {
			w.pending.Reset()
			w.pending.WriteString(line)
			return
		}
		data, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: ")
		if !ok || data == "" {
			continue
		}
		if data == "[DONE]" {
			w.st.finishStream(w.ResponseWriter)
			continue
		}
		if msg, ok := openaiErrorFrame(data); ok { // 适配器截断注入的 OpenAI error 帧
			w.st.emitError(w.ResponseWriter, msg)
			continue
		}
		w.st.handleChunk(w.ResponseWriter, data)
	}
}

// finalize：HandleChatCompletions 返回后收尾。
func (w *responsesWriter) finalize() {
	switch {
	case w.Status() >= 400:
		w.writeErrorEnvelope()
	case w.stream:
		w.startSSE()
		w.pumpSSE()
		if !w.st.stopped { // 流被截断（无 [DONE]）：补 response.failed，绝不伪造 completed
			w.st.emitError(w.ResponseWriter, "upstream stream ended abnormally")
		}
	default:
		w.writeNonStreamJSON()
	}
}

func (w *responsesWriter) writeErrorEnvelope() {
	msg := "request failed"
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(w.body.Bytes(), &parsed) == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	out, _ := json.Marshal(gin.H{"error": gin.H{
		"type": responsesErrTypeFor(w.Status()), "message": msg, "code": nil, "param": nil}})
	h := w.ResponseWriter.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(out))) // 适配器钉的旧 OpenAI 长度会截断转换后 body（CRITICAL）
	w.ResponseWriter.WriteHeader(w.Status())
	_, _ = w.ResponseWriter.Write(out)
}

func (w *responsesWriter) writeNonStreamJSON() {
	out, ok := openaiRespToResponses(w.body.Bytes())
	h := w.ResponseWriter.Header()
	h.Set("Content-Type", "application/json")
	if !ok {
		errBody, _ := json.Marshal(gin.H{"error": gin.H{
			"type": "api_error", "message": "upstream returned an unparseable response", "code": nil, "param": nil}})
		h.Set("Content-Length", strconv.Itoa(len(errBody)))
		w.ResponseWriter.WriteHeader(http.StatusBadGateway)
		_, _ = w.ResponseWriter.Write(errBody)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(out)))
	w.ResponseWriter.WriteHeader(http.StatusOK)
	_, _ = w.ResponseWriter.Write(out)
}

// openaiRespToResponses：OpenAI chat-completion → Responses 响应 JSON。
func openaiRespToResponses(raw []byte) ([]byte, bool) {
	var resp struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
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
			CompletionTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Choices) == 0 {
		return nil, false
	}
	ch := resp.Choices[0]
	respID := "resp_" + strings.TrimPrefix(resp.ID, "chatcmpl-")
	output := make([]any, 0, 2)
	if ch.Message.Content != "" {
		output = append(output, map[string]any{
			"type": "message", "id": "msg_" + newRequestID(), "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": ch.Message.Content, "annotations": []any{}}},
		})
	}
	for _, tc := range ch.Message.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		output = append(output, map[string]any{
			"type": "function_call", "id": "fc_" + newRequestID(), "status": "completed",
			"call_id": tc.ID, "name": tc.Function.Name, "arguments": args,
		})
	}
	status, incompleteDetails := responsesStatusFor(ch.FinishReason)
	out := map[string]any{
		"id": respID, "object": "response", "created_at": resp.Created,
		"status": status, "model": resp.Model,
		"output": output, "usage": responsesUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
			resp.Usage.PromptTokensDetails.CachedTokens, resp.Usage.CompletionTokensDetails.ReasoningTokens),
		"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{},
		"error": nil, "incomplete_details": incompleteDetails, "metadata": map[string]any{},
	}
	b, err := json.Marshal(out)
	return b, err == nil
}

// responsesStatusFor：chat finish_reason → Responses (status, incomplete_details)。
// length/content_filter 是"未跑完"终态 → status=incomplete + 具体 reason；其余 → completed。
func responsesStatusFor(finishReason string) (string, any) {
	switch finishReason {
	case "length":
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	case "content_filter":
		return "incomplete", map[string]any{"reason": "content_filter"}
	default:
		return "completed", nil
	}
}

// responsesUsage：内部 usage → Responses usage（input_tokens/output_tokens/details）。
// reasoning：思考 token 子集（Gemini thoughts / OpenAI o 系），对抗 review——此前恒 0 与 chat 面回显不一致。
func responsesUsage(prompt, completion, cached, reasoning int64) map[string]any {
	return map[string]any{
		"input_tokens": prompt, "output_tokens": completion, "total_tokens": prompt + completion,
		"input_tokens_details":  map[string]any{"cached_tokens": cached},
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoning},
	}
}

// ════════════════════════ EGRESS SSE 状态机：OpenAI chunk 流 → Responses 事件流 ════════════════════════

type responsesSSE struct {
	seq      int
	created  bool
	id       string
	model    string
	created0 int64

	// message 块
	msgOpen  bool
	msgID    string
	msgIndex int
	text     strings.Builder

	// 工具调用：OpenAI tool index → 状态
	toolItems map[int]*respToolItem
	toolOrder []int

	nextIndex    int
	inTok        int64
	cachedTok    int64
	reasoningTok int64 // 思考 token 子集（末帧 usage 带出，回显进 output_tokens_details）
	outTok       int64
	finish       string
	stopped      bool
}

type respToolItem struct {
	itemIndex int
	id        string // Responses item id（fc_...）
	callID    string // OpenAI tool call id → Responses call_id
	name      string
	args      strings.Builder
}

func (s *responsesSSE) nextSeq() int {
	n := s.seq
	s.seq++
	return n
}

func (s *responsesSSE) respID() string {
	if s.id == "" {
		s.id = "resp_" + newRequestID()
	}
	return s.id
}

// handleChunk：处理一个 OpenAI chunk。
func (s *responsesSSE) handleChunk(w sseFlusher, data string) {
	if s.stopped {
		return
	}
	var ck struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
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
			CompletionTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &ck) != nil {
		return
	}
	if ck.ID != "" && s.id == "" {
		s.id = "resp_" + strings.TrimPrefix(ck.ID, "chatcmpl-")
	}
	if ck.Model != "" {
		s.model = ck.Model
	}
	if ck.Created > 0 {
		s.created0 = ck.Created
	}
	if !s.created {
		s.emitCreated(w)
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
		if ck.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
			s.reasoningTok = ck.Usage.CompletionTokensDetails.ReasoningTokens
		}
	}
	for _, choice := range ck.Choices {
		if choice.Delta.Content != "" {
			s.appendText(w, choice.Delta.Content)
		}
		for _, tc := range choice.Delta.ToolCalls {
			s.appendToolCall(w, tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
		if choice.FinishReason != "" {
			s.finish = choice.FinishReason
		}
	}
	w.Flush()
}

func (s *responsesSSE) emitCreated(w sseFlusher) {
	s.created = true
	s.toolItems = map[int]*respToolItem{}
	writeResponsesEvent(w, "response.created", map[string]any{
		"type": "response.created", "sequence_number": s.nextSeq(),
		"response": map[string]any{
			"id": s.respID(), "object": "response", "created_at": s.created0,
			"status": "in_progress", "model": s.model, "output": []any{}, "usage": nil,
			"error": nil, "incomplete_details": nil,
		},
	})
	writeResponsesEvent(w, "response.in_progress", map[string]any{
		"type": "response.in_progress", "sequence_number": s.nextSeq(),
		"response": map[string]any{"id": s.respID(), "object": "response", "status": "in_progress", "model": s.model},
	})
}

// appendText：文本增量 → 首次开 message item + content_part，随后 output_text.delta。
func (s *responsesSSE) appendText(w sseFlusher, delta string) {
	if !s.msgOpen {
		s.msgOpen = true
		s.msgID = "msg_" + newRequestID()
		s.msgIndex = s.nextIndex
		s.nextIndex++
		writeResponsesEvent(w, "response.output_item.added", map[string]any{
			"type": "response.output_item.added", "sequence_number": s.nextSeq(), "output_index": s.msgIndex,
			"item": map[string]any{"type": "message", "id": s.msgID, "status": "in_progress", "role": "assistant", "content": []any{}},
		})
		writeResponsesEvent(w, "response.content_part.added", map[string]any{
			"type": "response.content_part.added", "sequence_number": s.nextSeq(),
			"item_id": s.msgID, "output_index": s.msgIndex, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		})
	}
	s.text.WriteString(delta)
	writeResponsesEvent(w, "response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "sequence_number": s.nextSeq(),
		"item_id": s.msgID, "output_index": s.msgIndex, "content_index": 0, "delta": delta,
	})
}

// appendToolCall：工具调用增量。首见此 index → 开 function_call item；args 增量 → function_call_arguments.delta。
func (s *responsesSSE) appendToolCall(w sseFlusher, idx int, id, name, args string) {
	it, exists := s.toolItems[idx]
	if !exists {
		it = &respToolItem{itemIndex: s.nextIndex, id: "fc_" + newRequestID(), callID: id, name: name}
		s.nextIndex++
		s.toolItems[idx] = it
		s.toolOrder = append(s.toolOrder, idx)
		writeResponsesEvent(w, "response.output_item.added", map[string]any{
			"type": "response.output_item.added", "sequence_number": s.nextSeq(), "output_index": it.itemIndex,
			"item": map[string]any{"type": "function_call", "id": it.id, "status": "in_progress",
				"call_id": it.callID, "name": it.name, "arguments": ""},
		})
	} else {
		if it.callID == "" && id != "" {
			it.callID = id
		}
		if it.name == "" && name != "" {
			it.name = name
		}
	}
	if args != "" {
		it.args.WriteString(args)
		writeResponsesEvent(w, "response.function_call_arguments.delta", map[string]any{
			"type": "response.function_call_arguments.delta", "sequence_number": s.nextSeq(),
			"item_id": it.id, "output_index": it.itemIndex, "delta": args,
		})
	}
}

// finishStream：收到 [DONE] → 闭合所有开块 + response.completed（携带完整 output + usage）。
func (s *responsesSSE) finishStream(w sseFlusher) {
	if s.stopped {
		return
	}
	if !s.created {
		s.emitCreated(w) // 空流兜底：至少发 created，客户端才认这是一个响应
	}
	output := make([]any, 0, 2)
	// 闭合 message 块
	if s.msgOpen {
		full := s.text.String()
		writeResponsesEvent(w, "response.output_text.done", map[string]any{
			"type": "response.output_text.done", "sequence_number": s.nextSeq(),
			"item_id": s.msgID, "output_index": s.msgIndex, "content_index": 0, "text": full,
		})
		writeResponsesEvent(w, "response.content_part.done", map[string]any{
			"type": "response.content_part.done", "sequence_number": s.nextSeq(),
			"item_id": s.msgID, "output_index": s.msgIndex, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": full, "annotations": []any{}},
		})
		msgItem := map[string]any{"type": "message", "id": s.msgID, "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": full, "annotations": []any{}}}}
		writeResponsesEvent(w, "response.output_item.done", map[string]any{
			"type": "response.output_item.done", "sequence_number": s.nextSeq(),
			"output_index": s.msgIndex, "item": msgItem,
		})
		output = append(output, msgItem)
	}
	// 闭合各 function_call 块（按首见顺序）
	for _, idx := range s.toolOrder {
		it := s.toolItems[idx]
		args := it.args.String()
		if args == "" {
			args = "{}"
		}
		writeResponsesEvent(w, "response.function_call_arguments.done", map[string]any{
			"type": "response.function_call_arguments.done", "sequence_number": s.nextSeq(),
			"item_id": it.id, "output_index": it.itemIndex, "arguments": args,
		})
		fcItem := map[string]any{"type": "function_call", "id": it.id, "status": "completed",
			"call_id": it.callID, "name": it.name, "arguments": args}
		writeResponsesEvent(w, "response.output_item.done", map[string]any{
			"type": "response.output_item.done", "sequence_number": s.nextSeq(),
			"output_index": it.itemIndex, "item": fcItem,
		})
		output = append(output, fcItem)
	}
	inputTokens := s.inTok
	// length/content_filter 终态 → 发 response.incomplete（而非 completed）+ incomplete_details（协议正确性）。
	status, incompleteDetails := responsesStatusFor(s.finish)
	eventType := "response.completed"
	if status == "incomplete" {
		eventType = "response.incomplete"
	}
	writeResponsesEvent(w, eventType, map[string]any{
		"type": eventType, "sequence_number": s.nextSeq(),
		"response": map[string]any{
			"id": s.respID(), "object": "response", "created_at": s.created0,
			"status": status, "model": s.model, "output": output,
			"usage":               responsesUsage(inputTokens, s.outTok, s.cachedTok, s.reasoningTok),
			"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{},
			"error": nil, "incomplete_details": incompleteDetails, "metadata": map[string]any{},
		},
	})
	s.stopped = true
	w.Flush()
}

// emitError：流中途上游错误 → response.failed（或流未开始时的 error 事件），标记 stopped。
func (s *responsesSSE) emitError(w sseFlusher, msg string) {
	if s.stopped {
		return
	}
	if !s.created {
		s.emitCreated(w)
	}
	writeResponsesEvent(w, "response.failed", map[string]any{
		"type": "response.failed", "sequence_number": s.nextSeq(),
		"response": map[string]any{
			"id": s.respID(), "object": "response", "status": "failed",
			"error": map[string]any{"type": "api_error", "code": "upstream_error", "message": msg},
		},
	})
	s.stopped = true
	w.Flush()
}

// writeResponsesEvent：写一条 Responses SSE 事件（event: 行 + data: 行；无 [DONE]，靠 EOF 结束）。
func writeResponsesEvent(w io.Writer, event string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "event: "+event+"\ndata: "+string(raw)+"\n\n")
}
