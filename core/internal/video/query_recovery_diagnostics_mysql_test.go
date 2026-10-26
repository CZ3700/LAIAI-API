package video

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// These races hold a real losing transaction connection before its first Task
// lock. The winning connection can therefore commit without reversing the
// production Task -> State -> receipt lock order or waiting on the loser.
func TestMySQLQueryRecoveryDiagnosticsUseCurrentEvidence(t *testing.T) {
	t.Run("successful_application_wins_before_late_diagnostic", func(t *testing.T) {
		h := queryRecoveryNew(t)
		item := h.pending(t, 0, 0)
		h.arrange(t, &item, false, 1)
		loser, winner := h.service(t), h.service(t)
		h.forbidNetwork(t, loser)
		evidence := h.evidenceStorage(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		type winnerKey struct{}
		var failed, entered atomic.Bool
		var applicationCause, winnerErr error
		var winnerResult queryRecoveryResult
		var loserID, winnerID uint64
		var winnerRow QueryReceipt
		var winnerMoney, winnerStorage [32]byte
		eventHook := "test:query-late-diagnostic-event"
		lockHook := "test:query-late-diagnostic-before-task"
		queryRecoveryCheck(t, h.db.Callback().Create().Before("gorm:create").Register(eventHook, func(tx *gorm.DB) {
			event, ok := tx.Statement.Dest.(*Event)
			if ok && event.OrgID == item.row.OrgID && event.TaskID == item.row.TaskID && event.Type == "task.status_changed" && failed.CompareAndSwap(false, true) {
				applicationCause = tx.Exec("INSERT INTO video_events (query_late_diagnostic_missing_column) VALUES (1)").Error
				tx.AddError(applicationCause)
			}
		}))
		defer h.db.Callback().Create().Remove(eventHook)
		queryRecoveryCheck(t, h.db.Callback().Query().Before("gorm:query").Register(lockHook, func(tx *gorm.DB) {
			_, lockedRead := tx.Statement.Clauses["FOR"]
			if tx.Statement.Table != "tasks" || !lockedRead {
				return
			}
			if tx.Statement.Context.Value(winnerKey{}) == true {
				queryRecoveryCheck(t, tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT CONNECTION_ID()").Scan(&winnerID).Error)
				return
			}
			if !failed.Load() || !entered.CompareAndSwap(false, true) {
				return
			}
			queryRecoveryCheck(t, tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT CONNECTION_ID()").Scan(&loserID).Error)
			winnerResult, winnerErr = winner.applyQueryReceiptProgress(context.WithValue(ctx, winnerKey{}, true), item.row)
			winnerRow = queryTemporalRow(t, h, item)
			winnerMoney, winnerStorage = h.finances(t), h.taskStorage(t, item.row.OrgID, item.row.TaskID)
		}))
		defer h.db.Callback().Query().Remove(lockHook)
		result, err := loser.applyQueryReceiptProgress(ctx, item.row)
		queryRecoveryCheck(t, h.db.Callback().Create().Remove(eventHook))
		queryRecoveryCheck(t, h.db.Callback().Query().Remove(lockHook))
		var driverErr *drivermysql.MySQLError
		if !failed.Load() || !entered.Load() || loserID == 0 || winnerID == 0 || loserID == winnerID || !errors.As(applicationCause, &driverErr) || driverErr.Number != 1054 || !errors.Is(err, applicationCause) {
			t.Fatal("late-diagnostic race did not prove actual SQL rollback and independent connections")
		}
		if winnerErr != nil || winnerResult.disposition != queryRecoveryApplied || winnerRow.AppliedAt == nil || winnerRow.RecoveryVersion != item.row.RecoveryVersion+1 || result.disposition != queryRecoveryNoop {
			t.Fatal("late diagnostic did not yield to a committed successful application")
		}
		if !reflect.DeepEqual(winnerRow, queryTemporalRow(t, h, item)) || h.finances(t) != winnerMoney || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != winnerStorage || h.evidenceStorage(t) != evidence || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
			t.Fatal("late failure rewrote the winner's state, progress, funds or original evidence")
		}
		queryRecoveryAssertTaskMoney(t, h, item, true)
		queryRecoveryCheck(t, winner.ProcessPersistedEvidenceOnce(ctx))
		if h.finances(t) != winnerMoney || !reflect.DeepEqual(winnerRow, queryTemporalRow(t, h, item)) {
			t.Fatal("successful race replay repeated a financial or progress mutation")
		}
	})

	t.Run("changed_receipt_cannot_inherit_old_decode_failure", func(t *testing.T) {
		h := queryRecoveryNew(t)
		item := h.pending(t, 0, 0)
		originalJSON := item.row.ObservationJSON
		h.arrange(t, &item, true, 1)
		s := h.service(t)
		h.forbidNetwork(t, s)
		money, evidence := h.finances(t), h.evidenceStorage(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		type writerKey struct{}
		var taskReads atomic.Int32
		var loserID, writerID uint64
		var injected QueryReceipt
		var injectedStorage [32]byte
		hook := "test:query-diagnostic-evidence-changed"
		queryRecoveryCheck(t, h.db.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
			_, lockedRead := tx.Statement.Clauses["FOR"]
			if tx.Statement.Table != "tasks" || !lockedRead || tx.Statement.Context.Value(writerKey{}) == true || taskReads.Add(1) != 2 {
				return
			}
			queryRecoveryCheck(t, tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT CONNECTION_ID()").Scan(&loserID).Error)
			queryRecoveryCheck(t, h.db.WithContext(context.WithValue(ctx, writerKey{}, true)).Transaction(func(other *gorm.DB) error {
				if err := other.Raw("SELECT CONNECTION_ID()").Scan(&writerID).Error; err != nil {
					return err
				}
				if _, _, err := LockBillingTaskTx(other, item.row.OrgID, item.row.TaskID); err != nil {
					return err
				}
				var before QueryReceipt
				if err := locked(other).Where("org_id = ? AND task_id = ? AND attempt_id = ?", item.row.OrgID, item.row.TaskID, item.row.AttemptID).Take(&before).Error; err != nil {
					return err
				}
				// Deliberate storage damage/replacement fixture. This is not an
				// editing API: only this column may change, without version help.
				write := other.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL", before.OrgID, before.TaskID, before.AttemptID, before.RecoveryVersion).UpdateColumn("observation_json", originalJSON)
				if write.Error != nil {
					return write.Error
				}
				if write.RowsAffected != 1 {
					return errors.New("evidence fixture did not alter its exact single receipt")
				}
				if err := other.Where("org_id = ? AND attempt_id = ?", before.OrgID, before.AttemptID).Take(&injected).Error; err != nil {
					return err
				}
				copy := injected
				copy.ObservationJSON = before.ObservationJSON
				if injected.ObservationJSON != originalJSON || !reflect.DeepEqual(copy, before) {
					return errors.New("evidence fixture changed progress or unrelated evidence")
				}
				return nil
			}))
			injectedStorage = h.taskStorage(t, item.row.OrgID, item.row.TaskID)
		}))
		defer h.db.Callback().Query().Remove(hook)
		result, err := s.applyQueryReceiptProgress(ctx, item.row)
		queryRecoveryCheck(t, h.db.Callback().Query().Remove(hook))
		if taskReads.Load() != 2 || loserID == 0 || writerID == 0 || loserID == writerID || !errors.Is(err, errQueryObservationInvalid) || !errors.Is(err, ErrConflict) || result.disposition != queryRecoveryFailed {
			t.Fatal("diagnostic did not reject an old decoder cause after actual locked evidence replacement")
		}
		if !reflect.DeepEqual(injected, queryTemporalRow(t, h, item)) || injected.RecoveryDueAt == nil || injected.AppliedAt != nil || h.taskStorage(t, item.row.OrgID, item.row.TaskID) != injectedStorage || h.finances(t) != money || h.evidenceStorage(t) != evidence || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
			t.Fatal("old diagnosis isolated changed evidence or mutated original task and financial facts")
		}
	})
}
