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

// PurgeExpired cryptographically erases expired payloads in one transaction per
// row while retaining metadata and access audits. Blob deletion follows a grace
// period longer than an in-flight upload lease; failed deletes remain retryable.
// The count measures newly expired payloads, not tombstone cleanup attempts.
func (s *VideoStore) PurgeExpired(ctx context.Context, batch int) (int64, error) {
	if batch < 1 || batch > 1000 {
		return 0, ErrVideoInvalid
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	var expired []VideoPayload
	if err := s.db.WithContext(ctx).Where("expires_at <= ? AND capture_status <> ?", now, VideoCaptureExpired).Order("expires_at, payload_id").Limit(batch).Find(&expired).Error; err != nil {
		return 0, ErrVideoStorage
	}
	var count int64
	for _, meta := range expired {
		var changed int64
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			cleanup := now.Add(videoUploadLease + videoBlobTimeout)
			updated := tx.Model(&VideoPayload{}).Where("payload_id = ? AND expires_at <= ? AND capture_status <> ?", meta.PayloadID, now, VideoCaptureExpired).
				Updates(map[string]any{"capture_status": VideoCaptureExpired, "wrapped_key": "", "nonce": []byte{}, "cleanup_after": cleanup})
			if updated.Error != nil {
				return ErrVideoStorage
			}
			if updated.RowsAffected == 0 {
				return nil
			}
			if err := tx.Where("payload_id = ?", meta.PayloadID).Delete(&VideoPayloadStaging{}).Error; err != nil {
				return ErrVideoStorage
			}
			changed = updated.RowsAffected
			return nil
		})
		if err != nil {
			return count, err
		}
		count += changed
	}
	var cleanup []VideoPayload
	if err := s.db.WithContext(ctx).Where("capture_status = ? AND object_deleted_at IS NULL AND cleanup_after <= ?", VideoCaptureExpired, now).
		Order("cleanup_after, payload_id").Limit(batch).Find(&cleanup).Error; err != nil {
		return count, ErrVideoStorage
	}
	for _, meta := range cleanup {
		if meta.ObjectRef != "" {
			if s.blobs == nil {
				continue
			}
			deleteCtx, cancel := context.WithTimeout(ctx, videoBlobTimeout)
			err := s.blobs.Delete(deleteCtx, meta.ObjectRef)
			cancel()
			if err != nil {
				return count, ErrVideoStorage
			}
		}
		if err := s.db.WithContext(ctx).Model(&VideoPayload{}).Where("payload_id = ? AND capture_status = ? AND object_deleted_at IS NULL", meta.PayloadID, VideoCaptureExpired).
			Update("object_deleted_at", now).Error; err != nil {
			return count, ErrVideoStorage
		}
	}
	return count, nil
}
