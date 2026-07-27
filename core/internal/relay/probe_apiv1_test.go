package relay

// /api/v1 前缀网关的探测支持（真实案例：MaaS 类聚合站把 OpenAI 兼容面挂在 /api 下，
// 用户只填主域名时此前三个候选全 404、真实端点 /api/v1/models 从未被试到）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestCandidateBaseURLsIncludesAPIV1(t *testing.T) {
	got := candidateBaseURLs("https://maas.example.top")
	if !slices.Contains(got, "https://maas.example.top/api/v1") {
		t.Fatalf("候选必须含 /api/v1 前缀约定，实际 = %v", got)
	}
	// 既有约定不得丢失（零回归）
	for _, want := range []string{
		"https://maas.example.top/v1",
		"https://maas.example.top",
		"https://maas.example.top/openai/v1",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("既有候选 %q 丢失，实际 = %v", want, got)
		}
	}

	// 用户已给到 /api：首候选即 /api/v1，不得再拼出 /api/api/v1
	got2 := candidateBaseURLs("https://maas.example.top/api")
	if got2[0] != "https://maas.example.top/api/v1" {
		t.Fatalf("给 /api 时首候选应为 /api/v1，实际 = %v", got2)
	}
	for _, c := range got2 {
		if strings.Contains(c, "/api/api/") {
			t.Fatalf("不应拼出重复 /api 段：%v", got2)
		}
	}

	// 已含显式版本段：只用原值（零回归）
	if got3 := candidateBaseURLs("https://api.deepseek.com/v1"); len(got3) != 1 || got3[0] != "https://api.deepseek.com/v1" {
		t.Fatalf("显式 /v1 应只用原值，实际 = %v", got3)
	}
}

// 端到端：网关**只**在 /api/v1/models 提供模型列表（其余路径 404），且响应带扩展字段
// （runtimeType/iconSlug 等）与非 ASCII owned_by —— 探测必须命中并解析出模型 id。
func TestProbeChannelAPIV1Gateway(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		if r.URL.Path != "/api/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// 真实上游响应形态（含 OpenAI 标准字段之外的扩展字段，解析须容忍）
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"doubao-seed-2-0-pro-260215",` +
			`"object":"model","created":1777007970,"owned_by":"火山引擎",` +
			`"runtimeType":"bifrost","iconSlug":"doubao"}]}`))
	}))
	defer srv.Close()

	r := &Relay{Client: srv.Client()}
	// 只给主域名（不含任何版本段）——正是此前失败的输入形态
	res := r.ProbeChannel(context.Background(), "sk-test", srv.URL, "openai", false)
	if !res.OK {
		t.Fatalf("探测应命中 /api/v1，实际失败：%s（试过 %v）", res.Message, hits)
	}
	if !strings.HasSuffix(res.DetectedBaseURL, "/api/v1") {
		t.Fatalf("命中的 base_url = %q，应为 …/api/v1", res.DetectedBaseURL)
	}
	if len(res.Models) != 1 || res.Models[0] != "doubao-seed-2-0-pro-260215" {
		t.Fatalf("模型解析 = %v，应解出扩展字段响应里的 id", res.Models)
	}
}
