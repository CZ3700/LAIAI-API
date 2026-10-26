package invocation

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// One current observation, not a cached send permit or a fence.
type VideoExecutionWindow struct {
	Identity     VideoExecutionIdentity
	DatabaseTime time.Time
}

func videoExecutionIdentityFromMetadata(m VideoPayload, org uint64, id string, schema uint32) (VideoExecutionIdentity, error) {
	var out VideoExecutionIdentity
	if org == 0 || !videoValidID(id, false) {
		return out, ErrVideoInvalid
	}
	if m.OrgID != org || m.PayloadID != id {
		return out, ErrVideoNotFound
	}
	if m.Purpose != VideoPurposeExecution || m.Direction != VideoEffectiveRequest {
		return out, ErrVideoAccessDenied
	}
	if (schema != 1 && schema != VideoActionInputSchemaVersion) || m.SchemaVersion != schema {
		return out, ErrVideoSchemaUnsupported
	}
	correlation, _ := json.Marshal([]any{m.OrgID, m.RequestID, m.AttemptID, m.Direction, m.Purpose, m.SchemaVersion})
	if !videoValidID(m.RequestID, false) || !videoValidID(m.AttemptID, true) || m.CorrelationKey != videoHash(correlation) {
		return out, ErrVideoCorrupt
	}
	out = VideoExecutionIdentity{PayloadID: m.PayloadID, OrgID: m.OrgID, RequestID: m.RequestID, AttemptID: m.AttemptID,
		CorrelationKey: m.CorrelationKey, Direction: m.Direction, Purpose: m.Purpose, SchemaVersion: m.SchemaVersion,
		CiphertextSHA256: m.CiphertextSHA256, PlaintextBytes: m.PlaintextBytes, BodyFormat: m.BodyFormat,
		CaptureRepresentation: m.CaptureRepresentation, RedactionVersion: m.RedactionVersion,
		CreatedAt: m.CreatedAt.UTC().Truncate(time.Millisecond), ExpiresAt: m.ExpiresAt.UTC().Truncate(time.Millisecond)}
	return out, nil
}

func videoExecutionMetadataTx(tx *gorm.DB, org uint64, id string, schema uint32) (VideoPayload, VideoExecutionIdentity, error) {
	var m VideoPayload
	if org == 0 || !videoValidID(id, false) {
		return m, VideoExecutionIdentity{}, ErrVideoInvalid
	}
	if err := videoExecutionTransactionRequired(tx); err != nil {
		return m, VideoExecutionIdentity{}, err
	}
	err := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "SHARE"}).Where("org_id = ? AND payload_id = ?", org, id).Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return VideoPayload{}, VideoExecutionIdentity{}, ErrVideoNotFound
	}
	if err != nil {
		if cause := tx.Statement.Context.Err(); cause != nil {
			err = errors.Join(err, cause)
		}
		return VideoPayload{}, VideoExecutionIdentity{}, videoExecutionAuthorityFailure(err)
	}
	identity, err := videoExecutionIdentityFromMetadata(m, org, id, schema)
	return m, identity, err
}

func videoExecutionLifecycle(m VideoPayload, now time.Time) error {
	if m.CaptureStatus == VideoCaptureExpired || !m.ExpiresAt.After(now) {
		return ErrVideoExpired
	}
	if m.CaptureStatus == VideoCaptureDisabled {
		return ErrVideoRetentionDenied
	}
	if m.CaptureStatus != VideoCaptureStaged && m.CaptureStatus != VideoCaptureStored {
		return ErrVideoCorrupt
	}
	return nil
}

func checkVideoExecutionWindowTx(tx *gorm.DB, org uint64, id string, schema uint32) (out VideoExecutionWindow, resultErr error) {
	db, cancel, err := videoExecutionBoundTx(tx)
	if err != nil {
		return out, err
	}
	defer cancel()
	// A completed metadata query can race cancellation even when its result is
	// missing or has an unsupported identity. Preserve both facts before the
	// local phase is canceled; no failed observation may retain a send window.
	defer func() {
		resultErr = videoExecutionResultError(db.Statement.Context, resultErr)
		if resultErr != nil {
			out = VideoExecutionWindow{}
		}
	}()
	m, identity, err := videoExecutionMetadataTx(db, org, id, schema)
	if err != nil {
		return out, err
	}
	now, err := videoExecutionAuthorityTimeTx(db)
	if err != nil {
		return out, err
	}
	if err := videoExecutionLifecycle(m, now); err != nil {
		return out, err
	}
	return VideoExecutionWindow{Identity: identity, DatabaseTime: now}, nil
}

func CheckVideoExecutionWindowTx(tx *gorm.DB, org uint64, id string, expectedSchema uint32) (VideoExecutionWindow, error) {
	// A caller may not opt create into the action interpreter.
	if expectedSchema != 1 {
		return VideoExecutionWindow{}, ErrVideoSchemaUnsupported
	}
	return checkVideoExecutionWindowTx(tx, org, id, 1)
}

func CheckVideoActionExecutionWindowTx(tx *gorm.DB, org uint64, id string) (VideoExecutionWindow, error) {
	return checkVideoExecutionWindowTx(tx, org, id, VideoActionInputSchemaVersion)
}
