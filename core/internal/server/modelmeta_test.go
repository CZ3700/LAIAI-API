package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/relay"
)

// 模型元数据：upsert → 出现在公开定价 + 管理目录；超长字段拒。
func TestAdminModelMeta(t *testing.T) {
	srv, db := setupRelayServer(t)
	// 渠道(活跃) + 定价，让 gpt-4 在公开 /api/pricing 可见
	db.Exec(`INSERT INTO channels (name,provider,base_url,credential_enc,models,status) VALUES ('c','openai','http://x','e','gpt-4',1)`)
	db.Exec(`INSERT INTO model_prices (model,input_usd_per_1m,output_usd_per_1m) VALUES ('gpt-4','1','2')`)

	w, out := srv.doAdmin(t, "POST", "/api/admin/model-meta", map[string]any{
		"model": "gpt-4", "vendor": "OpenAI", "category": "chat",
		"tags": "vision,tools", "description": "flagship", "context_length": 128000, "icon": "openai",
	})
	if w.Code != 200 {
		t.Fatalf("upsert meta = %d %v", w.Code, out)
	}

	// 公开定价带上元数据
	_, pr := srv.do(t, "GET", "/api/pricing", "", nil)
	models := pr["models"].([]any)
	var found map[string]any
	for _, m := range models {
		if mm := m.(map[string]any); mm["model"] == "gpt-4" {
			found = mm
		}
	}
	if found == nil || found["vendor"] != "OpenAI" || found["category"] != "chat" || int(found["context_length"].(float64)) != 128000 {
		t.Fatalf("pricing meta not surfaced: %v", found)
	}

	// 管理目录带上 meta
	_, cat := srv.doAdmin(t, "GET", "/api/admin/model-catalog", nil)
	cmodels := cat["models"].([]any)
	metaOK := false
	for _, m := range cmodels {
		mm := m.(map[string]any)
		if mm["model"] == "gpt-4" {
			if meta, ok := mm["meta"].(map[string]any); ok && meta["vendor"] == "OpenAI" {
				metaOK = true
			}
		}
	}
	if !metaOK {
		t.Fatal("catalog meta not surfaced")
	}

	// 超长 vendor（>64 runes）→ 400
	if w, _ := srv.doAdmin(t, "POST", "/api/admin/model-meta", map[string]any{
		"model": "gpt-4", "vendor": strings.Repeat("x", 65),
	}); w.Code != 400 {
		t.Fatalf("overlong vendor = %d, want 400", w.Code)
	}
	// 空 model → 400
	if w, _ := srv.doAdmin(t, "POST", "/api/admin/model-meta", map[string]any{"model": "  "}); w.Code != 400 {
		t.Fatalf("empty model = %d, want 400", w.Code)
	}
}

// 自动拉模型 + 缺失检测：拉上游模型，与渠道已配/平台定价做差集。
func TestAdminFetchChannelModels(t *testing.T) {
	srv, db := setupRelayServer(t)
	// 模拟上游 /v1/models（OpenAI 兼容）
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4"},{"id":"gpt-5"}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer upstream.Close()

	cred, err := testKeyring(t).Encrypt("sk-test")
	if err != nil {
		t.Fatal(err)
	}
	ch := relay.Channel{
		Name: "c1", Provider: "openai", BaseURL: upstream.URL,
		CredentialEnc: cred, Models: "gpt-4", Status: 1,
	}
	if e := db.Create(&ch).Error; e != nil {
		t.Fatalf("create channel: %v", e)
	}
	// gpt-4 已定价，gpt-5 未定价
	db.Exec(`INSERT INTO model_prices (model,input_usd_per_1m,output_usd_per_1m) VALUES ('gpt-4','1','2')`)

	_, out := srv.doAdmin(t, "GET", "/api/admin/channels/"+strconv.FormatUint(ch.ID, 10)+"/fetch-models", nil)
	if out["ok"] != true {
		t.Fatalf("fetch-models not ok: %v", out)
	}
	if int(out["upstream_count"].(float64)) != 2 {
		t.Fatalf("upstream_count = %v, want 2", out["upstream_count"])
	}
	// gpt-5 上游有、渠道未配
	missing := toStrSet(out["missing_in_config"])
	if !missing["gpt-5"] || missing["gpt-4"] {
		t.Fatalf("missing_in_config = %v, want only gpt-5", out["missing_in_config"])
	}
	// gpt-5 未定价
	unpriced := toStrSet(out["unpriced"])
	if !unpriced["gpt-5"] || unpriced["gpt-4"] {
		t.Fatalf("unpriced = %v, want only gpt-5", out["unpriced"])
	}
}

func toStrSet(v any) map[string]bool {
	m := map[string]bool{}
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok {
				m[s] = true
			}
		}
	}
	return m
}
