package video

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const queryFinancialConflictCode = "query_financial_conflict"

var errQueryFinancialConflict = errors.New("video query financial evidence requires review")

// Do not filter the marker by format, phase, pin, due or evidence integrity.
// Corruption of an already committed marker must not erase the task barrier.
const noTaskSettlementConflict = `NOT EXISTS (
	SELECT 1 FROM video_query_receipts financial_conflict
	WHERE financial_conflict.org_id = v.org_id AND financial_conflict.task_id = v.task_id
	AND financial_conflict.apply_error_code = 'query_financial_conflict'
)`

// The caller holds Task -> State. A bounded current locking read is necessary:
// an older RR snapshot must not hide a conflict committed before the Task lock.
func requireNoTaskSettlementConflictTx(tx *gorm.DB, st State) error {
	if err := responseTransaction(tx); err != nil {
		return err
	}
	if st.OrgID == 0 || st.TaskID == "" || tx.Statement.Context == nil {
		return ErrInvalid
	}
	ctx := tx.Statement.Context
	if err := ctx.Err(); err != nil {
		return safeRecoveryErrors(err)
	}
	var marker struct{ AttemptID string }
	q := tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard})
	err := locked(q).Table("video_query_receipts").Select("attempt_id").
		Where("org_id = ? AND task_id = ? AND apply_error_code = ?", st.OrgID, st.TaskID, queryFinancialConflictCode).
		Order("attempt_id").Take(&marker).Error
	if cause := ctx.Err(); cause != nil {
		return safeRecoveryErrors(err, cause)
	}
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		return safeRecoveryErrors(err)
	}
	return errQueryFinancialConflict
}
