package task

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// bindCreateReq 大小写语义永久回归（验修残留补锁）：
// ① 变体键兼容旧 gin 结构体绑定（encoding/json 大小写不敏感——{"Model":...} 曾被受理）；
// ② 精确键优先于变体；③ 所有变体从 Params 剔除（否则随透传进上游体，first-wins 解析器注入面）。
func TestBindCreateReqCaseVariants(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mk := func(body string) (*createReq, error) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/video/generations", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return bindCreateReq(c)
	}

	// ① 旧客户端变体键：受理（兼容回归封堵）
	req, err := mk(`{"Model":"m1","Content":[{"type":"text","text":"x"}]}`)
	if err != nil || req.Model != "m1" || len(req.Content) != 1 {
		t.Fatalf("variant keys must bind (legacy compat): %+v err=%v", req, err)
	}
	if req.Params != nil {
		t.Fatalf("variant keys must not leak into Params: %v", req.Params)
	}

	// ② 精确键优先 + ③ 变体剔除、合法参数保留
	req, err = mk(`{"model":"good","content":[{"type":"text","text":"x"}],"Model":"evil","MODEL":"evil2","duration":4}`)
	if err != nil || req.Model != "good" {
		t.Fatalf("exact key must win over variants: model=%q err=%v", req.Model, err)
	}
	if _, leaked := req.Params["Model"]; leaked {
		t.Fatal("variant key Model leaked into Params (upstream first-wins injection surface)")
	}
	if _, leaked := req.Params["MODEL"]; leaked {
		t.Fatal("variant key MODEL leaked into Params")
	}
	if req.Params["duration"] != float64(4) {
		t.Fatalf("legit params must survive: %v", req.Params)
	}

	// 缺 model/content 仍 400 语义
	if _, err := mk(`{"content":[{"type":"text","text":"x"}]}`); err == nil {
		t.Fatal("missing model must error")
	}
}
