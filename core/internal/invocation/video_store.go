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
			return videoStorageFailure(result.Error)
		}
		if result.RowsAffected == 0 {
			var old VideoPayload
			if err := db.Where("correlation_key = ? AND org_id = ?", meta.CorrelationKey, meta.OrgID).Take(&old).Error; err != nil {
				return videoStorageFailure(err)
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
			return videoStorageFailure(err)
		}
		return nil
	})
	if err != nil {
		// SAVEPOINT/rollback errors also remain safe but classifiable.
		if errors.Is(err, ErrVideoStorage) || errors.Is(err, ErrVideoExpired) || errors.Is(err, ErrVideoConflict) {
			return "", err
		}
		return "", videoStorageFailure(err)
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
	return s.ReadForExecutionSchema(ctx, orgID, payloadID, 1, access)
}

// ReadForExecutionSchema requires the format fixed by the task's runtime.
// Empty, unknown or mismatched formats are rejected before reading ciphertext.
func (s *VideoStore) ReadForExecutionSchema(ctx context.Context, orgID uint64, payloadID string, expectedSchema uint32, access VideoAccess) ([]byte, error) {
	return s.read(ctx, orgID, payloadID, VideoPurposeExecution, false, &expectedSchema, access)
}

// ValidateVideoExecutionSchemaTx checks only the execution interpretation
// identity before a host claim or a financial release decision. It deliberately
// does not decrypt, read staging/blob data, emit a plaintext-access audit, or
// interpret expiry. A known v1 payload may still expire under its v1 semantics.
func ValidateVideoExecutionSchemaTx(tx *gorm.DB, org uint64, payloadID string, expectedSchema uint32) error {
	if tx == nil || tx.Statement == nil {
		return ErrVideoTransactionRequired
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrVideoTransactionRequired
	}
	if org == 0 || !videoValidID(payloadID, false) {
		return ErrVideoInvalid
	}
	var meta VideoPayload
	err := tx.Select("payload_id", "org_id", "purpose", "direction", "schema_version").Where("org_id = ? AND payload_id = ?", org, payloadID).Take(&meta).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrVideoNotFound
	}
	if err != nil {
		return ErrVideoStorage
	}
	if meta.Purpose != VideoPurposeExecution || meta.Direction != VideoEffectiveRequest {
		return ErrVideoAccessDenied
	}
	if !videoReadSchemaSupported(meta, &expectedSchema) {
		return ErrVideoSchemaUnsupported
	}
	return nil
}

func (s *VideoStore) ReadForHistory(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error) {
	return s.read(ctx, orgID, payloadID, VideoPurposeHistory, false, nil, access)
}

// ReadForDelivery exposes only the separately validated result-link capsule.
// It cannot read history or execution data, including when history is disabled.
func (s *VideoStore) ReadForDelivery(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error) {
	return s.ReadForDeliverySchema(ctx, orgID, payloadID, 1, access)
}

// ReadForDeliverySchema applies the runtime's fixed capsule format while keeping
// the same tenant, role, purpose and audit requirements as ReadForDelivery.
func (s *VideoStore) ReadForDeliverySchema(ctx context.Context, orgID uint64, payloadID string, expectedSchema uint32, access VideoAccess) ([]byte, error) {
	return s.read(ctx, orgID, payloadID, VideoPurposeDelivery, false, &expectedSchema, access)
}

// ReadForRecovery is limited to upstream responses. It does not grant workers
// historical client requests, responses, or execution payloads.
func (s *VideoStore) ReadForRecovery(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error) {
	return s.read(ctx, orgID, payloadID, VideoPurposeHistory, true, nil, access)
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

func (s *VideoStore) read(ctx context.Context, orgID uint64, payloadID, purpose string, recovery bool, expectedSchema *uint32, access VideoAccess) ([]byte, error) {
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
	if !videoReadSchemaSupported(meta, expectedSchema) {
		return deny(ErrVideoSchemaUnsupported, "SCHEMA_UNSUPPORTED")
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
			// A concurrent upload replaced our metadata snapshot. Recheck
			// authorization and format before interpreting its retention state;
			// an unknown format is not evidence of an expired unsent request.
			if meta.Purpose != purpose || purpose == VideoPurposeExecution && meta.Direction != VideoEffectiveRequest || (recovery || purpose == VideoPurposeDelivery) && meta.Direction != VideoUpstreamResponse {
				return deny(ErrVideoAccessDenied, "PURPOSE_DENIED")
			}
			if !videoReadSchemaSupported(meta, expectedSchema) {
				return deny(ErrVideoSchemaUnsupported, "SCHEMA_UNSUPPORTED")
			}
			if meta.CaptureStatus != VideoCaptureStored || !meta.ExpiresAt.After(s.now()) {
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

func videoReadSchemaSupported(meta VideoPayload, expected *uint32) bool {
	// History/recovery returns retained evidence without choosing an execution
	// interpreter. Typed execution and delivery reads always pass a non-nil
	// expectation; zero never opts out of their format check.
	return expected == nil || *expected == 1 && meta.SchemaVersion == *expected
}
