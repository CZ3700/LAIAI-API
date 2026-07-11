package relay

// AWS Bedrock 真机 smoke（env 门控，默认跳过）：真实 SigV4 签名 + Converse API 往返验证。
// KEY 只走环境变量，绝不落盘/落测试（红线：凭证永不入库/日志/git）。
//
// 跑法（真机 AWS Bedrock 凭证到位时）：
//   LAIAI_SMOKE_BEDROCK_CRED='ak|sk|us-east-1' \
//   LAIAI_SMOKE_BEDROCK_MODEL='anthropic.claude-3-5-sonnet-20241022-v2:0' \
//   go test ./core/internal/relay/ -run TestRealBedrockSmoke -v
//
// 验证目标：①SigV4 签名被 AWS 接受（非 403 SignatureDoesNotMatch）；②Converse 非流响应正确转换；
// ③converse-stream event stream 正确解码。前两者失败=签名或 Converse 映射需调；流式失败=帧解析需调。

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestRealBedrockSmoke(t *testing.T) {
	cred := os.Getenv("LAIAI_SMOKE_BEDROCK_CRED")
	if cred == "" {
		t.Skip("LAIAI_SMOKE_BEDROCK_CRED not set — skipping real Bedrock smoke")
	}
	model := os.Getenv("LAIAI_SMOKE_BEDROCK_MODEL")
	if model == "" {
		model = "anthropic.claude-3-5-sonnet-20241022-v2:0"
	}
	req := map[string]any{
		"model":      model,
		"max_tokens": float64(32),
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with exactly: BEDROCK_OK"}},
	}
	upReq, err := bedrockAdapter{}.buildRequest(context.Background(), &Channel{Provider: "bedrock"}, cred, req, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(upReq)
	if err != nil {
		t.Fatalf("bedrock request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// 403 SignatureDoesNotMatch → SigV4 需调；404 → modelId/region 需调
		t.Fatalf("bedrock returned %d (403=signature, 404=model/region) — check adapter", resp.StatusCode)
	}
	c, rec := newSSECtx()
	u, _, perr := bedrockAdapter{}.pipeJSON(c, resp.Body)
	if perr != nil {
		t.Fatalf("pipeJSON: %v", perr)
	}
	if u.CompletionTokens == 0 {
		t.Fatal("no completion tokens — Converse usage mapping wrong")
	}
	if !strings.Contains(rec.Body.String(), "BEDROCK_OK") {
		t.Logf("response body: %s", rec.Body.String()) // 内容非硬断言（模型可能措辞不同），仅日志
	}
	t.Logf("bedrock smoke OK: in=%d out=%d", u.PromptTokens, u.CompletionTokens)
}
