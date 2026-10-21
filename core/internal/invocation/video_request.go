package invocation

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// VideoRequestStart contains classified metadata, never raw URLs, headers,
// prompts, user agents, or error messages. Route is the router's template.
// OrgID/UserID/APIKeyID remain NULL for unauthenticated requests.
type VideoRequestStart struct {
	RequestID        string
	TraceID          string
	OrgID            *uint64
	UserID           *uint64
	APIKeyID         *uint64
	AuthOutcome      string
	Method           string
	Route            string
	Operation        string
	ResourceType     string
	ResourceID       string
	RequestBytes     int64
	RequestPayloadID string
	StartedAt        time.Time
}

type VideoRequestFinish struct {
	RequestID         string
	OrgID             *uint64
	HTTPStatus        int
	ResponseBytes     int64
	ResponsePayloadID string
	SafeErrorCode     string
	Outcome           string // completed or incomplete
	FinishedAt        time.Time
}

type VideoRequestLog struct {
	VideoRequestStart `gorm:"embedded"`
	HTTPStatus        int
	ResponseBytes     int64
	ResponsePayloadID string
	SafeErrorCode     string
	Outcome           string
	FinishedAt        *time.Time
}

func (VideoRequestLog) TableName() string { return "api_request_logs" }

func videoSameOrg(a, b *uint64) bool          { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func videoValidOptionalNumber(v *uint64) bool { return v == nil || *v > 0 }

func (s *VideoStore) RequestStartTx(tx *gorm.DB, input VideoRequestStart) error {
	if tx == nil || !videoValidID(input.RequestID, false) || !videoValidID(input.TraceID, true) ||
		!videoValidOptionalNumber(input.OrgID) || !videoValidOptionalNumber(input.UserID) || !videoValidOptionalNumber(input.APIKeyID) ||
		!videoValidID(input.Operation, false) || !videoValidID(input.ResourceType, true) || !videoValidID(input.ResourceID, true) ||
		!videoValidID(input.RequestPayloadID, true) || input.RequestBytes < 0 || len(input.Route) > 160 ||
		!strings.HasPrefix(input.Route, "/") || strings.ContainsAny(input.Route, "?#@\r\n\t ") {
		return ErrVideoInvalid
	}
	switch input.AuthOutcome {
	case "authenticated", "rejected", "anonymous":
	default:
		return ErrVideoInvalid
	}
	if input.AuthOutcome == "authenticated" && input.OrgID == nil || input.OrgID == nil && (input.UserID != nil || input.APIKeyID != nil) {
		return ErrVideoInvalid
	}
	switch input.Method {
	case http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodPatch, http.MethodPut, http.MethodHead, http.MethodOptions:
	default:
		return ErrVideoInvalid
	}
	providedTime := !input.StartedAt.IsZero()
	if !providedTime {
		input.StartedAt = s.now()
	}
	input.StartedAt = input.StartedAt.UTC().Truncate(time.Millisecond)
	row := VideoRequestLog{VideoRequestStart: input, Outcome: "started"}
	result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(&row)
	if result.Error != nil {
		return ErrVideoStorage
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var old VideoRequestLog
	if err := tx.Where("request_id = ?", input.RequestID).Take(&old).Error; err != nil {
		return ErrVideoStorage
	}
	a, b := old.VideoRequestStart, input
	// A later authenticated capture may fill the pointer without changing the
	// original start intent. Replaying that original empty-pointer intent is safe.
	if b.RequestPayloadID == "" {
		b.RequestPayloadID = a.RequestPayloadID
	}
	if !providedTime {
		b.StartedAt = a.StartedAt
	}
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	if string(left) != string(right) {
		return ErrVideoConflict
	}
	return nil
}

func (s *VideoStore) RequestFinishTx(tx *gorm.DB, input VideoRequestFinish) error {
	if tx == nil || !videoValidID(input.RequestID, false) || !videoValidOptionalNumber(input.OrgID) ||
		!videoValidID(input.ResponsePayloadID, true) || !videoValidID(input.SafeErrorCode, true) || input.ResponseBytes < 0 ||
		(input.Outcome != "completed" && input.Outcome != "incomplete") || (input.Outcome == "completed" && (input.HTTPStatus < 100 || input.HTTPStatus > 599)) ||
		(input.Outcome == "incomplete" && input.HTTPStatus != 0) {
		return ErrVideoInvalid
	}
	var old VideoRequestLog
	if err := tx.Where("request_id = ?", input.RequestID).Take(&old).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return ErrVideoNotFound
		}
		return ErrVideoStorage
	}
	if !videoSameOrg(old.OrgID, input.OrgID) {
		return ErrVideoNotFound
	}
	providedTime := !input.FinishedAt.IsZero()
	if !providedTime {
		input.FinishedAt = s.now()
	}
	input.FinishedAt = input.FinishedAt.UTC().Truncate(time.Millisecond)
	if input.FinishedAt.Before(old.StartedAt) {
		return ErrVideoInvalid
	}
	if old.Outcome != "started" {
		if old.HTTPStatus != input.HTTPStatus || old.ResponseBytes != input.ResponseBytes || old.ResponsePayloadID != input.ResponsePayloadID ||
			old.SafeErrorCode != input.SafeErrorCode || old.Outcome != input.Outcome || old.FinishedAt == nil || providedTime && !old.FinishedAt.Equal(input.FinishedAt) {
			return ErrVideoConflict
		}
		return nil
	}
	result := tx.Model(&VideoRequestLog{}).Where("request_id = ? AND outcome = ?", input.RequestID, "started").Updates(map[string]any{
		"http_status": input.HTTPStatus, "response_bytes": input.ResponseBytes, "response_payload_id": input.ResponsePayloadID,
		"safe_error_code": input.SafeErrorCode, "outcome": input.Outcome, "finished_at": input.FinishedAt})
	if result.Error != nil {
		return ErrVideoStorage
	}
	if result.RowsAffected != 1 {
		return ErrVideoConflict
	}
	return nil
}

// RecordTx is the durable video path into the existing summary table. It never
// queues or logs a database error. Callers pass the same transaction as billing.
// Unknown usage belongs in the video ledger, not a fabricated zero-token row.
func RecordTx(tx *gorm.DB, input *Invocation) error {
	if tx == nil || input == nil || input.ID != 0 || input.OrgID == 0 || !videoValidID(input.RequestID, false) || len(input.RequestID) > 32 ||
		input.Modality != ModalityVideo || !videoValidID(input.Model, false) || !videoValidID(input.Provider, true) ||
		!videoValidID(input.ErrorBrief, true) || !videoValidID(input.UpstreamErrorCode, true) || !videoValidID(input.UpstreamRequestID, true) ||
		!videoValidID(input.FinishReason, true) || !videoValidID(input.Protocol, true) || input.PayloadObjectKey != "" ||
		input.ClientIP != "" || input.UserAgent != "" || input.Referer != "" || input.PromptTokens < 0 || input.CompletionTokens < 0 ||
		input.CachedTokens < 0 || input.ReasoningTokens < 0 || input.CostUSD.IsNegative() {
		return ErrVideoInvalid
	}
	switch input.Status {
	case StatusSuccess, StatusUpstreamError, StatusNoChannel, StatusInsufficientBalance:
	default:
		return ErrVideoInvalid
	}
	if input.UsageSource != UsageSourceUpstream {
		return ErrVideoInvalid
	}
	row := *input
	row.CreatedAt = row.CreatedAt.UTC().Truncate(time.Millisecond)
	result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(&row)
	if result.Error != nil {
		return ErrVideoStorage
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var old Invocation
	if err := tx.Where("request_id = ? AND org_id = ?", input.RequestID, input.OrgID).Take(&old).Error; err != nil {
		return ErrVideoConflict
	}
	row.ID = 0
	old.ID = 0
	if input.CreatedAt.IsZero() {
		row.CreatedAt = old.CreatedAt
	}
	// Alias with explicit hidden fields ensures tenancy/channel metadata also
	// participates in comparison despite the public JSON projection omitting it.
	fingerprint := func(v Invocation) string {
		raw, _ := json.Marshal(struct {
			Public           Invocation
			OrgID, ChannelID uint64
			Provider         string
			Attempts         int
			Payload          string
		}{v, v.OrgID, v.ChannelID, v.Provider, v.Attempts, v.PayloadObjectKey})
		return string(raw)
	}
	if fingerprint(old) != fingerprint(row) {
		return ErrVideoConflict
	}
	return nil
}
