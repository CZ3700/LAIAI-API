package video

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var catalogReasonOrder = []string{"unsupported_model", "model_disabled", "capability_unverified", "capability_invalid", "provider_binding_unavailable", "pricing_missing", "pricing_conflict", "pricing_version_mismatch", "pricing_invalid", "minimum_coverage_missing", "minimum_rule_invalid", "publication_unavailable", "scope_missing", "video_feature_disabled", "video_execution_disabled", "video_execution_policy_invalid", "credential_unavailable"}

func catalogReasons(values ...string) []string {
	out := []string{}
	for _, reason := range catalogReasonOrder {
		if publicationContains(values, reason) {
			out = append(out, reason)
		}
	}
	return out
}

func catalogError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	switch {
	case errors.Is(err, ErrCatalogInvalid), errors.Is(err, identity.ErrRoutingCandidateInvalid), errors.Is(err, identity.ErrVideoCatalogInvalid), errors.Is(err, relay.ErrVideoCatalogInvalid), errors.Is(err, videobilling.ErrCatalogTariffInvalid):
		return ErrCatalogInvalid
	case errors.Is(err, ErrCatalogDenied), errors.Is(err, identity.ErrRoutingCandidateFacts), errors.Is(err, identity.ErrVideoCatalogNotFound):
		return ErrCatalogDenied
	case errors.Is(err, ErrCatalogLimit), errors.Is(err, identity.ErrRoutingCandidateLimit), errors.Is(err, videobilling.ErrCatalogTariffLimit):
		return ErrCatalogLimit
	default:
		return ErrCatalogUnavailable
	}
}

func catalogValidTx(ctx context.Context, tx *gorm.DB) error {
	if ctx == nil || tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.DryRun {
		return ErrCatalogInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrCatalogInvalid
	}
	if name := tx.Dialector.Name(); name != "mysql" && name != "sqlite" {
		return ErrCatalogInvalid
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if tx.Error != nil {
		return ErrCatalogUnavailable
	}
	return nil
}

func catalogRead(ctx context.Context, tx *gorm.DB, columns string, query func(*gorm.DB) error) error {
	child, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := child.Err(); err != nil {
		return err
	}
	if tx.Dialector.Name() == "mysql" {
		deadline, _ := child.Deadline()
		ms := time.Until(deadline).Milliseconds()
		if ms < 1 {
			return context.DeadlineExceeded
		}
		columns = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", min(ms, int64(200)), columns)
	}
	q := tx.Session(&gorm.Session{NewDB: true, Context: child, Logger: logger.Discard}).Select(columns)
	err := query(q)
	if child.Err() != nil {
		return child.Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return gorm.ErrRecordNotFound
	}
	if err != nil {
		return catalogError(child, err)
	}
	return nil
}

func catalogLength(tx *gorm.DB, column string) string {
	if tx.Dialector.Name() == "sqlite" {
		return "LENGTH(CAST(" + column + " AS BLOB))"
	}
	return "OCTET_LENGTH(" + column + ")"
}

func (s *Service) Catalog(ctx context.Context, ref CatalogSubjectRef) (CatalogObservation, error) {
	var out CatalogObservation
	if ctx == nil || s == nil || s.DB == nil || s.DB.Config == nil || s.DB.Statement == nil || s.DB.Dialector == nil || s.DB.DryRun || s.Now == nil {
		return out, ErrCatalogInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := s.DB.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = s.ReadCatalogTx(ctx, tx, ref)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if ctx.Err() != nil {
		return CatalogObservation{}, ctx.Err()
	}
	if err != nil {
		return CatalogObservation{}, catalogError(ctx, err)
	}
	return out, nil
}

func catalogConfigs(ctx context.Context, tx *gorm.DB) ([]ModelConfig, error) {
	var rows []struct {
		ModelConfig
		CapabilityBytes, BoundsBytes *int64
	}
	capLen, boundsLen := catalogLength(tx, "capability_json"), catalogLength(tx, "bounds_json")
	columns := "public_model, version, provider_scope_id, channel_id, enabled, pricing_version, no_charge_statuses, max_input_video_duration_ms, verified_at, " + capLen + " AS capability_bytes, " + boundsLen + " AS bounds_bytes, CASE WHEN " + capLen + " <= 65536 THEN capability_json ELSE NULL END AS capability_json, CASE WHEN " + boundsLen + " <= 65536 THEN bounds_json ELSE NULL END AS bounds_json"
	if err := catalogRead(ctx, tx, columns, func(q *gorm.DB) error {
		return q.Table("video_model_configs").Order("public_model").Limit(65).Find(&rows).Error
	}); err != nil {
		return nil, err
	}
	if len(rows) > 64 {
		return nil, ErrCatalogLimit
	}
	configs := make([]ModelConfig, 0, len(rows))
	total := int64(0)
	for _, row := range rows {
		if row.CapabilityBytes == nil || row.BoundsBytes == nil {
			return nil, ErrCatalogUnavailable
		}
		if *row.CapabilityBytes > 65536 || *row.BoundsBytes > 65536 {
			return nil, ErrCatalogLimit
		}
		total += *row.CapabilityBytes + *row.BoundsBytes
		if total > 1048576 {
			return nil, ErrCatalogLimit
		}
		configs = append(configs, row.ModelConfig)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return configs, nil
}

// ReadCatalogTx consumes current authenticated references in one caller-owned
// RR transaction. No operation, reservation, credential selection or HTTP occurs.
func (s *Service) ReadCatalogTx(ctx context.Context, tx *gorm.DB, ref CatalogSubjectRef) (CatalogObservation, error) {
	var zero CatalogObservation
	if err := catalogValidTx(ctx, tx); err != nil {
		return zero, err
	}
	if s == nil || s.Now == nil || ref.OrgID == 0 || ref.KeyID == 0 {
		return zero, ErrCatalogInvalid
	}
	at := s.Now().UTC()
	subject, err := identity.ReadRoutingCandidateSubjectTx(ctx, tx, ref.OrgID, ref.KeyID, at)
	if err != nil {
		return zero, catalogError(ctx, err)
	}
	if !subject.Found || !subject.KeyActive || subject.KeyExpired || !subject.PrincipalActive || subject.OrgID != ref.OrgID || subject.KeyID != ref.KeyID || subject.Mode != "legacy" || subject.RoutingGroupID != nil || subject.KeyPolicyVersion == 0 {
		return zero, ErrCatalogDenied
	}
	out := CatalogObservation{CheckedAt: at, Models: []CatalogModel{}, validatedSubject: true, modelLimits: subject.ModelLimits}
	configs, err := catalogConfigs(ctx, tx)
	if err != nil {
		return zero, err
	}
	selected := make([]ModelConfig, 0, len(configs))
	scopeSet, channelSet := map[uint64]bool{}, map[uint64]bool{}
	models := []string{}
	refs := []videobilling.CatalogTariffRef{}
	for _, cfg := range configs {
		if !out.AllowsModel(cfg.PublicModel) {
			continue
		}
		selected = append(selected, cfg)
		if !IsOfficialPublicModel(cfg.PublicModel) {
			continue
		}
		models = append(models, cfg.PublicModel)
		if cfg.ProviderScopeID != 0 {
			scopeSet[cfg.ProviderScopeID] = true
			// Invalid persisted configuration remains a diagnostic row. It must
			// not become invalid caller input to the bounded tariff reader.
			if _, err := ParsePublicationCapability(cfg); err == nil {
				refs = append(refs, videobilling.CatalogTariffRef{ProviderScopeID: cfg.ProviderScopeID, Model: cfg.PublicModel, PricingVersion: cfg.PricingVersion})
			}
		}
		if cfg.ChannelID != 0 {
			channelSet[cfg.ChannelID] = true
		}
	}
	if len(selected) == 0 {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return out, nil
	}
	scopeIDs, channelIDs := catalogIDs(scopeSet), catalogIDs(channelSet)
	var scopeRows []ProviderScope
	if len(scopeIDs) > 0 {
		err = catalogRead(ctx, tx, "id, account_ref, region, project_name, enabled, fx_usd_per_cny, fx_version, fx_observed_at", func(q *gorm.DB) error { return q.Where("id IN ?", scopeIDs).Find(&scopeRows).Error })
		if err != nil {
			return zero, err
		}
	}
	scopes := map[uint64]ProviderScope{}
	for _, scope := range scopeRows {
		scopes[scope.ID] = scope
	}
	var policy Policy
	err = catalogRead(ctx, tx, "org_id, async_execution_allowed, capture_history, retention_days", func(q *gorm.DB) error { return q.Where("org_id = ?", ref.OrgID).Take(&policy).Error })
	policyExists := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return zero, err
	}
	channels, err := relay.ReadVideoCatalogChannelsTx(ctx, tx, channelIDs)
	if err != nil {
		return zero, catalogError(ctx, err)
	}
	channelMap := map[uint64]relay.VideoCatalogChannel{}
	for _, ch := range channels {
		channelMap[ch.ID] = ch
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ProviderScopeID != refs[j].ProviderScopeID {
			return refs[i].ProviderScopeID < refs[j].ProviderScopeID
		}
		return refs[i].Model < refs[j].Model
	})
	tariffs, err := videobilling.ReadCatalogTariffsTx(ctx, tx, refs, at)
	if err != nil {
		return zero, catalogError(ctx, err)
	}
	group, err := identity.ReadVideoBillingGroupTx(ctx, tx, ref.OrgID)
	if err != nil {
		return zero, catalogError(ctx, err)
	}
	sort.Strings(models)
	ratios, err := relay.ReadVideoCatalogRatiosTx(ctx, tx, group, models)
	if err != nil {
		return zero, catalogError(ctx, err)
	}
	ratioMap := map[string]decimal.Decimal{}
	for _, r := range ratios {
		ratioMap[r.Model] = r.Ratio
	}
	portraits, err := s.catalogPortraitsTx(ctx, tx, ref.OrgID, scopes, policy, policyExists)
	if err != nil {
		return zero, err
	}
	for _, cfg := range selected {
		row, err := catalogModel(cfg, scopes[cfg.ProviderScopeID], channelMap[cfg.ChannelID], ratioMap[cfg.PublicModel], tariffs)
		if err != nil {
			return zero, catalogError(ctx, err)
		}
		row.Capabilities.PortraitAssets = portraits[cfg.ProviderScopeID]
		row.Actions = catalogActions(subject, policy, policyExists, row.Publication.Status != "blocked", channelMap[cfg.ChannelID].HasUsableCredential)
		out.Models = append(out.Models, row)
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	return out, nil
}

func catalogIDs(values map[uint64]bool) []uint64 {
	ids := make([]uint64, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func catalogModel(cfg ModelConfig, scope ProviderScope, channel relay.VideoCatalogChannel, ratio decimal.Decimal, tariffs videobilling.CatalogTariffSnapshot) (CatalogModel, error) {
	row := CatalogModel{ID: cfg.PublicModel, PricingVersion: cfg.PricingVersion, Prices: []CatalogPrice{}, Capabilities: CatalogCapabilities{Resolutions: []string{}, Ratios: []string{}}, Publication: CatalogPublication{Status: "blocked", ReasonCodes: []string{}, Combinations: []CatalogCombination{}}}
	var raw ark.Capability
	decodeErr := json.Unmarshal([]byte(cfg.CapabilityJSON), &raw)
	row.Enabled, row.Verified = cfg.Enabled && raw.Enabled, cfg.VerifiedAt != nil && raw.Verified
	if !IsOfficialPublicModel(cfg.PublicModel) {
		row.Publication.ReasonCodes = []string{"unsupported_model"}
		return row, nil
	}
	base := []string{}
	if !cfg.Enabled || decodeErr == nil && !raw.Enabled {
		base = append(base, "model_disabled")
	}
	if cfg.VerifiedAt == nil || decodeErr == nil && !raw.Verified {
		base = append(base, "capability_unverified")
	}
	cap, err := ParsePublicationCapability(cfg)
	if err != nil || !utf8.ValidString(cfg.CapabilityJSON) || !utf8.ValidString(cfg.BoundsJSON) {
		row.Publication.ReasonCodes = catalogReasons(append(base, "capability_invalid")...)
		return row, nil
	}
	row.CapabilityVersion = cap.cap.Version
	row.Capabilities = CatalogCapabilities{Resolutions: append([]string{}, cap.cap.Resolutions...), Ratios: append([]string{}, cap.cap.Ratios...), MinDurationSeconds: cap.cap.MinDuration, MaxDurationSeconds: cap.cap.MaxDuration, AutomaticDuration: cap.cap.AutomaticDuration, Frames: cap.cap.Frames, InputVideo: cap.cap.VideoInput, InputAudio: cap.cap.AudioInput, Draft: cap.cap.Draft}
	if scope.ID != cfg.ProviderScopeID || scope.ID == 0 || !scope.Enabled || scope.Region != "cn-beijing" || scope.AccountRef == "" || scope.ProjectName == "" || channel.ID != cfg.ChannelID || !PublicationChannelUsable(channel) {
		base = append(base, "provider_binding_unavailable")
	} else {
		row.Provider = channel.Provider
	}
	billingContext, contextErr := cap.BillingContext(scope)
	published, formula := 0, ""
	formulaMixed := false
	for _, cell := range cap.Cells() {
		combination := CatalogCombination{Key: cell.Key(), Mode: cell.Mode, Resolution: cell.Resolution, InputVideo: cell.InputVideo, ServiceTier: cell.ServiceTier, Draft: cell.Draft, Status: "blocked", ReasonCodes: catalogReasons(base...)}
		if cfg.PricingVersion != "" {
			v := cfg.PricingVersion
			combination.PricingVersion = &v
		}
		if len(base) == 0 {
			if contextErr != nil {
				combination.ReasonCodes = []string{"pricing_invalid"}
			} else {
				result, err := tariffs.Evaluate(videobilling.PricingKey{ProviderScopeID: cfg.ProviderScopeID, Model: cfg.PublicModel, Mode: cell.Mode, Resolution: cell.Resolution, InputVideo: cell.InputVideo, ServiceTier: cell.ServiceTier, Draft: cell.Draft}, billingContext)
				if err != nil {
					return CatalogModel{}, err
				}
				for _, reason := range result.Reasons {
					combination.ReasonCodes = append(combination.ReasonCodes, string(reason))
				}
				combination.ReasonCodes = catalogReasons(combination.ReasonCodes...)
				if result.Price != nil {
					if !ratio.IsPositive() {
						return CatalogModel{}, ErrCatalogUnavailable
					}
					price := result.Price
					combination.Status = "published"
					pv, fv, mv := price.PricingVersion, price.FormulaVersion, price.MinimumRuleVersion
					combination.PricingVersion, combination.BillingRuleVersion, combination.MinimumRuleVersion = &pv, &fv, &mv
					inputLabel := "without_input_video"
					if cell.InputVideo {
						inputLabel = "with_input_video"
					}
					row.Prices = append(row.Prices, CatalogPrice{PriceRuleID: price.RuleID, Label: cell.Resolution + "/" + inputLabel + "/" + cell.ServiceTier, Mode: cell.Mode, Resolution: cell.Resolution, InputVideo: cell.InputVideo, ServiceTier: cell.ServiceTier, Draft: cell.Draft, Unit: "million_video_tokens", Amount: price.RetailUSDPer1M.Mul(ratio).String(), Currency: "USD", MinimumRuleVersion: mv})
					if published > 0 && formula != fv {
						formulaMixed = true
					}
					formula = fv
					published++
				}
			}
		}
		row.Publication.Combinations = append(row.Publication.Combinations, combination)
		row.Publication.ReasonCodes = catalogReasons(append(row.Publication.ReasonCodes, combination.ReasonCodes...)...)
	}
	if published > 0 {
		row.Publication.Status = "partial"
		if published == len(row.Publication.Combinations) {
			row.Publication.Status = "published"
		}
		if !formulaMixed {
			row.BillingRuleVersion = formula
		}
	}
	return row, nil
}

func catalogActions(subject identity.RoutingCandidateSubject, policy Policy, policyExists, published, credential bool) CatalogActions {
	scope := func(expected string) bool { return hasScope(&identity.APIKey{Scopes: subject.KeyScopes}, expected) }
	base := []string{}
	if !published {
		base = append(base, "publication_unavailable")
	}
	action := func(expected string, extras ...string) CatalogAction {
		reasons := append(append([]string{}, base...), extras...)
		if !scope(expected) {
			reasons = append(reasons, "scope_missing")
		}
		reasons = catalogReasons(reasons...)
		return CatalogAction{Eligible: len(reasons) == 0, ReasonCodes: reasons}
	}
	extra := []string{}
	if !subject.FeatureVideo {
		extra = append(extra, "video_feature_disabled")
	}
	if !policyExists || !policy.AsyncExecutionAllowed {
		extra = append(extra, "video_execution_disabled")
	}
	if policyExists && (policy.RetentionDays < 1 || policy.RetentionDays > 30) {
		extra = append(extra, "video_execution_policy_invalid")
	}
	if published && !credential {
		extra = append(extra, "credential_unavailable")
	}
	return CatalogActions{ModelsRead: action("video:read"), Estimate: action("video:write"), Create: action("video:write", extra...)}
}
