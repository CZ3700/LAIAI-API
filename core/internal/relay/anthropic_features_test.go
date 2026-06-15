package relay

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

// WS-1：Anthropic 原生通道视觉输入——OpenAI image_url 必须转 Claude image source 块，
// 否则 Claude /messages 拒收。data:URI→base64 source；http URL→url source；cache_control 透传。
func TestAnthropicVisionAndCacheControl(t *testing.T) {
	ch := &Channel{Provider: "anthropic", BaseURL: DefaultBaseURLFor("anthropic")}
	req := map[string]any{
		"model": "claude-3-5-sonnet",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "describe", "cache_control": map[string]any{"type": "ephemeral"}},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64,/9j/AAAA"}},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://ex.com/a.png"}},
			}},
		},
	}
	upReq, err := anthropicAdapter{}.buildRequest(context.Background(), ch, "k", req, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(upReq.Body)
	var body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(body.Messages))
	}
	blocks := body.Messages[0].Content
	if len(blocks) != 3 {
		t.Fatalf("content blocks = %d, want 3 (text+2 images)", len(blocks))
	}
	// 文本块 + cache_control 透传
	if blocks[0]["type"] != "text" || blocks[0]["cache_control"] == nil {
		t.Fatalf("text block missing/cache_control dropped: %v", blocks[0])
	}
	// base64 image source
	src0, _ := blocks[1]["source"].(map[string]any)
	if blocks[1]["type"] != "image" || src0["type"] != "base64" || src0["media_type"] != "image/jpeg" || src0["data"] != "/9j/AAAA" {
		t.Fatalf("base64 image block wrong: %v", blocks[1])
	}
	// url image source
	src1, _ := blocks[2]["source"].(map[string]any)
	if blocks[2]["type"] != "image" || src1["type"] != "url" || src1["url"] != "https://ex.com/a.png" {
		t.Fatalf("url image block wrong: %v", blocks[2])
	}
}

// thinking 透传 + max_tokens 按 budget 上调（Claude 强制 max_tokens > budget_tokens）。
func TestAnthropicThinkingPassthrough(t *testing.T) {
	ch := &Channel{Provider: "anthropic", BaseURL: DefaultBaseURLFor("anthropic")}
	req := map[string]any{
		"model":      "claude-3-7-sonnet",
		"max_tokens": float64(1000), // < budget，应被上调
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": float64(8000)},
	}
	upReq, err := anthropicAdapter{}.buildRequest(context.Background(), ch, "k", req, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(upReq.Body)
	var body struct {
		MaxTokens float64        `json:"max_tokens"`
		Thinking  map[string]any `json:"thinking"`
	}
	_ = json.Unmarshal(raw, &body)
	if body.Thinking == nil || body.Thinking["type"] != "enabled" {
		t.Fatalf("thinking not passed through: %v", body.Thinking)
	}
	if body.MaxTokens != 8000+4096 { // 钉死上调公式 budget+defaultMaxTokens，而非仅验方向（防"恰好越过 1"退化）
		t.Fatalf("max_tokens=%v, want budget+default=12096", body.MaxTokens)
	}
}
