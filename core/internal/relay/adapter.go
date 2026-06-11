package relay

import (
	"bufio"
	"context"
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
	// pipeJSON 读取非流式上游响应，转换为统一格式写给客户端，返回 usage 与响应字节数。
	pipeJSON(c *gin.Context, body io.Reader) (usage, int)
	// pipeSSE 流式透传/转换，返回 usage 与输出字节数。
	pipeSSE(c *gin.Context, body io.Reader) (usage, int)
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

// pumpLines 从上游流逐行读并回调 handle，三路 select：
//   - 新行到达 → 重置超时计时器
//   - per-chunk 超时 → 返回 timedOut=true（上游 stall）
//   - 客户端断连（ctx.Done）→ 停止（省上游 token，知识库 §2.4）
//
// 读 goroutine 在 caller 关闭 resp.Body 后解除阻塞退出，无泄漏。
func pumpLines(ctx context.Context, body io.Reader, handle func(line string)) (timedOut bool) {
	lines := make(chan string, 16)
	stop := make(chan struct{})
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
	}()
	defer close(stop)

	timer := time.NewTimer(StreamChunkTimeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return false // 上游正常结束
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
			return true // 上游 stall
		case <-ctx.Done():
			return false // 客户端断连
		}
	}
}
