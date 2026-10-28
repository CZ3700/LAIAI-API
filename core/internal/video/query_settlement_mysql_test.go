package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func settlementReadReceipt(t *testing.T, f *fixture, row QueryReceipt) QueryReceipt {
	t.Helper()
	var current QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND attempt_id = ?", row.OrgID, row.TaskID, row.AttemptID).Take(&current).Error)
	return current
}

func settlementMakeDue(t *testing.T, f *fixture, row QueryReceipt) QueryReceipt {
	t.Helper()
	q := f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL", row.OrgID, row.TaskID, row.AttemptID, row.RecoveryVersion).Update("recovery_due_at", gorm.Expr("UTC_TIMESTAMP(3) - INTERVAL 1 SECOND"))
	must(t, q.Error)
	if q.RowsAffected != 1 {
		t.Fatal("exact due precondition did not affect one row")
	}
	got := settlementReadReceipt(t, f, row)
	copy := got
	copy.RecoveryDueAt = row.RecoveryDueAt
	if !reflect.DeepEqual(copy, row) {
		t.Fatal("due fixture changed evidence")
	}
	return got
}

func settlementMoneyAndProjections(t *testing.T, f *fixture, id string) string {
	t.Helper()
	var usages []videobilling.UsageEvidence
	var invocations []invocation.Invocation
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("id").Find(&usages).Error)
	must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Order("id").Find(&invocations).Error)
	stored := []any{authorityFinancialFacts(t, f, id), generationTaskStorage(t, f, id)}
	for _, row := range usages {
		stored = append(stored, executionRecoveryStorageFields(row))
	}
	for _, row := range invocations {
		stored = append(stored, executionRecoveryStorageFields(row))
	}
	raw, err := json.Marshal(stored)
	must(t, err)
	return string(raw)
}

func TestMySQLQuerySettlementMoneyAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name                                                               string
		tokens                                                             int64
		nocharge                                                           bool
		captured, released, supplierCNY, calculated, absorbed, supplierUSD string
	}{
		{"positive", 100, false, "0.0008", "0.8632", "0.0046", "0.0008", "0", "0.000644"},
		{"zero", 0, false, "0", "0.864", "0", "0", "0", "0"},
		{"nocharge", 0, true, "0", "0.864", "0", "0", "0", "0"},
		{"overhold", 200000, false, "0.864", "0", "9.2", "1.6", "0.736", "1.288"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "settlement-"+tc.name)
			funds := authorityFinancialFacts(t, f, v.ID)
			status := ark.StatusSucceeded
			usage := &tc.tokens
			if tc.nocharge {
				status = ark.StatusCancelled
				usage = nil
			}
			row := settlementPoll(t, f, v.ID, status, usage)
			if authorityFinancialFacts(t, f, v.ID) != funds || row.AppliedAt != nil || row.RecoveryVersion != 1 || row.SettlementEvidenceSHA256 == "" {
				t.Fatal("G changed money or did not leave B pending")
			}
			var generated task.Task
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&generated).Error)
			if generated.Status != string(status) || generated.ActualTokens != 0 || !generated.CostUSD.IsZero() {
				t.Fatal("G projected money or omitted generation")
			}
			settlementForbidNetwork(t, f)
			posts, gets := f.posts.Load(), f.gets.Load()
			must(t, f.s.ProcessSettlementOnce(context.Background()))
			got := settlementReadReceipt(t, f, row)
			if got.AppliedAt == nil || got.RecoveryVersion != row.RecoveryVersion+1 || got.SettlementEvidenceSHA256 != row.SettlementEvidenceSHA256 || got.ApplyErrorCode != "" {
				t.Fatal("B completion not atomic or evidence changed")
			}
			var entry videobilling.BillingEntry
			must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND settlement_key = ?", f.key.OrgID, v.ID, "initial").Take(&entry).Error)
			if entry.CapturedUSD.String() != tc.captured || entry.ReleasedUSD.String() != tc.released || entry.SupplierCNY.String() != tc.supplierCNY || entry.CalculatedUSD.String() != tc.calculated || entry.AbsorbedUSD.String() != tc.absorbed || entry.SupplierUSD.String() != tc.supplierUSD {
				t.Fatal("immutable price/FX settlement differs from literal money oracle")
			}
			if tc.tokens == 0 && entry.JournalID != nil {
				t.Fatal("zero capture unexpectedly required a journal")
			}
			var current task.Task
			var st State
			var key identity.APIKey
			var account ledger.Account
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&current).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&st).Error)
			must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, f.key.ID).Take(&key).Error)
			must(t, f.s.DB.Where("org_id = ? AND type = ? AND shard = 0", f.key.OrgID, ledger.AccountUserCredit).Take(&account).Error)
			if current.Status != generated.Status || current.ActualTokens != tc.tokens || current.CostUSD.String() != tc.captured || key.UsedAmount.String() != tc.captured || !account.HeldAmount.IsZero() || !account.PostedBalance.Equal(decimal.NewFromInt(100).Sub(entry.CapturedUSD)) {
				t.Fatal("task/Key/account projection is inconsistent")
			}
			var reservations []videobilling.QuotaReservation
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Find(&reservations).Error)
			if len(reservations) != 1 || !reservations[0].Supplier || reservations[0].ReservedAmount.String() != "4.968" || reservations[0].ActualAmount.String() != tc.supplierCNY {
				t.Fatal("supplier quota changed reservation or actual amount")
			}
			var bucket videobilling.QuotaBucket
			must(t, f.s.DB.Where("id = ?", reservations[0].BucketID).Take(&bucket).Error)
			if bucket.OrgID != 0 || !bucket.ReservedAmount.IsZero() || bucket.ConsumedAmount.String() != tc.supplierCNY {
				t.Fatal("provider org0 quota not finalized")
			}
			var logs []invocation.Invocation
			must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Find(&logs).Error)
			if tc.nocharge {
				if st.BillingState != "released" || entry.CompletionTokens != nil || len(logs) != 0 {
					t.Fatal("release turned into usage")
				}
			} else {
				if st.BillingState != "settled" || st.ObservedCompletionTokens == nil || *st.ObservedCompletionTokens != tc.tokens || st.TotalTokens == nil || *st.TotalTokens != tc.tokens || len(logs) != 1 || logs[0].CompletionTokens != tc.tokens || logs[0].CostUSD.String() != tc.captured {
					t.Fatal("usage financial projections missing")
				}
				var evidence videobilling.UsageEvidence
				must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND evidence_id = ?", f.key.OrgID, v.ID, row.AttemptID).Take(&evidence).Error)
				if evidence.Source != "task_get" || evidence.SourceVersion != row.AttemptID || evidence.SourceRecordID != current.UpstreamTaskID || evidence.PayloadRef != row.PayloadRef || !evidence.ObservedAt.Equal(row.ReceivedAt) {
					t.Fatal("B changed original evidence provenance")
				}
			}
			before := settlementMoneyAndProjections(t, f, v.ID)
			must(t, f.s.ProcessSettlementOnce(context.Background()))
			r, err := f.s.applyQuerySettlement(context.Background(), row)
			must(t, err)
			if r.disposition != queryRecoveryNoop || settlementMoneyAndProjections(t, f, v.ID) != before || !reflect.DeepEqual(got, settlementReadReceipt(t, f, row)) || posts != f.posts.Load() || gets != f.gets.Load() {
				t.Fatal("replay changed money, progress, event or network count")
			}
		})
	}
}

func TestMySQLQuerySettlementFinancialTransactionRollback(t *testing.T) {
	for _, boundary := range []string{"quota", "ledger", "key", "initial", "invocation", "event", "outbox", "completion"} {
		t.Run(boundary, func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "rollback-"+boundary)
			tokens := int64(100)
			row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &tokens)
			settlementForbidNetwork(t, f)
			money := authorityFinancialFacts(t, f, v.ID)
			var beforeTask task.Task
			var beforeState State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&beforeTask).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&beforeState).Error)
			var invBefore, eventBefore, outboxBefore int64
			must(t, f.s.DB.Model(&invocation.Invocation{}).Where("org_id = ?", f.key.OrgID).Count(&invBefore).Error)
			must(t, f.s.DB.Model(&Event{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&eventBefore).Error)
			must(t, f.s.DB.Model(&Outbox{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&outboxBefore).Error)
			name := "test:settlement-rollback-" + boundary
			hits := 0
			inject := func(q *gorm.DB) {
				if hits != 0 {
					return
				}
				table := q.Statement.Table
				matched := (boundary == "quota" && table == "video_quota_buckets") || (boundary == "ledger" && table == "ledger_journal") || (boundary == "initial" && table == "video_billing_entries") || (boundary == "invocation" && table == "invocations") || (boundary == "event" && table == "video_events") || (boundary == "outbox" && table == "video_outbox")
				if boundary == "key" {
					// returnLegacyKey uses Exec, so this exact mutation is observed
					// through Raw callbacks rather than GORM model Update callbacks.
					const keyReturnSQL = "UPDATE api_keys SET used_amount = ROUND(used_amount - ?, 8) WHERE id = ? AND org_id = ? AND ROUND(used_amount - ?, 8) >= 0"
					vars := q.Statement.Vars
					matched = q.Statement.SQL.String() == keyReturnSQL && len(vars) == 4 && vars[1] == f.key.ID && vars[2] == f.key.OrgID
				}
				if boundary == "completion" && table == "video_query_receipts" {
					if updates, ok := q.Statement.Dest.(map[string]any); ok {
						_, matched = updates["applied_at"]
					}
				}
				if matched {
					hits++
					q.AddError(q.Session(&gorm.Session{NewDB: true}).Exec("SELECT settlement_missing_column FROM video_billing_entries LIMIT 1").Error)
				}
			}
			must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, inject))
			must(t, f.s.DB.Callback().Update().Before("gorm:update").Register(name, inject))
			must(t, f.s.DB.Callback().Raw().Before("gorm:raw").Register(name, inject))
			result, err := f.s.applyQuerySettlement(context.Background(), row)
			must(t, f.s.DB.Callback().Create().Remove(name))
			must(t, f.s.DB.Callback().Update().Remove(name))
			must(t, f.s.DB.Callback().Raw().Remove(name))
			var driver *drivermysql.MySQLError
			if hits != 1 || !errors.As(err, &driver) || driver.Number != 1054 || result.disposition != queryRecoveryDeferred || strings.Contains(err.Error(), "settlement_missing_column") {
				t.Fatalf("real SQL rollback not safely classified: hits=%d result=%d", hits, result.disposition)
			}
			if authorityFinancialFacts(t, f, v.ID) != money {
				t.Fatal("SQL failure left money/quota/Key/initial committed")
			}
			var afterTask task.Task
			var afterState State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&afterTask).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&afterState).Error)
			afterState.SyncState = beforeState.SyncState
			// The separate retry diagnostic updates sync_state and its ORM
			// updated_at. Every generation and financial field remains equal.
			afterState.UpdatedAt = beforeState.UpdatedAt
			if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeState, afterState) {
				t.Fatal("financial failure changed G/task projection beyond retry diagnostic")
			}
			var counts [3]int64
			must(t, f.s.DB.Model(&invocation.Invocation{}).Where("org_id = ?", f.key.OrgID).Count(&counts[0]).Error)
			must(t, f.s.DB.Model(&Event{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&counts[1]).Error)
			must(t, f.s.DB.Model(&Outbox{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&counts[2]).Error)
			if counts != [3]int64{invBefore, eventBefore, outboxBefore} {
				t.Fatal("financial rollback left invocation/event/outbox")
			}
			retry := settlementReadReceipt(t, f, row)
			if retry.AppliedAt != nil || retry.RecoveryDueAt == nil || retry.RecoveryVersion != row.RecoveryVersion+1 || retry.ApplyErrorCode != "query_apply_failed" {
				t.Fatal("temporary failure became completed/permanent")
			}
			retry = settlementMakeDue(t, f, retry)
			result, err = f.s.applyQuerySettlement(context.Background(), retry)
			must(t, err)
			if result.disposition != queryRecoveryApplied {
				t.Fatal("durable B retry failed")
			}
		})
	}
}

func settlementAdminInitial(t *testing.T, f *fixture, id string, tokens *int64, reason videobilling.ReleaseReason) {
	t.Helper()
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		_, st, e := LockBillingTaskTx(tx, f.key.OrgID, id)
		if e != nil {
			return e
		}
		now, e := authorityTimeTx(tx)
		if e != nil {
			return e
		}
		var result videobilling.SettlementResult
		if reason != "" {
			result, e = videobilling.ReleaseConfirmedTx(tx, videobilling.ReleaseInput{OrgID: st.OrgID, TaskID: id, EvidenceID: "synthetic-reviewed-release", Reason: reason, Confirmed: true, Now: now})
		} else {
			result, e = videobilling.SettleUsageTx(tx, videobilling.SettlementInput{OrgID: st.OrgID, TaskID: id, Evidence: videobilling.UsageEvidence{EvidenceID: "synthetic-reviewed-initial", Source: "task_get", SourceRecordID: "up_" + id, SourceVersion: "reviewed-initial", CompletionTokens: tokens, ObservedAt: now}, Now: now})
		}
		if e != nil {
			return e
		}
		return ApplyBillingSettlementTx(tx, st.OrgID, id, "reviewed-initial", "", result, now)
	}))
}

func settlementReviewedAdjustment(t *testing.T, f *fixture, id string) {
	t.Helper()
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		taskRow, st, e := LockBillingTaskTx(tx, f.key.OrgID, id)
		if e != nil {
			return e
		}
		now, e := authorityTimeTx(tx)
		if e != nil {
			return e
		}
		var binding UpstreamBinding
		if e = locked(tx).Where("org_id = ? AND task_id = ? AND upstream_task_id = ?", st.OrgID, id, taskRow.UpstreamTaskID).Take(&binding).Error; e != nil {
			return e
		}
		imp, e := videobilling.BeginImportTx(tx, videobilling.BeginImportInput{ImportKey: "s2-correction-" + id, ProviderScopeID: st.ProviderScopeID, Source: "official-bill", SourceVersion: "revision-2", SourceHash: strings.Repeat("b", 64), PayloadRef: "synthetic-bill", ExpectedRows: 1, Now: now})
		if e != nil {
			return e
		}
		n := int64(90)
		line, e := videobilling.ImportLineTx(tx, videobilling.ImportLineInput{ImportID: imp.Import.ID, RowNumber: 1, Line: videobilling.BillLine{ProviderScopeID: st.ProviderScopeID, Source: "official-bill", SourceRecordID: taskRow.UpstreamTaskID, SourceVersion: "revision-2", SourceRevision: 2, EvidenceHash: strings.Repeat("b", 64), PayloadRef: "synthetic-bill", Granularity: videobilling.BillTaskTotal, UpstreamTaskID: taskRow.UpstreamTaskID, Model: taskRow.Model, PeriodStart: now.Add(-time.Hour), PeriodEnd: now, CompletionTokens: &n, AmountCNY: decimal.RequireFromString("0.00414"), ObservedAt: now}, Binding: &videobilling.VerifiedBinding{ProviderScopeID: binding.ProviderScopeID, UpstreamTaskID: binding.UpstreamTaskID, OrgID: binding.OrgID, TaskID: binding.TaskID, BindingVersion: "s2-verified-binding"}, Now: now})
		if e != nil {
			return e
		}
		p, e := videobilling.ProposeAdjustmentTx(tx, videobilling.ProposeAdjustmentInput{OrgID: st.OrgID, TaskID: id, ProposalKey: "s2-reviewed-" + id, LineID: line.Line.ID, ProposerID: 100, Reason: "bill_correction", Now: now, ExpiresAt: now.Add(time.Hour)})
		if e != nil {
			return e
		}
		if _, e = videobilling.ReviewAdjustmentTx(tx, videobilling.ReviewAdjustmentInput{OrgID: st.OrgID, ProposalID: p.ID, ReviewerID: 200, Approved: true, Reason: "verified_bill", Now: now}); e != nil {
			return e
		}
		r, e := videobilling.ApplyAdjustmentTx(tx, videobilling.ApplyAdjustmentInput{OrgID: st.OrgID, ProposalID: p.ID, Now: now})
		if e != nil {
			return e
		}
		return ApplyBillingAdjustmentTx(tx, st.OrgID, id, "reviewed-correction", "synthetic-bill", r, now)
	}))
}

func TestMySQLQuerySettlementInitialWinnersAndTombstone(t *testing.T) {
	for _, kind := range []string{"same_tokens_other_source", "different_tokens", "different_release_reason", "adjusted_then_deleted"} {
		t.Run(kind, func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "initial-"+kind)
			n := int64(100)
			status := ark.StatusSucceeded
			var tokens *int64 = &n
			if kind == "different_release_reason" {
				status = ark.StatusCancelled
				tokens = nil
			}
			row := settlementPoll(t, f, v.ID, status, tokens)
			other := n
			if kind == "different_tokens" {
				other++
			}
			if kind == "different_release_reason" {
				settlementAdminInitial(t, f, v.ID, nil, videobilling.ReleaseNotDispatched)
			} else {
				settlementAdminInitial(t, f, v.ID, &other, "")
			}
			if kind == "adjusted_then_deleted" {
				settlementReviewedAdjustment(t, f, v.ID)
				op, _, err := f.s.Delete(context.Background(), f.key.OrgID, v.ID, "synthetic-delete")
				must(t, err)
				f.response = func(req *http.Request) (*http.Response, error) {
					if req.Method != http.MethodDelete {
						return nil, errors.New("unexpected deletion request")
					}
					return reply(204, ""), nil
				}
				settlementRatesDue(t, f, v.ID)
				must(t, f.s.processMutation(context.Background(), op))
				var st State
				var rowTask task.Task
				must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&st).Error)
				must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&rowTask).Error)
				if st.DeletedAt == nil || st.BillingState != "adjusted" || rowTask.CostUSD.String() != "0.00072" || rowTask.ActualTokens != 90 {
					t.Fatal("public correction/delete precondition failed")
				}
			}
			before := settlementMoneyAndProjections(t, f, v.ID)
			settlementForbidNetwork(t, f)
			result, err := f.s.applyQuerySettlement(context.Background(), row)
			after := settlementReadReceipt(t, f, row)
			conflict := kind == "different_tokens" || kind == "different_release_reason"
			if conflict {
				if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryReview || after.RecoveryDueAt != nil || after.AppliedAt != nil || after.ApplyErrorCode != queryFinancialConflictCode {
					t.Fatal("incompatible initial escaped task review")
				}
			} else {
				must(t, err)
				if result.disposition != queryRecoveryApplied || after.AppliedAt == nil {
					t.Fatal("compatible initial did not finish only progress")
				}
			}
			if settlementMoneyAndProjections(t, f, v.ID) != before {
				t.Fatal("B overwrote initial/adjustment/tombstone or appended money/event")
			}
		})
	}
}

func TestMySQLQuerySettlementConcurrentConsumers(t *testing.T) {
	f := newSettlementFixture(t)
	v := submitSettlementV2(t, f, "parallel-settlement")
	n := int64(100)
	row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
	settlementForbidNetwork(t, f)
	start := make(chan struct{})
	type outcome struct {
		r queryRecoveryResult
		e error
	}
	out := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, e := f.s.applyQuerySettlement(context.Background(), row)
			out <- outcome{r, e}
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	applied, noops := 0, 0
	for result := range out {
		must(t, result.e)
		if result.r.disposition == queryRecoveryApplied {
			applied++
		}
		if result.r.disposition == queryRecoveryNoop {
			noops++
		}
	}
	var entries, events int64
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
	must(t, f.s.DB.Model(&Event{}).Where("org_id = ? AND task_id = ? AND type = ?", f.key.OrgID, v.ID, "billing.settled").Count(&events).Error)
	if applied != 1 || noops != 1 || entries != 1 || events != 1 || f.hold(t, v.ID).CapturedAmount.String() != "0.0008" {
		t.Fatal("two consumers settled or projected more than once")
	}
}

func TestMySQLQuerySettlementAbsentThenReplayRollsBack(t *testing.T) {
	f := newSettlementFixture(t)
	v := submitSettlementV2(t, f, "absent-then-replay")
	n := int64(100)
	row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
	settlementForbidNetwork(t, f)
	before := authorityFinancialFacts(t, f, v.ID)
	var injected bool
	var nestedResult videobilling.SettlementResult
	var nestedErr error
	name := "test:settlement-absent-replay"
	// An explicit same-transaction fault boundary: after the opaque reader's
	// real absent SELECT, perform the real original settlement once. The B
	// writer must then observe Replay and roll back BOTH writes, never treat
	// the synthetic timing anomaly as a winner or patch projections.
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if injected || q.Statement.Table != "video_billing_entries" || q.Error != gorm.ErrRecordNotFound {
			return
		}
		injected = true
		nested := q.Session(&gorm.Session{NewDB: true, Context: q.Statement.Context})
		nested.Error = nil
		nestedResult, nestedErr = videobilling.SettleUsageTx(nested, videobilling.SettlementInput{OrgID: row.OrgID, TaskID: row.TaskID, Evidence: videobilling.UsageEvidence{EvidenceID: "synthetic-same-tx-winner", Source: "task_get", SourceRecordID: "up_" + v.ID, SourceVersion: "fault-window", CompletionTokens: &n, ObservedAt: row.ReceivedAt}, Now: row.ReceivedAt})
	}))
	result, err := f.s.applyQuerySettlement(context.Background(), row)
	must(t, f.s.DB.Callback().Query().Remove(name))
	if !injected || nestedErr != nil || nestedResult.Replay || nestedResult.Entry == nil || !errors.Is(err, videobilling.ErrInvariant) || result.disposition != queryRecoveryDeferred {
		t.Fatal("actual writer Replay anomaly was not rejected")
	}
	if authorityFinancialFacts(t, f, v.ID) != before {
		t.Fatal("absent-then-Replay committed nested or outer money")
	}
	var count int64
	must(t, f.s.DB.Model(&invocation.Invocation{}).Where("org_id = ?", row.OrgID).Count(&count).Error)
	got := settlementReadReceipt(t, f, row)
	if count != 0 || got.AppliedAt != nil || got.RecoveryDueAt == nil || got.ApplyErrorCode != "query_apply_failed" {
		t.Fatal("Replay anomaly left a success projection")
	}
}

func TestMySQLQuerySettlementDamagedClosedMarkerStillBlocks(t *testing.T) {
	f := newSettlementFixture(t)
	v := submitSettlementV2(t, f, "damaged-task-barrier")
	n := int64(100)
	row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
	other := int64(101)
	settlementAdminInitial(t, f, v.ID, &other, "")
	result, err := f.s.applyQuerySettlement(context.Background(), row)
	if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryReview {
		t.Fatal("real incompatible initial failed to establish barrier")
	}
	closed := settlementReadReceipt(t, f, row)
	must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ?", closed.OrgID, closed.TaskID, closed.AttemptID, closed.RecoveryVersion).Updates(map[string]any{"runtime_version": "damaged-runtime", "observation_schema_version": 99, "settlement_evidence_sha256": strings.Repeat("f", 64), "observation_json": `{"task_id":"damaged-target"}`}).Error)
	closed = settlementReadReceipt(t, f, row)
	before := settlementMoneyAndProjections(t, f, v.ID)
	settlementForbidNetwork(t, f)
	result, err = f.s.applyQuerySettlement(context.Background(), row)
	if !errors.Is(err, errQueryFinancialConflict) || result.disposition == queryRecoveryReview || !reflect.DeepEqual(closed, settlementReadReceipt(t, f, row)) || settlementMoneyAndProjections(t, f, v.ID) != before {
		t.Fatal("damaged closed marker vanished, changed or counted twice")
	}
	var selected []QueryReceipt
	must(t, f.s.recoverySettlementCandidates(context.Background()).Find(&selected).Error)
	if len(selected) != 0 {
		t.Fatal("task barrier reentered B candidates")
	}
}

// Hold the winning consumer after its real Task FOR UPDATE, then prove the
// other connection waits in MySQL. The scheduling callback never invents a row,
// result or financial state; both consumers run their original transactions.
func TestMySQLQuerySettlementGenerationRace(t *testing.T) {
	for _, first := range []string{"generation", "settlement"} {
		t.Run(first+"_first", func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "generation-settlement-race-"+first)
			n := int64(100)
			pin := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
			other := int64(101)
			pending := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, &other)
			before := authorityFinancialFacts(t, f, v.ID)
			settlementForbidNetwork(t, f)
			type roleKey struct{}
			type outcome struct {
				role   string
				result queryRecoveryResult
				err    error
			}
			winnerLocked, loserStarting, loserLocked := make(chan uint64, 1), make(chan uint64, 1), make(chan uint64, 1)
			release, finishLoser := make(chan struct{}), make(chan struct{})
			var releaseOnce, finishOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			allowLoser := func() { finishOnce.Do(func() { close(finishLoser) }) }
			var winnerSeen, loserSeen, loserLockedSeen atomic.Bool
			name := "test:g-b-lock-race-" + first
			isTaskLock := func(q *gorm.DB) bool {
				lock, ok := q.Statement.Clauses["FOR"].Expression.(clause.Locking)
				return q.Statement.Table == "tasks" && ok && lock.Strength == "UPDATE"
			}
			connectionID := func(q *gorm.DB) uint64 {
				var id uint64
				err := q.Session(&gorm.Session{NewDB: true, Context: q.Statement.Context}).Raw("SELECT CONNECTION_ID()").Scan(&id).Error
				if err != nil || id == 0 {
					q.AddError(errors.New("consumer connection identity unavailable"))
				}
				return id
			}
			must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name+"-before", func(q *gorm.DB) {
				role, ok := q.Statement.Context.Value(roleKey{}).(string)
				if !ok || role == first || !isTaskLock(q) || !loserSeen.CompareAndSwap(false, true) {
					return
				}
				loserStarting <- connectionID(q)
			}))
			must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name+"-after", func(q *gorm.DB) {
				role, ok := q.Statement.Context.Value(roleKey{}).(string)
				if !ok || !isTaskLock(q) || q.Error != nil {
					return
				}
				if role != first {
					if !loserLockedSeen.CompareAndSwap(false, true) {
						return
					}
					loserLocked <- connectionID(q)
					select {
					case <-finishLoser:
					case <-q.Statement.Context.Done():
						q.AddError(q.Statement.Context.Err())
					}
					return
				}
				if !winnerSeen.CompareAndSwap(false, true) {
					return
				}
				row, ok := q.Statement.Dest.(*task.Task)
				if !ok || row.OrgID != f.key.OrgID || row.TaskID != v.ID || q.RowsAffected != 1 {
					q.AddError(errors.New("winning consumer did not lock the exact task"))
					return
				}
				winnerLocked <- connectionID(q)
				select {
				case <-release:
				case <-q.Statement.Context.Done():
					q.AddError(q.Statement.Context.Err())
				}
			}))
			ctx, cancel := context.WithCancel(context.Background())
			results := make(chan outcome, 2)
			var workers sync.WaitGroup
			start := func(role string) {
				workers.Add(1)
				go func() {
					defer workers.Done()
					callCtx, stop := context.WithTimeout(context.WithValue(ctx, roleKey{}, role), querySettlementTimeout)
					defer stop()
					var r queryRecoveryResult
					var err error
					if role == "generation" {
						r, err = f.s.applyQueryReceiptProgress(callCtx, pending)
					} else {
						r, err = f.s.applyQuerySettlement(callCtx, pin)
					}
					results <- outcome{role, r, err}
				}()
			}
			defer func() {
				cancel()
				unblock()
				allowLoser()
				drained := make(chan struct{})
				go func() { workers.Wait(); close(drained) }()
				select {
				case <-drained:
					must(t, f.s.DB.Callback().Query().Remove(name+"-before"))
					must(t, f.s.DB.Callback().Query().Remove(name+"-after"))
				case <-time.After(3 * time.Second):
					t.Error("G/B consumer cleanup did not join")
				}
			}()
			start(first)
			var blockerID, waiterID uint64
			select {
			case blockerID = <-winnerLocked:
			case <-time.After(time.Second):
				t.Fatal("first consumer did not acquire real Task lock")
			}
			second := "settlement"
			if first == second {
				second = "generation"
			}
			start(second)
			select {
			case waiterID = <-loserStarting:
			case <-time.After(time.Second):
				t.Fatal("second consumer did not attempt real Task lock")
			}
			if blockerID == 0 || waiterID == 0 || blockerID == waiterID {
				t.Fatal("consumers did not own separate real connections")
			}
			waitCtx, waitCancel := context.WithTimeout(ctx, time.Second)
			defer waitCancel()
			for {
				var waits int64
				must(t, f.s.DB.WithContext(waitCtx).Raw(`SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.threads b ON b.THREAD_ID = w.BLOCKING_THREAD_ID JOIN performance_schema.threads r ON r.THREAD_ID = w.REQUESTING_THREAD_ID WHERE b.PROCESSLIST_ID = ? AND r.PROCESSLIST_ID = ?`, blockerID, waiterID).Scan(&waits).Error)
				if waits > 0 {
					break
				}
				select {
				case <-waitCtx.Done():
					t.Fatal("second consumer was not observed waiting in MySQL")
				case <-time.After(5 * time.Millisecond):
				}
			}
			// Neither the winner (paused just after Task lock) nor the blocked
			// loser may have committed any money at this exact observed boundary.
			if authorityFinancialFacts(t, f, v.ID) != before {
				t.Fatal("money committed before either consumer passed the Task boundary")
			}
			unblock()
			var got [2]outcome
			select {
			case got[0] = <-results:
			case <-time.After(3 * time.Second):
				t.Fatal("winning consumer did not finish")
			}
			if got[0].role != first {
				t.Fatal("loser finished before the controlled winning commit")
			}
			select {
			case id := <-loserLocked:
				if id != waiterID {
					t.Fatal("loser switched connection")
				}
			case <-time.After(time.Second):
				t.Fatal("loser did not acquire the released task lock")
			}
			// The winner has committed and the other transaction now owns only
			// Task. Preserve this real intermediate financial state as well.
			afterWinner := authorityFinancialFacts(t, f, v.ID)
			winnerPin := settlementReadReceipt(t, f, pin)
			if first == "generation" {
				if afterWinner != before || winnerPin.RecoveryDueAt != nil || winnerPin.ApplyErrorCode != queryFinancialConflictCode {
					t.Fatal("G winner changed money or failed to close pin")
				}
			} else if winnerPin.AppliedAt == nil || winnerPin.ApplyErrorCode != "" || f.hold(t, v.ID).CapturedAmount.String() != "0.0008" {
				t.Fatal("B winner did not commit its initial before G resumed")
			}
			allowLoser()
			select {
			case got[1] = <-results:
			case <-time.After(3 * time.Second):
				t.Fatal("bounded losing consumer did not finish")
			}
			if authorityFinancialFacts(t, f, v.ID) != afterWinner {
				t.Fatal("losing consumer changed the committed winning money")
			}
			for _, result := range got {
				if result.role == "generation" {
					if !errors.Is(result.err, errQueryFinancialConflict) || result.result.disposition != queryRecoveryReview {
						t.Fatal("real conflicting G failed to commit its closed marker")
					}
				} else if first == "settlement" {
					must(t, result.err)
					if result.result.disposition != queryRecoveryApplied {
						t.Fatal("winning B failed to settle exactly once")
					}
				} else if !errors.Is(result.err, errQueryFinancialConflict) || result.result.disposition == queryRecoveryApplied {
					t.Fatal("losing B bypassed the task conflict marker")
				}
			}
			closedPin, closedCurrent := settlementReadReceipt(t, f, pin), settlementReadReceipt(t, f, pending)
			if closedPin.ApplyErrorCode != queryFinancialConflictCode || closedCurrent.ApplyErrorCode != queryFinancialConflictCode || closedCurrent.AppliedAt != nil || closedCurrent.RecoveryDueAt != nil || closedCurrent.GenerationAppliedAt != nil || closedCurrent.RecoveryVersion != pending.RecoveryVersion+1 || closedPin.SettlementEvidenceSHA256 != pin.SettlementEvidenceSHA256 || !reflect.DeepEqual(closedPin.GenerationAppliedAt, pin.GenerationAppliedAt) {
				t.Fatal("race lost first evidence or closed-phase facts")
			}
			var entries, events, invocations int64
			must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
			must(t, f.s.DB.Model(&Event{}).Where("org_id = ? AND task_id = ? AND type = ?", f.key.OrgID, v.ID, "billing.settled").Count(&events).Error)
			must(t, f.s.DB.Model(&invocation.Invocation{}).Where("org_id = ?", f.key.OrgID).Count(&invocations).Error)
			if first == "generation" {
				if entries != 0 || events != 0 || invocations != 0 || closedPin.AppliedAt != nil || closedPin.RecoveryDueAt != nil || closedPin.RecoveryVersion != pin.RecoveryVersion+1 || authorityFinancialFacts(t, f, v.ID) != before {
					t.Fatal("G-first race released or settled money")
				}
			} else {
				if entries != 1 || events != 1 || invocations != 1 || closedPin.AppliedAt == nil || !reflect.DeepEqual(closedPin.RecoveryDueAt, pin.RecoveryDueAt) || closedPin.RecoveryVersion != pin.RecoveryVersion+2 || f.hold(t, v.ID).CapturedAmount.String() != "0.0008" {
					t.Fatal("B-first race lost settled money or regressed completed pin")
				}
			}
			stable := settlementMoneyAndProjections(t, f, v.ID)
			_, err := f.s.applyQuerySettlement(context.Background(), pin)
			if !errors.Is(err, errQueryFinancialConflict) || stable != settlementMoneyAndProjections(t, f, v.ID) || !reflect.DeepEqual(closedPin, settlementReadReceipt(t, f, pin)) {
				t.Fatal("post-race replay changed closed progress or money")
			}
		})
	}
}

func TestMySQLQuerySettlementAfterHistoryPurge(t *testing.T) {
	f := newSettlementFixture(t)
	v := submitSettlementV2(t, f, "settlement-after-query-history-purge")
	n := int64(100)
	row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
	var meta invocation.VideoPayload
	must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", row.OrgID, row.PayloadRef).Take(&meta).Error)
	if meta.Purpose != invocation.VideoPurposeHistory || meta.WrappedKey == "" || len(meta.Nonce) == 0 {
		t.Fatal("query history lacks real encrypted retention material")
	}
	otherPayloads := func() [32]byte {
		return queryRecoveryHash(t, []any{
			queryRecoveryProjection(t, f.s.DB, "invocation_payloads", "payload_id <> ?", row.PayloadRef),
			queryRecoveryProjection(t, f.s.DB, "invocation_payload_staging", "payload_id <> ?", row.PayloadRef),
		})
	}
	others, money := otherPayloads(), authorityFinancialFacts(t, f, v.ID)
	reads := responseInboxCount(t, f, &invocation.VideoPayloadAccess{}, "org_id = ?", row.OrgID)
	// History retains its existing host-clock policy. Only this exact query
	// history is selected; execution TTL and every other payload stay unchanged.
	purger, err := invocation.NewVideoStore(f.s.DB, f.s.Keyring, invocation.VideoStoreOptions{Now: func() time.Time { return meta.ExpiresAt.Add(time.Second) }})
	must(t, err)
	name := "test:settlement-purge-one-history"
	must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table == "invocation_payloads" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "org_id", Value: row.OrgID}, clause.Eq{Column: "payload_id", Value: row.PayloadRef}}})
		}
	}))
	t.Cleanup(func() { _ = f.s.DB.Callback().Query().Remove(name) })
	purged, err := purger.PurgeExpired(context.Background(), 100)
	must(t, f.s.DB.Callback().Query().Remove(name))
	must(t, err)
	var erased invocation.VideoPayload
	must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", row.OrgID, row.PayloadRef).Take(&erased).Error)
	if purged != 1 || erased.CaptureStatus != invocation.VideoCaptureExpired || erased.WrappedKey != "" || len(erased.Nonce) != 0 || responseInboxCount(t, f, &invocation.VideoPayloadStaging{}, "payload_id = ?", row.PayloadRef) != 0 || others != otherPayloads() || money != authorityFinancialFacts(t, f, v.ID) || !reflect.DeepEqual(row, settlementReadReceipt(t, f, row)) {
		t.Fatal("real history purge changed durable evidence, money or another payload")
	}
	restarted, err := NewService(f.s.DB, f.s.Keyring, f.s.Secret, f.s.Log)
	must(t, err)
	restarted.Client = f.s.Client
	restarted.Gate = f.s.Gate
	if restarted.ResponseInboxEnabled || restarted.MutationResponseInboxEnabled {
		t.Fatal("fresh consumer enabled a producer flag")
	}
	f.s = restarted
	settlementForbidNetwork(t, f)
	posts, gets := f.posts.Load(), f.gets.Load()
	must(t, f.s.ProcessSettlementOnce(context.Background()))
	completed := settlementReadReceipt(t, f, row)
	if completed.AppliedAt == nil || completed.RecoveryVersion != row.RecoveryVersion+1 || completed.SettlementEvidenceSHA256 != row.SettlementEvidenceSHA256 || f.hold(t, v.ID).CapturedAmount.String() != "0.0008" {
		t.Fatal("fresh service could not settle durable typed evidence after query history purge")
	}
	stable := settlementMoneyAndProjections(t, f, v.ID)
	must(t, f.s.ProcessSettlementOnce(context.Background()))
	if stable != settlementMoneyAndProjections(t, f, v.ID) || !reflect.DeepEqual(completed, settlementReadReceipt(t, f, row)) || posts != f.posts.Load() || gets != f.gets.Load() || reads != responseInboxCount(t, f, &invocation.VideoPayloadAccess{}, "org_id = ?", row.OrgID) {
		t.Fatal("post-purge replay decrypted, called upstream or repeated money")
	}
}

// Business-stack concurrency plus cmd/core's existing real six-job registration
// test is combination evidence, not a single full-host SIGTERM/binary scenario.
func TestMySQLQuerySettlementProgressWhileDispatchHTTPBlocked(t *testing.T) {
	f := newSettlementFixture(t)
	v := submitSettlementV2(t, f, "settlement-while-dispatch-blocked")
	n := int64(100)
	row := settlementPoll(t, f, v.ID, ark.StatusSucceeded, &n)
	blocked := createSettlementV2(t, f, "blocked-real-dispatch")
	settlementRatesDue(t, f, blocked.ID)
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, blocked.ID, "create_task").Take(&op).Error)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var calls atomic.Int32
	f.response = func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) != 1 || req.Method != http.MethodPost || req.URL.Path != "/api/v3/contents/generations/tasks" || req.Header.Get("Authorization") != "Bearer official-test-credential" {
			return nil, errors.New("unexpected blocked-dispatch HTTP")
		}
		close(entered)
		select {
		case <-release:
			return reply(200, `{"id":"up_`+blocked.ID+`"}`), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	type settlementOnlyKey struct{}
	name := "test:blocked-dispatch-b-no-input"
	must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Context.Value(settlementOnlyKey{}) == true && (q.Statement.Table == "video_credential_snapshots" || q.Statement.Table == "invocation_payloads" || q.Statement.Table == "invocation_payload_staging") {
			q.AddError(errors.New("B attempted encrypted storage while dispatch was blocked"))
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var dispatchErr error
	posts, gets := f.posts.Load(), f.gets.Load()
	go func() { defer close(done); dispatchErr = f.s.processOperation(ctx, op) }()
	defer func() {
		cancel()
		unblock()
		select {
		case <-done:
			must(t, f.s.DB.Callback().Query().Remove(name))
		case <-time.After(3 * time.Second):
			t.Error("blocked dispatch cleanup failed to join")
		}
	}()
	select {
	case <-entered:
	case <-done:
		t.Fatal("dispatch returned before reaching real HTTP")
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not enter real HTTP")
	}
	must(t, f.s.ProcessSettlementOnce(context.WithValue(context.Background(), settlementOnlyKey{}, true)))
	select {
	case <-done:
		t.Fatal("B progress was observed only after dispatch completed")
	default:
	}
	completed := settlementReadReceipt(t, f, row)
	if completed.AppliedAt == nil || completed.RecoveryVersion != row.RecoveryVersion+1 || f.hold(t, v.ID).CapturedAmount.String() != "0.0008" || f.hold(t, blocked.ID).Status != ledger.HoldActive || f.posts.Load() != posts+1 || f.gets.Load() != gets || calls.Load() != 1 {
		t.Fatal("independent B did not settle once during blocked dispatch")
	}
	unblock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("released dispatch did not join")
	}
	must(t, dispatchErr)
	var dispatched task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, blocked.ID).Take(&dispatched).Error)
	if dispatched.UpstreamTaskID != "up_"+blocked.ID || f.hold(t, blocked.ID).Status != ledger.HoldActive {
		t.Fatal("independent settlement corrupted dispatch or its reservation")
	}
	stable := settlementMoneyAndProjections(t, f, v.ID)
	must(t, f.s.ProcessSettlementOnce(context.WithValue(context.Background(), settlementOnlyKey{}, true)))
	if stable != settlementMoneyAndProjections(t, f, v.ID) || !reflect.DeepEqual(completed, settlementReadReceipt(t, f, row)) || f.posts.Load() != posts+1 || f.gets.Load() != gets || calls.Load() != 1 {
		t.Fatal("post-dispatch B replay changed money or HTTP")
	}
}
