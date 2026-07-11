package relay

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// G3：system 级 cache_control 必须在 /anthropic 入站→出站往返中存活（否则 Claude Code 的
// 大 system 每轮全价漏缓存）。

// egress：OpenAI canonical system 为 content-parts 带 cache_control → Anthropic system 块形保留。
func TestAnthropicSystemCacheEgress(t *testing.T) {
	ch := &Channel{Provider: "anthropic", BaseURL: "https://api.anthropic.com/v1"}
	req := map[string]any{
		"model":      "claude-3-5-sonnet",
		"max_tokens": float64(100),
		"messages": []any{
			map[string]any{"role": "system", "content": []any{
				map[string]any{"type": "text", "text": "big system prompt", "cache_control": map[string]any{"type": "ephemeral"}},
			}},
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	upReq, err := anthropicAdapter{}.buildRequest(context.Background(), ch, "k", req, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(upReq.Body)
	var b map[string]any
	_ = json.Unmarshal(body, &b)
	sys, ok := b["system"].([]any)
	if !ok {
		t.Fatalf("system must be block array when cache_control present, got %T: %s", b["system"], body)
	}
	blk := sys[0].(map[string]any)
	if blk["text"] != "big system prompt" || blk["cache_control"] == nil {
		t.Fatalf("system cache_control dropped: %v", blk)
	}

	// 零回归：纯字符串 system 仍拍平为字符串
	req["messages"].([]any)[0] = map[string]any{"role": "system", "content": "plain sys"}
	upReq2, _ := anthropicAdapter{}.buildRequest(context.Background(), ch, "k", req, false)
	body2, _ := io.ReadAll(upReq2.Body)
	var b2 map[string]any
	_ = json.Unmarshal(body2, &b2)
	if s, ok := b2["system"].(string); !ok || s != "plain sys" {
		t.Fatalf("plain system must stay string, got %T %v", b2["system"], b2["system"])
	}
}

// ingress：Anthropic system 块带 cache_control → OpenAI canonical 保留 content-parts（不拍平）。
func TestAnthropicSystemCacheIngress(t *testing.T) {
	// 带 cache_control：保留 parts
	got := anthropicSystemToOpenAI([]any{
		map[string]any{"type": "text", "text": "cached sys", "cache_control": map[string]any{"type": "ephemeral"}},
	})
	parts, ok := got.([]any)
	if !ok {
		t.Fatalf("system with cache_control must stay parts, got %T", got)
	}
	p0 := parts[0].(map[string]any)
	if p0["cache_control"] == nil || p0["text"] != "cached sys" {
		t.Fatalf("ingress dropped cache_control: %v", p0)
	}

	// 无 cache_control：join 成字符串（零回归）
	got2 := anthropicSystemToOpenAI([]any{
		map[string]any{"type": "text", "text": "a"},
		map[string]any{"type": "text", "text": "b"},
	})
	if s, ok := got2.(string); !ok || s != "a\n\nb" {
		t.Fatalf("no-cache system must join to string, got %T %v", got2, got2)
	}

	// string 原样；空 → nil
	if anthropicSystemToOpenAI("x") != "x" {
		t.Fatal("string system passthrough failed")
	}
	if anthropicSystemToOpenAI("") != nil || anthropicSystemToOpenAI([]any{}) != nil {
		t.Fatal("empty system must be nil")
	}
}

// response_format × 块形 system 交互（对抗 review CONFIRMED HIGH 回归）：appendSystem 此前对
// []any 断言失败得空串，把整个 system 连缓存断点一起覆盖——必须追加指令块而非覆盖。
func TestAnthropicSystemCacheWithResponseFormat(t *testing.T) {
	ch := &Channel{Provider: "anthropic", BaseURL: "https://api.anthropic.com/v1"}
	req := map[string]any{
		"model":      "claude-3-5-sonnet",
		"max_tokens": float64(100),
		"messages": []any{
			map[string]any{"role": "system", "content": []any{
				map[string]any{"type": "text", "text": "big cached system", "cache_control": map[string]any{"type": "ephemeral"}},
			}},
			map[string]any{"role": "user", "content": "hi"},
		},
		// json_object → appendSystem 指令；json_schema+thinking 同走 appendSystem 路径
		"response_format": map[string]any{"type": "json_object"},
	}
	upReq, err := anthropicAdapter{}.buildRequest(context.Background(), ch, "k", req, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(upReq.Body)
	var b map[string]any
	_ = json.Unmarshal(body, &b)
	sys, ok := b["system"].([]any)
	if !ok || len(sys) != 2 {
		t.Fatalf("system must stay block array with appended instruction, got %T len=%d: %s", b["system"], len(sys), body)
	}
	first := sys[0].(map[string]any)
	if first["text"] != "big cached system" || first["cache_control"] == nil {
		t.Fatalf("original cached block destroyed: %v", first)
	}
	second := sys[1].(map[string]any)
	if s, _ := second["text"].(string); !strings.Contains(s, "JSON") {
		t.Fatalf("instruction block missing: %v", second)
	}
}

// 端到端往返：ingress 保留 → egress 重建，cache_control 全程存活。
func TestAnthropicSystemCacheRoundTrip(t *testing.T) {
	sysContent := anthropicSystemToOpenAI([]any{
		map[string]any{"type": "text", "text": "10k token system", "cache_control": map[string]any{"type": "ephemeral"}},
	})
	req := map[string]any{
		"model": "claude-x", "max_tokens": float64(100),
		"messages": []any{
			map[string]any{"role": "system", "content": sysContent},
			map[string]any{"role": "user", "content": "go"},
		},
	}
	ch := &Channel{Provider: "anthropic", BaseURL: "https://api.anthropic.com/v1"}
	upReq, err := anthropicAdapter{}.buildRequest(context.Background(), ch, "k", req, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(upReq.Body)
	if !json.Valid(body) {
		t.Fatal("invalid body")
	}
	var b map[string]any
	_ = json.Unmarshal(body, &b)
	sys, ok := b["system"].([]any)
	if !ok || sys[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("round-trip lost system cache breakpoint: %s", body)
	}
}
