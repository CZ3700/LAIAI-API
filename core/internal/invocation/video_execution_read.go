package invocation

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *VideoStore) ReadForExecutionSchemaWithDatabaseTime(ctx context.Context, org uint64, id string, expectedSchema uint32, access VideoAccess) ([]byte, error) {
	if expectedSchema != 1 {
		// An unsupported expectation still follows role/tenant/purpose checks;
		// zero is a fixed rejecting policy, never an untyped read opt-out.
		return s.readExecutionWithDatabaseTime(ctx, org, id, 0, access)
	}
	return s.readExecutionWithDatabaseTime(ctx, org, id, 1, access)
}

func (s *VideoStore) ReadForActionExecutionWithDatabaseTime(ctx context.Context, org uint64, id string, access VideoAccess) ([]byte, error) {
	return s.readExecutionWithDatabaseTime(ctx, org, id, VideoActionInputSchemaVersion, access)
}

func (s *VideoStore) executionAccessTx(tx *gorm.DB, accessID, phase string, org uint64, id string, access VideoAccess, outcome, code string, idempotent bool) error {
	row := VideoPayloadAccess{AccessID: accessID, Phase: phase, OrgID: org, PayloadID: id,
		ActorRole: access.ActorRole, ActorID: access.ActorID, ReasonCode: access.ReasonCode, OperationID: access.OperationID,
		Outcome: outcome, SafeErrorCode: code, CreatedAt: s.now().UTC().Truncate(time.Millisecond)}
	q := tx.Session(&gorm.Session{NewDB: true})
	if idempotent {
		q = q.Clauses(clause.OnConflict{DoNothing: true})
	}
	if err := q.Create(&row).Error; err != nil {
		return errors.Join(ErrVideoAuditUnavailable, videoExecutionAuthorityFailure(err))
	}
	return nil
}

func videoExecutionDenialCode(err error) string {
	switch {
	case errors.Is(err, ErrVideoSchemaUnsupported):
		return "SCHEMA_UNSUPPORTED"
	case errors.Is(err, ErrVideoAccessDenied):
		return "ACCESS_DENIED"
	case errors.Is(err, ErrVideoNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrVideoCorrupt), errors.Is(err, ErrVideoConflict):
		return "INTEGRITY_FAILED"
	case errors.Is(err, ErrVideoExecutionAuthorityUnavailable):
		return "AUTHORITY_UNAVAILABLE"
	case errors.Is(err, ErrVideoExpired):
		return "EXPIRED"
	case errors.Is(err, ErrVideoRetentionDenied):
		return "RETENTION_DISABLED"
	default:
		return "READ_UNAVAILABLE"
	}
}

func (s *VideoStore) readExecutionWithDatabaseTime(ctx context.Context, org uint64, id string, schema uint32, access VideoAccess) ([]byte, error) {
	if ctx == nil || org == 0 || !videoValidID(id, false) || !videoValidID(access.ActorID, false) ||
		!videoValidID(access.OperationID, true) || !videoValidID(access.ReasonCode, false) ||
		(access.ActorRole != VideoRoleWorker && access.ActorRole != VideoRoleOrgReader && access.ActorRole != VideoRoleRoot) {
		return nil, ErrVideoInvalid
	}
	accessID, err := videoRandomID("vac_")
	if err != nil {
		return nil, err
	}
	deny := func(reason error) ([]byte, error) {
		// An audit failure never replaces an identity/unknown/authority denial.
		// In particular, a canceled audit cannot turn that denial into NotSent.
		auditErr := s.executionDatabasePhase(ctx, false, func(tx *gorm.DB) error {
			if err := s.executionAccessTx(tx, accessID, "started", org, id, access, "started", "", true); err != nil {
				return err
			}
			return s.executionAccessTx(tx, accessID, "finished", org, id, access, "denied", videoExecutionDenialCode(reason), false)
		})
		if auditErr != nil {
			return nil, errors.Join(reason, ErrVideoAuditUnavailable, auditErr)
		}
		return nil, reason
	}
	if access.ActorRole != VideoRoleWorker {
		return deny(ErrVideoAccessDenied)
	}
	var meta VideoPayload
	var identity VideoExecutionIdentity
	var ciphertext []byte
	err = s.executionDatabasePhase(ctx, false, func(tx *gorm.DB) error {
		var err error
		meta, identity, err = videoExecutionMetadataTx(tx, org, id, schema)
		if err != nil {
			return err
		}
		// Metadata identity precedes audit storage availability. No ciphertext
		// has been read or decrypted; unknown identity still reaches deny() if
		// even the started audit cannot be written.
		if err := s.executionAccessTx(tx, accessID, "started", org, id, access, "started", "", false); err != nil {
			return err
		}
		now, err := videoExecutionAuthorityTimeTx(tx)
		if err != nil {
			return err
		}
		if err := videoExecutionLifecycle(meta, now); err != nil {
			return err
		}
		if meta.CaptureStatus == VideoCaptureStaged {
			var stage VideoPayloadStaging
			if err := tx.Session(&gorm.Session{NewDB: true}).Where("payload_id = ?", id).Take(&stage).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrVideoCorrupt
				}
				return videoExecutionAuthorityFailure(err)
			}
			if stage.PayloadID != id {
				return ErrVideoCorrupt
			}
			ciphertext = stage.Ciphertext
		} else if meta.ObjectRef == "" {
			return ErrVideoCorrupt
		}
		return nil
	})
	if err != nil {
		return deny(err)
	}
	// No metadata lock or SQL transaction crosses external I/O or decryption.
	if meta.CaptureStatus == VideoCaptureStored {
		if s.blobs == nil {
			return deny(ErrVideoStorage)
		}
		blobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ciphertext, err = s.blobs.Get(blobCtx, meta.ObjectRef)
		cause := blobCtx.Err()
		cancel()
		if cause != nil {
			return deny(errors.Join(ErrVideoStorage, videoExecutionAuthorityFailure(cause)))
		}
		if err != nil {
			return deny(ErrVideoStorage)
		}
	}
	plain, err := s.decrypt(meta, ciphertext)
	if err != nil {
		return deny(err)
	}
	err = s.executionDatabasePhase(ctx, false, func(tx *gorm.DB) error {
		current, currentIdentity, err := videoExecutionMetadataTx(tx, org, id, schema)
		if err != nil {
			return err
		}
		if currentIdentity != identity {
			return ErrVideoCorrupt
		}
		// Persist success within this transaction, then obtain the LAST clock.
		// A delayed audit cannot authorize plaintext after expiry; rollback removes
		// that tentative success and deny() records the actual outcome instead.
		if err := s.executionAccessTx(tx, accessID, "finished", org, id, access, "succeeded", "", false); err != nil {
			return err
		}
		now, err := videoExecutionAuthorityTimeTx(tx)
		if err != nil {
			return err
		}
		if err := videoExecutionLifecycle(current, now); err != nil {
			return err
		}
		if !videoSamePayload(meta, current) {
			return ErrVideoCorrupt
		}
		return nil
	})
	if err != nil {
		clear(plain)
		return deny(err)
	}
	return plain, nil
}
