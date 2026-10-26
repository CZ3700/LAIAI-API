package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// These tests use tracked migrations in an owned, random ark_test schema. No
// altered schema, synthetic query results, official credentials or HTTP client
// are involved. Setup writes are outside the production reader's time budget.
var catalogTariffsMySQLAt = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

func catalogTariffsMySQLRule(n int) videobilling.PricingRule {
	return videobilling.PricingRule{
		RuleID: fmt.Sprintf("tariff-mysql-%04d", n), Version: "price-v1",
		PricingKey:     videobilling.PricingKey{ProviderScopeID: 991001, Model: "seedance-2.0", Mode: "generate", Resolution: "720p", ServiceTier: "default"},
		RetailUSDPer1M: decimal.RequireFromString("1.234567890123"), SupplierCNYPer1M: decimal.NewFromInt(8),
		RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "formula-v1", SourceHash: strings.Repeat("a", 64),
		EffectiveFrom: catalogTariffsMySQLAt.Add(-time.Hour), Enabled: true,
	}
}

func catalogTariffsMySQLContext() videobilling.PublicationContext {
	return videobilling.PublicationContext{CapabilityVersion: "cap-v1", Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: 4, FPS: 24,
		Bounds:                  []videobilling.PublicationBound{{Resolution: "720p", Ratio: "16:9", Width: 1280, Height: 720}},
		MaxInputVideoDurationMS: 15000, FXUSDPerCNY: decimal.RequireFromString("0.14"), FXVersion: "fx-v1", FXObservedAt: catalogTariffsMySQLAt}
}

func catalogTariffsMySQLRef() []videobilling.CatalogTariffRef {
	return []videobilling.CatalogTariffRef{{ProviderScopeID: 991001, Model: "seedance-2.0", PricingVersion: "price-v1"}}
}

func catalogTariffsMySQLInsertRules(t *testing.T, db *gorm.DB, rules []videobilling.PricingRule) {
	t.Helper()
	if err := db.CreateInBatches(&rules, 64).Error; err != nil {
		t.Fatal("cannot insert synthetic pricing rules into tracked schema")
	}
}

func catalogTariffsMySQLCount(t *testing.T, db *gorm.DB, table string, want int64) {
	t.Helper()
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil || n != want {
		t.Fatalf("synthetic %s row count=%d want=%d", table, n, want)
	}
}

type catalogTariffsMySQLStatement struct {
	sql           string
	args          []any
	rows          int64
	cells         int64
	decoded       int64
	executed      *gorm.DB
	openedNS      int64
	openedError   string
	openedContext string
}

// Failure-only test diagnostics. No SQL text/Vars or JSON payload is logged.
func catalogTariffsDiagnosticError(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, videobilling.ErrCatalogTariffLimit) {
		return "limit"
	}
	if errors.Is(err, videobilling.ErrCatalogTariffInvalid) {
		return "invalid"
	}
	if errors.Is(err, videobilling.ErrCatalogTariffUnavailable) {
		return "unavailable"
	}
	return "other"
}

type catalogTariffsDiagnosticStatement struct {
	Ordinal       int    `json:"ordinal"`
	Kind          string `json:"kind"`
	OpenedNS      int64  `json:"opened_ns"`
	OpenedError   string `json:"opened_error"`
	OpenedContext string `json:"opened_context"`
	PhysicalRows  int64  `json:"physical_rows"`
	DestAvailable bool   `json:"dest_available"`
	CountValid    bool   `json:"count_valid"`
	CellCount     int64  `json:"cell_count"`
	DecodedCount  int64  `json:"decoded_count"`
	JSONBytes     int    `json:"json_bytes"`
}

func catalogTariffsDiagnosticStatementFacts(i int, s catalogTariffsMySQLStatement) catalogTariffsDiagnosticStatement {
	out := catalogTariffsDiagnosticStatement{Ordinal: i + 1, Kind: "query", OpenedNS: s.openedNS, OpenedError: s.openedError, OpenedContext: s.openedContext, PhysicalRows: s.rows}
	if !strings.Contains(s.sql, "AS bounded_cells") {
		return out
	}
	out.Kind = "aggregate_row"
	if s.executed == nil || s.executed.Statement == nil {
		return out
	}
	v := reflect.ValueOf(s.executed.Statement.Dest)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return out
	}
	v = v.Elem()
	count, decoded, body := v.FieldByName("CellCount"), v.FieldByName("DecodedCount"), v.FieldByName("CellsJSON")
	if !count.IsValid() || !count.CanInterface() || !decoded.IsValid() || decoded.Kind() != reflect.Int || !body.IsValid() || body.Kind() != reflect.Slice || body.Type().Elem().Kind() != reflect.Uint8 {
		return out
	}
	n, ok := count.Interface().(sql.NullInt64)
	if !ok {
		return out
	}
	out.DestAvailable = true
	out.CountValid = n.Valid
	out.CellCount = n.Int64
	out.DecodedCount = decoded.Int()
	out.JSONBytes = body.Len()
	return out
}

// The private scanner result is inspected only after rows are closed. Its
// public-valued fields are actual facts, not an additional business getter.
func catalogTariffsAggregateFacts(t *testing.T, q *gorm.DB) (int64, int64, []byte) {
	t.Helper()
	v := reflect.ValueOf(q.Statement.Dest)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		t.Fatal("aggregate did not retain its completed private scan result")
	}
	v = v.Elem()
	count, decoded, body := v.FieldByName("CellCount"), v.FieldByName("DecodedCount"), v.FieldByName("CellsJSON")
	if !count.IsValid() || !decoded.IsValid() || !body.IsValid() || !count.CanInterface() || decoded.Kind() != reflect.Int || body.Kind() != reflect.Slice {
		t.Fatal("aggregate observation fields missing")
	}
	n, ok := count.Interface().(sql.NullInt64)
	if !ok || !n.Valid {
		t.Fatal("aggregate returned no exact COUNT")
	}
	return n.Int64, decoded.Int(), body.Bytes() // read-only test observation
}

// Capture actual GORM SELECTs/Vars/returned rows, then EXPLAIN those statements
// after the transaction ends. Plans are evidence, not a forced-index oracle.
func catalogTariffsMySQLRead(t *testing.T, db *gorm.DB, refs []videobilling.CatalogTariffRef, wantRows []int64, wantErr error, check func(videobilling.CatalogTariffSnapshot)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		t.Fatal("cannot begin read-only tariff snapshot")
	}
	defer tx.Rollback()
	var statements []catalogTariffsMySQLStatement
	var started time.Time
	const callback = "catalog_tariffs_mysql_actual_selects"
	observe := func(q *gorm.DB) {
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("tariff reader escaped caller transaction")
		}
		// Row callbacks run when sql.Rows opens. Retain the executed handle so
		// its actual scan count is inspected after the reader closes the rows.
		statements = append(statements, catalogTariffsMySQLStatement{sql: q.Statement.SQL.String(), args: append([]any(nil), q.Statement.Vars...), executed: q, openedNS: time.Since(started).Nanoseconds(), openedError: catalogTariffsDiagnosticError(q.Error), openedContext: catalogTariffsDiagnosticError(q.Statement.Context.Err())})
	}
	if err := db.Callback().Query().After("gorm:query").Register(callback, observe); err != nil {
		t.Fatal("cannot register actual tariff SELECT observer")
	}
	defer db.Callback().Query().Remove(callback)
	if err := db.Callback().Row().After("gorm:row").Register(callback, observe); err != nil {
		t.Fatal("cannot register actual tariff Row observer")
	}
	defer db.Callback().Row().Remove(callback)
	started = time.Now()
	snapshot, err := videobilling.ReadCatalogTariffsTx(ctx, tx, refs, catalogTariffsMySQLAt)
	elapsed := time.Since(started)
	for i := range statements {
		statements[i].rows = statements[i].executed.RowsAffected
	}

	if (wantErr == nil && err != nil) || (wantErr != nil && !errors.Is(err, wantErr)) {
		// The original reader has returned; observations do not extend its deadline.
		// Rows callback timestamp is opened-only, not a complete scan/Close span.
		diagnostic := struct {
			Format          string                              `json:"format"`
			ExpectedCells   int64                               `json:"expected_cells"`
			ElapsedNS       int64                               `json:"elapsed_ns"`
			ParentError     string                              `json:"parent_error"`
			ReaderError     string                              `json:"reader_error"`
			ReaderDeadline  bool                                `json:"reader_deadline"`
			ReaderCancelled bool                                `json:"reader_cancelled"`
			Statements      []catalogTariffsDiagnosticStatement `json:"statements"`
		}{Format: "catalog-helper-observation-v1", ExpectedCells: 0, ElapsedNS: elapsed.Nanoseconds(), ParentError: catalogTariffsDiagnosticError(ctx.Err()), ReaderError: catalogTariffsDiagnosticError(err), ReaderDeadline: errors.Is(err, context.DeadlineExceeded), ReaderCancelled: errors.Is(err, context.Canceled)}
		if len(wantRows) == 3 {
			diagnostic.ExpectedCells = wantRows[2]
		}
		for i, s := range statements {
			diagnostic.Statements = append(diagnostic.Statements, catalogTariffsDiagnosticStatementFacts(i, s))
		}
		rawDiagnostic, marshalErr := json.Marshal(diagnostic)
		if marshalErr != nil {
			t.Fatal("cannot marshal bounded diagnostic primitives")
		}
		t.Logf("RC46_TARIFF_HELPER_OBSERVATION=%s", rawDiagnostic)
		t.Fatalf("tariff reader error=%v want=%v elapsed=%s", err, wantErr, elapsed)
	}
	if wantErr != nil && !reflect.DeepEqual(snapshot, videobilling.CatalogTariffSnapshot{}) {
		t.Fatal("failed bounded read leaked partial opaque snapshot")
	}
	for i := range statements {
		if strings.Contains(statements[i].sql, "AS bounded_cells") {
			statements[i].cells, statements[i].decoded, _ = catalogTariffsAggregateFacts(t, statements[i].executed)
		}
	}
	if len(statements) != len(wantRows) || len(statements) > 3 {
		t.Fatalf("actual tariff SELECTs=%d want=%d", len(statements), len(wantRows))
	}
	hint := regexp.MustCompile(`MAX_EXECUTION_TIME\(([0-9]+)\)`)
	for i, s := range statements {
		match := hint.FindStringSubmatch(s.sql)
		if len(match) != 2 {
			t.Fatal("actual tariff SELECT has no execution-time hint")
		}
		ms, e := strconv.Atoi(match[1])
		physicalWant := wantRows[i]
		if strings.Contains(s.sql, "AS bounded_cells") {
			physicalWant = 1
			decodedWant := wantRows[i]
			if wantRows[i] == 32001 {
				decodedWant = 0
			}
			if s.cells != wantRows[i] || s.decoded != decodedWant || !strings.Contains(s.sql, "JSON_ARRAYAGG(JSON_ARRAY(") || !strings.Contains(s.sql, "ORDER BY id LIMIT 32001") {
				t.Fatalf("aggregate COUNT=%d decoded=%d want=%d/%d", s.cells, s.decoded, wantRows[i], decodedWant)
			}
		}
		if e != nil || ms < 1 || ms > 200 || s.rows != physicalWant || strings.Contains(s.sql, "FOR UPDATE") {
			t.Fatalf("SELECT %d physical_rows=%d want=%d hint_ms=%d", i+1, s.rows, physicalWant, ms)
		}
	}
	if check != nil {
		check(snapshot)
	}
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal("cannot remove actual tariff SELECT observer")
	}
	if err := db.Callback().Row().Remove(callback); err != nil {
		t.Fatal("cannot remove actual tariff Row observer")
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal("cannot finish read-only tariff snapshot")
	}
	t.Logf("tariff snapshot SELECTs=%d expected_fact_counts=%v elapsed=%s", len(statements), wantRows, elapsed)
	for i, statement := range statements {
		t.Logf("tariff SELECT=%d physical_rows=%d cell_count=%d decoded=%d", i+1, statement.rows, statement.cells, statement.decoded)
		explainCtx, explainCancel := context.WithTimeout(context.Background(), time.Second)
		var plans []struct {
			Table string
			Type  string
			Key   sql.NullString
			Rows  uint64
		}
		err := db.WithContext(explainCtx).Raw("EXPLAIN "+statement.sql, statement.args...).Scan(&plans).Error
		explainCancel()
		if err != nil || len(plans) == 0 {
			t.Fatal("cannot explain captured tariff SELECT")
		}
		for _, plan := range plans {
			t.Logf("tariff SELECT=%d table=%s access=%s key=%s estimated_rows=%d", i+1, plan.Table, plan.Type, plan.Key.String, plan.Rows)
		}
	}
}

func catalogTariffsMySQLReason(t *testing.T, snapshot videobilling.CatalogTariffSnapshot, key videobilling.PricingKey, reason videobilling.CatalogTariffReason) {
	t.Helper()
	result, err := snapshot.Evaluate(key, catalogTariffsMySQLContext())
	if err != nil || result.Price != nil || !reflect.DeepEqual(result.Reasons, []videobilling.CatalogTariffReason{reason}) {
		t.Fatalf("catalog result reason=%v want=%s err=%v", result.Reasons, reason, err)
	}
}

func TestMySQLVideoCatalogTariffsRuleBoundaryAndPairIsolation(t *testing.T) {
	db := routingAdminMySQLDatabase(t)
	rules := make([]videobilling.PricingRule, 256)
	for i := range rules {
		rules[i] = catalogTariffsMySQLRule(i)
	}
	catalogTariffsMySQLInsertRules(t, db, rules)
	// Same model on another scope and another model on the requested scope must
	// not form a Cartesian match or consume the requested pair's 256-row budget.
	otherScope, otherModel := catalogTariffsMySQLRule(900), catalogTariffsMySQLRule(901)
	otherScope.ProviderScopeID++
	otherModel.Model = "seedance-2.5"
	catalogTariffsMySQLInsertRules(t, db, []videobilling.PricingRule{otherScope, otherModel})
	catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{256}, nil, func(s videobilling.CatalogTariffSnapshot) {
		catalogTariffsMySQLReason(t, s, rules[0].PricingKey, videobilling.CatalogTariffPricingConflict)
	})
	catalogTariffsMySQLInsertRules(t, db, []videobilling.PricingRule{catalogTariffsMySQLRule(256)})
	catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{257}, videobilling.ErrCatalogTariffLimit, nil)
	catalogTariffsMySQLCount(t, db, "video_pricing_rules", 259)
}

func catalogTariffsMySQLSet(n int) videobilling.MinimumRuleSet {
	return videobilling.MinimumRuleSet{Model: "seedance-2.0", Version: fmt.Sprintf("minimum-%02d", n),
		SourceURL: "https://docs.volcengine.com/docs/ark/model-pricing", EvidenceHash: strings.Repeat("b", 64),
		ObservedAt: catalogTariffsMySQLAt, VerifiedAt: catalogTariffsMySQLAt, VerifiedBy: 1}
}

func catalogTariffsMySQLCell(i int) videobilling.MinimumRuleCell {
	// 4 resolutions * 6 ratios * 12 legal durations * 120 legal FPS permits
	// 2001 distinct cells without changing the tracked unique constraint.
	resolutions, ratios := []string{"480p", "720p", "1080p", "4k"}, []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9"}
	return videobilling.MinimumRuleCell{Resolution: resolutions[i%4], Ratio: ratios[(i/4)%6],
		OutputDurationMS: int64(4+(i/24)%12) * 1000, FPS: int64(1 + i/288), MinimumTokens: int64(1000 + i)}
}

func catalogTariffsMySQLPublishSet(t *testing.T, db *gorm.DB, n, count int) videobilling.MinimumRuleSet {
	t.Helper()
	cells := make([]videobilling.MinimumRuleCell, count)
	for i := range cells {
		cells[i] = catalogTariffsMySQLCell(i)
	}
	var set videobilling.MinimumRuleSet
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		set, err = videobilling.PublishMinimumTx(tx, catalogTariffsMySQLSet(n), cells)
		if err != nil {
			return err
		}
		rule := catalogTariffsMySQLRule(n)
		rule.InputVideo, rule.MinimumRuleVersion = true, set.Version
		return tx.Create(&rule).Error
	})
	if err != nil || set.ID == 0 {
		t.Fatal("cannot publish legal synthetic minimum-rule fixture")
	}
	return set
}

func TestMySQLVideoCatalogTariffsMinimumSetBoundary(t *testing.T) {
	db := routingAdminMySQLDatabase(t)
	for i := 0; i < 16; i++ {
		catalogTariffsMySQLPublishSet(t, db, i, 1)
	}
	catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{16, 16, 16}, nil, nil)
	catalogTariffsMySQLPublishSet(t, db, 16, 1)
	catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{17, 17}, videobilling.ErrCatalogTariffLimit, nil)
	catalogTariffsMySQLCount(t, db, "video_minimum_rule_sets", 17)
	catalogTariffsMySQLCount(t, db, "video_minimum_rule_cells", 17)
}

func TestMySQLVideoCatalogTariffsMinimumCellBoundary(t *testing.T) {
	db := routingAdminMySQLDatabase(t)
	var first videobilling.MinimumRuleSet
	for i := 0; i < 16; i++ {
		set := catalogTariffsMySQLPublishSet(t, db, i, 2000)
		if i == 0 {
			first = set
		}
	}
	catalogTariffsMySQLCount(t, db, "video_minimum_rule_sets", 16)
	catalogTariffsMySQLCount(t, db, "video_minimum_rule_cells", 32000)
	catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{16, 16, 32000}, nil, nil)
	// PublishMinimumTx caps each set at 2000, so 32001 is unreachable through
	// sixteen valid API publications. The unchanged SQL schema permits this
	// historical/imported extra fact; it tests the reader's defensive limit,
	// not a claim that the ordinary publication API can exceed its own bound.
	extra := catalogTariffsMySQLCell(2000)
	extra.RuleSetID = first.ID
	if err := db.Create(&extra).Error; err != nil {
		t.Fatal("tracked schema unexpectedly refused unique imported minimum cell")
	}
	catalogTariffsMySQLCount(t, db, "video_minimum_rule_cells", 32001)
	catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{16, 16, 32001}, videobilling.ErrCatalogTariffLimit, nil)
}

func TestMySQLVideoCatalogTariffsExactIdentityAndVersionOverlap(t *testing.T) {
	db := routingAdminMySQLDatabase(t)
	rule := catalogTariffsMySQLRule(0)
	catalogTariffsMySQLInsertRules(t, db, []videobilling.PricingRule{rule})
	key := rule.PricingKey
	checkPrice := func(t *testing.T, s videobilling.CatalogTariffSnapshot) {
		t.Helper()
		out, err := s.Evaluate(key, catalogTariffsMySQLContext())
		if err != nil || out.Price == nil || out.Price.RuleID != rule.RuleID || out.Price.RetailUSDPer1M.String() != "1.234567890123" || len(out.Reasons) != 0 {
			t.Fatal("exact MySQL DECIMAL or public price identity changed")
		}
	}
	t.Run("exact_decimal", func(t *testing.T) {
		catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1}, nil, func(s videobilling.CatalogTariffSnapshot) { checkPrice(t, s) })
	})
	t.Run("price_pad_space_is_not_exact_identity", func(t *testing.T) {
		routingAdminSQL(t, db, "UPDATE video_pricing_rules SET model=? WHERE rule_id=?", key.Model+" ", rule.RuleID)
		var matched int64
		if err := db.Model(&videobilling.PricingRule{}).Where("provider_scope_id=? AND model=?", key.ProviderScopeID, key.Model).Count(&matched).Error; err != nil || matched != 1 {
			t.Fatal("real price collation PAD SPACE positive control did not match")
		}
		catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1}, videobilling.ErrCatalogTariffUnavailable, nil)
		routingAdminSQL(t, db, "UPDATE video_pricing_rules SET model=? WHERE rule_id=?", key.Model, rule.RuleID)
	})
	t.Run("tuple_pad_space_cannot_authorize_plan", func(t *testing.T) {
		for _, field := range []struct{ name, value string }{{"mode", key.Mode}, {"resolution", key.Resolution}, {"service_tier", key.ServiceTier}} {
			routingAdminSQL(t, db, "UPDATE video_pricing_rules SET "+field.name+"=? WHERE rule_id=?", field.value+" ", rule.RuleID)
			var loaded videobilling.PricingRule
			if err := db.Where("provider_scope_id=? AND model=? AND mode=? AND resolution=? AND input_video=? AND service_tier=? AND draft=?",
				key.ProviderScopeID, key.Model, key.Mode, key.Resolution, key.InputVideo, key.ServiceTier, key.Draft).Take(&loaded).Error; err != nil || loaded.RuleID != rule.RuleID {
				t.Fatal("real full tuple PAD SPACE positive control did not match")
			}
			catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1}, nil, func(s videobilling.CatalogTariffSnapshot) {
				catalogTariffsMySQLReason(t, s, key, videobilling.CatalogTariffPricingMissing)
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return videobilling.ValidatePublishedRuleTx(ctx, tx, loaded, key, rule.Version, catalogTariffsMySQLContext())
			})
			cancel()
			var publication *videobilling.CatalogPublicationError
			if !errors.Is(err, videobilling.ErrPricingUnavailable) || !errors.As(err, &publication) || !reflect.DeepEqual(publication.Reasons, []videobilling.CatalogTariffReason{videobilling.CatalogTariffPricingInvalid}) {
				t.Fatal("collation-selected rule bypassed exact plan tuple validation")
			}
			routingAdminSQL(t, db, "UPDATE video_pricing_rules SET "+field.name+"=? WHERE rule_id=?", field.value, rule.RuleID)
		}
	})
	t.Run("price_binary_case_is_missing", func(t *testing.T) {
		routingAdminSQL(t, db, "UPDATE video_pricing_rules SET model=? WHERE rule_id=?", strings.ToUpper(key.Model), rule.RuleID)
		catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{0}, nil, func(s videobilling.CatalogTariffSnapshot) {
			catalogTariffsMySQLReason(t, s, key, videobilling.CatalogTariffPricingMissing)
		})
		routingAdminSQL(t, db, "UPDATE video_pricing_rules SET model=? WHERE rule_id=?", key.Model, rule.RuleID)
	})
	t.Run("cross_version_overlap_and_half_open_interval", func(t *testing.T) {
		second := rule
		second.Version = "price-v2"
		catalogTariffsMySQLInsertRules(t, db, []videobilling.PricingRule{second})
		catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{2}, nil, func(s videobilling.CatalogTariffSnapshot) {
			catalogTariffsMySQLReason(t, s, key, videobilling.CatalogTariffPricingConflict)
		})
		routingAdminSQL(t, db, "UPDATE video_pricing_rules SET effective_until=? WHERE version='price-v2'", catalogTariffsMySQLAt)
		catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1}, nil, func(s videobilling.CatalogTariffSnapshot) { checkPrice(t, s) })
		routingAdminSQL(t, db, "UPDATE video_pricing_rules SET enabled=FALSE WHERE version='price-v1'")
		routingAdminSQL(t, db, "UPDATE video_pricing_rules SET effective_until=NULL WHERE version='price-v2'")
		catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1}, nil, func(s videobilling.CatalogTariffSnapshot) {
			catalogTariffsMySQLReason(t, s, key, videobilling.CatalogTariffPricingVersionMismatch)
		})
		routingAdminSQL(t, db, "UPDATE video_pricing_rules SET enabled=FALSE")
	})
	t.Run("minimum_full_pair_near_match", func(t *testing.T) {
		set := catalogTariffsMySQLPublishSet(t, db, 10, 1)
		// The real minimum table uses the charset-default collation from 0110;
		// unlike 0049 it need not be binary. Probe SQL equality rather than
		// claiming a collation branch the deployed schema cannot exercise.
		for _, field := range []string{"model", "version"} {
			original := set.Model
			if field == "version" {
				original = set.Version
			}
			exercised := false
			for _, changed := range []string{strings.ToUpper(original), original + " "} {
				routingAdminSQL(t, db, "UPDATE video_minimum_rule_sets SET "+field+"=? WHERE id=?", changed, set.ID)
				var matches int64
				if err := db.Model(&videobilling.MinimumRuleSet{}).Where("model=? AND version=?", set.Model, set.Version).Count(&matches).Error; err != nil {
					t.Fatal("cannot inspect real minimum pair equality")
				}
				if matches == 1 {
					catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1, 1}, videobilling.ErrCatalogTariffUnavailable, nil)
					exercised = true
				} else if matches == 0 {
					catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1, 0}, nil, func(s videobilling.CatalogTariffSnapshot) {
						inputKey := key
						inputKey.InputVideo = true
						catalogTariffsMySQLReason(t, s, inputKey, videobilling.CatalogTariffMinimumCoverageMissing)
					})
				} else {
					t.Fatal("minimum pair uniqueness was lost")
				}
				routingAdminSQL(t, db, "UPDATE video_minimum_rule_sets SET "+field+"=? WHERE id=?", original, set.ID)
			}
			if !exercised {
				t.Fatal("tracked minimum collation exercised neither case nor PAD near-match")
			}
		}
	})
}
