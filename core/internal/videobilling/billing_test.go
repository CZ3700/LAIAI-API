package videobilling

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Hand-written SQLite schema, intentionally no AutoMigrate. MySQL DDL draft is
// in README; integration owns the versioned migration and real-MySQL lock gate.
const testSchema = `
CREATE TABLE api_keys(id INTEGER PRIMARY KEY,org_id INTEGER NOT NULL,quota_limit NUMERIC NOT NULL DEFAULT 0,used_amount NUMERIC NOT NULL DEFAULT 0);
CREATE TABLE ledger_accounts(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,type TEXT NOT NULL,shard INTEGER NOT NULL DEFAULT 0,posted_balance NUMERIC NOT NULL DEFAULT 0,held_amount NUMERIC NOT NULL DEFAULT 0,created_at DATETIME,updated_at DATETIME,UNIQUE(org_id,type,shard));
CREATE TABLE ledger_account_shards(org_id INTEGER NOT NULL,type TEXT NOT NULL,shard_count INTEGER NOT NULL DEFAULT 1,updated_at DATETIME,PRIMARY KEY(org_id,type));
CREATE TABLE ledger_holds(id INTEGER PRIMARY KEY AUTOINCREMENT,idempotency_key TEXT NOT NULL UNIQUE,account_id INTEGER NOT NULL,amount NUMERIC NOT NULL,captured_amount NUMERIC NOT NULL DEFAULT 0,status INTEGER NOT NULL DEFAULT 0,ref_type TEXT NOT NULL,ref_id TEXT NOT NULL,expires_at DATETIME,created_at DATETIME,updated_at DATETIME);
CREATE TABLE ledger_journal(id INTEGER PRIMARY KEY AUTOINCREMENT,idempotency_key TEXT NOT NULL UNIQUE,account_id INTEGER NOT NULL,kind TEXT NOT NULL,delta NUMERIC NOT NULL,balance_after NUMERIC NOT NULL,hold_id INTEGER,ref_type TEXT NOT NULL,ref_id TEXT NOT NULL,reason TEXT NOT NULL,created_at DATETIME);
CREATE TABLE video_pricing_rules(id INTEGER PRIMARY KEY AUTOINCREMENT,rule_id TEXT NOT NULL,version TEXT NOT NULL,provider_scope_id INTEGER NOT NULL,model TEXT NOT NULL,mode TEXT NOT NULL,resolution TEXT NOT NULL,input_video INTEGER NOT NULL,service_tier TEXT NOT NULL,draft INTEGER NOT NULL,retail_usd_per_1m NUMERIC NOT NULL,supplier_cny_per_1m NUMERIC NOT NULL,minimum_tokens INTEGER NOT NULL,minimum_rule_version TEXT NOT NULL,risk_multiplier NUMERIC NOT NULL,formula_version TEXT NOT NULL,source_hash TEXT NOT NULL,effective_from DATETIME NOT NULL,effective_until DATETIME,enabled INTEGER NOT NULL,UNIQUE(rule_id,version));
CREATE TABLE video_quota_policies(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,scope TEXT NOT NULL,owner_id INTEGER NOT NULL,model TEXT NOT NULL,metric TEXT NOT NULL,period TEXT NOT NULL,timezone TEXT NOT NULL,limit_amount NUMERIC,limit_units INTEGER,version TEXT NOT NULL,enabled INTEGER NOT NULL);
CREATE TABLE video_quota_buckets(id INTEGER PRIMARY KEY AUTOINCREMENT,policy_id INTEGER NOT NULL,scope_key TEXT NOT NULL,timezone TEXT NOT NULL,org_id INTEGER NOT NULL,period_start DATETIME NOT NULL,period_end DATETIME,limit_amount NUMERIC,limit_units INTEGER,reserved_amount NUMERIC NOT NULL DEFAULT 0,consumed_amount NUMERIC NOT NULL DEFAULT 0,reserved_units INTEGER NOT NULL DEFAULT 0,consumed_units INTEGER NOT NULL DEFAULT 0,blocked INTEGER NOT NULL DEFAULT 0,UNIQUE(scope_key,period_start));
CREATE TABLE video_quota_reservations(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,task_id TEXT NOT NULL,bucket_id INTEGER NOT NULL,metric TEXT NOT NULL,supplier INTEGER NOT NULL,reserved_amount NUMERIC NOT NULL,reserved_units INTEGER NOT NULL,actual_amount NUMERIC NOT NULL,actual_units INTEGER NOT NULL,state TEXT NOT NULL,created_at DATETIME NOT NULL,settled_at DATETIME,UNIQUE(task_id,bucket_id));
CREATE TABLE video_billing_snapshots(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,api_key_id INTEGER NOT NULL,task_id TEXT NOT NULL,provider_scope_id INTEGER NOT NULL,model TEXT NOT NULL,rule_id TEXT NOT NULL,rule_version TEXT NOT NULL,formula_version TEXT NOT NULL,minimum_rule_version TEXT NOT NULL,capability_version TEXT NOT NULL,request_hmac TEXT NOT NULL,metering_json TEXT NOT NULL,hold_id INTEGER NOT NULL,estimated_tokens INTEGER NOT NULL,hold_usd NUMERIC NOT NULL,retail_usd_per_1m NUMERIC NOT NULL,supplier_cny_per_1m NUMERIC NOT NULL,effective_ratio NUMERIC NOT NULL,fx_usd_per_cny NUMERIC NOT NULL,fx_version TEXT NOT NULL,fx_observed_at DATETIME NOT NULL,supplier_reserved_cny NUMERIC NOT NULL,supplier_reserved_usd NUMERIC NOT NULL,created_at DATETIME NOT NULL,UNIQUE(org_id,task_id),UNIQUE(hold_id));
CREATE TABLE video_usage_evidence(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,task_id TEXT NOT NULL,evidence_id TEXT NOT NULL UNIQUE,source TEXT NOT NULL,source_record_id TEXT NOT NULL,source_version TEXT NOT NULL,completion_tokens INTEGER,payload_ref TEXT NOT NULL,observed_at DATETIME NOT NULL,UNIQUE(org_id,task_id,source,source_record_id,source_version));
CREATE TABLE video_billing_entries(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,task_id TEXT NOT NULL,settlement_key TEXT NOT NULL,state TEXT NOT NULL,snapshot_id INTEGER NOT NULL,evidence_id TEXT NOT NULL,hold_id INTEGER NOT NULL,journal_id INTEGER,completion_tokens INTEGER,calculated_usd NUMERIC NOT NULL,captured_usd NUMERIC NOT NULL,released_usd NUMERIC NOT NULL,absorbed_usd NUMERIC NOT NULL,supplier_cny NUMERIC NOT NULL,supplier_usd NUMERIC NOT NULL,reason TEXT NOT NULL,created_at DATETIME NOT NULL,UNIQUE(org_id,task_id,settlement_key),UNIQUE(journal_id));
`

var testAt = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func d(s string) decimal.Decimal       { return decimal.RequireFromString(s) }
func amount(s string) *decimal.Decimal { x := d(s); return &x }
func token(n int64) *int64             { return &n }
func ruleFixture() PricingRule {
	return PricingRule{RuleID: "retail-test", Version: "v1", PricingKey: PricingKey{ProviderScopeID: 11, Model: "seedance-2.0", Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: d("8"), SupplierCNYPer1M: d("46"), RiskMultiplier: d("1"), FormulaVersion: "formula-v1", SourceHash: strings.Repeat("a", 64), EffectiveFrom: testAt.Add(-time.Hour), Enabled: true}
}
func meterFixture() MeteringInput {
	return MeteringInput{OutputDurationMS: 5000, Width: 1280, Height: 720, FPSNumerator: 24, FPSDenominator: 1, BoundsVerified: true, CapabilityVersion: "cap-v1", EffectiveRatio: d("1"), FXUSDPerCNY: d("0.14"), FXVersion: "fx-v1", FXObservedAt: testAt}
}
func setup(t *testing.T, balance string) (*gorm.DB, QuoteResult) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(testSchema).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO api_keys(id,org_id) VALUES(1,10),(2,20),(3,10)").Error; err != nil {
		t.Fatal(err)
	}
	rule := ruleFixture()
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		a, err := ledger.EnsureAccount(tx, 10, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		_, _, err = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "seed", AccountID: a.ID, Kind: ledger.KindCredit, Delta: d(balance), RefType: "test", RefID: "seed"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Quote(rule, meterFixture())
	if err != nil {
		t.Fatal(err)
	}
	return db, q
}
func reserve(t *testing.T, db *gorm.DB, q QuoteResult, id string) ReservationResult {
	t.Helper()
	var got ReservationResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		got, e = ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: id, RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func settle(t *testing.T, db *gorm.DB, id string, n *int64, version string) SettlementResult {
	t.Helper()
	var got SettlementResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		got, e = SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: id, Now: testAt, Evidence: UsageEvidence{EvidenceID: "e-" + id + version, Source: "task_get", SourceRecordID: id, SourceVersion: version, CompletionTokens: n, ObservedAt: testAt}})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func checkBalance(t *testing.T, db *gorm.DB, posted, held string) {
	t.Helper()
	p, h, err := ledger.OrgBalances(db, 10, ledger.AccountUserCredit)
	if err != nil || !p.Equal(d(posted)) || !h.Equal(d(held)) {
		t.Fatalf("balance %s/%s err=%v want %s/%s", p, h, err, posted, held)
	}
	drift, err := ledger.CheckDrift(db)
	if err != nil || len(drift) != 0 {
		t.Fatalf("drift=%v err=%v", drift, err)
	}
}
func keyUsed(t *testing.T, db *gorm.DB) decimal.Decimal {
	t.Helper()
	var x decimal.Decimal
	if err := db.Table("api_keys").Select("used_amount").Where("id=1").Scan(&x).Error; err != nil {
		t.Fatal(err)
	}
	return x
}
func count(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}
func policy(t *testing.T, db *gorm.DB, p QuotaPolicy) {
	t.Helper()
	p.Enabled = true
	if p.Version == "" {
		p.Version = "v1"
	}
	if p.Timezone == "" {
		p.Timezone = "Asia/Shanghai"
	}
	if p.Period == "" {
		p.Period = PeriodDay
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
}

func TestQuoteExactBoundsAndFrameFormula(t *testing.T) {
	r, m := ruleFixture(), meterFixture()
	q, err := Quote(r, m)
	if err != nil || q.EstimatedTokens != 108000 || !q.HoldUSD.Equal(d("0.864")) || !q.SupplierReservedCNY.Equal(d("4.968")) {
		t.Fatalf("%+v %v", q, err)
	}
	m.OutputDurationMS = 0
	m.OutputFrames = 120
	q2, err := Quote(r, m)
	if err != nil || q2.EstimatedTokens != q.EstimatedTokens {
		t.Fatalf("frames %+v %v", q2, err)
	}
	r.InputVideo = true
	r.MinimumRuleVersion = "min-v1"
	m.InputVideo = true
	m.InputDurationMS = 15000
	m.OutputFrames = 0
	m.OutputDurationMS = 15000
	m.Width = 3840
	m.Height = 2160
	q, err = Quote(r, m)
	if err != nil || q.EstimatedTokens != 5832000 {
		t.Fatalf("4M cap regression %+v %v", q, err)
	}
	r.MinimumTokens = 6000000
	q, err = Quote(r, m)
	if err != nil || q.EstimatedTokens != 6000000 {
		t.Fatal(q, err)
	}
}
func TestQuoteRejectsUnverifiedAndOverflow(t *testing.T) {
	for _, change := range []func(*MeteringInput){func(m *MeteringInput) { m.BoundsVerified = false }, func(m *MeteringInput) { m.FPSDenominator = 0 }, func(m *MeteringInput) { m.OutputDurationMS = -1 }, func(m *MeteringInput) { m.OutputFrames = 1 }, func(m *MeteringInput) { m.InputDurationMS = 1 }, func(m *MeteringInput) { m.Width = 1 << 62; m.Height = 1 << 62 }} {
		m := meterFixture()
		change(&m)
		if _, err := Quote(ruleFixture(), m); !errors.Is(err, ErrInvalid) {
			t.Fatalf("want invalid: %v", err)
		}
	}
	r := ruleFixture()
	r.RetailUSDPer1M = d("0.000000000001")
	q, err := Quote(r, meterFixture())
	if err != nil || !q.HoldUSD.Equal(d("0.00000001")) {
		t.Fatal(q, err)
	}
}
func TestReserveHardQuotaAndRollback(t *testing.T) {
	for _, which := range []string{"key", "policy", "wallet", "caller"} {
		t.Run(which, func(t *testing.T) {
			balance := "10"
			if which == "wallet" {
				balance = "0.1"
			}
			db, q := setup(t, balance)
			policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("2")})
			if which == "key" {
				db.Exec("UPDATE api_keys SET quota_limit=0.1 WHERE id=1")
			}
			if which == "policy" {
				policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeKey, OwnerID: 1, Metric: MetricTokens, LimitUnits: token(1)})
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				_, err := ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: "task_r", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
				if err != nil {
					return err
				}
				if which == "caller" {
					return errors.New("injected outbox failure")
				}
				return nil
			})
			if err == nil {
				t.Fatal("must fail")
			}
			checkBalance(t, db, balance, "0")
			if !keyUsed(t, db).IsZero() || count(t, db, "video_billing_snapshots") != 0 || count(t, db, "video_quota_reservations") != 0 || count(t, db, "ledger_holds") != 0 {
				t.Fatal("partial reserve persisted")
			}
		})
	}
}
func TestMissingUsageThenRealSettlementIsIdempotent(t *testing.T) {
	db, q := setup(t, "10")
	reserve(t, db, q, "task_a")
	checkBalance(t, db, "10", "0.864")
	r := settle(t, db, "task_a", nil, "1")
	if r.State != PendingUsage || r.Entry != nil {
		t.Fatal(r)
	}
	checkBalance(t, db, "10", "0.864")
	r = settle(t, db, "task_a", token(54000), "2")
	if r.State != Settled || !r.Entry.CapturedUSD.Equal(d("0.432")) {
		t.Fatal(r)
	}
	checkBalance(t, db, "9.568", "0")
	if !keyUsed(t, db).Equal(d("0.432")) {
		t.Fatal(keyUsed(t, db))
	}
	if !settle(t, db, "task_a", token(54000), "3").Replay {
		t.Fatal("not replay")
	}
	if count(t, db, "video_billing_entries") != 1 {
		t.Fatal("duplicate bill")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: "task_a", Now: testAt, Evidence: UsageEvidence{CompletionTokens: token(54001)}})
		return e
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestOverHoldPreservesUsageAndSupplierCost(t *testing.T) {
	db, q := setup(t, "1")
	policy(t, db, QuotaPolicy{OrgID: 0, Scope: ScopeProvider, OwnerID: 11, Metric: MetricCNY, LimitAmount: amount("100")})
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricTokens, LimitUnits: token(108000)})
	reserve(t, db, q, "task_over")
	r := settle(t, db, "task_over", token(216000), "1")
	if !r.QuotaOverrun || *r.Entry.CompletionTokens != 216000 || !r.Entry.CapturedUSD.Equal(d("0.864")) || !r.Entry.AbsorbedUSD.Equal(d("0.864")) || !r.Entry.SupplierCNY.Equal(d("9.936")) {
		t.Fatalf("%+v", r.Entry)
	}
	checkBalance(t, db, "0.136", "0")
	var bs []QuotaBucket
	db.Find(&bs)
	if len(bs) != 2 || !bs[0].Blocked || !bs[1].Blocked {
		t.Fatal(bs)
	}
}
func TestZeroUsageIsNotMissing(t *testing.T) {
	db, q := setup(t, "10")
	reserve(t, db, q, "task_zero")
	r := settle(t, db, "task_zero", token(0), "1")
	if r.State != Settled || r.Entry.CompletionTokens == nil || r.Entry.JournalID != nil {
		t.Fatal(r)
	}
	checkBalance(t, db, "10", "0")
	if !keyUsed(t, db).IsZero() {
		t.Fatal("key not refunded")
	}
}
func TestConfirmedReleaseAndConflictingTerminal(t *testing.T) {
	db, q := setup(t, "10")
	reserve(t, db, q, "task_release")
	for _, reason := range []ReleaseReason{"timeout", "unknown", "delete_200_404"} {
		err := db.Transaction(func(tx *gorm.DB) error {
			_, e := ReleaseConfirmedTx(tx, ReleaseInput{OrgID: 10, TaskID: "task_release", Reason: reason, Confirmed: true, EvidenceID: "e", Now: testAt})
			return e
		})
		if !errors.Is(err, ErrUnconfirmedRelease) {
			t.Fatal(err)
		}
	}
	checkBalance(t, db, "10", "0.864")
	for i := 0; i < 2; i++ {
		err := db.Transaction(func(tx *gorm.DB) error {
			r, e := ReleaseConfirmedTx(tx, ReleaseInput{OrgID: 10, TaskID: "task_release", Reason: ReleaseNotDispatched, Confirmed: true, EvidenceID: "confirmed_intent", Now: testAt})
			if e == nil && r.Replay != (i == 1) {
				t.Fatal(r)
			}
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkBalance(t, db, "10", "0")
	if count(t, db, "video_billing_entries") != 1 {
		t.Fatal("duplicate release")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: "task_release", Now: testAt, Evidence: UsageEvidence{CompletionTokens: token(1)}})
		return e
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestSettlementRollbackIncludesLedgerQuotaAndEvidence(t *testing.T) {
	db, q := setup(t, "10")
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("5")})
	reserve(t, db, q, "task_rollback")
	db.Exec("CREATE TRIGGER fail_bill BEFORE INSERT ON video_billing_entries BEGIN SELECT RAISE(ABORT,'injected billing failure'); END")
	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: "task_rollback", Now: testAt, Evidence: UsageEvidence{EvidenceID: "e", Source: "task_get", SourceRecordID: "upstream", SourceVersion: "1", CompletionTokens: token(54000), ObservedAt: testAt}})
		return e
	})
	if err == nil {
		t.Fatal("injection did not fail")
	}
	checkBalance(t, db, "10", "0.864")
	if !keyUsed(t, db).Equal(d("0.864")) || count(t, db, "video_usage_evidence") != 0 {
		t.Fatal("partial settlement")
	}
	var b QuotaBucket
	db.Take(&b)
	if !b.ReservedAmount.Equal(d("0.864")) || !b.ConsumedAmount.IsZero() {
		t.Fatal(b)
	}
}
func TestCrossPeriodAndMultiScopeBudgets(t *testing.T) {
	db, q := setup(t, "10")
	for _, p := range []QuotaPolicy{{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("5"), Period: PeriodMonth}, {OrgID: 10, Scope: ScopeKey, OwnerID: 1, Metric: MetricTokens, LimitUnits: token(500000)}, {OrgID: 10, Scope: ScopeOrgModel, OwnerID: 10, Model: q.Rule.Model, Metric: MetricTasks, LimitUnits: token(5)}, {Scope: ScopeProvider, OwnerID: 11, Metric: MetricUSD, LimitAmount: amount("5")}} {
		policy(t, db, p)
	}
	reserve(t, db, q, "task_period")
	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: "task_period", Now: testAt.AddDate(0, 2, 0), Evidence: UsageEvidence{EvidenceID: "period-e", Source: "task_get", SourceRecordID: "p", SourceVersion: "1", CompletionTokens: token(108000), ObservedAt: testAt}})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	var buckets []QuotaBucket
	db.Find(&buckets)
	if len(buckets) != 4 {
		t.Fatal(buckets)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	for _, b := range buckets {
		if b.PeriodStart.In(loc).Month() != time.October || !b.ReservedAmount.IsZero() || b.ReservedUnits != 0 {
			t.Fatal(b)
		}
	}
}
func TestTransactionsTenantIsolationAndReplay(t *testing.T) {
	db, q := setup(t, "10")
	if _, err := ReserveTx(db, ReserveInput{}); !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ReserveTx(tx, ReserveInput{OrgID: 20, APIKeyID: 1, TaskID: "x", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
		return e
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	reserve(t, db, q, "task_replay")
	if !reserve(t, db, q, "task_replay").Replay {
		t.Fatal("reserve not replay")
	}
	checkBalance(t, db, "10", "0.864")
	err = db.Transaction(func(tx *gorm.DB) error {
		_, e := SettleUsageTx(tx, SettlementInput{OrgID: 20, TaskID: "task_replay", Now: testAt})
		return e
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestConcurrentAdmissionCannotExceedKeyBudget(t *testing.T) {
	db, q := setup(t, "10")
	db.Exec("UPDATE api_keys SET quota_limit=1 WHERE id=1")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- db.Transaction(func(tx *gorm.DB) error {
				_, e := ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: fmt.Sprintf("task_c%d", i), RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
				return e
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrQuotaExceeded) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d", success)
	}
	checkBalance(t, db, "10", "0.864")
}

func TestPricingConflictsAndForgedQuotesFailClosed(t *testing.T) {
	db, q := setup(t, "10")
	q.HoldUSD = d("0.01")
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: "task_forged", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	r := ruleFixture()
	r.RuleID = "overlap"
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error { _, err := ResolvePricingTx(tx, r.PricingKey, testAt); return err })
	if !errors.Is(err, ErrPricingUnavailable) {
		t.Fatal(err)
	}
	checkBalance(t, db, "10", "0")
}

func TestImmutableSnapshotSurvivesCurrentPriceChange(t *testing.T) {
	db, q := setup(t, "10")
	reserve(t, db, q, "task_snapshot")
	// Simulate administrator drift; already-created tasks must still use snapshot.
	if err := db.Model(&PricingRule{}).Where("rule_id = ?", q.Rule.RuleID).Update("retail_usd_per_1m", 80).Error; err != nil {
		t.Fatal(err)
	}
	r := settle(t, db, "task_snapshot", token(108000), "1")
	if !r.Entry.CalculatedUSD.Equal(d("0.864")) {
		t.Fatal(r.Entry)
	}
	checkBalance(t, db, "9.136", "0")
}

func TestEvidenceRevisionCannotOverwriteAndZeroQuotaMeansDeny(t *testing.T) {
	db, q := setup(t, "10")
	reserve(t, db, q, "task_evidence")
	settle(t, db, "task_evidence", nil, "1")
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: "task_evidence", Now: testAt, Evidence: UsageEvidence{EvidenceID: "new-id", Source: "task_get", SourceRecordID: "task_evidence", SourceVersion: "1", CompletionTokens: token(1), ObservedAt: testAt}})
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	checkBalance(t, db, "10", "0.864")
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricTokens, LimitUnits: token(0)})
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: "task_denied", RequestHMAC: strings.Repeat("b", 64), Quote: q, Now: testAt})
		return err
	})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatal(err)
	}
	checkBalance(t, db, "10", "0.864")
}

func TestConcurrentReplayCreatesOneHoldAndOneSnapshot(t *testing.T) {
	db, q := setup(t, "10")
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Transaction(func(tx *gorm.DB) error {
				_, err := ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: "task_same", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
				return err
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count(t, db, "ledger_holds") != 1 || count(t, db, "video_billing_snapshots") != 1 {
		t.Fatal("duplicate reservation")
	}
	checkBalance(t, db, "10", "0.864")
}

func TestConflictingBudgetVersionsRollback(t *testing.T) {
	db, q := setup(t, "10")
	for i := 0; i < 2; i++ {
		policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("5"), Version: fmt.Sprintf("v%d", i)})
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: "task_conflicting_policies", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	checkBalance(t, db, "10", "0")
	if !keyUsed(t, db).IsZero() {
		t.Fatal("legacy key quota leaked")
	}
}

func TestPolicyVersionCannotResetSpentBudget(t *testing.T) {
	db, q := setup(t, "10")
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("1"), Version: "v1"})
	reserve(t, db, q, "task_policy_v1")
	settle(t, db, "task_policy_v1", token(108000), "1")
	db.Model(&QuotaPolicy{}).Where("version = ?", "v1").Update("enabled", false)
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("1"), Version: "v2"})
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := ReserveTx(tx, ReserveInput{OrgID: 10, APIKeyID: 1, TaskID: "task_policy_v2", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
		return err
	})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatal(err)
	}
	if count(t, db, "video_quota_buckets") != 1 {
		t.Fatal("policy version reset bucket")
	}
	checkBalance(t, db, "9.136", "0")
}
