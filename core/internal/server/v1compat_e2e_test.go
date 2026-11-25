package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
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
