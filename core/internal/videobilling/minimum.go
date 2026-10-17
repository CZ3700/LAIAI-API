package videobilling

import (
	"encoding/hex"
	"errors"
	"net/url"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// MinimumRuleSet records operator-verified official evidence, never a guessed floor.
type MinimumRuleSet struct {
	ID           uint64    `gorm:"primaryKey" json:"id"`
	Model        string    `json:"model"`
	Version      string    `json:"version"`
	SourceURL    string    `json:"source_url"`
	EvidenceHash string    `json:"evidence_hash"`
	ObservedAt   time.Time `json:"observed_at"`
	VerifiedBy   uint64    `json:"verified_by"`
	VerifiedAt   time.Time `json:"verified_at"`
}

func (MinimumRuleSet) TableName() string { return "video_minimum_rule_sets" }

type MinimumRuleCell struct {
	ID               uint64 `gorm:"primaryKey" json:"id"`
	RuleSetID        uint64 `json:"rule_set_id"`
	Resolution       string `json:"resolution"`
	Ratio            string `json:"ratio"`
	OutputDurationMS int64  `json:"output_duration_ms"`
	FPS              int64  `json:"fps"`
	MinimumTokens    int64  `json:"minimum_tokens"`
}

func (MinimumRuleCell) TableName() string { return "video_minimum_rule_cells" }

// MinimumContext lists the verified domain when duration or ratio is adaptive.
// No interpolation or monotonicity assumption is made across official cells.
type MinimumContext struct {
	Resolution, Ratio        string
	Ratios                   []string
	MinDuration, MaxDuration int
	AutomaticDuration        bool
}

// Keep the domain private and owned by the quote so a later reserve cannot
// accidentally replace automatic/adaptive coverage with just its upper bound.
type minimumSelection struct {
	context          MinimumContext
	outputDurationMS int64
	fps              int64
}

func validMinimumSource(s MinimumRuleSet) bool {
	u, e := url.Parse(s.SourceURL)
	h, he := hex.DecodeString(s.EvidenceHash)
	return (s.Model == "seedance-2.0" || s.Model == "seedance-2.5") && validID(s.Version) && len(s.SourceURL) <= 1024 && e == nil && u.Scheme == "https" && u.User == nil && u.Fragment == "" && (u.Host == "docs.volcengine.com" || u.Host == "ark.volcengine.com" || u.Host == "console.volcengine.com" || u.Host == "bytedance.larkoffice.com") && he == nil && len(h) == 32 && s.VerifiedBy != 0 && !s.ObservedAt.IsZero() && !s.VerifiedAt.Before(s.ObservedAt)
}

// PublishMinimumTx is append-only. An existing version cannot acquire new cells.
func PublishMinimumTx(tx *gorm.DB, set MinimumRuleSet, cells []MinimumRuleCell) (MinimumRuleSet, error) {
	if requireTx(tx) != nil || set.ID != 0 || !validMinimumSource(set) || len(cells) == 0 || len(cells) > 2000 {
		return set, ErrInvalid
	}
	seen := map[MinimumRuleCell]bool{}
	for _, c := range cells {
		if c.ID != 0 || c.RuleSetID != 0 || c.OutputDurationMS < 4000 || c.OutputDurationMS > 30000 || c.OutputDurationMS%1000 != 0 || c.FPS < 1 || c.FPS > 120 || c.MinimumTokens <= 0 || !oneOf(c.Resolution, "480p", "720p", "1080p", "4k") || !oneOf(c.Ratio, "16:9", "4:3", "1:1", "3:4", "9:16", "21:9") || (set.Model == "seedance-2.0" && c.OutputDurationMS > 15000) || (set.Model == "seedance-2.5" && c.Resolution == "4k") {
			return set, ErrInvalid
		}
		key := c
		key.MinimumTokens = 0
		if seen[key] {
			return set, ErrConflict
		}
		seen[key] = true
	}
	var prior MinimumRuleSet
	err := tx.Where("model = ? AND version = ?", set.Model, set.Version).Take(&prior).Error
	if err == nil {
		if prior.SourceURL != set.SourceURL || prior.EvidenceHash != set.EvidenceHash || !prior.ObservedAt.Equal(set.ObservedAt) || prior.VerifiedBy != set.VerifiedBy {
			return set, ErrConflict
		}
		var rows []MinimumRuleCell
		if e := tx.Where("rule_set_id = ?", prior.ID).Find(&rows).Error; e != nil {
			return set, e
		}
		if len(rows) != len(cells) {
			return set, ErrConflict
		}
		values := map[MinimumRuleCell]bool{}
		for _, c := range cells {
			values[c] = true
		}
		for _, c := range rows {
			c.ID = 0
			c.RuleSetID = 0
			if !values[c] {
				return set, ErrConflict
			}
		}
		return prior, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return set, err
	}
	if err := tx.Create(&set).Error; err != nil {
		return set, err
	}
	cells = append([]MinimumRuleCell(nil), cells...)
	for i := range cells {
		cells[i].RuleSetID = set.ID
	}
	return set, tx.Create(&cells).Error
}

// ValidateMinimumCoverageTx is shared by every price publication entry point.
// It checks the whole admitted domain, irrespective of scalar MinimumTokens.
func ValidateMinimumCoverageTx(tx *gorm.DB, rule PricingRule, ctx MinimumContext, fps int64) error {
	if err := requireTx(tx); err != nil {
		return err
	}
	if !rule.InputVideo {
		return nil
	}
	ctx.Ratio, ctx.AutomaticDuration = "adaptive", true
	// Quote validates the selected evidence; these unit bounds are only a
	// validation probe and are never published, reserved, or returned as prices.
	in := MeteringInput{InputVideo: true, InputDurationMS: 1, OutputDurationMS: int64(ctx.MaxDuration) * 1000,
		Width: 1, Height: 1, FPSNumerator: fps, FPSDenominator: 1, BoundsVerified: true,
		CapabilityVersion: "coverage-validation", EffectiveRatio: decimal.NewFromInt(1),
		FXUSDPerCNY: decimal.NewFromInt(1), FXVersion: "coverage-validation", FXObservedAt: time.Unix(1, 0)}
	_, err := QuoteWithMinimumTx(tx, rule, in, ctx)
	return err
}

func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}

// QuoteWithMinimumTx resolves only verified cells and records their provenance
// inside the immutable metering snapshot. Actual settlement still uses usage.
func QuoteWithMinimumTx(tx *gorm.DB, rule PricingRule, in MeteringInput, ctx MinimumContext) (QuoteResult, error) {
	if err := requireTx(tx); err != nil {
		return QuoteResult{}, err
	}
	in.Resolution, in.Ratio, in.Mode, in.ServiceTier, in.Draft = rule.Resolution, ctx.Ratio, rule.Mode, rule.ServiceTier, rule.Draft
	if !rule.InputVideo {
		rule.MinimumTokens = 0
		in.MinimumRuleSetID, in.MinimumTokens, in.MinimumEvidenceHash = 0, 0, ""
		return Quote(rule, in)
	}
	if ctx.Resolution != rule.Resolution || in.OutputFrames != 0 || in.FPSDenominator != 1 {
		return QuoteResult{}, ErrPricingUnavailable
	}
	var set MinimumRuleSet
	if err := tx.Where("model = ? AND version = ?", rule.Model, rule.MinimumRuleVersion).Take(&set).Error; err != nil {
		return QuoteResult{}, ErrPricingUnavailable
	}
	if !validMinimumSource(set) {
		return QuoteResult{}, ErrPricingUnavailable
	}
	ratios := []string{ctx.Ratio}
	if ctx.Ratio == "adaptive" {
		ratios = nil
		for _, r := range ctx.Ratios {
			if r != "adaptive" {
				ratios = append(ratios, r)
			}
		}
	}
	if len(ratios) == 0 || len(ratios) > 6 {
		return QuoteResult{}, ErrPricingUnavailable
	}
	durations := []int64{in.OutputDurationMS}
	if ctx.AutomaticDuration {
		if ctx.MinDuration < 4 || ctx.MaxDuration < ctx.MinDuration || ctx.MaxDuration > 30 || in.OutputDurationMS != int64(ctx.MaxDuration)*1000 {
			return QuoteResult{}, ErrPricingUnavailable
		}
		durations = nil
		for d := ctx.MinDuration; d <= ctx.MaxDuration; d++ {
			durations = append(durations, int64(d)*1000)
		}
	}
	var rows []MinimumRuleCell
	if err := tx.Where("rule_set_id = ? AND resolution = ? AND ratio IN ? AND output_duration_ms IN ? AND fps = ?", set.ID, rule.Resolution, ratios, durations, in.FPSNumerator).Find(&rows).Error; err != nil {
		return QuoteResult{}, err
	}
	if len(rows) != len(ratios)*len(durations) {
		return QuoteResult{}, ErrPricingUnavailable
	}
	rule.MinimumTokens = 0
	for _, c := range rows {
		if c.MinimumTokens <= 0 {
			return QuoteResult{}, ErrPricingUnavailable
		}
		if c.MinimumTokens > rule.MinimumTokens {
			rule.MinimumTokens = c.MinimumTokens
		}
	}
	in.MinimumRuleSetID, in.MinimumTokens, in.MinimumEvidenceHash = set.ID, rule.MinimumTokens, set.EvidenceHash
	q, err := Quote(rule, in)
	if err != nil {
		return QuoteResult{}, err
	}
	ctx.Ratios = append([]string(nil), ctx.Ratios...)
	q.minimumSelection = &minimumSelection{context: ctx, outputDurationMS: in.OutputDurationMS, fps: in.FPSNumerator}
	return q, nil
}

// Re-query trusted evidence inside the reservation transaction. Neither the
// quoted scalar minimum nor its exposed provenance is sufficient on its own.
func requoteReservationTx(tx *gorm.DB, rule PricingRule, quoted QuoteResult) (QuoteResult, error) {
	in := quoted.Metering
	if !rule.InputVideo {
		if in.MinimumRuleSetID != 0 || in.MinimumTokens != 0 || in.MinimumEvidenceHash != "" {
			return QuoteResult{}, ErrConflict
		}
		// Match QuoteWithMinimumTx: input-video floors never apply to text-only
		// requests, including legacy rules with a nonzero scalar minimum.
		rule.MinimumTokens = 0
		return Quote(rule, in)
	}
	selection := quoted.minimumSelection
	if selection == nil {
		return QuoteResult{}, ErrPricingUnavailable
	}
	if rule.MinimumRuleVersion != quoted.Rule.MinimumRuleVersion || in.Resolution != selection.context.Resolution || in.Ratio != selection.context.Ratio || in.Mode != rule.Mode || in.ServiceTier != rule.ServiceTier || in.Draft != rule.Draft || in.OutputDurationMS != selection.outputDurationMS || in.OutputFrames != 0 || in.FPSNumerator != selection.fps || in.FPSDenominator != 1 {
		return QuoteResult{}, ErrConflict
	}
	q, err := QuoteWithMinimumTx(tx, rule, in, selection.context)
	if err != nil {
		return QuoteResult{}, err
	}
	if q.Metering.MinimumRuleSetID != in.MinimumRuleSetID || q.Metering.MinimumTokens != in.MinimumTokens || q.Metering.MinimumEvidenceHash != in.MinimumEvidenceHash {
		return QuoteResult{}, ErrConflict
	}
	return q, nil
}
