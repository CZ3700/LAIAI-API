package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
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
	if err := json.Unmarshal(wFR.Body.Bytes(), &frList); err != nil || len(frList) != 52 {
		t.Fatalf("expected 52 visible forward rules, got %d (err=%v)", len(frList), err)
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

func TestV1CompatChannelSubmitSimulateAndRelayBilling(t *testing.T) {
	prevAllowPriv := relay.ByokAllowPrivateForTest
	relay.ByokAllowPrivateForTest = true
	t.Cleanup(func() { relay.ByokAllowPrivateForTest = prevAllowPriv })

	f, db, _, sessionToken, apiKey := setupStage2Server(t)
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

	var gotUpstreamAuth string
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUpstreamAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-v1compat-sim",
			"object":"chat.completion",
			"model":"gpt-4o-sim-test",
			"choices":[{"index":0,"message":{"role":"assistant","content":"v1compat billing ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500}
		}`))
	}))
	defer mockSrv.Close()

	// 1. Seed presets (14 billing + 54 forward) and locate an OpenAI chat forward rule.
	if err := v1compat.EnsurePresetRules(context.Background(), db); err != nil {
		t.Fatalf("EnsurePresetRules: %v", err)
	}
	wFR := adminDo(t, f, http.MethodGet, "/api/v1/forward-rules", nil, true)
	var frList []struct {
		ID       uint64 `json:"id"`
		EID      string `json:"eid"`
		RuleType string `json:"rule_type"`
	}
	if err := json.Unmarshal(wFR.Body.Bytes(), &frList); err != nil || len(frList) == 0 {
		t.Fatalf("expected seeded forward rules, got len=%d err=%v", len(frList), err)
	}
	var openaiFwdID uint64
	var openaiFwdEID string
	for _, item := range frList {
		if item.RuleType == "openai" {
			openaiFwdID = item.ID
			openaiFwdEID = item.EID
			break
		}
	}
	if openaiFwdID == 0 {
		t.Fatalf("expected at least one openai forward rule")
	}

	// Create deterministic billing rule via v1 compat (prompt=2.0/1M, completion=6.0/1M).
	wBR := adminDo(t, f, http.MethodPost, "/api/v1/billing-rules", map[string]any{
		"name":            "v1compat-sim-rule",
		"billing_type":    "tokens",
		"billing_rule":    "standard",
		"pricing_type":    "standard",
		"prompt_rate":     2.0,
		"completion_rate": 6.0,
		"is_active":       1,
	}, true)
	if wBR.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/billing-rules status=%d body=%s", wBR.Code, wBR.Body.String())
	}
	var brOut struct {
		Rule struct {
			ID  uint64 `json:"id"`
			PID string `json:"pid"`
		} `json:"rule"`
	}
	_ = json.Unmarshal(wBR.Body.Bytes(), &brOut)

	// 2. Create upstream channel-config with rate=0.5 & secret key.
	const upstreamSecret = "sk-mock-upstream-123"
	wCfg := adminDo(t, f, http.MethodPost, "/api/v1/channel-configs", map[string]any{
		"name":          "v1compat-mock-upstream",
		"provider_type": "openai",
		"base_url":      mockSrv.URL,
		"api_key":       upstreamSecret,
		"rate":          0.5,
	}, true)
	if wCfg.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/channel-configs status=%d body=%s", wCfg.Code, wCfg.Body.String())
	}
	var cfgOut struct {
		Data struct {
			ID uint64 `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wCfg.Body.Bytes(), &cfgOut)
	if cfgOut.Data.ID == 0 {
		t.Fatalf("expected created channel_config id, body=%s", wCfg.Body.String())
	}

	// 3. Bind model first, then submit high_availability_group channel referencing sub_channels & model mid (matching Channels.tsx!).
	wMod := adminDo(t, f, http.MethodPost, "/api/v1/models", map[string]any{
		"model_id":              "gpt-4o-sim-test",
		"name":                  "GPT-4o Sim Test",
		"billing_rule_id":       brOut.Rule.ID,
		"forward_rule_ids":      []uint64{openaiFwdID},
		"site_discount_enabled": 1,
		"site_discount":         0.8,
		"pre_deduction":         0.05,
		"is_listed":             1,
		"is_active":             1,
	}, true)
	if wMod.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/models status=%d body=%s", wMod.Code, wMod.Body.String())
	}
	var modResp struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(wMod.Body.Bytes(), &modResp)
	modelMid := "m-" + strconv.FormatUint(modResp.ID, 10)

	wCh := adminDo(t, f, http.MethodPost, "/api/v1/channels", map[string]any{
		"name":          "v1compat-ha-channel",
		"provider_type": "high_availability_group",
		"models":        []string{modelMid},
		"status":        1,
		"config":        map[string]any{"sub_channels": []uint64{cfgOut.Data.ID}},
	}, true)
	if wCh.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/channels status=%d body=%s", wCh.Code, wCh.Body.String())
	}
	var chOut struct {
		Data struct {
			ID           uint64   `json:"id"`
			ProviderType string   `json:"provider_type"`
			Models       []string `json:"models"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wCh.Body.Bytes(), &chOut)
	if chOut.Data.ID == 0 || chOut.Data.ProviderType != "high_availability_group" || len(chOut.Data.Models) != 1 || chOut.Data.Models[0] != modelMid {
		t.Fatalf("unexpected channel response: %+v body=%s", chOut, wCh.Body.String())
	}

	// 5. Run POST /api/v1/channels/:id/test and verify success & zero secret key leak.
	wTest := adminDo(t, f, http.MethodPost, "/api/v1/channels/"+strconv.FormatUint(chOut.Data.ID, 10)+"/test", map[string]any{
		"model":           "gpt-4o-sim-test",
		"forward_rule_id": openaiFwdID,
	}, true)
	if wTest.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/channels/:id/test status=%d body=%s", wTest.Code, wTest.Body.String())
	}
	if strings.Contains(wTest.Body.String(), upstreamSecret) {
		t.Fatalf("channel test response leaked raw upstream api_key")
	}
	var testResp struct {
		Success     bool           `json:"success"`
		MatchedRule map[string]any `json:"matched_rule"`
	}
	_ = json.Unmarshal(wTest.Body.Bytes(), &testResp)
	if !testResp.Success || testResp.MatchedRule == nil {
		t.Fatalf("expected channel test success with matched_rule, got %+v body=%s", testResp, wTest.Body.String())
	}

	// 6. Run POST /api/v1/channels/:id/simulate-billing and check exact math.
	// Base: 1000*2/1M + 500*6/1M = 0.002 + 0.003 = 0.005
	// Actual (0.8 discount): 0.004; pre_deduct: 0.05 -> refund: 0.046
	// Upstream (0.5 rate): 0.0025; gross profit: 0.0015 (37.5%)
	wSim := adminDo(t, f, http.MethodPost, "/api/v1/channels/"+strconv.FormatUint(chOut.Data.ID, 10)+"/simulate-billing", map[string]any{
		"model": "gpt-4o-sim-test",
		"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 500},
	}, true)
	if wSim.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/channels/:id/simulate-billing status=%d body=%s", wSim.Code, wSim.Body.String())
	}
	var simResp struct {
		Success    bool `json:"success"`
		Settlement struct {
			PreDeducted  float64 `json:"pre_deducted"`
			ActualCost   float64 `json:"actual_cost"`
			RefundAmount float64 `json:"refund_amount"`
		} `json:"settlement"`
		CostAndMargin struct {
			UpstreamCost float64 `json:"upstream_cost"`
			GrossProfit  float64 `json:"gross_profit"`
		} `json:"cost_and_margin"`
		PipelineTrace []map[string]any `json:"pipeline_trace"`
	}
	_ = json.Unmarshal(wSim.Body.Bytes(), &simResp)
	approx := func(got, want float64) bool { return math.Abs(got-want) < 1e-6 }
	if !simResp.Success || len(simResp.PipelineTrace) != 8 ||
		!approx(simResp.Settlement.PreDeducted, 0.05) ||
		!approx(simResp.Settlement.ActualCost, 0.004) ||
		!approx(simResp.Settlement.RefundAmount, 0.046) ||
		!approx(simResp.CostAndMargin.UpstreamCost, 0.0025) ||
		!approx(simResp.CostAndMargin.GrossProfit, 0.0015) {
		t.Fatalf("unexpected simulate-billing result: %+v body=%s", simResp, wSim.Body.String())
	}

	// 7. Execute real /v1/chat/completions through Relay and verify ledger balance, invocation PID/EID, & logs.
	wMe, meOut := f.do(t, http.MethodGet, "/api/me", sessionToken, nil)
	if wMe.Code != http.StatusOK {
		t.Fatalf("GET /api/me failed: %d", wMe.Code)
	}
	orgID := uint64(meOut["org_id"].(float64))
	postedBefore, _, _ := ledger.OrgBalances(db, orgID, ledger.AccountUserCredit)

	wRelay := doAPIKeyReq(t, f, http.MethodPost, "/v1/chat/completions", "Authorization", "Bearer "+apiKey, map[string]any{
		"model":    "gpt-4o-sim-test",
		"messages": []map[string]any{{"role": "user", "content": "hello sim"}},
	})
	if wRelay.Code != http.StatusOK || !strings.Contains(wRelay.Body.String(), "v1compat billing ok") {
		t.Fatalf("POST /v1/chat/completions failed: %d %s", wRelay.Code, wRelay.Body.String())
	}
	if gotUpstreamAuth != "Bearer "+upstreamSecret {
		t.Fatalf("expected relay to use sub_channel credential %q, got %q", "Bearer "+upstreamSecret, gotUpstreamAuth)
	}

	reqID := wRelay.Header().Get("X-Request-Id")
	inv := waitInvocation(t, db, "request_id = ?", reqID)
	if inv.BillingRulePID != brOut.Rule.PID || inv.ForwardRuleEID != openaiFwdEID {
		t.Fatalf("expected invocation tracking pid=%q eid=%q, got pid=%q eid=%q",
			brOut.Rule.PID, openaiFwdEID, inv.BillingRulePID, inv.ForwardRuleEID)
	}
	if !inv.CostUSD.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("expected settled invocation cost 0.004, got %s", inv.CostUSD.String())
	}

	postedAfter, heldAfter, _ := ledger.OrgBalances(db, orgID, ledger.AccountUserCredit)
	if !postedBefore.Sub(postedAfter).Equal(decimal.RequireFromString("0.004")) || !heldAfter.IsZero() {
		t.Fatalf("ledger mismatch: posted %s -> %s, held=%s", postedBefore, postedAfter, heldAfter)
	}

	wLogs, _ := f.do(t, http.MethodGet, "/api/v1/logs", sessionToken, nil)
	if wLogs.Code != http.StatusOK || !strings.Contains(wLogs.Body.String(), brOut.Rule.PID) {
		t.Fatalf("expected /api/v1/logs to expose billing_rule_pid=%q, body=%s", brOut.Rule.PID, wLogs.Body.String())
	}
	if strings.Contains(wLogs.Body.String(), `"channel_id"`) || strings.Contains(wLogs.Body.String(), `"channel_name"`) {
		t.Fatalf("tenant /api/v1/logs leaked channel metadata: %s", wLogs.Body.String())
	}
}
