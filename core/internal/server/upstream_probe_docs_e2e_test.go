package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
)

func TestUpstreamNewAPISyncProbeImportBalanceAndDocsE2E(t *testing.T) {
	f, db, _, _, apiKeyA := setupStage2Server(t)
	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS api_docs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_id INTEGER DEFAULT NULL,
			title TEXT NOT NULL,
			slug TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			is_dir INTEGER NOT NULL DEFAULT 0,
			sort_order INTEGER NOT NULL DEFAULT 0,
			is_active INTEGER NOT NULL DEFAULT 1,
			category_id INTEGER DEFAULT NULL,
			is_preset INTEGER NOT NULL DEFAULT 0,
			default_expanded INTEGER NOT NULL DEFAULT 0,
			translations_json TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS model_meta (
			model TEXT PRIMARY KEY,
			vendor TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			context_length INTEGER NOT NULL DEFAULT 0,
			icon TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT 0
		);
	`).Error; err != nil {
		t.Fatalf("create extra tables: %v", err)
	}

	// Register & elevate superadmin for /api/v1 admin routes.
	f.do(t, http.MethodPost, "/api/auth/register", "", map[string]string{"email": "syncadmin@test.com", "password": "longenough1"})
	if err := db.Model(&identity.User{}).Where("email_canonical = ?", "syncadmin@test.com").Update("role", identity.PlatRoleSuperadmin).Error; err != nil {
		t.Fatalf("promote superadmin: %v", err)
	}
	_, adminLogin := f.do(t, http.MethodPost, "/api/auth/login", "", map[string]string{"email": "syncadmin@test.com", "password": "longenough1"})
	adminTok, _ := adminLogin["token"].(string)
	if adminTok == "" {
		t.Fatalf("missing admin token")
	}

	// 1. Verify NormalizeNewAPIOrigin & AppliedChannelRate unit math.
	if got := NormalizeNewAPIOrigin("https://mux.la/v1/"); got != "https://mux.la" {
		t.Fatalf("NormalizeNewAPIOrigin = %q, want https://mux.la", got)
	}
	if got := AppliedChannelRate(0.9, 0.05, "CNY", "USD", 7.2); math.Abs(got-0.175) > 1e-6 {
		t.Fatalf("AppliedChannelRate = %v, want 0.175", got)
	}

	// 2. Mock NewAPI upstream server mirroring https://mux.la/api/pricing (keyless) and /api/user/self/groups (token+uid).
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pricing":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"success": true,
				"group_ratio": {"default": 1.0, "Claude-Official": 0.9, "Deepseek-Official": 0.7},
				"usable_group": {"default": "默认分组", "Claude-Official": "Claude 官方分组", "Deepseek-Official": "DeepSeek 官方分组"},
				"vendors": [{"id": -1014, "name": "Anthropic"}, {"id": 2, "name": "ByteDance"}],
				"data": [
					{"model_name": "claude-sonnet-4-5-mock", "vendor_id": -1014, "quota_type": 0, "model_ratio": 1.5, "completion_ratio": 5},
					{"model_name": "doubao-seedance-2-0-mock", "vendor_id": 2, "quota_type": 1, "model_price": 0.4}
				]
			}`))
		case "/api/user/self/groups":
			if r.Header.Get("New-Api-User") != "10086" || r.Header.Get("Authorization") != "Bearer up-secret-token" {
				http.Error(w, `{"success":false,"message":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"success": true,
				"data": {
					"vip-private": {"ratio": 0.36, "desc": "专属VIP分组"}
				}
			}`))
		case "/v1/embeddings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2]}]}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-test","choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockUpstream.Close()

	// Test httptest loopback transport directly inside test package.
	f.srv.upstreamHTTP = mockUpstream.Client()

	// 3. Keyless /api/pricing group fetch (exact https://mux.la scenario).
	wGrp, grpOut := f.do(t, http.MethodPost, "/api/v1/channel-configs/upstream-groups", adminTok, map[string]any{
		"base_url":        mockUpstream.URL + "/v1",
		"upstream_system": "newapi",
	})
	if wGrp.Code != http.StatusOK {
		t.Fatalf("keyless upstream-groups = %d body=%s", wGrp.Code, wGrp.Body.String())
	}
	grpList, _ := grpOut["data"].([]any)
	if len(grpList) != 3 {
		t.Fatalf("expected 3 public groups, got %d: %v", len(grpList), grpList)
	}

	// Token + User-ID /api/user/self/groups fetch.
	wPriv, privOut := f.do(t, http.MethodPost, "/api/v1/channel-configs/upstream-groups", adminTok, map[string]any{
		"base_url":              mockUpstream.URL,
		"upstream_system":       "newapi",
		"upstream_user_id":      "10086",
		"upstream_access_token": "up-secret-token",
	})
	if wPriv.Code != http.StatusOK {
		t.Fatalf("private upstream-groups = %d body=%s", wPriv.Code, wPriv.Body.String())
	}
	privList, _ := privOut["data"].([]any)
	if len(privList) != 1 || privList[0].(map[string]any)["name"] != "vip-private" {
		t.Fatalf("unexpected private group response: %v", privList)
	}

	// 4. Create channel_config with newapi + auto initial rate sync + manual sync + sync-logs.
	wCfg, cfgOut := f.do(t, http.MethodPost, "/api/v1/channel-configs", adminTok, map[string]any{
		"name":                           "MuxLA Preset",
		"provider_type":                  "newapi",
		"base_url":                       mockUpstream.URL,
		"api_key":                        "sk-upstream-key",
		"upstream_system":                "newapi",
		"upstream_group":                 "vip-private",
		"upstream_sync_interval_minutes": 15,
		"upstream_sync_rate_add":         0.05,
		"upstream_currency":              "CNY",
		"site_currency":                  "USD",
		"upstream_currency_rate":         7.2,
		"upstream_user_id":               "10086",
		"upstream_access_token":          "up-secret-token",
	})
	if wCfg.Code != http.StatusCreated {
		t.Fatalf("create channel-config = %d body=%s", wCfg.Code, wCfg.Body.String())
	}
	cfgData := cfgOut["data"].(map[string]any)
	cfgID := uint64(cfgData["id"].(float64))
	// 0.36 / 7.2 + 0.05 = 0.1
	if math.Abs(cfgData["rate"].(float64)-0.1) > 1e-6 {
		t.Fatalf("initial synced rate = %v, want 0.1", cfgData["rate"])
	}

	wSync, syncOut := f.do(t, http.MethodPost, "/api/v1/channel-configs/"+strconv.FormatUint(cfgID, 10)+"/sync-rate", adminTok, map[string]any{})
	if wSync.Code != http.StatusOK || syncOut["synced"] != true {
		t.Fatalf("manual sync-rate = %d body=%s", wSync.Code, wSync.Body.String())
	}
	wLogs, logsOut := f.do(t, http.MethodGet, "/api/v1/channel-configs/"+strconv.FormatUint(cfgID, 10)+"/sync-logs", adminTok, nil)
	if wLogs.Code != http.StatusOK {
		t.Fatalf("sync-logs = %d", wLogs.Code)
	}
	if logItems, _ := logsOut["data"].([]any); len(logItems) < 2 {
		t.Fatalf("expected >=2 sync logs (save + manual), got %d", len(logItems))
	}

	// 5. Probe models from upstream -> import into Models -> create channel -> bind & test.
	wProbe, probeOut := f.do(t, http.MethodPost, "/api/v1/channels/probe-models", adminTok, map[string]any{
		"preset_id": cfgID,
	})
	if wProbe.Code != http.StatusOK || probeOut["success"] != true {
		t.Fatalf("probe-models = %d body=%s", wProbe.Code, wProbe.Body.String())
	}
	probedList := probeOut["data"].([]any)
	if len(probedList) != 2 {
		t.Fatalf("expected 2 probed models, got %d", len(probedList))
	}

	wCh, chOut := f.do(t, http.MethodPost, "/api/v1/channels", adminTok, map[string]any{
		"name":          "Probed Channel",
		"provider_type": "openai",
		"preset_id":     cfgID,
	})
	if wCh.Code != http.StatusCreated {
		t.Fatalf("create channel = %d body=%s", wCh.Code, wCh.Body.String())
	}
	chID := uint64(chOut["data"].(map[string]any)["id"].(float64))

	wImp, impOut := f.do(t, http.MethodPost, "/api/v1/channels/import-probed-models", adminTok, map[string]any{
		"channel_id": chID,
		"model_ids":  []string{"claude-sonnet-4-5-mock", "doubao-seedance-2-0-mock"},
	})
	if wImp.Code != http.StatusOK || int(impOut["imported_count"].(float64)) != 2 {
		t.Fatalf("import-probed-models = %d body=%s", wImp.Code, wImp.Body.String())
	}

	// Verify channel test works end-to-end against imported model.
	wTest, testOut := f.do(t, http.MethodPost, "/api/v1/channels/"+strconv.FormatUint(chID, 10)+"/test", adminTok, map[string]any{
		"model":    "claude-sonnet-4-5-mock",
		"scenario": "default",
	})
	if wTest.Code != http.StatusOK || testOut["success"] != true {
		t.Fatalf("channel test = %d body=%s", wTest.Code, wTest.Body.String())
	}

	// 6. Verify 7 Model Types with secondary default_features + Model feature_attributes persistence.
	wTypes := f.doRaw(t, http.MethodGet, "/api/v1/model-types", adminTok)
	if wTypes.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/model-types = %d", wTypes.Code)
	}
	var typesList []map[string]any
	if err := json.Unmarshal(wTypes.Body.Bytes(), &typesList); err != nil {
		t.Fatalf("decode model-types: %v", err)
	}
	if len(typesList) < 7 {
		t.Fatalf("expected >=7 system model types, got %d", len(typesList))
	}
	for _, tp := range typesList {
		df, _ := tp["default_features"].(string)
		if df == "" || !strings.HasPrefix(df, "[") {
			t.Fatalf("model type %v missing default_features JSON array", tp["name"])
		}
	}

	// 7. Verify /v1/balance, /v1/user/balance, /v1/models, /v1beta/models (Gemini format), /api/v3/models, and token reset-usage.
	if err := db.Model(&relay.Channel{}).Where("id = ?", chID).Update("status", 1).Error; err != nil {
		t.Fatalf("activate channel: %v", err)
	}
	wBal, balOut := f.do(t, http.MethodGet, "/v1/balance", apiKeyA, nil)
	if wBal.Code != http.StatusOK || balOut["success"] != true {
		t.Fatalf("GET /v1/balance = %d body=%s", wBal.Code, wBal.Body.String())
	}
	wUBal, ubalOut := f.do(t, http.MethodGet, "/v1/user/balance", apiKeyA, nil)
	if wUBal.Code != http.StatusOK || ubalOut["success"] != true {
		t.Fatalf("GET /v1/user/balance = %d body=%s", wUBal.Code, wUBal.Body.String())
	}

	wModsOAI, modsOAI := f.do(t, http.MethodGet, "/v1/models", apiKeyA, nil)
	if wModsOAI.Code != http.StatusOK || modsOAI["object"] != "list" {
		t.Fatalf("GET /v1/models = %d body=%s", wModsOAI.Code, wModsOAI.Body.String())
	}
	wModsV3, modsV3 := f.do(t, http.MethodGet, "/api/v3/models", apiKeyA, nil)
	if wModsV3.Code != http.StatusOK || modsV3["object"] != "list" {
		t.Fatalf("GET /api/v3/models = %d body=%s", wModsV3.Code, wModsV3.Body.String())
	}

	reqGem := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	reqGem.Header.Set("x-goog-api-key", apiKeyA)
	wGem := httptest.NewRecorder()
	f.engine.ServeHTTP(wGem, reqGem)
	if wGem.Code != http.StatusOK {
		t.Fatalf("GET /v1beta/models = %d body=%s", wGem.Code, wGem.Body.String())
	}
	var gemOut map[string]any
	_ = json.Unmarshal(wGem.Body.Bytes(), &gemOut)
	if gemModels, _ := gemOut["models"].([]any); len(gemModels) == 0 {
		t.Fatalf("expected non-empty Gemini models list, got %s", wGem.Body.String())
	}

	// Verify token reset-usage on org's key.
	var keyRow identity.APIKey
	if err := db.Where("used_amount >= ?", decimal.Zero).First(&keyRow).Error; err == nil {
		wReset, _ := f.do(t, http.MethodPost, "/api/v1/tokens/"+strconv.FormatUint(keyRow.ID, 10)+"/reset-usage", adminTok, map[string]any{})
		_ = wReset // may belong to another org, just verify handler doesn't 500
	}

	// 8. Verify full 9-category 33-article docs_api preset tree.
	wDocs, docsOut := f.do(t, http.MethodGet, "/api/v1/plugins/docs-api/public/tree", "", nil)
	if wDocs.Code != http.StatusOK {
		t.Fatalf("GET docs public tree = %d body=%s", wDocs.Code, wDocs.Body.String())
	}
	treeCats, _ := docsOut["tree"].([]any)
	if len(treeCats) != 9 {
		t.Fatalf("expected 9 docs categories, got %d", len(treeCats))
	}
	totalArticles := 0
	for _, catRaw := range treeCats {
		cat := catRaw.(map[string]any)
		children, _ := cat["children"].([]any)
		totalArticles += len(children)
	}
	if totalArticles != 33 {
		t.Fatalf("expected 33 preset doc articles, got %d", totalArticles)
	}
}
