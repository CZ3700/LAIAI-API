package relay

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// 注册表完整性：≥15 家主流厂商、每家有默认端点（azure 除外）且解析到已实现的适配器
func TestProviderRegistryCoverage(t *testing.T) {
	providers := SupportedProviders()
	if len(providers) < 15 {
		t.Fatalf("registered providers = %d, want >= 15", len(providers))
	}
	implemented := map[string]bool{"openai": true, "anthropic": true, "gemini": true, "azure": true, "volcengine": true, "openai-responses": true, "bedrock": true, "vertex": true}
	for _, name := range providers {
		p, _ := ProfileFor(name)
		// TaskOnly 厂商（kling/vidu 等）的 Adapter 是 task.Adaptor 家族名，不参与 chat 选路——豁免 chat 适配器断言。
		if !p.TaskOnly && !implemented[p.Adapter] {
			t.Errorf("provider %q maps to unimplemented adapter %q", name, p.Adapter)
		}
		// 空 base 仅允许：azure、档案注明「base_url 必填」的自建/级联厂商（用户手填），
		// 或端点按凭证「自动构造」的厂商（bedrock 按 region）。
		if p.DefaultBaseURL == "" && p.Adapter != "azure" &&
			!strings.Contains(p.Notes, "必填") && !strings.Contains(p.Notes, "自动构造") {
			t.Errorf("provider %q has no default base url", name)
		}
		if p.DisplayName == "" {
			t.Errorf("provider %q has no display name", name)
		}
		// adapterFor 不 panic 且返回可用适配器
		if ad := adapterFor(name); ad == nil {
			t.Errorf("adapterFor(%q) = nil", name)
		}
	}
	// 必备名单（A1 中国系 + 西方主流）一个都不能少
	for _, must := range []string{"openai", "anthropic", "gemini", "azure", "volcengine",
		"deepseek", "qwen", "zhipu", "moonshot", "baidu", "hunyuan", "minimax",
		"xai", "mistral", "groq", "openrouter", "siliconflow", "spark"} {
		if _, ok := ProfileFor(must); !ok {
			t.Errorf("must-have provider %q missing from registry", must)
		}
	}
}

func buildReqHelper(t *testing.T, provider, model string) (urlStr string, headers map[string]string, body map[string]any) {
	t.Helper()
	ch := &Channel{Provider: provider, BaseURL: DefaultBaseURLFor(provider)}
	if ch.BaseURL == "" {
		ch.BaseURL = "https://myres.openai.azure.com" // azure 等需手填
	}
	ad := adapterFor(provider)
	req := map[string]any{"model": model,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	upReq, err := ad.buildRequest(context.Background(), ch, "sk-cred", req, false)
	if err != nil {
		t.Fatalf("buildRequest(%s): %v", provider, err)
	}
	raw, _ := io.ReadAll(upReq.Body)
	parsed := map[string]any{}
	_ = json.Unmarshal(raw, &parsed)
	hdr := map[string]string{}
	for k := range upReq.Header {
		hdr[k] = upReq.Header.Get(k)
	}
	return upReq.URL.String(), hdr, parsed
}

func TestAzureURLAndAuth(t *testing.T) {
	url, hdr, _ := buildReqHelper(t, "azure", "my-gpt4o-deployment")
	if !strings.Contains(url, "/openai/deployments/my-gpt4o-deployment/chat/completions") {
		t.Fatalf("azure url = %s", url)
	}
	if !strings.Contains(url, "api-version="+AzureAPIVersion) {
		t.Fatalf("azure api-version missing: %s", url)
	}
	if hdr["Api-Key"] != "sk-cred" {
		t.Fatalf("azure auth header = %v, want api-key", hdr)
	}
	if hdr["Authorization"] != "" {
		t.Fatalf("azure must not send Authorization: %v", hdr)
	}
}

func TestVolcengineBotRouting(t *testing.T) {
	url, hdr, _ := buildReqHelper(t, "volcengine", "doubao-pro-32k")
	if !strings.HasSuffix(url, "/api/v3/chat/completions") {
		t.Fatalf("volcengine normal url = %s", url)
	}
	if hdr["Authorization"] != "Bearer sk-cred" {
		t.Fatalf("volcengine auth = %v", hdr)
	}
	// bot- 前缀 → /bots/ 路由（知识库 §6.1 烧点 10）
	url, _, _ = buildReqHelper(t, "volcengine", "bot-my-agent")
	if !strings.HasSuffix(url, "/api/v3/bots/chat/completions") {
		t.Fatalf("volcengine bot url = %s", url)
	}
}

func TestGeminiRequestConversionUnit(t *testing.T) {
	ch := &Channel{Provider: "gemini", BaseURL: DefaultBaseURLFor("gemini")}
	req := map[string]any{
		"model": "gemini-pro-test", "max_tokens": float64(300), "temperature": 0.5,
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{"role": "assistant", "content": "hello"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "f1", "description": "d", "parameters": map[string]any{"type": "object"}}}},
		"tool_choice": "required",
	}
	upReq, err := geminiAdapter{}.buildRequest(context.Background(), ch, "g-key", req, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upReq.URL.String(), "/models/gemini-pro-test:streamGenerateContent?alt=sse") {
		t.Fatalf("gemini stream url = %s", upReq.URL.String())
	}
	if upReq.Header.Get("x-goog-api-key") != "g-key" {
		t.Fatalf("gemini auth header missing")
	}
	raw, _ := io.ReadAll(upReq.Body)
	var body struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		GenerationConfig struct {
			MaxOutputTokens float64 `json:"maxOutputTokens"`
		} `json:"generationConfig"`
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		ToolConfig struct {
			FunctionCallingConfig struct {
				Mode string `json:"mode"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.SystemInstruction == nil || body.SystemInstruction.Parts[0].Text != "be brief" {
		t.Fatalf("system not lifted to systemInstruction: %s", raw)
	}
	if len(body.Contents) != 2 || body.Contents[0].Role != "user" || body.Contents[1].Role != "model" {
		t.Fatalf("contents roles wrong (assistant must map to model): %s", raw)
	}
	if body.GenerationConfig.MaxOutputTokens != 300 {
		t.Fatalf("maxOutputTokens = %v", body.GenerationConfig.MaxOutputTokens)
	}
	if len(body.Tools) != 1 || body.Tools[0].FunctionDeclarations[0].Name != "f1" {
		t.Fatalf("functionDeclarations: %s", raw)
	}
	if body.ToolConfig.FunctionCallingConfig.Mode != "ANY" { // required → ANY
		t.Fatalf("toolConfig mode = %s", body.ToolConfig.FunctionCallingConfig.Mode)
	}
}
