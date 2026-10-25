package video

import (
	"errors"

	"gorm.io/gorm"
)

var errCreateInboxReview = errors.New("create response identity requires review")

// The fixed alias v is used by both video-owned query candidate builders.
// Exclude quarantined identities before LIMIT; a later GET must not erase the
// review barrier or select one of two conflicting create results to settle.
const noCreateInboxReview = `NOT EXISTS (
	SELECT 1 FROM video_receipt_applications quarantine
	JOIN video_execution_attempts execution
	ON execution.org_id = quarantine.org_id AND execution.task_id = quarantine.task_id
	AND execution.attempt_id = quarantine.attempt_id
	WHERE quarantine.org_id = v.org_id AND quarantine.task_id = v.task_id
	AND quarantine.state = 'review' AND execution.action = 'create_task'
)`

// Call under the same Task -> State locks used by the inbox reducer. This
// serializes a newly discovered identity conflict with query application and
// preserves a query response already committed before the conflict was seen.
func requireNoCreateInboxReviewTx(tx *gorm.DB, org uint64, taskID string) error {
	if err := responseTransaction(tx); err != nil {
		return err
	}
	var count int64
	err := tx.Table("video_receipt_applications quarantine").
		Joins("JOIN video_execution_attempts execution ON execution.org_id = quarantine.org_id AND execution.task_id = quarantine.task_id AND execution.attempt_id = quarantine.attempt_id").
		Where("quarantine.org_id = ? AND quarantine.task_id = ? AND quarantine.state = ? AND execution.action = ?", org, taskID, "review", "create_task").Count(&count).Error
	if err != nil {
		return err
	}
	if count != 0 {
		return errCreateInboxReview
	}
	return nil
}
