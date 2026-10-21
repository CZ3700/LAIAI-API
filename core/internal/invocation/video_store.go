package invocation

import (
	"bytes"
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StageTx participates in the supplied business transaction. The savepoint
// also makes its own metadata+staging pair atomic if a caller handles the error.
// Duplicate delivery of the same PreparedVideoPayload is safe. A new prepared
// payload with an occupied correlation is a conflict, never an overwrite.
func (s *VideoStore) StageTx(tx *gorm.DB, prepared *PreparedVideoPayload) (string, error) {
	if tx == nil || prepared == nil || !videoValidID(prepared.metadata.PayloadID, false) {
		return "", ErrVideoInvalid
	}
	if !prepared.metadata.ExpiresAt.After(s.now()) {
		return "", ErrVideoExpired
	}
	err := tx.Transaction(func(db *gorm.DB) error {
		meta := prepared.metadata
		result := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "correlation_key"}}, DoNothing: true}).Create(&meta)
		if result.Error != nil {
			return ErrVideoStorage
		}
		if result.RowsAffected == 0 {
			var old VideoPayload
			if err := db.Where("correlation_key = ? AND org_id = ?", meta.CorrelationKey, meta.OrgID).Take(&old).Error; err != nil {
				return ErrVideoStorage
			}
			if old.CaptureStatus == VideoCaptureExpired {
				return ErrVideoExpired
			}
			if !videoSamePayload(old, meta) {
				return ErrVideoConflict
			}
			return nil
		}
		if meta.CaptureStatus == VideoCaptureDisabled {
			return nil
		}
		stage := prepared.staging
		if err := db.Create(&stage).Error; err != nil {
			return ErrVideoStorage
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return prepared.PayloadID(), nil
}

func videoSamePayload(a, b VideoPayload) bool {
	return a.PayloadID == b.PayloadID && a.OrgID == b.OrgID && a.RequestID == b.RequestID && a.AttemptID == b.AttemptID &&
		a.CorrelationKey == b.CorrelationKey && a.Direction == b.Direction && a.Purpose == b.Purpose && a.SchemaVersion == b.SchemaVersion &&
		a.EncryptionKeyID == b.EncryptionKeyID && a.WrappedKey == b.WrappedKey && bytes.Equal(a.Nonce, b.Nonce) &&
		a.CiphertextSHA256 == b.CiphertextSHA256 && a.PlaintextBytes == b.PlaintextBytes && a.RedactionVersion == b.RedactionVersion && a.BodyFormat == b.BodyFormat &&
		a.CaptureRepresentation == b.CaptureRepresentation && a.CreatedAt.Equal(b.CreatedAt) && a.ExpiresAt.Equal(b.ExpiresAt)
}

func (s *VideoStore) ReadForExecution(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error) {
	return s.read(ctx, orgID, payloadID, VideoPurposeExecution, false, access)
}

func (s *VideoStore) ReadForHistory(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error) {
	return s.read(ctx, orgID, payloadID, VideoPurposeHistory, false, access)
}

// ReadForDelivery exposes only the separately validated result-link capsule.
// It cannot read history or execution data, including when history is disabled.
func (s *VideoStore) ReadForDelivery(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error) {
	return s.read(ctx, orgID, payloadID, VideoPurposeDelivery, false, access)
}

// ReadForRecovery is limited to upstream responses. It does not grant workers
// historical client requests, responses, or execution payloads.
func (s *VideoStore) ReadForRecovery(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error) {
	return s.read(ctx, orgID, payloadID, VideoPurposeHistory, true, access)
}

func (s *VideoStore) accessEvent(ctx context.Context, accessID, phase string, orgID uint64, payloadID string, access VideoAccess, outcome, code string) error {
	row := VideoPayloadAccess{AccessID: accessID, Phase: phase, OrgID: orgID, PayloadID: payloadID,
		ActorRole: access.ActorRole, ActorID: access.ActorID, ReasonCode: access.ReasonCode, OperationID: access.OperationID,
		Outcome: outcome, SafeErrorCode: code, CreatedAt: s.now().UTC().Truncate(time.Millisecond)}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return ErrVideoAuditUnavailable
	}
	return nil
}

func (s *VideoStore) read(ctx context.Context, orgID uint64, payloadID, purpose string, recovery bool, access VideoAccess) ([]byte, error) {
	if orgID == 0 || !videoValidID(payloadID, false) || !videoValidID(access.ActorID, false) ||
		!videoValidID(access.OperationID, true) || !videoValidID(access.ReasonCode, false) ||
		(access.ActorRole != VideoRoleWorker && access.ActorRole != VideoRoleOrgReader && access.ActorRole != VideoRoleRoot) {
		return nil, ErrVideoInvalid
	}
	accessID, err := videoRandomID("vac_")
	if err != nil {
		return nil, err
	}
	// Persist an attempted read before any lookup or decryption. A failed
	// audit write always prevents plaintext from crossing the package boundary.
	if err := s.accessEvent(ctx, accessID, "started", orgID, payloadID, access, "started", ""); err != nil {
		return nil, err
	}
	deny := func(err error, code string) ([]byte, error) {
		if auditErr := s.accessEvent(ctx, accessID, "finished", orgID, payloadID, access, "denied", code); auditErr != nil {
			return nil, auditErr
		}
		return nil, err
	}
	roleOK := (purpose == VideoPurposeExecution || recovery) && access.ActorRole == VideoRoleWorker ||
		(purpose == VideoPurposeHistory || purpose == VideoPurposeDelivery) && !recovery && (access.ActorRole == VideoRoleOrgReader || access.ActorRole == VideoRoleRoot)
	if !roleOK {
		return deny(ErrVideoAccessDenied, "ROLE_DENIED")
	}
	var meta VideoPayload
	if err := s.db.WithContext(ctx).Where("org_id = ? AND payload_id = ?", orgID, payloadID).Take(&meta).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return deny(ErrVideoNotFound, "NOT_FOUND")
		}
		return deny(ErrVideoStorage, "STORAGE_UNAVAILABLE")
	}
	if meta.Purpose != purpose || purpose == VideoPurposeExecution && meta.Direction != VideoEffectiveRequest || (recovery || purpose == VideoPurposeDelivery) && meta.Direction != VideoUpstreamResponse {
		return deny(ErrVideoAccessDenied, "PURPOSE_DENIED")
	}
	if meta.CaptureStatus == VideoCaptureExpired || !meta.ExpiresAt.After(s.now()) {
		return deny(ErrVideoExpired, "EXPIRED")
	}
	if meta.CaptureStatus == VideoCaptureDisabled {
		return deny(ErrVideoRetentionDenied, "RETENTION_DISABLED")
	}
	var ciphertext []byte
	switch meta.CaptureStatus {
	case VideoCaptureStaged:
		var staged VideoPayloadStaging
		if err := s.db.WithContext(ctx).Where("payload_id = ?", payloadID).Take(&staged).Error; err != nil {
			// Upload may have committed between the metadata and staging reads.
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return deny(ErrVideoStorage, "STAGING_UNAVAILABLE")
			}
			if err := s.db.WithContext(ctx).Where("org_id = ? AND payload_id = ?", orgID, payloadID).Take(&meta).Error; err != nil {
				return deny(ErrVideoStorage, "STAGING_UNAVAILABLE")
			}
			if meta.CaptureStatus != VideoCaptureStored {
				return deny(ErrVideoExpired, "EXPIRED")
			}
		} else {
			ciphertext = staged.Ciphertext
		}
	case VideoCaptureStored:
	default:
		return deny(ErrVideoCorrupt, "INVALID_STATE")
	}
	if meta.CaptureStatus == VideoCaptureStored {
		if s.blobs == nil || meta.ObjectRef == "" {
			return deny(ErrVideoStorage, "BLOB_UNAVAILABLE")
		}
		ciphertext, err = s.blobs.Get(ctx, meta.ObjectRef)
		if err != nil {
			return deny(ErrVideoStorage, "BLOB_UNAVAILABLE")
		}
	}
	plain, err := s.decrypt(meta, ciphertext)
	if err != nil {
		return deny(err, "INTEGRITY_FAILED")
	}
	// Recheck after potentially slow I/O and before recording success. The
	// access audit records the authorized read, not ownership inferred by ID.
	if !meta.ExpiresAt.After(s.now()) {
		clear(plain)
		return deny(ErrVideoExpired, "EXPIRED")
	}
	if err := s.accessEvent(ctx, accessID, "finished", orgID, payloadID, access, "succeeded", ""); err != nil {
		clear(plain)
		return nil, err
	}
	return plain, nil
}
