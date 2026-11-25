package pricing

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

func pickResolutionTier(tiers []ResolutionTier, res string) (*ResolutionTier, bool) {
	for i := range tiers {
		if tiers[i].IsEnabled() && strings.EqualFold(tiers[i].Resolution, res) {
			return &tiers[i], true
		}
	}
	var fallback *ResolutionTier
	for i := range tiers {
		if !tiers[i].IsEnabled() {
			continue
		}
		if fallback == nil || tiers[i].Rate.GreaterThan(fallback.Rate) {
			fallback = &tiers[i]
		}
	}
	return fallback, false
}

func resolveSizeTierRate(t SizeTier, quality string) (decimal.Decimal, string) {
	if !t.QualityPricing {
		return t.Rate, ""
	}
	q := quality
	if q == "" {
		q = "medium"
	}
	var r decimal.Decimal
	switch q {
	case "low":
		if t.RateLow != nil {
			r = *t.RateLow
		}
	case "high":
		if t.RateHigh != nil {
			r = *t.RateHigh
		}
	default:
		if t.RateMedium != nil {
			r = *t.RateMedium
		}
	}
	return r, "画质:" + q
}

func maxSizeTierRate(tiers []SizeTier, quality string) (decimal.Decimal, string, bool) {
	var bestRate decimal.Decimal
	var bestSize string
	found := false
	for _, t := range tiers {
		if !t.IsEnabled() {
			continue
		}
		r, _ := resolveSizeTierRate(t, quality)
		if !found || r.GreaterThan(bestRate) {
			bestRate, bestSize, found = r, t.Size, true
		}
	}
	return bestRate, bestSize, found
}

func applyPromptExtend(ext map[string]json.RawMessage, promptExtend bool, rate *decimal.Decimal, detail *string) {
	if !promptExtend {
		return
	}
	if m, ok := extDecimal(ext, "prompt_extend_multiplier"); ok {
		*rate = rate.Mul(m)
		*detail += fmt.Sprintf(" [提示词扩写 x%s]", fmtRate(m))
	}
}

func computeRequestsCost(
	rule *BillingRule,
	_ UsageTokens,
	discount decimal.Decimal,
	feat ExtractedFeatures,
) (decimal.Decimal, string) {
	ext := parseExtMap(rule.ExtendedConfig)
	rate := rule.FixedRate
	count := decimal.NewFromInt(1)
	detailDesc := "固定按次计费"

	defaultImgCount := func() decimal.Decimal {
		if feat.ImageCount != nil && *feat.ImageCount > 1 {
			return decimal.NewFromInt(*feat.ImageCount)
		}
		return decimal.NewFromInt(1)
	}

	switch rule.BillingRule {
	case "per_image":
		if feat.ImageCount != nil && *feat.ImageCount > 0 {
			count = decimal.NewFromInt(*feat.ImageCount)
		} else {
			count = decimal.Zero
		}
		detailDesc = "按张返回计费"
		applyPromptExtend(ext, feat.PromptExtend, &rate, &detailDesc)
		if feat.HasImageRef {
			if m, ok := extDecimal(ext, "image_ref_multiplier"); ok {
				rate = rate.Mul(m)
				detailDesc += fmt.Sprintf(" [图生图 x%s]", fmtRate(m))
			}
		}

	case "image_resolution":
		count = defaultImgCount()
		detailDesc = fmt.Sprintf("分辨率匹配计费(默认单价: %s)", fmtRate(rate))
		res := feat.Resolution
		if res == "" {
			res = "1k"
		}
		var tiers []ResolutionTier
		if json.Unmarshal([]byte(rule.PricingTiers), &tiers) == nil {
			tier, exact := pickResolutionTier(tiers, res)
			if tier != nil && exact {
				rate = tier.Rate
				detailDesc = fmt.Sprintf("命中分辨率阶梯 %s 单价: %s", res, fmtRate(rate))
				if feat.HasImageRef && tier.ImageRefMultiplier != nil && !tier.ImageRefMultiplier.Equal(decimal.NewFromInt(1)) {
					rate = rate.Mul(*tier.ImageRefMultiplier)
					detailDesc += fmt.Sprintf(" [图生图 x%s]", fmtRate(*tier.ImageRefMultiplier))
				}
			} else if tier != nil {
				rate = tier.Rate
				detailDesc = fmt.Sprintf("分辨率%s未命中,兜底最高阶梯(%s 单价:%s)", res, tier.Resolution, fmtRate(rate))
			}
		}
		applyPromptExtend(ext, feat.PromptExtend, &rate, &detailDesc)

	case "image_resolution_io":
		outputN := int64(1)
		if feat.ImageCount != nil && *feat.ImageCount > 1 {
			outputN = *feat.ImageCount
		}
		var inputN int64
		if feat.ImageRefCount != nil {
			inputN = *feat.ImageRefCount
		}
		res := feat.Resolution
		if res == "" {
			res = "1k"
		}
		var tiers []ResolutionTier
		_ = json.Unmarshal([]byte(rule.PricingTiers), &tiers)
		inRate, outRate, hitRes := decimal.Zero, rule.FixedRate, res
		if tier, _ := pickResolutionTier(tiers, res); tier != nil {
			inRate, outRate, hitRes = tier.InputRate, tier.Rate, tier.Resolution
		}
		cost := decimal.NewFromInt(inputN).Mul(inRate).
			Add(decimal.NewFromInt(outputN).Mul(outRate)).
			Mul(discount)
		return cost, fmt.Sprintf("命中分辨率 %s -> (输入:%d张*%s + 生成:%d张*%s) * %s倍率",
			hitRes, inputN, fmtRate(inRate), outputN, fmtRate(outRate), fmtDiscount(discount))

	case "image_size_pixel":
		count = defaultImgCount()
		detailDesc = fmt.Sprintf("分辨率像素匹配计费(默认单价: %s)", fmtRate(rate))
		normalized := NormalizePixelSize(feat.Size)
		var tiers []SizeTier
		if json.Unmarshal([]byte(rule.PricingTiers), &tiers) == nil {
			var matched *SizeTier
			if normalized != "" {
				for i := range tiers {
					if tiers[i].IsEnabled() && NormalizePixelSize(tiers[i].Size) == normalized {
						matched = &tiers[i]
						break
					}
				}
			}
			if matched != nil {
				r, qDesc := resolveSizeTierRate(*matched, feat.Quality)
				rate = r
				if qDesc == "" {
					detailDesc = fmt.Sprintf("命中分辨率像素 %s 单价: %s", normalized, fmtRate(rate))
				} else {
					detailDesc = fmt.Sprintf("命中分辨率像素 %s %s 单价: %s", normalized, qDesc, fmtRate(rate))
				}
				if feat.HasImageRef && matched.ImageRefMultiplier != nil && !matched.ImageRefMultiplier.Equal(decimal.NewFromInt(1)) {
					rate = rate.Mul(*matched.ImageRefMultiplier)
					detailDesc += fmt.Sprintf(" [图生图 x%s]", fmtRate(*matched.ImageRefMultiplier))
				}
			} else if mr, ms, ok := maxSizeTierRate(tiers, feat.Quality); ok {
				rate = mr
				if normalized == "" {
					detailDesc = fmt.Sprintf("size(%s)无法解析,兜底最高阶梯(%s 单价:%s)", feat.Size, ms, fmtRate(mr))
				} else {
					detailDesc = fmt.Sprintf("分辨率像素%s未命中,兜底最高阶梯(%s 单价:%s)", normalized, ms, fmtRate(mr))
				}
			}
		}
		applyPromptExtend(ext, feat.PromptExtend, &rate, &detailDesc)

	case "vidu_image":
		count = defaultImgCount()
		detailDesc = fmt.Sprintf("Vidu图片查表(默认单价: %s)", fmtRate(rate))
		res := feat.Resolution
		if res == "" {
			res = "1k"
		}
		var refCnt int64
		if feat.ImageRefCount != nil {
			refCnt = *feat.ImageRefCount
		}
		attr := "text"
		if feat.HasImageRef {
			switch {
			case refCnt <= 1:
				attr = "img2img"
			case refCnt <= 3:
				attr = "ref_1_3"
			default:
				attr = "ref_4_7"
			}
		}
		tableKey := attr + "|" + res
		if tr, ok := lookupPriceTable(ext, tableKey); ok {
			rate = tr
			detailDesc = fmt.Sprintf("Vidu图片查表(%s 单价:%s)", translateBillingKey(tableKey), fmtRate(rate))
		} else if mk, mp, ok := lookupPriceTablePrefixMax(ext, attr); ok {
			rate = mp
			detailDesc = fmt.Sprintf("Vidu图片%s未命中,兜底最高价(%s 单价:%s)",
				translateBillingKey(tableKey), translateBillingKey(mk), fmtRate(mp))
		}

	case "characters":
		var chars int64
		if feat.TextCharacters != nil {
			chars = *feat.TextCharacters
		}
		count = decimal.NewFromInt(chars).Div(tenThousand)
		detailDesc = fmt.Sprintf("按字符计费(%d字符)", chars)

	case "volc_seedream_pro":
		freeImages := freeImageCountFromExt(rule.ExtendedConfig, 1)
		var inputImages int64
		if feat.ImageRefCount != nil {
			inputImages = *feat.ImageRefCount
		}
		billableInputs := max0(inputImages - freeImages)
		inputRate := rule.PromptRate
		inputCost := decimal.NewFromInt(billableInputs).Mul(inputRate)

		imgCount := defaultImgCount()
		sz := feat.Size
		if sz == "" {
			sz = "1024x1024"
		}
		totalPixelsWan := PixelsWanFromSize(sz)
		outputRate := rule.FixedRate
		matchDesc := "默认输出单价"

		var tiers []VolcSeedreamTier
		if json.Unmarshal([]byte(rule.PricingTiers), &tiers) == nil && len(tiers) > 0 {
			slices.SortStableFunc(tiers, func(a, b VolcSeedreamTier) int {
				return a.MaxPixelsWan.Cmp(b.MaxPixelsWan)
			})
			var matched *VolcSeedreamTier
			for i := range tiers {
				if tiers[i].IsEnabled() && totalPixelsWan.LessThanOrEqual(tiers[i].MaxPixelsWan) {
					matched = &tiers[i]
					break
				}
			}
			if matched == nil {
				for i := len(tiers) - 1; i >= 0; i-- {
					if tiers[i].IsEnabled() {
						matched = &tiers[i]
						break
					}
				}
			}
			if matched != nil {
				layer := feat.LayerDecomposition && matched.LayerRate.IsPositive()
				kindLabel := "单图"
				if layer {
					outputRate = matched.LayerRate
					kindLabel = "图层"
				} else {
					outputRate = matched.Rate
				}
				matchDesc = fmt.Sprintf("命中阶梯(<= %s万像素 %s 单价: %s)",
					matched.MaxPixelsWan.Round(0).String(), kindLabel, fmtRate(outputRate))
			}
		}

		outputCost := imgCount.Mul(outputRate)
		totalCost := inputCost.Add(outputCost).Mul(discount)
		detail := fmt.Sprintf(
			"火山SeedreamPro计费 -> (输入图超额:%d张(共%d张,免费%d张)*%s¤/张 + 输出图: %s张*[总像素: %s万, %s])*%s倍率",
			billableInputs, inputImages, freeImages, fmtRate(inputRate),
			fmtRate(imgCount), totalPixelsWan.StringFixed(2), matchDesc, fmtDiscount(discount),
		)
		return totalCost, detail
	}

	cost := count.Mul(rate).Mul(discount)
	return cost, fmt.Sprintf("%s -> (%s量 * %s单价 * %s倍率)", detailDesc, fmtRate(count), fmtRate(rate), fmtDiscount(discount))
}
