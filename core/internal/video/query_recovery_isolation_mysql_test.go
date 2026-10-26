package video

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

func queryIsolationMoney(t *testing.T, h *queryRecoveryFixture) {
	t.Helper()
	// Org0 in this fixture has two held tasks; Org1 has one settled task.
	// These literals do not derive their expectations from a production quote.
	for index, f := range h.users {
		var key identity.APIKey
		var account ledger.Account
		queryRecoveryCheck(t, h.db.Where("org_id = ? AND id = ?", f.key.OrgID, f.key.ID).Take(&key).Error)
		queryRecoveryCheck(t, h.db.Where("org_id = ? AND type = ? AND shard = 0", f.key.OrgID, ledger.AccountUserCredit).Take(&account).Error)
		held, used, posted := "1.728", "1.728", "100"
		if index == 1 {
			held, used, posted = "0", "0.0008", "99.9992"
		}
		if !account.HeldAmount.Equal(decimal.RequireFromString(held)) || !account.PostedBalance.Equal(decimal.RequireFromString(posted)) || !key.UsedAmount.Equal(decimal.RequireFromString(used)) {
			t.Fatal("cross-lane recovery changed reserved/actual Key or account amounts")
		}
	}
	var buckets []videobilling.QuotaBucket
	queryRecoveryCheck(t, h.db.Find(&buckets).Error)
	if len(buckets) != 1 || buckets[0].OrgID != 0 || !buckets[0].ReservedAmount.Equal(decimal.RequireFromString("9.936")) || !buckets[0].ConsumedAmount.Equal(decimal.RequireFromString("0.0046")) {
		t.Fatal("cross-lane recovery changed shared provider budget units or reservations")
	}
}

func TestMySQLQueryRecoveryNoNetworkReplayAndIsolation(t *testing.T) {
	t.Run("three_lanes_continue_after_real_sql_failure", func(t *testing.T) {
		h := queryRecoveryNew(t)
		query := h.pending(t, 1, 0)
		h.arrange(t, &query, false, 1)
		f := h.users[0]
		create := f.create(t, "zero-network-create")
		createAttempt, createClaim := responseInboxRegister(t, f, create.ID)
		createReceipt := responseInboxAppend(t, f, createAttempt, responseInboxResult(ark.OutcomeAccepted, "up_"+create.ID))
		expireFixtureOperationLease(t, h.db, createClaim)
		responseInboxMakeDue(t, f, createReceipt)
		// Reuse the actual consumer prepare/Stage/register/append path within
		// this private schema, without constructing fake immutable HMAC rows.
		mutationTask, _, _ := responseInboxAcceptWithoutHTTP(t, f, "zero-network-cancel")
		m := &mutationConsumerFixture{fixture: f, taskID: mutationTask.ID}
		mutationAttempt, mutationClaim := m.register(t, "cancel_task")
		mutationReceipt := m.append(t, mutationAttempt, ark.Result{Action: ark.ActionDelete, Outcome: ark.OutcomeAccepted, HTTPStatus: 204})
		expireFixtureOperationLease(t, h.db, mutationClaim)
		responseInboxMakeDue(t, f, mutationReceipt)
		s := h.service(t)
		h.forbidNetwork(t, s)
		evidence := h.evidenceStorage(t)
		var hits int
		hook := "test:query-isolation-first-lane-sql"
		queryRecoveryCheck(t, h.db.Callback().Row().Before("gorm:row").Register(hook, func(tx *gorm.DB) {
			if tx.Statement.Table != "ap" {
				return
			}
			callbacks.BuildQuerySQL(tx)
			sqlText := strings.ToLower(tx.Statement.SQL.String())
			if !strings.Contains(sqlText, "ea.target_upstream_task_id is null") {
				return
			}
			hits++
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT query_isolation_missing_candidate FROM video_receipt_applications LIMIT 1")
			tx.Statement.Vars = nil
		}))
		err := s.ProcessPersistedEvidenceOnce(context.Background())
		queryRecoveryCheck(t, h.db.Callback().Row().Remove(hook))
		var cause *drivermysql.MySQLError
		stats := s.metrics().runtimeSnapshot().RecoveryLanes
		if hits != 1 || !errors.As(err, &cause) || cause.Number != 1054 || strings.Contains(err.Error(), "query_isolation_missing_candidate") || stats[0].Selected != 0 || stats[0].Failed != 1 || stats[1].Applied != 1 || stats[3].Applied != 1 {
			t.Fatal("first lane SQL failure was hidden or prevented later committed recovery")
		}
		if responseInboxApp(t, f, createReceipt).State != "pending" || responseInboxApp(t, f, mutationReceipt).State != "applied" || m.operation(t, mutationClaim.ID).Status != "unknown" || f.hold(t, create.ID).Status != ledger.HoldActive || f.hold(t, mutationTask.ID).Status != ledger.HoldActive {
			t.Fatal("partial cycle misapplied create or inferred financial cancellation from ACK")
		}
		queryRecoveryAssertTaskMoney(t, h, query, true)
		queryIsolationMoney(t, h)
		queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(context.Background()))
		if responseInboxApp(t, f, createReceipt).State != "applied" || runtimeWorkerCreateOperation(t, f, create.ID).Status != "confirmed" {
			t.Fatal("recovered first lane did not consume its durable create evidence")
		}
		queryIsolationMoney(t, h)
		funds := h.finances(t)
		createApp, mutationApp, queryRow := responseInboxApp(t, f, createReceipt), responseInboxApp(t, f, mutationReceipt), queryTemporalRow(t, h, query)
		queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(context.Background()))
		if h.finances(t) != funds || h.evidenceStorage(t) != evidence || !reflect.DeepEqual(createApp, responseInboxApp(t, f, createReceipt)) || !reflect.DeepEqual(mutationApp, responseInboxApp(t, f, mutationReceipt)) || !reflect.DeepEqual(queryRow, queryTemporalRow(t, h, query)) || h.blocked.Load() != 0 || h.readKeys.Load() != 0 || h.users[0].posts.Load() != 0 || h.users[0].gets.Load() != 0 || h.users[1].posts.Load() != 1 || h.users[1].gets.Load() != 1 {
			t.Fatal("zero-network recovery replay changed evidence, financial facts or fetched credentials")
		}
	})

	for _, mode := range []string{"tombstone", "stale", "review_then_tombstone", "review_then_stale"} {
		t.Run(mode, func(t *testing.T) {
			h := queryRecoveryNew(t)
			item := h.pending(t, 0, 0)
			review := strings.HasPrefix(mode, "review_")
			h.arrange(t, &item, review, 1)
			s := h.service(t)
			// Setup remains on the counted synthetic transport. The credential
			// read prohibition starts after the deliberate newer GET below.
			s.Client = item.f.s.Client
			if review {
				result, err := s.applyQueryReceiptProgress(context.Background(), item.row)
				if result.disposition != queryRecoveryReview || !errors.Is(err, errQueryObservationInvalid) {
					t.Fatal("current parser did not establish the real NULL review barrier")
				}
				if item.f.posts.Load() != 1 || item.f.gets.Load() != 1 {
					t.Fatal("review application unexpectedly fetched provider facts")
				}
			}
			if strings.HasSuffix(mode, "tombstone") {
				// Explicit storage-state boundary injection. This is not an
				// administrative deletion/permission implementation or release.
				q := h.db.Model(&State{}).Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).UpdateColumn("deleted_at", gorm.Expr("UTC_TIMESTAMP(3)"))
				queryRecoveryCheck(t, q.Error)
				if q.RowsAffected != 1 {
					t.Fatal("tombstone fixture did not affect exactly its task")
				}
			} else {
				// Produce the newer terminal observation through an actual GET
				// and reducer; do not invent AppliedQuerySequence with SQL.
				var state State
				queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).Take(&state).Error)
				makeFixtureQueryDue(t, h.db, item.row.OrgID, item.row.TaskID, state.QuerySequence, true)
				makeFixtureProviderRateDue(t, h.db, h.scope, "query")
				queryRecoveryCheck(t, item.f.s.pollTask(context.Background(), item.row.OrgID, item.row.TaskID))
				queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).Take(&state).Error)
				if state.AppliedQuerySequence != item.row.Sequence+1 || item.f.gets.Load() != 2 {
					t.Fatal("real newer GET did not make the original receipt stale")
				}
			}
			h.forbidNetwork(t, s)
			before := queryTemporalRow(t, h, item)
			funds, storage, evidence := h.finances(t), h.taskStorage(t, item.row.OrgID, item.row.TaskID), h.evidenceStorage(t)
			result, err := s.applyQueryReceiptProgress(context.Background(), item.row)
			after := queryTemporalRow(t, h, item)
			if review {
				if !errors.Is(err, errQueryRecoveryReview) {
					t.Fatal("stale/tombstone ordinary apply did not return the committed review error")
				}
				if result.disposition != queryRecoveryNoop {
					t.Fatal("stale/tombstone ordinary apply did not preserve the review no-op disposition")
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("stale/tombstone ordinary apply changed the committed review receipt")
				}
			} else {
				want := "stale_query_response"
				if mode == "tombstone" {
					want = "task_deleted"
				}
				if err != nil || result.disposition != queryRecoveryApplied || after.AppliedAt == nil || after.ApplyErrorCode != want || after.RecoveryVersion != before.RecoveryVersion+1 {
					t.Fatal("original tombstone/stale reducer behavior changed")
				}
			}
			queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(context.Background()))
			if h.finances(t) != funds || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != storage || h.evidenceStorage(t) != evidence || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
				t.Fatal("boundary recovery changed task/financial facts or used network/decryption")
			}
		})
	}
}
