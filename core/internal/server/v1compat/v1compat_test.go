package v1compat_test

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/server/v1compat"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const compatTestSchema = `
CREATE TABLE api_docs (
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
CREATE TABLE billing_rules (
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
CREATE TABLE model_rule_bindings (
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
CREATE TABLE model_prices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model TEXT NOT NULL UNIQUE,
    input_usd_per_1m NUMERIC NOT NULL DEFAULT 0,
    output_usd_per_1m NUMERIC NOT NULL DEFAULT 0,
    cached_input_usd_per_1m NUMERIC NOT NULL DEFAULT 0,
    image_usd NUMERIC NOT NULL DEFAULT 0,
    per_call_usd NUMERIC NOT NULL DEFAULT 0,
    tts_usd_per_1k_chars NUMERIC NOT NULL DEFAULT 0,
    transcription_usd_per_call NUMERIC NOT NULL DEFAULT 0,
    audio_input_usd_per_1m NUMERIC NOT NULL DEFAULT 0,
    audio_output_usd_per_1m NUMERIC NOT NULL DEFAULT 0
);
CREATE TABLE model_meta (
    model TEXT PRIMARY KEY,
    vendor TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    context_length INTEGER NOT NULL DEFAULT 0,
    icon TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT 'openai',
    base_url TEXT NOT NULL DEFAULT '',
    models TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 1,
    owner_org_id INTEGER NOT NULL DEFAULT 0
);
`

func openCompatDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(compatTestSchema).Error; err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	return db
}

func TestPresetDocsAndCRUD(t *testing.T) {
	ctx := t.Context()
	db := openCompatDB(t)

	tree, err := v1compat.ListDocTree(ctx, db, "zh", true)
	if err != nil {
		t.Fatalf("ListDocTree failed: %v", err)
	}
	if len(tree) == 0 || tree[0].Slug != "quickstart" {
		t.Fatalf("expected quickstart root dir, got %+v", tree)
	}
	// doc0003 should be endpoints under quickstart
	doc3, err := v1compat.GetDocDetail(ctx, db, 3, "en", false, true)
	if err != nil {
		t.Fatalf("GetDocDetail(3) failed: %v", err)
	}
	if doc3.Slug != "endpoints" || doc3.Title != "Available Endpoints Overview" {
		t.Fatalf("unexpected doc3: %+v", doc3)
	}

	// Admin CRUD + cycle protection
	customDir, err := v1compat.CreateDoc(ctx, db, v1compat.DocMutationRequest{
		Title:    "自定义目录",
		Slug:     "custom-dir",
		IsDir:    1,
		IsActive: 1,
	})
	if err != nil {
		t.Fatalf("CreateDoc dir failed: %v", err)
	}
	if _, err := v1compat.UpdateDoc(ctx, db, customDir.ID, v1compat.DocMutationRequest{
		ParentID: &customDir.ID,
		Title:    "循环目录",
		Slug:     "custom-dir",
		IsActive: 1,
	}); err == nil {
		t.Fatalf("expected cycle error when setting self as parent")
	}
	if err := v1compat.SetDocDefaultExpanded(ctx, db, customDir.ID, true); err != nil {
		t.Fatalf("SetDocDefaultExpanded failed: %v", err)
	}
	if err := v1compat.DeleteDoc(ctx, db, customDir.ID); err != nil {
		t.Fatalf("DeleteDoc failed: %v", err)
	}
}

func TestBuildPublicMarketplacePrivacyAndBilling(t *testing.T) {
	ctx := t.Context()
	db := openCompatDB(t)

	rule := pricing.BillingRule{
		PID:          "60001",
		Name:         "Seedance 2.0",
		BillingType:  pricing.BillingTypeDuration,
		BillingRule:  "seedance2.0",
		PricingType:  "tiers",
		DurationRate: decimal.RequireFromString("0.08"),
		IsActive:     1,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if err := db.Create(&pricing.ModelRuleBinding{
		Model:         "doubao-seedance-2-0-260128",
		DisplayName:   "Seedance 2.0",
		BillingRuleID: rule.ID,
		IsListed:      1,
		IsActive:      1,
		SortOrder:     100,
	}).Error; err != nil {
		t.Fatalf("create listed binding: %v", err)
	}
	if err := db.Create(&pricing.ModelRuleBinding{
		Model:         "internal-hidden-model",
		DisplayName:   "Hidden",
		BillingRuleID: rule.ID,
		IsListed:      0,
		IsActive:      1,
	}).Error; err != nil {
		t.Fatalf("create unlisted binding: %v", err)
	}
	if err := db.Exec(`INSERT INTO channels(name, provider, base_url, models, status, owner_org_id) VALUES('secret-upstream', 'volcengine', 'https://secret.example.com', 'doubao-seedance-2-0-260128,gpt-4o', 1, 0)`).Error; err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if err := db.Create(&relay.ModelPrice{
		Model:          "gpt-4o",
		InputUSDPer1M:  decimal.RequireFromString("2.5"),
		OutputUSDPer1M: decimal.RequireFromString("10"),
	}).Error; err != nil {
		t.Fatalf("insert price: %v", err)
	}

	res, err := v1compat.BuildPublicMarketplace(ctx, db)
	if err != nil {
		t.Fatalf("BuildPublicMarketplace failed: %v", err)
	}
	models := res["models"].([]v1compat.MarketplaceModel)
	if len(models) != 2 {
		t.Fatalf("expected 2 listed models (seedance + gpt-4o), got %d", len(models))
	}
	if models[0].ModelID != "doubao-seedance-2-0-260128" || models[0].TypeName != "视频" {
		t.Fatalf("expected sorted seedance first with 视频 category, got %+v", models[0])
	}
}

func TestPresetForwardRulesAllValidAndCascadeVisible(t *testing.T) {
	ctx := t.Context()
	db := openCompatDB(t)
	if err := db.Exec(`
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
	`).Error; err != nil {
		t.Fatalf("create forward_rules: %v", err)
	}
	if err := v1compat.EnsurePresetRules(ctx, db); err != nil {
		t.Fatalf("EnsurePresetRules: %v", err)
	}
	var rules []forward.ForwardRule
	if err := db.Find(&rules).Error; err != nil {
		t.Fatalf("find forward_rules: %v", err)
	}
	if len(rules) < 50 {
		t.Fatalf("expected >=50 preset forward rules, got %d", len(rules))
	}
	var foundCascade bool
	for _, r := range rules {
		if err := forward.ValidateConfigJSON(r.ConfigJSON); err != nil {
			t.Fatalf("preset forward rule %s (%s) invalid config_json: %v", r.EID, r.Name, err)
		}
		if r.EID == "12184" {
			foundCascade = true
			if v1compat.ShouldHidePluginGatedForwardRule(r) {
				t.Fatalf("cascade forward rule 12184 (%s) must be visible", r.Name)
			}
		}
	}
	if !foundCascade {
		t.Fatal("missing cascade forward rule 12184")
	}
}
