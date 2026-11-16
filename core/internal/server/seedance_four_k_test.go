package server

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func TestSeedance25FourKVerificationAndPricingUseCapturedMinimum(t *testing.T) {
	f, db, tokens, _ := verificationFixture(t, ark.Model25)
	p, err := arkverification.Load4K(ark.Model25)
	arkBillMust(t, err)
	path := "/api/admin/video/models/seedance-2.5/verification"
	w, _ := f.do(t, "GET", path+"?profile=4k", tokens[1], nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w, _ = f.do(t, "POST", path, tokens[1], map[string]string{"expected_config_version": "config-v1", "profile_version": p.Version})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var status arkVerificationStatus
	arkBillMust(t, json.Unmarshal(w.Body.Bytes(), &status))
	if !status.Verified || len(status.Resolutions) != 4 || len(status.MinimumResolutions) != 4 || status.MinimumCellCount != 648 {
		t.Fatalf("capability verification lost minimum coverage distinction: %+v", status)
	}
	arkBillMust(t, db.Transaction(func(tx *gorm.DB) error {
		cfg, cap, scope, bounds, err := loadPricingContext(tx, ark.Model25, false)
		if err != nil {
			return err
		}
		prices, _, err := initialOfficialPricing(tx, cfg, cap, scope, f.srv.now())
		if err != nil {
			return err
		}
		fourK := 0
		for _, price := range prices {
			if price.Resolution != "4k" {
				continue
			}
			fourK++
			want := "31"
			if price.InputVideo {
				want = "19"
			}
			if price.SupplierCNYPer1M.String() != want || !price.Enabled || price.InputVideo && price.MinimumRuleVersion != p.MinimumVersion || !price.InputVideo && price.MinimumRuleVersion != "" {
				t.Fatalf("4K price or captured minimum incorrect: %+v", price)
			}
		}
		if fourK != 2 {
			t.Fatal("4K tariffs missing")
		}
		current, err := currentPricingRules(tx, cfg)
		if err != nil {
			return err
		}
		cells := priceMatrix(cap, current)
		for i := range cells {
			c := &cells[i]
			if (c.Mode == "draft_conversion" || c.Mode == "generate") && c.Resolution == "4k" {
				cost := "31"
				if c.InputVideo {
					cost = "19"
					c.MinimumRuleVersion = p.MinimumVersion
				}
				c.Enabled, c.SupplierCNYPer1M, c.RetailUSDPer1M = true, cost, cost
			}
		}
		sources, evidence, err := resolvePricingSources(tx, cfg, cells, current)
		if err != nil {
			return err
		}
		request := pricingRequest{ExpectedConfigVersion: cfg.Version, ConfigVersion: "4k-config-v1", PricingVersion: "4k-price-v1", EvidenceHash: evidence, Cells: cells, SourceSet: &sources}
		if _, _, _, err := validatePricingRequestWithContext(tx, ark.Model25, request, f.srv.now(), cfg, cap, scope, bounds); err != nil {
			return err
		}
		// Every 4K ratio/duration is required, even when a rate and version exist.
		var set videobilling.MinimumRuleSet
		if err := tx.Where("model = ? AND version = ?", ark.Model25, p.MinimumVersion).Take(&set).Error; err != nil {
			return err
		}
		if err := tx.Where("rule_set_id = ? AND resolution = ? AND ratio = ? AND output_duration_ms = ?", set.ID, "4k", "21:9", 30000).Delete(&videobilling.MinimumRuleCell{}).Error; err != nil {
			return err
		}
		for _, mode := range []string{"generate", "draft_conversion"} {
			probe := videobilling.PricingRule{PricingKey: videobilling.PricingKey{Model: ark.Model25, Mode: mode, Resolution: "4k", InputVideo: true}, MinimumRuleVersion: p.MinimumVersion}
			err := videobilling.ValidateMinimumCoverageTx(tx, probe, videobilling.MinimumContext{Resolution: "4k", Ratios: cap.Ratios, MinDuration: cap.MinDuration, MaxDuration: cap.MaxDuration}, 24)
			if !errors.Is(err, videobilling.ErrPricingUnavailable) {
				t.Fatalf("%s allowed missing 4K minimum evidence: %v", mode, err)
			}
		}
		return nil
	}))
	// Historical template/profile records are independent from the 4K upgrade.
	var count int64
	arkBillMust(t, db.Model(&video.ModelConfig{}).Where("public_model = ?", ark.Model25).Count(&count).Error)
	if count != 1 {
		t.Fatal("upgrade created a duplicate public model")
	}
}
