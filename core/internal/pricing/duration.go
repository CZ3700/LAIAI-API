package pricing

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

func computeDurationCost(
	rule *BillingRule,
	_ UsageTokens,
	discount decimal.Decimal,
	feat ExtractedFeatures,
) (decimal.Decimal, string) {
	ext := parseExtMap(rule.ExtendedConfig)
	dur := feat.TotalVideoSeconds()
	rate := rule.DurationRate
	detailDesc := "固定按秒时长计费"

	matchResRate := func(res string, allowMaxWithoutRes bool) (decimal.Decimal, string) {
		if res == "" && !allowMaxWithoutRes {
			return rate, fmt.Sprintf("默认单价: %s", fmtRate(rate))
		}
		var tiers []ResolutionTier
		if json.Unmarshal([]byte(rule.PricingTiers), &tiers) != nil {
			return rate, fmt.Sprintf("默认单价: %s", fmtRate(rate))
		}
		var maxRate decimal.Decimal
		var maxRes string
		hasMax := false
		for _, t := range tiers {
			if !t.IsEnabled() {
				continue
			}
			if !hasMax || t.Rate.GreaterThan(maxRate) {
				maxRate, maxRes, hasMax = t.Rate, t.Resolution, true
			}
			if res != "" && strings.EqualFold(t.Resolution, res) {
				return t.Rate, fmt.Sprintf("命中分辨率 %s 单价: %s", res, fmtRate(t.Rate))
			}
		}
		if hasMax {
			if res != "" {
				return maxRate, fmt.Sprintf("分辨率%s未命中，兜底最高阶梯(%s 单价:%s)", res, maxRes, fmtRate(maxRate))
			}
			return maxRate, fmt.Sprintf("无分辨率，兜底最高阶梯(%s 单价:%s)", maxRes, fmtRate(maxRate))
		}
		return rate, fmt.Sprintf("默认单价: %s", fmtRate(rate))
	}

	settleVideoIO := func(detail string, outDur, outRate, inDur, inRate decimal.Decimal) (decimal.Decimal, string) {
		freeImages := freeImageCountFromExt(rule.ExtendedConfig, 5)
		var inputImages int64
		if feat.ImageRefCount != nil && *feat.ImageRefCount > 0 {
			inputImages = *feat.ImageRefCount
		}
		billableImages := max0(inputImages - freeImages)
		imageRate := rule.PromptRate
		totalCost := outDur.Mul(outRate).
			Add(inDur.Mul(inRate)).
			Add(decimal.NewFromInt(billableImages).Mul(imageRate)).
			Mul(discount)
		return totalCost, fmt.Sprintf(
			"%s -> (生成:%s秒*%s + 输入:%s秒*%s + 输入图超额:%d张(共%d张,免费%d张)*%s¤/张)*%s倍率",
			detail, fmtSec(outDur), fmtRate(outRate), fmtSec(inDur), fmtRate(inRate),
			billableImages, inputImages, freeImages, fmtRate(imageRate), fmtDiscount(discount),
		)
	}

	switch rule.BillingRule {
	case "video_resolution":
		detailDesc = fmt.Sprintf("视频分辨率阶梯找寻(默认单价: %s)", fmtRate(rate))
		if feat.Resolution != "" {
			rate, detailDesc = matchResRate(feat.Resolution, false)
		}

	case "minimax_h3":
		var desc string
		rate, desc = matchResRate(feat.Resolution, true)
		detailDesc = "视频秒价+输入图 " + desc
		freeImages := freeImageCountFromExt(rule.ExtendedConfig, 5)
		var inputImages int64
		if feat.ImageRefCount != nil && *feat.ImageRefCount > 0 {
			inputImages = *feat.ImageRefCount
		}
		billableImages := max0(inputImages - freeImages)
		imageRate := rule.PromptRate
		totalCost := dur.Mul(rate).Add(decimal.NewFromInt(billableImages).Mul(imageRate)).Mul(discount)
		return totalCost, fmt.Sprintf(
			"%s -> (%s秒*%s + 输入图超额:%d张(共%d张,免费%d张)*%s¤/张)*%s倍率",
			detailDesc, fmtSec(dur), fmtRate(rate), billableImages, inputImages, freeImages, fmtRate(imageRate), fmtDiscount(discount),
		)

	case "video_seconds_io":
		outDur := decimal.Zero
		if feat.DurationSeconds != nil {
			outDur = *feat.DurationSeconds
		}
		inDur := decimal.Zero
		if feat.VideoRefSeconds != nil {
			inDur = *feat.VideoRefSeconds
		}
		var tiers []ResolutionTier
		_ = json.Unmarshal([]byte(rule.PricingTiers), &tiers)
		var exact *ResolutionTier
		if feat.Resolution != "" {
			for i := range tiers {
				if tiers[i].IsEnabled() && strings.EqualFold(tiers[i].Resolution, feat.Resolution) {
					exact = &tiers[i]
					break
				}
			}
		}
		inRate, outRate, matchDesc := decimal.Zero, rate, fmt.Sprintf("默认单价: %s", fmtRate(rate))
		if exact != nil {
			inRate, outRate, matchDesc = exact.InputRate, exact.Rate, "命中分辨率 "+exact.Resolution
		} else {
			var best *ResolutionTier
			for i := range tiers {
				if tiers[i].IsEnabled() && (best == nil || tiers[i].Rate.GreaterThan(best.Rate)) {
					best = &tiers[i]
				}
			}
			if best != nil {
				inRate, outRate = best.InputRate, best.Rate
				if feat.Resolution != "" {
					matchDesc = fmt.Sprintf("分辨率%s未命中，兜底最高阶梯(%s)", feat.Resolution, best.Resolution)
				} else {
					matchDesc = fmt.Sprintf("无分辨率，兜底最高阶梯(%s)", best.Resolution)
				}
			}
		}
		return settleVideoIO("视频秒价(输入/生成)+输入图 "+matchDesc, outDur, outRate, inDur, inRate)

	case "video_seconds_ref":
		outDur := decimal.Zero
		if feat.DurationSeconds != nil {
			outDur = *feat.DurationSeconds
		}
		inDur := decimal.Zero
		if feat.VideoRefSeconds != nil {
			inDur = *feat.VideoRefSeconds
		}
		hasRefVideo := inDur.IsPositive() || feat.HasVideo
		refAware := extBool(ext, "enable_video_ref", true)

		pickRates := func(t ResolutionTier) (decimal.Decimal, decimal.Decimal) {
			if !refAware {
				return t.InputRate, t.Rate
			}
			if hasRefVideo {
				ir, or := t.InputRate, t.Rate
				if t.InputRateRef != nil {
					ir = *t.InputRateRef
				}
				if t.RateRef != nil {
					or = *t.RateRef
				}
				return ir, or
			}
			or := t.Rate
			if t.RateNoRef != nil {
				or = *t.RateNoRef
			}
			return decimal.Zero, or
		}

		var tiers []ResolutionTier
		_ = json.Unmarshal([]byte(rule.PricingTiers), &tiers)
		var exact *ResolutionTier
		if feat.Resolution != "" {
			for i := range tiers {
				if !tiers[i].IsEnabled() {
					continue
				}
				if strings.EqualFold(tiers[i].Resolution, feat.Resolution) {
					exact = &tiers[i]
					break
				}
				for _, part := range strings.Split(tiers[i].Resolution, "/") {
					if strings.EqualFold(strings.TrimSpace(part), feat.Resolution) {
						exact = &tiers[i]
						break
					}
				}
				if exact != nil {
					break
				}
			}
		}
		inRate, outRate, matchDesc := decimal.Zero, rate, fmt.Sprintf("默认单价: %s", fmtRate(rate))
		if exact != nil {
			inRate, outRate = pickRates(*exact)
			matchDesc = "命中分辨率 " + exact.Resolution
		} else {
			var best *ResolutionTier
			for i := range tiers {
				if tiers[i].IsEnabled() && (best == nil || tiers[i].Rate.GreaterThan(best.Rate)) {
					best = &tiers[i]
				}
			}
			if best != nil {
				inRate, outRate = pickRates(*best)
				if feat.Resolution != "" {
					matchDesc = fmt.Sprintf("分辨率%s未命中，兜底最高阶梯(%s)", feat.Resolution, best.Resolution)
				} else {
					matchDesc = fmt.Sprintf("无分辨率，兜底最高阶梯(%s)", best.Resolution)
				}
			}
		}
		modeDesc := "标准双向秒价"
		if refAware {
			if hasRefVideo {
				modeDesc = "有参考视频"
			} else {
				modeDesc = "无参考视频"
			}
		}
		return settleVideoIO(fmt.Sprintf("双向秒价+参考视频(%s): %s", modeDesc, matchDesc), outDur, outRate, inDur, inRate)

	case "fal_ref_video":
		outDur := decimal.Zero
		if feat.DurationSeconds != nil {
			outDur = *feat.DurationSeconds
		}
		outputCost := outDur.Mul(rule.DurationRate)
		freeRefTokens, ok := extDecimal(ext, "free_ref_tokens")
		if !ok {
			freeRefTokens = decimal.NewFromInt(4096)
		}
		refTokenRatePer1k, ok := extDecimal(ext, "ref_token_rate_per_1k")
		if !ok {
			refTokenRatePer1k = decimal.RequireFromString("0.02")
		}
		imgTokensDefault, ok := extDecimal(ext, "image_tokens_default")
		if !ok {
			imgTokensDefault = decimal.NewFromInt(1024)
		}
		audioTokensPerSec, ok := extDecimal(ext, "audio_ref_tokens_per_sec")
		if !ok {
			audioTokensPerSec = decimal.NewFromInt(80)
		}
		is480p := strings.Contains(strings.ToLower(feat.Resolution), "480")
		videoTokensPerSec := decimal.NewFromInt(7459)
		if is480p {
			videoTokensPerSec = decimal.NewFromInt(2886)
		}
		if rawVMap, ok := ext["video_ref_tokens_per_sec"]; ok {
			var vMap map[string]json.RawMessage
			if json.Unmarshal(rawVMap, &vMap) == nil {
				key := "768p"
				if is480p {
					key = "480p"
				}
				if rawV, ok := vMap[key]; ok {
					if d, ok := parseJSONDecimal(rawV); ok {
						videoTokensPerSec = d
					}
				}
			}
		}

		var imgCount int64
		if feat.ImageRefCount != nil && *feat.ImageRefCount > 0 {
			imgCount = *feat.ImageRefCount
		}
		imgTokens := decimal.NewFromInt(imgCount).Mul(imgTokensDefault)
		videoRefSec := decimal.Zero
		if feat.VideoRefSeconds != nil && feat.VideoRefSeconds.IsPositive() {
			videoRefSec = *feat.VideoRefSeconds
		}
		videoTokens := videoRefSec.Mul(videoTokensPerSec).Round(0)
		audioRefSec := decimal.Zero
		if feat.AudioRefSeconds != nil && feat.AudioRefSeconds.IsPositive() {
			audioRefSec = *feat.AudioRefSeconds
		}
		audioTokens := audioRefSec.Mul(audioTokensPerSec).Round(0)

		totalRefTokens := imgTokens.Add(videoTokens).Add(audioTokens)
		billableRefTokens := totalRefTokens.Sub(freeRefTokens)
		if billableRefTokens.IsNegative() {
			billableRefTokens = decimal.Zero
		}
		refCost := billableRefTokens.Div(oneThousand).Mul(refTokenRatePer1k)
		totalCost := outputCost.Add(refCost).Mul(discount)
		var refParts []string
		if imgCount > 0 {
			refParts = append(refParts, fmt.Sprintf("图:%d张(%sToken)", imgCount, imgTokens.Round(0).String()))
		}
		if videoRefSec.IsPositive() {
			rLabel := "768p"
			if is480p {
				rLabel = "480p"
			}
			refParts = append(refParts, fmt.Sprintf("视频:%s秒(%sToken@%s)", videoRefSec.StringFixed(1), videoTokens.Round(0).String(), rLabel))
		}
		if audioRefSec.IsPositive() {
			refParts = append(refParts, fmt.Sprintf("音频:%s秒(%sToken)", audioRefSec.StringFixed(1), audioTokens.Round(0).String()))
		}
		refDesc := "无参考素材"
		if len(refParts) > 0 {
			refDesc = strings.Join(refParts, "+")
		}
		return totalCost, fmt.Sprintf(
			"fal H3-MAX 视频 -> (成片:%s秒*%s¤ + 参考Token:[%s -> 总%s-免%s=计费%s]*%s¤/千Token)*%s倍率",
			fmtSec(outDur), fmtRate(rule.DurationRate), refDesc,
			totalRefTokens.Round(0).String(), freeRefTokens.Round(0).String(), billableRefTokens.Round(0).String(),
			fmtRate(refTokenRatePer1k), fmtDiscount(discount),
		)

	case "video_quality":
		detailDesc = fmt.Sprintf("视频画质阶梯找寻(默认单价: %s)", fmtRate(rate))
		if feat.Resolution != "" {
			var tiers []VideoQualityTier
			if json.Unmarshal([]byte(rule.PricingTiers), &tiers) == nil {
				fpsVal := decimal.NewFromInt(30)
				if feat.FPS != nil {
					fpsVal = *feat.FPS
				}
				isHighFPS := fpsVal.GreaterThan(decimal.NewFromInt(30))
				targetRes, shortSide := parseVideoQualityRes(feat.Resolution)
				var maxRate decimal.Decimal
				var maxTierDesc string
				hasMax, matched := false, false
				for _, t := range tiers {
					if !t.IsEnabled() {
						continue
					}
					if !hasMax || t.Rate.GreaterThan(maxRate) {
						maxRate, maxTierDesc, hasMax = t.Rate, t.Resolution+" "+t.FPSRange, true
					}
					fpsMatch := false
					if isHighFPS {
						fpsMatch = strings.Contains(t.FPSRange, ">30") || strings.Contains(t.FPSRange, "> 30")
					} else {
						fpsMatch = strings.Contains(t.FPSRange, "<30") || strings.Contains(t.FPSRange, "<=30") || strings.Contains(t.FPSRange, "<= 30")
					}
					if strings.EqualFold(t.Resolution, targetRes) && fpsMatch {
						rate = t.Rate
						if shortSide > 0 {
							detailDesc = fmt.Sprintf("命中视频画质阶梯 [%s] [帧率: %s] (解析短边: %d) 单价: %s", targetRes, t.FPSRange, shortSide, fmtRate(rate))
						} else {
							detailDesc = fmt.Sprintf("命中视频画质阶梯 [%s] [帧率: %s] 单价: %s", targetRes, t.FPSRange, fmtRate(rate))
						}
						matched = true
						break
					}
				}
				if !matched && hasMax {
					rate = maxRate
					detailDesc = fmt.Sprintf("视频画质阶梯未命中 [%s] (帧率: %s)，兜底最高阶梯(%s 单价:%s)", targetRes, fmtRate(fpsVal), maxTierDesc, fmtRate(maxRate))
				}
			}
		}

	case "kling_video":
		detailDesc = fmt.Sprintf("可灵视频按秒计费(基准: %s)", fmtRate(rate))
		modeKey := "std"
		if extBool(ext, "enable_mode", true) && feat.Resolution != "" {
			switch strings.ToLower(feat.Resolution) {
			case "720p", "480p":
				modeKey = "std"
			case "1080p":
				modeKey = "pro"
			default:
				modeKey = strings.ToLower(feat.Resolution)
			}
		}
		soundKey := "off"
		if extBool(ext, "enable_sound", true) && feat.Sound != "" {
			soundKey = feat.Sound
		}
		videoKey := "no"
		if feat.HasVideo && extBool(ext, "enable_video_ref", false) {
			videoKey = "yes"
		}
		tableKey := fmt.Sprintf("%s|%s|%s", modeKey, soundKey, videoKey)
		if tr, ok := lookupPriceTable(ext, tableKey); ok {
			rate = tr
			detailDesc = fmt.Sprintf("可灵视频查表(%s 单价:%s)", translateBillingKey(tableKey), fmtRate(rate))
		} else {
			mMult := lookupNestedMultiplier(ext, "mode_multipliers", modeKey)
			sMult := lookupNestedMultiplier(ext, "sound_multipliers", soundKey)
			rate = rate.Mul(mMult).Mul(sMult)
			detailDesc = fmt.Sprintf("可灵视频倍率(mode:%sx%s sound:%sx%s 单价:%s)",
				modeKey, fmtRate(mMult), soundKey, fmtRate(sMult), fmtRate(rate))
		}

	case "kling4_video":
		tOut := decimal.NewFromInt(5)
		if feat.DurationSeconds != nil && feat.DurationSeconds.GreaterThanOrEqual(decimal.NewFromInt(1)) {
			tOut = *feat.DurationSeconds
		}
		tIn := decimal.Zero
		if feat.VideoRefSeconds != nil && feat.VideoRefSeconds.IsPositive() {
			tIn = *feat.VideoRefSeconds
		}
		hasIn := feat.HasVideo || tIn.IsPositive()
		resRaw := strings.ToLower(feat.Resolution)
		if resRaw == "" {
			resRaw = "720p"
		}
		matchedRes := "720p"
		rateNoIn := decimal.NewFromInt(1)
		rateWithIn := decimal.RequireFromString("0.6")
		if rawRates, ok := ext["resolution_rates"]; ok {
			var rMap map[string]map[string]json.RawMessage
			if json.Unmarshal(rawRates, &rMap) == nil && len(rMap) > 0 {
				var tier map[string]json.RawMessage
				if t, ok := rMap[resRaw]; ok {
					matchedRes, tier = resRaw, t
				} else if t, ok := rMap["720p"]; ok {
					matchedRes, tier = "720p", t
				} else {
					for k, t := range rMap {
						matchedRes, tier = k, t
						break
					}
				}
				if d, ok := parseJSONDecimal(tier["no_input"]); ok {
					rateNoIn = d
				}
				if d, ok := parseJSONDecimal(tier["with_input"]); ok {
					rateWithIn = d
				}
			}
		}
		var tBill, activeRate decimal.Decimal
		var sceneDesc string
		switch {
		case !hasIn:
			tBill, activeRate = tOut, rateNoIn
			sceneDesc = fmt.Sprintf("无视, 计费时长:%ss[输出%ss]", tOut.StringFixed(1), tOut.StringFixed(1))
		case tIn.LessThanOrEqual(tOut):
			tBill, activeRate = tOut.Mul(twoDec), rateWithIn
			sceneDesc = fmt.Sprintf("含视(入<=出), 计费时长:%ss[输出%ss*2, 输入%ss]", tBill.StringFixed(1), tOut.StringFixed(1), tIn.StringFixed(1))
		default:
			tBill, activeRate = tIn.Add(tOut), rateWithIn
			sceneDesc = fmt.Sprintf("含视(入>出), 计费时长:%ss[输入%ss+输出%ss]", tBill.StringFixed(1), tIn.StringFixed(1), tOut.StringFixed(1))
		}
		totalCost := tBill.Mul(activeRate).Mul(discount)
		return totalCost, fmt.Sprintf("可灵4.0视频[%s] %s 单价:%s -> (%s秒 * %s单价 * %s倍率)",
			matchedRes, sceneDesc, fmtRate(activeRate), fmtSec(tBill), fmtRate(activeRate), fmtDiscount(discount))

	case "vidu_video":
		detailDesc = fmt.Sprintf("Vidu视频查表(基准: %s)", fmtRate(rate))
		res := feat.Resolution
		if res == "" {
			res = "540p"
		}
		var refCnt int64
		if feat.ImageRefCount != nil {
			refCnt = *feat.ImageRefCount
		}
		attr := "text"
		if feat.HasVideo || feat.HasAudio || refCnt > 2 {
			attr = "ref"
		} else if feat.HasImageRef {
			attr = "image"
		}
		tableKey := attr + "|" + res
		if tr, ok := lookupPriceTable(ext, tableKey); ok {
			rate = tr
			detailDesc = fmt.Sprintf("Vidu视频查表(%s 单价:%s)", translateBillingKey(tableKey), fmtRate(rate))
		} else if mk, mp, ok := lookupPriceTablePrefixMax(ext, attr); ok {
			rate = mp
			detailDesc = fmt.Sprintf("Vidu视频%s未命中,兜底最高价(%s 单价:%s)",
				translateBillingKey(tableKey), translateBillingKey(mk), fmtRate(mp))
		}
		if feat.ServiceTier == "flex" {
			if offDisc, ok := extDecimal(ext, "offpeak_discount"); ok && offDisc.LessThan(decimal.NewFromInt(1)) {
				rate = rate.Mul(offDisc)
				detailDesc += fmt.Sprintf(" [错峰 x%s]", fmtRate(offDisc))
			}
		}

	case "volc_enhance_cascade":
		detailDesc = fmt.Sprintf("火山级联封装查表(基准: %s)", fmtRate(rate))
		ver := feat.Version
		if ver == "" {
			ver = "standard"
		}
		res := feat.Resolution
		if res == "" {
			res = "720p"
		}
		vid := "no"
		if feat.HasVideo {
			vid = "yes"
		}
		tableKey := fmt.Sprintf("%s|%s|%s", ver, res, vid)
		if tr, ok := lookupPriceTable(ext, tableKey); ok {
			rate = tr
			detailDesc = fmt.Sprintf("火山级联封装查表(%s 单价:%s)", translateBillingKey(tableKey), fmtRate(rate))
		} else if mk, mp, ok := lookupPriceTablePrefixMax(ext, ver); ok {
			rate = mp
			detailDesc = fmt.Sprintf("火山级联封装%s未命中,兜底最高价(%s 单价:%s)",
				translateBillingKey(tableKey), translateBillingKey(mk), fmtRate(mp))
		}
	}

	cost := dur.Mul(rate).Mul(discount)
	return cost, fmt.Sprintf("%s -> (%s秒 * %s单价 * %s倍率)", detailDesc, fmtSec(dur), fmtRate(rate), fmtDiscount(discount))
}

func lookupNestedMultiplier(ext map[string]json.RawMessage, groupKey, itemKey string) decimal.Decimal {
	one := decimal.NewFromInt(1)
	if ext == nil {
		return one
	}
	rawGrp, ok := ext[groupKey]
	if !ok {
		return one
	}
	var grp map[string]json.RawMessage
	if json.Unmarshal(rawGrp, &grp) != nil {
		return one
	}
	if raw, ok := grp[itemKey]; ok {
		if d, ok := parseJSONDecimal(raw); ok {
			return d
		}
	}
	return one
}

func parseVideoQualityRes(res string) (string, int64) {
	rn := strings.ReplaceAll(strings.ToLower(res), "*", "x")
	switch {
	case strings.Contains(rn, "720p") || rn == "720":
		return "720p", 720
	case strings.Contains(rn, "1080p") || rn == "1080":
		return "1080p", 1080
	case strings.Contains(rn, "2k"):
		return "2k", 1440
	case strings.Contains(rn, "4k"):
		return "4k", 2160
	}
	var side int64
	if wStr, hStr, ok := strings.Cut(rn, "x"); ok {
		w, e1 := strconv.ParseInt(strings.TrimSpace(wStr), 10, 64)
		h, e2 := strconv.ParseInt(strings.TrimSpace(hStr), 10, 64)
		if e1 == nil && e2 == nil {
			if w < h {
				side = w
			} else {
				side = h
			}
		}
	}
	if side <= 0 {
		return "unknown", 0
	}
	switch {
	case side <= 720:
		return "720p", side
	case side <= 1080:
		return "1080p", side
	case side <= 1440:
		return "2k", side
	default:
		return "4k", side
	}
}
