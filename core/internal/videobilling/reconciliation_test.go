package videobilling

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"gorm.io/gorm"
)

const reconciliationSchema = `
CREATE TABLE video_provider_bill_imports(id INTEGER PRIMARY KEY AUTOINCREMENT,import_key TEXT NOT NULL,provider_scope_id INTEGER NOT NULL,source TEXT NOT NULL,source_version TEXT NOT NULL,source_hash TEXT NOT NULL,payload_ref TEXT NOT NULL,expected_rows INTEGER NOT NULL,state TEXT NOT NULL,created_at DATETIME NOT NULL,completed_at DATETIME,UNIQUE(provider_scope_id,import_key));
CREATE TABLE video_provider_bill_lines(id INTEGER PRIMARY KEY AUTOINCREMENT,provider_scope_id INTEGER NOT NULL,source TEXT NOT NULL,source_record_id TEXT NOT NULL,source_version TEXT NOT NULL,source_revision INTEGER NOT NULL,content_hash TEXT NOT NULL,evidence_hash TEXT NOT NULL,payload_ref TEXT NOT NULL,granularity TEXT NOT NULL,upstream_task_id TEXT NOT NULL,model TEXT NOT NULL,period_start DATETIME NOT NULL,period_end DATETIME NOT NULL,completion_tokens INTEGER,amount_cny NUMERIC NOT NULL,observed_at DATETIME NOT NULL,UNIQUE(provider_scope_id,source,source_record_id,source_version),UNIQUE(provider_scope_id,source,source_record_id,source_revision));
CREATE TABLE video_provider_bill_import_rows(id INTEGER PRIMARY KEY AUTOINCREMENT,import_id INTEGER NOT NULL,source_ordinal INTEGER NOT NULL,line_id INTEGER NOT NULL,content_hash TEXT NOT NULL,UNIQUE(import_id,source_ordinal));
CREATE TABLE video_provider_bill_heads(id INTEGER PRIMARY KEY AUTOINCREMENT,provider_scope_id INTEGER NOT NULL,source TEXT NOT NULL,source_record_id TEXT NOT NULL,source_revision INTEGER NOT NULL,line_id INTEGER NOT NULL,UNIQUE(provider_scope_id,source,source_record_id));
CREATE TABLE video_provider_bill_bindings(line_id INTEGER PRIMARY KEY,org_id INTEGER NOT NULL,task_id TEXT NOT NULL,binding_version TEXT NOT NULL,created_at DATETIME NOT NULL);
CREATE TABLE video_reconciliation_items(id INTEGER PRIMARY KEY AUTOINCREMENT,line_id INTEGER NOT NULL,org_id INTEGER NOT NULL,task_id TEXT NOT NULL,binding_version TEXT NOT NULL,state TEXT NOT NULL,reason TEXT NOT NULL,initial_entry_id INTEGER,observed_tokens INTEGER,expected_tokens INTEGER,observed_cny NUMERIC NOT NULL,expected_cny NUMERIC NOT NULL,difference_cny NUMERIC NOT NULL,created_at DATETIME NOT NULL,UNIQUE(line_id,binding_version));
CREATE TABLE video_adjustment_proposals(id INTEGER PRIMARY KEY AUTOINCREMENT,proposal_key TEXT NOT NULL,org_id INTEGER NOT NULL,task_id TEXT NOT NULL,line_id INTEGER NOT NULL,proposer_id INTEGER NOT NULL,reason TEXT NOT NULL,supplier_only INTEGER NOT NULL,base_application_id INTEGER NOT NULL,base_net_usd NUMERIC NOT NULL,target_net_usd NUMERIC NOT NULL,target_calculated_usd NUMERIC NOT NULL,target_cny NUMERIC NOT NULL,target_supplier_usd NUMERIC NOT NULL,target_tokens INTEGER,created_at DATETIME NOT NULL,expires_at DATETIME NOT NULL,UNIQUE(org_id,proposal_key));
CREATE TABLE video_adjustment_reviews(id INTEGER PRIMARY KEY AUTOINCREMENT,proposal_id INTEGER NOT NULL UNIQUE,org_id INTEGER NOT NULL,reviewer_id INTEGER NOT NULL,approved INTEGER NOT NULL,reason TEXT NOT NULL,created_at DATETIME NOT NULL);
CREATE TABLE video_adjustment_applications(id INTEGER PRIMARY KEY AUTOINCREMENT,proposal_id INTEGER NOT NULL UNIQUE,review_id INTEGER NOT NULL,org_id INTEGER NOT NULL,task_id TEXT NOT NULL,line_id INTEGER NOT NULL,journal_id INTEGER UNIQUE,customer_delta_usd NUMERIC NOT NULL,supplier_delta_cny NUMERIC NOT NULL,supplier_delta_usd NUMERIC NOT NULL,tokens_delta INTEGER NOT NULL,net_usd NUMERIC NOT NULL,calculated_usd NUMERIC NOT NULL,absorbed_usd NUMERIC NOT NULL,supplier_cny NUMERIC NOT NULL,supplier_usd NUMERIC NOT NULL,completion_tokens INTEGER,created_at DATETIME NOT NULL);
`

func setupReconciliation(t *testing.T, balance string) (*gorm.DB, QuoteResult) {
	t.Helper()
	db, q := setup(t, balance)
	if err := db.Exec(reconciliationSchema).Error; err != nil {
		t.Fatal(err)
	}
	return db, q
}
func beginImport(t *testing.T, db *gorm.DB, key string, rows int64) BillImport {
	t.Helper()
	var out BeginImportResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = BeginImportTx(tx, BeginImportInput{ImportKey: key, ProviderScopeID: 11, Source: "official-bill", SourceVersion: "export-v1", SourceHash: strings.Repeat("b", 64), PayloadRef: "payload-bill", ExpectedRows: rows, Now: testAt})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Import
}
func lineFixture(version string, revision int64, n *int64, cny string) BillLine {
	return BillLine{ProviderScopeID: 11, Source: "official-bill", SourceRecordID: "record-upstream-1", SourceVersion: version, SourceRevision: revision, EvidenceHash: strings.Repeat("b", 64), PayloadRef: "payload-line", Granularity: BillTaskTotal, UpstreamTaskID: "upstream-1", Model: "seedance-2.0", PeriodStart: testAt.Add(-time.Hour), PeriodEnd: testAt, CompletionTokens: n, AmountCNY: d(cny), ObservedAt: testAt}
}
func bindingFixture() *VerifiedBinding {
	return &VerifiedBinding{ProviderScopeID: 11, UpstreamTaskID: "upstream-1", OrgID: 10, TaskID: "task_1", BindingVersion: "binding-v1"}
}
func importLine(t *testing.T, db *gorm.DB, l BillLine, b *VerifiedBinding) ImportLineResult {
	t.Helper()
	imp := beginImport(t, db, "import-"+l.SourceVersion, 1)
	var out ImportLineResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = ImportLineTx(tx, ImportLineInput{ImportID: imp.ID, RowNumber: 1, Line: l, Binding: b, Now: testAt})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func propose(t *testing.T, db *gorm.DB, lineID uint64, key string, supplierOnly bool) AdjustmentProposal {
	t.Helper()
	var p AdjustmentProposal
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		p, err = ProposeAdjustmentTx(tx, ProposeAdjustmentInput{OrgID: 10, TaskID: "task_1", ProposalKey: key, LineID: lineID, ProposerID: 100, Reason: "bill_correction", SupplierOnly: supplierOnly, Now: testAt, ExpiresAt: testAt.Add(time.Hour)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func approve(t *testing.T, db *gorm.DB, p AdjustmentProposal) {
	t.Helper()
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := ReviewAdjustmentTx(tx, ReviewAdjustmentInput{OrgID: 10, ProposalID: p.ID, ReviewerID: 200, Approved: true, Reason: "verified_bill", Now: testAt})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func apply(t *testing.T, db *gorm.DB, p AdjustmentProposal) AdjustmentResult {
	t.Helper()
	var r AdjustmentResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		r, err = ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 10, ProposalID: p.ID, Now: testAt})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBillImportResumesDeduplicatesAndConflicts(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	reserve(t, db, q, "task_1")
	settle(t, db, "task_1", token(54000), "v1")
	i := beginImport(t, db, "first", 2)
	l := lineFixture("v1", 1, token(54000), "2.484")
	put := func(row int64, l BillLine) (out ImportLineResult, err error) {
		err = db.Transaction(func(tx *gorm.DB) error {
			var e error
			out, e = ImportLineTx(tx, ImportLineInput{ImportID: i.ID, RowNumber: row, Line: l, Binding: bindingFixture(), Now: testAt})
			return e
		})
		return
	}
	r, err := put(1, l)
	if err != nil || r.Item.State != ReconcileMatched {
		t.Fatal(r, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { _, e := CompleteImportTx(tx, i.ID, testAt); return e }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	r, err = put(1, l)
	if err != nil || !r.Replay {
		t.Fatal(r, err)
	}
	bad := l
	bad.AmountCNY = d("2.5")
	if _, err := put(1, bad); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := put(2, l); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { _, e := CompleteImportTx(tx, i.ID, testAt); return e }); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "video_provider_bill_lines") != 1 || count(t, db, "video_provider_bill_import_rows") != 2 {
		t.Fatal("duplicate evidence")
	}
	other := beginImport(t, db, "second", 1)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ImportLineTx(tx, ImportLineInput{ImportID: other.ID, RowNumber: 1, Line: bad, Now: testAt})
		return e
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := BeginImportTx(tx, BeginImportInput{ImportKey: "first", ProviderScopeID: 11, Source: "official-bill", SourceVersion: "export-v1", SourceHash: strings.Repeat("c", 64), PayloadRef: "payload", ExpectedRows: 2, Now: testAt})
		return e
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	checkBalance(t, db, "9.568", "0")
}

func TestQuarantineRecoveryAndNoAutomaticSettlement(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	reserve(t, db, q, "task_1")
	r := importLine(t, db, lineFixture("v1", 1, token(0), "0"), nil)
	if r.Item.State != ReconcileQuarantined {
		t.Fatal(r)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		item, e := BindBillLineTx(tx, r.Line.ID, *bindingFixture(), testAt)
		if e == nil && item.State != ReconcilePending {
			t.Fatal(item)
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "video_reconciliation_items") != 2 {
		t.Fatal("recovery must append")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ProposeAdjustmentTx(tx, ProposeAdjustmentInput{OrgID: 10, TaskID: "task_1", ProposalKey: "p", LineID: r.Line.ID, ProposerID: 100, Reason: "bill", Now: testAt, ExpiresAt: testAt.Add(time.Hour)})
		return e
	}); !errors.Is(err, ErrNotSettled) {
		t.Fatal(err)
	}
	checkBalance(t, db, "10", "0.864")
	agg := lineFixture("agg", 1, token(100000), "4.6")
	agg.SourceRecordID = "aggregate"
	agg.Granularity = BillAggregate
	agg.UpstreamTaskID = ""
	a := importLine(t, db, agg, nil)
	if a.Item.Reason != "aggregate_not_task_usage" {
		t.Fatal(a)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { _, e := BindBillLineTx(tx, a.Line.ID, *bindingFixture(), testAt); return e }); !errors.Is(err, ErrUnmapped) {
		t.Fatal(err)
	}
	bad := *bindingFixture()
	bad.OrgID = 20
	if err := db.Transaction(func(tx *gorm.DB) error { _, e := BindBillLineTx(tx, r.Line.ID, bad, testAt); return e }); !errors.Is(err, ErrUnmapped) {
		t.Fatal(err)
	}
}

func TestBillRevisionOrderingAndMappingIsolation(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	reserve(t, db, q, "task_1")
	settle(t, db, "task_1", token(54000), "v1")
	newer := importLine(t, db, lineFixture("v2", 2, token(60000), "2.76"), bindingFixture())
	older := importLine(t, db, lineFixture("v1", 1, token(54000), "2.484"), bindingFixture())
	if older.Item.State != ReconcileStale || newer.Item.State != ReconcileDifference {
		t.Fatal(older, newer)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ProposeAdjustmentTx(tx, ProposeAdjustmentInput{OrgID: 10, TaskID: "task_1", ProposalKey: "old", LineID: older.Line.ID, ProposerID: 100, Reason: "bill", Now: testAt, ExpiresAt: testAt.Add(time.Hour)})
		return e
	}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	i := beginImport(t, db, "duplicate-revision", 1)
	conflict := lineFixture("other-v2", 2, token(60000), "2.76")
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ImportLineTx(tx, ImportLineInput{ImportID: i.ID, RowNumber: 1, Line: conflict, Now: testAt})
		return e
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	bad := *bindingFixture()
	bad.ProviderScopeID = 12
	if err := db.Transaction(func(tx *gorm.DB) error { _, e := BindBillLineTx(tx, newer.Line.ID, bad, testAt); return e }); !errors.Is(err, ErrUnmapped) {
		t.Fatal(err)
	}
	checkBalance(t, db, "9.568", "0")
}

func TestReviewedAdjustmentDebitRefundCapAndAppendOnly(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("10")})
	policy(t, db, QuotaPolicy{Scope: ScopeProvider, OwnerID: 11, Metric: MetricCNY, LimitAmount: amount("20")})
	policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricTokens, LimitUnits: token(200000)})
	reserve(t, db, q, "task_1")
	settle(t, db, "task_1", token(54000), "v1")
	l := importLine(t, db, lineFixture("v1", 1, token(216000), "9.936"), bindingFixture())
	p := propose(t, db, l.Line.ID, "debit", false)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 10, ProposalID: p.ID, Now: testAt})
		return e
	}); !errors.Is(err, ErrReviewRequired) {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ReviewAdjustmentTx(tx, ReviewAdjustmentInput{OrgID: 10, ProposalID: p.ID, ReviewerID: 100, Approved: true, Reason: "self", Now: testAt})
		return e
	}); !errors.Is(err, ErrReviewRequired) {
		t.Fatal(err)
	}
	approve(t, db, p)
	a := apply(t, db, p)
	if !a.Application.CustomerDeltaUSD.Equal(d("0.432")) || !a.Application.NetUSD.Equal(d("0.864")) || !a.Application.AbsorbedUSD.Equal(d("0.864")) || !a.QuotaOverrun {
		t.Fatal(a)
	}
	if !apply(t, db, p).Replay {
		t.Fatal("not replay")
	}
	checkBalance(t, db, "9.136", "0")
	l2 := importLine(t, db, lineFixture("v2", 2, token(27000), "1.242"), bindingFixture())
	p2 := propose(t, db, l2.Line.ID, "refund", false)
	approve(t, db, p2)
	a2 := apply(t, db, p2)
	if !a2.Application.CustomerDeltaUSD.Equal(d("-0.648")) || a2.Application.TokensDelta != -189000 {
		t.Fatal(a2)
	}
	checkBalance(t, db, "9.784", "0")
	if !keyUsed(t, db).Equal(d("0.216")) {
		t.Fatal(keyUsed(t, db))
	}
	if count(t, db, "video_billing_entries") != 1 || count(t, db, "video_adjustment_applications") != 2 || count(t, db, "ledger_journal") != 4 {
		t.Fatal("history overwritten or duplicated")
	}
	var b QuotaBucket
	if err := db.Joins("JOIN video_quota_policies p ON p.id=video_quota_buckets.policy_id").Where("p.metric = ?", MetricTokens).Select("video_quota_buckets.*").Take(&b).Error; err != nil {
		t.Fatal(err)
	}
	if b.ConsumedUnits != 27000 || !b.Blocked {
		t.Fatal(b)
	} // correction never silently unblocks
}

func TestSupplierOnlyCorrectionNeverChangesCustomer(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	reserve(t, db, q, "task_1")
	settle(t, db, "task_1", token(54000), "v1")
	l := importLine(t, db, lineFixture("v1", 1, nil, "0"), bindingFixture())
	p := propose(t, db, l.Line.ID, "supplier", true)
	approve(t, db, p)
	a := apply(t, db, p)
	if !a.Application.CustomerDeltaUSD.IsZero() || a.Application.CompletionTokens == nil || *a.Application.CompletionTokens != 54000 || !a.Application.SupplierCNY.IsZero() || a.Application.JournalID != nil {
		t.Fatal(a)
	}
	checkBalance(t, db, "9.568", "0")
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ProposeAdjustmentTx(tx, ProposeAdjustmentInput{OrgID: 10, TaskID: "task_1", ProposalKey: "bad", LineID: l.Line.ID, ProposerID: 100, Reason: "bill", Now: testAt, ExpiresAt: testAt.Add(time.Hour)})
		return e
	}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestAdjustmentRequiresFreshReviewedBasisAndTenant(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	reserve(t, db, q, "task_1")
	settle(t, db, "task_1", token(54000), "v1")
	l := importLine(t, db, lineFixture("v1", 1, token(27000), "1.242"), bindingFixture())
	p := propose(t, db, l.Line.ID, "p1", false)
	p2 := propose(t, db, l.Line.ID, "p2", false)
	approve(t, db, p)
	approve(t, db, p2)
	apply(t, db, p)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 10, ProposalID: p2.ID, Now: testAt})
		return e
	}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 20, ProposalID: p.ID, Now: testAt})
		return e
	}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	l2 := importLine(t, db, lineFixture("v2", 2, token(30000), "1.38"), bindingFixture())
	p3 := propose(t, db, l2.Line.ID, "p3", false)
	approve(t, db, p3)
	importLine(t, db, lineFixture("v3", 3, token(31000), "1.426"), bindingFixture())
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 10, ProposalID: p3.ID, Now: testAt})
		return e
	}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}

func TestAdjustmentQuotaAndBalanceFailuresRollback(t *testing.T) {
	for _, mode := range []string{"balance", "key", "bucket", "late_failure"} {
		t.Run(mode, func(t *testing.T) {
			db, q := setupReconciliation(t, "1")
			policy(t, db, QuotaPolicy{OrgID: 10, Scope: ScopeOrg, OwnerID: 10, Metric: MetricUSD, LimitAmount: amount("1")})
			reserve(t, db, q, "task_1")
			settle(t, db, "task_1", token(54000), "v1")
			l := importLine(t, db, lineFixture("v1", 1, token(108000), "4.968"), bindingFixture())
			p := propose(t, db, l.Line.ID, "p", false)
			approve(t, db, p)
			switch mode {
			case "balance":
				if err := db.Transaction(func(tx *gorm.DB) error {
					a, e := ledger.EnsureAccount(tx, 10, ledger.AccountUserCredit)
					if e != nil {
						return e
					}
					_, _, e = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "drain", AccountID: a.ID, Kind: ledger.KindDebit, Delta: d("-0.5"), RefType: "test", RefID: "drain"})
					return e
				}); err != nil {
					t.Fatal(err)
				}
			case "key":
				db.Exec("UPDATE api_keys SET quota_limit=0.5 WHERE id=1")
			case "bucket":
				db.Exec("UPDATE video_quota_buckets SET limit_amount=0.5")
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				_, e := ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 10, ProposalID: p.ID, Now: testAt})
				if e == nil && mode == "late_failure" {
					return errors.New("outbox failed")
				}
				return e
			})
			if err == nil {
				t.Fatal("expected rollback")
			}
			if count(t, db, "video_adjustment_applications") != 0 || !keyUsed(t, db).Equal(d("0.432")) {
				t.Fatal("partial adjustment")
			}
			posted := "0.568"
			if mode == "balance" {
				posted = "0.068"
			}
			checkBalance(t, db, posted, "0")
		})
	}
}

func TestConcurrentAdjustmentReplayAndExpiredReview(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	reserve(t, db, q, "task_1")
	settle(t, db, "task_1", token(54000), "v1")
	l := importLine(t, db, lineFixture("v1", 1, token(27000), "1.242"), bindingFixture())
	p := propose(t, db, l.Line.ID, "p", false)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ReviewAdjustmentTx(tx, ReviewAdjustmentInput{OrgID: 10, ProposalID: p.ID, ReviewerID: 200, Approved: true, Reason: "late", Now: testAt.Add(2 * time.Hour)})
		return e
	}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	approve(t, db, p)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Transaction(func(tx *gorm.DB) error {
				_, e := ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 10, ProposalID: p.ID, Now: testAt})
				return e
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
	if count(t, db, "video_adjustment_applications") != 1 {
		t.Fatal("duplicate adjustment")
	}
	checkBalance(t, db, "9.784", "0")
}

func TestExplicitBillRecoveryMissingUsageAndCappedInitialSettlement(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	reserve(t, db, q, "task_1")
	l := importLine(t, db, lineFixture("v1", 1, nil, "4.968"), bindingFixture())
	var first SettlementResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		first, e = SettleBillUsageTx(tx, 10, "task_1", l.Line.ID, testAt)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if first.State != PendingUsage || first.Entry != nil {
		t.Fatal(first)
	}
	checkBalance(t, db, "10", "0.864")
	l2 := importLine(t, db, lineFixture("v2", 2, token(216000), "9.936"), bindingFixture())
	var final SettlementResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		final, e = SettleBillUsageTx(tx, 10, "task_1", l2.Line.ID, testAt)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if final.State != Settled || !final.QuotaOverrun || !final.Entry.CapturedUSD.Equal(d("0.864")) || *final.Entry.CompletionTokens != 216000 {
		t.Fatal(final)
	}
	checkBalance(t, db, "9.136", "0")
	if err := db.Transaction(func(tx *gorm.DB) error { _, e := SettleBillUsageTx(tx, 20, "task_1", l2.Line.ID, testAt); return e }); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if count(t, db, "video_adjustment_applications") != 0 {
		t.Fatal("recovery auto-adjusted")
	}
}

func TestRejectedReviewIdentityAndImmutableBillSeries(t *testing.T) {
	db, q := setupReconciliation(t, "10")
	reserve(t, db, q, "task_1")
	settle(t, db, "task_1", token(54000), "v1")
	l := importLine(t, db, lineFixture("v1", 1, token(27000), "1.242"), bindingFixture())
	p := propose(t, db, l.Line.ID, "p", false)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ReviewAdjustmentTx(tx, ReviewAdjustmentInput{OrgID: 10, ProposalID: p.ID, ReviewerID: 200, Approved: false, Reason: "insufficient_evidence", Now: testAt})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 10, ProposalID: p.ID, Now: testAt})
		return e
	}); !errors.Is(err, ErrReviewRequired) {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ReviewAdjustmentTx(tx, ReviewAdjustmentInput{OrgID: 10, ProposalID: p.ID, ReviewerID: 200, Approved: true, Reason: "changed_mind", Now: testAt})
		return e
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ProposeAdjustmentTx(tx, ProposeAdjustmentInput{OrgID: 10, TaskID: "task_1", ProposalKey: "p", LineID: l.Line.ID, ProposerID: 100, Reason: "bill_correction", SupplierOnly: true, Now: testAt, ExpiresAt: testAt.Add(time.Hour)})
		return e
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	imp := beginImport(t, db, "moved-series", 1)
	changed := lineFixture("v2", 2, token(27000), "1.242")
	changed.UpstreamTaskID = "different-upstream"
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := ImportLineTx(tx, ImportLineInput{ImportID: imp.ID, RowNumber: 1, Line: changed, Now: testAt})
		return e
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	credit := lineFixture("aggregate-credit", 1, nil, "-1.2")
	credit.Granularity = BillAggregate
	credit.UpstreamTaskID = ""
	credit.SourceRecordID = "aggregate-credit"
	c := importLine(t, db, credit, nil)
	if c.Item.State != ReconcileQuarantined || !c.Line.AmountCNY.Equal(d("-1.2")) {
		t.Fatal(c)
	}
	checkBalance(t, db, "9.568", "0")
}

func TestBillTxAPIsRejectGlobalDB(t *testing.T) {
	db, _ := setupReconciliation(t, "10")
	_, err := BeginImportTx(db, BeginImportInput{})
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	_, err = ImportLineTx(db, ImportLineInput{})
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	_, err = CompleteImportTx(db, 1, testAt)
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	_, err = BindBillLineTx(db, 1, *bindingFixture(), testAt)
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	_, err = SettleBillUsageTx(db, 10, "task_1", 1, testAt)
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	_, err = ProposeAdjustmentTx(db, ProposeAdjustmentInput{})
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	_, err = ReviewAdjustmentTx(db, ReviewAdjustmentInput{})
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
	_, err = ApplyAdjustmentTx(db, ApplyAdjustmentInput{})
	if !errors.Is(err, ErrTransactionRequired) {
		t.Fatal(err)
	}
}
