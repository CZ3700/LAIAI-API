package relay

import (
	"strings"
	"testing"
)

// WS-5 修复回归：cohere rerank 必须归一到原生 /v1/rerank（不能命中 /compatibility/v1/rerank → 404）。
func TestRerankUpstreamURLCohere(t *testing.T) {
	ch := &Channel{Provider: "cohere", BaseURL: DefaultBaseURLFor("cohere")}
	url, ah, _, err := rerankUpstreamURL(ch, "k")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(url, "/compatibility") {
		t.Fatalf("cohere rerank url must not contain /compatibility: %s", url)
	}
	if !strings.HasSuffix(url, "/v1/rerank") {
		t.Fatalf("cohere rerank url = %s, want .../v1/rerank", url)
	}
	if ah != "Authorization" {
		t.Fatalf("cohere auth header = %s", ah)
	}

	// jina/siliconflow：base 已含 /v1 → base + /rerank = /v1/rerank
	for _, p := range []string{"siliconflow", "jina"} {
		c := &Channel{Provider: p, BaseURL: DefaultBaseURLFor(p)}
		u, _, _, e := rerankUpstreamURL(c, "k")
		if e != nil || !strings.HasSuffix(u, "/v1/rerank") {
			t.Fatalf("%s rerank url = %s err=%v, want .../v1/rerank", p, u, e)
		}
	}

	// anthropic/gemini 无 rerank 端点 → 报错
	for _, p := range []string{"anthropic", "gemini"} {
		if _, _, _, e := rerankUpstreamURL(&Channel{Provider: p, BaseURL: "x"}, "k"); e == nil {
			t.Fatalf("%s rerank must be unsupported", p)
		}
	}
}
