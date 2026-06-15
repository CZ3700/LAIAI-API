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
	// 深比对：证明取的是内层 schema 而非 json_schema 包裹层（弱 nil 断言会放过取错层级的回归）
	rs, ok := body.GenerationConfig["responseSchema"].(map[string]any)
	if !ok {
		t.Fatalf("responseSchema not an object: %v", body.GenerationConfig["responseSchema"])
	}
	if rs["type"] != "object" {
		t.Fatalf("responseSchema.type=%v, want object (可能误取了 json_schema 包裹层)", rs["type"])
	}
	if _, leaked := rs["name"]; leaked {
		t.Fatal("responseSchema 含 name 字段——误取了 json_schema 包裹层而非内层 schema")
	}
}

// HIGH 修复回归：Gemini responseSchema 必须递归剥离 JSON-Schema 专有关键字
// （additionalProperties/title/$schema），否则原生 Gemini 渠道 400；且不得原地改客户端 schema。
func TestGeminiSchemaSanitized(t *testing.T) {
	ch := &Channel{Provider: "gemini", BaseURL: DefaultBaseURLFor("gemini")}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"title":                "Person",
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "title": "Name"},
		},
	}
	req := map[string]any{
		"model":    "gemini-2.0-flash",
		"messages": []any{map[string]any{"role": "user", "content": "x"}},
		"response_format": map[string]any{
			"type": "json_schema", "json_schema": map[string]any{"name": "p", "schema": schema},
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
		t.Fatal(err)
	}
	rs := body.GenerationConfig["responseSchema"].(map[string]any)
	for _, bad := range []string{"additionalProperties", "title", "$schema"} {
		if _, present := rs[bad]; present {
			t.Fatalf("top-level %q not stripped from responseSchema", bad)
		}
	}
	props := rs["properties"].(map[string]any)
	nameProp := props["name"].(map[string]any)
	if _, present := nameProp["title"]; present {
		t.Fatal("nested title not stripped — 递归清洗未生效")
	}
	// 深拷贝：客户端原始 schema 不被原地修改
	if _, ok := schema["additionalProperties"]; !ok {
		t.Fatal("client schema mutated in place — 必须深拷贝")
	}
}

// json_object 分支：只设 responseMimeType、不设 responseSchema（防两分支写串）。
func TestGeminiJSONObject(t *testing.T) {
	ch := &Channel{Provider: "gemini", BaseURL: DefaultBaseURLFor("gemini")}
	req := map[string]any{
		"model":           "gemini-2.0-flash",
		"messages":        []any{map[string]any{"role": "user", "content": "give me json"}},
		"response_format": map[string]any{"type": "json_object"},
	}
	upReq, err := geminiAdapter{}.buildRequest(context.Background(), ch, "g", req, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(upReq.Body)
	var body struct {
		GenerationConfig map[string]any `json:"generationConfig"`
	}
	json.Unmarshal(raw, &body)
	if body.GenerationConfig["responseMimeType"] != "application/json" {
		t.Fatalf("responseMimeType = %v", body.GenerationConfig["responseMimeType"])
	}
	if _, ok := body.GenerationConfig["responseSchema"]; ok {
		t.Fatal("json_object 不应设 responseSchema")
	}
}
