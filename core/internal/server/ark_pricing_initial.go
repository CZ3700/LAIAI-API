package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/arkpricetemplate"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The preview is an authenticated, short-lived proposal, not an admission or
// upstream probe. Its signature binds every supplied field to its reviewer.
type initialPricingIntent struct {
	PricingMode   string    `json:"pricing_mode"`
	ContextSHA256 string    `json:"context_sha256"`
	ActorID       uint64    `json:"actor_id"`
	ExpiresAt     time.Time `json:"expires_at"`
	Signature     string    `json:"signature"`
}

type initialPricingInput struct {
	ChannelID        uint64 `json:"channel_id"`
	TemplateVersion  string `json:"template_version"`
	PricingMode      string `json:"pricing_mode"`
	ProviderScopeID  uint64 `json:"provider_scope_id"`
	AccountRef       string `json:"account_ref"`
	ProjectName      string `json:"project_name"`
	FXUSDPerCNY      string `json:"fx_usd_per_cny"`
	DailyBudgetCNY   string `json:"daily_budget_cny"`
	MonthlyBudgetCNY string `json:"monthly_budget_cny"`
	Enabled          bool   `json:"enabled"`
}

type initialPricingError string

func (e initialPricingError) Error() string { return string(e) }

func initialPricingFail(c *gin.Context, status int, err error) {
	code := "initial_setup_unavailable"
	var known initialPricingError
	if errors.As(err, &known) {
		code = string(known)
	}
	message := map[string]string{
		"initial_setup_required":      "该模型尚未完成首次接入，请先设置账户、项目、汇率和供应商预算。",
		"configuration_exists":        "该模型已有生效配置，请使用现有定价或渠道绑定修复。",
		"channel_unavailable":         "请选择已启用的平台火山官方渠道。",
		"channel_model_mismatch":      "渠道模型映射与所选官方模型版本不一致。",
		"channel_credential_required": "请先在渠道管理保存并启用官方 API Key。",
		"provider_scope_required":     "请填写真实账户 UID、项目名称和有效汇率。",
		"provider_scope_exists":       "该账户和项目已有作用域，请选择已有作用域，避免重置共享预算。",
		"provider_scope_unavailable":  "所选账户作用域不可用或汇率快照无效。",
		"supplier_budgets_required":   "请设置正数的供应商日预算和月预算；已有作用域必须具备有效日/月预算。",
		"scope_fields_conflict":       "已有作用域的汇率和预算必须保留，请勿提交新的账户参数。",
		"minimum_evidence_conflict":   "最低用量证据与已核验资料不一致，暂不能发布。",
		"initial_setup_changed":       "渠道、凭据、账户预算或最低用量资料已变化，请重新预览。",
		"initial_preview_expired":     "首次接入预览已过期，请重新预览。",
		"initial_preview_invalid":     "预览内容无效或已修改，请重新预览后原样提交。",
		"template_unavailable":        "定价模板或官方核验资料不可用。",
		"video_runtime_required":      "视频运行服务尚未配置，请联系管理员。",
		"pricing_mode_invalid":        "请选择官方默认价格或已保存的定价模板。",
	}[code]
	if message == "" {
		message = "无法准备首次接入配置，请检查服务状态后重试。"
	}
	c.AbortWithStatusJSON(status, gin.H{"error": message, "code": code, "blockers": []string{code}})
}

func initialQuery(tx *gorm.DB, lock bool) *gorm.DB {
	q := tx.Session(&gorm.Session{})
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return q
}

type initialChannelContext struct {
	rebindChannel
	LegacyCredentialSHA256 string
}
type initialCredentialContext struct {
	rebindCredential
	CiphertextSHA256 string
}

func initialChannel(tx *gorm.DB, model, upstream string, id uint64, lock bool) (initialChannelContext, []initialCredentialContext, error) {
	var ch initialChannelContext
	if err := initialQuery(tx, lock).Table("channels").Select("id,name,provider,base_url,status,owner_org_id,models,model_mapping,credential_enc <> '' AS has_legacy_credential").Where("id = ?", id).Take(&ch.rebindChannel).Error; err != nil {
		return ch, nil, initialPricingError("channel_unavailable")
	}
	if ch.OwnerOrgID != 0 || ch.Status != 1 || (ch.Provider != "volcengine" && ch.Provider != "modelark") || ch.BaseURL != "https://ark.cn-beijing.volces.com/api/v3" {
		return ch, nil, initialPricingError("channel_unavailable")
	}
	mapping := map[string]string{}
	if ch.ModelMapping != "" && json.Unmarshal([]byte(ch.ModelMapping), &mapping) != nil {
		return ch, nil, initialPricingError("channel_model_mismatch")
	}
	matched := false
	for _, listed := range strings.Split(ch.Models, ",") {
		listed = strings.TrimSpace(listed)
		target := listed
		if mapped, ok := mapping[listed]; ok {
			target = mapped
		}
		public, reason := seedanceIdentity(listed)
		if target == upstream && reason == "" && (public == "" || public == model) {
			matched = true
		}
	}
	if !matched {
		return ch, nil, initialPricingError("channel_model_mismatch")
	}
	var rawCredentials []struct {
		ID            uint64
		Status        int8
		Fingerprint   string
		CredentialEnc string
	}
	if err := initialQuery(tx, lock).Table("channel_credentials").Select("id,status,fingerprint,credential_enc").Where("channel_id = ?", id).Order("id").Find(&rawCredentials).Error; err != nil {
		return ch, nil, err
	}
	// Bind rotations without exposing ciphertext or allowing an unchanged
	// fingerprint to hide a credential replacement. Nothing is decrypted.
	credentials := make([]initialCredentialContext, 0, len(rawCredentials))
	for _, raw := range rawCredentials {
		digest := sha256.Sum256([]byte(raw.CredentialEnc))
		credentials = append(credentials, initialCredentialContext{rebindCredential: rebindCredential{ID: raw.ID, Status: raw.Status, Fingerprint: raw.Fingerprint, HasValue: raw.CredentialEnc != ""}, CiphertextSHA256: hex.EncodeToString(digest[:])})
	}
	if len(credentials) == 0 {
		var raw struct{ CredentialEnc string }
		if err := initialQuery(tx, lock).Table("channels").Select("credential_enc").Where("id = ?", id).Take(&raw).Error; err != nil {
			return ch, nil, err
		}
		digest := sha256.Sum256([]byte(raw.CredentialEnc))
		ch.LegacyCredentialSHA256 = hex.EncodeToString(digest[:])
	}
	has := len(credentials) == 0 && ch.HasLegacyCredential
	for _, v := range credentials {
		has = has || v.Status == relay.CredentialActive && v.HasValue
	}
	if !has {
		return ch, credentials, initialPricingError("channel_credential_required")
	}
	return ch, credentials, nil
}

func initialBudgetsValid(rows []videobilling.QuotaPolicy) bool {
	if len(rows) < 2 || len(rows) > 20 {
		return false
	}
	day, month := false, false
	for _, p := range rows {
		if !p.Enabled || p.OrgID != 0 || p.Scope != videobilling.ScopeProvider {
			return false
		}
		if p.Model != "" {
			continue
		} // Preserve additional model policies too.
		if (p.Metric == videobilling.MetricCNY || p.Metric == videobilling.MetricUSD) && p.LimitAmount != nil && p.LimitAmount.IsPositive() && p.LimitUnits == nil {
			if _, err := time.LoadLocation(p.Timezone); err != nil {
				return false
			}
			day = day || p.Period == videobilling.PeriodDay
			month = month || p.Period == videobilling.PeriodMonth
		}
	}
	return day && month
}

func initialBudgetView(rows []videobilling.QuotaPolicy) []gin.H {
	out := make([]gin.H, 0, len(rows))
	for _, p := range rows {
		out = append(out, gin.H{"id": p.ID, "model": p.Model, "metric": p.Metric, "period": p.Period, "timezone": p.Timezone, "limit_amount": p.LimitAmount, "limit_units": p.LimitUnits, "version": p.Version})
	}
	return out
}
func initialScopeView(scope video.ProviderScope, budgets []videobilling.QuotaPolicy) gin.H {
	return gin.H{"id": scope.ID, "account_ref": scope.AccountRef, "region": scope.Region, "project_name": scope.ProjectName, "enabled": scope.Enabled, "fx_usd_per_cny": scope.FXUSDPerCNY, "fx_version": scope.FXVersion, "fx_observed_at": scope.FXObservedAt, "supplier_budgets": initialBudgetView(budgets)}
}

func initialScopeValid(scope video.ProviderScope, now time.Time) bool {
	_, err := parsePrice(scope.FXUSDPerCNY)
	return scope.Enabled && scope.AccountRef != "" && scope.ProjectName != "" && scope.Region == "cn-beijing" && err == nil && priceVersion.MatchString(scope.FXVersion) && !scope.FXObservedAt.IsZero() && !scope.FXObservedAt.After(now)
}

func (s *Server) getArkInitialPricingSetup(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	id, err := strconv.ParseUint(c.Query("channel_id"), 10, 64)
	if err != nil || id == 0 {
		initialPricingFail(c, 422, initialPricingError("channel_unavailable"))
		return
	}
	model := c.Param("model")
	items, err := arkpricetemplate.List(s.db.WithContext(c.Request.Context()), model)
	if err != nil || !ark.IsPublicModel(model) {
		initialPricingFail(c, 503, initialPricingError("template_unavailable"))
		return
	}
	blockers := []string{}
	var count int64
	if err = s.db.Model(&video.ModelConfig{}).Where("public_model = ?", model).Count(&count).Error; err != nil {
		initialPricingFail(c, 503, err)
		return
	}
	if count > 0 {
		blockers = append(blockers, "configuration_exists")
	}
	name := ""
	if len(items) == 0 {
		blockers = append(blockers, "template_unavailable")
	} else {
		ch, _, e := initialChannel(s.db, model, items[0].UpstreamModel, id, false)
		name = ch.Name
		if e != nil {
			var known initialPricingError
			if errors.As(e, &known) {
				blockers = append(blockers, string(known))
			} else {
				initialPricingFail(c, 503, e)
				return
			}
		}
	}
	if s.video == nil || s.video.Evidence == nil {
		blockers = append(blockers, "video_runtime_required")
	}
	var scopes []video.ProviderScope
	if err = s.db.Where("region = ?", "cn-beijing").Order("id").Find(&scopes).Error; err != nil {
		initialPricingFail(c, 503, err)
		return
	}
	views := []gin.H{}
	for _, scope := range scopes {
		var budgets []videobilling.QuotaPolicy
		if err = s.db.Where("org_id = 0 AND scope = ? AND owner_id = ? AND enabled = ?", videobilling.ScopeProvider, scope.ID, true).Order("id").Find(&budgets).Error; err != nil {
			initialPricingFail(c, 503, err)
			return
		}
		views = append(views, initialScopeView(scope, budgets))
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"model": model, "channel_id": id, "channel_name": name, "configuration_exists": count > 0, "available": len(blockers) == 0, "blockers": blockers, "templates": items, "provider_scopes": views})
}

// Context excludes live budget consumption: publication never changes buckets.
// It includes all enabled policies, credential identity and minimum evidence.
func initialPricingContext(tx *gorm.DB, in videoPublication, t arkpricetemplate.Template, p arkverification.Profile, now time.Time, lock bool) (string, error) {
	var cfg video.ModelConfig
	err := initialQuery(tx, lock).Where("public_model = ?", in.Configuration.PublicModel).Take(&cfg).Error
	if err == nil {
		return "", initialPricingError("configuration_exists")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	ch, credentials, err := initialChannel(tx, t.Model, t.UpstreamModel, in.Configuration.ChannelID, lock)
	if err != nil {
		return "", err
	}
	scope := in.Scope
	budgets := in.SupplierBudgets
	if scope.ID == 0 {
		var existing video.ProviderScope
		err := initialQuery(tx, lock).Where("account_ref = ? AND region = ? AND project_name = ?", scope.AccountRef, scope.Region, scope.ProjectName).Take(&existing).Error
		if err == nil {
			return "", initialPricingError("provider_scope_exists")
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
	} else {
		var actual video.ProviderScope
		if err := initialQuery(tx, lock).Where("id = ?", scope.ID).Take(&actual).Error; err != nil {
			return "", initialPricingError("provider_scope_unavailable")
		}
		var actualBudgets []videobilling.QuotaPolicy
		if err := initialQuery(tx, lock).Where("org_id = 0 AND scope = ? AND owner_id = ? AND enabled = ?", videobilling.ScopeProvider, scope.ID, true).Order("id").Find(&actualBudgets).Error; err != nil {
			return "", err
		}
		if verificationDigest(actual) != verificationDigest(scope) || verificationDigest(actualBudgets) != verificationDigest(budgets) {
			return "", initialPricingError("initial_setup_changed")
		}
	}
	if !initialScopeValid(scope, now) {
		return "", initialPricingError("provider_scope_unavailable")
	}
	if !initialBudgetsValid(budgets) {
		return "", initialPricingError("supplier_budgets_required")
	}
	if _, err := matchingVerificationMinimum(tx, t.Model, p); err != nil {
		return "", initialPricingError("minimum_evidence_conflict")
	}
	var sets []videobilling.MinimumRuleSet
	var cells []videobilling.MinimumRuleCell
	if err := initialQuery(tx, lock).Where("model = ? AND version = ?", t.Model, p.MinimumVersion).Order("id").Find(&sets).Error; err != nil {
		return "", err
	}
	for _, set := range sets {
		var rows []videobilling.MinimumRuleCell
		if err := initialQuery(tx, lock).Where("rule_set_id = ?", set.ID).Order("id").Find(&rows).Error; err != nil {
			return "", err
		}
		cells = append(cells, rows...)
	}
	reference, err := arkpricecatalog.ReferenceCandidates(t.Model)
	if err != nil {
		return "", err
	}
	return verificationDigest(struct {
		Channel       initialChannelContext
		Credentials   []initialCredentialContext
		Scope         video.ProviderScope
		Budgets       []videobilling.QuotaPolicy
		Template      arkpricetemplate.Template
		MinimumSets   []videobilling.MinimumRuleSet
		MinimumCells  []videobilling.MinimumRuleCell
		CatalogSHA256 string
	}{ch, credentials, scope, budgets, t, sets, cells, reference.ManifestSHA256}), nil
}

func (s *Server) initialPublicationSignature(in videoPublication) string {
	intent := *in.InitialSetup
	intent.Signature = ""
	in.InitialSetup = &intent
	raw, _ := json.Marshal(in)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("laiai/video/initial-pricing/v1\x00"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func officialInitialPrices(in *videoPublication) error {
	ref, err := arkpricecatalog.ReferenceCandidates(in.Configuration.PublicModel)
	if err != nil {
		return err
	}
	fx, err := parsePrice(in.Scope.FXUSDPerCNY)
	if err != nil {
		return err
	}
	for i := range in.Prices {
		p := &in.Prices[i]
		matched := false
		for _, price := range ref.Prices {
			if price.Key != priceCellKey(cellFromRule(*p)) {
				continue
			}
			cost, e := parsePrice(price.SupplierCNYPer1M)
			if e != nil || price.Precision != "exact" {
				return videobilling.ErrInvalid
			}
			retail, e := parsePrice(cost.Mul(fx).Round(12).String())
			if e != nil {
				return e
			}
			p.SupplierCNYPer1M, p.RetailUSDPer1M, p.RiskMultiplier = cost, retail, decimal.NewFromInt(1)
			matched = true
		}
		if !matched {
			return videobilling.ErrPricingUnavailable
		}
	}
	return nil
}

func (s *Server) previewArkInitialPricing(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	if s.video == nil || s.video.Evidence == nil {
		initialPricingFail(c, 503, initialPricingError("video_runtime_required"))
		return
	}
	var input initialPricingInput
	if videoAdminDecode(c, &input) != nil || (input.PricingMode != "official" && input.PricingMode != "template") {
		initialPricingFail(c, 422, initialPricingError("pricing_mode_invalid"))
		return
	}
	now := s.now().UTC()
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		initialPricingFail(c, 503, err)
		return
	}
	version := hex.EncodeToString(token[:])
	var publication videoPublication
	var template arkpricetemplate.Template
	var profile arkverification.Profile
	var cells []pricingCell
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		template, err = arkpricetemplate.Load(tx, c.Param("model"), input.TemplateVersion)
		if err != nil {
			return initialPricingError("template_unavailable")
		}
		ref := template.Reference()
		publication = videoPublication{Configuration: video.ModelConfig{PublicModel: template.Model, Version: "cfg-" + version, PricingVersion: "price-" + version, ChannelID: input.ChannelID, Enabled: input.Enabled}, PricingTemplate: &ref, InitialSetup: &initialPricingIntent{PricingMode: input.PricingMode, ActorID: actor.ID, ExpiresAt: now.Add(15 * time.Minute)}}
		if input.ProviderScopeID == 0 {
			if strings.TrimSpace(input.AccountRef) != input.AccountRef || len(input.AccountRef) < 1 || len(input.AccountRef) > 128 || strings.TrimSpace(input.ProjectName) != input.ProjectName || len(input.ProjectName) < 1 || len(input.ProjectName) > 128 {
				return initialPricingError("provider_scope_required")
			}
			fx, e := parsePrice(input.FXUSDPerCNY)
			if e != nil {
				return initialPricingError("provider_scope_required")
			}
			day, de := parsePrice(input.DailyBudgetCNY)
			month, me := parsePrice(input.MonthlyBudgetCNY)
			// Quota amounts are DECIMAL(20,8). parsePrice limits the integer
			// part to 12 digits; require exact fractional representability too.
			// Never let database rounding turn a positive limit into zero.
			if de != nil || me != nil || !day.Equal(day.Round(8)) || !month.Equal(month.Round(8)) {
				return initialPricingError("supplier_budgets_required")
			}
			publication.Scope = video.ProviderScope{ScopeKey: "ark-" + verificationDigest([]string{input.AccountRef, "cn-beijing", input.ProjectName})[:40], AccountRef: input.AccountRef, Region: "cn-beijing", ProjectName: input.ProjectName, Enabled: true, FXUSDPerCNY: fx.String(), FXVersion: "fx-" + version, FXObservedAt: now.Truncate(time.Millisecond)}
			publication.SupplierBudgets = []videobilling.QuotaPolicy{{Scope: videobilling.ScopeProvider, Metric: videobilling.MetricCNY, Period: videobilling.PeriodDay, Timezone: "Asia/Shanghai", LimitAmount: &day, Version: "budget-" + version, Enabled: true}, {Scope: videobilling.ScopeProvider, Metric: videobilling.MetricCNY, Period: videobilling.PeriodMonth, Timezone: "Asia/Shanghai", LimitAmount: &month, Version: "budget-" + version, Enabled: true}}
		} else {
			if input.AccountRef != "" || input.ProjectName != "" || input.FXUSDPerCNY != "" || input.DailyBudgetCNY != "" || input.MonthlyBudgetCNY != "" {
				return initialPricingError("scope_fields_conflict")
			}
			if e := tx.Where("id = ?", input.ProviderScopeID).Take(&publication.Scope).Error; e != nil {
				return initialPricingError("provider_scope_unavailable")
			}
			if e := tx.Where("org_id = 0 AND scope = ? AND owner_id = ? AND enabled = ?", videobilling.ScopeProvider, input.ProviderScopeID, true).Order("id").Find(&publication.SupplierBudgets).Error; e != nil {
				return e
			}
		}
		prepared := publication
		template, profile, err = resolveInitialPricingTemplate(tx, &prepared, now)
		if err != nil {
			return initialPricingError("template_unavailable")
		}
		if input.PricingMode == "official" {
			if err := officialInitialPrices(&prepared); err != nil {
				return err
			}
		}
		for _, p := range prepared.Prices {
			cells = append(cells, cellFromRule(p))
		}
		digest, err := initialPricingContext(tx, publication, template, profile, now, false)
		if err != nil {
			return err
		}
		publication.InitialSetup.ContextSHA256 = digest
		publication.InitialSetup.Signature = s.initialPublicationSignature(publication)
		return nil
	})
	if err != nil {
		initialPricingFail(c, 409, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"model": template.Model, "channel_id": input.ChannelID, "config_version": publication.Configuration.Version, "pricing_version": publication.Configuration.PricingVersion, "pricing_mode": input.PricingMode, "publishable": true, "live_tested": false, "cells": cells, "template_ref": template.Reference(), "minimum_rule_version": profile.MinimumVersion, "minimum_cell_count": len(profile.MinimumCells), "provider_scope": initialScopeView(publication.Scope, publication.SupplierBudgets), "supplier_budgets": initialBudgetView(publication.SupplierBudgets), "enabled": input.Enabled, "publication": publication})
}

func (s *Server) validateInitialPublication(in videoPublication, actor uint64) error {
	if in.InitialSetup == nil {
		return nil
	}
	i := in.InitialSetup
	if in.PricingTemplate == nil || len(in.Prices) != 0 || i.ActorID != actor || (i.PricingMode != "official" && i.PricingMode != "template") || len(i.ContextSHA256) != 64 || !hmac.Equal([]byte(i.Signature), []byte(s.initialPublicationSignature(in))) {
		return initialPricingError("initial_preview_invalid")
	}
	return nil
}

func (s *Server) validateInitialPublicationContext(tx *gorm.DB, in videoPublication, t arkpricetemplate.Template, p arkverification.Profile, now time.Time) error {
	if in.InitialSetup == nil {
		return nil
	}
	if !now.Before(in.InitialSetup.ExpiresAt) {
		return initialPricingError("initial_preview_expired")
	}
	digest, err := initialPricingContext(tx, in, t, p, now, true)
	if err != nil {
		return err
	}
	if digest != in.InitialSetup.ContextSHA256 {
		return initialPricingError("initial_setup_changed")
	}
	return nil
}

// Keep a compact auditable mode name on the existing publication receipt.
func initialPricingAudit(in videoPublication) string {
	if in.InitialSetup == nil {
		return ""
	}
	return fmt.Sprintf(";initial_mode=%s;preview_context=%s", in.InitialSetup.PricingMode, in.InitialSetup.ContextSHA256)
}
