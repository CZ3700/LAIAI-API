package invocation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const videoUploadLease = 2 * time.Minute
const videoBlobTimeout = 30 * time.Second

// UploadPending promotes encrypted SQL staging to the optional private blob
// store. SQL-only operation is supported: no blob backend is required to read
// staged evidence. Each claim uses a lease and monotonically increasing fence.
func (s *VideoStore) UploadPending(ctx context.Context, workerID string, batch int) (int64, error) {
	if s.blobs == nil || !videoValidID(workerID, false) || batch < 1 || batch > 1000 {
		return 0, ErrVideoInvalid
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var candidates []VideoPayloadStaging
	if err := s.db.WithContext(ctx).Where("next_attempt_at <= ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)", now, now).
		Order("next_attempt_at, payload_id").Limit(batch).Find(&candidates).Error; err != nil {
		return 0, ErrVideoStorage
	}
	var uploaded int64
	for _, candidate := range candidates {
		var meta VideoPayload
		fence := candidate.FencingToken + 1
		lease := s.now().UTC().Add(videoUploadLease).Truncate(time.Millisecond)
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			claimed := tx.Model(&VideoPayloadStaging{}).Where("payload_id = ? AND fencing_token = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)", candidate.PayloadID, candidate.FencingToken, s.now()).
				Updates(map[string]any{"lease_owner": workerID, "lease_expires_at": lease, "fencing_token": fence, "attempts": gorm.Expr("attempts + 1")})
			if claimed.Error != nil {
				return ErrVideoStorage
			}
			if claimed.RowsAffected != 1 {
				return ErrVideoConflict
			}
			if err := tx.Where("payload_id = ?", candidate.PayloadID).Take(&meta).Error; err != nil {
				return ErrVideoStorage
			}
			if meta.CaptureStatus != VideoCaptureStaged || !meta.ExpiresAt.After(s.now()) {
				return ErrVideoExpired
			}
			objectRef := fmt.Sprintf("invocation/v1/%d/%s", meta.OrgID, meta.PayloadID)
			if meta.ObjectRef != "" && meta.ObjectRef != objectRef {
				return ErrVideoCorrupt
			}
			meta.ObjectRef = objectRef
			if err := tx.Model(&VideoPayload{}).Where("payload_id = ? AND capture_status = ?", meta.PayloadID, VideoCaptureStaged).Update("object_ref", objectRef).Error; err != nil {
				return ErrVideoStorage
			}
			return nil
		})
		if errors.Is(err, ErrVideoConflict) || errors.Is(err, ErrVideoExpired) {
			continue
		}
		if err != nil {
			return uploaded, err
		}
		if videoHash(candidate.Ciphertext) != meta.CiphertextSHA256 {
			return uploaded, ErrVideoCorrupt
		}
		putCtx, cancel := context.WithTimeout(ctx, videoBlobTimeout)
		err = s.blobs.Put(putCtx, meta.ObjectRef, candidate.Ciphertext)
		cancel()
		if err != nil {
			// A failed/ambiguous Put may already exist remotely. Keep the same key
			// and bytes, preserve staging, and let a later lease holder repeat it.
			_ = s.db.WithContext(ctx).Model(&VideoPayloadStaging{}).Where("payload_id = ? AND fencing_token = ? AND lease_owner = ?", meta.PayloadID, fence, workerID).
				Updates(map[string]any{"lease_owner": "", "lease_expires_at": nil, "next_attempt_at": s.now().Add(time.Minute)}).Error
			return uploaded, ErrVideoStorage
		}
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			removed := tx.Where("payload_id = ? AND fencing_token = ? AND lease_owner = ? AND lease_expires_at > ?", meta.PayloadID, fence, workerID, s.now()).Delete(&VideoPayloadStaging{})
			if removed.Error != nil {
				return ErrVideoStorage
			}
			if removed.RowsAffected != 1 {
				return ErrVideoConflict
			}
			updated := tx.Model(&VideoPayload{}).Where("payload_id = ? AND capture_status = ? AND expires_at > ?", meta.PayloadID, VideoCaptureStaged, s.now()).Update("capture_status", VideoCaptureStored)
			if updated.Error != nil {
				return ErrVideoStorage
			}
			if updated.RowsAffected != 1 {
				return ErrVideoExpired
			}
			return nil
		})
		// A newer fence may be uploading the same object. Never delete it here;
		// the TTL tombstone cleaner owns deletion after all leases have expired.
		if errors.Is(err, ErrVideoConflict) || errors.Is(err, ErrVideoExpired) {
			continue
		}
		if err != nil {
			return uploaded, err
		}
		uploaded++
	}
	return uploaded, nil
}

// PurgeExpired erases at most batch execution and batch non-execution
// payloads, retaining metadata and access audits. Each execution row is checked
// against a fresh primary database time after its metadata lock is acquired.
// The returned count excludes tombstone cleanup and already expired rows.
func (s *VideoStore) PurgeExpired(ctx context.Context, batch int) (int64, error) {
	if ctx == nil || batch < 1 || batch > 1000 {
		return 0, ErrVideoInvalid
	}
	var count int64
	for _, execution := range []bool{true, false} {
		n, err := s.purgeVideoPartition(ctx, batch, execution)
		count += n
		if err != nil {
			return count, err
		}
	}
	for _, execution := range []bool{true, false} {
		if err := s.cleanupVideoPartition(ctx, batch, execution); err != nil {
			return count, err
		}
	}
	return count, nil
}
