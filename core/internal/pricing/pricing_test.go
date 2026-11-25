package pricing

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }
func ptrI64(v int64) *int64        { return &v }
func ptrDec(s string) *decimal.Decimal {
	d := dec(s)
	return &d
}

func assertDecEq(t *testing.T, name string, got, want decimal.Decimal) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s: got %s, want %s", name, got.String(), want.String())
	}
}

func TestTokensStandardAndCacheIsolation(t *testing.T) {
	one := dec("1")
	rule := &BillingRule{
		BillingType:    BillingTypeTokens,
		BillingRule:    "standard",
		PromptRate:     dec("10"),
		CompletionRate: dec("20"),
		CachedRate:     dec("1"),
	}
	// OpenAI subset semantics: Total == Prompt + Completion => Cached subtracted from Prompt
	// (1M - 200k)*10/1M + 200k*1/1M + 500k*20/1M = 8 + 0.2 + 10 = 18.2
	openAIUsage := UsageTokens{
		Prompt:         1_000_000,
		Completion:     500_000,
		Total:          1_500_000,
		HasTotalTokens: true,
		Cached:         200_000,
		AudioTokens:    100_000, // standard 规则不应按音频单价额外计费
		ImageTokens:    50_000,
	}
	costOpenAI, detail := ComputeCost(rule, openAIUsage, one, ExtractedFeatures{})
	assertDecEq(t, "standard openai subset cost", costOpenAI, dec("18.2"))
	if detail == "" {
		t.Fatal("expected non-empty billing detail")
	}

	// Anthropic additive semantics (HasTotalTokens=false): Prompt not decremented by Cached
	// 1M*10/1M + 200k*1/1M + 500k*20/1M = 10 + 0.2 + 10 = 20.2
	additiveUsage := openAIUsage
	additiveUsage.HasTotalTokens = false
	costAdditive, _ := ComputeCost(rule, additiveUsage, one, ExtractedFeatures{})
	assertDecEq(t, "standard additive cost", costAdditive, dec("20.2"))
}

func TestTokensSeedance2And15Pro(t *testing.T) {
	one := dec("1")
	s2 := &BillingRule{
		BillingType: BillingTypeTokens,
		BillingRule: "seedance2.0",
		PromptRate:  dec("46"),
		ExtendedConfig: `{
			"resolution_rates": {
				"720p": {"with_video": "28", "without_video": "46"},
				"1080p": {"with_video": "35", "without_video": "55"}
			}
		}`,
	}
	cost1, _ := ComputeCost(s2, UsageTokens{Prompt: 1_000_000, Completion: 0}, one, ExtractedFeatures{
		Resolution: "720p",
		HasVideo:   true,
	})
	assertDecEq(t, "seedance2 720p with_video", cost1, dec("28"))

	cost2, _ := ComputeCost(s2, UsageTokens{Prompt: 500_000, Completion: 500_000}, one, ExtractedFeatures{
		Size:     "1920x1080",
		HasVideo: false,
	})
	assertDecEq(t, "seedance2 1080p without_video", cost2, dec("55"))

	s15 := &BillingRule{
		BillingType: BillingTypeTokens,
		BillingRule: "seedance1.5pro",
		ExtendedConfig: `{
			"base_rate": "8",
			"audio_rate": "16"
		}`,
	}
	cAudio, _ := ComputeCost(s15, UsageTokens{Completion: 1_000_000}, one, ExtractedFeatures{HasAudio: true})
	assertDecEq(t, "seedance1.5 audio", cAudio, dec("16"))
	cBase, _ := ComputeCost(s15, UsageTokens{Completion: 1_000_000}, one, ExtractedFeatures{HasAudio: false})
	assertDecEq(t, "seedance1.5 no audio", cBase, dec("8"))
}

func TestTokensClaudeAndKimiCache(t *testing.T) {
	one := dec("1")
	claudeRule := &BillingRule{
		BillingType:             BillingTypeTokens,
		BillingRule:             "claude",
		PromptRate:              dec("3"),
		CompletionRate:          dec("15"),
		ClaudeCacheCreationRate: dec("3.75"),
		ClaudeCacheReadRate:     dec("0.3"),
		ExtendedConfig:          `{"claude_cache_creation_1h_rate": "6"}`,
	}
	// prompt=100k, completion=20k, cc=50k (5m), read=200k
	// = 0.1*3 + 0.02*15 + 0.05*3.75 + 0.2*0.3 = 0.3 + 0.3 + 0.1875 + 0.06 = 0.8475
	cost5m, _ := ComputeCost(claudeRule, UsageTokens{
		Prompt:        100_000,
		Completion:    20_000,
		CacheCreation: 50_000,
		Cached:        200_000,
	}, one, ExtractedFeatures{})
	assertDecEq(t, "claude 5m cache creation", cost5m, dec("0.8475"))

	// With CacheCreation1h > 0 => 1h rate (6)
	// = 0.1*3 + 0.02*15 + 0.05*6 + 0.2*0.3 = 0.3 + 0.3 + 0.3 + 0.06 = 0.96
	cost1h, _ := ComputeCost(claudeRule, UsageTokens{
		Prompt:          100_000,
		Completion:      20_000,
		CacheCreation:   50_000,
		CacheCreation1h: 50_000,
		Cached:          200_000,
	}, one, ExtractedFeatures{})
	assertDecEq(t, "claude 1h cache creation", cost1h, dec("0.96"))

	kimiRule := &BillingRule{
		BillingType:    BillingTypeTokens,
		BillingRule:    "kimi_chat",
		PromptRate:     dec("4"),
		CompletionRate: dec("16"),
		CachedRate:     dec("0.4"),
		ExtendedConfig: `{"cache_write_rate": "5", "cache_write_1h_rate": "8"}`,
	}
	// OpenAI subset: Total=300k => effPrompt = 200k - 50k(write)? wait splitPromptSubsets subtracts read+write when OpenAI format.
	// With additive format (HasTotalTokens=false): 200k*4 + 100k*16 + 50k*8 + 500k*0.4 = 0.8 + 1.6 + 0.4 + 0.2 = 3.0
	kCost, _ := ComputeCost(kimiRule, UsageTokens{
		Prompt:     200_000,
		Completion: 100_000,
		CacheWrite: 50_000,
		Cached:     500_000,
	}, one, ExtractedFeatures{CacheTTL: "1h"})
	assertDecEq(t, "kimi_chat 1h cache write", kCost, dec("3"))
}

func TestTokensDoubaoChatAndMultimodalAndGPT(t *testing.T) {
	one := dec("1")
	doubao := &BillingRule{
		BillingType:    BillingTypeTokens,
		BillingRule:    "doubao_chat",
		PricingTiers:   `[{"max_prompt_tokens": 32, "max_completion_tokens": 200, "prompt_rate": "0.8", "completion_rate": "2", "cached_rate": "0.16"}]`,
		ExtendedConfig: `{"web_search_rate": "30"}`,
	}
	// 10k prompt (2k cached) + 5k completion + 2 web searches
	// tokens = (8k*0.8 + 2k*0.16 + 5k*2)/1M = 0.01672; ws = 2*30/1000 = 0.06 -> 0.07672
	dCost, _ := ComputeCost(doubao, UsageTokens{
		Prompt:     10_000,
		Cached:     2_000,
		Completion: 5_000,
		WebSearch:  2,
	}, one, ExtractedFeatures{})
	assertDecEq(t, "doubao_chat + web search", dCost, dec("0.07672"))

	mm := &BillingRule{
		BillingType:    BillingTypeTokens,
		BillingRule:    "multimodal",
		PromptRate:     dec("2"),
		CompletionRate: dec("8"),
		ExtendedConfig: `{"image_prompt_rate": "5", "image_completion_rate": "40"}`,
	}
	// textPrompt = 100k - 10k(imgIn) = 90k -> 0.18
	// imgIn = 10k * 5 = 0.05
	// textComp (incl reasoning) = 30k - 5k(imgOut) = 25k -> 0.20
	// imgOut = 5k * 40 = 0.20
	// total = 0.63
	mmCost, _ := ComputeCost(mm, UsageTokens{
		Prompt:            100_000,
		ImageTokens:       10_000,
		Completion:        30_000,
		OutputImageTokens: 5_000,
		ReasoningTokens:   5_000,
	}, one, ExtractedFeatures{})
	assertDecEq(t, "multimodal", mmCost, dec("0.63"))

	gpt := &BillingRule{
		BillingType: BillingTypeTokens,
		BillingRule: "gpt_billing",
		ExtendedConfig: `{"gpt_config":{
			"input_text":{"enabled":true,"rate":"5"},
			"input_image":{"enabled":true,"rate":"10"},
			"cached_input_text":{"enabled":true,"rate":"1.25"},
			"output_image":{"enabled":true,"rate":"40"}
		}}`,
	}
	// prompt=100k, image=20k, cached=10k(text), completion=50k
	// input_text=70k*5 + input_image=20k*10 + cached_text=10k*1.25 + output_image=50k*40
	// = (350 + 200 + 12.5 + 2000)/1000 = 2.5625
	gCost, _ := ComputeCost(gpt, UsageTokens{
		Prompt:      100_000,
		ImageTokens: 20_000,
		Cached:      10_000,
		Completion:  50_000,
	}, one, ExtractedFeatures{})
	assertDecEq(t, "gpt_billing", gCost, dec("2.5625"))
}

func TestRequestsAndDurationRules(t *testing.T) {
	one := dec("1")
	imgSize := &BillingRule{
		BillingType:  BillingTypeRequests,
		BillingRule:  "image_size_pixel",
		PricingTiers: `[{"size": "1024x1024", "quality_pricing": true, "rate_low": "0.1", "rate_medium": "0.2", "rate_high": "0.4", "image_ref_multiplier": "1.5"}]`,
	}
	// 2 images, 1024x1024 high with image ref => 0.4 * 1.5 * 2 = 1.2
	ic, _ := ComputeCost(imgSize, UsageTokens{}, one, ExtractedFeatures{
		Size:        "1024x1024",
		Quality:     "high",
		HasImageRef: true,
		ImageCount:  ptrI64(2),
	})
	assertDecEq(t, "image_size_pixel", ic, dec("1.2"))

	seedream := &BillingRule{
		BillingType:    BillingTypeRequests,
		BillingRule:    "volc_seedream_pro",
		PromptRate:     dec("0.05"),
		PricingTiers:   `[{"max_pixels_wan": 105, "rate": "0.2", "layer_rate": "0.3"}, {"max_pixels_wan": 420, "rate": "0.4", "layer_rate": "0.5"}]`,
		ExtendedConfig: `{"free_image_count": 1}`,
	}
	// 1024x1024 = 104.8576 wan <= 105, layer decomposition => output 0.3 + excess (3-1)*0.05 = 0.4
	sc, _ := ComputeCost(seedream, UsageTokens{}, one, ExtractedFeatures{
		Size:               "1024x1024",
		LayerDecomposition: true,
		ImageRefCount:      ptrI64(3),
	})
	assertDecEq(t, "volc_seedream_pro", sc, dec("0.4"))

	kling := &BillingRule{
		BillingType:  BillingTypeDuration,
		BillingRule:  "kling_video",
		DurationRate: dec("0.14"),
		ExtendedConfig: `{
			"price_table": {
				"std|off|no": "0.14",
				"pro|off|no": "0.28",
				"pro|on|no": "0.7"
			}
		}`,
	}
	kc, _ := ComputeCost(kling, UsageTokens{}, one, ExtractedFeatures{
		Resolution:      "1080p",
		Sound:           "on",
		DurationSeconds: ptrDec("10"),
	})
	assertDecEq(t, "kling_video 1080p sound on", kc, dec("7"))

	kling4 := &BillingRule{
		BillingType: BillingTypeDuration,
		BillingRule: "kling4_video",
		ExtendedConfig: `{
			"resolution_rates": {
				"720p": {"no_input": "0.5", "with_input": "0.3"}
			}
		}`,
	}
	// HasVideo=true, VideoRefSeconds=3 <= DurationSeconds=5 => tBill = 5*2 = 10s * 0.3 = 3.0
	k4c, _ := ComputeCost(kling4, UsageTokens{}, one, ExtractedFeatures{
		Resolution:      "720p",
		HasVideo:        true,
		DurationSeconds: ptrDec("5"),
		VideoRefSeconds: ptrDec("3"),
	})
	assertDecEq(t, "kling4_video with input video", k4c, dec("3"))

	vIO := &BillingRule{
		BillingType:    BillingTypeDuration,
		BillingRule:    "video_seconds_io",
		PromptRate:     dec("0.05"),
		PricingTiers:   `[{"resolution": "720p", "rate": "0.3", "input_rate": "0.2"}]`,
		ExtendedConfig: `{"free_image_count": 2}`,
	}
	// 5s out * 0.3 + 2s in * 0.2 + (4-2)*0.05 = 1.5 + 0.4 + 0.1 = 2.0
	vc, _ := ComputeCost(vIO, UsageTokens{}, one, ExtractedFeatures{
		Resolution:      "720p",
		DurationSeconds: ptrDec("5"),
		VideoRefSeconds: ptrDec("2"),
		ImageRefCount:   ptrI64(4),
	})
	assertDecEq(t, "video_seconds_io", vc, dec("2"))
}

func TestTimeMultiplierCrossMidnightAndInvert(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	rule := &BillingRule{
		BillingType:    BillingTypeTokens,
		BillingRule:    "standard",
		PromptRate:     dec("10"),
		CompletionRate: dec("10"),
		ExtendedConfig: `{
			"enable_time_multipliers": true,
			"invert_time_multipliers": false,
			"time_multipliers": [
				{"start": "23:00", "end": "02:00", "multiplier": "0.5", "days": [5]}
			]
		}`,
	}
	if err := ValidateTimeMultiplierConfig(rule.ExtendedConfig); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}

	friNight := time.Date(2026, 11, 20, 23, 30, 0, 0, loc)
	assertDecEq(t, "friday 23:30", ResolveTimeMultiplierAt(rule, friNight, loc), dec("0.5"))

	satEarly := time.Date(2026, 11, 21, 1, 30, 0, 0, loc)
	assertDecEq(t, "saturday 01:30", ResolveTimeMultiplierAt(rule, satEarly, loc), dec("0.5"))

	satBoundary := time.Date(2026, 11, 21, 2, 0, 0, 0, loc)
	assertDecEq(t, "saturday 02:00", ResolveTimeMultiplierAt(rule, satBoundary, loc), dec("1"))

	invRule := &BillingRule{
		BillingType:    BillingTypeTokens,
		BillingRule:    "standard",
		PromptRate:     dec("10"),
		CompletionRate: dec("10"),
		ExtendedConfig: `{
			"enable_time_multipliers": true,
			"invert_time_multipliers": true,
			"invert_remainder_multiplier": "2",
			"time_multipliers": [
				{"start": "09:00", "end": "18:00", "multiplier": "0.8"}
			]
		}`,
	}
	noon := time.Date(2026, 11, 20, 12, 0, 0, 0, loc)
	midnight := time.Date(2026, 11, 20, 0, 30, 0, 0, loc)
	assertDecEq(t, "invert in-window", ResolveTimeMultiplierAt(invRule, noon, loc), dec("0.8"))
	assertDecEq(t, "invert remainder", ResolveTimeMultiplierAt(invRule, midnight, loc), dec("2"))

	invRule.AppliedMultiplier = dec("2")
	cost, detail := ComputeCost(invRule, UsageTokens{Prompt: 1_000_000}, dec("1"), ExtractedFeatures{})
	assertDecEq(t, "cost with time multiplier", cost, dec("20"))
	if detail == "" {
		t.Fatal("expected detail")
	}
}

func TestResolveModelRuleSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	ddl := []string{
		`CREATE TABLE billing_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pid TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
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
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE model_rule_bindings (
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
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range ddl {
		if err := db.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}

	rule := BillingRule{
		PID:            "60001",
		Name:           "Seedance 2.0",
		BillingType:    BillingTypeTokens,
		BillingRule:    "seedance2.0",
		PricingType:    "tiered",
		PromptRate:     dec("46"),
		CompletionRate: dec("46"),
		ExtendedConfig: `{"resolution_rates":{"720p":{"with_video":"28","without_video":"46"}}}`,
		IsActive:       1,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	binding := ModelRuleBinding{
		Model:         "doubao-seedance-2-0",
		DisplayName:   "Seedance 2.0",
		BillingRuleID: rule.ID,
		IsListed:      1,
		IsActive:      1,
	}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}

	gotBind, gotRule, err := ResolveModelRule(db, "doubao-seedance-2-0")
	if err != nil {
		t.Fatalf("ResolveModelRule error: %v", err)
	}
	if gotBind == nil || gotRule == nil || gotRule.PID != "60001" {
		t.Fatalf("unexpected resolved rule: %+v / %+v", gotBind, gotRule)
	}
}

func TestRegistryExtensibilityAndErrorDistinction(t *testing.T) {
	RegisterCalculator(BillingTypeDuration, "custom_vendor_v1", func(rule *BillingRule, _ UsageTokens, discount decimal.Decimal, feat ExtractedFeatures) (decimal.Decimal, string) {
		sec := feat.TotalVideoSeconds()
		return sec.Mul(rule.DurationRate).Mul(discount), "custom_vendor_v1"
	})
	if !IsKnownBillingRule(BillingTypeDuration, "custom_vendor_v1") {
		t.Fatal("expected custom_vendor_v1 to be recognized")
	}
	if IsKnownBillingRule(BillingTypeDuration, "non_existent_rule_xyz") {
		t.Fatal("expected unknown rule to be rejected")
	}
	customRule := &BillingRule{
		BillingType:  BillingTypeDuration,
		BillingRule:  "custom_vendor_v1",
		DurationRate: dec("1.5"),
	}
	cost, detail := ComputeCost(customRule, UsageTokens{}, dec("2"), ExtractedFeatures{DurationSeconds: ptrDec("4")})
	assertDecEq(t, "custom registered calculator", cost, dec("12"))
	if detail != "custom_vendor_v1" {
		t.Fatalf("unexpected detail: %q", detail)
	}

	// Empty SQLite DB without model_rule_bindings table must return an error, NOT silently swallow it
	bareDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveModelRuleContext(context.Background(), bareDB, "any-model"); err == nil {
		t.Fatal("expected real DB error when table is missing")
	}
}
