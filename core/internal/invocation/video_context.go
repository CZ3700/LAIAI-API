package invocation

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// VideoRequestContext enriches a request after its immutable start record.
// It contains neither bodies nor copied financial facts.
type VideoRequestContext struct {
	RequestID           string `gorm:"primaryKey;column:request_id"`
	OrgID               uint64
	Operation           string
	PublicModel         string
	TaskID              string
	OperationID         string
	IdempotencyRecordID *uint64
	ReplayState         string
	OriginRequestID     string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (VideoRequestContext) TableName() string { return "video_request_contexts" }

func PutVideoRequestContextTx(tx *gorm.DB, in VideoRequestContext) error {
	if tx == nil || in.OrgID == 0 || !videoValidID(in.RequestID, false) || !videoValidID(in.TaskID, true) ||
		!videoValidID(in.OperationID, true) || !videoValidID(in.OriginRequestID, true) || !videoValidID(in.Operation, false) ||
		len(in.PublicModel) > 128 || in.PublicModel != "" && !videoIdentifier.MatchString(in.PublicModel) {
		return ErrVideoInvalid
	}
	if in.ReplayState == "" {
		in.ReplayState = "unknown"
	}
	if in.ReplayState != "unknown" && in.ReplayState != "new" && in.ReplayState != "replayed" {
		return ErrVideoInvalid
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	in.UpdatedAt = in.CreatedAt
	r := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(&in)
	if r.Error != nil {
		return ErrVideoStorage
	}
	if r.RowsAffected == 1 {
		return nil
	}
	var old VideoRequestContext
	q := tx.Where("org_id = ? AND request_id = ?", in.OrgID, in.RequestID)
	if tx.Dialector.Name() == "mysql" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Take(&old).Error; err != nil {
		return ErrVideoConflict
	}
	if old.Operation != in.Operation || old.TaskID != "" && in.TaskID != "" && old.TaskID != in.TaskID ||
		old.OriginRequestID != "" && in.OriginRequestID != "" && old.OriginRequestID != in.OriginRequestID ||
		old.OperationID != "" && in.OperationID != "" && old.OperationID != in.OperationID ||
		old.PublicModel != "" && in.PublicModel != "" && old.PublicModel != in.PublicModel ||
		old.IdempotencyRecordID != nil && in.IdempotencyRecordID != nil && *old.IdempotencyRecordID != *in.IdempotencyRecordID ||
		old.ReplayState != "unknown" && in.ReplayState != "unknown" && old.ReplayState != in.ReplayState {
		return ErrVideoConflict
	}
	u := map[string]any{"updated_at": in.UpdatedAt}
	if in.PublicModel != "" {
		u["public_model"] = in.PublicModel
	}
	if in.TaskID != "" {
		u["task_id"] = in.TaskID
	}
	if in.OperationID != "" {
		u["operation_id"] = in.OperationID
	}
	if in.OriginRequestID != "" {
		u["origin_request_id"] = in.OriginRequestID
	}
	if in.IdempotencyRecordID != nil {
		u["idempotency_record_id"] = in.IdempotencyRecordID
	}
	if in.ReplayState != "unknown" {
		u["replay_state"] = in.ReplayState
	}
	return tx.Model(&VideoRequestContext{}).Where("org_id = ? AND request_id = ?", in.OrgID, in.RequestID).Updates(u).Error
}

// LinkRequestPayloadTx only enriches an empty pointer with an owned client-history
// payload. It cannot change auth, start/finish facts or replace an earlier body.
func (s *VideoStore) LinkRequestPayloadTx(tx *gorm.DB, org uint64, rid, payload string) error {
	var p VideoPayload
	if err := tx.Where("org_id = ? AND request_id = ? AND payload_id = ? AND purpose = ? AND direction = ?", org, rid, payload, VideoPurposeHistory, VideoClientRequest).Take(&p).Error; err != nil {
		return ErrVideoNotFound
	}
	var row VideoRequestLog
	if err := tx.Where("org_id = ? AND request_id = ?", org, rid).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} // direct application calls have no HTTP request
		return ErrVideoStorage
	}
	if row.RequestPayloadID == payload {
		return nil
	}
	if row.RequestPayloadID != "" {
		return ErrVideoConflict
	}
	r := tx.Model(&VideoRequestLog{}).Where("org_id = ? AND request_id = ? AND request_payload_id = ?", org, rid, "").Update("request_payload_id", payload)
	if r.Error != nil {
		return ErrVideoStorage
	}
	if r.RowsAffected != 1 {
		return ErrVideoConflict
	}
	return nil
}
