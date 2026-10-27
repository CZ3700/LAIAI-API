package video

import (
	"crypto/sha256"
	"encoding/json"
	"time"
)

// queryReceiptDigestV1 is the published pre-S2 JSON shape. Its exported field
// names, types and order are part of the digest. Keep the cleared progress
// fields in the encoding; future ORM fields must not enter this projection.
type queryReceiptDigestV1 struct {
	AttemptID                string
	OrgID                    uint64
	TaskID                   string
	Sequence                 uint64
	TaskRowVersion           uint64
	RuntimeVersion           string
	AdapterVersion           string
	ProtocolContractVersion  string
	ObservationSchemaVersion uint32
	HTTPStatus               int
	Outcome                  string
	ProviderRequestID        string
	ObservationJSON          string
	HasVideo                 bool
	HasLastFrame             bool
	PayloadRef               string
	ReceivedAt               time.Time
	AppliedAt                *time.Time
	ApplyErrorCode           string
	RecoveryDueAt            *time.Time
	RecoveryVersion          uint64
}

func queryReceiptDigestProjection(row QueryReceipt) queryReceiptDigestV1 {
	return queryReceiptDigestV1{
		AttemptID: row.AttemptID, OrgID: row.OrgID, TaskID: row.TaskID,
		Sequence: row.Sequence, TaskRowVersion: row.TaskRowVersion,
		RuntimeVersion: row.RuntimeVersion, AdapterVersion: row.AdapterVersion,
		ProtocolContractVersion:  row.ProtocolContractVersion,
		ObservationSchemaVersion: row.ObservationSchemaVersion,
		HTTPStatus:               row.HTTPStatus, Outcome: row.Outcome,
		ProviderRequestID: row.ProviderRequestID, ObservationJSON: row.ObservationJSON,
		HasVideo: row.HasVideo, HasLastFrame: row.HasLastFrame,
		PayloadRef: row.PayloadRef, ReceivedAt: row.ReceivedAt.UTC(),
		AppliedAt: nil, ApplyErrorCode: "", RecoveryDueAt: nil, RecoveryVersion: 0,
	}
}

func queryEvidenceDigest(row QueryReceipt) ([32]byte, error) {
	raw, err := json.Marshal(queryReceiptDigestProjection(row))
	if err != nil {
		return [32]byte{}, safeRecoveryErrors(err)
	}
	return sha256.Sum256(raw), nil
}
