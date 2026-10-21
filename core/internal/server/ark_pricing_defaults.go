package server

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Initial prices inherit the captured official tariff and the target account's
// FX. Price provenance does not confer capability or minimum-token evidence.
func initialOfficialPricing(tx *gorm.DB, cfg video.ModelConfig, cap ark.Capability, scope video.ProviderScope, now time.Time) ([]videobilling.PricingRule, arkpricecatalog.Candidates, error) {
	ref, err := arkpricecatalog.ReferenceCandidates(cfg.PublicModel)
	if err != nil {
		return nil, ref, err
	}
	fx, err := parsePrice(scope.FXUSDPerCNY)
	if err != nil || !priceVersion.MatchString(cfg.PricingVersion) {
		return nil, ref, videobilling.ErrInvalid
	}
	sourceHashes := make(map[string]string, len(ref.Sources))
	for _, source := range ref.Sources {
		sourceHashes[source.ID] = source.RawSHA256
	}
	profile, err := arkverification.Load(cfg.PublicModel)
	if err != nil {
		return nil, ref, err
	}
	minimumAvailable, err := matchingVerificationMinimum(tx, cfg.PublicModel, profile)
	if err != nil {
		return nil, ref, err
	}
	prices := make([]videobilling.PricingRule, 0, len(ref.Prices))
	rulePrefix := "official-" + verificationDigest(struct {
		ProviderScopeID uint64
		Model, Version  string
	}{scope.ID, cfg.PublicModel, cfg.PricingVersion})[:40]
	for i, price := range ref.Prices {
		cost, err := parsePrice(price.SupplierCNYPer1M)
		if err != nil || price.Precision != "exact" || len(price.SourceRows) == 0 {
			return nil, ref, videobilling.ErrInvalid
		}
		retail, err := parsePrice(cost.Mul(fx).Round(12).String())
		sourceHash := sourceHashes[price.SourceRows[0].SourceID]
		digest, digestErr := hex.DecodeString(sourceHash)
		if err != nil || digestErr != nil || len(digest) != 32 {
			return nil, ref, videobilling.ErrInvalid
		}
		cell := pricingCell{Mode: price.Mode, Resolution: price.Resolution, InputVideo: price.InputVideo, ServiceTier: price.ServiceTier, Draft: price.Draft}
		rule := videobilling.PricingRule{
			RuleID: fmt.Sprintf("%s-%d", rulePrefix, i), Version: cfg.PricingVersion,
			PricingKey:     videobilling.PricingKey{ProviderScopeID: scope.ID, Model: cfg.PublicModel, Mode: price.Mode, Resolution: price.Resolution, InputVideo: price.InputVideo, ServiceTier: price.ServiceTier, Draft: price.Draft},
			RetailUSDPer1M: retail, SupplierCNYPer1M: cost, RiskMultiplier: decimal.NewFromInt(1),
			FormulaVersion: "ark-video-token-v1", SourceHash: sourceHash, EffectiveFrom: now,
			Enabled: priceCellSupported(cell, cap),
		}
		if price.InputVideo {
			if minimumAvailable {
				rule.MinimumRuleVersion = profile.MinimumVersion
			}
			if !minimumAvailable || cfg.MaxInputVideoDurationMS <= 0 {
				rule.Enabled = false
			} else if rule.Enabled {
				if err := videobilling.ValidateMinimumCoverageTx(tx, rule, videobilling.MinimumContext{Resolution: price.Resolution, Ratios: cap.Ratios, MinDuration: cap.MinDuration, MaxDuration: cap.MaxDuration}, int64(cap.FPS)); err != nil {
					return nil, ref, err
				}
			}
		}
		prices = append(prices, rule)
	}
	return prices, ref, nil
}
