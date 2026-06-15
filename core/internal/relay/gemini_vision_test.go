package relay

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

// D1-a：Gemini 原生通道的视觉输入——OpenAI image_url 的 data: URI 必须转成 inlineData，不再丢图。
func TestGeminiVisionInputInlineData(t *testing.T) {
	ch := &Channel{Provider: "gemini", BaseURL: DefaultBaseURLFor("gemini")}
	req := map[string]any{
		"model": "gemini-2.0-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "what is in this image"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgoAAAA="}},
			}},
		},
	}
	upReq, err := geminiAdapter{}.buildRequest(context.Background(), ch, "g-key", req, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(upReq.Body)
	var body struct {
		Contents []struct {
			Role  string           `json:"role"`
			Parts []map[string]any `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(body.Contents))
	}
	var foundText, foundImage bool
	for _, p := range body.Contents[0].Parts {
		if _, ok := p["text"]; ok {
			foundText = true
		}
		if inl, ok := p["inlineData"].(map[string]any); ok {
			foundImage = true
			if inl["mimeType"] != "image/png" || inl["data"] != "iVBORw0KGgoAAAA=" {
				t.Fatalf("inlineData = %v, want png + base64 data", inl)
			}
		}
	}
	if !foundText || !foundImage {
		t.Fatalf("parts missing: text=%v image=%v (image must not be dropped)", foundText, foundImage)
	}
}

func TestParseDataURI(t *testing.T) {
	mime, data, ok := parseDataURI("data:image/jpeg;base64,/9j/4AAQ=")
	if !ok || mime != "image/jpeg" || data != "/9j/4AAQ=" {
		t.Fatalf("parseDataURI = %q %q %v", mime, data, ok)
	}
	if _, _, ok := parseDataURI("https://example.com/a.png"); ok {
		t.Fatal("http url must not parse as data URI")
	}
	if _, _, ok := parseDataURI("data:image/png,notbase64"); ok {
		t.Fatal("non-base64 data URI must not parse")
	}
}
