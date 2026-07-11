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

// anthropicJSONEmuTool：response_format json_schema 模拟的哨兵工具名。buildRequest 注入、
// pipeJSON/pipeSSE 识别后把工具 input 重写回 content——adapter 无状态，哨兵名是唯一的
// 请求↔响应信道。与客户端工具重名概率可忽略（重名时客户端本就带 tools，走降级分支不注入）。
const anthropicJSONEmuTool = "laiai_json_output"

func (anthropicAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	out, err := anthropicRequestBody(req, stream)
	if err != nil {
		return nil, err
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

// anthropicRequestBody 把统一 OpenAI 格式请求转成 Anthropic Messages body（供原生 Anthropic
// 后端与 Vertex Claude 复用——Vertex 调用后删 model/stream 字段、加 anthropic_version）。
func anthropicRequestBody(req map[string]any, stream bool) (map[string]any, error) {
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
					"content":     anthropicContent(m["content"]), // 图片型 tool_result 也转 Claude 块
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
					// assistant 历史也可能带多模态 content（多轮视觉回放）→ 同 user 转 Claude 块
					messages = append(messages, map[string]any{"role": role, "content": anthropicContent(m["content"])})
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
				// user（及未知角色）：content 可能是 OpenAI 多模态数组（text/image_url）→ 转 Claude 块
				messages = append(messages, map[string]any{"role": role, "content": anthropicContent(m["content"])})
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

	// response_format 保真（顶级审计修排名 4）：OpenAI 结构化输出此前对 Claude 后端被静默丢弃
	// ——同一参数在 OpenAI/Gemini 后端生效、Anthropic 后端失效，客户端拿回自由文本（静默=最坏）。
	//   - json_schema：注入哨兵工具 + 强制 tool_choice（OpenRouter 同款模拟），响应侧把该工具的
	//     input 重写回 content（pipeJSON/pipeSSE 的哨兵分支，adapter 无状态——哨兵名即请求↔响应信道）。
	//     客户端自带 tools（强制哨兵会饿死客户端工具）或开 thinking（Claude 禁止 thinking+强制
	//     tool_choice）时降级为 system 指令约束。
	//   - json_object：system 指令（OpenAI 该模式本身即弱约束，无 schema 可强制）。
	if rf, ok := req["response_format"].(map[string]any); ok {
		appendSystem := func(s string) {
			if cur, _ := out["system"].(string); cur != "" {
				out["system"] = cur + "\n\n" + s
			} else {
				out["system"] = s
			}
		}
		switch rf["type"] {
		case "json_object":
			appendSystem("Respond with a single valid JSON object only — no prose, no markdown code fences.")
		case "json_schema":
			var schema any = map[string]any{"type": "object"}
			if js, ok := rf["json_schema"].(map[string]any); ok {
				if s, ok := js["schema"].(map[string]any); ok {
					schema = s
				}
			}
			_, hasTools := out["tools"]
			_, hasThinking := req["thinking"]
			if hasTools || hasThinking {
				b, _ := json.Marshal(schema)
				appendSystem("Your final answer MUST be a single JSON object that validates against this JSON Schema (no prose, no code fences): " + string(b))
			} else {
				out["tools"] = []map[string]any{{
					"name":         anthropicJSONEmuTool,
					"description":  "Record the final answer as structured JSON matching the required schema.",
					"input_schema": schema,
				}}
				// disable_parallel_tool_use：强制单次哨兵调用——否则 Claude 可并行多次调用同一工具，
				// 各自的 input 增量拼接成非法 JSON（对抗 review CONFIRMED，官方推荐单实例结构化提取禁用并行）。
				out["tool_choice"] = map[string]any{"type": "tool", "name": anthropicJSONEmuTool, "disable_parallel_tool_use": true}
			}
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

	// 扩展思考（reasoning）：Claude 原生 thinking 配置透传。Claude 强制 max_tokens > budget_tokens，
	// 故按需上调 max_tokens 防上游 400。OpenAI 的 reasoning_effort 不透传（Claude 不识别该字段，会拒）。
	if th, ok := req["thinking"].(map[string]any); ok {
		out["thinking"] = th
		if bt, ok := th["budget_tokens"].(float64); ok && bt > 0 && maxTokens <= bt {
			bumped := bt + float64(defaultMaxTokens)
			if bumped > float64(maxOutputTokensCeiling) {
				bumped = float64(maxOutputTokensCeiling) // 与 hold 口径一致钳上限
			}
			out["max_tokens"] = bumped
		}
	}

	return out, nil
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`     // 命中缓存的输入（折扣价）
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"` // 写缓存的输入（1.0x 计入 promptTotal，溢价另补——见 cacheWrites）
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"` // TTL 分列明细（新版 API 才有）
}

// cacheWrites：写缓存 token 按 TTL 分列。Anthropic 对 cache_creation 实收 1.25x(5min)/2x(1h)
// 输入价，promptTotal 只计了 1.0x，差额由 ModelPrice.CacheWriteSurcharge 补收。
// 上游无分列明细时全按 5m（1.25x 较低档）——绝不因猜测按 2x 多收客户。
// 明细必须与 legacy 总数对账（对抗 review CONFIRMED）：promptTotal 的 1.0x 基价只计
// cache_creation_input_tokens，坏中转渠道回传"明细和 > 总数"时若全信明细，等于对从未计入
// 基价的幽灵 token 收溢价=多收客户。取计费更低的解释：回退 (总数, 0) 全按 5m 低档。
func (u anthropicUsage) cacheWrites() (w5m, w1h int64) {
	w5m, w1h = u.CacheCreationInputTokens, 0
	if u.CacheCreation != nil && (u.CacheCreation.Ephemeral5m > 0 || u.CacheCreation.Ephemeral1h > 0) {
		w5m, w1h = u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
		if total := u.CacheCreationInputTokens; total > 0 && w5m+w1h > total {
			w5m, w1h = total, 0
		}
	}
	if w5m < 0 {
		w5m = 0
	}
	if w1h < 0 {
		w1h = 0
	}
	return
}

// promptTotal：Anthropic 的 input_tokens 不含缓存读/写部分，总输入需相加（否则少计 prompt）。
func (u anthropicUsage) promptTotal() int64 {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// anthropicContent 把 OpenAI 形态的消息 content 转成 Claude 形态（D1 视觉输入补齐）：
//   - string → 原样（Claude 接受纯字符串 content）
//   - []{type:text|image_url} → []{type:text} / {type:image, source:{...}}
//     image_url 的 data:base64 URI → source{type:base64,media_type,data}；http(s) URL → source{type:url,url}
//     （Claude Messages 原生支持 base64 与 url 两种 image source）
//   - 块上的 cache_control 透传（prompt caching：客户端在块上标记，重建消息时必须保留）
//   - 未知块型原样透传（已是 Claude 形态时不破坏）
func anthropicContent(content any) any {
	arr, ok := content.([]any)
	if !ok {
		return content
	}
	var blocks []any
	for _, raw := range arr {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch p["type"] {
		case "text":
			blk := map[string]any{"type": "text", "text": p["text"]}
			if cc, ok := p["cache_control"]; ok {
				blk["cache_control"] = cc
			}
			blocks = append(blocks, blk)
		case "image_url":
			urlStr := ""
			if iu, ok := p["image_url"].(map[string]any); ok {
				urlStr, _ = iu["url"].(string)
			}
			var src map[string]any
			if mimeType, data, ok := parseDataURI(urlStr); ok {
				src = map[string]any{"type": "base64", "media_type": mimeType, "data": data}
			} else if strings.HasPrefix(urlStr, "http://") || strings.HasPrefix(urlStr, "https://") {
				src = map[string]any{"type": "url", "url": urlStr}
			} else {
				continue // 无法识别的 image_url：丢弃该块而非污染请求
			}
			blk := map[string]any{"type": "image", "source": src}
			if cc, ok := p["cache_control"]; ok {
				blk["cache_control"] = cc
			}
			blocks = append(blocks, blk)
		default:
			blocks = append(blocks, p) // 未知/已是 Claude 块：透传
		}
	}
	if len(blocks) == 0 {
		return content
	}
	return blocks
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
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"` // 扩展思考块（A-2 转换保真）
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
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
	var reasoning strings.Builder // 扩展思考内容 → reasoning_content（A-2）
	var toolCalls []gin.H
	jsonEmu := false
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking) // redacted_thinking（加密）无明文，自然跳过
		case "tool_use":
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			if block.Name == anthropicJSONEmuTool {
				// response_format json_schema 模拟：哨兵工具的 input 就是结构化输出本体，
				// 重写回 content（客户端并没有要工具，它要的是 JSON 内容）。
				text.WriteString(args)
				jsonEmu = true
				continue
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
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String() // OpenAI 兼容推理字段（DeepSeek/OpenRouter 同名）
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if text.Len() == 0 {
			message["content"] = nil // OpenAI 惯例：纯工具调用时 content 为 null
		}
	}
	finishReason := mapStopReason(resp.StopReason)
	// 哨兵是我们注入的工具：对客户端而言应显示为文本收尾。但只把**本应是 tool_use** 的
	// 收尾改写为 stop——绝不掩盖 max_tokens(length) 等真实截断（对抗 review：pipeJSON 此前无条件
	// 覆盖，与 pipeSSE 的条件守卫不一致）。
	if jsonEmu && len(toolCalls) == 0 && finishReason == "tool_calls" {
		finishReason = "stop"
	}
	cw5m, cw1h := resp.Usage.cacheWrites() // 写缓存（对账后口径）：计费与回显同源
	converted := gin.H{
		"id":     resp.ID,
		"object": "chat.completion",
		"model":  resp.Model,
		"choices": []gin.H{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": gin.H{
			"prompt_tokens":         resp.Usage.promptTotal(),
			"completion_tokens":     resp.Usage.OutputTokens,
			"total_tokens":          resp.Usage.promptTotal() + resp.Usage.OutputTokens,
			"prompt_tokens_details": gin.H{"cached_tokens": resp.Usage.CacheReadInputTokens},
			// 扩展键（非 OpenAI 标准，标准客户端忽略）：anthropic_inbound 靠它把写缓存拆回
			// 原生 usage（否则 native 客户端看到 cache_creation 恒 0、被并进 input——回显失真）。
			"cache_creation_input_tokens": cw5m + cw1h,
		},
	}
	c.JSON(http.StatusOK, converted)
	u := usage{PromptTokens: resp.Usage.promptTotal(), CompletionTokens: resp.Usage.OutputTokens, FinishReason: finishReason}
	u.PromptTokensDetails.CachedTokens = resp.Usage.CacheReadInputTokens // 缓存读按折扣价计费（P2-9）
	u.CacheWrite5m, u.CacheWrite1h = cw5m, cw1h                         // 写缓存溢价补收
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
	jsonEmuBlocks := map[int]bool{} // response_format json_schema 模拟：哨兵工具块 → 重写为 content 流
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
				Thinking    string `json:"thinking"` // thinking_delta 增量（A-2）
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
			u.CacheWrite5m, u.CacheWrite1h = ev.Message.Usage.cacheWrites()            // 写缓存溢价补收
			emit(gin.H{"object": "chat.completion.chunk",
				"choices": []gin.H{{"index": 0, "delta": gin.H{"role": "assistant"}}}})
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				if ev.ContentBlock.Name == anthropicJSONEmuTool {
					jsonEmuBlocks[ev.Index] = true // 哨兵：不发 tool_call 起始帧，input 将作为 content 流出
					break
				}
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
			case "thinking_delta": // 扩展思考增量 → reasoning_content 流（A-2）
				if ev.Delta.Thinking != "" {
					emit(gin.H{"object": "chat.completion.chunk",
						"choices": []gin.H{{"index": 0, "delta": gin.H{"reasoning_content": ev.Delta.Thinking}}}})
				}
			case "input_json_delta":
				if jsonEmuBlocks[ev.Index] {
					if ev.Delta.PartialJSON != "" { // 哨兵 input 增量 = 客户端要的 JSON 内容流
						emit(gin.H{"object": "chat.completion.chunk",
							"choices": []gin.H{{"index": 0, "delta": gin.H{"content": ev.Delta.PartialJSON}}}})
					}
					break
				}
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
				if finishReason == "tool_calls" && len(jsonEmuBlocks) > 0 && toolCount == 0 {
					finishReason = "stop" // 只有哨兵工具在跑：对客户端是正常文本收尾
				}
			}
		case "message_stop":
			emit(gin.H{"object": "chat.completion.chunk",
				"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": finishReason}},
				"usage": gin.H{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
					"total_tokens": u.PromptTokens + u.CompletionTokens,
					// 缓存读/写随末帧带出（对抗 review：此前流式丢 cached_tokens，inbound 回显失真）
					"prompt_tokens_details":       gin.H{"cached_tokens": u.PromptTokensDetails.CachedTokens},
					"cache_creation_input_tokens": u.CacheWrite5m + u.CacheWrite1h}})
			_, _ = w.WriteString("data: [DONE]\n\n")
			w.Flush()
		}
	})
	// 排障闭环（0045）：只在真收到过上游 stop_reason 时记 finish_reason；截断/断连（err!=nil，
	// 从未走到 message_delta）留空，不把默认 "stop" 伪造成真实收尾（对抗 review，对齐 openai/gemini 口径）。
	if err == nil {
		u.FinishReason = finishReason
	} else { // 截断流（无 message_stop）：补 error 帧，不可计 success
		emitStreamError(w, "upstream stream ended abnormally")
	}
	return u, outBytes, err
}
