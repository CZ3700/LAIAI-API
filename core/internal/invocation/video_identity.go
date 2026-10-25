package invocation

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// VideoExecutionIdentity contains immutable metadata only. Lifecycle fields,
// wrapped keys, ciphertext and plaintext never cross this domain boundary.
type VideoExecutionIdentity struct {
	PayloadID, RequestID, AttemptID, CorrelationKey     string
	OrgID                                               uint64
	Direction, Purpose                                  string
	SchemaVersion                                       uint32
	CiphertextSHA256                                    string
	PlaintextBytes                                      int64
	BodyFormat, CaptureRepresentation, RedactionVersion string
	CreatedAt, ExpiresAt                                time.Time
}

func videoIdentityTransaction(tx *gorm.DB) error {
	if tx == nil || tx.Statement == nil {
		return ErrVideoTransactionRequired
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrVideoTransactionRequired
	}
	return nil
}

// ReadVideoExecutionIdentityTx deliberately ignores expiry/capture status. A
// durable response may outlive its input capsule; identity is still verifiable
// without restoring access to expired plaintext.
func ReadVideoExecutionIdentityTx(tx *gorm.DB, org uint64, payloadID string, expectedSchema uint32) (VideoExecutionIdentity, error) {
	return readVideoExecutionIdentityTx(tx, org, payloadID, func(meta VideoPayload) bool {
		return videoReadSchemaSupported(meta, &expectedSchema)
	})
}

func readVideoExecutionIdentityTx(tx *gorm.DB, org uint64, payloadID string, schemaPolicy func(VideoPayload) bool) (VideoExecutionIdentity, error) {
	var out VideoExecutionIdentity
	if err := videoIdentityTransaction(tx); err != nil {
		return out, err
	}
	if org == 0 || !videoValidID(payloadID, false) {
		return out, ErrVideoInvalid
	}
	var meta VideoPayload
	err := tx.Select("payload_id", "org_id", "request_id", "attempt_id", "correlation_key", "direction", "purpose", "schema_version", "ciphertext_sha256", "plaintext_bytes", "body_format", "capture_representation", "redaction_version", "created_at", "expires_at").Where("org_id = ? AND payload_id = ?", org, payloadID).Take(&meta).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, ErrVideoNotFound
	}
	if err != nil {
		return out, videoStorageFailure(err)
	}
	if meta.Purpose != VideoPurposeExecution || meta.Direction != VideoEffectiveRequest {
		return out, ErrVideoAccessDenied
	}
	if !schemaPolicy(meta) {
		return out, ErrVideoSchemaUnsupported
	}
	correlation, _ := json.Marshal([]any{meta.OrgID, meta.RequestID, meta.AttemptID, meta.Direction, meta.Purpose, meta.SchemaVersion})
	if meta.CorrelationKey != videoHash(correlation) {
		return out, ErrVideoCorrupt
	}
	out = VideoExecutionIdentity{PayloadID: meta.PayloadID, OrgID: meta.OrgID, RequestID: meta.RequestID, AttemptID: meta.AttemptID,
		CorrelationKey: meta.CorrelationKey, Direction: meta.Direction, Purpose: meta.Purpose, SchemaVersion: meta.SchemaVersion,
		CiphertextSHA256: meta.CiphertextSHA256, PlaintextBytes: meta.PlaintextBytes, BodyFormat: meta.BodyFormat,
		CaptureRepresentation: meta.CaptureRepresentation, RedactionVersion: meta.RedactionVersion,
		CreatedAt: meta.CreatedAt.UTC().Truncate(time.Millisecond), ExpiresAt: meta.ExpiresAt.UTC().Truncate(time.Millisecond)}
	return out, nil
}

type VideoPayloadReference struct {
	OrgID                                               uint64
	PayloadID, RequestID, AttemptID, Direction, Purpose string
	SchemaVersion                                       uint32
}

// ValidateVideoPayloadReferenceTx proves a receipt's exact metadata association.
// Disabled/expired history remains valid evidence ownership. It never decrypts
// or reads staging/blob bytes and does not grant a historical read permission.
func ValidateVideoPayloadReferenceTx(tx *gorm.DB, expected VideoPayloadReference) error {
	if err := videoIdentityTransaction(tx); err != nil {
		return err
	}
	if expected.OrgID == 0 || !videoValidID(expected.PayloadID, false) || !videoValidID(expected.RequestID, false) || !videoValidID(expected.AttemptID, false) {
		return ErrVideoInvalid
	}
	if expected.SchemaVersion != 1 {
		return ErrVideoSchemaUnsupported
	}
	var meta VideoPayload
	err := tx.Select("payload_id", "org_id", "request_id", "attempt_id", "correlation_key", "direction", "purpose", "schema_version", "capture_status", "object_ref").Where("org_id = ? AND payload_id = ?", expected.OrgID, expected.PayloadID).Take(&meta).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrVideoNotFound
	}
	if err != nil {
		return videoStorageFailure(err)
	}
	if meta.RequestID != expected.RequestID || meta.AttemptID != expected.AttemptID || meta.Direction != expected.Direction || meta.Purpose != expected.Purpose {
		return ErrVideoConflict
	}
	if meta.SchemaVersion != expected.SchemaVersion {
		return ErrVideoSchemaUnsupported
	}
	correlation, _ := json.Marshal([]any{meta.OrgID, meta.RequestID, meta.AttemptID, meta.Direction, meta.Purpose, meta.SchemaVersion})
	if meta.CorrelationKey != videoHash(correlation) {
		return ErrVideoCorrupt
	}
	switch meta.CaptureStatus {
	case VideoCaptureStaged:
		var n int64
		if err := tx.Model(&VideoPayloadStaging{}).Where("payload_id = ?", meta.PayloadID).Count(&n).Error; err != nil {
			return videoStorageFailure(err)
		}
		if n != 1 {
			return ErrVideoCorrupt
		}
	case VideoCaptureStored:
		if meta.ObjectRef == "" {
			return ErrVideoCorrupt
		}
	case VideoCaptureDisabled, VideoCaptureExpired:
	default:
		return ErrVideoCorrupt
	}
	return nil
}
