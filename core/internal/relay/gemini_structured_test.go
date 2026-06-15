package relay

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

// WS-1：Gemini 原生通道结构化输出——OpenAI response_format(json_schema) 必须转
// generationConfig.responseMimeType + responseSchema，否则原生 Gemini 渠道丢失结构化能力。
func TestGeminiStructuredOutput(t *testing.T) {
	ch := &Channel{Provider: "gemini", BaseURL: DefaultBaseURLFor("gemini")}
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	req := map[string]any{
		"model":    "gemini-2.0-flash",
		"messages": []any{map[string]any{"role": "user", "content": "give me json"}},
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "person", "schema": schema},
		},
	}
	upReq, err := geminiAdapter{}.buildRequest(context.Background(), ch, "g", req, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(upReq.Body)
	var body struct {
		GenerationConfig map[string]any `json:"generationConfig"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.GenerationConfig["responseMimeType"] != "application/json" {
		t.Fatalf("responseMimeType = %v, want application/json", body.GenerationConfig["responseMimeType"])
	}
	if body.GenerationConfig["responseSchema"] == nil {
		t.Fatal("responseSchema missing")
	}
}
