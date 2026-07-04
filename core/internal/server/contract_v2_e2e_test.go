package server

// Adapter contract v2 第一期 e2e（商用迭代指令 §3.1；设计 §4.4）：
// 验收——header_override（{{credential}} 自定义认证）作用域扩展到非 chat 模态；
// multipart 入口 Content-Type 防御性跳过（boundary 不被打碎）；
// (format×mode) 矩阵在真实 handler 链路上生效（azure embeddings deployments 形态）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 自定义认证渠道服务 embeddings：X-Api-Key {{credential}} 注入 + Authorization null 删除。
// contract v2 之前 header_override 只在 chat 生效——自定义认证上游的 embeddings 恒 401。
func TestEmbeddingsHeaderOverrideCustomAuth(t *testing.T) {
	srv, _ := setupRelayServer(t)
	var gotAuth, gotXApiKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotXApiKey = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, embeddingsFixture)
	}))
	defer up.Close()

	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "embed-custom-auth", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"embed-test"},
		"header_override": `{"X-Api-Key":"{{credential}}","Authorization":null}`,
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "embed-test", "input_usd_per_1m": "0.10", "output_usd_per_1m": "0",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, _ := seedUserWithCredit(t, srv, "embed-hovr@example.com", "10")

	w, resp := srv.do(t, "POST", "/v1/embeddings", apiKey, map[string]any{
		"model": "embed-test", "input": "hello world",
	})
	if w.Code != 200 {
		t.Fatalf("embeddings = %d %v", w.Code, resp)
	}
	if gotXApiKey != "sk-upstream-secret" {
		t.Fatalf("X-Api-Key = %q, want credential injected via placeholder", gotXApiKey)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want deleted by null override", gotAuth)
	}
}

// multipart 入口（/v1/images/edits）的 header override：自定义头生效，但 Content-Type
// 防御性跳过——运营误配 Content-Type 覆盖不得打碎 multipart boundary（上游仍可解析表单）。
func TestImageEditsHeaderOverrideMultipartProtected(t *testing.T) {
	srv, _ := setupRelayServer(t)
	var gotModel, gotTrace, gotCT string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotTrace = r.Header.Get("X-Trace")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(400) // boundary 被打碎会走到这
			return
		}
		gotModel = r.FormValue("model")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"url": "a"}}})
	}))
	defer up.Close()

	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "imgedit-hovr", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"img-test"},
		// 运营配了自定义头 + 一个会打碎 multipart 的 Content-Type 覆盖（必须被防御性跳过）
		"header_override": `{"X-Trace":"laiai","Content-Type":"application/json"}`,
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "img-test", "input_usd_per_1m": "0", "output_usd_per_1m": "0", "image_usd": "0.04",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, _ := seedUserWithCredit(t, srv, "imgedit-hovr@example.com", "10")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "img-test")
	_ = mw.WriteField("prompt", "add a hat")
	fw, _ := mw.CreateFormFile("image", "x.png")
	_, _ = fw.Write([]byte("PNGDATA"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	srv.engine.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("edits = %d %s", rec.Code, rec.Body.String())
	}
	if gotTrace != "laiai" {
		t.Fatalf("X-Trace = %q, want custom header applied on multipart mode", gotTrace)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Fatalf("upstream Content-Type = %q — override 打碎了 multipart（必须被防御性跳过）", gotCT)
	}
	if gotModel != "img-test" {
		t.Fatalf("upstream model = %q, want form parsed intact", gotModel)
	}
}

// azure 家族 embeddings 走 deployments 形态 + api-key 头（矩阵在真实链路生效；
// 此前该方言在 5 个 builder 各抄一份，本用例锁 e2e 行为）。
func TestEmbeddingsAzureDialect(t *testing.T) {
	srv, _ := setupRelayServer(t)
	var gotPath, gotAPIKey, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAPIKey, gotAuth = r.URL.Path, r.Header.Get("api-key"), r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, embeddingsFixture)
	}))
	defer up.Close()

	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "azure-embed", "provider": "azure", "base_url": up.URL,
		"api_key": "azure-key-1", "models": []string{"embed-test"},
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "embed-test", "input_usd_per_1m": "0.10", "output_usd_per_1m": "0",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, _ := seedUserWithCredit(t, srv, "azure-embed@example.com", "10")

	w, resp := srv.do(t, "POST", "/v1/embeddings", apiKey, map[string]any{
		"model": "embed-test", "input": "hello",
	})
	if w.Code != 200 {
		t.Fatalf("embeddings = %d %v", w.Code, resp)
	}
	if gotPath != "/openai/deployments/embed-test/embeddings" {
		t.Fatalf("azure path = %q, want deployments dialect", gotPath)
	}
	if gotAPIKey != "azure-key-1" || gotAuth != "" {
		t.Fatalf("azure auth: api-key=%q Authorization=%q, want api-key only", gotAPIKey, gotAuth)
	}
}
