package relay

import (
	"net/http"
	"reflect"
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
	// 无 override / 解析失败：零拷贝返回**同一个** map（指针恒等，非长度比较——对抗 review 死断言修复）
	if same := applyParamOverride(req, ""); reflect.ValueOf(same).Pointer() != reflect.ValueOf(req).Pointer() {
		t.Fatal("empty override must return the original map (zero-copy)")
	}
	if same := applyParamOverride(req, "not json"); reflect.ValueOf(same).Pointer() != reflect.ValueOf(req).Pointer() {
		t.Fatal("unparseable override must return the original map (defensive)")
	}
}

// delete-only 嵌套 override 在基底缺键时是 no-op——绝不物化 {} 注入请求体（部分上游 400）。
func TestApplyParamOverrideNoEmptyMaterialization(t *testing.T) {
	req := map[string]any{"model": "m"}
	out := applyParamOverride(req, `{"stream_options":{"include_obfuscation":null}}`)
	if _, ok := out["stream_options"]; ok {
		t.Fatalf("delete-only override materialized empty object: %v", out["stream_options"])
	}
	// 嵌套同语义
	req2 := map[string]any{"model": "m", "a": map[string]any{"keep": 1}}
	out2 := applyParamOverride(req2, `{"a":{"b":{"gone":null}}}`)
	if _, ok := out2["a"].(map[string]any)["b"]; ok {
		t.Fatalf("nested delete-only materialized empty object: %v", out2["a"])
	}
	// 混合：有实值的键正常设置
	out3 := applyParamOverride(req, `{"x":{"real":1,"dead":null}}`)
	x := out3["x"].(map[string]any)
	if x["real"] != float64(1) || len(x) != 1 {
		t.Fatalf("mixed prune = %v", x)
	}
}

// 方言别名受保护（对抗 review：openai 系整 map 透传，别名可绕 hold）。
func TestProtectedDialectAliases(t *testing.T) {
	for _, k := range []string{"max_output_tokens", "max_new_tokens", "num_predict", "max_tokens_to_sample", "best_of"} {
		if err := ValidateParamOverride(`{"` + k + `":999999}`); err == nil {
			t.Fatalf("dialect alias %q not protected", k)
		}
		out := applyParamOverride(map[string]any{"model": "m"}, `{"`+k+`":999999}`)
		if _, ok := out[k]; ok {
			t.Fatalf("dialect alias %q applied despite protection", k)
		}
	}
}

// requestedMaxOutput 识别全部别名（hold 估算口径 = 转发体钳制口径 = 受保护键口径）。
func TestRequestedMaxOutputAliases(t *testing.T) {
	for _, k := range outputCapAliases {
		if got := requestedMaxOutput(map[string]any{k: float64(777)}); got != 777 {
			t.Fatalf("alias %q not recognized by hold estimation: %d", k, got)
		}
	}
}

// ensureIncludeUsage：保留兄弟字段 + 强制 include_usage + 不写穿原嵌套 map。
func TestEnsureIncludeUsage(t *testing.T) {
	orig := map[string]any{"include_obfuscation": true, "include_usage": false}
	req := map[string]any{"stream_options": orig}
	ensureIncludeUsage(req)
	so := req["stream_options"].(map[string]any)
	if so["include_usage"] != true || so["include_obfuscation"] != true {
		t.Fatalf("merged stream_options = %v", so)
	}
	if orig["include_usage"] != false {
		t.Fatal("original nested map mutated (would leak across attempts)")
	}
	// 无既有对象：只设 include_usage
	req2 := map[string]any{}
	ensureIncludeUsage(req2)
	if req2["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("bare = %v", req2["stream_options"])
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
	// 认证类头：字面明文拒绝（红线 7 误用路径），占位符/带前缀占位符放行
	if err := ValidateHeaderOverride(`{"X-Api-Key":"sk-live-plaintext"}`); err == nil {
		t.Fatal("plaintext auth header accepted (red line 7 misuse path)")
	}
	if err := ValidateHeaderOverride(`{"x-api-key":"sk-live-plaintext"}`); err == nil {
		t.Fatal("lowercase plaintext auth header accepted")
	}
	if err := ValidateHeaderOverride(`{"Authorization":"Bearer {{credential}}"}`); err != nil {
		t.Fatalf("placeholder auth header rejected: %v", err)
	}
	// 控制字符：过校验后会在 transport 层炸并被误分类，写入即拒
	if err := ValidateHeaderOverride(`{"X-Custom":"a\r\nInjected: x"}`); err == nil {
		t.Fatal("control characters in header value accepted")
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
