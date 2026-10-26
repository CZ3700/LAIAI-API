package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Observe the real clock statement on its transaction connection. The delayed
// case changes only that statement in this test's callback, exercising driver
// cancellation rather than substituting a clock implementation.
func TestMySQLPersistedRecoveryClockBudgetIsScoped(t *testing.T) {
	for _, mode := range []string{"ordinary", "recovery", "short_parent", "slow_recovery"} {
		t.Run(mode, func(t *testing.T) {
			f := mysqlFixture(t)
			var hits atomic.Int32
			var pool gorm.ConnPool
			var parentDeadline time.Time
			name := fmt.Sprintf("test:recovery-clock-%d", f.key.OrgID)
			must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
				if !strings.Contains(q.Statement.SQL.String(), "SELECT UTC_TIMESTAMP(3) AS database_time") {
					return
				}
				n := hits.Add(1)
				deadline, ok := q.Statement.Context.Deadline()
				if !ok || q.Statement.ConnPool != pool {
					t.Error("clock lost its caller deadline or transaction connection")
				}
				if mode == "ordinary" {
					if !deadline.Equal(parentDeadline) {
						t.Error("recovery clock limit leaked into ordinary dispatch")
					}
				} else if deadline.After(parentDeadline) || time.Until(deadline) > 200*time.Millisecond {
					t.Error("recovery clock exceeded its child or earlier parent deadline")
				}
				if mode == "slow_recovery" && n == 1 {
					q.Statement.SQL.Reset()
					q.Statement.SQL.WriteString("SELECT UTC_TIMESTAMP(3) AS database_time, SLEEP(3) AS clock_delay")
					q.Statement.Vars = nil
				}
			}))
			t.Cleanup(func() { f.s.DB.Callback().Row().Remove(name) })
			budget := 4 * time.Second
			if mode == "short_parent" {
				budget = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			parentDeadline, _ = ctx.Deadline()
			read := func(callCtx context.Context) (recoveryLaneResult, error) {
				tx := f.s.DB.WithContext(callCtx).Begin()
				if tx.Error != nil {
					return recoveryLaneResult{Failed: 1}, tx.Error
				}
				defer tx.Rollback()
				pool = tx.Statement.ConnPool
				var before, after uint64
				if err := tx.Raw("SELECT CONNECTION_ID()").Scan(&before).Error; err != nil {
					return recoveryLaneResult{Failed: 1}, err
				}
				clock, err := authorityTimeTx(tx)
				if err != nil {
					return recoveryLaneResult{Failed: 1}, err
				}
				if err = tx.Raw("SELECT CONNECTION_ID()").Scan(&after).Error; err != nil {
					return recoveryLaneResult{Failed: 1}, err
				}
				if clock.IsZero() || before == 0 || before != after {
					t.Error("clock did not execute on the same live MySQL transaction")
				}
				return recoveryLaneResult{Noop: 1}, nil
			}
			var err error
			if mode == "ordinary" {
				_, err = read(ctx)
			} else {
				lanes := []recoveryLane{{recoveryCreateInbox, read}}
				if mode == "slow_recovery" {
					lanes = append(lanes, recoveryLane{recoveryQueryReceipts, read})
				}
				err = f.s.runRecoveryLanes(ctx, lanes)
			}
			if mode == "slow_recovery" {
				if !errors.Is(err, context.DeadlineExceeded) || hits.Load() != 2 || ctx.Err() != nil {
					t.Fatal("actual slow clock did not time out independently before the healthy lane")
				}
				metrics := f.s.metrics().runtimeSnapshot().RecoveryLanes
				if metrics[recoveryCreateInbox].Failed != 1 || metrics[recoveryQueryReceipts].Noop != 1 || metrics[recoveryQueryReceipts].Errors != 0 {
					t.Fatal("clock failure prevented successful recovery in the following lane")
				}
			} else if err != nil || hits.Load() != 1 {
				t.Fatal("real clock positive control did not execute exactly once")
			}
		})
	}
}
