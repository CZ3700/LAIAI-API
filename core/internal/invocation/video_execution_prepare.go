package invocation

import (
	"bytes"
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Callers cannot choose purpose, schema, capture policy, clock or TTL.
type VideoExecutionInput struct {
	OrgID                uint64
	RequestID, AttemptID string
	Body                 []byte
}

func (s *VideoStore) executionInputValid(input VideoExecutionInput, schema uint32) error {
	if s == nil || input.OrgID == 0 || !videoValidID(input.RequestID, false) || !videoValidID(input.AttemptID, true) || len(input.Body) > s.maxBytes {
		return ErrVideoInvalid
	}
	if schema == 1 && input.AttemptID != "" || schema == VideoActionInputSchemaVersion && input.AttemptID == "" {
		return ErrVideoInvalid
	}
	if s.maxRetention < 24*time.Hour {
		return ErrVideoRetentionDenied
	}
	return nil
}

func (s *VideoStore) prepareExecutionAt(input VideoExecutionInput, schema uint32, now time.Time) (*PreparedVideoPayload, error) {
	p, err := s.prepareAt(VideoPayloadInput{OrgID: input.OrgID, RequestID: input.RequestID, AttemptID: input.AttemptID,
		Body: input.Body, Purpose: VideoPurposeExecution, Direction: VideoEffectiveRequest, SchemaVersion: schema,
		CaptureEnabled: true, ExpiresAt: now.Add(24 * time.Hour)}, now)
	if err != nil {
		return nil, err
	}
	p.executionPrepared = true
	return p, nil
}

func (s *VideoStore) PrepareCreateExecutionTx(tx *gorm.DB, input VideoExecutionInput) (*PreparedVideoPayload, error) {
	if err := s.executionInputValid(input, 1); err != nil {
		return nil, err
	}
	now, err := videoExecutionAuthorityTimeTx(tx)
	if err != nil {
		return nil, err
	}
	return s.prepareExecutionAt(input, 1, now)
}

func (s *VideoStore) PrepareActionExecution(ctx context.Context, input VideoExecutionInput) (*PreparedVideoPayload, error) {
	if err := s.executionInputValid(input, VideoActionInputSchemaVersion); err != nil {
		return nil, err
	}
	var now time.Time
	err := s.executionDatabasePhase(ctx, true, func(tx *gorm.DB) error {
		var err error
		now, err = videoExecutionAuthorityTimeTx(tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.prepareExecutionAt(input, VideoActionInputSchemaVersion, now)
}

// Metadata+staging and the post-lock DB-time check share one savepoint. A
// handled error cannot leave a partial pair inside the outer business tx.
func (s *VideoStore) StageExecutionTx(tx *gorm.DB, p *PreparedVideoPayload) (string, error) {
	if p == nil || !p.executionPrepared || p.metadata.CaptureStatus != VideoCaptureStaged || p.metadata.ObjectRef != "" || p.metadata.CleanupAfter != nil || p.metadata.ObjectDeletedAt != nil {
		return "", ErrVideoInvalid
	}
	m := p.metadata
	if _, err := videoExecutionIdentityFromMetadata(m, m.OrgID, m.PayloadID, m.SchemaVersion); err != nil {
		return "", err
	}
	if m.SchemaVersion != 1 && m.SchemaVersion != VideoActionInputSchemaVersion {
		return "", ErrVideoSchemaUnsupported
	}
	if p.staging.PayloadID != m.PayloadID || videoHash(p.staging.Ciphertext) != m.CiphertextSHA256 || m.WrappedKey == "" || len(m.Nonce) == 0 {
		return "", ErrVideoCorrupt
	}
	db, cancel, err := videoExecutionBoundTx(tx)
	if err != nil {
		return "", err
	}
	defer cancel()
	// Use a savepoint on this exact connection. Cleanup must not inherit an
	// already expired method deadline (GORM nested Transaction does).
	point, err := videoRandomID("vex_")
	if err != nil {
		return "", err
	}
	if err := db.SavePoint(point).Error; err != nil {
		return "", videoExecutionAuthorityFailure(err)
	}
	err = func(q *gorm.DB) error {
		meta := m
		var before VideoPayload
		beforeErr := q.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("correlation_key = ? AND org_id = ?", m.CorrelationKey, m.OrgID).Take(&before).Error
		if beforeErr != nil && !errors.Is(beforeErr, gorm.ErrRecordNotFound) {
			return videoExecutionAuthorityFailure(beforeErr)
		}
		result := q.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "correlation_key"}}, DoNothing: true}).Create(&meta)
		if result.Error != nil {
			return videoExecutionAuthorityFailure(result.Error)
		}
		// MySQL ON DUPLICATE KEY's affected-row mode is not a proof of insert.
		// Always re-read the unique row under a current lock and compare bytes.
		var old VideoPayload
		if err := q.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("correlation_key = ? AND org_id = ?", m.CorrelationKey, m.OrgID).Take(&old).Error; err != nil {
			return videoExecutionAuthorityFailure(err)
		}
		if !videoSamePayload(old, m) {
			return ErrVideoConflict
		}
		if old.CaptureStatus == VideoCaptureExpired {
			return ErrVideoExpired
		}
		if old.CaptureStatus == VideoCaptureStaged {
			var stage VideoPayloadStaging
			err := q.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("payload_id = ?", m.PayloadID).Take(&stage).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if beforeErr == nil {
					return ErrVideoCorrupt
				}
				stage = p.staging
				if err := q.Session(&gorm.Session{NewDB: true}).Create(&stage).Error; err != nil {
					return videoExecutionAuthorityFailure(err)
				}
			} else if err != nil {
				return videoExecutionAuthorityFailure(err)
			} else if stage.PayloadID != m.PayloadID || !bytes.Equal(stage.Ciphertext, p.staging.Ciphertext) {
				return ErrVideoCorrupt
			}
		} else if old.CaptureStatus != VideoCaptureStored || old.ObjectRef == "" {
			return ErrVideoCorrupt
		}
		now, err := videoExecutionAuthorityTimeTx(q)
		if err != nil {
			return err
		}
		if !old.ExpiresAt.After(now) {
			return ErrVideoExpired
		}
		return nil
	}(db)
	if err = videoExecutionResultError(db.Statement.Context, err); err != nil {
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(tx.Statement.Context), videoExecutionDatabaseTimeout)
		rollbackErr := tx.Session(&gorm.Session{NewDB: true, Context: cleanupCtx, Logger: logger.Discard}).RollbackTo(point).Error
		stop()
		if rollbackErr != nil {
			return "", errors.Join(err, videoExecutionAuthorityFailure(rollbackErr))
		}
		return "", err
	}
	return m.PayloadID, nil
}
