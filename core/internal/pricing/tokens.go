package pricing

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

func computeTokensCost(
	rule *BillingRule,
	usage UsageTokens,
	discount decimal.Decimal,
	feat ExtractedFeatures,
) (decimal.Decimal, string) {
	ext := parseExtMap(rule.ExtendedConfig)

	applyWebSearch := func(tokenCostRaw decimal.Decimal, tokenDetailRaw string) (decimal.Decimal, string) {
		wsCount := usage.WebSearch
		if feat.WebSearch != nil {
			wsCount = *feat.WebSearch
		}
		if wsCount < 0 {
			wsCount = 0
		}
		wsRate, _ := extDecimal(ext, "web_search_rate")
		if wsCount > 0 && wsRate.IsPositive() {
			wsCost := decimal.NewFromInt(wsCount).Mul(wsRate).Div(oneThousand)
			cost := tokenCostRaw.Add(wsCost).Mul(discount)
			detail := fmt.Sprintf("[ %s + 联网搜索: %d次*%s¤/千次 ] * %s倍率",
				tokenDetailRaw, wsCount, fmtRate(wsRate), fmtDiscount(discount))
			return cost, detail
		}
		return tokenCostRaw.Mul(discount), fmt.Sprintf("%s * %s倍率", tokenDetailRaw, fmtDiscount(discount))
	}

	promptTokens := usage.Prompt
	completionTokens := usage.Completion
	cachedTokens := usage.Cached
	cacheWriteTokens := usage.CacheWrite
	audioTokens := usage.AudioTokens
	audioCachedTokens := usage.AudioCachedTokens

	pRate := rule.PromptRate
	cRate := rule.CompletionRate
	isOverridden := false
	isGLM53 := rule.BillingRule == "glm_5_3"
	isClaudeRule := rule.BillingRule == "claude"
	detailDesc := "标准 Tokens 计费"
	if isGLM53 {
		detailDesc = "glm 5.3"
	} else if isClaudeRule {
		detailDesc = "Claude 计费"
	}

	switch rule.BillingRule {
	case "seedance2.0":
		if rawRates, ok := ext["resolution_rates"]; ok {
			var rates map[string]map[string]json.RawMessage
			if json.Unmarshal(rawRates, &rates) == nil && len(rates) > 0 {
				normRes := Seedance2RequestResolution(feat.Resolution, feat.Size)
				resKey := normRes
				if resKey == "" {
					resKey = "720p"
				}
				rateField, videoLabel := "without_video", "无视频"
				if feat.HasVideo {
					rateField, videoLabel = "with_video", "含视频"
				}
				if matchedKey, rateVal, ok := seedance2MatchTier(rates, normRes, rateField); ok {
					pRate = rateVal
					cRate = rateVal
					isOverridden = true
					displayRes := matchedKey
					if strings.EqualFold(matchedKey, resKey) {
						displayRes = resKey
					}
					detailDesc = fmt.Sprintf("Seedance2规则(%s|%s|基本单价:%s)", displayRes, videoLabel, fmtRate(rateVal))
				}
			}
		}
	case "seedance1.5pro":
		var picked decimal.Decimal
		var found bool
		var desc string
		if feat.HasAudio {
			if ar, ok := extDecimal(ext, "audio_rate"); ok {
				picked, found = ar, true
				desc = fmt.Sprintf("Seedance1.5规则(含语音单价:%s)", fmtRate(ar))
			}
		} else {
			if br, ok := extDecimal(ext, "base_rate"); ok {
				picked, found = br, true
				desc = fmt.Sprintf("Seedance1.5规则(无语音单价:%s)", fmtRate(br))
			}
		}
		if found {
			if feat.ServiceTier == "flex" {
				offDisc, ok := extDecimal(ext, "offline_discount")
				if !ok {
					offDisc = pointFive
				}
				picked = picked.Mul(offDisc)
				desc += " [离线推理]"
			}
			pRate = picked
			cRate = picked
			isOverridden = true
			detailDesc = desc
		}
	case "seedance1.0":
		if feat.ServiceTier == "flex" {
			if r, ok := extDecimal(ext, "offline_rate"); ok {
				pRate, cRate, isOverridden = r, r, true
				detailDesc = fmt.Sprintf("Seedance1规则(离线推理单价:%s)", fmtRate(r))
			}
		} else {
			if r, ok := extDecimal(ext, "online_rate"); ok {
				pRate, cRate, isOverridden = r, r, true
				detailDesc = fmt.Sprintf("Seedance1规则(在线推理单价:%s)", fmtRate(r))
			}
		}
	}

	cachedR := rule.CachedRate
	isCachedRateSet := cachedR.IsPositive()
	var cacheWriteR decimal.Decimal
	isCacheWriteRateSet := false

	if !isOverridden && rule.BillingRule == "tiered" {
		var tiers []PricingTier
		_ = json.Unmarshal([]byte(rule.PricingTiers), &tiers)
		sort.SliceStable(tiers, func(i, j int) bool {
			return tiers[i].MaxPromptTokens.LessThan(tiers[j].MaxPromptTokens)
		})
		pK := decimal.NewFromInt(promptTokens).Div(oneThousand)
		cK := decimal.NewFromInt(completionTokens).Div(oneThousand)
		var matched *PricingTier
		var desc string
		for i := range tiers {
			t := &tiers[i]
			if pK.LessThanOrEqual(t.MaxPromptTokens) && (t.MaxCompletionTokens == nil || cK.LessThanOrEqual(*t.MaxCompletionTokens)) {
				matched = t
				if t.MaxCompletionTokens != nil {
					desc = fmt.Sprintf("阶梯(≤%sK输入|≤%sK输出)", fmtRate(t.MaxPromptTokens), fmtRate(*t.MaxCompletionTokens))
				} else {
					desc = fmt.Sprintf("阶梯(≤%sK输入)", fmtRate(t.MaxPromptTokens))
				}
				break
			}
		}
		if matched == nil && len(tiers) > 0 {
			matched = &tiers[len(tiers)-1]
			desc = fmt.Sprintf("阶梯(超限·最高档%sK输入)", fmtRate(matched.MaxPromptTokens))
		}
		if matched != nil {
			pRate = matched.PromptRate
			cRate = matched.CompletionRate
			isCachedRateSet = matched.CachedRate.IsPositive()
			if isCachedRateSet {
				cachedR = matched.CachedRate
			}
			isCacheWriteRateSet = matched.CacheWriteRate.IsPositive()
			if isCacheWriteRateSet {
				cacheWriteR = matched.CacheWriteRate
			}
			detailDesc = desc
		}
	} else if !isOverridden && rule.BillingRule == "doubao_chat" {
		var tiers []PricingTier
		_ = json.Unmarshal([]byte(rule.PricingTiers), &tiers)
		sort.SliceStable(tiers, func(i, j int) bool {
			return tiers[i].MaxPromptTokens.LessThan(tiers[j].MaxPromptTokens)
		})
		pK := decimal.NewFromInt(promptTokens).Div(oneThousand)
		cK := decimal.NewFromInt(completionTokens).Div(oneThousand)
		var matched *PricingTier
		for i := range tiers {
			t := &tiers[i]
			if pK.LessThanOrEqual(t.MaxPromptTokens) && (t.MaxCompletionTokens == nil || cK.LessThanOrEqual(*t.MaxCompletionTokens)) {
				matched = t
				break
			}
		}
		if matched == nil && len(tiers) > 0 {
			matched = &tiers[len(tiers)-1]
		}
		if matched != nil {
			isFast := feat.ServiceTier == "fast"
			tierDesc := fmt.Sprintf("≤%sK输入", fmtRate(matched.MaxPromptTokens))
			if matched.MaxCompletionTokens != nil {
				tierDesc = fmt.Sprintf("≤%sK输入|≤%sK输出", fmtRate(matched.MaxPromptTokens), fmtRate(*matched.MaxCompletionTokens))
			}
			effPrompt := max0(promptTokens - cachedTokens)
			nonAudioPrompt := max0(effPrompt - audioTokens)
			nonAudioCached := max0(cachedTokens - audioCachedTokens)

			pick := func(fast, base decimal.Decimal) decimal.Decimal {
				if isFast && fast.IsPositive() {
					return fast
				}
				return base
			}
			pr := pick(matched.FastPromptRate, matched.PromptRate)
			cr := pick(matched.FastCompletionRate, matched.CompletionRate)
			cacheR := pick(matched.FastCachedRate, matched.CachedRate)
			audioPR := pick(matched.FastAudioPromptRate, matched.AudioPromptRate)
			audioCacheR := pick(matched.FastAudioCachedRate, matched.AudioCachedRate)

			costRaw := decimal.NewFromInt(nonAudioPrompt).Mul(pr).
				Add(decimal.NewFromInt(audioTokens).Mul(audioPR)).
				Add(decimal.NewFromInt(nonAudioCached).Mul(cacheR)).
				Add(decimal.NewFromInt(audioCachedTokens).Mul(audioCacheR)).
				Add(decimal.NewFromInt(completionTokens).Mul(cr)).
				Div(oneMillion)

			modeLabel := "常规"
			if isFast {
				modeLabel = "低延迟"
			}
			dRaw := fmt.Sprintf(
				"豆包阶梯[%s](%s) -> (%d非音频输入*%s + %d音频输入*%s + %d非音频缓存*%s + %d音频缓存*%s + %d输出*%s)/1M",
				modeLabel, tierDesc,
				nonAudioPrompt, fmtRate(pr), audioTokens, fmtRate(audioPR),
				nonAudioCached, fmtRate(cacheR), audioCachedTokens, fmtRate(audioCacheR),
				completionTokens, fmtRate(cr),
			)
			return applyWebSearch(costRaw, dRaw)
		}
	} else if !isOverridden && rule.BillingRule == "kimi_chat" {
		is1h := feat.CacheTTL == "1h"
		pRate = rule.PromptRate
		cRate = rule.CompletionRate
		isCachedRateSet = true
		if rule.CachedRate.IsPositive() {
			cachedR = rule.CachedRate
		} else {
			cachedR = pRate.Mul(pointOne).Round(6)
		}
		w5m, ok5 := extDecimal(ext, "cache_write_rate")
		if !ok5 || !w5m.IsPositive() {
			w5m = pRate
		}
		w1h, ok1h := extDecimal(ext, "cache_write_1h_rate")
		if !ok1h || !w1h.IsPositive() {
			w1h = w5m.Mul(twoDec).Round(6)
		}
		isCacheWriteRateSet = true
		if is1h {
			cacheWriteR = w1h
			detailDesc = "Kimi缓存(1h写)"
		} else {
			cacheWriteR = w5m
			detailDesc = "Kimi缓存(5m写)"
		}
	} else if !isOverridden && rule.BillingRule == "multimodal" {
		imgPromptRate := rule.PromptRate
		if r, ok := extDecimal(ext, "image_prompt_rate"); ok {
			imgPromptRate = r
		}
		imgCompRate := rule.CompletionRate
		if r, ok := extDecimal(ext, "image_completion_rate"); ok {
			imgCompRate = r
		}
		textPrompt := max0(promptTokens - usage.ImageTokens)
		textComp := max0(completionTokens - usage.OutputImageTokens)
		thoughts := usage.ReasoningTokens
		if thoughts > textComp {
			thoughts = textComp
		}
		pureText := textComp - thoughts

		costRaw := decimal.NewFromInt(textPrompt).Mul(pRate).
			Add(decimal.NewFromInt(usage.ImageTokens).Mul(imgPromptRate)).
			Add(decimal.NewFromInt(textComp).Mul(cRate)).
			Add(decimal.NewFromInt(usage.OutputImageTokens).Mul(imgCompRate)).
			Div(oneMillion)

		var parts []string
		if textPrompt > 0 || (usage.ImageTokens == 0 && promptTokens > 0) {
			parts = append(parts, fmt.Sprintf("%d文本输入*%s", textPrompt, fmtRate(pRate)))
		}
		if usage.ImageTokens > 0 {
			parts = append(parts, fmt.Sprintf("%d图片输入*%s", usage.ImageTokens, fmtRate(imgPromptRate)))
		}
		if thoughts > 0 {
			if pureText > 0 {
				parts = append(parts, fmt.Sprintf("%d文本输出*%s", pureText, fmtRate(cRate)))
			}
			parts = append(parts, fmt.Sprintf("%d思考*%s", thoughts, fmtRate(cRate)))
		} else if textComp > 0 || (usage.OutputImageTokens == 0 && completionTokens > 0) {
			parts = append(parts, fmt.Sprintf("%d输出*%s", textComp, fmtRate(cRate)))
		}
		if usage.OutputImageTokens > 0 {
			parts = append(parts, fmt.Sprintf("%d图片输出*%s", usage.OutputImageTokens, fmtRate(imgCompRate)))
		}
		if len(parts) == 0 {
			parts = append(parts, fmt.Sprintf("%d总输入*%s", promptTokens, fmtRate(pRate)))
		}
		return applyWebSearch(costRaw, fmt.Sprintf("多模态计费 -> (%s)/1M", strings.Join(parts, " + ")))
	} else if !isOverridden && rule.BillingRule == "gpt_billing" {
		costRaw := decimal.Zero
		var detailParts []string
		if rawGpt, ok := ext["gpt_config"]; ok {
			var gptCfg map[string]struct {
				Enabled bool            `json:"enabled"`
				Rate    decimal.Decimal `json:"rate"`
			}
			if json.Unmarshal(rawGpt, &gptCfg) == nil {
				imgInput := usage.ImageTokens
				textInput := max0(promptTokens - imgInput)
				var imgCached, textCached int64
				if cachedTokens > 0 {
					if feat.HasImageRef {
						imgCached = cachedTokens
					} else {
						textCached = cachedTokens
					}
				}
				items := []struct {
					key, label string
					tokens     int64
				}{
					{"input_text", "文本输入", max0(textInput - textCached)},
					{"input_image", "图片输入", max0(imgInput - imgCached)},
					{"cached_input_text", "文本缓存", textCached},
					{"cached_input_image", "图片缓存", imgCached},
					{"output_image", "图片输出", completionTokens},
				}
				for _, it := range items {
					if cfg, ok := gptCfg[it.key]; ok && cfg.Enabled {
						costRaw = costRaw.Add(decimal.NewFromInt(it.tokens).Mul(cfg.Rate))
						detailParts = append(detailParts, fmt.Sprintf("%s:%d*%s", it.label, it.tokens, fmtRate(cfg.Rate)))
					}
				}
				costRaw = costRaw.Div(oneMillion)
			}
		}
		dRaw := "GPT图片计费(未启用任何计费项)"
		if len(detailParts) > 0 {
			dRaw = fmt.Sprintf("GPT图片计费 -> (%s)/1M", strings.Join(detailParts, " + "))
		}
		return costRaw.Mul(discount), fmt.Sprintf("%s * %s倍率", dRaw, fmtDiscount(discount))
	}

	if usage.ImageTokens > promptTokens {
		promptTokens = usage.ImageTokens
	}

	if feat.ServiceTier == "flex" && !strings.Contains(detailDesc, "离线") && rule.BillingRule != "seedance2.0" {
		offDisc, ok := extDecimal(ext, "offline_discount")
		if !ok {
			offDisc = pointFive
		}
		pRate = pRate.Mul(offDisc)
		cRate = cRate.Mul(offDisc)
		if isCachedRateSet {
			cachedR = cachedR.Mul(offDisc)
		}
		if isCacheWriteRateSet {
			cacheWriteR = cacheWriteR.Mul(offDisc)
		}
		detailDesc = fmt.Sprintf("%s [叠加Flex离线折扣: %s倍]", detailDesc, fmtRate(offDisc))
	}

	cc := usage.CacheCreation
	is1hTTL := feat.CacheTTL == "1h"
	isClaude1h := is1hTTL || usage.CacheCreation1h > 0
	ccRate := rule.ClaudeCacheCreationRate
	if isClaude1h && rule.ClaudeCacheCreationRate.IsPositive() {
		if r1h, ok := extDecimal(ext, "claude_cache_creation_1h_rate"); ok && r1h.IsPositive() {
			ccRate = r1h
		} else {
			ccRate = rule.ClaudeCacheCreationRate.Mul(onePointFive)
		}
	}
	crRate := rule.ClaudeCacheReadRate
	billCC := !isGLM53 && cc > 0 && ccRate.IsPositive()
	billCR := !isGLM53 && cachedTokens > 0 && crRate.IsPositive()
	isClaude := isClaudeRule || billCC || billCR

	if is1hTTL && isCacheWriteRateSet && rule.BillingRule != "kimi_chat" && !isClaudeRule {
		if r1h, ok := extDecimal(ext, "cache_write_1h_rate"); ok && r1h.IsPositive() {
			cacheWriteR = r1h
		} else if cacheWriteR.IsPositive() {
			cacheWriteR = cacheWriteR.Mul(twoDec)
		}
	}

	isOpenAIFormat := isGLM53 || (usage.HasTotalTokens && usage.Total == promptTokens+completionTokens)

	var cost decimal.Decimal
	var detailStr string
	if isClaude {
		var subCC, subCR int64
		if billCC {
			subCC = cc
		}
		if billCR {
			subCR = cachedTokens
		}
		effPrompt := promptTokens
		if isOpenAIFormat && promptTokens >= subCC+subCR {
			effPrompt = promptTokens - subCC - subCR
		}
		creation := decimal.Zero
		if billCC {
			creation = decimal.NewFromInt(cc).Mul(ccRate)
		}
		read := decimal.Zero
		if billCR {
			read = decimal.NewFromInt(cachedTokens).Mul(crRate)
		}
		cost = decimal.NewFromInt(effPrompt).Mul(pRate).
			Add(decimal.NewFromInt(completionTokens).Mul(cRate)).
			Add(creation).
			Add(read).
			Div(oneMillion)

		var sb strings.Builder
		fmt.Fprintf(&sb, "%s -> (%d输入*%s + %d输出*%s", detailDesc, effPrompt, fmtRate(pRate), completionTokens, fmtRate(cRate))
		if billCC {
			tag := "创建"
			if isClaude1h {
				tag = "创建(1h)"
			}
			fmt.Fprintf(&sb, " + %d%s@%s", cc, tag, fmtRate(ccRate))
		}
		if billCR {
			fmt.Fprintf(&sb, " + %d读取@%s", cachedTokens, fmtRate(crRate))
		}
		sb.WriteString(")/1M")
		detailStr = sb.String()
	} else {
		effPrompt, billCache, billWrite := splitPromptSubsets(
			promptTokens, cachedTokens, cacheWriteTokens,
			isCachedRateSet, isCacheWriteRateSet, isOpenAIFormat,
		)
		cost = decimal.NewFromInt(effPrompt).Mul(pRate).
			Add(decimal.NewFromInt(completionTokens).Mul(cRate)).
			Add(decimal.NewFromInt(billCache).Mul(cachedR)).
			Add(decimal.NewFromInt(billWrite).Mul(cacheWriteR)).
			Div(oneMillion)

		var sb strings.Builder
		fmt.Fprintf(&sb, "%s -> (%d输入*%s + %d输出*%s", detailDesc, effPrompt, fmtRate(pRate), completionTokens, fmtRate(cRate))
		if billCache > 0 {
			fmt.Fprintf(&sb, " + %d缓存读*%s", billCache, fmtRate(cachedR))
		}
		if billWrite > 0 {
			tag := "缓存写"
			if is1hTTL {
				tag = "缓存写(1h)"
			}
			fmt.Fprintf(&sb, " + %d%s*%s", billWrite, tag, fmtRate(cacheWriteR))
		}
		sb.WriteString(")/1M")
		if isOpenAIFormat && cachedTokens > 0 && billCache == 0 {
			fmt.Fprintf(&sb, " [含%d缓存读·按输入价]", cachedTokens)
		}
		if isOpenAIFormat && cacheWriteTokens > 0 && billWrite == 0 {
			fmt.Fprintf(&sb, " [含%d缓存写·按输入价]", cacheWriteTokens)
		}
		detailStr = sb.String()
	}

	if isGLM53 {
		return cost.Mul(discount), fmt.Sprintf("%s * %s倍率", detailStr, fmtDiscount(discount))
	}
	return applyWebSearch(cost, detailStr)
}

func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func videoResolutionFromPixels(size string) string {
	norm := NormalizePixelSize(size)
	wStr, hStr, ok := strings.Cut(norm, "x")
	if !ok {
		return ""
	}
	w, e1 := strconv.ParseUint(wStr, 10, 64)
	h, e2 := strconv.ParseUint(hStr, 10, 64)
	if e1 != nil || e2 != nil || w == 0 || h == 0 {
		return ""
	}
	area := w * h
	switch {
	case area <= 620_000:
		return "480p"
	case area <= 1_400_000:
		return "720p"
	case area <= 2_800_000:
		return "1080p"
	case area <= 5_500_000:
		return "2k"
	default:
		return "4k"
	}
}

// Seedance2RequestResolution 规范化 Seedance 2.0 分辨率标签（按视频像素面积修正 1920x1080 等尺寸）。
func Seedance2RequestResolution(resolution, size string) string {
	pixelLabel := videoResolutionFromPixels(size)
	norm := NormalizeResolutionLabel(resolution)
	if norm == "" {
		return pixelLabel
	}
	if size != "" && pixelLabel != "" && parseImagePixelTier(size) == norm {
		return pixelLabel
	}
	return norm
}

func parseImagePixelTier(size string) string {
	norm := NormalizePixelSize(size)
	wStr, hStr, ok := strings.Cut(norm, "x")
	if !ok {
		return ""
	}
	w, e1 := strconv.ParseUint(wStr, 10, 64)
	h, e2 := strconv.ParseUint(hStr, 10, 64)
	if e1 != nil || e2 != nil || w == 0 || h == 0 {
		return ""
	}
	area := w * h
	switch {
	case area <= 1_600_000:
		return "1k"
	case area <= 5_500_000:
		return "2k"
	default:
		return "4k"
	}
}

func seedance2MatchTier(rates map[string]map[string]json.RawMessage, resolution, rateField string) (string, decimal.Decimal, bool) {
	target := resolution
	if target == "" {
		target = "720p"
	}
	for k, tier := range rates {
		if NormalizeResolutionLabel(k) == target {
			if raw, ok := tier[rateField]; ok {
				if d, ok := parseJSONDecimal(raw); ok {
					return k, d, true
				}
			}
		}
	}
	var bestKey string
	var bestVal decimal.Decimal
	found := false
	for k, tier := range rates {
		if raw, ok := tier[rateField]; ok {
			if d, ok := parseJSONDecimal(raw); ok {
				if !found || d.GreaterThan(bestVal) {
					bestKey, bestVal, found = k, d, true
				}
			}
		}
	}
	return bestKey, bestVal, found
}

// Seedance2RateTier 返回 Seedance 2.0 单价实际命中的规范化分辨率档位。
func Seedance2RateTier(rule *BillingRule, resolution, size string, hasVideo bool) string {
	if rule == nil || rule.BillingRule != "seedance2.0" {
		return ""
	}
	reqRes := Seedance2RequestResolution(resolution, size)
	rateField := "without_video"
	if hasVideo {
		rateField = "with_video"
	}
	ext := parseExtMap(rule.ExtendedConfig)
	if rawRates, ok := ext["resolution_rates"]; ok {
		var rates map[string]map[string]json.RawMessage
		if json.Unmarshal(rawRates, &rates) == nil {
			if k, _, ok := seedance2MatchTier(rates, reqRes, rateField); ok {
				return NormalizeResolutionLabel(k)
			}
		}
	}
	if reqRes != "" {
		return reqRes
	}
	return "720p"
}
