package video

import (
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// User task limits apply to one authenticated user across all of that user's
// API keys. Zero in a configured dimension means no local cap. The provider's
// account/model gates remain independent and may be stricter.
type UserTaskLimit struct {
	OrgID            uint64    `gorm:"primaryKey;column:org_id" json:"org_id"`
	UserID           uint64    `gorm:"primaryKey;column:user_id" json:"user_id"`
	CreateTaskRPM    int       `gorm:"column:create_task_rpm" json:"create_task_rpm"`
	MaxInflightTasks int       `gorm:"column:max_inflight_tasks" json:"max_inflight_tasks"`
	DailyTaskLimit   int64     `gorm:"column:daily_task_limit" json:"daily_task_limit"`
	MonthlyTaskLimit int64     `gorm:"column:monthly_task_limit" json:"monthly_task_limit"`
	Timezone         string    `gorm:"column:timezone" json:"timezone"`
	Version          uint64    `gorm:"column:version" json:"version"`
	UpdatedBy        uint64    `gorm:"column:updated_by" json:"updated_by"`
	UpdatedAt        time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (UserTaskLimit) TableName() string { return "video_user_task_limits" }

type UserSendGate struct {
	OrgID         uint64    `gorm:"primaryKey;column:org_id"`
	UserID        uint64    `gorm:"primaryKey;column:user_id"`
	NextAllowedAt time.Time `gorm:"column:next_allowed_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

func (UserSendGate) TableName() string { return "video_user_send_gates" }

var (
	ErrUserTaskRPM         = errors.New("video: user create task rpm reached")
	ErrUserTaskConcurrency = errors.New("video: user inflight task limit reached")
	ErrUserDailyTasks      = errors.New("video: user daily task limit reached")
	ErrUserMonthlyTasks    = errors.New("video: user monthly task limit reached")
)

func userLimitNowTx(tx *gorm.DB, supplied time.Time) (time.Time, error) {
	if tx == nil || tx.Dialector == nil {
		return time.Time{}, ErrUnavailable
	}
	if tx.Dialector.Name() == "mysql" {
		return authorityTimeTx(tx)
	}
	if supplied.IsZero() {
		return time.Time{}, ErrInvalid
	}
	return supplied.UTC(), nil
}

func readUserTaskLimitTx(tx *gorm.DB, orgID, userID uint64) (UserTaskLimit, bool, error) {
	var limit UserTaskLimit
	err := locked(tx).Where("org_id = ? AND user_id = ?", orgID, userID).Take(&limit).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return limit, false, nil
	}
	if err != nil {
		return limit, false, err
	}
	if limit.OrgID != orgID || limit.UserID != userID || limit.Version == 0 || limit.CreateTaskRPM < 0 || limit.MaxInflightTasks < 0 || limit.DailyTaskLimit < 0 || limit.MonthlyTaskLimit < 0 {
		return limit, false, ErrUnavailable
	}
	return limit, true, nil
}

// checkUserAdmissionTx runs after an idempotency key was claimed and confirmed
// new, but before Task INSERT, hold, quota reserve or outbox. The locked limit
// row serializes same-user admissions across API keys. Locking task counts are
// current reads under MySQL repeatable-read; ordinary snapshot counts can miss
// a just-committed task after the idempotency pre-read.
//
// Both Draft and draft conversion are separate accepted create tasks. Failed
// tasks continue to count toward RPM and daily/monthly accepted task limits.
func (s *Service) checkUserAdmissionTx(tx *gorm.DB, orgID, userID uint64, at time.Time) error {
	if tx == nil || orgID == 0 {
		return ErrUnavailable
	}
	if userID == 0 { // organization-scoped keys have no user allocation
		return nil
	}
	limit, found, err := readUserTaskLimitTx(tx, orgID, userID)
	if err != nil || !found {
		return err
	}
	now, err := userLimitNowTx(tx, at)
	if err != nil {
		return err
	}
	base := func() *gorm.DB {
		return locked(tx).Model(&task.Task{}).Where("org_id = ? AND user_id = ? AND provider = ?", orgID, userID, Provider)
	}
	var count int64
	if limit.CreateTaskRPM > 0 {
		if err := base().Where("submitted_at > ? AND submitted_at <= ?", now.Add(-time.Minute), now).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(limit.CreateTaskRPM) {
			return ErrUserTaskRPM
		}
	}
	if limit.MaxInflightTasks > 0 {
		count = 0
		if err := base().Where("status IN ?", []string{"accepted", "submitting", "submission_unknown", "queued", "running"}).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(limit.MaxInflightTasks) {
			return ErrUserTaskConcurrency
		}
	}
	if limit.DailyTaskLimit == 0 && limit.MonthlyTaskLimit == 0 {
		return nil
	}
	loc, err := time.LoadLocation(limit.Timezone)
	if err != nil {
		return ErrUnavailable
	}
	local := now.In(loc)
	if limit.DailyTaskLimit > 0 {
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		count = 0
		if err := base().Where("submitted_at >= ? AND submitted_at < ?", start.UTC(), start.AddDate(0, 0, 1).UTC()).Count(&count).Error; err != nil {
			return err
		}
		if count >= limit.DailyTaskLimit {
			return ErrUserDailyTasks
		}
	}
	if limit.MonthlyTaskLimit > 0 {
		start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		count = 0
		if err := base().Where("submitted_at >= ? AND submitted_at < ?", start.UTC(), start.AddDate(0, 1, 0).UTC()).Count(&count).Error; err != nil {
			return err
		}
		if count >= limit.MonthlyTaskLimit {
			return ErrUserMonthlyTasks
		}
	}
	return nil
}

// claimUserSendTx paces outbound create attempts by the user's configured RPM.
// Call it in the existing durable create claim transaction, after verifying the
// same user/task and before any HTTP send. A denied claim is retryable: leave
// the accepted task and its hold untouched, and reschedule for retryAt.
// A granted slot remains spent even if the process crashes before POST; this
// conservative rule never turns an unknown send into a second paid POST.
func (s *Service) claimUserSendTx(tx *gorm.DB, orgID, userID uint64, at time.Time) (allowed bool, retryAt time.Time, err error) {
	if tx == nil || orgID == 0 {
		return false, time.Time{}, ErrUnavailable
	}
	if userID == 0 {
		return true, time.Time{}, nil
	}
	limit, found, err := readUserTaskLimitTx(tx, orgID, userID)
	if err != nil || !found || limit.CreateTaskRPM == 0 {
		return err == nil, time.Time{}, err
	}
	now, err := userLimitNowTx(tx, at)
	if err != nil {
		return false, time.Time{}, err
	}
	if limit.CreateTaskRPM > 60000 {
		return false, time.Time{}, ErrUnavailable
	}
	intervalMS := (int64(time.Minute/time.Millisecond) + int64(limit.CreateTaskRPM) - 1) / int64(limit.CreateTaskRPM)
	if intervalMS < 1 {
		intervalMS = 1
	}
	// INSERT IGNORE creates a gate only once. The subsequent locking read uses
	// current time after any wait, just like the provider's scope/action gate.
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&UserSendGate{OrgID: orgID, UserID: userID, NextAllowedAt: now, UpdatedAt: now}).Error; err != nil {
		return false, time.Time{}, err
	}
	var gate UserSendGate
	if err := locked(tx).Where("org_id = ? AND user_id = ?", orgID, userID).Take(&gate).Error; err != nil {
		return false, time.Time{}, err
	}
	now, err = userLimitNowTx(tx, at)
	if err != nil {
		return false, time.Time{}, err
	}
	if gate.NextAllowedAt.After(now) {
		return false, gate.NextAllowedAt, nil
	}
	next := now.Add(time.Duration(intervalMS) * time.Millisecond)
	result := tx.Model(&UserSendGate{}).Where("org_id = ? AND user_id = ?", orgID, userID).Updates(map[string]any{"next_allowed_at": next, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return false, time.Time{}, ErrUnavailable
	}
	return true, next, nil
}
