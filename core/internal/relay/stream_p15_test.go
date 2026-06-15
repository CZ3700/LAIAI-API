package relay

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// errAfterData：先吐一段数据，再返回非 EOF 错误（模拟连接中断/读错误）。
type errAfterData struct {
	data string
	sent bool
}

func (e *errAfterData) Read(p []byte) (int, error) {
	if !e.sent {
		e.sent = true
		return copy(p, e.data), nil
	}
	return 0, errors.New("connection reset by peer")
}

func newSSECtx() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	return c, rec
}

// P1-5：流中途读错误 → pipeSSE 返回 errStreamReadFail，并给客户端补 error 帧（不静默当完整流）。
func TestPipeSSEReadErrorReturnsUpstreamError(t *testing.T) {
	c, rec := newSSECtx()
	r := &errAfterData{data: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n"}

	_, _, err := openaiAdapter{}.pipeSSE(c, r)
	if !errors.Is(err, errStreamReadFail) {
		t.Fatalf("pipeSSE err = %v, want errStreamReadFail", err)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("expected SSE error frame, got: %q", rec.Body.String())
	}
}

// P1-5：上游 stall（per-chunk 超时）→ pipeSSE 返回 errStreamStalled。
func TestPipeSSEStallReturnsUpstreamError(t *testing.T) {
	old := StreamChunkTimeout
	StreamChunkTimeout = 50 * time.Millisecond
	defer func() { StreamChunkTimeout = old }()

	c, _ := newSSECtx()
	pr, pw := io.Pipe()
	defer pw.Close() // 收尾解除读 goroutine 阻塞
	// 写一行后不再写、不关闭 → per-chunk 计时器超时
	go func() { _, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n")) }()

	_, _, err := openaiAdapter{}.pipeSSE(c, pr)
	if !errors.Is(err, errStreamStalled) {
		t.Fatalf("pipeSSE err = %v, want errStreamStalled", err)
	}
}

// P1-5：正常 EOF 收尾 → 无错误（不误伤正常流）。
func TestPipeSSENormalEOFNoError(t *testing.T) {
	c, _ := newSSECtx()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n" +
		"data: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5}}\n" +
		"data: [DONE]\n"
	u, _, err := openaiAdapter{}.pipeSSE(c, strings.NewReader(body))
	if err != nil {
		t.Fatalf("normal stream err = %v, want nil", err)
	}
	if u.PromptTokens != 3 || u.CompletionTokens != 5 {
		t.Fatalf("usage = %+v, want 3/5", u)
	}
}

// P1-5：客户端断连（ctx 取消）不算上游错误（用户主动放弃，不应记 upstream_error）。
func TestPipeSSEClientCancelNotUpstreamError(t *testing.T) {
	old := StreamChunkTimeout
	StreamChunkTimeout = 5 * time.Second
	defer func() { StreamChunkTimeout = old }()

	c, _ := newSSECtx()
	ctx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n"))
		time.Sleep(20 * time.Millisecond)
		cancel() // 客户端断连
	}()

	_, _, err := openaiAdapter{}.pipeSSE(c, pr)
	if err != nil {
		t.Fatalf("client-cancel err = %v, want nil (not upstream error)", err)
	}
}
