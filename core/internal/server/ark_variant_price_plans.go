package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This is deliberately not a PricingRule or ModelConfig. A saved plan conveys
// neither a verified minimum nor permission to invoke an unsupported adapter.
type variantPricePlan struct {
	ID              uint64 `gorm:"primaryKey"`
	ChannelID       uint64
	Model           string
	UpstreamModel   string
	CanonicalModel  string
	ProviderScopeID uint64
	Version         string
	CreatedBy       uint64
	IdempotencyKey  string
	BodyHash        string
	ProposalJSON    string `gorm:"column:proposal_json"`
	ContextJSON     string `gorm:"column:context_json"`
	CreatedAt       time.Time
}

func (variantPricePlan) TableName() string { return "video_variant_price_plans" }

type variantPlanPrice struct {
	Resolution       string `json:"resolution"`
	InputVideo       bool   `json:"input_video"`
	RetailUSDPer1M   string `json:"retail_usd_per_1m"`
	SupplierCNYPer1M string `json:"supplier_cny_per_1m"`
	RiskMultiplier   string `json:"risk_multiplier"`
}

type variantRiskSource struct {
	RuleID         string `json:"rule_id"`
	Version        string `json:"version"`
	SourceHash     string `json:"source_hash"`
	RiskMultiplier string `json:"risk_multiplier"`
}

type variantPriceProposal struct {
	FXUSDPerCNY          string                   `json:"fx_usd_per_cny"`
	FXVersion            string                   `json:"fx_version"`
	FXObservedAt         time.Time                `json:"fx_observed_at"`
	SourceConfigVersion  string                   `json:"source_config_version"`
	SourcePricingVersion string                   `json:"source_pricing_version"`
	RiskSource           string                   `json:"risk_source"`
	RiskSourceRules      []variantRiskSource      `json:"risk_source_rules"`
	MinimumStatus        string                   `json:"minimum_status"`
	Prices               []variantPlanPrice       `json:"prices"`
	Sources              []arkverification.Source `json:"sources"`
	EvidenceSHA256       string                   `json:"evidence_sha256"`
}

type savedVariantPricePlan struct {
	ID        uint64               `json:"id"`
	Version   string               `json:"version"`
	CreatedAt time.Time            `json:"created_at"`
	CreatedBy uint64               `json:"created_by"`
	Proposal  variantPriceProposal `json:"proposal"`
}

type variantPricePlanView struct {
	ChannelID            uint64                 `json:"channel_id"`
	Model                string                 `json:"model"`
	UpstreamModel        string                 `json:"upstream_model"`
	CanonicalModel       string                 `json:"canonical_model"`
	ContextSHA256        string                 `json:"context_sha256"`
	AdapterStatus        string                 `json:"adapter_status"`
	Invocable            bool                   `json:"invocable"`
	Proposal             variantPriceProposal   `json:"proposal"`
	Saved                *savedVariantPricePlan `json:"saved"`
	SavedMatchesProposal bool                   `json:"saved_matches_proposal"`
}

func (s *Server) registerArkVariantPricePlans(su *gin.RouterGroup) {
	base := "/video/variant-price-plans/:channel/:model"
	su.GET(base, s.getArkVariantPricePlan)
	su.POST(base, s.saveArkVariantPricePlan)
}

func variantPlanContextHash(source string, latestID uint64) string {
	return verificationDigest(struct {
		Source json.RawMessage `json:"source"`
		Latest uint64          `json:"latest_plan_id"`
	}{json.RawMessage(source), latestID})
}

func variantSavedPlan(row variantPricePlan) (*savedVariantPricePlan, error) {
	var proposal variantPriceProposal
	if e := json.Unmarshal([]byte(row.ProposalJSON), &proposal); e != nil {
		return nil, e
	}
	return &savedVariantPricePlan{row.ID, row.Version, row.CreatedAt.UTC(), row.CreatedBy, proposal}, nil
}

func variantPlanReplay(row variantPricePlan) (variantPricePlanView, error) {
	saved, e := variantSavedPlan(row)
	if e != nil {
		return variantPricePlanView{}, e
	}
	return variantPricePlanView{ChannelID: row.ChannelID, Model: row.Model, UpstreamModel: row.UpstreamModel, CanonicalModel: row.CanonicalModel, ContextSHA256: variantPlanContextHash(row.ContextJSON, row.ID), AdapterStatus: "not_integrated", Proposal: saved.Proposal, Saved: saved, SavedMatchesProposal: true}, nil
}

// Config -> channel -> scope matches the configuration publication lock order.
// No channel credential column is read, copied into context, or returned.
func loadVariantPricePlan(tx *gorm.DB, channelID uint64, model string, now time.Time, lock bool) (variantPricePlanView, string, uint64, error) {
	out := variantPricePlanView{ChannelID: channelID, Model: model, AdapterStatus: "not_integrated"}
	query := func() *gorm.DB {
		q := tx.Session(&gorm.Session{})
		if lock {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		return q
	}
	var cfg video.ModelConfig
	if e := query().Where("public_model = ?", ark.Model20).Take(&cfg).Error; e != nil {
		return out, "", 0, e
	}
	if cfg.ChannelID != channelID || !cfg.Enabled || cfg.VerifiedAt == nil || cfg.Version == "" || cfg.PricingVersion == "" {
		return out, "", 0, videobilling.ErrConflict
	}
	var cap ark.Capability
	var bounds map[string]video.Dimensions
	if json.Unmarshal([]byte(cfg.CapabilityJSON), &cap) != nil || json.Unmarshal([]byte(cfg.BoundsJSON), &bounds) != nil || validateVideoCapability(cfg, cap, bounds) != nil {
		return out, "", 0, videobilling.ErrConflict
	}
	if standard, reason := seedanceIdentity(cap.UpstreamModel); standard != ark.Model20 || reason != "" {
		return out, "", 0, videobilling.ErrConflict
	}
	var channel relay.Channel
	if e := query().Select("id,provider,base_url,status,owner_org_id,models,model_mapping").Where("id = ?", channelID).Take(&channel).Error; e != nil {
		return out, "", 0, e
	}
	listed := false
	for _, name := range strings.Split(channel.Models, ",") {
		listed = listed || strings.TrimSpace(name) == model
	}
	if !listed || channel.Status != 1 {
		return out, "", 0, videobilling.ErrConflict
	}
	mapping := map[string]string{}
	if channel.ModelMapping != "" && json.Unmarshal([]byte(channel.ModelMapping), &mapping) != nil {
		return out, "", 0, videobilling.ErrConflict
	}
	out.UpstreamModel = model
	if mapped, ok := mapping[model]; ok {
		out.UpstreamModel = mapped
	}
	switch out.UpstreamModel {
	case "doubao-seedance-2-0-fast-260128", "doubao-seedance-2-0-mini-260615":
	default:
		return out, "", 0, videobilling.ErrConflict
	}
	out.CanonicalModel = officialVariantTariffModel(catalogModel{Model: model, UpstreamModel: out.UpstreamModel}, channel)
	if out.CanonicalModel == "" {
		return out, "", 0, videobilling.ErrConflict
	}
	var scope video.ProviderScope
	if e := query().Where("id = ?", cfg.ProviderScopeID).Take(&scope).Error; e != nil {
		return out, "", 0, e
	}
	fx, e := parsePrice(scope.FXUSDPerCNY)
	if e != nil || !scope.Enabled || scope.Region != "cn-beijing" || scope.AccountRef == "" || scope.ProjectName == "" || !priceVersion.MatchString(scope.FXVersion) || scope.FXObservedAt.IsZero() || scope.FXObservedAt.After(now) {
		return out, "", 0, videobilling.ErrConflict
	}
	rules, e := currentPricingRules(query(), cfg)
	if e != nil {
		return out, "", 0, e
	}
	proposal := variantPriceProposal{FXUSDPerCNY: fx.StringFixed(12), FXVersion: scope.FXVersion, FXObservedAt: scope.FXObservedAt.UTC(), SourceConfigVersion: cfg.Version, SourcePricingVersion: cfg.PricingVersion, RiskSource: "existing_standard_2_0_policy", MinimumStatus: "unverified", Prices: []variantPlanPrice{}, RiskSourceRules: []variantRiskSource{}}
	var risk decimal.Decimal
	for _, rule := range rules {
		if !rule.Enabled || rule.EffectiveFrom.After(now) || rule.EffectiveUntil != nil && !rule.EffectiveUntil.After(now) || rule.Mode != "generate" || rule.ServiceTier != "default" || rule.Draft {
			continue
		}
		value, err := parsePrice(rule.RiskMultiplier.String())
		_, retailErr := parsePrice(rule.RetailUSDPer1M.String())
		_, costErr := parsePrice(rule.SupplierCNYPer1M.String())
		sourceHash, hashErr := hex.DecodeString(rule.SourceHash)
		if err != nil || value.LessThan(decimal.NewFromInt(1)) || value.GreaterThan(decimal.NewFromInt(100)) || !priceCellSupported(cellFromRule(rule), cap) || retailErr != nil || costErr != nil || rule.RuleID == "" || hashErr != nil || len(sourceHash) != 32 {
			return out, "", 0, videobilling.ErrConflict
		}
		if len(proposal.RiskSourceRules) > 0 && !risk.Equal(value) {
			return out, "", 0, videobilling.ErrConflict
		}
		risk = value
		proposal.RiskSourceRules = append(proposal.RiskSourceRules, variantRiskSource{rule.RuleID, rule.Version, rule.SourceHash, risk.String()})
	}
	if len(proposal.RiskSourceRules) == 0 {
		return out, "", 0, videobilling.ErrConflict
	}
	tariff, e := arkverification.LoadTariff(out.CanonicalModel)
	if e != nil || tariff.VerifiedAt.After(now) {
		return out, "", 0, videobilling.ErrConflict
	}
	proposal.Sources, proposal.EvidenceSHA256 = tariff.Sources, verificationDigest(tariff)
	for _, price := range tariff.Prices {
		cost, err := parsePrice(price.SupplierCNYPer1M)
		if err != nil {
			return out, "", 0, err
		}
		retail := cost.Mul(fx)
		if _, err := parsePrice(retail.String()); err != nil {
			return out, "", 0, err
		}
		proposal.Prices = append(proposal.Prices, variantPlanPrice{price.Resolution, price.InputVideo, retail.StringFixed(12), cost.String(), risk.String()})
	}
	out.Proposal = proposal
	// Include the entire source configuration and rule set, not just their
	// human-assigned version strings; in-place changes invalidate the preview.
	contextRaw, e := json.Marshal(struct {
		ChannelID                                           uint64
		Model, Upstream, Provider, BaseURL, Models, Mapping string
		Status                                              int8
		OwnerOrgID                                          uint64
		Config                                              video.ModelConfig
		Scope                                               video.ProviderScope
		Rules                                               []videobilling.PricingRule
		Proposal                                            variantPriceProposal
	}{channelID, model, out.UpstreamModel, channel.Provider, channel.BaseURL, channel.Models, channel.ModelMapping, channel.Status, channel.OwnerOrgID, cfg, scope, rules, proposal})
	if e != nil {
		return out, "", 0, e
	}
	var latest variantPricePlan
	e = query().Where("channel_id = ? AND model = ?", channelID, model).Order("id DESC").Take(&latest).Error
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return out, "", 0, e
	}
	if e == nil {
		out.Saved, e = variantSavedPlan(latest)
		if e != nil {
			return out, "", 0, e
		}
		out.SavedMatchesProposal = latest.UpstreamModel == out.UpstreamModel && latest.ContextJSON == string(contextRaw) && verificationDigest(out.Saved.Proposal) == verificationDigest(proposal)
	}
	out.ContextSHA256 = variantPlanContextHash(string(contextRaw), latest.ID)
	return out, string(contextRaw), scope.ID, nil
}

func variantPlanPath(c *gin.Context) (uint64, string, bool) {
	id, e := strconv.ParseUint(c.Param("channel"), 10, 64)
	model := c.Param("model")
	if e != nil || id == 0 || len(model) == 0 || len(model) > 128 || strings.TrimSpace(model) != model {
		fail(c, 422, "invalid variant price plan identity")
		return 0, "", false
	}
	return id, model, true
}

func (s *Server) getArkVariantPricePlan(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	id, model, ok := variantPlanPath(c)
	if !ok {
		return
	}
	var out variantPricePlanView
	e := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		out, _, _, err = loadVariantPricePlan(tx, id, model, s.now().UTC(), false)
		return err
	})
	if e != nil {
		fail(c, 409, "verified variant price, channel, FX or source policy unavailable")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}

func (s *Server) saveArkVariantPricePlan(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	id, model, ok := variantPlanPath(c)
	if !ok {
		return
	}
	var in struct {
		ExpectedContextSHA256 string `json:"expected_context_sha256"`
	}
	key := c.GetHeader("Idempotency-Key")
	if videoAdminDecode(c, &in) != nil || len(in.ExpectedContextSHA256) != 64 || len(key) < 1 || len(key) > 128 || strings.TrimSpace(key) != key {
		fail(c, 422, "strict expected context and Idempotency-Key required")
		return
	}
	if digest, e := hex.DecodeString(in.ExpectedContextSHA256); e != nil || len(digest) != 32 {
		fail(c, 422, "invalid context digest")
		return
	}
	bodyHash := verificationDigest(in)
	var out variantPricePlanView
	// A completed request remains replayable after a later policy change. Its
	// original response is reconstructed from immutable, noncredential context.
	replay := func(tx *gorm.DB) (bool, error) {
		var row variantPricePlan
		e := tx.Where("created_by = ? AND idempotency_key = ?", actor.ID, key).Take(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if row.ChannelID != id || row.Model != model || row.BodyHash != bodyHash {
			return false, videobilling.ErrConflict
		}
		out, e = variantPlanReplay(row)
		return true, e
	}
	done, e := replay(s.db.WithContext(c.Request.Context()))
	if e == nil && !done {
		e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			now := s.now().UTC().Truncate(time.Millisecond)
			var source string
			var scopeID uint64
			var err error
			out, source, scopeID, err = loadVariantPricePlan(tx, id, model, now, true)
			if err != nil {
				return err
			}
			// Re-check with a current locking read after the shared source lock;
			// concurrent same-key requests must return the first committed plan.
			if found, err := replay(tx.Clauses(clause.Locking{Strength: "UPDATE"})); err != nil || found {
				return err
			}
			if out.ContextSHA256 != in.ExpectedContextSHA256 {
				return videobilling.ErrConflict
			}
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				return err
			}
			proposalRaw, _ := json.Marshal(out.Proposal)
			row := variantPricePlan{ChannelID: id, Model: model, UpstreamModel: out.UpstreamModel, CanonicalModel: out.CanonicalModel, ProviderScopeID: scopeID, Version: "variant-price-" + hex.EncodeToString(token[:]), CreatedBy: actor.ID, IdempotencyKey: key, BodyHash: bodyHash, ProposalJSON: string(proposalRaw), ContextJSON: source, CreatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			if err := tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor.ID), Action: "video.variant_price_plan.save", Target: fmt.Sprintf("channel#%d/%s", id, model), Detail: "version=" + row.Version + ";context_sha256=" + in.ExpectedContextSHA256, CreatedAt: now.UnixMilli()}).Error; err != nil {
				return err
			}
			out, err = variantPlanReplay(row)
			return err
		})
	}
	if e != nil {
		fail(c, 409, "variant price plan rejected: stale context, conflicting key or unavailable source")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}
