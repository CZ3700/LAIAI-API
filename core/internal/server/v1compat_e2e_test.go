package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/server/v1compat"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
)

func TestV1CompatE2EContractsAndSecurityRedlines(t *testing.T) {
	f, db, _, userTokenA, _ := setupStage2Server(t)
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
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS api_docs_intl (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			doc_id INTEGER NOT NULL,
			lang TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (doc_id, lang)
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
		t.Fatalf("exec compat tables: %v", err)
	}

	// Register a second org/user to verify multi-tenant isolation.
	f.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "v1userB@test.com", "password": "longenough1"})
	_, loginB := f.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "v1userB@test.com", "password": "longenough1"})
	userTokenB := loginB["token"].(string)

	// Determine OrgID of user A from /api/me
	wMe, meOut := f.do(t, http.MethodGet, "/api/me", userTokenA, nil)
	if wMe.Code != http.StatusOK {
		t.Fatalf("GET /api/me failed: %d", wMe.Code)
	}
	orgIDA := uint64(meOut["org_id"].(float64))

	// 1. Public endpoints require zero auth
	for _, path := range []string{
		"/api/v1/settings",
		"/api/v1/plugins/active",
		"/api/v1/marketplace/public",
		"/api/v1/plugins/docs-api/public/tree",
		"/api/v1/plugins/docs-api/public/docs/3",
	} {
		w, _ := f.do(t, http.MethodGet, path, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d body=%s", path, w.Code, w.Body.String())
		}
	}

	wDoc3, _ := f.do(t, http.MethodGet, "/api/v1/plugins/docs-api/public/docs/3", "", nil)
	var doc3Body struct {
		Doc struct {
			ID   uint64 `json:"id"`
			Slug string `json:"slug"`
		} `json:"doc"`
	}
	if err := json.Unmarshal(wDoc3.Body.Bytes(), &doc3Body); err != nil {
		t.Fatalf("decode doc3: %v", err)
	}
	if doc3Body.Doc.ID != 3 || doc3Body.Doc.Slug != "endpoints" {
		t.Fatalf("unexpected doc0003 metadata: %+v", doc3Body.Doc)
	}

	// 2. API Key one-time plaintext return + Redline 7 reveal block + cross-org isolation
	wCreate, _ := f.do(t, http.MethodPost, "/api/v1/tokens", userTokenA, map[string]any{
		"name":        "react-key",
		"quota_limit": 25.5,
	})
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/tokens expected 201, got %d body=%s", wCreate.Code, wCreate.Body.String())
	}
	var createdTok struct {
		ID  uint64 `json:"id"`
		Key string `json:"key"`
	}
	_ = json.Unmarshal(wCreate.Body.Bytes(), &createdTok)
	if createdTok.Key == "" {
		t.Fatalf("expected one-time plaintext key on creation")
	}

	wReveal, _ := f.do(t, http.MethodPost, "/api/v1/tokens/1/reveal", userTokenA, map[string]any{"password": "longenough1"})
	if wReveal.Code != http.StatusForbidden {
		t.Fatalf("POST /api/v1/tokens/1/reveal expected 403 Forbidden, got %d", wReveal.Code)
	}

	wListB, _ := f.do(t, http.MethodGet, "/api/v1/tokens", userTokenB, nil)
	var listB struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(wListB.Body.Bytes(), &listB)
	if len(listB.Data) != 0 {
		t.Fatalf("org B should see 0 tokens of org A, got %d", len(listB.Data))
	}

	// 3. Task cancel & ledger release isolation
	postedBefore, heldBefore, err := ledger.OrgBalances(db, orgIDA, ledger.AccountUserCredit)
	if err != nil {
		t.Fatalf("OrgBalances before: %v", err)
	}
	acc, err := ledger.EnsureAccount(db, orgIDA, ledger.AccountUserCredit)
	if err != nil {
		t.Fatalf("ensure account: %v", err)
	}
	hold, _, err := ledger.CreateHold(db, ledger.HoldInput{
		IdempotencyKey: "task-v1compat-hold-1",
		AccountID:      acc.ID,
		Amount:         decimal.RequireFromString("2.5"),
		RefType:        "task",
		RefID:          "task-v1compat-1",
	})
	if err != nil {
		t.Fatalf("create hold: %v", err)
	}
	if err := db.Create(&task.Task{
		TaskID:        "task-v1compat-1",
		OrgID:         orgIDA,
		APIKeyID:      createdTok.ID,
		Model:         "doubao-seedance-2-0-260128",
		ChannelID:     1,
		Provider:      "volcengine",
		Status:        task.StatusQueued,
		HoldID:        hold.ID,
		HoldAmountUSD: decimal.RequireFromString("2.5"),
		GroupRatio:    decimal.NewFromInt(1),
	}).Error; err != nil {
		t.Fatalf("insert queued task: %v", err)
	}

	// Org B cannot cancel Org A's task
	wCancelB, _ := f.do(t, http.MethodPost, "/api/v1/task_logs/task-v1compat-1/cancel", userTokenB, nil)
	if wCancelB.Code != http.StatusNotFound {
		t.Fatalf("cross-org cancel expected 404, got %d", wCancelB.Code)
	}

	// Org A cancels task -> hold released cleanly
	wCancelA, _ := f.do(t, http.MethodPost, "/api/v1/task_logs/task-v1compat-1/cancel", userTokenA, nil)
	if wCancelA.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/task_logs/task-v1compat-1/cancel expected 200, got %d body=%s", wCancelA.Code, wCancelA.Body.String())
	}
	postedAfter, heldAfter, err := ledger.OrgBalances(db, orgIDA, ledger.AccountUserCredit)
	if err != nil {
		t.Fatalf("OrgBalances after: %v", err)
	}
	if !heldAfter.Equal(heldBefore) || !postedAfter.Equal(postedBefore) {
		t.Fatalf("expected full hold release (posted=%s->%s, held=%s->%s)", postedBefore, postedAfter, heldBefore, heldAfter)
	}

	// 4. Non-admin logs never expose channel_id or upstream_cost_ratio
	if err := db.Create(&invocation.Invocation{
		OrgID:             orgIDA,
		APIKeyID:          createdTok.ID,
		RequestID:         "req-v1compat-1",
		Model:             "gpt-4o",
		Modality:          invocation.ModalityChat,
		ChannelID:         77,
		Provider:          "openai",
		Status:            invocation.StatusSuccess,
		StatusCode:        200,
		CostUSD:           decimal.RequireFromString("0.12"),
		UpstreamCostRatio: decimal.RequireFromString("0.6"),
	}).Error; err != nil {
		t.Fatalf("insert invocation: %v", err)
	}
	wLogsA, _ := f.do(t, http.MethodGet, "/api/v1/logs", userTokenA, nil)
	if wLogsA.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/logs expected 200, got %d", wLogsA.Code)
	}
	var logsResp struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(wLogsA.Body.Bytes(), &logsResp)
	if len(logsResp.Data) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logsResp.Data))
	}
	if _, leaked := logsResp.Data[0]["channel_id"]; leaked {
		t.Fatalf("tenant logs leaked channel_id")
	}
	if _, leaked := logsResp.Data[0]["upstream_cost_ratio"]; leaked {
		t.Fatalf("tenant logs leaked upstream_cost_ratio")
	}

	// 5. v1 BillingRules & ForwardRules coercion (array pricing_tiers + integer is_active = 1)
	wBR := adminDo(t, f, http.MethodPost, "/api/v1/billing-rules", map[string]any{
		"name":            "v1compat-tiered",
		"billing_type":    "tokens",
		"billing_rule":    "tiered",
		"pricing_type":    "tiers",
		"pricing_tiers":   []map[string]any{{"max_prompt_tokens": 128000, "prompt_rate": 1.2, "completion_rate": 4.0}},
		"extended_config": map[string]any{"enable_time_multipliers": false},
		"is_active":       1,
	}, true)
	if wBR.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/billing-rules array tiers expected 201, got %d body=%s", wBR.Code, wBR.Body.String())
	}

	wFR := adminDo(t, f, http.MethodPost, "/api/v1/forward-rules", map[string]any{
		"name":        "v1compat-ark-video",
		"rule_type":   "volcengine",
		"category":    "视频",
		"config_json": map[string]any{"target_type": "volcengine", "auth_type": "bearer", "path_accept": []string{"/api/v3/contents/generations/tasks"}},
		"is_active":   1,
	}, true)
	if wFR.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/forward-rules int is_active expected 201, got %d body=%s", wFR.Code, wFR.Body.String())
	}

	_ = identity.PlatRoleAdmin
}

func TestV1CompatStage3Endpoints(t *testing.T) {
	f, db, _, userToken, _ := setupStage2Server(t)

	// Ensure auxiliary tables queried by marketplace / classification / topup exist in SQLite test fixture.
	if err := db.Exec(`
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
		t.Fatalf("exec model_meta: %v", err)
	}

	// 1. Seeded presets: 14 billing rules, 54 total forward rules in DB, 46 visible via GET /forward-rules.
	if err := v1compat.EnsurePresetRules(context.Background(), db); err != nil {
		t.Fatalf("EnsurePresetRules: %v", err)
	}
	var dbBillingCnt, dbForwardCnt int64
	db.Table("billing_rules").Count(&dbBillingCnt)
	db.Table("forward_rules").Count(&dbForwardCnt)
	if dbBillingCnt != 14 {
		t.Fatalf("expected 14 billing rules in DB, got %d", dbBillingCnt)
	}
	if dbForwardCnt != 54 {
		t.Fatalf("expected 54 forward rules in DB, got %d", dbForwardCnt)
	}

	wBR := adminDo(t, f, http.MethodGet, "/api/v1/billing-rules", nil, true)
	if wBR.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/billing-rules status=%d body=%s", wBR.Code, wBR.Body.String())
	}
	var brList []map[string]any
	if err := json.Unmarshal(wBR.Body.Bytes(), &brList); err != nil || len(brList) != 14 {
		t.Fatalf("expected 14 billing rules in API response, got %d (err=%v)", len(brList), err)
	}

	wFR := adminDo(t, f, http.MethodGet, "/api/v1/forward-rules", nil, true)
	if wFR.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/forward-rules status=%d body=%s", wFR.Code, wFR.Body.String())
	}
	var frList []map[string]any
	if err := json.Unmarshal(wFR.Body.Bytes(), &frList); err != nil || len(frList) != 46 {
		t.Fatalf("expected 46 visible forward rules, got %d (err=%v)", len(frList), err)
	}

	// 2. Dashboard / Live Metrics / Models 30d
	for _, ep := range []string{"/api/v1/dashboard", "/api/v1/metrics/live", "/api/v1/dashboard/models_30d"} {
		w, _ := f.do(t, http.MethodGet, ep, userToken, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d body=%s", ep, w.Code, w.Body.String())
		}
	}

	// 3. Channels, bare array channel-categories, channel-configs, admin models
	wCat := adminDo(t, f, http.MethodGet, "/api/v1/channel-categories", nil, true)
	if wCat.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channel-categories expected 200, got %d", wCat.Code)
	}
	var catArr []map[string]any
	if err := json.Unmarshal(wCat.Body.Bytes(), &catArr); err != nil {
		t.Fatalf("expected bare JSON array from channel-categories, err=%v body=%s", err, wCat.Body.String())
	}

	wChCreate := adminDo(t, f, http.MethodPost, "/api/v1/channels", map[string]any{
		"name":     "e2e-openai-channel",
		"provider": "openai",
		"base_url": "https://api.openai.com/v1",
		"api_key":  "sk-e2e-channel-secret",
		"models":   []string{"gpt-4o"},
		"status":   1,
	}, true)
	if wChCreate.Code != http.StatusCreated && wChCreate.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/channels expected 200/201, got %d body=%s", wChCreate.Code, wChCreate.Body.String())
	}
	if w := adminDo(t, f, http.MethodGet, "/api/v1/channels", nil, true); w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channels expected 200, got %d", w.Code)
	}

	wCfgCreate := adminDo(t, f, http.MethodPost, "/api/v1/channel-configs", map[string]any{
		"name":          "e2e-cfg",
		"provider_type": "openai",
		"base_url":      "https://api.openai.com",
		"api_key":       "sk-cfg-secret",
	}, true)
	if wCfgCreate.Code != http.StatusCreated && wCfgCreate.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/channel-configs expected 200/201, got %d body=%s", wCfgCreate.Code, wCfgCreate.Body.String())
	}
	if w := adminDo(t, f, http.MethodGet, "/api/v1/channel-configs", nil, true); w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channel-configs expected 200, got %d", w.Code)
	}

	wModels := adminDo(t, f, http.MethodGet, "/api/v1/models", nil, true)
	if wModels.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/models expected 200, got %d body=%s", wModels.Code, wModels.Body.String())
	}
	var modelsBody map[string]any
	_ = json.Unmarshal(wModels.Body.Bytes(), &modelsBody)
	if _, ok := modelsBody["data"]; !ok {
		t.Fatalf("GET /api/v1/models missing data field")
	}

	wStats := adminDo(t, f, http.MethodGet, "/api/v1/classifications/stats", nil, true)
	if wStats.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/classifications/stats expected 200, got %d body=%s", wStats.Code, wStats.Body.String())
	}
	var statsBody map[string]any
	_ = json.Unmarshal(wStats.Body.Bytes(), &statsBody)
	for _, key := range []string{"providers", "types", "source_counts"} {
		if _, ok := statsBody[key]; !ok {
			t.Fatalf("GET /api/v1/classifications/stats missing %s field", key)
		}
	}

	// 4. Finance & Full Settings endpoints
	for _, ep := range []string{
		"/api/v1/finance/recharges",
		"/api/v1/finance/orders",
		"/api/v1/finance/daily-stats",
		"/api/v1/settings/full",
		"/api/v1/system/about",
		"/api/v1/plugins/high_availability_channel/ha-config",
	} {
		w := adminDo(t, f, http.MethodGet, ep, nil, true)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d body=%s", ep, w.Code, w.Body.String())
		}
	}
	wFX := adminDo(t, f, http.MethodGet, "/api/v1/settings/currency/fx", nil, true)
	if wFX.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings/currency/fx expected 200, got %d body=%s", wFX.Code, wFX.Body.String())
	}
	var fxBody map[string]any
	_ = json.Unmarshal(wFX.Body.Bytes(), &fxBody)
	if _, ok := fxBody["channels"].([]any); !ok {
		t.Fatalf("GET /api/v1/settings/currency/fx channels should be array, got %T", fxBody["channels"])
	}
	if _, ok := fxBody["rates"].([]any); !ok {
		t.Fatalf("GET /api/v1/settings/currency/fx rates should be array, got %T", fxBody["rates"])
	}
	wSetSave := adminDo(t, f, http.MethodPost, "/api/v1/settings", map[string]any{
		"site": map[string]any{"name": "LAIAI Preview", "admin_path": "admin"},
	}, true)
	if wSetSave.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/settings expected 200, got %d body=%s", wSetSave.Code, wSetSave.Body.String())
	}

	// 5. Task log channel isolation for non-admin caller
	if err := db.Create(&task.Task{
		TaskID:    "task-iso-1",
		OrgID:     1,
		APIKeyID:  1,
		Model:     "doubao-seedance-2-0-260128",
		ChannelID: 99,
		Provider:  "volcengine-secret-channel",
		Status:    task.StatusQueued,
	}).Error; err != nil {
		t.Fatalf("insert isolation task: %v", err)
	}
	wTL, _ := f.do(t, http.MethodGet, "/api/v1/task_logs", userToken, nil)
	if wTL.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/task_logs expected 200, got %d", wTL.Code)
	}
	var tlBody struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(wTL.Body.Bytes(), &tlBody)
	for _, row := range tlBody.Data {
		for _, secretKey := range []string{"channel_id", "channel_name", "sub_channel_name"} {
			if _, leaked := row[secretKey]; leaked {
				t.Fatalf("tenant task_logs leaked %s", secretKey)
			}
		}
	}
}
