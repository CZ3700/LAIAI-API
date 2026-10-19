package videobilling

// CatalogMinimumTokens validates a preloaded read snapshot without querying for
// each model/card. It never authorizes a reservation; QuoteWithMinimumTx remains
// authoritative inside each create transaction.
func CatalogMinimumTokens(rule PricingRule, sets []MinimumRuleSet, cells []MinimumRuleCell, ctx MinimumContext, fps int64) (int64, error) {
	if !rule.InputVideo {
		return 0, nil
	}
	var selected *MinimumRuleSet
	for i := range sets {
		m := &sets[i]
		if m.Model == rule.Model && m.Version == rule.MinimumRuleVersion {
			if selected != nil {
				return 0, ErrPricingUnavailable
			}
			selected = m
		}
	}
	if selected == nil || !validMinimumSource(*selected) || ctx.MinDuration < 4 || ctx.MaxDuration < ctx.MinDuration || ctx.MaxDuration > 30 {
		return 0, ErrPricingUnavailable
	}
	type key struct {
		ratio    string
		duration int64
	}
	wanted := map[key]bool{}
	for _, ratio := range ctx.Ratios {
		if ratio == "adaptive" {
			continue
		}
		for d := ctx.MinDuration; d <= ctx.MaxDuration; d++ {
			wanted[key{ratio, int64(d) * 1000}] = true
		}
	}
	if len(wanted) == 0 {
		return 0, ErrPricingUnavailable
	}
	seen := map[key]bool{}
	var maximum int64
	for _, c := range cells {
		k := key{c.Ratio, c.OutputDurationMS}
		if c.RuleSetID != selected.ID || c.Resolution != rule.Resolution || c.FPS != fps || !wanted[k] {
			continue
		}
		if seen[k] || c.MinimumTokens <= 0 {
			return 0, ErrPricingUnavailable
		}
		seen[k] = true
		if c.MinimumTokens > maximum {
			maximum = c.MinimumTokens
		}
	}
	if len(seen) != len(wanted) {
		return 0, ErrPricingUnavailable
	}
	return maximum, nil
}
