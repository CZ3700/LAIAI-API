package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
)

// E08 injects actual SQL errors at each host boundary. Before commit there may
// be no partial reserve/input/registration; after commit there must be no local
// NotSent, refund, or HTTP merely because the execution authority is unavailable.
func TestMySQLExecutionInputHostClockFailuresRemainUncertain(t *testing.T) {
	for _, mode := range []string{"create", "action"} {
		for _, phase := range []string{"prepare", "stage", "read", "final_count"} {
			t.Run(mode+"/"+phase, func(t *testing.T) {
				var f *fixture
				var p *mutationProducerFixture
				var taskID string
				var op Operation
				if mode == "create" {
					f = mysqlFixture(t)
					f.s.ResponseInboxEnabled = true
					taskID = f.create(t, "execution-clock-anchor").ID
					_, _, op = executionCreateRows(t, f, taskID)
				} else {
					p = newMutationProducerFixture(t, "")
					f = p.fixture
					taskID = p.taskID
					op = p.pending(t, "cancel_task")
				}
				before := authorityFinancialFacts(t, f, taskID)
				payloads := responseInboxCount(t, f, &invocation.VideoPayload{}, "org_id = ?", f.key.OrgID)
				tasks := responseInboxCount(t, f, &task.Task{}, "org_id = ?", f.key.OrgID)
				attempts := responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ?", f.key.OrgID)
				var armed, hit atomic.Bool
				if phase == "prepare" {
					armed.Store(true)
				}
				name := fmt.Sprintf("execution-clock-%d", f.key.OrgID)
				must(t, f.s.DB.Callback().Create().After("gorm:create").Register(name, func(q *gorm.DB) {
					if phase == "stage" && q.Statement.Table == "invocation_payload_staging" {
						armed.Store(true)
					}
				}))
				must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
					if phase == "read" && q.Statement.Table == "invocation_payloads" {
						if lock, ok := q.Statement.Clauses["FOR"].Expression.(clause.Locking); ok && lock.Strength == "SHARE" {
							armed.Store(true)
						}
					}
					if phase != "final_count" || q.Statement.Table != "o" {
						return
					}
					callbacks.BuildQuerySQL(q)
					sql := q.Statement.SQL.String()
					if strings.Contains(strings.ToLower(sql), "count(*)") && strings.Contains(sql, "? > UTC_TIMESTAMP(3)") && hit.CompareAndSwap(false, true) {
						q.Statement.SQL.Reset()
						q.Statement.SQL.WriteString(strings.ReplaceAll(sql, "UTC_TIMESTAMP(3)", "b1b_nonexistent_clock()"))
					}
				}))
				must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
					if !armed.Load() || !strings.Contains(q.Statement.SQL.String(), "SELECT UTC_TIMESTAMP(3) AS database_time") || !hit.CompareAndSwap(false, true) {
						return
					}
					q.Statement.SQL.Reset()
					q.Statement.SQL.WriteString("SELECT b1b_nonexistent_clock_column AS database_time")
					q.Statement.Vars = nil
				}))
				remove := func() {
					must(t, f.s.DB.Callback().Create().Remove(name))
					must(t, f.s.DB.Callback().Query().Remove(name))
					must(t, f.s.DB.Callback().Row().Remove(name))
				}
				t.Cleanup(func() {
					_ = f.s.DB.Callback().Create().Remove(name)
					_ = f.s.DB.Callback().Query().Remove(name)
					_ = f.s.DB.Callback().Row().Remove(name)
				})
				var err error
				if mode == "create" && (phase == "prepare" || phase == "stage") {
					_, _, err = f.s.Create(context.Background(), f.key, "clock-failing-create", "clock-failing-create", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "clock boundary"}}})
				} else if mode == "create" {
					err = f.s.processOperation(context.Background(), op)
				} else {
					err = f.s.processMutation(context.Background(), op)
				}
				remove()
				if !hit.Load() || err == nil {
					t.Fatal("actual execution clock fault did not reject operation", err)
				}
				if phase != "final_count" && !errors.Is(err, invocation.ErrVideoExecutionAuthorityUnavailable) {
					t.Fatal("execution clock error lost its dedicated classification", err)
				}
				if strings.Contains(err.Error(), "b1b_nonexistent") || strings.Contains(err.Error(), "SELECT") {
					t.Fatal("SQL diagnostic escaped safe execution error")
				}
				if authorityFinancialFacts(t, f, taskID) != before || f.posts.Load() != 0 || p != nil && p.deletes.Load() != 0 {
					t.Fatal("clock failure refunded, reserved, or sent HTTP")
				}
				if responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND operation_id = ?", op.OrgID, op.ID) != 0 {
					t.Fatal("clock failure became definite no-send evidence")
				}
				if phase == "prepare" || phase == "stage" {
					if responseInboxCount(t, f, &invocation.VideoPayload{}, "org_id = ?", f.key.OrgID) != payloads || responseInboxCount(t, f, &task.Task{}, "org_id = ?", f.key.OrgID) != tasks || responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ?", f.key.OrgID) != attempts {
						t.Fatal("failed preparation/stage left partial accepted records")
					}
				} else if responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ?", f.key.OrgID) != attempts+1 {
					t.Fatal("post-registration authority failure lost its immutable attempt")
				}
			})
		}
	}
}
