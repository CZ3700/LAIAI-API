package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

// All four cases block a real producer on its exact scope/action rate row on
// connection B. The callback only signals SQL entry; it never simulates a lock
// or supplies a clock. No provider request is made outside the mock transport.
func TestMySQLDatabaseAuthorityClaimTimeAfterLockWait(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		inbox        bool
	}{
		{"legacy_create", "create_task", false},
		{"inbox_create", "create_task", true},
		{"legacy_delete", "delete_task", false},
		{"inbox_cancel", "cancel_task", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f *fixture
			var op Operation
			var id string
			if tc.action == "create_task" {
				f = mysqlFixture(t)
				f.s.ResponseInboxEnabled = tc.inbox
				id = f.create(t, "authority-lock-create").ID
				op = runtimeWorkerCreateOperation(t, f, id)
			} else {
				final := ""
				if tc.action == "delete_task" {
					final = "succeeded"
				}
				m := newMutationConsumerFixture(t, final)
				f, id = m.fixture, m.taskID
				f.s.MutationResponseInboxEnabled = tc.inbox
				op = m.pending(t, tc.action)
			}
			_, state, err := loadTask(f.s.DB, f.key.OrgID, id, false)
			must(t, err)
			action := "delete"
			if tc.action == "create_task" {
				action = "create"
			}
			beforeFunds := authorityFinancialFacts(t, f, id)
			must(t, f.s.DB.Model(&ProviderRate{}).Create(map[string]any{"scope_id": state.ProviderScopeID, "action": action, "next_allowed_at": gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)")}).Error)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			blocker := f.s.DB.WithContext(ctx).Begin()
			must(t, blocker.Error)
			defer blocker.Rollback()
			var rate ProviderRate
			must(t, locked(blocker).Where("scope_id = ? AND action = ?", state.ProviderScopeID, action).Take(&rate).Error)
			lockedAt, err := authorityFixtureNow(blocker)
			must(t, err)
			var blockerID uint64
			must(t, blocker.Raw("SELECT CONNECTION_ID()").Scan(&blockerID).Error)
			entered := make(chan struct{})
			var signalled atomic.Bool
			name := fmt.Sprintf("test:authority-claim-lock-%d", f.key.OrgID)
			must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "video_provider_rates" && signalled.CompareAndSwap(false, true) {
					close(entered)
				}
			}))
			t.Cleanup(func() { f.s.DB.Callback().Create().Remove(name) })
			var sends atomic.Int32
			var sentClaim Operation
			var sentAt time.Time
			f.s.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				sends.Add(1)
				if err := f.s.DB.WithContext(ctx).Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).Take(&sentClaim).Error; err != nil {
					return nil, err
				}
				sentAt, err = authorityFixtureNow(f.s.DB.WithContext(ctx))
				if err != nil {
					return nil, err
				}
				if tc.action == "create_task" && req.Method == http.MethodPost {
					return reply(200, fmt.Sprintf(`{"id":%q}`, "up_"+id)), nil
				}
				if tc.action != "create_task" && req.Method == http.MethodDelete {
					return reply(204, ""), nil
				}
				return nil, errors.New("unexpected authority claim HTTP method")
			})}
			done := make(chan error, 1)
			go func() { done <- f.s.processOperation(ctx, op) }()
			finished := false
			defer func() {
				if !finished {
					cancel()
					_ = blocker.Rollback().Error
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("authority producer did not stop during cleanup")
					}
				}
			}()
			select {
			case <-entered:
			case err := <-done:
				finished = true
				t.Fatal("producer missed the real rate lock", err)
			case <-ctx.Done():
				t.Fatal("producer did not enter the rate SQL")
			}
			// Prove this is an engine lock wait on the blocker connection, not
			// merely a scheduled goroutine or a mocked callback delay.
			waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
			defer waitCancel()
			for {
				var waits int64
				must(t, f.s.DB.WithContext(waitCtx).Raw(`SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.threads b ON b.THREAD_ID = w.BLOCKING_THREAD_ID WHERE b.PROCESSLIST_ID = ?`, blockerID).Scan(&waits).Error)
				if waits > 0 {
					break
				}
				select {
				case err := <-done:
					finished = true
					t.Fatal("producer escaped the rate lock", err)
				case <-waitCtx.Done():
					t.Fatal("real engine wait was not observed")
				case <-time.After(5 * time.Millisecond):
				}
			}
			var releasedAt time.Time
			for {
				releasedAt, err = authorityFixtureNow(blocker)
				must(t, err)
				if releasedAt.Sub(lockedAt) >= 25*time.Millisecond {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("database clock did not advance during the lock wait")
				case <-time.After(5 * time.Millisecond):
				}
			}
			if sends.Load() != 0 || !releasedAt.After(lockedAt) {
				t.Fatal("claim sent while its rate authority remained locked")
			}
			must(t, blocker.Commit().Error)
			select {
			case err := <-done:
				finished = true
				must(t, err)
			case <-ctx.Done():
				t.Fatal("producer did not finish after rate lock release")
			}
			if sends.Load() != 1 || sentClaim.Status != "in_flight" || sentClaim.AttemptID == "" || sentClaim.Version != op.Version+1 || sentClaim.LeaseUntil == nil {
				t.Fatal("lock wait did not produce one durable exact claim and request")
			}
			claimAt := sentClaim.LeaseUntil.Add(-180 * time.Second)
			if claimAt.Before(releasedAt) || claimAt.After(sentAt) {
				t.Fatalf("claim authority was sampled before the lock wait ended: locked=%s released=%s claimed=%s sent=%s", lockedAt, releasedAt, claimAt, sentAt)
			}
			var currentRate ProviderRate
			must(t, f.s.DB.Where("scope_id = ? AND action = ?", state.ProviderScopeID, action).Take(&currentRate).Error)
			rateAt := currentRate.NextAllowedAt.Add(-time.Second)
			if rateAt.Before(releasedAt) || rateAt.After(claimAt) || authorityFinancialFacts(t, f, id) != beforeFunds {
				t.Fatal("rate used pre-lock time or the claim changed funds")
			}
			wantRegistered := int64(0)
			phase := "delete_sending"
			if tc.action == "create_task" {
				phase = "sending"
			}
			if tc.inbox {
				wantRegistered = 1
			}
			if responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", f.key.OrgID, op.ID) != wantRegistered || responseInboxCount(t, f, &Attempt{}, "org_id = ? AND operation_id = ? AND phase = ?", f.key.OrgID, op.ID, phase) != 1 {
				t.Fatal("claim, sending attempt and registration did not commit together")
			}
			// Action input registration subsequently updates payload_ref, which
			// advances Operation.UpdatedAt. The immutable sending observation is
			// the timestamp written atomically with this exact claim.
			var sending Attempt
			must(t, f.s.DB.Where("org_id = ? AND operation_id = ? AND attempt_id = ? AND phase = ?", f.key.OrgID, op.ID, sentClaim.AttemptID, phase).Take(&sending).Error)
			if !sending.ObservedAt.Equal(claimAt) {
				t.Fatal("claim lease and its sending attempt used different authority times")
			}
			t.Logf("DB lock proof: locked=%s released=%s rate=%s claim=%s sent=%s", lockedAt, releasedAt, rateAt, claimAt, sentAt)
		})
	}
}

func TestMySQLDatabaseAuthorityFinalQueryGuard(t *testing.T) {
	for _, fault := range []string{"sequence_replaced", "lease_expired", "create_review", "target_changed", "terminal_pending_usage", "response_after_expiry"} {
		t.Run(fault, func(t *testing.T) {
			m := newMutationConsumerFixture(t, "")
			f, id := m.fixture, m.taskID
			if fault == "terminal_pending_usage" {
				// Establish the first observation through the real GET producer.
				// The consumer-only observe helper does not claim QuerySequence,
				// so using it here would manufacture a receipt/claim collision.
				initialTask, initialState := m.rows(t)
				initialFunds := authorityFinancialFacts(t, f, id)
				var initialGETs atomic.Int32
				f.s.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.Method != http.MethodGet {
						return nil, errors.New("terminal query fixture attempted a mutation")
					}
					initialGETs.Add(1)
					return reply(200, fmt.Sprintf(`{"id":%q,"status":"succeeded"}`, initialTask.UpstreamTaskID)), nil
				})}
				makeFixtureQueryDue(t, f.s.DB, f.key.OrgID, id, initialState.QuerySequence, false)
				initialCtx, initialCancel := context.WithTimeout(context.Background(), 15*time.Second)
				initialErr := f.s.pollTask(initialCtx, f.key.OrgID, id)
				initialCancel()
				must(t, initialErr)
				terminalTask, terminalState := m.rows(t)
				if initialGETs.Load() != 1 || terminalTask.Status != string(ark.StatusSucceeded) || terminalState.BillingState != "pending_usage" || terminalState.QuerySequence != 1 || terminalState.AppliedQuerySequence != 1 || authorityFinancialFacts(t, f, id) != initialFunds {
					t.Fatal("first real GET did not establish an unpaid terminal observation and sequence")
				}
				makeFixtureProviderRateDue(t, f.s.DB, terminalState.ProviderScopeID, "query")
			}
			row, state := m.rows(t)
			makeFixtureQueryDue(t, f.s.DB, f.key.OrgID, id, state.QuerySequence, false)
			beforeFunds := authorityFinancialFacts(t, f, id)
			beforeReceipts := responseInboxCount(t, f, &QueryReceipt{}, "org_id = ? AND task_id = ?", f.key.OrgID, id)
			var gets atomic.Int32
			var expiredDuringHTTP atomic.Bool
			f.s.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					return nil, errors.New("query authority attempted a mutation")
				}
				gets.Add(1)
				if fault == "response_after_expiry" {
					_, current := m.rows(t)
					makeFixtureQueryDue(t, f.s.DB, f.key.OrgID, id, current.QuerySequence, true)
					expiredDuringHTTP.Store(true)
				}
				status := "running"
				if fault == "terminal_pending_usage" {
					status = "succeeded"
				}
				return reply(200, fmt.Sprintf(`{"id":%q,"status":%q}`, row.UpstreamTaskID, status)), nil
			})}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			stopped := make(chan struct{})
			entered, resume := pauseVideoCredentialRead(t, f, cancel, stopped)
			done := make(chan error, 1)
			go func() {
				defer close(stopped)
				done <- f.s.pollTask(ctx, f.key.OrgID, id)
			}()
			finished := false
			defer func() {
				resume()
				if !finished {
					cancel()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("authority query did not stop during cleanup")
					}
				}
			}()
			select {
			case <-entered:
			case err := <-done:
				finished = true
				t.Fatal("query missed its post-credential barrier", err)
			case <-ctx.Done():
				t.Fatal("query did not reach credential preparation")
			}
			_, claim := m.rows(t)
			if claim.QuerySequence != state.QuerySequence+1 || claim.QueryLeaseUntil == nil || gets.Load() != 0 {
				t.Fatal("query preparation did not follow its committed claim")
			}
			switch fault {
			case "sequence_replaced":
				result := f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ? AND query_sequence = ?", f.key.OrgID, id, claim.QuerySequence).UpdateColumn("query_sequence", claim.QuerySequence+1)
				must(t, result.Error)
				if result.RowsAffected != 1 {
					t.Fatal("query replacement did not hit the exact sequence")
				}
			case "lease_expired":
				makeFixtureQueryDue(t, f.s.DB, f.key.OrgID, id, claim.QuerySequence, true)
			case "create_review":
				create := runtimeWorkerCreateOperation(t, f, id)
				var attempt ExecutionAttempt
				must(t, f.s.DB.Where("org_id = ? AND operation_id = ?", f.key.OrgID, create.ID).Take(&attempt).Error)
				conflict := responseInboxAppend(t, f, attempt, responseInboxResult(ark.OutcomeAccepted, "other_"+id))
				if err := f.s.applyCreateInbox(context.Background(), f.key.OrgID, conflict.ReceiptID, nil); !errors.Is(err, ErrConflict) || responseInboxApp(t, f, conflict).State != "review" {
					t.Fatal("query barrier did not overlap a real identity quarantine", err)
				}
			case "target_changed":
				result := f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ? AND upstream_task_id = ?", f.key.OrgID, id, row.UpstreamTaskID).UpdateColumn("upstream_task_id", "other_"+id)
				must(t, result.Error)
				if result.RowsAffected != 1 {
					t.Fatal("target replacement did not hit the original binding")
				}
			}
			afterFault := authorityTaskFacts(t, f, id)
			resume()
			select {
			case err := <-done:
				finished = true
				must(t, err)
			case <-ctx.Done():
				t.Fatal("query did not finish after preparation resumed")
			}
			positive := fault == "terminal_pending_usage" || fault == "response_after_expiry"
			if !positive {
				if gets.Load() != 0 || responseInboxCount(t, f, &QueryReceipt{}, "org_id = ? AND task_id = ?", f.key.OrgID, id) != beforeReceipts || authorityTaskFacts(t, f, id) != afterFault {
					t.Fatal("lost query authority sent HTTP, fabricated evidence or cleared its lease")
				}
			} else {
				var receipt QueryReceipt
				must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND sequence = ?", f.key.OrgID, id, claim.QuerySequence).Take(&receipt).Error)
				var payload invocation.VideoPayload
				must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, receipt.PayloadRef).Take(&payload).Error)
				if gets.Load() != 1 || receipt.AppliedAt == nil || receipt.PayloadRef == "" || payload.CaptureStatus != invocation.VideoCaptureStaged || fault == "response_after_expiry" && !expiredDuringHTTP.Load() {
					t.Fatal("valid or already-received GET lost durable typed evidence")
				}
				must(t, f.s.applyQueryReceipt(context.Background(), receipt))
				if gets.Load() != 1 {
					t.Fatal("query receipt replay performed another HTTP request")
				}
			}
			if authorityFinancialFacts(t, f, id) != beforeFunds {
				t.Fatal("query authority test changed the hold, Key, quota or ledger without usage")
			}
		})
	}
}
