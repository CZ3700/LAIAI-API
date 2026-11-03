// Package watermark owns the deferred watermark-removal delivery pipeline:
// a per-task job registered inside the trusted-succeeded transaction, a
// lease-based worker loop, and the superadmin toggle. The user-facing video
// address stays a single field; when a ready processed artifact exists the
// video service swaps the delivery URL for the MediaStore copy.
package watermark

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Job states. A job is admitted as pending, held under a lease while the
// worker downloads+encodes, and ends ready or failed. Leases expire so a
// crashed worker's job is retried by any replica.
const (
	StatePending    = "pending"
	StateProcessing = "processing"
	StateReady      = "ready"
	StateFailed     = "failed"
)

const (
	// MaxAttempts bounds full end-to-end attempts; a failed attempt is
	// re-queued with linear backoff until the budget is spent.
	MaxAttempts = 3
	// DefaultLease bounds one attempt; the worker renews nothing — the
	// ffmpeg timeout must stay below it.
	DefaultLease = 15 * time.Minute
	// DefaultBackoff is the re-queue delay after attempt N (N×Backoff).
	DefaultBackoff = 5 * time.Minute
)

var ErrNotFound = errors.New("watermark job not found")
var ErrLeaseLost = errors.New("watermark job lease lost")

type Job struct {
	ID                 int64   `gorm:"primaryKey"`
	OrgID              uint64  `gorm:"column:org_id"`
	UserID             *uint64 `gorm:"column:user_id"`
	TaskID             string  `gorm:"column:task_id"`
	State              string
	Attempts           int
	LeaseUntil         *time.Time `gorm:"column:lease_until"`
	ProcessedMediaID   string     `gorm:"column:processed_media_id"`
	ProcessedExpiresAt *time.Time `gorm:"column:processed_expires_at"`
	LastError          *string    `gorm:"column:last_error"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (Job) TableName() string { return "video_watermark_jobs" }

type Settings struct {
	ID        int64 `gorm:"primaryKey"`
	Enabled   bool
	UpdatedBy uint64
	UpdatedAt time.Time
}

func (Settings) TableName() string { return "video_watermark_settings" }

type UserGrant struct {
	UserID    uint64 `gorm:"primaryKey;column:user_id"`
	GrantedBy uint64
	GrantedAt time.Time
}

func (UserGrant) TableName() string { return "video_watermark_users" }

func UserAllowed(db *gorm.DB, userID uint64) (bool, error) {
	if userID == 0 {
		return false, nil
	}
	var count int64
	err := db.Model(&UserGrant{}).Where("user_id = ?", userID).Count(&count).Error
	return count == 1, err
}

// EnabledTx reads the singleton toggle inside the caller's transaction so the
// registration decision matches the admission transaction's snapshot.
func EnabledTx(tx *gorm.DB) (bool, error) {
	var row Settings
	if err := tx.Where("id = 1").Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.Enabled, nil
}

// RegisterTx inserts the job inside the trusted-succeeded transaction. It is
// idempotent per task (the unique key wins on replay) and a no-op while the
// toggle is off — the feature must stay fully bypassable.
func RegisterTx(tx *gorm.DB, org uint64, userID *uint64, taskID string, now time.Time) error {
	if org == 0 || userID == nil || *userID == 0 || taskID == "" {
		return nil
	}
	enabled, err := EnabledTx(tx)
	if err != nil || !enabled {
		return err
	}
	allowed, err := UserAllowed(tx, *userID)
	if err != nil || !allowed {
		return err
	}
	row := Job{OrgID: org, UserID: userID, TaskID: taskID, State: StatePending, CreatedAt: now, UpdatedAt: now}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "task_id"}}, DoNothing: true}).Create(&row).Error
}

// Lease claims up to limit due jobs atomically: the guarded UPDATE decides
// ownership via rows-affected, so concurrent replicas never double-process.
func Lease(db *gorm.DB, limit int, ttl time.Duration, now time.Time) ([]Job, error) {
	if limit <= 0 {
		return nil, nil
	}
	var due []Job
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Job{}).Where("state = ? AND attempts >= ? AND lease_until <= ?", StateProcessing, MaxAttempts, now).
			Updates(map[string]any{"state": StateFailed, "lease_until": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Where("attempts < ? AND EXISTS (SELECT 1 FROM video_watermark_users wu WHERE wu.user_id = video_watermark_jobs.user_id) AND ((state = ? AND (lease_until IS NULL OR lease_until <= ?)) OR (state = ? AND lease_until IS NOT NULL AND lease_until <= ?))", MaxAttempts, StatePending, now, StateProcessing, now).
			Order("created_at, id").Limit(limit).Find(&due).Error; err != nil {
			return err
		}
		until := now.Add(ttl)
		for i := range due {
			claim := tx.Model(&Job{}).Where("id = ? AND attempts = ? AND EXISTS (SELECT 1 FROM video_watermark_users wu WHERE wu.user_id = video_watermark_jobs.user_id) AND ((state = ? AND (lease_until IS NULL OR lease_until <= ?)) OR (state = ? AND lease_until IS NOT NULL AND lease_until <= ?))", due[i].ID, due[i].Attempts, StatePending, now, StateProcessing, now).
				Updates(map[string]any{"state": StateProcessing, "attempts": due[i].Attempts + 1, "lease_until": until, "updated_at": now})
			if claim.Error != nil {
				return claim.Error
			}
			if claim.RowsAffected == 1 {
				due[i].State = StateProcessing
				due[i].Attempts++
				due[i].LeaseUntil = &until
			} else {
				due[i].ID = 0 // lost the race; the caller skips zero IDs
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	kept := due[:0]
	for _, job := range due {
		if job.ID != 0 {
			kept = append(kept, job)
		}
	}
	return kept, nil
}

// Complete records the processed MediaStore object and its retention deadline.
func Complete(db *gorm.DB, job Job, mediaID string, expires, now time.Time) error {
	write := db.Model(&Job{}).Where("id = ? AND state = ? AND attempts = ?", job.ID, StateProcessing, job.Attempts).
		Updates(map[string]any{"state": StateReady, "processed_media_id": mediaID, "processed_expires_at": expires, "lease_until": nil, "last_error": nil, "updated_at": now})
	if write.Error != nil {
		return write.Error
	}
	if write.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Fail re-queues with linear backoff while attempts remain, else terminates
// the job. The source URL delivery is unaffected either way.
func Fail(db *gorm.DB, job Job, reason string, now time.Time, backoff time.Duration) error {
	updates := map[string]any{"lease_until": nil, "last_error": truncate(reason, 512), "updated_at": now}
	if job.Attempts >= MaxAttempts {
		updates["state"] = StateFailed
	} else {
		updates["state"] = StatePending
		updates["lease_until"] = now.Add(time.Duration(job.Attempts) * backoff)
	}
	write := db.Model(&Job{}).Where("id = ? AND state = ? AND attempts = ?", job.ID, StateProcessing, job.Attempts).Updates(updates)
	if write.Error != nil {
		return write.Error
	}
	if write.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

// RetainedMedia lets the shared media cleaner preserve a processed object until
// its job's delivery deadline. Orphaned and expired objects remain collectable.
func RetainedMedia(db *gorm.DB, mediaID string, now time.Time) (bool, error) {
	var count int64
	err := db.Model(&Job{}).Where("processed_media_id = ? AND state = ? AND processed_expires_at > ?", mediaID, StateReady, now).Count(&count).Error
	return count > 0, err
}

// Ready returns the processed artifact for a delivered task, or nil. Expired
// artifacts stop being served; MediaStore cleanup owns the file itself.
func Ready(db *gorm.DB, org uint64, taskID string, now time.Time) (*Job, error) {
	var row Job
	err := db.Where("org_id = ? AND task_id = ? AND state = ? AND processed_media_id <> '' AND (processed_expires_at IS NULL OR processed_expires_at > ?)", org, taskID, StateReady, now).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
