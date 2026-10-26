package video

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

// E05: the DB expiry occurs AFTER successful immutable/window preparation and
// BEFORE the actual last COUNT. Inspect its real SQL/Vars, never fake Count.
func TestMySQLExecutionInputFinalSQLUsesDatabaseExpiry(t *testing.T) {
	for _, mode := range []string{"create_legacy", "create_inbox", "cancel_task", "delete_task"} {
		for _, expire := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expire=%v", mode, expire), func(t *testing.T) {
				var f *fixture
				var taskID string
				var op Operation
				var a ExecutionAttempt
				var producer *mutationProducerFixture
				if strings.HasPrefix(mode, "create_") {
					f = mysqlFixture(t)
					f.s.ResponseInboxEnabled = mode == "create_inbox"
					v := f.create(t, "execution-final-expiry")
					taskID = v.ID
				} else {
					final := ""
					if mode == "delete_task" {
						final = "succeeded"
					}
					producer = newMutationProducerFixture(t, final)
					f = producer.fixture
					taskID = producer.taskID
				}
				expires := executionFixtureNow(t, f.s.DB).Add(3 * time.Second)
				if !expire {
					expires = expires.Add(time.Hour)
				}
				if producer == nil {
					executionOldCreateInput(t, f, taskID, expires)
					_, _, op = executionCreateRows(t, f, taskID)
				} else {
					a, op = executionOldMutationClaim(t, producer.mutationConsumerFixture, mode, expires)
				}
				before := authorityFinancialFacts(t, f, taskID)
				var hit atomic.Bool
				name := fmt.Sprintf("execution-final-expiry-%d", f.key.OrgID)
				must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
					if q.Statement.Table != "o" {
						return
					}
					callbacks.BuildQuerySQL(q)
					text := q.Statement.SQL.String()
					if !strings.Contains(strings.ToLower(text), "count(*)") || !strings.Contains(text, "? > UTC_TIMESTAMP(3)") || !hit.CompareAndSwap(false, true) {
						return
					}
					found := false
					for _, v := range q.Statement.Vars {
						if at, ok := v.(time.Time); ok && at.Equal(expires) {
							found = true
						}
					}
					if !found {
						t.Error("final COUNT did not bind the verified immutable expiry")
					}
					if expire {
						executionWaitExpired(t, f.s.DB, expires)
					}
				}))
				t.Cleanup(func() { _ = f.s.DB.Callback().Query().Remove(name) })
				if producer == nil {
					must(t, f.s.processOperation(context.Background(), op))
					want := int32(1)
					if expire {
						want = 0
					}
					if f.posts.Load() != want {
						t.Fatal("create dispatch did not obey final DB expiry", f.posts.Load())
					}
				} else {
					_, err := f.s.readMutationAction(context.Background(), op, a, "official-test-credential")
					must(t, err)
					allowed, err := f.s.mutationOperationMaySend(context.Background(), op, a)
					must(t, err)
					if allowed == expire {
						t.Fatal("action final permission ignored database expiry", allowed)
					}
					// This is the final SQL guard, not a claim that a short-lived
					// input was emitted by the new 24h producer factory.
				}
				must(t, f.s.DB.Callback().Query().Remove(name))
				if !hit.Load() {
					t.Fatal("actual final COUNT expiry predicate did not execute")
				}
				if authorityFinancialFacts(t, f, taskID) != before {
					t.Fatal("final expiry check changed financial facts")
				}
				if expire && responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND operation_id = ?", op.OrgID, op.ID) != 0 {
					t.Fatal("last-moment loss of permission synthesized no-send evidence")
				}
				var identity invocation.VideoExecutionIdentity
				must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
					var err error
					if producer == nil {
						_, st, current := executionCreateRows(t, f, taskID)
						identity, err = readCreateExecutionIdentityTx(tx, st, current)
					} else {
						identity, err = invocation.ReadVideoActionInputIdentityTx(tx, op.OrgID, a.InputPayloadRef)
					}
					return err
				}))
				if !identity.ExpiresAt.Equal(expires) {
					t.Fatal("test changed immutable expiry")
				}
			})
		}
	}
}
