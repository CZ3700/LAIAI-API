package videoasset

import (
	"context"
	"strconv"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Pagination is read-only, but every attempt still needs policy-bound evidence.
// No provider page, signed URL, credential or cursor belongs in the sync row.
func (s *Service) recordSyncAttempt(ctx context.Context, job SyncJob, r RuntimeScope, action string, request []byte, result Result) error {
	if action != "ListAssetGroups" && action != "ListAssets" {
		return ErrInvalid
	}
	requestID := job.RequestID
	if requestID == "" {
		requestID = job.ID
	} // Correlate legacy jobs without inventing an HTTP request ID.
	attemptID := job.ID + "_" + strconv.FormatUint(job.Fence, 10)
	entries := []struct {
		direction string
		body      []byte
	}{
		{invocation.VideoEffectiveRequest, request},
		{invocation.VideoUpstreamResponse, result.Body},
	}
	prepared := make([]*invocation.PreparedVideoPayload, len(entries))
	for i, entry := range entries {
		if len(entry.body) == 0 {
			continue
		}
		representation := "effective_json_redacted"
		if entry.direction == invocation.VideoUpstreamResponse {
			representation = "response_json_redacted"
		}
		p, err := s.prepareHistoryEvidence(r, invocation.VideoPayloadInput{
			OrgID: job.OrgID, RequestID: requestID, AttemptID: attemptID, Direction: entry.direction,
			SchemaVersion: 1, Body: entry.body, SecretValues: []string{r.Credentials.AccessKey, r.Credentials.SecretKey, r.Credentials.SessionToken},
			CaptureRepresentation: representation,
		})
		if err != nil {
			return ErrStorage
		}
		prepared[i] = p
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, job.OrgID); err != nil {
			return err
		}
		var current SyncJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND org_id = ? AND fence = ? AND status = ?", job.ID, job.OrgID, job.Fence, "running").Take(&current).Error; err != nil {
			return ErrConflict
		}
		refs := make([]string, 2)
		for i, p := range prepared {
			if p == nil {
				continue
			}
			if _, err := s.evidence.StageTx(tx, p); err != nil {
				return ErrStorage
			}
			refs[i] = p.PayloadID()
			if err := s.event(tx, job.OrgID, "sync", job.ID, "", "sync_"+entries[i].direction, refs[i], "worker"); err != nil {
				return err
			}
		}
		return tx.Model(&SyncJob{}).Where("id = ? AND org_id = ? AND fence = ?", job.ID, job.OrgID, job.Fence).Updates(map[string]any{
			"attempts": gorm.Expr("attempts + 1"), "http_status": result.HTTPStatus, "upstream_request_id": result.RequestID,
			"last_request_payload_id": refs[0], "last_response_payload_id": refs[1], "updated_at": nowMillis(s),
		}).Error
	})
}

// This never handles a mutating operation. A page stops after 3 recorded transient
// failures. A lost worker may replay its read-only claim before recording a result;
// the durable cursor stays unchanged during backoff and fences reject late work.
func (s *Service) deferSyncFailure(ctx context.Context, job SyncJob, result Result) error {
	code := result.SafeErrorCode
	if code == "" {
		code = "provider_sync_failed"
	}
	updates := map[string]any{"status": "failed", "error_code": code, "lease_until": nil, "next_attempt_at": nil, "updated_at": nowMillis(s)}
	if result.Retryable && job.RetryCount < 2 {
		updates["status"] = "pending"
		updates["retry_count"] = job.RetryCount + 1
		updates["next_attempt_at"] = nowMillis(s).Add(time.Duration(5*(1<<job.RetryCount)) * time.Second)
	}
	return s.db.WithContext(ctx).Model(&SyncJob{}).Where("id = ? AND org_id = ? AND fence = ? AND status = ?", job.ID, job.OrgID, job.Fence, "running").Updates(updates).Error
}
