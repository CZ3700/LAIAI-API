package videobilling

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type catalogTariffContextError struct{ cause error }

func (e *catalogTariffContextError) Error() string      { return ErrCatalogTariffUnavailable.Error() }
func (e *catalogTariffContextError) Unwrap() error      { return e.cause }
func (*catalogTariffContextError) Is(target error) bool { return target == ErrCatalogTariffUnavailable }
func catalogTariffFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &catalogTariffContextError{context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &catalogTariffContextError{context.DeadlineExceeded}
	}
	for _, e := range []error{ErrCatalogTariffInvalid, ErrCatalogTariffLimit, ErrCatalogTariffUnavailable} {
		if errors.Is(err, e) {
			return e
		}
	}
	return ErrCatalogTariffUnavailable
}
func catalogTariffTx(ctx context.Context, tx *gorm.DB) error {
	if ctx == nil || tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.DryRun || requireTx(tx) != nil {
		return ErrCatalogTariffInvalid
	}
	if n := tx.Dialector.Name(); n != "mysql" && n != "sqlite" {
		return ErrCatalogTariffInvalid
	}
	if err := ctx.Err(); err != nil {
		return catalogTariffFailure(err)
	}
	return catalogTariffFailure(tx.Error)
}

func catalogTariffSelect(ctx context.Context, tx *gorm.DB, execute func(*gorm.DB) error) error {
	return catalogTariffSelectColumns(ctx, tx, "*", execute)
}

// columns is a package-owned projection, never request data.
func catalogTariffSelectColumns(ctx context.Context, tx *gorm.DB, columns string, execute func(*gorm.DB) error) error {
	child, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := child.Err(); err != nil {
		return catalogTariffFailure(err)
	}
	if tx.Dialector.Name() == "mysql" {
		deadline, _ := child.Deadline()
		ms := time.Until(deadline).Milliseconds()
		if ms < 1 {
			return catalogTariffFailure(context.DeadlineExceeded)
		}
		columns = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", min(ms, int64(200)), columns)
	}
	q := tx.Session(&gorm.Session{NewDB: true, Context: child, Logger: logger.Discard}).Select(columns)
	err := execute(q)
	if contextErr := child.Err(); contextErr != nil {
		return catalogTariffFailure(contextErr)
	}
	return catalogTariffFailure(err)
}

// ReadCatalogTariffsTx uses at most three bounded reads in the caller's
// transaction. Effective rules of every version remain visible to detect overlap.
func ReadCatalogTariffsTx(ctx context.Context, tx *gorm.DB, refs []CatalogTariffRef, at time.Time) (CatalogTariffSnapshot, error) {
	if err := catalogTariffTx(ctx, tx); err != nil {
		return CatalogTariffSnapshot{}, err
	}
	if at.IsZero() || len(refs) > 4 {
		return CatalogTariffSnapshot{}, ErrCatalogTariffInvalid
	}
	for i, r := range refs {
		if r.ProviderScopeID == 0 || !ark.IsPublicModel(r.Model) || len(r.PricingVersion) > 64 {
			return CatalogTariffSnapshot{}, ErrCatalogTariffInvalid
		}
		if i > 0 && (refs[i-1].ProviderScopeID > r.ProviderScopeID || (refs[i-1].ProviderScopeID == r.ProviderScopeID && refs[i-1].Model >= r.Model)) {
			return CatalogTariffSnapshot{}, ErrCatalogTariffInvalid
		}
	}
	s := CatalogTariffSnapshot{ctx: ctx, at: at.UTC(), refs: append([]CatalogTariffRef(nil), refs...)}
	if len(refs) == 0 {
		return s, nil
	}
	pairs := make([]clause.Expression, 0, len(refs))
	for _, r := range refs {
		pairs = append(pairs, clause.Expr{SQL: "(provider_scope_id = ? AND model = ?)", Vars: []any{r.ProviderScopeID, r.Model}})
	}
	err := catalogTariffSelect(ctx, tx, func(q *gorm.DB) error {
		active := "enabled = ? AND effective_from <= ? AND (effective_until IS NULL OR effective_until > ?)"
		if tx.Dialector.Name() == "sqlite" {
			// SQLite stores driver timestamps as text, possibly with different UTC
			// offsets. Compare instants, not their lexical local wall times. Its
			// millisecond precision may round equal instants together; include the
			// until boundary here and enforce the exact half-open interval below.
			active = "enabled = ? AND julianday(effective_from) <= julianday(?) AND (effective_until IS NULL OR julianday(effective_until) >= julianday(?))"
		}
		return q.Where(clause.Or(pairs...)).Where(active, true, s.at, s.at).Order("id").Limit(257).Find(&s.rules).Error
	})
	if err != nil {
		return CatalogTariffSnapshot{}, err
	}
	if len(s.rules) > 256 {
		return CatalogTariffSnapshot{}, ErrCatalogTariffLimit
	}
	effective := s.rules[:0]
	for _, r := range s.rules {
		matched := false
		for _, ref := range refs {
			if r.ProviderScopeID == ref.ProviderScopeID && r.Model == ref.Model {
				matched = true
				break
			}
		}
		if !matched {
			return CatalogTariffSnapshot{}, ErrCatalogTariffUnavailable
		}
		if !r.Enabled || r.EffectiveFrom.After(s.at) || (r.EffectiveUntil != nil && !r.EffectiveUntil.After(s.at)) {
			continue
		}
		effective = append(effective, r)
	}
	// Only truly inactive intervals are removed; every effective version remains
	// in the snapshot so overlap still blocks instead of selecting a version.
	s.rules = effective
	s.sets, s.cells, err = readCatalogMinimums(ctx, tx, s.rules)
	if err != nil {
		return CatalogTariffSnapshot{}, err
	}
	if err = ctx.Err(); err != nil {
		return CatalogTariffSnapshot{}, catalogTariffFailure(err)
	}
	return s, nil
}

func readCatalogMinimums(ctx context.Context, tx *gorm.DB, rules []PricingRule) ([]MinimumRuleSet, []MinimumRuleCell, error) {
	type minimumRef struct{ model, version string }
	wanted := map[minimumRef]bool{}
	for _, r := range rules {
		if r.InputVideo && validID(r.MinimumRuleVersion) {
			wanted[minimumRef{r.Model, r.MinimumRuleVersion}] = true
		}
	}
	if len(wanted) == 0 {
		return nil, nil, nil
	}
	keys := make([]minimumRef, 0, len(wanted))
	for k := range wanted {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		return keys[i].version < keys[j].version
	})
	pairs := make([]clause.Expression, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, clause.Expr{SQL: "(model = ? AND version = ?)", Vars: []any{k.model, k.version}})
	}
	var sets []MinimumRuleSet
	if err := catalogTariffSelect(ctx, tx, func(q *gorm.DB) error { return q.Where(clause.Or(pairs...)).Order("id").Limit(17).Find(&sets).Error }); err != nil {
		return nil, nil, err
	}
	if len(sets) > 16 {
		return nil, nil, ErrCatalogTariffLimit
	}
	ids := make([]uint64, 0, len(sets))
	idSet := map[uint64]bool{}
	for _, s := range sets {
		if s.ID == 0 || !wanted[minimumRef{s.Model, s.Version}] || idSet[s.ID] {
			return nil, nil, ErrCatalogTariffUnavailable
		}
		ids = append(ids, s.ID)
		idSet[s.ID] = true
	}
	if len(ids) == 0 {
		return sets, nil, nil
	}
	const columns = "id, rule_set_id, resolution, ratio, output_duration_ms, fps, minimum_tokens"
	// Aggregate only after the same bounded selection. Do not filter malformed
	// business facts here: the shared minimum-rule validation must still see them.
	aggregation := "COUNT(*) AS cell_count, JSON_ARRAYAGG(JSON_ARRAY(" + columns + ")) AS cells_json"
	if tx.Dialector.Name() == "sqlite" {
		aggregation = "COUNT(*) AS cell_count, json_group_array(json_array(" + columns + ")) AS cells_json"
	}
	var aggregate catalogMinimumAggregate
	if err := catalogTariffSelectColumns(ctx, tx, aggregation, func(q *gorm.DB) error {
		// A fixed derived-table expression avoids executing GORM's query
		// callbacks merely to render a nested DryRun query. IDs remain bound
		// parameters; this is one actual SELECT and one observation callback.
		q = q.Table("(SELECT "+columns+" FROM video_minimum_rule_cells WHERE rule_set_id IN ? ORDER BY id LIMIT 32001) AS bounded_cells", ids)
		rows, err := q.Rows()
		q.RowsAffected = 0
		if err != nil {
			// An after-query callback may report failure after the driver opened
			// rows. Close that result as well, inside the same child deadline.
			if rows != nil {
				_ = rows.Close()
			}
			return err
		}
		if rows == nil {
			return ErrCatalogTariffUnavailable
		}
		q.RowsAffected, err = scanCatalogMinimumAggregate(q.Statement.Context, rows, &aggregate)
		// Rows() has already obtained its result and the scanner has closed it.
		// Keep the actual private result for post-completion diagnostics only.
		q.Statement.Dest = &aggregate
		return err
	}); err != nil {
		return nil, nil, err
	}
	if tx.Dialector.Name() == "sqlite" && aggregate.CellsJSON == nil {
		// SQLite's aggregate has the explicit [] empty representation. A NULL
		// result must not inherit MySQL's otherwise valid count=0 convention.
		if err := ctx.Err(); err != nil {
			return nil, nil, catalogTariffFailure(err)
		}
		return nil, nil, ErrCatalogTariffUnavailable
	}
	// Only decoding and domain validation run under the parent budget; the full
	// database read, including Close, has completed inside the SQL child budget.
	cells, err := decodeCatalogMinimumAggregate(ctx, &aggregate)
	if err != nil {
		return nil, nil, catalogTariffFailure(err)
	}
	if err := validateCatalogMinimumCells(cells, idSet); err != nil {
		return nil, nil, err
	}
	return sets, cells, nil
}

func validateCatalogMinimumCells(cells []MinimumRuleCell, idSet map[uint64]bool) error {
	if len(cells) > 32000 {
		return ErrCatalogTariffLimit
	}
	for _, c := range cells {
		if !idSet[c.RuleSetID] {
			return ErrCatalogTariffUnavailable
		}
	}
	return nil
}

// ValidatePublishedRuleTx validates the already selected exact rule. It does
// not select a replacement, reserve funds or alter the caller's transaction.
func ValidatePublishedRuleTx(ctx context.Context, tx *gorm.DB, rule PricingRule, expectedKey PricingKey, expectedVersion string, p PublicationContext) error {
	if err := catalogTariffTx(ctx, tx); err != nil {
		return err
	}
	if !catalogKeyValid(expectedKey) {
		return ErrCatalogTariffInvalid
	}
	// Invalid identities/rates need no evidence reads; preserve the same reason
	// precedence as Evaluate without treating missing floors as infrastructure errors.
	pre := evaluateCatalogRule(rule, expectedKey, expectedVersion, p, nil, nil)
	if err := ctx.Err(); err != nil {
		return catalogTariffFailure(err)
	}
	if pre.Price != nil {
		return nil
	}
	if len(pre.Reasons) != 1 || pre.Reasons[0] != CatalogTariffMinimumCoverageMissing {
		return &CatalogPublicationError{Reasons: pre.Reasons}
	}
	sets, cells, err := readCatalogMinimums(ctx, tx, []PricingRule{rule})
	if err != nil {
		return err
	}
	out := evaluateCatalogRule(rule, expectedKey, expectedVersion, p, sets, cells)
	if err := ctx.Err(); err != nil {
		return catalogTariffFailure(err)
	}
	if out.Price == nil {
		return &CatalogPublicationError{Reasons: out.Reasons}
	}
	return nil
}
