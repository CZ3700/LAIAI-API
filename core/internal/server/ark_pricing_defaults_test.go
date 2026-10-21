package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func initialOfficialPricingFixture(t *testing.T, model string) (*gin999, *gorm.DB, map[uint64]string, videoPublication) {
	t.Helper()
	f, db, tokens := pricingCatalogFixture(t)
	arkBillMust(t, db.Where("public_model = ?", model).Delete(&video.ModelConfig{}).Error)
	arkBillMust(t, db.Where("model = ?", model).Delete(&videobilling.PricingRule{}).Error)
	f.srv.video = &video.Service{ArtifactService: &videoartifact.Service{}}
	arkBillMust(t, db.Exec(`CREATE TABLE video_quota_policies(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,scope TEXT NOT NULL,owner_id INTEGER NOT NULL,model TEXT NOT NULL,metric TEXT NOT NULL,period TEXT NOT NULL,timezone TEXT NOT NULL,limit_amount NUMERIC,limit_units INTEGER,version TEXT NOT NULL,enabled INTEGER NOT NULL);`).Error)
	arkBillMust(t, db.Model(&video.ProviderScope{}).Where("id = ?", 1).Update("fx_usd_per_cny", "0.125").Error)
	var scope video.ProviderScope
	arkBillMust(t, db.First(&scope, 1).Error)
	profile, err := arkverification.Load(model)
	arkBillMust(t, err)
	profile.Capability.UpstreamModel = map[string]string{
		ark.Model20: "doubao-seedance-2-0-260128", ark.Model25: "doubao-seedance-2-5-260628",
		ark.Model20Fast: "doubao-seedance-2-0-fast-260128", ark.Model20Mini: "doubao-seedance-2-0-mini-260615",
	}[model]
	capRaw, _ := json.Marshal(profile.Capability)
	boundsRaw, _ := json.Marshal(profile.Bounds)
	limit := decimal.NewFromInt(20)
	in := videoPublication{
		Configuration: video.ModelConfig{PublicModel: model, Version: "auto-config-v1", PricingVersion: "auto-prices-v1", ChannelID: 99, Enabled: true, NoChargeStatuses: "failed,cancelled", CapabilityJSON: string(capRaw), BoundsJSON: string(boundsRaw), MaxInputVideoDurationMS: profile.MaxInputVideoDurationMS},
		Scope:         scope, EvidenceHash: profile.EvidenceSHA256,
		SupplierBudgets: []videobilling.QuotaPolicy{
			{Scope: videobilling.ScopeProvider, Metric: videobilling.MetricCNY, Period: videobilling.PeriodDay, Timezone: "UTC", LimitAmount: &limit, Version: "auto-budget-v1"},
			{Scope: videobilling.ScopeProvider, Metric: videobilling.MetricCNY, Period: videobilling.PeriodMonth, Timezone: "UTC", LimitAmount: &limit, Version: "auto-budget-v1"},
		},
	}
	return f, db, tokens, in
}

func TestInitialOfficialPricingInheritsWithoutPriceVerification(t *testing.T) {
	for _, model := range []string{ark.Model20, ark.Model25, ark.Model20Fast, ark.Model20Mini} {
		t.Run(model, func(t *testing.T) {
			f, db, tokens, in := initialOfficialPricingFixture(t, model)
			scopeBefore := verificationSnapshot(t, db, "video_provider_scopes")
			templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 201)
			var cfg video.ModelConfig
			arkBillMust(t, db.Where("public_model = ?", model).Take(&cfg).Error)
			if cfg.CapabilityJSON != in.Configuration.CapabilityJSON || cfg.BoundsJSON != in.Configuration.BoundsJSON {
				t.Fatal("price inheritance changed capabilities")
			}
			prices, err := currentPricingRules(db, cfg)
			arkBillMust(t, err)
			ref, err := arkpricecatalog.ReferenceCandidates(model)
			arkBillMust(t, err)
			if len(prices) != len(ref.Prices) {
				t.Fatal("official price matrix incomplete")
			}
			for i, price := range prices {
				cost := decimal.RequireFromString(ref.Prices[i].SupplierCNYPer1M)
				if !price.SupplierCNYPer1M.Equal(cost) || !price.RetailUSDPer1M.Equal(cost.Mul(decimal.RequireFromString("0.125"))) || !price.RiskMultiplier.Equal(decimal.NewFromInt(1)) {
					t.Fatalf("official price or target FX/default lost: %#v", price)
				}
				if price.Enabled == price.InputVideo || price.MinimumRuleVersion != "" {
					t.Fatal("missing minimum evidence enabled or fabricated")
				}
				sourceFound := false
				for _, source := range ref.Sources {
					if source.ID == ref.Prices[i].SourceRows[0].SourceID && source.RawSHA256 == price.SourceHash {
						sourceFound = true
					}
				}
				if !sourceFound {
					t.Fatal("price lost captured source digest")
				}
			}
			var minimumCount int64
			arkBillMust(t, db.Model(&videobilling.MinimumRuleSet{}).Where("model = ?", model).Count(&minimumCount).Error)
			if minimumCount != 0 || scopeBefore != verificationSnapshot(t, db, "video_provider_scopes") {
				t.Fatal("inheritance wrote evidence or changed target FX")
			}
			var audit AuditLog
			arkBillMust(t, db.Where("action = ? AND target = ?", "video.configuration.publish", model).Take(&audit).Error)
			if !strings.Contains(audit.Detail, ref.CatalogVersion) || !strings.Contains(audit.Detail, ref.ManifestSHA256) {
				t.Fatal("missing inherited price provenance")
			}
			before := verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_provider_scopes", "video_quota_policies", "audit_logs")
			in.Configuration.Version, in.Configuration.PricingVersion = "auto-config-v2", "auto-prices-v2"
			templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 422)
			if before != verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_provider_scopes", "video_quota_policies", "audit_logs") {
				t.Fatal("automatic inheritance replaced explicit existing configuration")
			}
		})
	}
}

func TestInitialOfficialPricingUsesOnlyExistingMinimumEvidence(t *testing.T) {
	f, db, tokens, in := initialOfficialPricingFixture(t, ark.Model20)
	profile, err := arkverification.Load(ark.Model20)
	arkBillMust(t, err)
	arkBillMust(t, db.Transaction(func(tx *gorm.DB) error { return importTemplateMinimum(tx, profile, 1, time.Now().UTC()) }))
	minimumBefore := verificationSnapshot(t, db, "video_minimum_rule_sets", "video_minimum_rule_cells")
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 201)
	var cfg video.ModelConfig
	arkBillMust(t, db.First(&cfg).Error)
	prices, err := currentPricingRules(db, cfg)
	arkBillMust(t, err)
	for _, price := range prices {
		if !price.Enabled || price.InputVideo && price.MinimumRuleVersion != profile.MinimumVersion {
			t.Fatal("existing minimum coverage not inherited")
		}
	}
	if minimumBefore != verificationSnapshot(t, db, "video_minimum_rule_sets", "video_minimum_rule_cells") {
		t.Fatal("existing minimum evidence mutated")
	}
}

func TestInitialOfficialPricingKeepsAdmissionRequirementsAndRollback(t *testing.T) {
	for _, change := range []string{"capability", "evidence", "budgets", "channel", "audit"} {
		t.Run(change, func(t *testing.T) {
			f, db, tokens, in := initialOfficialPricingFixture(t, ark.Model20)
			switch change {
			case "capability":
				var cap ark.Capability
				arkBillMust(t, json.Unmarshal([]byte(in.Configuration.CapabilityJSON), &cap))
				cap.Verified = false
				raw, _ := json.Marshal(cap)
				in.Configuration.CapabilityJSON = string(raw)
			case "evidence":
				in.EvidenceHash = ""
			case "budgets":
				in.SupplierBudgets = nil
			case "channel":
				in.Configuration.ChannelID = 100
			case "audit":
				arkBillMust(t, db.Callback().Create().Before("gorm:create").Register("auto-pricing:audit-fail", func(tx *gorm.DB) {
					if tx.Statement.Table == "audit_logs" {
						tx.AddError(assertCatalogWrite{})
					}
				}))
			}
			before := verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_provider_scopes", "video_quota_policies", "audit_logs")
			templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 422)
			if before != verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_provider_scopes", "video_quota_policies", "audit_logs") {
				t.Fatal("failed default-price publication partially wrote state")
			}
		})
	}
}

func TestInitialOfficialPricingSupportsMaximumVersionLength(t *testing.T) {
	for _, length := range []int{62, 63, 64} {
		t.Run(strings.Repeat("v", length), func(t *testing.T) {
			f, db, tokens, in := initialOfficialPricingFixture(t, ark.Model20)
			in.Configuration.PricingVersion = strings.Repeat("v", length)
			templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 201)
			var prices []videobilling.PricingRule
			arkBillMust(t, db.Where("version = ?", in.Configuration.PricingVersion).Find(&prices).Error)
			if len(prices) != 8 {
				t.Fatal("long version lost inherited price rows")
			}
			for _, price := range prices {
				if len(price.RuleID) > 64 {
					t.Fatal("derived rule ID exceeds billing constraint")
				}
			}
		})
	}
}

func TestInitialOfficialPricingSharesVersionAcrossModels(t *testing.T) {
	f, db, tokens, in := initialOfficialPricingFixture(t, ark.Model20)
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 201)
	profile, err := arkverification.Load(ark.Model20Fast)
	arkBillMust(t, err)
	profile.Capability.UpstreamModel = "doubao-seedance-2-0-fast-260128"
	capRaw, _ := json.Marshal(profile.Capability)
	boundsRaw, _ := json.Marshal(profile.Bounds)
	in.Configuration.PublicModel = ark.Model20Fast
	in.Configuration.CapabilityJSON, in.Configuration.BoundsJSON = string(capRaw), string(boundsRaw)
	in.Configuration.MaxInputVideoDurationMS = profile.MaxInputVideoDurationMS
	in.EvidenceHash = profile.EvidenceSHA256
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 201)
	var prices []videobilling.PricingRule
	arkBillMust(t, db.Where("version = ?", in.Configuration.PricingVersion).Find(&prices).Error)
	if len(prices) != 12 {
		t.Fatal("shared version lost model prices")
	}
	seen := map[string]bool{}
	for _, price := range prices {
		if seen[price.RuleID] {
			t.Fatal("derived rule ID collides across models")
		}
		seen[price.RuleID] = true
	}
}
