package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func pricingRebindFixture(t *testing.T, model string) (*gin999, *gorm.DB, map[uint64]string) {
	t.Helper()
	f, db, tokens, in := initialOfficialPricingFixture(t, model)
	profile, err := arkverification.Load(model)
	arkBillMust(t, err)
	var cap ark.Capability
	var bounds map[string]video.Dimensions
	arkBillMust(t, json.Unmarshal([]byte(in.Configuration.CapabilityJSON), &cap))
	arkBillMust(t, json.Unmarshal([]byte(in.Configuration.BoundsJSON), &bounds))
	cap, bounds, err = verificationTarget(in.Configuration, cap, bounds, profile)
	arkBillMust(t, err)
	capRaw, _ := json.Marshal(cap)
	boundsRaw, _ := json.Marshal(bounds)
	in.Configuration.CapabilityJSON, in.Configuration.BoundsJSON = string(capRaw), string(boundsRaw)
	arkBillMust(t, db.Transaction(func(tx *gorm.DB) error { return importTemplateMinimum(tx, profile, 1, time.Now().UTC()) }))
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 201)
	arkBillMust(t, db.Model(&videobilling.PricingRule{}).Where("model = ?", model).Update("risk_multiplier", 4).Error)
	arkBillMust(t, db.Where("id = ?", 99).Delete(&relay.Channel{}).Error)
	arkBillMust(t, db.Create(&relay.Channel{ID: 200, Name: "replacement", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Status: 1, Models: cap.UpstreamModel}).Error)
	arkBillMust(t, db.Create(&relay.ChannelCredential{ChannelID: 200, Status: relay.CredentialActive, Fingerprint: "test-rebind", CredentialEnc: "ciphertext-must-not-be-returned"}).Error)
	return f, db, tokens
}

func rebindPreview(t *testing.T, f *gin999, model, token string, want int) pricingRebindView {
	t.Helper()
	w := catalogGET(t, f, "/api/admin/video/models/"+model+"/pricing/rebind-preview?channel_id=200", token, "", want)
	var out pricingRebindView
	if want == 200 {
		arkBillMust(t, json.Unmarshal(w.Body.Bytes(), &out))
		if strings.Contains(w.Body.String(), "ciphertext-must-not-be-returned") || strings.Contains(w.Body.String(), "fingerprint") {
			t.Fatal("credential data exposed in repair preview")
		}
	}
	return out
}

func TestPricingRebindRestoresAllModelsWithoutReverification(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model20Fast, ark.Model20Mini, ark.Model25} {
		t.Run(model, func(t *testing.T) {
			f, db, tokens := pricingRebindFixture(t, model)
			var oldConfig video.ModelConfig
			arkBillMust(t, db.Where("public_model = ?", model).Take(&oldConfig).Error)
			oldRules, err := currentPricingRules(db, oldConfig)
			arkBillMust(t, err)
			stableTables := []string{"video_provider_scopes", "video_quota_policies", "video_minimum_rule_sets", "video_minimum_rule_cells", "channels", "channel_credentials", "ledger_accounts", "ledger_holds", "ledger_journal"}
			stableBefore := verificationSnapshot(t, db, stableTables...)
			writableTables := []string{"video_model_configs", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "audit_logs"}
			beforePreview := verificationSnapshot(t, db, writableTables...)
			view := rebindPreview(t, f, model, tokens[1], 200)
			if view.CurrentChannelID != 99 || view.TargetChannelID != 200 || view.ProviderScopeID != oldConfig.ProviderScopeID || !view.Publishable || !view.HasActiveCredential || view.LiveTested {
				t.Fatalf("incorrect repair plan: %#v", view)
			}
			enabled := 0
			for _, cell := range view.Cells {
				if cell.Enabled {
					enabled++
				}
				if cell.RiskMultiplier != "1" || cell.Mode == "generate" && !cell.Draft && !cell.Enabled {
					t.Fatal("official supported price was not made ready")
				}
			}
			if enabled != len(oldRules) {
				t.Fatal("repair enabled additional unsupported combinations")
			}
			if beforePreview != verificationSnapshot(t, db, writableTables...) {
				t.Fatal("repair preview mutated state")
			}
			path := "/api/admin/video/models/" + model + "/pricing/rebind-publications"
			body := map[string]any{"channel_id": 200, "expected_context_sha256": view.ContextSHA256}
			templateCall(t, f, path, tokens[1], "repair-"+model, body, 200)
			after := verificationSnapshot(t, db, writableTables...)
			templateCall(t, f, path, tokens[1], "repair-"+model, body, 200)
			if after != verificationSnapshot(t, db, writableTables...) {
				t.Fatal("same-key repair wrote twice")
			}
			templateCall(t, f, path, tokens[1], "stale-"+model, body, 409)
			if stableBefore != verificationSnapshot(t, db, stableTables...) {
				t.Fatal("repair changed scope, budgets, minimum evidence, credentials or balances")
			}
			var cfg video.ModelConfig
			arkBillMust(t, db.Where("public_model = ?", model).Take(&cfg).Error)
			if cfg.ChannelID != 200 || cfg.ProviderScopeID != oldConfig.ProviderScopeID || cfg.CapabilityJSON != oldConfig.CapabilityJSON || cfg.BoundsJSON != oldConfig.BoundsJSON || !cfg.VerifiedAt.Equal(*oldConfig.VerifiedAt) || cfg.Version == oldConfig.Version || cfg.PricingVersion == oldConfig.PricingVersion {
				t.Fatal("repair changed capability evidence or failed to version binding")
			}
			rules, err := currentPricingRules(db, cfg)
			arkBillMust(t, err)
			oldByKey := map[string]videobilling.PricingRule{}
			for _, rule := range oldRules {
				oldByKey[priceCellKey(cellFromRule(rule))] = rule
			}
			for _, rule := range rules {
				old := oldByKey[priceCellKey(cellFromRule(rule))]
				if !rule.Enabled || rule.RiskMultiplier.String() != "1" || rule.MinimumRuleVersion != old.MinimumRuleVersion || !rule.SupplierCNYPer1M.Equal(old.SupplierCNYPer1M) || !rule.RetailUSDPer1M.Equal(old.RetailUSDPer1M) {
					t.Fatal("published pricing or minimum identity incorrect")
				}
			}
			var prior []videobilling.PricingRule
			arkBillMust(t, db.Where("model = ? AND version = ?", model, oldConfig.PricingVersion).Find(&prior).Error)
			for _, rule := range prior {
				if rule.Enabled || rule.EffectiveUntil == nil || rule.RiskMultiplier.String() != "4" {
					t.Fatal("old price history overwritten instead of ended")
				}
			}
			var verified arkVerificationStatus
			verification := catalogGET(t, f, "/api/admin/video/models/"+model+"/verification", tokens[1], "", 200)
			arkBillMust(t, json.Unmarshal(verification.Body.Bytes(), &verified))
			if !verified.Verified || verified.LiveTested {
				t.Fatal("restored documentation binding still needs manual verification or claims a live test")
			}
		})
	}
}

func TestPricingRebindRejectsChangedContextAndRollsBack(t *testing.T) {
	for _, change := range []string{"config", "fx", "credentials", "disabled_credentials", "minimum", "budget", "target_mapping", "old_channel", "audit"} {
		t.Run(change, func(t *testing.T) {
			f, db, tokens := pricingRebindFixture(t, ark.Model20)
			view := rebindPreview(t, f, ark.Model20, tokens[1], 200)
			switch change {
			case "config":
				arkBillMust(t, db.Model(&video.ModelConfig{}).Where("public_model = ?", ark.Model20).Update("version", "concurrently-updated").Error)
			case "fx":
				arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id = ?", 1).Update("fx_usd_per_cny", "0.15").Error)
			case "credentials":
				arkBillMust(t, db.Model(&relay.ChannelCredential{}).Where("channel_id = ?", 200).Update("fingerprint", "rotated").Error)
			case "disabled_credentials":
				arkBillMust(t, db.Model(&relay.ChannelCredential{}).Where("channel_id = ?", 200).Update("status", relay.CredentialDisabled).Error)
			case "minimum":
				arkBillMust(t, db.Model(&videobilling.MinimumRuleCell{}).Where("id > 0").Update("minimum_tokens", gorm.Expr("minimum_tokens + 1")).Error)
			case "budget":
				arkBillMust(t, db.Model(&videobilling.QuotaPolicy{}).Where("owner_id = ?", 1).Update("limit_amount", 30).Error)
			case "target_mapping":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 200).Update("models", "doubao-seedance-2-5-260628").Error)
			case "old_channel":
				arkBillMust(t, db.Create(&relay.Channel{ID: 99, Provider: "volcengine", Status: 1}).Error)
			case "audit":
				arkBillMust(t, db.Callback().Create().Before("gorm:create").Register("rebind:audit-failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "audit_logs" {
						tx.AddError(assertCatalogWrite{})
					}
				}))
			}
			tables := []string{"video_model_configs", "video_pricing_rules", "video_configuration_revisions", "video_pricing_publications", "audit_logs", "video_provider_scopes", "video_quota_policies", "video_minimum_rule_sets", "video_minimum_rule_cells"}
			before := verificationSnapshot(t, db, tables...)
			templateCall(t, f, "/api/admin/video/models/seedance-2.0/pricing/rebind-publications", tokens[1], "rebind-changed", map[string]any{"channel_id": 200, "expected_context_sha256": view.ContextSHA256}, 409)
			if before != verificationSnapshot(t, db, tables...) {
				t.Fatal("failed repair partially published")
			}
		})
	}
}

func TestPricingRebindPreservesDisabledNonstandardPolicy(t *testing.T) {
	f, db, tokens := pricingRebindFixture(t, ark.Model20)
	var original videobilling.PricingRule
	arkBillMust(t, db.Where("model = ? AND input_video = ?", ark.Model20, false).First(&original).Error)
	hidden := original
	hidden.ID, hidden.RuleID, hidden.ServiceTier, hidden.Enabled = 0, "hidden-existing", "flex", false
	arkBillMust(t, db.Create(&hidden).Error)
	view := rebindPreview(t, f, ark.Model20, tokens[1], 200)
	templateCall(t, f, "/api/admin/video/models/seedance-2.0/pricing/rebind-publications", tokens[1], "preserve-hidden", map[string]any{"channel_id": 200, "expected_context_sha256": view.ContextSHA256}, 200)
	var cfg video.ModelConfig
	arkBillMust(t, db.Where("public_model = ?", ark.Model20).Take(&cfg).Error)
	var saved videobilling.PricingRule
	arkBillMust(t, db.Where("model = ? AND version = ? AND service_tier = ?", ark.Model20, cfg.PricingVersion, "flex").Take(&saved).Error)
	if saved.Enabled || !saved.RetailUSDPer1M.Equal(hidden.RetailUSDPer1M) || !saved.SupplierCNYPer1M.Equal(hidden.SupplierCNYPer1M) || !saved.RiskMultiplier.Equal(hidden.RiskMultiplier) || saved.MinimumRuleVersion != hidden.MinimumRuleVersion {
		t.Fatal("nonstandard disabled policy changed")
	}
}

func TestPricingRebindRequiresOfficialIdentityCredentialsAndActor(t *testing.T) {
	for _, change := range []string{"no_credential", "disabled_credential", "third_party", "byok", "disabled_channel", "unverified_capability"} {
		t.Run(change, func(t *testing.T) {
			f, db, tokens := pricingRebindFixture(t, ark.Model20)
			switch change {
			case "no_credential":
				arkBillMust(t, db.Where("channel_id = ?", 200).Delete(&relay.ChannelCredential{}).Error)
			case "disabled_credential":
				arkBillMust(t, db.Model(&relay.ChannelCredential{}).Where("channel_id = ?", 200).Update("status", relay.CredentialDisabled).Error)
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 200).Update("credential_enc", "legacy-must-not-bypass-disabled-rows").Error)
			case "third_party":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 200).Update("base_url", "https://proxy.invalid/api/v3").Error)
			case "byok":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 200).Update("owner_org_id", 42).Error)
			case "disabled_channel":
				arkBillMust(t, db.Model(&relay.Channel{}).Where("id = ?", 200).Update("status", 0).Error)
			case "unverified_capability":
				arkBillMust(t, db.Model(&video.ModelConfig{}).Where("public_model = ?", ark.Model20).Update("verified_at", nil).Error)
			}
			rebindPreview(t, f, ark.Model20, tokens[1], 409)
		})
	}
	f, _, tokens := pricingRebindFixture(t, ark.Model20)
	for _, actor := range []uint64{2, 4} {
		rebindPreview(t, f, ark.Model20, tokens[actor], 403)
		templateCall(t, f, "/api/admin/video/models/seedance-2.0/pricing/rebind-publications", tokens[actor], "denied", map[string]any{"channel_id": 200, "expected_context_sha256": strings.Repeat("a", 64)}, 403)
	}
	templateCall(t, f, "/api/admin/video/models/seedance-2.0/pricing/rebind-publications", tokens[1], "", map[string]any{"channel_id": 200, "expected_context_sha256": strings.Repeat("a", 64)}, 422)
}
