package video

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func queryTemporalRow(t *testing.T, h *queryRecoveryFixture, item queryRecoveryPending) QueryReceipt {
	t.Helper()
	var row QueryReceipt
	queryRecoveryCheck(t, h.db.Where("org_id = ? AND task_id = ? AND attempt_id = ?", item.row.OrgID, item.row.TaskID, item.row.AttemptID).Take(&row).Error)
	return row
}

func TestMySQLQueryRecoveryTemporaryFailuresRemainRetryable(t *testing.T) {
	t.Run("event_sql_natural_expiry", func(t *testing.T) {
		h := queryRecoveryNew(t)
		start, err := authorityFixtureNow(h.db)
		queryRecoveryCheck(t, err)
		// pending performs a real event INSERT rejected with MySQL 1054 after
		// the original GET receipt commits. Its due is never edited here.
		item := h.pending(t, 0, 0)
		end, err := authorityFixtureNow(h.db)
		queryRecoveryCheck(t, err)
		if item.row.RecoveryDueAt == nil || item.row.RecoveryDueAt.Before(start.Add(30*time.Second)) || item.row.RecoveryDueAt.After(end.Add(30*time.Second)) || !item.row.RecoveryDueAt.After(end) {
			t.Fatal("real SQL failure did not establish a future database thirty-second retry")
		}
		s := h.service(t)
		h.forbidNetwork(t, s)
		funds, evidence := h.finances(t), h.evidenceStorage(t)
		queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(context.Background()))
		if s.metrics().runtimeSnapshot().RecoveryLanes[3].Selected != 0 || !reflect.DeepEqual(item.row, queryTemporalRow(t, h, item)) || h.finances(t) != funds {
			t.Fatal("new recovery ignored a genuine future retry")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		var observed time.Time
		for {
			observed, err = authorityFixtureNow(h.db.WithContext(ctx))
			queryRecoveryCheck(t, err)
			if !observed.Before(*item.row.RecoveryDueAt) {
				break
			}
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				t.Fatal("database retry deadline did not naturally arrive within the bounded wait")
			}
		}
		t.Logf("natural retry observed after %d database milliseconds", observed.Sub(start).Milliseconds())
		queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(ctx))
		after := queryTemporalRow(t, h, item)
		if after.AppliedAt == nil || after.RecoveryVersion != item.row.RecoveryVersion+1 || h.evidenceStorage(t) != evidence || h.blocked.Load() != 0 || h.readKeys.Load() != 0 || item.f.posts.Load() != 1 || item.f.gets.Load() != 1 {
			t.Fatal("natural retry did not apply once without fetching or decrypting evidence")
		}
		queryRecoveryAssertTaskMoney(t, h, item, true)
		settled := h.finances(t)
		queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(ctx))
		if !reflect.DeepEqual(after, queryTemporalRow(t, h, item)) || h.finances(t) != settled {
			t.Fatal("natural retry replay changed committed evidence or money")
		}
	})

	t.Run("real_lock_wait_timeout", func(t *testing.T) {
		h := queryRecoveryNew(t)
		item := h.pending(t, 0, 0)
		h.arrange(t, &item, false, 1)
		s := h.service(t)
		h.forbidNetwork(t, s)
		funds, storage, evidence := h.finances(t), h.taskStorage(t, item.row.OrgID, item.row.TaskID), h.evidenceStorage(t)
		blocker := h.db.Begin()
		queryRecoveryCheck(t, blocker.Error)
		defer blocker.Rollback()
		var lockedID string
		queryRecoveryCheck(t, blocker.Raw("SELECT task_id FROM tasks WHERE org_id = ? AND task_id = ? FOR UPDATE", item.row.OrgID, item.row.TaskID).Scan(&lockedID).Error)
		var blockerID, waiterID uint64
		queryRecoveryCheck(t, blocker.Raw("SELECT CONNECTION_ID()").Scan(&blockerID).Error)
		var hits atomic.Int32
		hook := "test:query-temporal-lock-timeout"
		queryRecoveryCheck(t, h.db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
			var cause *drivermysql.MySQLError
			if tx.Statement.Table == "tasks" && errors.As(tx.Error, &cause) && cause.Number == 1205 && hits.Add(1) == 1 {
				tx.AddError(blocker.Rollback().Error)
			}
		}))
		defer h.db.Callback().Query().Remove(hook)
		start, err := authorityFixtureNow(h.db)
		queryRecoveryCheck(t, err)
		var result queryRecoveryResult
		var applyErr error
		queryRecoveryCheck(t, h.db.Connection(func(conn *gorm.DB) error {
			var original int
			if err := conn.Raw("SELECT @@SESSION.innodb_lock_wait_timeout").Scan(&original).Error; err != nil {
				return err
			}
			defer func() { queryRecoveryCheck(t, conn.Exec("SET SESSION innodb_lock_wait_timeout = ?", original).Error) }()
			if err := conn.Exec("SET SESSION innodb_lock_wait_timeout = 1").Error; err != nil {
				return err
			}
			if err := conn.Raw("SELECT CONNECTION_ID()").Scan(&waiterID).Error; err != nil {
				return err
			}
			local, err := NewService(conn, h.keyring, s.Secret, s.Log)
			if err != nil {
				return err
			}
			local.Client, local.Now = s.Client, s.Now
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, applyErr = local.applyQueryReceiptProgress(ctx, item.row)
			return nil
		}))
		end, err := authorityFixtureNow(h.db)
		queryRecoveryCheck(t, err)
		var cause *drivermysql.MySQLError
		if lockedID != item.row.TaskID || blockerID == 0 || waiterID == 0 || blockerID == waiterID || hits.Load() != 1 || !errors.As(applyErr, &cause) || cause.Number != 1205 {
			t.Fatal("fixture did not exercise a real independent-connection lock timeout")
		}
		// Failure before observing a receipt has no authority to mutate its
		// progress. It stays retryable and cannot become a permanent review.
		if result.disposition != queryRecoveryNoop || !reflect.DeepEqual(item.row, queryTemporalRow(t, h, item)) || h.finances(t) != funds || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != storage || h.evidenceStorage(t) != evidence || end.Before(start) {
			t.Fatal("pre-receipt lock timeout invented diagnostic authority or changed money")
		}
		queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(context.Background()))
		queryRecoveryAssertTaskMoney(t, h, item, true)
		if h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
			t.Fatal("lock-timeout recovery used provider credentials or HTTP")
		}
	})

	t.Run("cancel_after_current_receipt", func(t *testing.T) {
		h := queryRecoveryNew(t)
		item := h.pending(t, 0, 0)
		h.arrange(t, &item, false, 1)
		s := h.service(t)
		h.forbidNetwork(t, s)
		funds, storage := h.finances(t), h.taskStorage(t, item.row.OrgID, item.row.TaskID)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var hits atomic.Int32
		hook := "test:query-temporal-cancel"
		queryRecoveryCheck(t, h.db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
			row, ok := tx.Statement.Dest.(*QueryReceipt)
			_, current := tx.Statement.Clauses["FOR"]
			if tx.Error == nil && tx.RowsAffected == 1 && current && ok && row.OrgID == item.row.OrgID && row.AttemptID == item.row.AttemptID && hits.Add(1) == 1 {
				cancel()
			}
		}))
		result, err := s.applyQueryReceiptProgress(ctx, item.row)
		queryRecoveryCheck(t, h.db.Callback().Query().Remove(hook))
		if hits.Load() != 1 || !errors.Is(err, context.Canceled) || result.disposition != queryRecoveryNoop || !reflect.DeepEqual(item.row, queryTemporalRow(t, h, item)) || h.finances(t) != funds || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != storage || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
			t.Fatal("cancelled current read launched detached diagnostics or changed durable facts")
		}
		queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(context.Background()))
		queryRecoveryAssertTaskMoney(t, h, item, true)
	})

	t.Run("application_and_clock_sql_failures", func(t *testing.T) {
		h := queryRecoveryNew(t)
		item := h.pending(t, 0, 0)
		h.arrange(t, &item, false, 1)
		s := h.service(t)
		h.forbidNetwork(t, s)
		funds, storage, evidence := h.finances(t), h.taskStorage(t, item.row.OrgID, item.row.TaskID), h.evidenceStorage(t)
		var eventHits, clockHits int
		var eventCause error
		eventHook, clockHook := "test:query-temporal-event", "test:query-temporal-clock"
		queryRecoveryCheck(t, h.db.Callback().Create().Before("gorm:create").Register(eventHook, func(tx *gorm.DB) {
			event, ok := tx.Statement.Dest.(*Event)
			if ok && event.OrgID == item.row.OrgID && event.TaskID == item.row.TaskID && event.Type == "task.status_changed" {
				eventHits++
				eventCause = tx.Exec("INSERT INTO video_events (query_temporal_missing_event_column) VALUES (1)").Error
				tx.AddError(eventCause)
			}
		}))
		queryRecoveryCheck(t, h.db.Callback().Row().Before("gorm:row").Register(clockHook, func(tx *gorm.DB) {
			if eventHits == 1 && strings.Contains(tx.Statement.SQL.String(), "SELECT UTC_TIMESTAMP(3) AS database_time") {
				clockHits++
				tx.Statement.SQL.Reset()
				tx.Statement.SQL.WriteString("SELECT query_temporal_missing_clock_column AS database_time")
			}
		}))
		result, err := s.applyQueryReceiptProgress(context.Background(), item.row)
		queryRecoveryCheck(t, h.db.Callback().Create().Remove(eventHook))
		queryRecoveryCheck(t, h.db.Callback().Row().Remove(clockHook))
		var clockErr *authorityClockError
		var clockSQL *drivermysql.MySQLError
		if eventHits != 1 || clockHits != 1 || eventCause == nil || !errors.Is(err, eventCause) || !errors.As(err, &clockErr) || !errors.As(clockErr, &clockSQL) || clockSQL.Number != 1054 || result.disposition != queryRecoveryFailed || strings.Contains(err.Error(), "query_temporal_missing") {
			t.Fatal("application and diagnostic SQL failures were not both retained safely")
		}
		if !reflect.DeepEqual(item.row, queryTemporalRow(t, h, item)) || h.finances(t) != funds || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != storage || h.evidenceStorage(t) != evidence {
			t.Fatal("failed authority clock wrote progress, state or financial facts")
		}
		queryRecoveryCheck(t, s.ProcessPersistedEvidenceOnce(context.Background()))
		queryRecoveryAssertTaskMoney(t, h, item, true)
		if h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
			t.Fatal("clock failure recovery used provider credentials or HTTP")
		}
	})
}
