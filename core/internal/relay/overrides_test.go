package relay

import (
	"net/http"
	"testing"
)

// param override 合并语义：增/删/嵌套合并/嵌套删/受保护键跳过/原 map 不动。
func TestApplyParamOverride(t *testing.T) {
	req := map[string]any{
		"model":    "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream_options": map[string]any{
			"include_usage": true, "include_obfuscation": true,
		},
		"parallel_tool_calls": true,
	}

	out := applyParamOverride(req, `{
		"service_tier": "flex",
		"reasoning": {"effort": "low"},
		"parallel_tool_calls": null,
		"stream_options": {"include_obfuscation": null},
		"max_tokens": 999999,
		"model": "evil"
	}`)

	// 增：上游私有字段
	if out["service_tier"] != "flex" {
		t.Fatalf("service_tier = %v", out["service_tier"])
	}
	if out["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("reasoning = %v", out["reasoning"])
	}
	// 删：顶层 null
	if _, ok := out["parallel_tool_calls"]; ok {
		t.Fatalf("parallel_tool_calls not deleted: %v", out)
	}
	// 嵌套删：只删 include_obfuscation，include_usage 保留
	so := out["stream_options"].(map[string]any)
	if _, ok := so["include_obfuscation"]; ok {
		t.Fatalf("nested delete failed: %v", so)
	}
	if so["include_usage"] != true {
		t.Fatalf("sibling nested key lost: %v", so)
	}
	// 受保护键：max_tokens/model 被跳过（资金钳制/路由保护）
	if _, ok := out["max_tokens"]; ok {
		t.Fatalf("protected max_tokens applied: %v", out["max_tokens"])
	}
	if out["model"] != "m" {
		t.Fatalf("protected model overridden: %v", out["model"])
	}
	// 原 map 绝不改动（跨 attempt/跨渠道无残留）
	if _, ok := req["service_tier"]; ok {
		t.Fatal("original request mutated (service_tier)")
	}
	if req["parallel_tool_calls"] != true {
		t.Fatal("original request mutated (parallel_tool_calls)")
	}
	if req["stream_options"].(map[string]any)["include_obfuscation"] != true {
		t.Fatal("original nested map mutated")
	}
	// 无 override：零拷贝返回原 map
	if same := applyParamOverride(req, ""); &same == nil || len(same) != len(req) {
		t.Fatal("empty override changed request")
	}
}

// header override：设置/删除/{{credential}} 占位/禁改头跳过。
func TestApplyHeaderOverride(t *testing.T) {
	upReq, _ := http.NewRequest("POST", "https://up.example/v1/chat/completions", nil)
	upReq.Header.Set("Authorization", "Bearer sk-default")
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Host = "up.example"

	applyHeaderOverride(upReq, `{
		"X-Upstream-Tenant": "acme",
		"Authorization": null,
		"X-Api-Key": "{{credential}}",
		"Host": "evil.example",
		"Content-Length": "1"
	}`, "sk-real-secret")

	if got := upReq.Header.Get("X-Upstream-Tenant"); got != "acme" {
		t.Fatalf("set header = %q", got)
	}
	if got := upReq.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization not deleted: %q", got)
	}
	if got := upReq.Header.Get("X-Api-Key"); got != "sk-real-secret" {
		t.Fatalf("credential placeholder = %q", got)
	}
	if upReq.Host != "up.example" || upReq.Header.Get("Host") != "" {
		t.Fatalf("forbidden Host applied")
	}
	if got := upReq.Header.Get("Content-Length"); got != "" {
		t.Fatalf("forbidden Content-Length applied: %q", got)
	}
}

// 校验：合法/非法 JSON/受保护键/禁改头/非法头值。
func TestValidateOverrides(t *testing.T) {
	if err := ValidateParamOverride(""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateParamOverride(`{"service_tier":"flex","store":null}`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateParamOverride(`not json`); err == nil {
		t.Fatal("invalid json accepted")
	}
	if err := ValidateParamOverride(`{"max_tokens":1}`); err == nil {
		t.Fatal("protected key accepted")
	}
	if err := ValidateParamOverride(`{"n":5}`); err == nil {
		t.Fatal("protected n accepted")
	}
	if err := ValidateHeaderOverride(`{"X-Custom":"v","Authorization":null}`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHeaderOverride(`{"host":"x"}`); err == nil {
		t.Fatal("forbidden Host (lowercase) accepted")
	}
	if err := ValidateHeaderOverride(`{"X-N":123}`); err == nil {
		t.Fatal("non-string header value accepted")
	}
}

// 审计 keys：稳定排序、绝不含值。
func TestOverrideKeys(t *testing.T) {
	keys := OverrideKeys(`{"b":"secret-value","a":null}`)
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("keys = %v", keys)
	}
	if OverrideKeys("") != nil {
		t.Fatal("empty should be nil")
	}
}
