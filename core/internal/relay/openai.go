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

// openaiAdapter：OpenAI 兼容上游（含 OpenAI 本尊与众多兼容协议厂商）。
// 请求/响应均为透传；流式注入 stream_options.include_usage（usage 三档抽取第一档）。
type openaiAdapter struct{}

func (openaiAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	if stream {
		ensureIncludeUsage(req) // 保留 override/客户端兄弟字段，强制 include_usage（对抗 review）
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// 端点/鉴权查 (format×mode) 矩阵（contract v2）。openai 家族 chat 走 ch.upstreamURL——
	// 自定义端点路径（0036 advanced_custom）：upstream_path 非空则覆盖默认 /chat/completions。
	spec, err := formatModeSpec("openai", ch, ModeChat, "")
	if err != nil {
		return nil, err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	spec.applyAuth(upReq, credential)
	return upReq, nil
}

func (openaiAdapter) pipeJSON(c *gin.Context, body io.Reader) (usage, int, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return usage{}, 0, fmt.Errorf("%w: %v", errStreamReadFail, err)
	}
	var parsed struct {
		Usage usage `json:"usage"`
	}
	_ = json.Unmarshal(raw, &parsed)
	c.Data(http.StatusOK, "application/json", raw)
	return parsed.Usage, len(raw), nil
}

func (openaiAdapter) pipeSSE(c *gin.Context, body io.Reader) (usage, int, error) {
	sseHeaders(c)
	w := c.Writer

	var u usage
	outBytes := 0
	err := pumpLines(c.Request.Context(), body, func(line string) {
		outBytes += len(line)
		if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
			var chunk struct {
				Usage *usage `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &chunk) == nil && chunk.Usage != nil &&
				(chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
				u = *chunk.Usage
			}
		}
		_, _ = w.WriteString(line + "\n")
		w.Flush()
	})
	if err != nil { // 截断流：补 error 帧，避免客户端把残缺流当完整响应
		emitStreamError(w, "upstream stream ended abnormally")
	}
	return u, outBytes, err
}
