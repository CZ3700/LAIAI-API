package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricetemplate"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var initialPricingTables = []string{
	"video_model_configs", "video_provider_scopes", "video_pricing_rules",
	"video_configuration_revisions", "video_pricing_publications",
	"video_minimum_rule_sets", "video_minimum_rule_cells", "video_quota_policies",
	"video_pricing_templates", "channels", "channel_credentials", "audit_logs",
	"ledger_accounts", "ledger_journal", "ledger_holds",
}

func initialSetupFixture(t *testing.T, model string) (*gin999, *gorm.DB, map[uint64]string, arkpricetemplate.Template, map[string]any) {
	t.Helper()
	f, db, tokens := pricingCatalogFixture(t)
	templates := seedPricingTemplates(t, db)
	arkBillMust(t, db.Where("1 = 1").Delete(&video.ModelConfig{}).Error)
	arkBillMust(t, db.Where("1 = 1").Delete(&videobilling.PricingRule{}).Error)
	configureDirectVideoRuntime(t, f.srv)
	arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id = ?", 1).Update("fx_observed_at", time.Now().UTC().Add(-time.Minute)).Error)
	arkBillMust(t, db.Exec(`CREATE TABLE video_quota_policies(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,scope TEXT NOT NULL,owner_id INTEGER NOT NULL,model TEXT NOT NULL,metric TEXT NOT NULL,period TEXT NOT NULL,timezone TEXT NOT NULL,limit_amount NUMERIC,limit_units INTEGER,version TEXT NOT NULL,enabled INTEGER NOT NULL);`).Error)
	template := templates[model]
	arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Updates(map[string]any{"models": template.UpstreamModel, "model_mapping": ""}).Error)
	arkBillMust(t, db.Create(&relay.ChannelCredential{ChannelID: 99, Status: relay.CredentialActive, Fingerprint: "initial-test-fingerprint", CredentialEnc: "initial-ciphertext-must-stay-private"}).Error)
	in := map[string]any{
		"channel_id": 99, "template_version": template.Version, "pricing_mode": "official",
		"provider_scope_id": 0, "account_ref": "2132103052", "project_name": "LAIAI-Gateway",
		"fx_usd_per_cny": "0.125", "daily_budget_cny": "20", "monthly_budget_cny": "100", "enabled": true,
	}
	return f, db, tokens, template, in
}

type initialSetupTestView struct {
	Available           bool              `json:"available"`
	ConfigurationExists bool              `json:"configuration_exists"`
	Blockers            []json.RawMessage `json:"blockers"`
	Templates           []json.RawMessage `json:"templates"`
	ProviderScopes      []json.RawMessage `json:"provider_scopes"`
}

type initialPreviewTestView struct {
	Publication        json.RawMessage `json:"publication"`
	Cells              []pricingCell   `json:"cells"`
	MinimumRuleVersion string          `json:"minimum_rule_version"`
	MinimumCellCount   int             `json:"minimum_cell_count"`
}

func readInitialSetup(t *testing.T, f *gin999, model, token string) initialSetupTestView {
	t.Helper()
	w := catalogGET(t, f, "/api/admin/video/models/"+model+"/pricing/initial-setup?channel_id=99", token, "", 200)
	var out initialSetupTestView
	arkBillMust(t, json.Unmarshal(w.Body.Bytes(), &out))
	if strings.Contains(w.Body.String(), "initial-ciphertext-must-stay-private") || strings.Contains(w.Body.String(), "initial-test-fingerprint") {
		t.Fatal("initial setup exposed channel credential data")
	}
	return out
}

func previewInitialSetup(t *testing.T, f *gin999, model, token string, body any) initialPreviewTestView {
	t.Helper()
	raw := templateCall(t, f, "/api/admin/video/models/"+model+"/pricing/initial-preview", token, "", body, 200)
	var out initialPreviewTestView
	arkBillMust(t, json.Unmarshal(raw, &out))
	if len(out.Publication) == 0 || string(out.Publication) == "null" || len(out.Cells) == 0 {
		t.Fatal("initial preview omitted its reviewable publication or price matrix")
	}
	if strings.Contains(string(raw), "initial-ciphertext-must-stay-private") || strings.Contains(string(raw), "initial-test-fingerprint") {
		t.Fatal("initial preview exposed channel credential data")
	}
	return out
}

func TestPricingInitialSetupAllModelsPreviewAndPublication(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model20Fast, ark.Model20Mini, ark.Model25} {
		for _, mode := range []string{"official", "template"} {
			t.Run(model+"/"+mode, func(t *testing.T) {
				f, db, tokens, template, in := initialSetupFixture(t, model)
				in["pricing_mode"] = mode
				before := verificationSnapshot(t, db, initialPricingTables...)
				setup := readInitialSetup(t, f, model, tokens[1])
				if !setup.Available || setup.ConfigurationExists || len(setup.Blockers) != 0 || len(setup.Templates) == 0 {
					t.Fatalf("template-only installation cannot enter first setup: %#v", setup)
				}
				preview := previewInitialSetup(t, f, model, tokens[1], in)
				if len(preview.Cells) != len(template.Cells) || preview.MinimumRuleVersion != template.MinimumRuleVersion || preview.MinimumCellCount != template.MinimumCellCount {
					t.Fatalf("first preview lost matrix or minimum evidence: %#v", preview)
				}
				byKey := map[string]arkpricetemplate.Cell{}
				for _, cell := range template.Cells {
					byKey[cell.Key()] = cell
				}
				for _, cell := range preview.Cells {
					source, ok := byKey[priceCellKey(cell)]
					if !ok || !cell.Enabled || cell.MinimumRuleVersion != source.MinimumRuleVersion {
						t.Fatal("initial preview changed dimensions or omitted complete minimum coverage")
					}
					retail := decimal.RequireFromString(source.RetailUSDPer1M)
					risk := "4"
					if mode == "official" {
						retail = decimal.RequireFromString(source.SupplierCNYPer1M).Mul(decimal.RequireFromString("0.125")).Round(12)
						risk = "1"
					}
					if !decimal.RequireFromString(cell.RetailUSDPer1M).Equal(retail) || cell.RiskMultiplier != risk || cell.SupplierCNYPer1M != source.SupplierCNYPer1M {
						t.Fatalf("%s did not preserve the selected pricing policy: %#v", mode, cell)
					}
				}
				if before != verificationSnapshot(t, db, initialPricingTables...) {
					t.Fatal("initial setup read or preview wrote business state")
				}
				stableBefore := verificationSnapshot(t, db, "channels", "channel_credentials", "ledger_accounts", "ledger_journal", "ledger_holds", "video_pricing_templates")
				path := "/api/admin/video/configurations"
				key := "initial-" + model + "-" + mode
				templateCall(t, f, path, tokens[1], key, preview.Publication, 201)
				after := verificationSnapshot(t, db, initialPricingTables...)
				templateCall(t, f, path, tokens[1], key, preview.Publication, 201)
				if after != verificationSnapshot(t, db, initialPricingTables...) {
					t.Fatal("initial publication replay changed state")
				}
				if stableBefore != verificationSnapshot(t, db, "channels", "channel_credentials", "ledger_accounts", "ledger_journal", "ledger_holds", "video_pricing_templates") {
					t.Fatal("initial pricing publication changed credentials, templates or funds")
				}
				var cfg video.ModelConfig
				arkBillMust(t, db.Where("public_model = ?", model).Take(&cfg).Error)
				if cfg.ChannelID != 99 || cfg.ProviderScopeID <= 1 || !cfg.Enabled || cfg.VerifiedAt == nil {
					t.Fatalf("first publication omitted new scope, channel or verification: %#v", cfg)
				}
				var scope video.ProviderScope
				arkBillMust(t, db.First(&scope, cfg.ProviderScopeID).Error)
				if scope.AccountRef != in["account_ref"] || scope.ProjectName != in["project_name"] || scope.Region != "cn-beijing" || !decimal.RequireFromString(scope.FXUSDPerCNY).Equal(decimal.RequireFromString("0.125")) {
					t.Fatal("published scope does not match explicitly supplied identity and FX")
				}
				rules, err := currentPricingRules(db, cfg)
				arkBillMust(t, err)
				if len(rules) != len(preview.Cells) {
					t.Fatal("published pricing matrix is incomplete")
				}
				published := map[string]videobilling.PricingRule{}
				for _, rule := range rules {
					published[priceCellKey(cellFromRule(rule))] = rule
				}
				for _, cell := range preview.Cells {
					rule, ok := published[priceCellKey(cell)]
					if !ok || !rule.Enabled || !rule.RetailUSDPer1M.Equal(decimal.RequireFromString(cell.RetailUSDPer1M)) || !rule.RiskMultiplier.Equal(decimal.RequireFromString(cell.RiskMultiplier)) || rule.MinimumRuleVersion != cell.MinimumRuleVersion {
						t.Fatal("published rule differs from reviewed preview")
					}
				}
				var minimum videobilling.MinimumRuleSet
				arkBillMust(t, db.Where("model = ? AND version = ?", model, template.MinimumRuleVersion).Take(&minimum).Error)
				var minimumCount int64
				arkBillMust(t, db.Model(&videobilling.MinimumRuleCell{}).Where("rule_set_id = ?", minimum.ID).Count(&minimumCount).Error)
				if minimumCount != int64(template.MinimumCellCount) {
					t.Fatal("initial publication did not atomically install complete minimum evidence")
				}
				setup = readInitialSetup(t, f, model, tokens[1])
				if !setup.ConfigurationExists || setup.Available {
					t.Fatal("existing configuration still presented as first-time setup")
				}
			})
		}
	}
}

func seedInitialScopeBudgets(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, item := range []struct{ period, amount string }{{videobilling.PeriodDay, "20"}, {videobilling.PeriodMonth, "100"}} {
		limit := decimal.RequireFromString(item.amount)
		arkBillMust(t, db.Create(&videobilling.QuotaPolicy{Scope: videobilling.ScopeProvider, OwnerID: 1, Metric: videobilling.MetricCNY, Period: item.period, Timezone: "Asia/Shanghai", LimitAmount: &limit, Version: "existing-budget-v1", Enabled: true}).Error)
	}
}

func TestPricingInitialSetupReusesScopeWithoutRewritingBudgets(t *testing.T) {
	f, db, tokens, _, in := initialSetupFixture(t, ark.Model20Fast)
	seedInitialScopeBudgets(t, db)
	in = map[string]any{"channel_id": 99, "template_version": arkpricetemplate.InitialVersion, "pricing_mode": "official", "provider_scope_id": 1, "enabled": true}
	before := verificationSnapshot(t, db, "video_provider_scopes", "video_quota_policies")
	setup := readInitialSetup(t, f, ark.Model20Fast, tokens[1])
	if len(setup.ProviderScopes) != 1 {
		t.Fatal("existing scope was not available for explicit reuse")
	}
	preview := previewInitialSetup(t, f, ark.Model20Fast, tokens[1], in)
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "reuse-scope", preview.Publication, 201)
	if before != verificationSnapshot(t, db, "video_provider_scopes", "video_quota_policies") {
		t.Fatal("scope reuse changed shared FX, budgets or their versions")
	}
	var cfg video.ModelConfig
	arkBillMust(t, db.Where("public_model = ?", ark.Model20Fast).Take(&cfg).Error)
	if cfg.ProviderScopeID != 1 {
		t.Fatal("scope reuse created a new financial budget boundary")
	}
}

func TestPricingInitialSetupRejectsWrongIdentityAndCredentials(t *testing.T) {
	for _, issue := range []string{"wrong_model", "wrong_mapping", "missing_credentials", "disabled_credentials", "third_party", "byok"} {
		t.Run(issue, func(t *testing.T) {
			f, db, tokens, _, in := initialSetupFixture(t, ark.Model20Fast)
			switch issue {
			case "wrong_model":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Update("models", "doubao-seedance-2-0-mini-260615").Error)
			case "wrong_mapping":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Updates(map[string]any{"models": ark.Model20Fast, "model_mapping": `{"seedance-2.0-fast":"doubao-seedance-2-0-mini-260615"}`}).Error)
			case "missing_credentials":
				arkBillMust(t, db.Where("channel_id = ?", 99).Delete(&relay.ChannelCredential{}).Error)
			case "disabled_credentials":
				arkBillMust(t, db.Model(&relay.ChannelCredential{}).Where("channel_id = ?", 99).Update("status", relay.CredentialDisabled).Error)
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Update("credential_enc", "legacy-key-must-not-bypass-disabled-rows").Error)
			case "third_party":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Update("base_url", "https://proxy.invalid/api/v3").Error)
			case "byok":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Update("owner_org_id", 42).Error)
			}
			before := verificationSnapshot(t, db, initialPricingTables...)
			setup := readInitialSetup(t, f, ark.Model20Fast, tokens[1])
			if setup.Available || setup.ConfigurationExists || len(setup.Blockers) == 0 {
				t.Fatalf("blocked initial setup gave no actionable blockers: %#v", setup)
			}
			templateCall(t, f, "/api/admin/video/models/"+ark.Model20Fast+"/pricing/initial-preview", tokens[1], "", in, 409)
			if before != verificationSnapshot(t, db, initialPricingTables...) {
				t.Fatal("rejected first setup changed state")
			}
		})
	}
}

func TestPricingInitialSetupPublicationRejectsChangedContext(t *testing.T) {
	for _, change := range []string{"credentials", "credential_ciphertext", "legacy_ciphertext", "disabled_credentials", "budget", "scope_fx", "channel_mapping", "template"} {
		t.Run(change, func(t *testing.T) {
			f, db, tokens, _, _ := initialSetupFixture(t, ark.Model20)
			seedInitialScopeBudgets(t, db)
			if change == "legacy_ciphertext" {
				arkBillMust(t, db.Where("channel_id = ?", 99).Delete(&relay.ChannelCredential{}).Error)
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Update("credential_enc", "legacy-before-preview").Error)
			}
			in := map[string]any{"channel_id": 99, "template_version": arkpricetemplate.InitialVersion, "pricing_mode": "official", "provider_scope_id": 1, "enabled": true}
			preview := previewInitialSetup(t, f, ark.Model20, tokens[1], in)
			switch change {
			case "credentials":
				arkBillMust(t, db.Model(&relay.ChannelCredential{}).Where("channel_id = ?", 99).Update("fingerprint", "rotated-after-preview").Error)
			case "credential_ciphertext":
				arkBillMust(t, db.Model(&relay.ChannelCredential{}).Where("channel_id = ?", 99).Update("credential_enc", "rotated-without-fingerprint-change").Error)
			case "legacy_ciphertext":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Update("credential_enc", "legacy-after-preview").Error)
			case "disabled_credentials":
				arkBillMust(t, db.Model(&relay.ChannelCredential{}).Where("channel_id = ?", 99).Update("status", relay.CredentialDisabled).Error)
			case "budget":
				arkBillMust(t, db.Model(&videobilling.QuotaPolicy{}).Where("owner_id = ? AND period = ?", 1, videobilling.PeriodDay).Update("limit_amount", 30).Error)
			case "scope_fx":
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id = ?", 1).Update("fx_usd_per_cny", "0.15").Error)
			case "channel_mapping":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 99).Update("models", "doubao-seedance-2-0-fast-260128").Error)
			case "template":
				arkBillMust(t, db.Model(&arkpricetemplate.Record{}).Where("model = ?", ark.Model20).Update("body_hash", strings.Repeat("0", 64)).Error)
			}
			before := verificationSnapshot(t, db, initialPricingTables...)
			templateCall(t, f, "/api/admin/video/configurations", tokens[1], "initial-stale", preview.Publication, 422)
			if before != verificationSnapshot(t, db, initialPricingTables...) {
				t.Fatal("stale initial preview partially published")
			}
		})
	}
}

func TestPricingInitialSetupRequiresIndividualSuperadmin(t *testing.T) {
	f, db, tokens, _, in := initialSetupFixture(t, ark.Model20Mini)
	base := "/api/admin/video/models/" + ark.Model20Mini + "/pricing"
	before := verificationSnapshot(t, db, initialPricingTables...)
	for _, actor := range []uint64{2, 4} {
		catalogGET(t, f, base+"/initial-setup?channel_id=99", tokens[actor], "", 403)
		templateCall(t, f, base+"/initial-preview", tokens[actor], "", in, 403)
	}
	catalogGET(t, f, base+"/initial-setup?channel_id=99", tokens[3], "", 401)
	templateCall(t, f, base+"/initial-preview", tokens[3], "", in, 401)
	catalogGET(t, f, base+"/initial-setup?channel_id=99", "", "fixture-emergency-token", 403)
	if before != verificationSnapshot(t, db, initialPricingTables...) {
		t.Fatal("unauthorized initial setup changed state")
	}
}

func TestPricingInitialSetupAcceptsFXObservedEarlierInSameSecond(t *testing.T) {
	f, db, tokens, _, _ := initialSetupFixture(t, ark.Model20)
	seedInitialScopeBudgets(t, db)
	now := time.Now().UTC().Truncate(time.Second).Add(900 * time.Millisecond)
	f.srv.now = func() time.Time { return now }
	arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id = ?", 1).Update("fx_observed_at", now.Add(-100*time.Millisecond)).Error)
	in := map[string]any{"channel_id": 99, "template_version": arkpricetemplate.InitialVersion, "pricing_mode": "official", "provider_scope_id": 1, "enabled": true}
	before := verificationSnapshot(t, db, initialPricingTables...)
	previewInitialSetup(t, f, ark.Model20, tokens[1], in)
	if before != verificationSnapshot(t, db, initialPricingTables...) {
		t.Fatal("same-second FX preview changed state")
	}
}

func TestPricingInitialSetupRequiresExactlyRepresentableBudgets(t *testing.T) {
	f, db, tokens, _, in := initialSetupFixture(t, ark.Model20Mini)
	path := "/api/admin/video/models/" + ark.Model20Mini + "/pricing/initial-preview"
	before := verificationSnapshot(t, db, initialPricingTables...)
	for _, field := range []string{"daily_budget_cny", "monthly_budget_cny"} {
		for _, amount := range []string{"0.000000001", "1.000000009", "999999999999.999999999", "1000000000000", "1000000000000.00000001", "0", "-1", "1e3"} {
			t.Run(field+"/"+amount, func(t *testing.T) {
				body := map[string]any{}
				for key, value := range in {
					body[key] = value
				}
				body[field] = amount
				raw := templateCall(t, f, path, tokens[1], "", body, 409)
				var failure struct {
					Code string `json:"code"`
				}
				arkBillMust(t, json.Unmarshal(raw, &failure))
				if failure.Code != "supplier_budgets_required" {
					t.Fatalf("budget rejection code = %s", failure.Code)
				}
			})
		}
	}
	for _, amount := range []string{"0.00000001", "1.000000010", "999999999999.99999999"} {
		in["daily_budget_cny"], in["monthly_budget_cny"] = amount, amount
		preview := previewInitialSetup(t, f, ark.Model20Mini, tokens[1], in)
		var publication videoPublication
		arkBillMust(t, json.Unmarshal(preview.Publication, &publication))
		for _, budget := range publication.SupplierBudgets {
			if budget.LimitAmount == nil || !budget.LimitAmount.Equal(decimal.RequireFromString(amount)) {
				t.Fatal("preview rounded an exactly representable quota amount")
			}
		}
	}
	if before != verificationSnapshot(t, db, initialPricingTables...) {
		t.Fatal("budget validation wrote business state")
	}
}

func TestPricingInitialSetupPreviewCannotBeModifiedOrUsedByAnotherActor(t *testing.T) {
	f, db, tokens, _, in := initialSetupFixture(t, ark.Model20)
	preview := previewInitialSetup(t, f, ark.Model20, tokens[1], in)
	arkBillMust(t, db.Exec("UPDATE users SET role = 'superadmin' WHERE id = 2").Error)
	before := verificationSnapshot(t, db, initialPricingTables...)
	path := "/api/admin/video/configurations"
	templateCall(t, f, path, tokens[2], "other-actor", preview.Publication, 422)
	for _, field := range []string{"mode", "scope", "enabled", "budget", "signature"} {
		var body map[string]any
		arkBillMust(t, json.Unmarshal(preview.Publication, &body))
		switch field {
		case "mode":
			body["initial_setup"].(map[string]any)["pricing_mode"] = "template"
		case "scope":
			body["provider_scope"].(map[string]any)["ProjectName"] = "other-project"
		case "enabled":
			body["configuration"].(map[string]any)["Enabled"] = false
		case "budget":
			body["supplier_budgets"].([]any)[0].(map[string]any)["LimitAmount"] = "1000"
		case "signature":
			body["initial_setup"].(map[string]any)["signature"] = strings.Repeat("0", 64)
		}
		templateCall(t, f, path, tokens[1], "modified-"+field, body, 422)
	}
	if before != verificationSnapshot(t, db, initialPricingTables...) {
		t.Fatal("modified or cross-actor preview changed business state")
	}
}

func TestPricingInitialSetupRollsBackLatePublicationFailure(t *testing.T) {
	f, db, tokens, _, in := initialSetupFixture(t, ark.Model20Mini)
	preview := previewInitialSetup(t, f, ark.Model20Mini, tokens[1], in)
	arkBillMust(t, db.Callback().Create().Before("gorm:create").Register("initial:receipt-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "video_pricing_publications" {
			tx.AddError(assertCatalogWrite{})
		}
	}))
	before := verificationSnapshot(t, db, initialPricingTables...)
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "initial-atomic", preview.Publication, 422)
	if before != verificationSnapshot(t, db, initialPricingTables...) {
		t.Fatal("late initial publication failure left scope, budgets, rules or minimum evidence behind")
	}
	arkBillMust(t, db.Callback().Create().Remove("initial:receipt-failure"))
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "initial-atomic", preview.Publication, 201)
}

func TestPricingInitialSetupPreviewExpiryPreservesCompletedReplay(t *testing.T) {
	for _, published := range []bool{false, true} {
		name := "unpublished"
		if published {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			f, db, tokens, _, in := initialSetupFixture(t, ark.Model20Fast)
			now := time.Now().UTC()
			f.srv.now = func() time.Time { return now }
			preview := previewInitialSetup(t, f, ark.Model20Fast, tokens[1], in)
			path := "/api/admin/video/configurations"
			if published {
				templateCall(t, f, path, tokens[1], "expiry", preview.Publication, 201)
			}
			now = now.Add(16 * time.Minute)
			before := verificationSnapshot(t, db, initialPricingTables...)
			want := 422
			if published {
				want = 201
			}
			templateCall(t, f, path, tokens[1], "expiry", preview.Publication, want)
			if before != verificationSnapshot(t, db, initialPricingTables...) {
				t.Fatal("expired proposal wrote state or completed replay published twice")
			}
		})
	}
}

func TestPricingInitialSetupDisabledConfigurationKeepsReviewedPrices(t *testing.T) {
	f, db, tokens, template, in := initialSetupFixture(t, ark.Model20Mini)
	in["enabled"] = false
	preview := previewInitialSetup(t, f, ark.Model20Mini, tokens[1], in)
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "initial-disabled", preview.Publication, 201)
	var cfg video.ModelConfig
	arkBillMust(t, db.Where("public_model = ?", ark.Model20Mini).Take(&cfg).Error)
	if cfg.Enabled {
		t.Fatal("publishing a disabled proposal admitted the model")
	}
	prices, err := currentPricingRules(db, cfg)
	arkBillMust(t, err)
	if len(prices) != len(template.Cells) {
		t.Fatal("disabled model configuration lost reviewed price matrix")
	}
	for _, price := range prices {
		if !price.RiskMultiplier.Equal(decimal.NewFromInt(1)) || !price.RetailUSDPer1M.Equal(price.SupplierCNYPer1M.Mul(decimal.RequireFromString("0.125"))) {
			t.Fatal("disabled publication altered reviewed official prices")
		}
	}
}

// This uses only the dedicated migrated ark_test database. All changes below
// are rolled back, including the temporary removal of globally keyed model
// configurations created by other integration fixtures. No provider is called.
func TestMySQLPricingInitialSetupPublishesAndReusesScope(t *testing.T) {
	f := arkBillMySQLFixture(t, "running")
	tx := f.db.Begin()
	arkBillMust(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	f.s.db = tx
	configureDirectVideoRuntime(t, f.s)
	su := f.r.Group("/api/admin", f.s.adminRequired(identity.PlatRoleAdmin), f.s.requireSuperadmin())
	f.s.registerVideoConfiguration(su)
	f.s.registerArkPricingConsole(su)
	httpFixture := &gin999{engine: f.r, srv: f.s}
	models := []string{ark.Model20Fast, ark.Model20Mini}
	arkBillMust(t, tx.Where("public_model IN ?", models).Delete(&video.ModelConfig{}).Error)
	ch := relay.Channel{Name: "Initial pricing " + f.uid, Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Models: "doubao-seedance-2-0-fast-260128,doubao-seedance-2-0-mini-260615", Status: 1, Weight: 1}
	arkBillMust(t, tx.Create(&ch).Error)
	arkBillMust(t, tx.Create(&relay.ChannelCredential{ChannelID: ch.ID, Status: relay.CredentialActive, Fingerprint: verificationDigest(f.uid)[:12], CredentialEnc: "test-never-sent"}).Error)
	var sharedScope uint64
	var scopeBeforeReuse string
	for i, model := range models {
		template, err := arkpricetemplate.Load(tx, model, arkpricetemplate.InitialVersion)
		arkBillMust(t, err)
		var oldSets []videobilling.MinimumRuleSet
		arkBillMust(t, tx.Where("model = ? AND version = ?", model, template.MinimumRuleVersion).Find(&oldSets).Error)
		for _, old := range oldSets {
			arkBillMust(t, tx.Where("rule_set_id = ?", old.ID).Delete(&videobilling.MinimumRuleCell{}).Error)
			arkBillMust(t, tx.Delete(&old).Error)
		}
		base := "/api/admin/video/models/" + model + "/pricing"
		setupPath := base + "/initial-setup?channel_id=" + decimal.NewFromInt(int64(ch.ID)).String()
		var setup initialSetupTestView
		arkBillMust(t, json.Unmarshal(catalogGET(t, httpFixture, setupPath, f.tokens[0], "", 200).Body.Bytes(), &setup))
		if !setup.Available || setup.ConfigurationExists || len(setup.Blockers) != 0 {
			t.Fatalf("dedicated MySQL first setup is blocked: %#v", setup)
		}
		in := map[string]any{"channel_id": ch.ID, "template_version": template.Version, "pricing_mode": "official", "provider_scope_id": sharedScope, "enabled": true}
		if i == 0 {
			in["account_ref"], in["project_name"], in["fx_usd_per_cny"] = "initial-"+f.uid, "initial-project", "0.125"
			in["daily_budget_cny"], in["monthly_budget_cny"] = "20", "100"
		}
		before := verificationSnapshot(t, tx, initialPricingTables...)
		preview := previewInitialSetup(t, httpFixture, model, f.tokens[0], in)
		if before != verificationSnapshot(t, tx, initialPricingTables...) {
			t.Fatal("MySQL initial preview changed state")
		}
		path, key := "/api/admin/video/configurations", "mysql-initial-"+f.uid+model
		templateCall(t, httpFixture, path, f.tokens[0], key, preview.Publication, 201)
		after := verificationSnapshot(t, tx, initialPricingTables...)
		templateCall(t, httpFixture, path, f.tokens[0], key, preview.Publication, 201)
		if after != verificationSnapshot(t, tx, initialPricingTables...) {
			t.Fatal("MySQL initial publication replay changed state")
		}
		var cfg video.ModelConfig
		arkBillMust(t, tx.Where("public_model = ?", model).Take(&cfg).Error)
		if !cfg.Enabled || cfg.ChannelID != ch.ID || cfg.ProviderScopeID == 0 {
			t.Fatal("MySQL initial publication lost binding")
		}
		if i == 0 {
			sharedScope = cfg.ProviderScopeID
			scopeBeforeReuse = verificationSnapshot(t, tx, "video_provider_scopes", "video_quota_policies", "video_quota_buckets")
		} else if cfg.ProviderScopeID != sharedScope || scopeBeforeReuse != verificationSnapshot(t, tx, "video_provider_scopes", "video_quota_policies", "video_quota_buckets") {
			t.Fatal("second MySQL model changed shared scope, FX, budgets or usage buckets")
		}
		rules, err := currentPricingRules(tx, cfg)
		arkBillMust(t, err)
		if len(rules) != len(template.Cells) {
			t.Fatal("MySQL initial publication lost a price dimension")
		}
		for _, rule := range rules {
			if !rule.Enabled || !rule.RiskMultiplier.Equal(decimal.NewFromInt(1)) || !rule.RetailUSDPer1M.Equal(rule.SupplierCNYPer1M.Mul(decimal.RequireFromString("0.125"))) || rule.InputVideo && rule.MinimumRuleVersion != template.MinimumRuleVersion {
				t.Fatal("MySQL published price differs from preview policy or minimum identity")
			}
		}
	}
}
