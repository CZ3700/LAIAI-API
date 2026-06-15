package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// P2-10：provider 归一到协议族。
func TestProbeFamily(t *testing.T) {
	cases := map[string]string{
		"anthropic": "anthropic",
		"gemini":    "gemini",
		"openai":    "openai",
		"deepseek":  "openai", // 未登记/OpenAI 兼容
		"":          "openai",
	}
	for in, want := range cases {
		if got := probeFamily(in); got != want {
			t.Errorf("probeFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

// P2-10：按族解析模型列表——gemini {models:[{name:"models/x"}]} 去前缀；其余 {data:[{id}]}。
func TestParseModelList(t *testing.T) {
	gem, err := parseModelList("gemini", []byte(`{"models":[{"name":"models/gemini-2.0-flash"},{"name":"models/gemini-1.5-pro"}]}`))
	if err != nil || len(gem) != 2 || gem[0] != "gemini-2.0-flash" {
		t.Fatalf("gemini parse = %v err=%v", gem, err)
	}
	oai, err := parseModelList("openai", []byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
	if err != nil || len(oai) != 2 || oai[0] != "gpt-4o" {
		t.Fatalf("openai parse = %v err=%v", oai, err)
	}
	ant, err := parseModelList("anthropic", []byte(`{"data":[{"id":"claude-opus-4"}]}`))
	if err != nil || len(ant) != 1 || ant[0] != "claude-opus-4" {
		t.Fatalf("anthropic parse = %v err=%v", ant, err)
	}
}

// P2-10：fetchModels 按族用对的鉴权——gemini 用 ?key= 查询参数、anthropic 用 x-api-key 头。
func TestFetchModelsProviderAuth(t *testing.T) {
	// gemini：要求 query 带 key，返回 gemini 形态
	gem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "gk" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-x"}]}`))
	}))
	defer gem.Close()

	// anthropic：要求 x-api-key 头，返回 {data:[{id}]}
	ant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "ak" || r.Header.Get("anthropic-version") == "" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-x"}]}`))
	}))
	defer ant.Close()

	r := &Relay{Client: http.DefaultClient}
	if m, _, _, err := r.fetchModels(context.Background(), gem.URL, "gk", "gemini"); err != nil || len(m) != 1 || m[0] != "gemini-x" {
		t.Fatalf("gemini fetch = %v err=%v", m, err)
	}
	if m, _, _, err := r.fetchModels(context.Background(), ant.URL, "ak", "anthropic"); err != nil || len(m) != 1 || m[0] != "claude-x" {
		t.Fatalf("anthropic fetch = %v err=%v", m, err)
	}
}
