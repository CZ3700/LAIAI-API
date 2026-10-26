package video

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestMySQLQueryRecoveryBadQueueProgress(t *testing.T) {
	h := queryRecoveryNew(t)
	items := make([]queryRecoveryPending, 0, 42)
	for index := 0; index < 42; index++ {
		item := h.pending(t, index%2, index)
		// Explicit fixture scheduling, not a claim of natural retry time. Oldest
		// forty are due one hour ago; both healthy successors are due now.
		seconds := 1
		if index < 40 {
			seconds = 3600
		}
		h.arrange(t, &item, index < 40, seconds)
		items = append(items, item)
	}
	s := h.service(t)
	h.forbidNetwork(t, s)
	var candidates []QueryReceipt
	queryRecoveryCheck(t, s.recoveryQueryCandidates(context.Background()).Order("r.recovery_due_at, r.received_at, r.attempt_id").Limit(40).Find(&candidates).Error)
	bad := map[string]bool{}
	for _, item := range items[:40] {
		bad[item.row.AttemptID] = true
	}
	if len(candidates) != 40 {
		t.Fatal("the actual LIMIT40 selector did not select the full bad window")
	}
	for _, candidate := range candidates {
		if !bad[candidate.AttemptID] {
			t.Fatal("fixture did not put forty bad records before healthy successors")
		}
	}
	before, evidence := h.finances(t), h.evidenceStorage(t)
	var healthyEvents [2]int64
	for index, item := range items[40:] {
		queryRecoveryCheck(t, h.db.Model(&Event{}).Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).Count(&healthyEvents[index]).Error)
	}
	complete := false
	for cycle := 0; cycle < 10; cycle++ {
		err := s.ProcessPersistedEvidenceOnce(context.Background())
		if !queryRecoveryExpectedQueueError(err) {
			t.Fatal("recovery returned an unexpected storage/identity failure")
		}
		if cycle == 0 && (h.finances(t) != before || s.metrics().runtimeSnapshot().RecoveryLanes[3].Selected != 40) {
			t.Fatal("first bounded bad window changed funds or bypassed LIMIT40")
		}
		review, settled := 0, 0
		for index, item := range items {
			var row QueryReceipt
			queryRecoveryCheck(t, h.db.Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Take(&row).Error)
			if index < 40 {
				if row.RecoveryDueAt == nil && row.ApplyErrorCode == "query_observation_invalid" && row.AppliedAt == nil {
					review++
				}
			} else if row.AppliedAt != nil {
				settled++
			}
		}
		if review == 40 && settled == 2 {
			complete = true
			break
		}
	}
	if !complete {
		t.Fatal("ten bounded production recovery cycles did not finish both review and healthy work")
	}
	for index, item := range items {
		var row QueryReceipt
		queryRecoveryCheck(t, h.db.Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Take(&row).Error)
		if row.RecoveryVersion != item.row.RecoveryVersion+1 {
			t.Fatal("one committed application/review did not advance exactly one version")
		}
		if index < 40 && h.taskStorage(t, row.OrgID, row.TaskID) != item.storage {
			t.Fatal("bad-record isolation changed task, state, original evidence or financial storage")
		}
		queryRecoveryAssertTaskMoney(t, h, item, index >= 40)
		if index >= 40 {
			var count int64
			queryRecoveryCheck(t, h.db.Model(&Event{}).Where("org_id = ? AND task_id = ?", row.OrgID, row.TaskID).Count(&count).Error)
			if count != healthyEvents[index-40]+1 {
				t.Fatal("healthy recovery did not append exactly one status event")
			}
		}
	}
	queryRecoveryAssertAggregateMoney(t, h, 20, 1)
	if h.evidenceStorage(t) != evidence || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
		t.Fatal("persisted recovery used HTTP, credentials, decryption or changed encrypted evidence")
	}
	for _, f := range h.users {
		if f.posts.Load() != 21 || f.gets.Load() != 21 {
			t.Fatal("recovery repeated a request instead of consuming durable evidence")
		}
	}
	var all []QueryReceipt
	queryRecoveryCheck(t, h.db.Order("attempt_id").Find(&all).Error)
	settledFunds := h.finances(t)
	observation := s.metrics().runtimeSnapshot()
	queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(context.Background()))
	var replay []QueryReceipt
	queryRecoveryCheck(t, h.db.Order("attempt_id").Find(&replay).Error)
	var remaining []QueryReceipt
	// No extra test Org filter and no LIMIT: this is the actual selector over
	// the entire private schema, including both organizations.
	queryRecoveryCheck(t, s.recoveryQueryCandidates(context.Background()).Find(&remaining).Error)
	if len(remaining) != 0 || !reflect.DeepEqual(all, replay) || h.finances(t) != settledFunds || h.evidenceStorage(t) != evidence || s.metrics().runtimeSnapshot().RecoveryLanes[3].Review != observation.RecoveryLanes[3].Review {
		t.Fatal("replay reselected isolated evidence or changed committed financial facts")
	}
	drift, err := ledger.CheckDrift(h.db)
	queryRecoveryCheck(t, err)
	if len(drift) != 0 {
		t.Fatal("query recovery introduced ledger drift")
	}
}

// Independent acceptance oracle: a single expected leaf must not hide an SQL,
// authority or unknown wrapper joined to it by another recovery lane.
func queryRecoveryExpectedQueueError(err error) bool {
	if err == nil {
		return true
	}
	remaining := 128
	var accept func(error) bool
	accept = func(err error) bool {
		remaining--
		if err == nil || remaining < 0 {
			return false
		}
		if aggregate, ok := err.(*persistedRecoveryErrors); ok {
			if aggregate == nil || len(aggregate.causes) == 0 {
				return false
			}
			for _, cause := range aggregate.causes {
				if !accept(cause) {
					return false
				}
			}
			return true
		}
		if decoded, ok := err.(*queryObservationDecodeError); ok {
			return decoded != nil && (decoded.kind == queryJSONSyntax || decoded.kind == queryJSONType)
		}
		return err == context.DeadlineExceeded
	}
	return accept(err)
}

func TestQueryRecoveryQueueErrorOracle(t *testing.T) {
	_, invalid := decodeQueryObservation(`{"total_tokens":"bad"}`)
	if invalid == nil || !queryRecoveryExpectedQueueError(safeRecoveryErrors(invalid, context.DeadlineExceeded)) {
		t.Fatal("queue oracle rejected its explicit syntax/type or lane deadline boundary")
	}
	for _, err := range []error{safeRecoveryErrors(invalid, errors.New("storage failure")), safeRecoveryErrors(invalid, authorityClockFailure(context.DeadlineExceeded)), errors.Join(invalid, context.DeadlineExceeded), errQueryObservationInvalid} {
		if queryRecoveryExpectedQueueError(err) {
			t.Fatal("expected JSON leaf masked an unrelated failure")
		}
	}
}

func queryRecoveryAssertTaskMoney(t *testing.T, h *queryRecoveryFixture, item queryRecoveryPending, settled bool) {
	t.Helper()
	var snap videobilling.Snapshot
	var row task.Task
	var state State
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).Take(&snap).Error)
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).Take(&row).Error)
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).Take(&state).Error)
	hold := item.f.hold(t, item.row.TaskID)
	if !snap.HoldUSD.Equal(decimal.RequireFromString("0.864")) || !hold.Amount.Equal(decimal.RequireFromString("0.864")) || snap.EstimatedTokens != 108000 {
		t.Fatal("original fixed-price acceptance differs from independent amount oracle")
	}
	var entries []videobilling.BillingEntry
	var reservations []videobilling.QuotaReservation
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ?", row.OrgID, row.TaskID).Find(&entries).Error)
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ?", row.OrgID, row.TaskID).Find(&reservations).Error)
	if len(reservations) != 1 || !reservations[0].Supplier || !reservations[0].ReservedAmount.Equal(decimal.RequireFromString("4.968")) {
		t.Fatal("supplier reservation does not match the independent CNY oracle")
	}
	if !settled {
		if len(entries) != 0 || hold.Status != ledger.HoldActive || !hold.CapturedAmount.IsZero() || state.BillingState != "reserved" || reservations[0].State != "active" || !reservations[0].ActualAmount.IsZero() {
			t.Fatal("malformed evidence released or captured a pending task")
		}
		return
	}
	if len(entries) != 1 || hold.Status != ledger.HoldCaptured || !hold.CapturedAmount.Equal(decimal.RequireFromString("0.0008")) || row.Status != "succeeded" || state.BillingState != "settled" || reservations[0].State != "consumed" || !reservations[0].ActualAmount.Equal(decimal.RequireFromString("0.0046")) {
		t.Fatal("healthy receipt did not settle exactly once using the original literal amounts")
	}
	entry := entries[0]
	if !entry.CapturedUSD.Equal(decimal.RequireFromString("0.0008")) || !entry.ReleasedUSD.Equal(decimal.RequireFromString("0.8632")) || !entry.SupplierCNY.Equal(decimal.RequireFromString("0.0046")) || !entry.SupplierUSD.Equal(decimal.RequireFromString("0.000644")) || entry.SnapshotID != snap.ID || entry.HoldID != hold.ID {
		t.Fatal("original price/FX, hold or supplier settlement changed")
	}
}

func queryRecoveryAssertAggregateMoney(t *testing.T, h *queryRecoveryFixture, heldPerOrg, settledPerOrg int64) {
	t.Helper()
	held := decimal.RequireFromString("0.864").Mul(decimal.NewFromInt(heldPerOrg))
	spent := decimal.RequireFromString("0.0008").Mul(decimal.NewFromInt(settledPerOrg))
	for _, f := range h.users {
		var key identity.APIKey
		var account ledger.Account
		queryRecoveryCheck(t, h.db.Where("org_id = ? AND id = ?", f.key.OrgID, f.key.ID).Take(&key).Error)
		queryRecoveryCheck(t, h.db.Where("org_id = ? AND type = ? AND shard = 0", f.key.OrgID, ledger.AccountUserCredit).Take(&account).Error)
		if !key.UsedAmount.Equal(held.Add(spent)) || !account.HeldAmount.Equal(held) || !account.PostedBalance.Equal(decimal.NewFromInt(100).Sub(spent)) {
			t.Fatal("tenant account or Key budget lost reserved versus actual amounts")
		}
	}
	var buckets []videobilling.QuotaBucket
	queryRecoveryCheck(t, h.db.Find(&buckets).Error)
	if len(buckets) != 1 || buckets[0].OrgID != 0 || !buckets[0].ReservedAmount.Equal(decimal.RequireFromString("4.968").Mul(decimal.NewFromInt(2*heldPerOrg))) || !buckets[0].ConsumedAmount.Equal(decimal.RequireFromString("0.0046").Mul(decimal.NewFromInt(2*settledPerOrg))) {
		t.Fatal("shared provider budget omitted Org0 or changed supplier currency units")
	}
}

func TestMySQLQueryRecoveryCommittedStatistics(t *testing.T) {
	h := queryRecoveryNew(t)
	item := h.pending(t, 0, 0)
	h.arrange(t, &item, true, 1)
	loser, winner := h.service(t), h.service(t)
	h.forbidNetwork(t, loser)
	before, evidence := h.finances(t), h.evidenceStorage(t)
	var entered atomic.Bool
	var aID, bID uint64
	var winnerResult queryRecoveryResult
	var winnerErr error
	var committed QueryReceipt
	type winnerContextKey struct{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hook := "test:query-review-winner-before-task-lock"
	queryRecoveryCheck(t, h.db.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table != "tasks" {
			return
		}
		if tx.Statement.Context.Value(winnerContextKey{}) == true {
			queryRecoveryCheck(t, tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT CONNECTION_ID()").Scan(&bID).Error)
			return
		}
		if !entered.CompareAndSwap(false, true) {
			return
		}
		queryRecoveryCheck(t, tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT CONNECTION_ID()").Scan(&aID).Error)
		// A owns a real transaction connection, but has not locked Task. B
		// commits its actual decoder+diagnostic on a distinct connection.
		winnerResult, winnerErr = winner.applyQueryReceiptProgress(context.WithValue(ctx, winnerContextKey{}, true), item.row)
		queryRecoveryCheck(t, h.db.WithContext(ctx).Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Take(&committed).Error)
	}))
	result, err := loser.recoverQueryReceiptsProgress(ctx)
	queryRecoveryCheck(t, h.db.Callback().Query().Remove(hook))
	if !entered.Load() || aID == 0 || bID == 0 || aID == bID || !errors.Is(winnerErr, errQueryObservationInvalid) || winnerResult.disposition != queryRecoveryReview || !errors.Is(err, errQueryRecoveryReview) || result != (recoveryLaneResult{Selected: 1, Failed: 1}) {
		t.Fatal("a candidate that lost to committed review was counted as a new review/noop")
	}
	var after QueryReceipt
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Take(&after).Error)
	if committed.RecoveryDueAt != nil || committed.RecoveryVersion != item.row.RecoveryVersion+1 || !reflect.DeepEqual(committed, after) || h.finances(t) != before || h.evidenceStorage(t) != evidence || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != item.storage || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
		t.Fatal("late review rejection changed evidence, version or financial state")
	}
}

func TestMySQLQueryRecoveryOldDiagnosticCannotReplaceReview(t *testing.T) {
	h := queryRecoveryNew(t)
	item := h.pending(t, 0, 0)
	h.arrange(t, &item, true, 1)
	s := h.service(t)
	h.forbidNetwork(t, s)
	before := h.finances(t)
	var token queryRecoveryAttempt
	var cause error
	queryRecoveryCheck(t, h.db.Transaction(func(tx *gorm.DB) error {
		if _, _, err := LockBillingTaskTx(tx, item.row.OrgID, item.row.TaskID); err != nil {
			return err
		}
		var current QueryReceipt
		if err := locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", item.row.OrgID, item.row.TaskID, item.row.AttemptID).Take(&current).Error; err != nil {
			return err
		}
		var err error
		token, err = observeQueryRecovery(current)
		_, cause = decodeQueryObservation(current.ObservationJSON)
		return err
	}))
	first, err := s.applyQueryReceiptProgress(context.Background(), item.row)
	if first.disposition != queryRecoveryReview || !errors.Is(err, errQueryObservationInvalid) {
		t.Fatal("actual current consumer did not commit the review winner")
	}
	var committed QueryReceipt
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Take(&committed).Error)
	for _, oldCause := range []error{cause, errors.New("temporary-storage-failure")} {
		result, err := s.recordQueryRecoveryFailure(context.Background(), token, oldCause)
		if err != nil || result.disposition != queryRecoveryNoop {
			t.Fatal("older diagnostic did not yield to the committed version")
		}
	}
	var after QueryReceipt
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Take(&after).Error)
	if !reflect.DeepEqual(committed, after) || after.RecoveryDueAt != nil || h.finances(t) != before || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != item.storage || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
		t.Fatal("stale diagnostic cleared review or changed immutable/financial facts")
	}
}

func TestMySQLQueryRecoveryUnsupportedCompatibilityDiagnostic(t *testing.T) {
	h := queryRecoveryNew(t)
	names := []string{"known_receipt", "unknown_receipt", "existing_null", "cancelled", "mixed_storage"}
	items := make([]queryRecoveryPending, len(names))
	for i := range names {
		items[i] = h.pending(t, 0, i)
		h.arrange(t, &items[i], false, 1)
	}
	s := h.service(t)
	h.forbidNetwork(t, s)
	for index, name := range names {
		t.Run(name, func(t *testing.T) {
			item := items[index]
			queryRecoveryCheck(t, h.db.Model(&State{}).Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).Update("runtime_version", "future-runtime-test").Error)
			if name == "known_receipt" {
				// The caller's copied row is deliberately older than current DB
				// progress. The compatibility token must use the locked row.
				queryRecoveryCheck(t, h.db.Model(&QueryReceipt{}).Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Update("recovery_version", item.row.RecoveryVersion+1).Error)
			} else if name == "unknown_receipt" {
				queryRecoveryCheck(t, h.db.Model(&QueryReceipt{}).Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Update("observation_schema_version", 2).Error)
			} else if name == "existing_null" {
				queryRecoveryCheck(t, h.db.Model(&QueryReceipt{}).Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Updates(map[string]any{"recovery_due_at": nil, "apply_error_code": "query_observation_invalid"}).Error)
			}
			var before QueryReceipt
			queryRecoveryCheck(t, h.db.Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Take(&before).Error)
			money, storage, evidence := h.finances(t), h.taskStorage(t, item.row.OrgID, item.row.TaskID), h.evidenceStorage(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "cancelled" {
				cancel()
			}
			var result queryRecoveryResult
			var err error
			if name == "mixed_storage" {
				var token queryCompatibilityAttempt
				queryRecoveryCheck(t, h.db.Transaction(func(tx *gorm.DB) error {
					row, state, err := LockBillingTaskTx(tx, before.OrgID, before.TaskID)
					if err != videoruntime.ErrUnsupported {
						return errors.New("missing unsupported contract positive control")
					}
					token, err = observeQueryCompatibilityTx(tx, row, state, before)
					return err
				}))
				var number int
				storageErr := h.db.Raw("SELECT query_recovery_nonexistent_column FROM video_query_receipts LIMIT 1").Scan(&number).Error
				if !token.identity.valid || storageErr == nil {
					t.Fatal("mixed-error guard did not get a real token and database failure")
				}
				result, err = s.recordQueryCompatibilityFailure(ctx, token, safeRecoveryErrors(videoruntime.ErrUnsupported, storageErr))
				if err != nil || result.disposition != queryRecoveryNoop {
					t.Fatal("mixed storage cause authorized an unsupported-contract diagnostic")
				}
			} else {
				result, err = s.applyQueryReceiptProgress(ctx, item.row)
				if name == "cancelled" {
					if !errors.Is(err, context.Canceled) {
						t.Fatal("compatibility diagnostic lost caller cancellation")
					}
				} else if !errors.Is(err, videoruntime.ErrUnsupported) {
					t.Fatal("unsupported contract was interpreted or lost its error")
				}
			}
			var after QueryReceipt
			queryRecoveryCheck(t, h.db.Where("org_id = ? AND attempt_id = ?", item.row.OrgID, item.row.AttemptID).Take(&after).Error)
			if name == "known_receipt" {
				if result.disposition != queryRecoveryDeferred || after.ApplyErrorCode != "runtime_contract_unsupported" {
					t.Fatal("old supported receipt diagnostic was not preserved")
				}
				// The old label alone may change; due/version/unknown State do not.
				after.ApplyErrorCode = before.ApplyErrorCode
			} else if result.disposition != queryRecoveryNoop {
				t.Fatal("a rejected compatibility path claimed a committed action")
			}
			if !reflect.DeepEqual(before, after) || h.finances(t) != money || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != storage || h.evidenceStorage(t) != evidence || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
				t.Fatal("compatibility diagnosis changed unknown state, progress, evidence or funds")
			}
		})
	}
}
