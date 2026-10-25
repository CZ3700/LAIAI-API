package invocation

import (
	"context"

	"gorm.io/gorm"
)

const VideoActionInputSchemaVersion = uint32(2)

// ReadForActionExecution is the narrow worker reader for mutation input v2.
// It does not expand the schema accepted by create, history or delivery APIs.
// The shared read lifecycle enforces tenant, worker role, purpose, expiry,
// integrity and both mandatory access audit writes before returning plaintext.
func (s *VideoStore) ReadForActionExecution(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error) {
	return s.readWithSchemaPolicy(ctx, orgID, payloadID, VideoPurposeExecution, false, videoActionInputSchema, access)
}

// ReadVideoActionInputIdentityTx is an internal transaction-only metadata
// boundary. Expired input can still prove durable response ownership without
// restoring plaintext access or requiring staging, blob, or keyring services.
func ReadVideoActionInputIdentityTx(tx *gorm.DB, org uint64, payloadID string) (VideoExecutionIdentity, error) {
	return readVideoExecutionIdentityTx(tx, org, payloadID, videoActionInputSchema)
}

func videoActionInputSchema(meta VideoPayload) bool {
	return meta.Purpose == VideoPurposeExecution && meta.Direction == VideoEffectiveRequest &&
		meta.SchemaVersion == VideoActionInputSchemaVersion
}
