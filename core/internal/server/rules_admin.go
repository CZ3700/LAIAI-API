package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func parseUintIDParam(c *gin.Context, name string) (uint64, bool) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param(name)), 10, 64)
	if err != nil || id == 0 {
		fail(c, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return id, true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func generateUniqueCode5(ctx context.Context, db *gorm.DB, table, col, prefix string) (string, error) {
	for range 25 {
		n, err := rand.Int(rand.Reader, big.NewInt(10000))
		if err != nil {
			return "", fmt.Errorf("read crypto/rand: %w", err)
		}
		code := fmt.Sprintf("%s%04d", prefix, n.Int64())
		var cnt int64
		if err := db.WithContext(ctx).Table(table).Where(col+" = ?", code).Count(&cnt).Error; err != nil {
			return "", fmt.Errorf("check unique %s.%s: %w", table, col, err)
		}
		if cnt == 0 {
			return code, nil
		}
	}
	return "", errors.New("failed to allocate unique 5-digit code")
}

func deduplicateUint64s(in []uint64) []uint64 {
	if len(in) <= 1 {
		return slices.Clone(in)
	}
	seen := make(map[uint64]struct{}, len(in))
	out := make([]uint64, 0, len(in))
	for _, id := range in {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	s := strings.TrimSpace(string(raw))
	switch strings.ToLower(s) {
	case "true", "1", `"true"`, `"1"`:
		*b = true
		return nil
	case "false", "0", `"false"`, `"0"`:
		*b = false
		return nil
	default:
		return fmt.Errorf("invalid bool value %s", s)
	}
}

type flexJSONString string

func (j *flexJSONString) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*j = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*j = flexJSONString(s)
		return nil
	}
	if !json.Valid(trimmed) {
		return errors.New("invalid JSON payload")
	}
	*j = flexJSONString(trimmed)
	return nil
}

// ── 渠道分类 / 分组（channel_categories） ────────────────────────

type channelCategoryReq struct {
	Name        string    `json:"name" binding:"required"`
	Description string    `json:"description"`
	SortOrder   int       `json:"sort_order"`
	IsActive    *flexBool `json:"is_active"`
}

func (s *Server) adminListChannelCategories(c *gin.Context) {
	rows := []forward.ChannelCategory{}
	if err := s.readOnly().WithContext(c.Request.Context()).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list channel categories failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"categories": rows})
}

func (s *Server) adminCreateChannelCategory(c *gin.Context) {
	var req channelCategoryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name required")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "name required")
		return
	}
	active := int8(1)
	if req.IsActive != nil {
		active = b2i8(bool(*req.IsActive))
	}
	cat := &forward.ChannelCategory{
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		SortOrder:   req.SortOrder,
		IsActive:    active,
	}
	if err := s.db.WithContext(c.Request.Context()).Create(cat).Error; err != nil {
		fail(c, http.StatusConflict, "channel category already exists or invalid")
		return
	}
	s.writeAudit(c, "channel_category_create", strconv.FormatUint(cat.ID, 10), "created category "+cat.Name)
	c.JSON(http.StatusCreated, gin.H{"category": cat})
}

func (s *Server) adminUpdateChannelCategory(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		SortOrder   *int      `json:"sort_order"`
		IsActive    *flexBool `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			fail(c, http.StatusBadRequest, "name cannot be empty")
			return
		}
		updates["name"] = name
	}
	if req.Description != nil {
		updates["description"] = strings.TrimSpace(*req.Description)
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if req.IsActive != nil {
		updates["is_active"] = b2i8(bool(*req.IsActive))
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "nothing to update")
		return
	}
	res := s.db.WithContext(c.Request.Context()).Model(&forward.ChannelCategory{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		fail(c, http.StatusConflict, "update channel category failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "channel category not found")
		return
	}
	s.writeAudit(c, "channel_category_update", strconv.FormatUint(id, 10), "updated category")
	c.JSON(http.StatusOK, gin.H{"updated": id})
}

func (s *Server) adminDeleteChannelCategory(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	db := s.db.WithContext(c.Request.Context())
	var used int64
	if err := db.Table("channels").Where("category_id = ?", id).Count(&used).Error; err != nil {
		fail(c, http.StatusInternalServerError, "check channel category usage failed")
		return
	}
	if used > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":           "channel category is referenced by existing channels",
			"channels_in_use": used,
		})
		return
	}
	res := db.Where("id = ?", id).Delete(&forward.ChannelCategory{})
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "delete channel category failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "channel category not found")
		return
	}
	s.writeAudit(c, "channel_category_delete", strconv.FormatUint(id, 10), "deleted category")
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// ── 多模态计费规则（billing_rules） ────────────────────────

type billingRuleReq struct {
	PID                     string           `json:"pid"`
	Name                    string           `json:"name"`
	BillingType             string           `json:"billing_type"`
	BillingRule             string           `json:"billing_rule"`
	PricingType             string           `json:"pricing_type"`
	PromptRate              *decimal.Decimal `json:"prompt_rate"`
	CompletionRate          *decimal.Decimal `json:"completion_rate"`
	CachedRate              *decimal.Decimal `json:"cached_rate"`
	ClaudeCacheCreationRate *decimal.Decimal `json:"claude_cache_creation_rate"`
	ClaudeCacheReadRate     *decimal.Decimal `json:"claude_cache_read_rate"`
	FixedRate               *decimal.Decimal `json:"fixed_rate"`
	DurationRate            *decimal.Decimal `json:"duration_rate"`
	PricingTiers            *flexJSONString  `json:"pricing_tiers"`
	ExtendedConfig          *flexJSONString  `json:"extended_config"`
	IsActive                *flexBool        `json:"is_active"`
	SortOrder               *int             `json:"sort_order"`
}

func validateBillingRuleFields(billingType, billingRule, pricingTiers, extConfig string, rates ...*decimal.Decimal) error {
	switch billingType {
	case pricing.BillingTypeTokens, pricing.BillingTypeRequests, pricing.BillingTypeDuration:
	default:
		return errors.New("billing_type must be tokens|requests|duration")
	}
	if !pricing.IsKnownBillingRule(billingType, billingRule) {
		return fmt.Errorf("unknown billing_rule %q for billing_type %q", billingRule, billingType)
	}
	for _, r := range rates {
		if r != nil && r.IsNegative() {
			return errors.New("rates cannot be negative")
		}
	}
	if strings.TrimSpace(pricingTiers) != "" && !json.Valid([]byte(pricingTiers)) {
		return errors.New("pricing_tiers must be valid JSON")
	}
	return pricing.ValidateTimeMultiplierConfig(extConfig)
}

func (s *Server) listedModelsUsingBillingRule(ctx context.Context, ruleID uint64) ([]string, error) {
	models := []string{}
	err := s.db.WithContext(ctx).
		Table("model_rule_bindings").
		Where("billing_rule_id = ? AND is_listed = 1", ruleID).
		Order("model ASC").
		Pluck("model", &models).Error
	return models, err
}

func (s *Server) adminListBillingRules(c *gin.Context) {
	rows := []pricing.BillingRule{}
	if err := s.readOnly().WithContext(c.Request.Context()).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list billing rules failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rows})
}

func (s *Server) adminCreateBillingRule(c *gin.Context) {
	ctx := c.Request.Context()
	var req billingRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "name required")
		return
	}
	bType := cmp.Or(strings.TrimSpace(req.BillingType), pricing.BillingTypeTokens)
	bRule := cmp.Or(strings.TrimSpace(req.BillingRule), "standard")
	pType := cmp.Or(strings.TrimSpace(req.PricingType), "standard")

	tiers := "[]"
	if req.PricingTiers != nil && strings.TrimSpace(string(*req.PricingTiers)) != "" {
		tiers = strings.TrimSpace(string(*req.PricingTiers))
	}
	extCfg := "{}"
	if req.ExtendedConfig != nil && strings.TrimSpace(string(*req.ExtendedConfig)) != "" {
		extCfg = strings.TrimSpace(string(*req.ExtendedConfig))
	}
	if err := validateBillingRuleFields(
		bType, bRule, tiers, extCfg,
		req.PromptRate, req.CompletionRate, req.CachedRate,
		req.ClaudeCacheCreationRate, req.ClaudeCacheReadRate, req.FixedRate, req.DurationRate,
	); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	pid := strings.TrimSpace(req.PID)
	if pid == "" {
		var err error
		pid, err = generateUniqueCode5(ctx, s.db, "billing_rules", "pid", "6")
		if err != nil {
			fail(c, http.StatusInternalServerError, "generate pid failed")
			return
		}
	} else if len(pid) != 5 || !isAllDigits(pid) || !strings.HasPrefix(pid, "6") {
		fail(c, http.StatusBadRequest, "pid must be 5 digits starting with 6")
		return
	}

	dVal := func(p *decimal.Decimal) decimal.Decimal {
		if p == nil {
			return decimal.Zero
		}
		return *p
	}
	active := int8(1)
	if req.IsActive != nil {
		active = b2i8(bool(*req.IsActive))
	}
	sortOrder := 0
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}

	rule := &pricing.BillingRule{
		PID:                     pid,
		Name:                    name,
		BillingType:             bType,
		BillingRule:             bRule,
		PricingType:             pType,
		PromptRate:              dVal(req.PromptRate),
		CompletionRate:          dVal(req.CompletionRate),
		CachedRate:              dVal(req.CachedRate),
		ClaudeCacheCreationRate: dVal(req.ClaudeCacheCreationRate),
		ClaudeCacheReadRate:     dVal(req.ClaudeCacheReadRate),
		FixedRate:               dVal(req.FixedRate),
		DurationRate:            dVal(req.DurationRate),
		PricingTiers:            tiers,
		ExtendedConfig:          extCfg,
		IsActive:                active,
		SortOrder:               sortOrder,
	}
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		fail(c, http.StatusConflict, "billing rule pid or name already exists")
		return
	}
	s.writeAudit(c, "billing_rule_create", strconv.FormatUint(rule.ID, 10), "created billing rule "+rule.Name+" pid="+rule.PID)
	s.invalidateCache(relay.KindBillingRule, "")
	c.JSON(http.StatusCreated, gin.H{"id": rule.ID, "pid": rule.PID, "rule": rule})
}

func (s *Server) adminUpdateBillingRule(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	db := s.db.WithContext(ctx)
	var cur pricing.BillingRule
	if err := db.First(&cur, id).Error; err != nil {
		fail(c, http.StatusNotFound, "billing rule not found")
		return
	}
	var req billingRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}

	effType := cmp.Or(strings.TrimSpace(req.BillingType), cur.BillingType)
	effRule := cmp.Or(strings.TrimSpace(req.BillingRule), cur.BillingRule)
	effTiers := cur.PricingTiers
	if req.PricingTiers != nil {
		effTiers = cmp.Or(strings.TrimSpace(string(*req.PricingTiers)), "[]")
	}
	effExt := cur.ExtendedConfig
	if req.ExtendedConfig != nil {
		effExt = cmp.Or(strings.TrimSpace(string(*req.ExtendedConfig)), "{}")
	}
	if err := validateBillingRuleFields(
		effType, effRule, effTiers, effExt,
		req.PromptRate, req.CompletionRate, req.CachedRate,
		req.ClaudeCacheCreationRate, req.ClaudeCacheReadRate, req.FixedRate, req.DurationRate,
	); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	if req.IsActive != nil && !bool(*req.IsActive) && cur.IsActive == 1 {
		models, err := s.listedModelsUsingBillingRule(ctx, id)
		if err != nil {
			fail(c, http.StatusInternalServerError, "check billing rule references failed")
			return
		}
		if len(models) > 0 {
			c.JSON(http.StatusConflict, gin.H{
				"error":                    "billing rule is still used by listed models",
				"listed_models_using_rule": models,
			})
			return
		}
	}

	updates := map[string]any{}
	if strings.TrimSpace(req.Name) != "" {
		updates["name"] = strings.TrimSpace(req.Name)
	}
	if strings.TrimSpace(req.BillingType) != "" {
		updates["billing_type"] = effType
	}
	if strings.TrimSpace(req.BillingRule) != "" {
		updates["billing_rule"] = effRule
	}
	if strings.TrimSpace(req.PricingType) != "" {
		updates["pricing_type"] = strings.TrimSpace(req.PricingType)
	}
	if req.PromptRate != nil {
		updates["prompt_rate"] = *req.PromptRate
	}
	if req.CompletionRate != nil {
		updates["completion_rate"] = *req.CompletionRate
	}
	if req.CachedRate != nil {
		updates["cached_rate"] = *req.CachedRate
	}
	if req.ClaudeCacheCreationRate != nil {
		updates["claude_cache_creation_rate"] = *req.ClaudeCacheCreationRate
	}
	if req.ClaudeCacheReadRate != nil {
		updates["claude_cache_read_rate"] = *req.ClaudeCacheReadRate
	}
	if req.FixedRate != nil {
		updates["fixed_rate"] = *req.FixedRate
	}
	if req.DurationRate != nil {
		updates["duration_rate"] = *req.DurationRate
	}
	if req.PricingTiers != nil {
		updates["pricing_tiers"] = effTiers
	}
	if req.ExtendedConfig != nil {
		updates["extended_config"] = effExt
	}
	if req.IsActive != nil {
		updates["is_active"] = b2i8(bool(*req.IsActive))
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "nothing to update")
		return
	}

	if err := db.Model(&pricing.BillingRule{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		fail(c, http.StatusConflict, "update billing rule failed")
		return
	}
	s.writeAudit(c, "billing_rule_update", strconv.FormatUint(id, 10), "updated billing rule "+cur.PID)
	s.invalidateCache(relay.KindBillingRule, "")
	var updated pricing.BillingRule
	if err := db.First(&updated, id).Error; err == nil {
		c.JSON(http.StatusOK, gin.H{"updated": id, "id": updated.ID, "pid": updated.PID, "rule": updated})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": id, "id": id})
}

func (s *Server) adminDeleteBillingRule(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	db := s.db.WithContext(ctx)
	var cur pricing.BillingRule
	if err := db.First(&cur, id).Error; err != nil {
		fail(c, http.StatusNotFound, "billing rule not found")
		return
	}
	if cur.IsSystem == 1 {
		fail(c, http.StatusConflict, "system billing rule cannot be deleted")
		return
	}
	models, err := s.listedModelsUsingBillingRule(ctx, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "check billing rule references failed")
		return
	}
	if len(models) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":                    "billing rule is still used by listed models",
			"listed_models_using_rule": models,
		})
		return
	}
	if err := db.Where("id = ?", id).Delete(&pricing.BillingRule{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "delete billing rule failed")
		return
	}
	s.writeAudit(c, "billing_rule_delete", strconv.FormatUint(id, 10), "deleted billing rule "+cur.PID)
	s.invalidateCache(relay.KindBillingRule, "")
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// ── 数据驱动转发规则（forward_rules） ────────────────────────

type forwardRuleReq struct {
	EID         string          `json:"eid"`
	Name        string          `json:"name"`
	RuleType    string          `json:"rule_type"`
	Category    string          `json:"category"`
	ConfigJSON  *flexJSONString `json:"config_json"`
	Description *string         `json:"description"`
	Remark      *string         `json:"remark"`
	IsActive    *flexBool       `json:"is_active"`
	SortOrder   *int            `json:"sort_order"`
}

func (s *Server) adminListForwardRules(c *gin.Context) {
	rows := []forward.ForwardRule{}
	if err := s.readOnly().WithContext(c.Request.Context()).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list forward rules failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rows})
}

func (s *Server) adminCreateForwardRule(c *gin.Context) {
	ctx := c.Request.Context()
	var req forwardRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "name required")
		return
	}
	cfg := "{}"
	if req.ConfigJSON != nil && strings.TrimSpace(string(*req.ConfigJSON)) != "" {
		cfg = strings.TrimSpace(string(*req.ConfigJSON))
	}
	if err := forward.ValidateConfigJSON(cfg); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	eid := strings.TrimSpace(req.EID)
	if eid == "" {
		var err error
		eid, err = generateUniqueCode5(ctx, s.db, "forward_rules", "eid", "1")
		if err != nil {
			fail(c, http.StatusInternalServerError, "generate eid failed")
			return
		}
	} else if len(eid) != 5 || !isAllDigits(eid) {
		fail(c, http.StatusBadRequest, "eid must be 5 digits")
		return
	}

	active := int8(1)
	if req.IsActive != nil {
		active = b2i8(bool(*req.IsActive))
	}
	sortOrder := 0
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}
	desc := ""
	if req.Description != nil {
		desc = strings.TrimSpace(*req.Description)
	}
	remark := ""
	if req.Remark != nil {
		remark = strings.TrimSpace(*req.Remark)
	}

	rule := &forward.ForwardRule{
		EID:         eid,
		Name:        name,
		RuleType:    cmp.Or(strings.TrimSpace(req.RuleType), "openai"),
		Category:    cmp.Or(strings.TrimSpace(req.Category), "聊天"),
		ConfigJSON:  cfg,
		Description: desc,
		Remark:      remark,
		IsActive:    active,
		SortOrder:   sortOrder,
	}
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		fail(c, http.StatusConflict, "forward rule eid or name already exists")
		return
	}
	s.writeAudit(c, "forward_rule_create", strconv.FormatUint(rule.ID, 10), "created forward rule "+rule.Name+" eid="+rule.EID)
	s.invalidateCache(relay.KindForwardRule, "")
	c.JSON(http.StatusCreated, gin.H{"id": rule.ID, "eid": rule.EID, "rule": rule})
}

func (s *Server) adminUpdateForwardRule(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	db := s.db.WithContext(ctx)
	var cur forward.ForwardRule
	if err := db.First(&cur, id).Error; err != nil {
		fail(c, http.StatusNotFound, "forward rule not found")
		return
	}
	var req forwardRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]any{}
	if strings.TrimSpace(req.Name) != "" {
		updates["name"] = strings.TrimSpace(req.Name)
	}
	if strings.TrimSpace(req.RuleType) != "" {
		updates["rule_type"] = strings.TrimSpace(req.RuleType)
	}
	if strings.TrimSpace(req.Category) != "" {
		updates["category"] = strings.TrimSpace(req.Category)
	}
	if req.ConfigJSON != nil {
		cfg := cmp.Or(strings.TrimSpace(string(*req.ConfigJSON)), "{}")
		if err := forward.ValidateConfigJSON(cfg); err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		updates["config_json"] = cfg
	}
	if req.Description != nil {
		updates["description"] = strings.TrimSpace(*req.Description)
	}
	if req.Remark != nil {
		updates["remark"] = strings.TrimSpace(*req.Remark)
	}
	if req.IsActive != nil {
		updates["is_active"] = b2i8(bool(*req.IsActive))
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if len(updates) == 0 {
		fail(c, http.StatusBadRequest, "nothing to update")
		return
	}
	if err := db.Model(&forward.ForwardRule{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		fail(c, http.StatusConflict, "update forward rule failed")
		return
	}
	s.writeAudit(c, "forward_rule_update", strconv.FormatUint(id, 10), "updated forward rule "+cur.EID)
	s.invalidateCache(relay.KindForwardRule, "")
	c.JSON(http.StatusOK, gin.H{"updated": id, "id": id, "eid": cur.EID})
}

func (s *Server) adminDeleteForwardRule(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	db := s.db.WithContext(ctx)
	var cur forward.ForwardRule
	if err := db.First(&cur, id).Error; err != nil {
		fail(c, http.StatusNotFound, "forward rule not found")
		return
	}
	if cur.IsSystem == 1 {
		fail(c, http.StatusConflict, "system forward rule cannot be deleted")
		return
	}
	if err := db.Where("id = ?", id).Delete(&forward.ForwardRule{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "delete forward rule failed")
		return
	}
	s.writeAudit(c, "forward_rule_delete", strconv.FormatUint(id, 10), "deleted forward rule "+cur.EID)
	s.invalidateCache(relay.KindForwardRule, "")
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// ── 模型⇄规则绑定（model_rule_bindings） ────────────────────────

type modelRuleBindingReq struct {
	Model                 string           `json:"model"`
	DisplayName           string           `json:"display_name"`
	ModelAlias            string           `json:"model_alias"`
	BillingRuleID         uint64           `json:"billing_rule_id"`
	ForwardRuleIDs        []uint64         `json:"forward_rule_ids"`
	PreDeduction          *decimal.Decimal `json:"pre_deduction"`
	EnableLogContent      *bool            `json:"enable_log_content"`
	SiteDiscount          *decimal.Decimal `json:"site_discount"`
	SiteDiscountEnabled   *bool            `json:"site_discount_enabled"`
	GlobalDiscount        *decimal.Decimal `json:"global_discount"`
	GlobalDiscountEnabled *bool            `json:"global_discount_enabled"`
	DiscountSchedule      string           `json:"discount_schedule"`
	IsListed              *bool            `json:"is_listed"`
	IsActive              *bool            `json:"is_active"`
	SortOrder             int              `json:"sort_order"`
}

func (s *Server) adminListModelRuleBindings(c *gin.Context) {
	rows := []pricing.ModelRuleBinding{}
	if err := s.readOnly().WithContext(c.Request.Context()).Order("sort_order ASC, model ASC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list model rule bindings failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"bindings": rows})
}

func (s *Server) adminUpsertModelRuleBinding(c *gin.Context) {
	ctx := c.Request.Context()
	db := s.db.WithContext(ctx)
	var req modelRuleBindingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	model := cmp.Or(strings.TrimSpace(c.Param("model")), strings.TrimSpace(req.Model))
	if model == "" {
		fail(c, http.StatusBadRequest, "model required")
		return
	}
	if req.BillingRuleID > 0 {
		var cnt int64
		if err := db.Table("billing_rules").Where("id = ?", req.BillingRuleID).Count(&cnt).Error; err != nil || cnt == 0 {
			fail(c, http.StatusBadRequest, "referenced billing_rule_id not found")
			return
		}
	}
	uniqueFwdIDs := deduplicateUint64s(req.ForwardRuleIDs)
	if len(uniqueFwdIDs) > 0 {
		var cnt int64
		if err := db.Table("forward_rules").Where("id IN ?", uniqueFwdIDs).Count(&cnt).Error; err != nil || cnt != int64(len(uniqueFwdIDs)) {
			fail(c, http.StatusBadRequest, "referenced forward_rule_ids contain unknown id")
			return
		}
	}
	if req.PreDeduction != nil && req.PreDeduction.IsNegative() {
		fail(c, http.StatusBadRequest, "pre_deduction cannot be negative")
		return
	}
	siteDisc := decimal.NewFromInt(1)
	if req.SiteDiscount != nil {
		if !req.SiteDiscount.IsPositive() {
			fail(c, http.StatusBadRequest, "site_discount must be positive")
			return
		}
		siteDisc = *req.SiteDiscount
	}
	globalDisc := decimal.NewFromInt(1)
	if req.GlobalDiscount != nil {
		if !req.GlobalDiscount.IsPositive() {
			fail(c, http.StatusBadRequest, "global_discount must be positive")
			return
		}
		globalDisc = *req.GlobalDiscount
	}

	preDed := decimal.Zero
	if req.PreDeduction != nil {
		preDed = *req.PreDeduction
	}
	fwdJSON := "[]"
	if len(uniqueFwdIDs) > 0 {
		b, _ := json.Marshal(uniqueFwdIDs)
		fwdJSON = string(b)
	}
	isListed := int8(1)
	if req.IsListed != nil {
		isListed = b2i8(*req.IsListed)
	}
	isActive := int8(1)
	if req.IsActive != nil {
		isActive = b2i8(*req.IsActive)
	}
	enableLog := int8(0)
	if req.EnableLogContent != nil {
		enableLog = b2i8(*req.EnableLogContent)
	}
	siteEnabled := int8(0)
	if req.SiteDiscountEnabled != nil {
		siteEnabled = b2i8(*req.SiteDiscountEnabled)
	}
	globalEnabled := int8(0)
	if req.GlobalDiscountEnabled != nil {
		globalEnabled = b2i8(*req.GlobalDiscountEnabled)
	}

	row := pricing.ModelRuleBinding{
		Model:                 model,
		DisplayName:           strings.TrimSpace(req.DisplayName),
		ModelAlias:            strings.TrimSpace(req.ModelAlias),
		BillingRuleID:         req.BillingRuleID,
		ForwardRuleIDs:        fwdJSON,
		PreDeduction:          preDed,
		EnableLogContent:      enableLog,
		SiteDiscount:          siteDisc,
		SiteDiscountEnabled:   siteEnabled,
		GlobalDiscount:        globalDisc,
		GlobalDiscountEnabled: globalEnabled,
		DiscountSchedule:      strings.TrimSpace(req.DiscountSchedule),
		IsListed:              isListed,
		IsActive:              isActive,
		SortOrder:             req.SortOrder,
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "model"}},
		UpdateAll: true,
	}).Create(&row).Error; err != nil {
		fail(c, http.StatusInternalServerError, "upsert model rule binding failed")
		return
	}
	s.writeAudit(c, "model_rule_binding_upsert", model, fmt.Sprintf("billing_rule_id=%d forward_rule_ids=%s", row.BillingRuleID, row.ForwardRuleIDs))
	s.invalidateCache(relay.KindForwardRule, model)
	s.invalidateCache(relay.KindBillingRule, model)
	c.JSON(http.StatusOK, gin.H{"binding": row})
}

func (s *Server) adminDeleteModelRuleBinding(c *gin.Context) {
	model := strings.TrimSpace(c.Param("model"))
	if model == "" {
		fail(c, http.StatusBadRequest, "model required")
		return
	}
	res := s.db.WithContext(c.Request.Context()).Where("model = ?", model).Delete(&pricing.ModelRuleBinding{})
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "delete model rule binding failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "model rule binding not found")
		return
	}
	s.writeAudit(c, "model_rule_binding_delete", model, "deleted binding")
	s.invalidateCache(relay.KindForwardRule, model)
	s.invalidateCache(relay.KindBillingRule, model)
	c.JSON(http.StatusOK, gin.H{"deleted": model})
}
