package videobilling

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Compare persisted facts, including balances, reservations and existing rows,
// rather than only counting new journals. The denied call is committed so this
// test cannot hide an early write behind the caller's normal error rollback.
func billingContractFacts(t *testing.T, db *gorm.DB) string {
	t.Helper()
	tables := []string{"api_keys", "ledger_accounts", "ledger_account_shards", "ledger_holds", "ledger_journal", "video_billing_snapshots", "video_usage_evidence", "video_billing_entries", "video_quota_buckets", "video_quota_reservations", "video_adjustment_proposals", "video_adjustment_reviews", "video_adjustment_applications", "video_provider_bill_imports", "video_provider_bill_lines", "video_provider_bill_bindings", "video_reconciliation_items"}
	facts := map[string][]map[string]any{}
	for _, table := range tables {
		rows, err := db.Raw("SELECT * FROM " + table + " ORDER BY 1").Rows()
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		var records []map[string]any
		for rows.Next() {
			values, destinations := make([]any, len(columns)), make([]any, len(columns))
			for i := range destinations {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			record := map[string]any{}
			for i, column := range columns {
				record[column] = values[i]
			}
			records = append(records, record)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		facts[table] = records
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertBillingContractDenied(t *testing.T, db *gorm.DB, call func(*gorm.DB) error) {
	t.Helper()
	before := billingContractFacts(t, db)
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := call(tx); !errors.Is(err, videoruntime.ErrUnsupported) {
			return fmt.Errorf("want runtime contract refusal, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := billingContractFacts(t, db); before != after {
		t.Fatal("unsupported billing schema changed persisted financial/evidence facts")
	}
}

func alterSnapshotSchema(t *testing.T, db *gorm.DB, schema string) {
	t.Helper()
	if err := db.Model(&Snapshot{}).Where("org_id = ? AND task_id = ?", 10, "task_1").Update("billing_schema_version", schema).Error; err != nil {
		t.Fatal(err)
	}
}

func TestReserveRequiresExplicitSupportedBillingSchema(t *testing.T) {
	for _, schema := range []string{"", "usd-decimal-v2"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/existing=%v", schema, existing), func(t *testing.T) {
				db, q := setupReconciliation(t, "100")
				if existing {
					reserve(t, db, q, "task_1")
				}
				assertBillingContractDenied(t, db, func(tx *gorm.DB) error {
					_, err := ReserveTx(tx, ReserveInput{BillingSchemaVersion: schema, OrgID: 10, APIKeyID: 1, TaskID: "task_1", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
					return err
				})
			})
		}
	}
}

func TestUnsupportedBillingSnapshotCannotReserveSettleReleaseOrDispatch(t *testing.T) {
	for _, schema := range []string{"", "usd-decimal-v2"} {
		for _, action := range []string{"reserve_replay", "settle", "missing_usage", "release", "bill_settle", "dispatch"} {
			t.Run(fmt.Sprintf("%q/%s", schema, action), func(t *testing.T) {
				db, q := setupReconciliation(t, "100")
				policy(t, db, QuotaPolicy{Scope: ScopeProvider, OwnerID: q.Rule.ProviderScopeID, Metric: MetricCNY, Period: PeriodLifetime, LimitAmount: amount("20")})
				res := reserve(t, db, q, "task_1")
				if res.Snapshot.BillingSchemaVersion != videoruntime.BillingV1 {
					t.Fatal("reservation did not persist the explicit billing contract")
				}
				line := importLine(t, db, lineFixture("1", 1, token(100), "0.0046"), bindingFixture())
				alterSnapshotSchema(t, db, schema)
				assertBillingContractDenied(t, db, func(tx *gorm.DB) error {
					switch action {
					case "reserve_replay":
						_, err := ReserveTx(tx, ReserveInput{BillingSchemaVersion: videoruntime.BillingV1, OrgID: 10, APIKeyID: 1, TaskID: "task_1", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt})
						return err
					case "settle", "missing_usage":
						n := token(100)
						if action == "missing_usage" {
							n = nil
						}
						_, err := SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: "task_1", Now: testAt, Evidence: UsageEvidence{EvidenceID: "e-schema", Source: "task_get", SourceRecordID: "task_1", SourceVersion: "1", CompletionTokens: n, ObservedAt: testAt}})
						return err
					case "release":
						_, err := ReleaseConfirmedTx(tx, ReleaseInput{OrgID: 10, TaskID: "task_1", EvidenceID: "release-1", Reason: ReleaseNotDispatched, Confirmed: true, Now: testAt})
						return err
					case "bill_settle":
						_, err := SettleBillUsageTx(tx, 10, "task_1", line.Line.ID, testAt)
						return err
					case "dispatch":
						return ValidateDispatchBudgetTx(tx, 10, "task_1")
					default:
						return errors.New("unhandled test action")
					}
				})
				checkBalance(t, db, "100", q.HoldUSD.String())
			})
		}
	}
}

func TestUnsupportedBillingSchemaBlocksAdjustmentAndReplay(t *testing.T) {
	for _, schema := range []string{"", "usd-decimal-v2"} {
		for _, action := range []string{"propose", "propose_replay", "review", "review_replay", "apply", "apply_replay", "settle_replay"} {
			t.Run(fmt.Sprintf("%q/%s", schema, action), func(t *testing.T) {
				db, q := setupReconciliation(t, "100")
				policy(t, db, QuotaPolicy{Scope: ScopeProvider, OwnerID: q.Rule.ProviderScopeID, Metric: MetricCNY, Period: PeriodLifetime, LimitAmount: amount("20")})
				reserve(t, db, q, "task_1")
				settle(t, db, "task_1", token(100), "1")
				line := importLine(t, db, lineFixture("1", 1, token(200), "0.0092"), bindingFixture())
				var p AdjustmentProposal
				if action != "propose" {
					p = propose(t, db, line.Line.ID, "proposal-1", false)
				}
				if action == "review_replay" || action == "apply" || action == "apply_replay" {
					approve(t, db, p)
				}
				if action == "apply_replay" {
					apply(t, db, p)
				}
				alterSnapshotSchema(t, db, schema)
				assertBillingContractDenied(t, db, func(tx *gorm.DB) error {
					switch action {
					case "propose", "propose_replay":
						_, err := ProposeAdjustmentTx(tx, ProposeAdjustmentInput{OrgID: 10, TaskID: "task_1", ProposalKey: "proposal-1", LineID: line.Line.ID, ProposerID: 100, Reason: "bill_correction", Now: testAt, ExpiresAt: testAt.Add(time.Hour)})
						return err
					case "review", "review_replay":
						_, err := ReviewAdjustmentTx(tx, ReviewAdjustmentInput{OrgID: 10, ProposalID: p.ID, ReviewerID: 200, Approved: true, Reason: "verified_bill", Now: testAt})
						return err
					case "apply", "apply_replay":
						_, err := ApplyAdjustmentTx(tx, ApplyAdjustmentInput{OrgID: 10, ProposalID: p.ID, Now: testAt})
						return err
					case "settle_replay":
						_, err := SettleUsageTx(tx, SettlementInput{OrgID: 10, TaskID: "task_1", Now: testAt, Evidence: UsageEvidence{CompletionTokens: token(100)}})
						return err
					default:
						return errors.New("unhandled test action")
					}
				})
			})
		}
	}
}

func TestUnsupportedBillingSchemaBlocksReleaseReplay(t *testing.T) {
	for _, schema := range []string{"", "usd-decimal-v2"} {
		t.Run(fmt.Sprintf("%q", schema), func(t *testing.T) {
			db, q := setupReconciliation(t, "100")
			reserve(t, db, q, "task_1")
			in := ReleaseInput{OrgID: 10, TaskID: "task_1", EvidenceID: "release-1", Reason: ReleaseNotDispatched, Confirmed: true, Now: testAt}
			if err := db.Transaction(func(tx *gorm.DB) error {
				_, err := ReleaseConfirmedTx(tx, in)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			alterSnapshotSchema(t, db, schema)
			assertBillingContractDenied(t, db, func(tx *gorm.DB) error {
				_, err := ReleaseConfirmedTx(tx, in)
				return err
			})
		})
	}
}

func TestValidateSnapshotContractIsScopedReadOnlyAndRequiresTransaction(t *testing.T) {
	// No key, budget or ledger table exists: this check must not introduce their
	// locks ahead of the host's asset lock order.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec("CREATE TABLE video_billing_snapshots(id INTEGER PRIMARY KEY,org_id INTEGER NOT NULL,task_id TEXT NOT NULL,billing_schema_version TEXT NOT NULL); INSERT INTO video_billing_snapshots VALUES(1,10,'task_1','usd-decimal-v1'),(2,10,'task_empty',''),(3,10,'task_unknown','usd-decimal-v2')").Error; err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshotContractTx(db, 10, "task_1", 1, videoruntime.BillingV1); !errors.Is(err, ErrTransactionRequired) {
		t.Fatalf("global DB error=%v", err)
	}
	for _, tc := range []struct {
		name, task, expected string
		org, snapshot        uint64
		want                 error
	}{
		{"supported", "task_1", videoruntime.BillingV1, 10, 1, nil},
		{"wrong organization", "task_1", videoruntime.BillingV1, 20, 1, ErrNotFound},
		{"wrong task", "task_other", videoruntime.BillingV1, 10, 1, ErrNotFound},
		{"wrong snapshot", "task_1", videoruntime.BillingV1, 10, 2, ErrNotFound},
		{"missing snapshot", "task_1", videoruntime.BillingV1, 10, 99, ErrNotFound},
		{"empty expected", "task_1", "", 10, 1, videoruntime.ErrUnsupported},
		{"unknown expected", "task_1", "usd-decimal-v2", 10, 1, videoruntime.ErrUnsupported},
		{"empty stored", "task_empty", videoruntime.BillingV1, 10, 2, videoruntime.ErrUnsupported},
		{"unknown stored", "task_unknown", videoruntime.BillingV1, 10, 3, videoruntime.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				return ValidateSnapshotContractTx(tx, tc.org, tc.task, tc.snapshot, tc.expected)
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
}
