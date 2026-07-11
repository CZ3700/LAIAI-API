package relay

// Vertex 适配器单测：凭证解析、Claude/Gemini 端点与 body 方言、响应侧委托路由。
// token 换取走 SA JSON 的 token_uri（指向本地 mock），不触真网络、不用真凭证。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// vertexMockCred 造 `region|<sa_json>` 凭证，SA 的 token_uri 指向返回固定 token 的 mock。
func vertexMockCred(t *testing.T, region string) (cred string, close func()) {
	t.Helper()
	gcpTokenCache = map[string]gcpCachedToken{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ya29.vtx","expires_in":3600}`))
	}))
	saJSON, _ := testSAJSON(t, srv.URL)
	return region + "|" + saJSON, srv.Close
}

func TestParseVertexCred(t *testing.T) {
	cred, done := vertexMockCred(t, "us-east5")
	defer done()
	region, sa, err := parseVertexCred(cred)
	if err != nil || region != "us-east5" || sa.ProjectID != "my-proj" {
		t.Fatalf("region=%q sa=%v err=%v", region, sa, err)
	}
	if _, _, err := parseVertexCred("no-pipe"); err == nil {
		t.Fatal("missing sa json should error")
	}
	if _, _, err := parseVertexCred("us-east5|"); err == nil {
		t.Fatal("empty sa json should error")
	}
}

func TestVertexBuildRequestClaude(t *testing.T) {
	cred, done := vertexMockCred(t, "us-east5")
	defer done()
	ch := &Channel{Provider: "vertex"}
	req := map[string]any{
		"model":      "claude-3-5-sonnet-v2@20241022",
		"max_tokens": float64(256),
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	// 非流式
	upReq, err := vertexAdapter{}.buildRequest(context.Background(), ch, cred, req, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	u := upReq.URL.String()
	if !strings.Contains(u, "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/") {
		t.Fatalf("claude endpoint = %s", u)
	}
	if !strings.Contains(u, ":rawPredict") || strings.Contains(u, "stream") {
		t.Fatalf("claude non-stream suffix = %s", u)
	}
	if upReq.Header.Get("Authorization") != "Bearer ya29.vtx" {
		t.Fatalf("auth = %q", upReq.Header.Get("Authorization"))
	}
	body, _ := io.ReadAll(upReq.Body)
	var b map[string]any
	if json.Unmarshal(body, &b) != nil {
		t.Fatalf("body not json: %s", body)
	}
	if b["anthropic_version"] != vertexAnthropicVersion {
		t.Fatalf("anthropic_version = %v", b["anthropic_version"])
	}
	if _, ok := b["model"]; ok {
		t.Fatalf("Vertex Claude body 不应带 model（在 URL 里）: %s", body)
	}
	if _, ok := b["stream"]; ok {
		t.Fatalf("Vertex Claude body 不应带 stream（端点决定）: %s", body)
	}
	if _, ok := b["messages"].([]any); !ok {
		t.Fatalf("missing messages: %s", body)
	}

	// 流式：streamRawPredict?alt=sse
	upReq2, _ := vertexAdapter{}.buildRequest(context.Background(), ch, cred, req, true)
	if u2 := upReq2.URL.String(); !strings.Contains(u2, ":streamRawPredict") || !strings.Contains(u2, "alt=sse") {
		t.Fatalf("claude stream suffix = %s", u2)
	}
}

func TestVertexBuildRequestGemini(t *testing.T) {
	cred, done := vertexMockCred(t, "us-central1")
	defer done()
	ch := &Channel{Provider: "vertex"}
	req := map[string]any{
		"model":      "gemini-1.5-pro",
		"max_tokens": float64(256),
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	upReq, err := vertexAdapter{}.buildRequest(context.Background(), ch, cred, req, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	u := upReq.URL.String()
	if !strings.Contains(u, "/publishers/google/models/gemini-1.5-pro:generateContent") {
		t.Fatalf("gemini endpoint = %s", u)
	}
	if !strings.Contains(u, "us-central1-aiplatform.googleapis.com") {
		t.Fatalf("region endpoint = %s", u)
	}
	if upReq.Header.Get("Authorization") != "Bearer ya29.vtx" {
		t.Fatalf("auth = %q", upReq.Header.Get("Authorization"))
	}
	body, _ := io.ReadAll(upReq.Body)
	var b map[string]any
	_ = json.Unmarshal(body, &b)
	if _, ok := b["contents"].([]any); !ok {
		t.Fatalf("gemini body missing contents: %s", body)
	}
	if _, ok := b["anthropic_version"]; ok {
		t.Fatalf("gemini body 不应带 anthropic_version: %s", body)
	}

	upReq2, _ := vertexAdapter{}.buildRequest(context.Background(), ch, cred, req, true)
	if u2 := upReq2.URL.String(); !strings.Contains(u2, ":streamGenerateContent") || !strings.Contains(u2, "alt=sse") {
		t.Fatalf("gemini stream suffix = %s", u2)
	}
}

// 响应侧委托：按上游 model 名选 anthropic/gemini 解析器（跨 claude/gemini 重命名不错配）。
func TestVertexPipeJSONDelegation(t *testing.T) {
	// Claude 上游名 → anthropic 解析器
	c, rec := newSSECtx()
	c.Set(respModelKey, "outward-name")                    // 对外名故意与上游归属不同
	c.Set(respUpstreamModelKey, "claude-3-5-sonnet@x")     // 上游是 Claude
	antBody := `{"type":"message","role":"assistant","content":[{"type":"text","text":"hi from claude"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`
	u, _, err := vertexAdapter{}.pipeJSON(c, strings.NewReader(antBody))
	if err != nil {
		t.Fatalf("pipeJSON claude: %v", err)
	}
	if u.PromptTokens != 7 || u.CompletionTokens != 3 {
		t.Fatalf("claude usage = %+v", u)
	}
	if !strings.Contains(rec.Body.String(), "hi from claude") {
		t.Fatalf("claude text missing: %s", rec.Body.String())
	}

	// Gemini 上游名 → gemini 解析器
	c2, rec2 := newSSECtx()
	c2.Set(respUpstreamModelKey, "gemini-1.5-pro")
	gemBody := `{"candidates":[{"content":{"parts":[{"text":"hi from gemini"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`
	u2, _, err := vertexAdapter{}.pipeJSON(c2, strings.NewReader(gemBody))
	if err != nil {
		t.Fatalf("pipeJSON gemini: %v", err)
	}
	if u2.PromptTokens != 5 || u2.CompletionTokens != 2 {
		t.Fatalf("gemini usage = %+v", u2)
	}
	if !strings.Contains(rec2.Body.String(), "hi from gemini") {
		t.Fatalf("gemini text missing: %s", rec2.Body.String())
	}
}
