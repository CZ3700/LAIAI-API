package relay

import (
	"context"
	"io"
	"net/http"

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

// adapterFor 按渠道 provider 选择适配器；未知 provider 按 OpenAI 兼容处理
// （大量厂商与聚合上游本身就是 OpenAI 兼容协议）。
func adapterFor(provider string) adapter {
	switch provider {
	case "anthropic":
		return anthropicAdapter{}
	default:
		return openaiAdapter{}
	}
}

func sseHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)
}
