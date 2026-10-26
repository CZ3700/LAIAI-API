package videobilling

// CatalogMinimumTokens validates a preloaded read snapshot without querying for
// each model/card. It never authorizes a reservation; QuoteWithMinimumTx remains
// authoritative inside each create transaction.
func CatalogMinimumTokens(rule PricingRule, sets []MinimumRuleSet, cells []MinimumRuleCell, ctx MinimumContext, fps int64) (int64, error) {
	value, reason := catalogMinimumTokens(rule, sets, cells, ctx, fps)
	if reason != "" {
		return 0, ErrPricingUnavailable
	}
	return value, nil
}

func catalogMinimumTokens(rule PricingRule, sets []MinimumRuleSet, cells []MinimumRuleCell, ctx MinimumContext, fps int64) (int64, CatalogTariffReason) {
	if !rule.InputVideo {
		return 0, ""
	}
	var selected *MinimumRuleSet
	for i := range sets {
		m := &sets[i]
		if m.Model == rule.Model && m.Version == rule.MinimumRuleVersion {
			if selected != nil {
				return 0, CatalogTariffMinimumRuleInvalid
			}
			selected = m
		}
	}
	if selected == nil {
		return 0, CatalogTariffMinimumCoverageMissing
	}
	if !validMinimumSource(*selected) || ctx.MinDuration < 4 || ctx.MaxDuration < ctx.MinDuration || ctx.MaxDuration > 30 {
		return 0, CatalogTariffMinimumRuleInvalid
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
		return 0, CatalogTariffMinimumRuleInvalid
	}
	seen := map[key]bool{}
	var maximum int64
	for _, c := range cells {
		k := key{c.Ratio, c.OutputDurationMS}
		if c.RuleSetID != selected.ID || c.Resolution != rule.Resolution || c.FPS != fps || !wanted[k] {
			continue
		}
		if seen[k] || c.MinimumTokens <= 0 {
			return 0, CatalogTariffMinimumRuleInvalid
		}
		seen[k] = true
		if c.MinimumTokens > maximum {
			maximum = c.MinimumTokens
		}
	}
	if len(seen) != len(wanted) {
		return 0, CatalogTariffMinimumCoverageMissing
	}
	return maximum, ""
}
