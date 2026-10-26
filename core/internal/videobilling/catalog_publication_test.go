package videobilling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func publicationFixture() PublicationContext {
	return PublicationContext{CapabilityVersion: "cap-v1", Ratios: []string{"16:9", "1:1", "adaptive"}, MinDuration: 4, MaxDuration: 5, FPS: 24,
		Bounds: []PublicationBound{{"720p", "16:9", 1280, 720}, {"720p", "1:1", 960, 960}}, MaxInputVideoDurationMS: 15000,
		FXUSDPerCNY: d("0.14"), FXVersion: "fx-v1", FXObservedAt: testAt}
}
func publicationMinimumFixture() (MinimumRuleSet, []MinimumRuleCell) {
	set := MinimumRuleSet{ID: 1, Model: "seedance-2.0", Version: "minimum-v1", SourceURL: "https://docs.volcengine.com/docs/ark/model-pricing", EvidenceHash: strings.Repeat("a", 64), ObservedAt: testAt, VerifiedAt: testAt, VerifiedBy: 1}
	cells := []MinimumRuleCell{
		{ID: 1, RuleSetID: 1, Resolution: "720p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 500000},
		{ID: 2, RuleSetID: 1, Resolution: "720p", Ratio: "16:9", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 600000},
		{ID: 3, RuleSetID: 1, Resolution: "720p", Ratio: "1:1", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 700000},
		{ID: 4, RuleSetID: 1, Resolution: "720p", Ratio: "1:1", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 800000},
	}
	return set, cells
}
func publicationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, _ := setup(t, "100")
	if err := db.Exec(minimumTestSchema).Error; err != nil {
		t.Fatal(err)
	}
	return db
}
func publicationRead(t *testing.T, tx *gorm.DB) CatalogTariffSnapshot {
	t.Helper()
	s, err := ReadCatalogTariffsTx(context.Background(), tx, []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func requirePublicationReason(t *testing.T, out CatalogCellResult, want CatalogTariffReason) {
	t.Helper()
	if out.Price != nil || !reflect.DeepEqual(out.Reasons, []CatalogTariffReason{want}) {
		t.Fatalf("reason=%v want=%s", out.Reasons, want)
	}
}

func TestCatalogPublicationSafePriceAndExactIdentity(t *testing.T) {
	r := ruleFixture()
	p := publicationFixture()
	out := evaluateCatalogRule(r, r.PricingKey, "v1", p, nil, nil)
	if out.Price == nil || !out.Price.RetailUSDPer1M.Equal(d("8")) || out.Price.RuleID != "retail-test" || out.Price.PricingVersion != "v1" || out.Price.FormulaVersion != "formula-v1" || out.Price.MinimumRuleVersion != "" || out.Reasons == nil || len(out.Reasons) != 0 {
		t.Fatal("safe publication mismatch")
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"Supplier", "SourceHash", "FX", "Evidence", "0.14"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private facts exposed")
		}
	}
	keys := []PricingKey{}
	for _, change := range []func(*PricingKey){func(k *PricingKey) { k.ProviderScopeID++ }, func(k *PricingKey) { k.Model = "seedance-2.5" }, func(k *PricingKey) { k.Mode = "draft" }, func(k *PricingKey) { k.Resolution = "480p" }, func(k *PricingKey) { k.InputVideo = true }, func(k *PricingKey) { k.ServiceTier = "flex" }, func(k *PricingKey) { k.Draft = true }} {
		k := r.PricingKey
		change(&k)
		keys = append(keys, k)
	}
	for _, key := range keys {
		requirePublicationReason(t, evaluateCatalogRule(r, key, "v1", p, nil, nil), CatalogTariffPricingInvalid)
	}
	for _, version := range []string{"", "v1 ", "V1", "v2"} {
		requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, version, p, nil, nil), CatalogTariffPricingVersionMismatch)
	}
}

func TestCatalogPublicationMinimumClassificationAndFrames(t *testing.T) {
	r := ruleFixture()
	r.InputVideo = true
	r.MinimumRuleVersion = "minimum-v1"
	p := publicationFixture()
	set, cells := publicationMinimumFixture()
	requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", p, nil, nil), CatalogTariffMinimumCoverageMissing)
	requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", p, []MinimumRuleSet{set}, cells[:3]), CatalogTariffMinimumCoverageMissing)
	badSet := set
	badSet.EvidenceHash = "invalid"
	requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", p, []MinimumRuleSet{badSet}, cells), CatalogTariffMinimumRuleInvalid)
	requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", p, []MinimumRuleSet{set, set}, cells), CatalogTariffMinimumRuleInvalid)
	duplicates := append(append([]MinimumRuleCell(nil), cells...), cells[0])
	requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", p, []MinimumRuleSet{set}, duplicates), CatalogTariffMinimumRuleInvalid)
	zero := append([]MinimumRuleCell(nil), cells...)
	zero[0].MinimumTokens = 0
	requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", p, []MinimumRuleSet{set}, zero), CatalogTariffMinimumRuleInvalid)
	// Frames remain unsupported for reference requests, but do not hide the
	// independently valid duration combination in a capability with frame support.
	p.Frames = true
	p.MinFrames = 0
	p.MaxFrames = math.MaxInt64
	p.FrameStep = 0
	out := evaluateCatalogRule(r, r.PricingKey, "v1", p, []MinimumRuleSet{set}, cells)
	if out.Price == nil || out.Price.MinimumRuleVersion != "minimum-v1" {
		t.Fatal("reference duration incorrectly blocked", out.Reasons)
	}
	maximum, err := CatalogMinimumTokens(r, []MinimumRuleSet{set}, cells, MinimumContext{Ratios: p.Ratios, MinDuration: 4, MaxDuration: 5}, 24)
	if err != nil || maximum != 800000 {
		t.Fatal("common floor algorithm diverged", maximum, err)
	}
	if _, err = CatalogMinimumTokens(r, []MinimumRuleSet{set}, cells[:3], MinimumContext{Ratios: p.Ratios, MinDuration: 4, MaxDuration: 5}, 24); !errors.Is(err, ErrPricingUnavailable) {
		t.Fatal("legacy classification changed")
	}
}

func TestCatalogPublicationFrameProbeAndScalarFloor(t *testing.T) {
	r := ruleFixture()
	p := publicationFixture()
	// A stale scalar floor would exceed Quote's money bound. It must be cleared
	// exactly as non-reference QuoteWithMinimumTx clears it.
	r.MinimumTokens = math.MaxInt64
	if got := evaluateCatalogRule(r, r.PricingKey, "v1", p, nil, nil); got.Price == nil {
		t.Fatal("stale scalar floor affected non-reference publication")
	}
	p.Frames = true
	p.MinFrames = 1
	p.MaxFrames = math.MaxInt64
	p.FrameStep = math.MaxInt64
	// MaxFrames is unreachable: only MinFrames belongs to the progression.
	if got := evaluateCatalogRule(r, r.PricingKey, "v1", p, nil, nil); got.Price == nil {
		t.Fatal("unreachable frame maximum used", got.Reasons)
	}
	p.FrameStep = 1
	requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", p, nil, nil), CatalogTariffPricingInvalid)
	for _, change := range []func(*PublicationContext){func(p *PublicationContext) { p.MinFrames = 0 }, func(p *PublicationContext) { p.MinFrames = 200; p.MaxFrames = 199 }, func(p *PublicationContext) { p.FrameStep = 0 }} {
		x := publicationFixture()
		x.Frames = true
		x.MinFrames = 97
		x.MaxFrames = 121
		x.FrameStep = 8
		change(&x)
		requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", x, nil, nil), CatalogTariffPricingInvalid)
	}
	for _, change := range []func(*PublicationContext){func(p *PublicationContext) { p.FXUSDPerCNY = d("0") }, func(p *PublicationContext) { p.FXVersion = "" }, func(p *PublicationContext) { p.FXObservedAt = time.Time{} }, func(p *PublicationContext) { p.Ratios = []string{"adaptive"} }, func(p *PublicationContext) { p.Bounds = p.Bounds[:1] }, func(p *PublicationContext) { p.Bounds = append(p.Bounds, p.Bounds[0]) }} {
		x := publicationFixture()
		change(&x)
		requirePublicationReason(t, evaluateCatalogRule(r, r.PricingKey, "v1", x, nil, nil), CatalogTariffPricingInvalid)
	}
}

func TestCatalogPublicationReaderTransactionPairsAndOverlap(t *testing.T) {
	db := publicationDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	// Dirty scope and pending uncommitted facts must retain this actual pool.
	r := ruleFixture()
	r.RuleID = "uncommitted"
	r.Model = "seedance-2.5"
	r.ProviderScopeID = 12
	if err := tx.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range []PricingRule{func() PricingRule { x := ruleFixture(); x.RuleID = "other-pair"; x.ProviderScopeID = 12; return x }(), func() PricingRule {
		x := ruleFixture()
		x.RuleID = "future"
		x.Version = "future"
		x.EffectiveFrom = testAt.Add(time.Hour)
		return x
	}(), func() PricingRule { x := ruleFixture(); x.RuleID = "disabled"; x.Enabled = false; return x }()} {
		if err := tx.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries := 0
	if err := db.Callback().Query().After("gorm:query").Register("publication-pool", func(q *gorm.DB) {
		queries++
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("escaped caller transaction")
		}
		deadline, ok := q.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > 200*time.Millisecond {
			t.Error("query lacks bounded child")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("publication-pool") })
	s, err := ReadCatalogTariffsTx(context.Background(), tx.Table("wrong_private_table").Where("id=999").Select("secret").Limit(1), []CatalogTariffRef{{11, "seedance-2.0", "v1"}, {12, "seedance-2.5", "v1"}}, testAt)
	if err != nil || len(s.rules) != 2 || queries != 1 {
		t.Fatal("paired bounded read failed", len(s.rules), queries, err)
	}
	if got, err := s.Evaluate(r.PricingKey, publicationFixture()); err != nil || got.Price == nil {
		t.Fatal("uncommitted caller row absent", err)
	}
	encoded, err := json.Marshal(s)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("snapshot exposed internal rows")
	}
	r2 := ruleFixture()
	r2.RuleID = "overlap"
	r2.Version = "v2"
	if err := tx.Create(&r2).Error; err != nil {
		t.Fatal(err)
	}
	s = publicationRead(t, tx)
	out, err := s.Evaluate(ruleFixture().PricingKey, publicationFixture())
	if err != nil {
		t.Fatal(err)
	}
	requirePublicationReason(t, out, CatalogTariffPricingConflict)
	key := ruleFixture().PricingKey
	key.Resolution = "480p"
	out, err = s.Evaluate(key, publicationFixture())
	if err != nil {
		t.Fatal(err)
	}
	requirePublicationReason(t, out, CatalogTariffPricingMissing)
}

func TestCatalogPublicationReadAndPlanShareFullCoverage(t *testing.T) {
	db := publicationDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	r := ruleFixture()
	r.RuleID = "reference"
	r.InputVideo = true
	r.MinimumRuleVersion = "minimum-v1"
	set, cells := publicationMinimumFixture()
	for _, v := range []any{&r, &set, &cells} {
		if err := tx.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries := 0
	if err := db.Callback().Query().After("gorm:query").Register("publication-count", func(*gorm.DB) { queries++ }); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().After("gorm:row").Register("publication-count", func(*gorm.DB) { queries++ }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("publication-count") })
	t.Cleanup(func() { _ = db.Callback().Row().Remove("publication-count") })
	s := publicationRead(t, tx)
	if queries != 3 {
		t.Fatalf("reads=%d want 3", queries)
	}
	out, err := s.Evaluate(r.PricingKey, publicationFixture())
	if err != nil || out.Price == nil {
		t.Fatal(out.Reasons, err)
	}
	queries = 0
	if err := ValidatePublishedRuleTx(context.Background(), tx, r, r.PricingKey, "v1", publicationFixture()); err != nil || queries != 2 {
		t.Fatal("plan must use two finite evidence reads", queries, err)
	}
	if err := tx.Delete(&MinimumRuleCell{}, 4).Error; err != nil {
		t.Fatal(err)
	}
	s = publicationRead(t, tx)
	out, err = s.Evaluate(r.PricingKey, publicationFixture())
	if err != nil {
		t.Fatal(err)
	}
	requirePublicationReason(t, out, CatalogTariffMinimumCoverageMissing)
	err = ValidatePublishedRuleTx(context.Background(), tx, r, r.PricingKey, "v1", publicationFixture())
	var publicationError *CatalogPublicationError
	if !errors.Is(err, ErrPricingUnavailable) || !errors.As(err, &publicationError) || !reflect.DeepEqual(publicationError.Reasons, out.Reasons) {
		t.Fatal("plan and directory disagreed", err)
	}
	queries = 0
	key := r.PricingKey
	key.Draft = true
	if err := ValidatePublishedRuleTx(context.Background(), tx, r, key, "v1", publicationFixture()); !errors.Is(err, ErrPricingUnavailable) || queries != 0 {
		t.Fatal("foreign identity queried evidence", err, queries)
	}
}

func TestCatalogPublicationReadLimitsAndInvalidArgs(t *testing.T) {
	db := publicationDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	ref := CatalogTariffRef{11, "seedance-2.0", "v1"}
	for _, refs := range [][]CatalogTariffRef{{ref, ref}, {{0, "seedance-2.0", "v1"}}, {{11, "Seedance-2.0", "v1"}}, {{11, "seedance-2.0 ", "v1"}}, {{12, "seedance-2.5", "v1"}, ref}, {{11, "seedance-2.0", strings.Repeat("v", 65)}}} {
		if _, err := ReadCatalogTariffsTx(context.Background(), tx, refs, testAt); !errors.Is(err, ErrCatalogTariffInvalid) {
			t.Fatal("invalid refs accepted", err)
		}
	}
	for _, handle := range []*gorm.DB{nil, db, tx.Session(&gorm.Session{DryRun: true})} {
		if _, err := ReadCatalogTariffsTx(context.Background(), handle, nil, testAt); !errors.Is(err, ErrCatalogTariffInvalid) {
			t.Fatal("invalid tx accepted", err)
		}
	}
	if _, err := ReadCatalogTariffsTx(context.Background(), tx, nil, testAt); err != nil {
		t.Fatal("empty batch must be allowed", err)
	}
	rows := make([]PricingRule, 256)
	for i := range rows {
		rows[i] = ruleFixture()
		rows[i].RuleID = fmt.Sprintf("boundary-%d", i)
	}
	if err := tx.CreateInBatches(rows, 32).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCatalogTariffsTx(context.Background(), tx, []CatalogTariffRef{ref}, testAt); !errors.Is(err, ErrCatalogTariffLimit) {
		t.Fatal("257 rules must reject whole snapshot", err)
	}
	if err := tx.Where("rule_id = ?", "boundary-255").Delete(&PricingRule{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCatalogTariffsTx(context.Background(), tx, []CatalogTariffRef{ref}, testAt); err != nil {
		t.Fatal("256 rules should fit", err)
	}
}

func TestCatalogPublicationSafeErrorsCancellationAndLogger(t *testing.T) {
	db := publicationDB(t)
	var logs bytes.Buffer
	warn := logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})
	tx := db.Session(&gorm.Session{Logger: warn}).Begin()
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := ReadCatalogTariffsTx(ctx, tx, []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCatalogTariffUnavailable) || !reflect.DeepEqual(s, CatalogTariffSnapshot{}) {
		t.Fatal("cancellation lost", err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("publication-failure", func(q *gorm.DB) { q.AddError(errors.New("private-driver-and-sql-secret")) }); err != nil {
		t.Fatal(err)
	}
	s, err = ReadCatalogTariffsTx(context.Background(), tx, []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
	if err == nil || err.Error() != ErrCatalogTariffUnavailable.Error() || !reflect.DeepEqual(s, CatalogTariffSnapshot{}) || logs.Len() != 0 {
		t.Fatal("error/log disclosure", err)
	}
	_ = db.Callback().Query().Remove("publication-failure")
	// A positive control proves the inherited Warn logger was not disabled globally.
	_ = tx.Exec("SELECT inherited_warn_control FROM nonexistent_publication_test_table").Error
	if !strings.Contains(logs.String(), "inherited_warn_control") {
		t.Fatal("positive logger control did not run")
	}
	ctx, cancel = context.WithCancel(context.Background())
	s, err = ReadCatalogTariffsTx(ctx, tx, []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	out, err := s.Evaluate(ruleFixture().PricingKey, publicationFixture())
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(out, CatalogCellResult{}) {
		t.Fatal("cancelled snapshot published")
	}
}

func TestCatalogPublicationQueryDeadlineDoesNotBecomeCPUDeadline(t *testing.T) {
	db := publicationDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	var child context.Context
	if err := db.Callback().Query().After("gorm:query").Register("publication-context", func(q *gorm.DB) { child = q.Statement.Context }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("publication-context") })
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := ReadCatalogTariffsTx(parent, tx, []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
	if err != nil || child == nil || !errors.Is(child.Err(), context.Canceled) || parent.Err() != nil {
		t.Fatal("completed query child was not disposed independently", err)
	}
	out, err := s.Evaluate(ruleFixture().PricingKey, publicationFixture())
	if err != nil || out.Price == nil {
		t.Fatal("query child leaked into CPU evaluation", err)
	}
	_ = db.Callback().Query().Remove("publication-context")
	if err := db.Callback().Query().Before("gorm:query").Register("publication-deadline", func(q *gorm.DB) { <-q.Statement.Context.Done() }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("publication-deadline") })
	short, shortCancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer shortCancel()
	s, err = ReadCatalogTariffsTx(short, tx, []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrCatalogTariffUnavailable) || !reflect.DeepEqual(s, CatalogTariffSnapshot{}) {
		t.Fatal("parent deadline lost or partial result returned", err)
	}
}

func TestCatalogPublicationMinimumSetAndCellReadBounds(t *testing.T) {
	db := publicationDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	for i := 0; i < 17; i++ {
		r := ruleFixture()
		r.RuleID = fmt.Sprintf("reference-%d", i)
		r.InputVideo = true
		r.MinimumRuleVersion = fmt.Sprintf("minimum-%d", i)
		s, _ := publicationMinimumFixture()
		s.ID = 0
		s.Version = r.MinimumRuleVersion
		if err := tx.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
	ref := []CatalogTariffRef{{11, "seedance-2.0", "v1"}}
	if _, err := ReadCatalogTariffsTx(context.Background(), tx, ref, testAt); !errors.Is(err, ErrCatalogTariffLimit) {
		t.Fatal("17 minimum sets accepted", err)
	}
	if err := tx.Where("version = ?", "minimum-16").Delete(&MinimumRuleSet{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCatalogTariffsTx(context.Background(), tx, ref, testAt); err != nil {
		t.Fatal("16 sets should fit", err)
	}
	// Pure cardinality/association validation is separate from SQL scanning.
	// Actual 32000/32001 reads and deadlines remain real MySQL acceptance cases.
	cells := make([]MinimumRuleCell, 32001)
	for i := range cells {
		cells[i].RuleSetID = 1
	}
	if err := validateCatalogMinimumCells(cells[:32000], map[uint64]bool{1: true}); err != nil {
		t.Fatal("32000 result cardinality should fit", err)
	}
	if err := validateCatalogMinimumCells(cells, map[uint64]bool{1: true}); !errors.Is(err, ErrCatalogTariffLimit) {
		t.Fatal("32001 result cardinality accepted", err)
	}
	if err := validateCatalogMinimumCells(cells[:1], map[uint64]bool{2: true}); !errors.Is(err, ErrCatalogTariffUnavailable) {
		t.Fatal("foreign minimum cell accepted", err)
	}
}

func TestCatalogPublicationRejectsCollationIdentityMismatch(t *testing.T) {
	for _, test := range []struct {
		name, collation string
		minimum         bool
		mutate          func(string) string
	}{
		{"model_case", "NOCASE", false, strings.ToUpper},
		{"model_trailing_space", "RTRIM", false, func(s string) string { return s + " " }},
		{"minimum_version_case", "NOCASE", true, strings.ToUpper},
		{"minimum_version_trailing_space", "RTRIM", true, func(s string) string { return s + " " }},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			pool.SetMaxOpenConns(1)
			defer pool.Close()
			// Handwritten SQLite schemas use real NOCASE / RTRIM collations only for
			// this exact-identity guard test; production MySQL DDL is never changed.
			schema := strings.ReplaceAll(testSchema+minimumTestSchema, "model TEXT NOT NULL", "model TEXT COLLATE "+test.collation+" NOT NULL")
			schema = strings.ReplaceAll(schema, "version TEXT NOT NULL", "version TEXT COLLATE "+test.collation+" NOT NULL")
			if err = db.Exec(schema).Error; err != nil {
				t.Fatal(err)
			}
			r := ruleFixture()
			if test.minimum {
				r.InputVideo = true
				r.MinimumRuleVersion = "minimum-v1"
				s, _ := publicationMinimumFixture()
				s.Version = test.mutate(s.Version)
				if err = db.Create(&s).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				r.Model = test.mutate(r.Model)
			}
			if err = db.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			tx := db.Begin()
			defer tx.Rollback()
			got, err := ReadCatalogTariffsTx(context.Background(), tx, []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
			if !errors.Is(err, ErrCatalogTariffUnavailable) || !reflect.DeepEqual(got, CatalogTariffSnapshot{}) {
				t.Fatal("nonexact row published or silently discarded", err)
			}
		})
	}
}

func TestCatalogPublicationEffectiveInstantsAcrossOffsets(t *testing.T) {
	for _, test := range []struct {
		name  string
		from  time.Time
		until *time.Time
		want  bool
	}{
		{"active_positive_offset", testAt.Add(-time.Hour).In(time.FixedZone("east", 8*3600)), nil, true},
		{"active_negative_offset", testAt.Add(-time.Hour).In(time.FixedZone("west", -5*3600)), nil, true},
		{"future_negative_offset", testAt.Add(time.Hour).In(time.FixedZone("west", -5*3600)), nil, false},
		{"expired_positive_offset", testAt.Add(-2 * time.Hour), func() *time.Time { x := testAt.Add(-time.Hour).In(time.FixedZone("east", 8*3600)); return &x }(), false},
		{"from_exact", testAt.In(time.FixedZone("east", 8*3600)), nil, true},
		{"until_exact", testAt.Add(-time.Hour), func() *time.Time { x := testAt.In(time.FixedZone("west", -5*3600)); return &x }(), false},
		{"until_one_microsecond_after", testAt.Add(-time.Hour), func() *time.Time { x := testAt.Add(time.Microsecond); return &x }(), true},
		{"from_one_microsecond_after", testAt.Add(time.Microsecond), nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := publicationDB(t)
			if err := db.Model(&PricingRule{}).Where("id = 1").Updates(map[string]any{"effective_from": test.from, "effective_until": test.until}).Error; err != nil {
				t.Fatal(err)
			}
			tx := db.Begin()
			defer tx.Rollback()
			s := publicationRead(t, tx)
			got, err := s.Evaluate(ruleFixture().PricingKey, publicationFixture())
			if err != nil {
				t.Fatal(err)
			}
			if test.want {
				if got.Price == nil {
					t.Fatal("valid instant blocked", got.Reasons)
				}
			} else {
				requirePublicationReason(t, got, CatalogTariffPricingMissing)
			}
		})
	}
}
