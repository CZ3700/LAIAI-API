package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const rulesAdminTestSchema = `
CREATE TABLE IF NOT EXISTS channel_categories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS billing_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pid TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL UNIQUE,
    billing_type TEXT NOT NULL DEFAULT 'tokens',
    billing_rule TEXT NOT NULL DEFAULT 'standard',
    pricing_type TEXT NOT NULL DEFAULT 'standard',
    prompt_rate NUMERIC NOT NULL DEFAULT 0,
    completion_rate NUMERIC NOT NULL DEFAULT 0,
    cached_rate NUMERIC NOT NULL DEFAULT 0,
    claude_cache_creation_rate NUMERIC NOT NULL DEFAULT 0,
    claude_cache_read_rate NUMERIC NOT NULL DEFAULT 0,
    fixed_rate NUMERIC NOT NULL DEFAULT 0,
    duration_rate NUMERIC NOT NULL DEFAULT 0,
    pricing_tiers TEXT NOT NULL DEFAULT '[]',
    extended_config TEXT NOT NULL DEFAULT '{}',
    is_active INTEGER NOT NULL DEFAULT 1,
    is_system INTEGER NOT NULL DEFAULT 0,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS forward_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    eid TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL UNIQUE,
    rule_type TEXT NOT NULL DEFAULT 'openai',
    category TEXT NOT NULL DEFAULT '聊天',
    config_json TEXT NOT NULL DEFAULT '{}',
    description TEXT NOT NULL DEFAULT '',
    remark TEXT NOT NULL DEFAULT '',
    is_active INTEGER NOT NULL DEFAULT 1,
    is_system INTEGER NOT NULL DEFAULT 0,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS model_rule_bindings (
    model TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    model_alias TEXT NOT NULL DEFAULT '',
    billing_rule_id INTEGER NOT NULL DEFAULT 0,
    forward_rule_ids TEXT NOT NULL DEFAULT '[]',
    pre_deduction NUMERIC NOT NULL DEFAULT 0,
    enable_log_content INTEGER NOT NULL DEFAULT 0,
    site_discount NUMERIC NOT NULL DEFAULT 1,
    site_discount_enabled INTEGER NOT NULL DEFAULT 0,
    global_discount NUMERIC NOT NULL DEFAULT 1,
    global_discount_enabled INTEGER NOT NULL DEFAULT 0,
    discount_schedule TEXT NOT NULL DEFAULT '',
    is_listed INTEGER NOT NULL DEFAULT 1,
    is_active INTEGER NOT NULL DEFAULT 1,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

func adminDo(t *testing.T, f *gin999, method, path string, body any, withToken bool) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if withToken {
		req.Header.Set("X-Admin-Token", testAdminToken)
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, req)
	return w
}

func setupRulesAdminTest(t *testing.T) (*gin999, *gorm.DB) {
	t.Helper()
	f, db := setupRelayServer(t)
	if err := db.Exec(rulesAdminTestSchema).Error; err != nil {
		t.Fatalf("rulesAdminTestSchema: %v", err)
	}
	return f, db
}

func TestRulesAdminCRUDAndGuards(t *testing.T) {
	f, db := setupRulesAdminTest(t)

	// 1. RBAC guard: no token -> 401
	for _, path := range []string{
		"/api/admin/channel-categories",
		"/api/admin/billing-rules",
		"/api/admin/forward-rules",
		"/api/admin/model-rule-bindings",
	} {
		w := adminDo(t, f, http.MethodGet, path, nil, false)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without token: got %d, want 401", path, w.Code)
		}
	}

	// 2. Channel Category CRUD + Channel category_id / upstream_cost_ratio + 409 guard
	wCat := adminDo(t, f, http.MethodPost, "/api/admin/channel-categories", map[string]any{
		"name": "视频专用池", "description": "火山直连视频池", "sort_order": 1,
	}, true)
	if wCat.Code != http.StatusCreated {
		t.Fatalf("create category: %d %s", wCat.Code, wCat.Body.String())
	}
	var catResp struct {
		Category struct {
			ID   uint64 `json:"id"`
			Name string `json:"name"`
		} `json:"category"`
	}
	_ = json.Unmarshal(wCat.Body.Bytes(), &catResp)
	if catResp.Category.ID == 0 || catResp.Category.Name != "视频专用池" {
		t.Fatalf("unexpected category resp: %s", wCat.Body.String())
	}

	// Create channel bound to category with upstream_cost_ratio=0.75
	wCh := adminDo(t, f, http.MethodPost, "/api/admin/channels", map[string]any{
		"name":                "ark-video-1",
		"provider":            "volcengine",
		"base_url":            "https://ark.cn-beijing.volces.com/api/v3",
		"api_key":             "sk-test-secret",
		"models":              []string{"doubao-seedance-2-0"},
		"category_id":         catResp.Category.ID,
		"upstream_cost_ratio": "0.75",
	}, true)
	if wCh.Code != http.StatusCreated {
		t.Fatalf("create channel: %d %s", wCh.Code, wCh.Body.String())
	}
	wListCh := adminDo(t, f, http.MethodGet, "/api/admin/channels", nil, true)
	if !strings.Contains(wListCh.Body.String(), `"upstream_cost_ratio":"0.75"`) {
		t.Fatalf("expected upstream_cost_ratio in channel list: %s", wListCh.Body.String())
	}

	// Deleting referenced category fails with 409 Conflict
	wDelCat := adminDo(t, f, http.MethodDelete, fmt.Sprintf("/api/admin/channel-categories/%d", catResp.Category.ID), nil, true)
	if wDelCat.Code != http.StatusConflict {
		t.Fatalf("delete referenced category: got %d, want 409", wDelCat.Code)
	}

	// 3. Billing Rule validation + CRUD
	wBadPID := adminDo(t, f, http.MethodPost, "/api/admin/billing-rules", map[string]any{
		"pid": "50001", "name": "Bad PID", "billing_type": "tokens",
	}, true)
	if wBadPID.Code != http.StatusBadRequest {
		t.Fatalf("invalid pid: got %d, want 400", wBadPID.Code)
	}

	wBadTM := adminDo(t, f, http.MethodPost, "/api/admin/billing-rules", map[string]any{
		"name":            "Bad Time Window",
		"billing_type":    "tokens",
		"extended_config": `{"enable_time_multipliers":true,"time_multipliers":[{"start":"99:00","end":"02:00","multiplier":"0.5"}]}`,
	}, true)
	if wBadTM.Code != http.StatusBadRequest {
		t.Fatalf("invalid time window: got %d, want 400", wBadTM.Code)
	}

	wBR := adminDo(t, f, http.MethodPost, "/api/admin/billing-rules", map[string]any{
		"name":         "Seedance 2.0",
		"billing_type": "tokens",
		"billing_rule": "seedance2.0",
		"pricing_type": "tiered",
		"prompt_rate":  "46",
		"extended_config": `{
			"resolution_rates":{"720p":{"with_video":"28","without_video":"46"}},
			"enable_time_multipliers":true,
			"time_multipliers":[{"start":"00:00","end":"08:00","multiplier":"0.8"}]
		}`,
	}, true)
	if wBR.Code != http.StatusCreated {
		t.Fatalf("create billing rule: %d %s", wBR.Code, wBR.Body.String())
	}
	var brResp struct {
		Rule struct {
			ID  uint64 `json:"id"`
			PID string `json:"pid"`
		} `json:"rule"`
	}
	_ = json.Unmarshal(wBR.Body.Bytes(), &brResp)
	if len(brResp.Rule.PID) != 5 || !strings.HasPrefix(brResp.Rule.PID, "6") {
		t.Fatalf("expected auto 5-digit 6xxxx pid, got %q", brResp.Rule.PID)
	}

	// 4. Forward Rule CRUD + invalid JSON guard
	wBadFR := adminDo(t, f, http.MethodPost, "/api/admin/forward-rules", map[string]any{
		"name": "bad json", "config_json": "{broken",
	}, true)
	if wBadFR.Code != http.StatusBadRequest {
		t.Fatalf("invalid forward config_json: got %d, want 400", wBadFR.Code)
	}

	wFR := adminDo(t, f, http.MethodPost, "/api/admin/forward-rules", map[string]any{
		"name":      "Volc Seedance Native",
		"rule_type": "volcengine",
		"category":  "视频",
		"config_json": `{
			"target_type": "volcengine",
			"auth_type": "bearer",
			"path_rewrite": {"old": "/v1/videos", "new": "/api/v3/contents/generations/tasks"},
			"path_passthrough": ["/api/v3/contents/generations/tasks"],
			"poll_path": "/api/v3/contents/generations/tasks/${task_id}"
		}`,
	}, true)
	if wFR.Code != http.StatusCreated {
		t.Fatalf("create forward rule: %d %s", wFR.Code, wFR.Body.String())
	}
	var frResp struct {
		Rule struct {
			ID  uint64 `json:"id"`
			EID string `json:"eid"`
		} `json:"rule"`
	}
	_ = json.Unmarshal(wFR.Body.Bytes(), &frResp)
	if len(frResp.Rule.EID) != 5 || !strings.HasPrefix(frResp.Rule.EID, "1") {
		t.Fatalf("expected auto 5-digit 1xxxx eid, got %q", frResp.Rule.EID)
	}

	// 5. Bind Model -> BillingRule + ForwardRule + test forwardResolver & 409 guards
	wBind := adminDo(t, f, http.MethodPost, "/api/admin/model-rule-bindings", map[string]any{
		"model":            "doubao-seedance-2-0",
		"display_name":     "Seedance 2.0",
		"billing_rule_id":  brResp.Rule.ID,
		"forward_rule_ids": []uint64{frResp.Rule.ID},
		"is_listed":        true,
	}, true)
	if wBind.Code != http.StatusOK {
		t.Fatalf("upsert binding: %d %s", wBind.Code, wBind.Body.String())
	}

	resolved, err := f.srv.forwardResolver.ResolveForModel(context.Background(), db, "doubao-seedance-2-0", "视频", "/v1/videos")
	if err != nil || resolved == nil || resolved.TargetType != "volcengine" || resolved.EID != frResp.Rule.EID {
		t.Fatalf("unexpected forwardResolver result: %+v err=%v", resolved, err)
	}

	// 409 when trying to disable or delete billing rule while listed model uses it
	falseVal := false
	wDisBR := adminDo(t, f, http.MethodPut, fmt.Sprintf("/api/admin/billing-rules/%d", brResp.Rule.ID), map[string]any{
		"is_active": falseVal,
	}, true)
	if wDisBR.Code != http.StatusConflict || !strings.Contains(wDisBR.Body.String(), "doubao-seedance-2-0") {
		t.Fatalf("disable billing rule in use: got %d %s, want 409 with listed_models_using_rule", wDisBR.Code, wDisBR.Body.String())
	}
	wDelBR := adminDo(t, f, http.MethodDelete, fmt.Sprintf("/api/admin/billing-rules/%d", brResp.Rule.ID), nil, true)
	if wDelBR.Code != http.StatusConflict || !strings.Contains(wDelBR.Body.String(), "listed_models_using_rule") {
		t.Fatalf("delete billing rule in use: got %d %s, want 409", wDelBR.Code, wDelBR.Body.String())
	}

	// Unlist binding -> delete billing rule succeeds
	wUnlist := adminDo(t, f, http.MethodPut, "/api/admin/model-rule-bindings/doubao-seedance-2-0", map[string]any{
		"billing_rule_id":  brResp.Rule.ID,
		"forward_rule_ids": []uint64{frResp.Rule.ID},
		"is_listed":        false,
	}, true)
	if wUnlist.Code != http.StatusOK {
		t.Fatalf("unlist binding: %d %s", wUnlist.Code, wUnlist.Body.String())
	}
	wDelBR2 := adminDo(t, f, http.MethodDelete, fmt.Sprintf("/api/admin/billing-rules/%d", brResp.Rule.ID), nil, true)
	if wDelBR2.Code != http.StatusOK {
		t.Fatalf("delete billing rule after unlist: %d %s", wDelBR2.Code, wDelBR2.Body.String())
	}

	// Verify audit logs recorded
	var auditCount int64
	if err := db.Table("audit_logs").Where("action IN ?", []string{
		"channel_category_create", "billing_rule_create", "forward_rule_create", "model_rule_binding_upsert", "billing_rule_delete",
	}).Count(&auditCount).Error; err != nil || auditCount < 5 {
		t.Fatalf("expected >=5 audit log entries, got %d (err=%v)", auditCount, err)
	}
}
