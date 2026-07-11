package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// adapter：厂商协议适配器（设计 §4.4）。对外统一 OpenAI 格式，adapter 负责双向转换。
// 新增厂商 = 新增一个实现 + adapterFor 注册 + 真实 raw response fixture 测试（红线 4 泛化）。
type adapter interface {
	// buildRequest 把统一格式请求转换为上游请求（含端点、headers、鉴权）。
	buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error)
	// pipeJSON 读取非流式上游响应，转换为统一格式写给客户端，返回 usage、响应字节数、
	// 以及流终止错误（读体失败=上游异常；nil=正常）。
	pipeJSON(c *gin.Context, body io.Reader) (usage, int, error)
	// pipeSSE 流式透传/转换，返回 usage、输出字节数、以及流终止错误
	// （stall/读错误=上游异常，不可计为 success；nil=正常收尾）。
	pipeSSE(c *gin.Context, body io.Reader) (usage, int, error)
}

// 流终止异常分类（P1-5）：用于把"已发 200 头但中途 stall/读错误"的截断流判为上游错误，
// 而非误记为 success。客户端断连（ctx.Done）不算上游错误（用户主动放弃）。
var (
	errStreamStalled  = errors.New("relay: upstream stream stalled (per-chunk timeout)")
	errStreamReadFail = errors.New("relay: upstream stream read failed")
	// errUpstreamReported：上游在流内显式上报失败（如 Responses 的 response.failed 事件）。
	// 与截断同类——不可计为 success，交由 relay 记 StatusUpstreamError。
	errUpstreamReported = errors.New("relay: upstream reported failure mid-stream")
)

// emitStreamError 在流中途异常时给客户端补一个 OpenAI 形态的 error 帧 + [DONE]，
// 避免客户端把截断的流误当完整响应（与前端 Playground 的 error 帧探测一致）。
func emitStreamError(w gin.ResponseWriter, msg string) {
	b, _ := json.Marshal(gin.H{"error": gin.H{"message": msg, "type": "upstream_error"}})
	_, _ = w.WriteString("data: " + string(b) + "\n\n")
	_, _ = w.WriteString("data: [DONE]\n\n")
	w.Flush()
}

// converter 枚举（advanced_custom，0036）：出站 openai chat → 上游格式，复用既有 adapter 家族。
const (
	ConverterNone          = "none"
	ConverterToAnthropic   = "openai_chat_to_anthropic_messages"
	ConverterToGemini      = "openai_chat_to_gemini_generate_content"
	ConverterToOpenAIResp  = "openai_chat_to_openai_responses"
	ProviderAdvancedCustom = "advanced_custom"
)

// ValidConverter：converter 是否合法（空 = 不用 converter，按 provider）。
func ValidConverter(c string) bool {
	switch c {
	case "", ConverterNone, ConverterToAnthropic, ConverterToGemini, ConverterToOpenAIResp:
		return true
	}
	return false
}

// adapterForChannel：渠道级适配器选择。converter 非空（advanced_custom）→ 按 converter 分发；
// 否则回退按 provider（adapterFor）。
func adapterForChannel(ch *Channel) adapter {
	switch ch.Converter {
	case "", ConverterNone:
		if ch.Converter == ConverterNone { // 显式 none：openai 透传（advanced_custom 常用）
			return openaiAdapter{}
		}
		return adapterFor(ch.Provider)
	case ConverterToAnthropic:
		return anthropicAdapter{}
	case ConverterToGemini:
		return geminiAdapter{}
	case ConverterToOpenAIResp:
		return openaiResponsesAdapter{}
	default:
		return adapterFor(ch.Provider) // 未知 converter：保守回退 provider（admin 写入已校验）
	}
}

// adapterFor 按渠道 provider 选择适配器：先查档案注册表（providers.go）的 Adapter 字段，
// 未登记的 provider 按 OpenAI 兼容处理（自建/聚合上游大多说 OpenAI 方言）。
func adapterFor(provider string) adapter {
	name := provider
	if p, ok := ProfileFor(provider); ok {
		name = p.Adapter
	}
	switch name {
	case "anthropic":
		return anthropicAdapter{}
	case "gemini":
		return geminiAdapter{}
	case "azure":
		return azureAdapter{}
	case "volcengine":
		return volcengineChatAdapter{}
	case "bedrock":
		return bedrockAdapter{}
	case "vertex":
		return vertexAdapter{}
	case "openai-responses":
		return openaiResponsesAdapter{}
	default:
		return openaiAdapter{}
	}
}

func sseHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)
}

// StreamChunkTimeout：流式 per-chunk 超时（知识库 §2.4：每收到一行重置，
// 不是全局超时——全局超时会杀死合法的长思考响应）。测试可缩短。
var StreamChunkTimeout = 90 * time.Second

// pumpLines 从上游流逐行读并回调 handle，三路 select，返回流终止错误（P1-5）：
//   - 上游正常 EOF        → nil
//   - 中途读错误(sc.Err)  → errStreamReadFail 包装（截断流，不可计 success）
//   - per-chunk 超时       → errStreamStalled（上游 stall）
//   - 客户端断连(ctx.Done) → nil（用户主动放弃，非上游错误）
//
// 读 goroutine 在 caller 关闭 resp.Body 后解除阻塞退出，无泄漏。
func pumpLines(ctx context.Context, body io.Reader, handle func(line string)) error {
	lines := make(chan string, 16)
	stop := make(chan struct{})
	var readErr error // 仅在扫描循环自然结束时写；close(lines) 前赋值，建立 happens-before
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-stop:
				return
			}
		}
		readErr = sc.Err() // EOF → nil；连接中断/读错误 → 非 nil
	}()
	defer close(stop)

	timer := time.NewTimer(StreamChunkTimeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				if readErr != nil {
					return fmt.Errorf("%w: %v", errStreamReadFail, readErr)
				}
				return nil // 上游正常结束
			}
			handle(line)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(StreamChunkTimeout)
		case <-timer.C:
			return errStreamStalled // 上游 stall
		case <-ctx.Done():
			return nil // 客户端断连：非上游错误
		}
	}
}
