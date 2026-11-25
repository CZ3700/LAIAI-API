package v1compat

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed preset_forward_rules.json
var presetForwardRulesJSON []byte

//go:embed preset_billing_rules.json
var presetBillingRulesJSON []byte

type rawPresetForwardRule struct {
	EID         string `json:"eid"`
	Name        string `json:"name"`
	RuleType    string `json:"rule_type"`
	Category    string `json:"category"`
	Description string `json:"description"`
	ConfigJSON  string `json:"config_json"`
}

type rawPresetBillingRule struct {
	PID                     string          `json:"pid"`
	Name                    string          `json:"name"`
	BillingType             string          `json:"billing_type"`
	PromptRate              decimal.Decimal `json:"prompt_rate"`
	CompletionRate          decimal.Decimal `json:"completion_rate"`
	CachedRate              decimal.Decimal `json:"cached_rate"`
	ClaudeCacheCreationRate decimal.Decimal `json:"claude_cache_creation_rate"`
	ClaudeCacheReadRate     decimal.Decimal `json:"claude_cache_read_rate"`
	FixedRate               decimal.Decimal `json:"fixed_rate"`
	DurationRate            decimal.Decimal `json:"duration_rate"`
	BillingRule             string          `json:"billing_rule"`
	PricingType             string          `json:"pricing_type"`
	PricingTiers            string          `json:"pricing_tiers"`
	ExtendedConfig          string          `json:"extended_config"`
}

var (
	presetsOnce        sync.Once
	parsedForwardRules []forward.ForwardRule
	parsedBillingRules []pricing.BillingRule
	presetsParseErr    error
)

func loadParsedPresets() ([]forward.ForwardRule, []pricing.BillingRule, error) {
	presetsOnce.Do(func() {
		var rawFwd []rawPresetForwardRule
		if err := json.Unmarshal(presetForwardRulesJSON, &rawFwd); err != nil {
			presetsParseErr = fmt.Errorf("parse preset forward rules: %w", err)
			return
		}
		parsedForwardRules = make([]forward.ForwardRule, 0, len(rawFwd))
		for _, r := range rawFwd {
			parsedForwardRules = append(parsedForwardRules, forward.ForwardRule{
				EID:         strings.TrimSpace(r.EID),
				Name:        strings.TrimSpace(r.Name),
				RuleType:    cmp.Or(strings.TrimSpace(r.RuleType), "openai"),
				Category:    cmp.Or(strings.TrimSpace(r.Category), "聊天"),
				Description: truncateRunes(strings.TrimSpace(r.Description), 500),
				ConfigJSON:  cmp.Or(strings.TrimSpace(r.ConfigJSON), "{}"),
				IsActive:    1,
				IsSystem:    1,
			})
		}

		var rawBill []rawPresetBillingRule
		if err := json.Unmarshal(presetBillingRulesJSON, &rawBill); err != nil {
			presetsParseErr = fmt.Errorf("parse preset billing rules: %w", err)
			return
		}
		parsedBillingRules = make([]pricing.BillingRule, 0, len(rawBill))
		for _, r := range rawBill {
			parsedBillingRules = append(parsedBillingRules, pricing.BillingRule{
				PID:                     strings.TrimSpace(r.PID),
				Name:                    strings.TrimSpace(r.Name),
				BillingType:             cmp.Or(strings.TrimSpace(r.BillingType), pricing.BillingTypeTokens),
				BillingRule:             cmp.Or(strings.TrimSpace(r.BillingRule), "standard"),
				PricingType:             cmp.Or(strings.TrimSpace(r.PricingType), "custom"),
				PromptRate:              r.PromptRate,
				CompletionRate:          r.CompletionRate,
				CachedRate:              r.CachedRate,
				ClaudeCacheCreationRate: r.ClaudeCacheCreationRate,
				ClaudeCacheReadRate:     r.ClaudeCacheReadRate,
				FixedRate:               r.FixedRate,
				DurationRate:            r.DurationRate,
				PricingTiers:            cmp.Or(strings.TrimSpace(r.PricingTiers), "[]"),
				ExtendedConfig:          cmp.Or(strings.TrimSpace(r.ExtendedConfig), "{}"),
				IsActive:                1,
				IsSystem:                1,
			})
		}
	})
	return parsedForwardRules, parsedBillingRules, presetsParseErr
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// PresetBillingRuleByPIDOrName 按 PID 或规则名查找内置计价规则出厂默认配置。
func PresetBillingRuleByPIDOrName(pid, name string) (pricing.BillingRule, bool) {
	_, billRules, err := loadParsedPresets()
	if err != nil {
		return pricing.BillingRule{}, false
	}
	cleanPID := strings.TrimSpace(pid)
	cleanName := strings.TrimSpace(name)
	for _, r := range billRules {
		if cleanPID != "" && r.PID == cleanPID {
			return r, true
		}
		if cleanName != "" && r.Name == cleanName {
			return r, true
		}
	}
	return pricing.BillingRule{}, false
}

var pluginGatedForwardRuleNames = []string{
	"火山方舟 级联视频生成",
	"火山方舟 视频素材转换",
	"火山方舟 视频素材转换(国际版)",
	"火山方舟 视频素材免审核转换(国际版)",
	"ComfyUI",
}

// ShouldHidePluginGatedForwardRule 对齐 tkeapi forward_rules.rs：隐藏未开启封装/素材插件的规则。
func ShouldHidePluginGatedForwardRule(r forward.ForwardRule) bool {
	if r.Category == "封装" || strings.Contains(r.Name, "MediaKit") {
		return true
	}
	if slices.Contains(pluginGatedForwardRuleNames, r.Name) {
		return true
	}
	var cfg struct {
		TargetType     string `json:"target_type"`
		IsCascade      any    `json:"is_cascade"`
		AssetConvertNS string `json:"asset_convert_ns"`
	}
	if err := json.Unmarshal([]byte(r.ConfigJSON), &cfg); err == nil {
		if cfg.TargetType == "volcengine_media_enhance" || cfg.AssetConvertNS != "" {
			return true
		}
		switch v := cfg.IsCascade.(type) {
		case bool:
			if v {
				return true
			}
		case float64:
			if v == 1 {
				return true
			}
		case string:
			if v == "1" || strings.EqualFold(v, "true") {
				return true
			}
		}
	}
	return false
}

// EnsurePresetRules 幂等补齐 tkeapi 全量预置转发规则与计费规则（不覆盖用户已修改项）。
func EnsurePresetRules(ctx context.Context, db *gorm.DB) error {
	fwdPresets, billPresets, err := loadParsedPresets()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureForwardRulePresetsTx(tx, fwdPresets); err != nil {
			return err
		}
		return ensureBillingRulePresetsTx(tx, billPresets)
	})
}

func ensureForwardRulePresetsTx(tx *gorm.DB, presets []forward.ForwardRule) error {
	var existing []forward.ForwardRule
	if err := tx.Select("name, eid").Find(&existing).Error; err != nil {
		if IsMissingTable(err) {
			return nil
		}
		return fmt.Errorf("load forward rules: %w", err)
	}
	existingNames := make(map[string]struct{}, len(existing))
	usedEIDs := make(map[string]struct{}, len(existing)+len(presets))
	for _, r := range existing {
		existingNames[r.Name] = struct{}{}
		if r.EID != "" {
			usedEIDs[r.EID] = struct{}{}
		}
	}
	nextFallback := 19000
	toInsert := make([]forward.ForwardRule, 0)
	for _, item := range presets {
		if _, exists := existingNames[item.Name]; exists {
			continue
		}
		eid := item.EID
		if eid == "" {
			eid = allocateUniqueCode(usedEIDs, &nextFallback)
		} else if _, taken := usedEIDs[eid]; taken {
			eid = allocateUniqueCode(usedEIDs, &nextFallback)
		}
		usedEIDs[eid] = struct{}{}
		existingNames[item.Name] = struct{}{}
		row := item
		row.EID = eid
		toInsert = append(toInsert, row)
	}
	if len(toInsert) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&toInsert).Error
}

func ensureBillingRulePresetsTx(tx *gorm.DB, presets []pricing.BillingRule) error {
	var existing []pricing.BillingRule
	if err := tx.Select("name, pid").Find(&existing).Error; err != nil {
		if IsMissingTable(err) {
			return nil
		}
		return fmt.Errorf("load billing rules: %w", err)
	}
	existingNames := make(map[string]struct{}, len(existing))
	usedPIDs := make(map[string]struct{}, len(existing)+len(presets))
	for _, r := range existing {
		existingNames[r.Name] = struct{}{}
		if r.PID != "" {
			usedPIDs[r.PID] = struct{}{}
		}
	}
	nextFallback := 79000
	toInsert := make([]pricing.BillingRule, 0)
	for _, item := range presets {
		if _, exists := existingNames[item.Name]; exists {
			continue
		}
		pid := item.PID
		if pid == "" {
			pid = allocateUniqueCode(usedPIDs, &nextFallback)
		} else if _, taken := usedPIDs[pid]; taken {
			pid = allocateUniqueCode(usedPIDs, &nextFallback)
		}
		usedPIDs[pid] = struct{}{}
		existingNames[item.Name] = struct{}{}
		row := item
		row.PID = pid
		toInsert = append(toInsert, row)
	}
	if len(toInsert) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&toInsert).Error
}

func allocateUniqueCode(used map[string]struct{}, cursor *int) string {
	for {
		*cursor++
		code := fmt.Sprintf("%d", *cursor)
		if _, exists := used[code]; !exists {
			return code
		}
	}
}
