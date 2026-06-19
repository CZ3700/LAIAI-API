package relay

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
)

// setClientMeta 采集访客 UA/Referer（GetHeader 确定性）、客户端 IP（非空）、并对超长值按字节截断。
func TestSetClientMeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("User-Agent", "MyAgent/1.0")
	req.Header.Set("Referer", "https://ref.example/")
	c.Request = req

	inv := &invocation.Invocation{}
	setClientMeta(inv, c)
	if inv.UserAgent != "MyAgent/1.0" {
		t.Fatalf("user_agent = %q", inv.UserAgent)
	}
	if inv.Referer != "https://ref.example/" {
		t.Fatalf("referer = %q", inv.Referer)
	}
	if inv.ClientIP == "" {
		t.Fatalf("client_ip empty")
	}

	// 超长 UA 截到 256 字节且仍合法 UTF-8（ASCII 用例：恰好 256）
	req.Header.Set("User-Agent", strings.Repeat("a", 300))
	setClientMeta(inv, c)
	if len(inv.UserAgent) != 256 {
		t.Fatalf("user_agent not clipped: %d bytes", len(inv.UserAgent))
	}
}
