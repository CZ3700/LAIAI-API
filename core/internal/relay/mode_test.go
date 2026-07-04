package relay

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func newTestRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://up.example/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// (format×mode) 矩阵表驱动测试（contract v2）：锁死每个方言的 URL/鉴权形态与不支持哨兵。
// 任何"看起来等价"的矩阵改动打破存量渠道请求形态时，这里先红。
func TestFormatModeSpecMatrix(t *testing.T) {
	azureCh := &Channel{Provider: "azure", BaseURL: "https://res.openai.azure.com"}
	openaiCh := &Channel{Provider: "openai", BaseURL: "https://api.openai.com/v1"}
	arkCh := &Channel{Provider: "volcengine", BaseURL: "https://ark.example.com/api/v3"}
	unknownCh := &Channel{Provider: "my-custom-gw", BaseURL: "http://gw.local/v1"}

	cases := []struct {
		name    string
		ch      *Channel
		mode    RelayMode
		upModel string
		wantURL string
		wantAH  string // auth header
		wantAP  string // auth prefix
		wantErr error  // 哨兵错误（等值比较——handler 依赖）
	}{
		// —— azure deployments 方言（此前抄 5 份的那个 switch）——
		{"azure chat", azureCh, ModeChat, "dep1",
			"https://res.openai.azure.com/openai/deployments/dep1/chat/completions?api-version=" + AzureAPIVersion, "api-key", "", nil},
		{"azure embeddings", azureCh, ModeEmbeddings, "emb-dep",
			"https://res.openai.azure.com/openai/deployments/emb-dep/embeddings?api-version=" + AzureAPIVersion, "api-key", "", nil},
		{"azure images gen", azureCh, ModeImagesGenerations, "dalle",
			"https://res.openai.azure.com/openai/deployments/dalle/images/generations?api-version=" + AzureAPIVersion, "api-key", "", nil},
		{"azure images edits", azureCh, ModeImagesEdits, "dalle",
			"https://res.openai.azure.com/openai/deployments/dalle/images/edits?api-version=" + AzureAPIVersion, "api-key", "", nil},
		{"azure audio speech", azureCh, ModeAudioSpeech, "tts",
			"https://res.openai.azure.com/openai/deployments/tts/audio/speech?api-version=" + AzureAPIVersion, "api-key", "", nil},
		{"azure audio stt", azureCh, ModeAudioTranscriptions, "whisper",
			"https://res.openai.azure.com/openai/deployments/whisper/audio/transcriptions?api-version=" + AzureAPIVersion, "api-key", "", nil},
		// 不变量②：azure 无 rerank 端点 → 沿现状落 openai 默认形态（Bearer）
		{"azure rerank falls to default", azureCh, ModeRerank, "",
			"https://res.openai.azure.com/rerank", "Authorization", "Bearer ", nil},

		// —— openai 兼容默认 ——
		{"openai chat", openaiCh, ModeChat, "",
			"https://api.openai.com/v1/chat/completions", "Authorization", "Bearer ", nil},
		{"openai embeddings", openaiCh, ModeEmbeddings, "text-embedding-3-small",
			"https://api.openai.com/v1/embeddings", "Authorization", "Bearer ", nil},
		{"unknown provider default", unknownCh, ModeEmbeddings, "m",
			"http://gw.local/v1/embeddings", "Authorization", "Bearer ", nil},
		{"unknown provider audio", unknownCh, ModeAudioTranslations, "m",
			"http://gw.local/v1/audio/translations", "Authorization", "Bearer ", nil},

		// —— volcengine 方言：bot- 前缀 chat 路由（不变量④）——
		{"volcengine chat bot", arkCh, ModeChat, "bot-abc",
			"https://ark.example.com/api/v3/bots/chat/completions", "Authorization", "Bearer ", nil},
		{"volcengine chat normal", arkCh, ModeChat, "seed-1.6",
			"https://ark.example.com/api/v3/chat/completions", "Authorization", "Bearer ", nil},
		{"volcengine embeddings", arkCh, ModeEmbeddings, "emb",
			"https://ark.example.com/api/v3/embeddings", "Authorization", "Bearer ", nil},

		// —— cohere rerank 原生端点方言（不变量③）——
		{"cohere rerank strips compat", &Channel{Provider: "cohere", BaseURL: "https://api.cohere.ai/compatibility/v1"}, ModeRerank, "",
			"https://api.cohere.ai/v1/rerank", "Authorization", "Bearer ", nil},

		// —— anthropic/gemini：非 chat 不支持（不变量⑤：哨兵等值）——
		{"anthropic embeddings unsupported", &Channel{Provider: "anthropic", BaseURL: "x"}, ModeEmbeddings, "", "", "", "", errEmbeddingsUnsupported},
		{"gemini rerank unsupported", &Channel{Provider: "gemini", BaseURL: "x"}, ModeRerank, "", "", "", "", errRerankUnsupported},
		{"anthropic images unsupported", &Channel{Provider: "anthropic", BaseURL: "x"}, ModeImagesVariations, "", "", "", "", errImagesUnsupported},
		{"gemini audio unsupported", &Channel{Provider: "gemini", BaseURL: "x"}, ModeAudioSpeech, "", "", "", "", errAudioUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := modeUpstreamSpec(tc.ch, tc.mode, tc.upModel)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want sentinel %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if spec.URL != tc.wantURL {
				t.Fatalf("URL = %q, want %q", spec.URL, tc.wantURL)
			}
			if spec.AuthHeader != tc.wantAH || spec.AuthPrefix != tc.wantAP {
				t.Fatalf("auth = (%q,%q), want (%q,%q)", spec.AuthHeader, spec.AuthPrefix, tc.wantAH, tc.wantAP)
			}
		})
	}
}

// 不变量①：UpstreamPath（0036）只对 openai 家族 chat 生效；非 chat 模态与其他家族照旧忽略。
func TestFormatModeSpecUpstreamPathScope(t *testing.T) {
	ch := &Channel{Provider: "advanced_custom", BaseURL: "http://gw.local", UpstreamPath: "/api/custom/chat"}
	// openai 家族 chat：upstream_path 生效（advanced_custom 档案 Adapter=openai）
	spec, err := formatModeSpec("openai", ch, ModeChat, "")
	if err != nil || spec.URL != "http://gw.local/api/custom/chat" {
		t.Fatalf("openai chat with upstream_path = %q (err %v), want custom path honored", spec.URL, err)
	}
	// 同渠道 embeddings：upstream_path 忽略（现状保真——统一生效=存量渠道 URL 突变）
	spec, err = modeUpstreamSpec(ch, ModeEmbeddings, "m")
	if err != nil || spec.URL != "http://gw.local/embeddings" {
		t.Fatalf("embeddings with upstream_path = %q (err %v), want default path (upstream_path ignored)", spec.URL, err)
	}
	// volcengine chat：upstream_path 忽略（此前 adapter 手拼 base+path，不走 upstreamURL）
	arkCh := &Channel{Provider: "volcengine", BaseURL: "http://ark.local/api/v3", UpstreamPath: "/weird"}
	spec, err = formatModeSpec("volcengine", arkCh, ModeChat, "m")
	if err != nil || spec.URL != "http://ark.local/api/v3/chat/completions" {
		t.Fatalf("volcengine chat with upstream_path = %q (err %v), want upstream_path ignored", spec.URL, err)
	}
}

// multipart 模态识别（header override 的 Content-Type 保护面）。
func TestMultipartModes(t *testing.T) {
	multipart := []RelayMode{ModeImagesEdits, ModeImagesVariations, ModeAudioTranscriptions, ModeAudioTranslations}
	plain := []RelayMode{ModeChat, ModeEmbeddings, ModeRerank, ModeImagesGenerations, ModeAudioSpeech}
	for _, m := range multipart {
		if !m.multipartMode() {
			t.Fatalf("%s must be multipart", m)
		}
	}
	for _, m := range plain {
		if m.multipartMode() {
			t.Fatalf("%s must not be multipart", m)
		}
	}
}

// applyHeaderOverrideForMode：multipart 模态 Content-Type 防御性跳过；其余键正常生效。
func TestHeaderOverrideMultipartProtection(t *testing.T) {
	override := `{"Content-Type":"application/json","X-Api-Key":"{{credential}}","Authorization":null}`
	// multipart：Content-Type 不被覆盖，其余键生效
	req := newTestRequest(t)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc123")
	req.Header.Set("Authorization", "Bearer orig")
	applyHeaderOverrideForMode(req, override, "sk-real", ModeImagesEdits)
	if got := req.Header.Get("Content-Type"); !strings.Contains(got, "boundary=abc123") {
		t.Fatalf("multipart Content-Type overridden: %q — boundary 被打碎", got)
	}
	if req.Header.Get("X-Api-Key") != "sk-real" {
		t.Fatalf("X-Api-Key = %q, want credential injected", req.Header.Get("X-Api-Key"))
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("Authorization null-delete must still apply on multipart mode")
	}
	// 非 multipart：Content-Type 可覆盖（与 chat 同语义）
	req2 := newTestRequest(t)
	req2.Header.Set("Content-Type", "application/json")
	applyHeaderOverrideForMode(req2, `{"Content-Type":"application/json; charset=utf-8"}`, "k", ModeEmbeddings)
	if req2.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("non-multipart Content-Type override must apply, got %q", req2.Header.Get("Content-Type"))
	}
}
