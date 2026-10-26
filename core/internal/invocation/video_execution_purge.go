package invocation

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const videoPurgeDatabaseTimeout = time.Second

type videoPurgeCandidate struct {
	PayloadID string
	OrgID     uint64
}

// MySQL's VARCHAR equality can ignore trailing spaces. Execution authority is
// selected by exact bytes; all other purposes keep the historical retention
// policy, including unknown legacy values.
func videoPurgePurpose(q *gorm.DB, execution bool) *gorm.DB {
	if execution {
		return q.Where("BINARY purpose = BINARY ?", VideoPurposeExecution)
	}
	return q.Where("BINARY purpose <> BINARY ?", VideoPurposeExecution)
}

func videoPurgeSamePurpose(meta VideoPayload, execution bool) bool {
	return (meta.Purpose == VideoPurposeExecution) == execution
}

func (s *VideoStore) videoPurgeTransaction(ctx context.Context, execution bool, work func(*gorm.DB) error) error {
	dbCtx, cancel := context.WithTimeout(ctx, videoPurgeDatabaseTimeout)
	defer cancel()
	err := s.db.Session(&gorm.Session{NewDB: true, Context: dbCtx, Logger: logger.Discard}).Transaction(work)
	// A confirmed Commit is a completed erasure, even if cancellation arrives
	// immediately afterwards. Preserve its count; a later phase observes ctx.
	if err != nil && dbCtx.Err() != nil {
		err = dbCtx.Err()
	}
	if err == nil {
		return nil
	}
	if execution {
		return videoExecutionAuthorityFailure(err)
	}
	return videoStorageFailure(err)
}

func (s *VideoStore) videoPurgeTimeTx(tx *gorm.DB, execution bool) (time.Time, error) {
	if execution {
		return videoExecutionAuthorityTimeTx(tx)
	}
	return s.now().UTC().Truncate(time.Millisecond), nil
}

func (s *VideoStore) videoPurgeCandidates(ctx context.Context, batch int, execution, cleanup bool) ([]videoPurgeCandidate, error) {
	var rows []videoPurgeCandidate
	err := s.videoPurgeTransaction(ctx, execution, func(tx *gorm.DB) error {
		now, err := s.videoPurgeTimeTx(tx, execution)
		if err != nil {
			return err
		}
		partition := 0
		if execution {
			partition = 1
		}
		// 0132 derives both flags from the original columns, including their
		// exact purpose / PAD SPACE status semantics. Keep the locking read's
		// original-field checks: this indexed candidate is not permission.
		q := tx.Model(&VideoPayload{}).Select("payload_id", "org_id").Where("purge_is_execution = ?", partition)
		if cleanup {
			q = q.Where("capture_status = ? AND object_deleted_at IS NULL AND cleanup_after <= ?", VideoCaptureExpired, now).
				Order("cleanup_after, payload_id")
		} else {
			q = q.Where("purge_needs_erasure = ? AND expires_at <= ?", 1, now).Order("expires_at, payload_id")
		}
		return q.Limit(batch).Find(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func videoPurgeLockTx(tx *gorm.DB, candidate videoPurgeCandidate, execution bool) (VideoPayload, bool, error) {
	var meta VideoPayload
	err := videoPurgePurpose(tx.Where("org_id = ? AND payload_id = ?", candidate.OrgID, candidate.PayloadID), execution).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&meta).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return VideoPayload{}, false, nil
	}
	if err != nil {
		return VideoPayload{}, false, err
	}
	if !videoPurgeSamePurpose(meta, execution) {
		return VideoPayload{}, false, ErrVideoCorrupt
	}
	return meta, true, nil
}

func (s *VideoStore) purgeVideoPartition(ctx context.Context, batch int, execution bool) (int64, error) {
	candidates, err := s.videoPurgeCandidates(ctx, batch, execution, false)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, candidate := range candidates {
		var changed int64
		err := s.videoPurgeTransaction(ctx, execution, func(tx *gorm.DB) error {
			meta, found, err := videoPurgeLockTx(tx, candidate, execution)
			if err != nil || !found {
				return err
			}
			now, err := s.videoPurgeTimeTx(tx, execution)
			if err != nil {
				return err
			}
			if meta.CaptureStatus == VideoCaptureExpired || meta.ExpiresAt.After(now) {
				return nil
			}
			updated := tx.Model(&VideoPayload{}).Where("org_id = ? AND payload_id = ? AND capture_status <> ?", meta.OrgID, meta.PayloadID, VideoCaptureExpired).
				Updates(map[string]any{"capture_status": VideoCaptureExpired, "wrapped_key": "", "nonce": []byte{}, "cleanup_after": now.Add(videoUploadLease + videoBlobTimeout)})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrVideoConflict
			}
			if err := tx.Where("payload_id = ?", meta.PayloadID).Delete(&VideoPayloadStaging{}).Error; err != nil {
				return err
			}
			changed = 1
			return nil
		})
		if err != nil {
			return count, err
		}
		count += changed
	}
	return count, nil
}

func videoCleanupDue(meta VideoPayload, now time.Time) bool {
	return meta.CaptureStatus == VideoCaptureExpired && meta.ObjectDeletedAt == nil &&
		meta.CleanupAfter != nil && !meta.CleanupAfter.After(now)
}

func (s *VideoStore) cleanupVideoPartition(ctx context.Context, batch int, execution bool) error {
	candidates, err := s.videoPurgeCandidates(ctx, batch, execution, true)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		var ready *VideoPayload
		if err := s.videoPurgeTransaction(ctx, execution, func(tx *gorm.DB) error {
			meta, found, err := videoPurgeLockTx(tx, candidate, execution)
			if err != nil || !found {
				return err
			}
			now, err := s.videoPurgeTimeTx(tx, execution)
			if err != nil {
				return err
			}
			if videoCleanupDue(meta, now) {
				ready = &meta
			}
			return nil
		}); err != nil {
			return err
		}
		if ready == nil {
			continue
		}
		// No metadata lock survives this external operation. The optional
		// uploader remains disabled: its late-Put orphan gap is a separate
		// protocol issue, not solved by this current-row clock check.
		if ready.ObjectRef != "" {
			if s.blobs == nil {
				continue
			}
			deleteCtx, cancel := context.WithTimeout(ctx, videoBlobTimeout)
			err := s.blobs.Delete(deleteCtx, ready.ObjectRef)
			cancel()
			if err != nil {
				return videoStorageFailure(err)
			}
		}
		if err := s.videoPurgeTransaction(ctx, execution, func(tx *gorm.DB) error {
			meta, found, err := videoPurgeLockTx(tx, candidate, execution)
			if err != nil || !found {
				return err
			}
			now, err := s.videoPurgeTimeTx(tx, execution)
			if err != nil {
				return err
			}
			if !videoCleanupDue(meta, now) || meta.ObjectRef != ready.ObjectRef || !meta.CleanupAfter.Equal(*ready.CleanupAfter) {
				return nil
			}
			updated := tx.Model(&VideoPayload{}).Where("org_id = ? AND payload_id = ? AND capture_status = ? AND object_deleted_at IS NULL", meta.OrgID, meta.PayloadID, VideoCaptureExpired).
				Update("object_deleted_at", now)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrVideoConflict
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
