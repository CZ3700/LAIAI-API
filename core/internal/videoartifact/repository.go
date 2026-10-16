package videoartifact

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ DB *gorm.DB }

var taskPattern = regexp.MustCompile(`^task_[A-Za-z0-9_-]{1,123}$`)

func validIdentity(org uint64, task string, kind Kind) bool {
	return org != 0 && taskPattern.MatchString(task) && (kind == Video || kind == LastFrame)
}

func scope(db *gorm.DB, org uint64, task string, kind Kind) *gorm.DB {
	return db.Model(&Record{}).Where("org_id = ? AND task_id = ? AND kind = ?", org, task, kind)
}

// RegisterTx is idempotent within the successful-task transaction. It does not
// extend retention or resurrect deleted/expired outputs on a repeated event.
func (r Repository) RegisterTx(tx *gorm.DB, spec Spec, now time.Time) (Record, error) {
	now = now.UTC()
	if tx == nil || !validIdentity(spec.OrgID, spec.TaskID, spec.Kind) || !spec.ExpiresAt.After(now) || (spec.Width != nil && *spec.Width <= 0) || (spec.Height != nil && *spec.Height <= 0) {
		return Record{}, failure("invalid_artifact", 422, false)
	}
	record := Record{OrgID: spec.OrgID, TaskID: spec.TaskID, Kind: spec.Kind, Status: Pending, ExpiresAt: spec.ExpiresAt.UTC(), Width: spec.Width, Height: spec.Height, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error; err != nil {
		return Record{}, failure("artifact_store_unavailable", 503, true)
	}
	var existing Record
	if err := scope(tx, spec.OrgID, spec.TaskID, spec.Kind).Take(&existing).Error; err != nil {
		return Record{}, failure("artifact_store_unavailable", 503, true)
	}
	return existing, nil
}

func (r Repository) Lookup(ctx context.Context, org uint64, task string, kind Kind, now time.Time) (Record, error) {
	now = now.UTC()
	if r.DB == nil || !validIdentity(org, task, kind) {
		return Record{}, failure("not_found", 404, false)
	}
	var record Record
	err := scope(r.DB.WithContext(ctx), org, task, kind).Where("deleted_at IS NULL").Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Record{}, failure("not_found", 404, false)
	}
	if err != nil {
		return Record{}, failure("artifact_store_unavailable", 503, true)
	}
	if !record.ExpiresAt.After(now) || record.Status == Expired {
		return record, failure("artifact_expired", 410, false)
	}
	return record, nil
}

// Claim obtains an atomic lease. Retry must be explicitly authorized after a
// failed attempt or an expired in-flight lease: either may already have consumed
// an upstream download. An expired lease alone never triggers another GET.
func (r Repository) Claim(ctx context.Context, org uint64, task string, kind Kind, now time.Time, ttl time.Duration, retry bool, maxAttempts int) (Lease, error) {
	now = now.UTC()
	if ttl <= 0 || maxAttempts < 1 {
		return Lease{}, failure("invalid_artifact_config", 503, false)
	}
	record, err := r.Lookup(ctx, org, task, kind, now)
	if err != nil {
		return Lease{}, err
	}
	if record.Status == Ready {
		return Lease{}, failure("artifact_already_ready", 409, false)
	}
	if record.LeaseUntil != nil && record.LeaseUntil.After(now) {
		return Lease{}, failure("artifact_busy", 409, true)
	}
	if record.Attempts >= maxAttempts {
		return Lease{}, failure("artifact_attempt_limit", 409, false)
	}
	if record.Attempts > 0 && !retry {
		return Lease{}, failure("artifact_retry_required", 409, false)
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return Lease{}, failure("artifact_store_unavailable", 503, true)
	}
	token := hex.EncodeToString(tokenBytes[:])
	until := now.Add(ttl).UTC()
	query := scope(r.DB.WithContext(ctx), org, task, kind).Where("deleted_at IS NULL AND expires_at > ? AND status IN ? AND fence = ? AND attempts < ? AND (lease_until IS NULL OR lease_until <= ?)", now, []Status{Pending, Failed}, record.Fence, maxAttempts, now)
	result := query.Updates(map[string]any{"status": Pending, "fence": gorm.Expr("fence + 1"), "attempts": gorm.Expr("attempts + 1"), "lease_token": token, "lease_until": until, "error_code": "", "updated_at": now.UTC()})
	if result.Error != nil {
		return Lease{}, failure("artifact_store_unavailable", 503, true)
	}
	if result.RowsAffected != 1 {
		return Lease{}, failure("artifact_busy", 409, true)
	}
	record.Fence++
	record.Attempts++
	record.LeaseToken = token
	record.LeaseUntil = &until
	record.Status = Pending
	return Lease{Record: record, Token: token, Fence: record.Fence}, nil
}

func leaseScope(db *gorm.DB, lease Lease, now time.Time) *gorm.DB {
	return scope(db, lease.Record.OrgID, lease.Record.TaskID, lease.Record.Kind).Where("deleted_at IS NULL AND status = ? AND fence = ? AND lease_token = ? AND lease_until > ? AND expires_at > ?", Pending, lease.Fence, lease.Token, now, now)
}

func objectKey(lease Lease) string {
	return fmt.Sprintf("video-artifacts/%d/%s/%s/%d-%s", lease.Record.OrgID, lease.Record.TaskID, lease.Record.Kind, lease.Fence, lease.Token)
}

func (r Repository) Complete(ctx context.Context, lease Lease, metadata Metadata, now time.Time) error {
	now = now.UTC()
	hash, hashErr := hex.DecodeString(metadata.SHA256)
	if metadata.Size <= 0 || hashErr != nil || len(hash) != 32 || !allowedMIME(lease.Record.Kind, metadata.MediaType) || (metadata.Width != nil && *metadata.Width <= 0) || (metadata.Height != nil && *metadata.Height <= 0) {
		return failure("invalid_artifact", 422, false)
	}
	updates := map[string]any{"status": Ready, "object_key": objectKey(lease), "size": metadata.Size, "sha256": metadata.SHA256, "media_type": metadata.MediaType, "lease_token": "", "lease_until": nil, "error_code": "", "updated_at": now.UTC()}
	if metadata.Width != nil {
		updates["width"] = metadata.Width
	}
	if metadata.Height != nil {
		updates["height"] = metadata.Height
	}
	res := leaseScope(r.DB.WithContext(ctx), lease, now).Updates(updates)
	if res.Error != nil {
		return failure("artifact_store_unavailable", 503, true)
	}
	if res.RowsAffected != 1 {
		return failure("artifact_lease_lost", 409, false)
	}
	return nil
}

// Fail stores only module-defined error codes. It never changes task or money.
func (r Repository) Fail(ctx context.Context, lease Lease, cause error, now time.Time) error {
	now = now.UTC()
	code := "artifact_transfer_failed"
	var safe *Error
	if errors.As(cause, &safe) && safeFailureCodes[safe.Code] {
		code = safe.Code
	}
	res := leaseScope(r.DB.WithContext(ctx), lease, now).Updates(map[string]any{"status": Failed, "error_code": code, "lease_token": "", "lease_until": nil, "updated_at": now.UTC()})
	if res.Error != nil {
		return failure("artifact_store_unavailable", 503, true)
	}
	if res.RowsAffected != 1 {
		return failure("artifact_lease_lost", 409, false)
	}
	return nil
}

// Defer returns an unused attempt only for a proven pre-dispatch scheduling
// deferral. The fence/token prevent an old resolver from refunding a new lease.
func (r Repository) Defer(ctx context.Context, lease Lease, now time.Time) error {
	now = now.UTC()
	res := leaseScope(r.DB.WithContext(ctx), lease, now).Where("attempts = ? AND attempts > 0", lease.Record.Attempts).Updates(map[string]any{"status": Pending, "attempts": gorm.Expr("attempts - 1"), "lease_token": "", "lease_until": nil, "error_code": "", "updated_at": now})
	if res.Error != nil {
		return failure("artifact_store_unavailable", 503, true)
	}
	if res.RowsAffected != 1 {
		return failure("artifact_lease_lost", 409, false)
	}
	return nil
}

var safeFailureCodes = map[string]bool{"artifact_source_unavailable": true, "artifact_download_failed": true, "artifact_download_timeout": true, "artifact_url_blocked": true, "artifact_too_large": true, "artifact_invalid_media": true, "artifact_blob_unavailable": true, "artifact_transfer_failed": true}

// HideTaskTx must run in the SAME transaction as task soft deletion. It
// invalidates every lease before asynchronous physical cleanup is scheduled.
func (r Repository) HideTaskTx(tx *gorm.DB, org uint64, task string, now time.Time) error {
	now = now.UTC()
	if tx == nil || org == 0 || !taskPattern.MatchString(task) {
		return failure("not_found", 404, false)
	}
	res := tx.Model(&Record{}).Where("org_id = ? AND task_id = ? AND deleted_at IS NULL", org, task).Updates(map[string]any{"deleted_at": now.UTC(), "fence": gorm.Expr("fence + 1"), "lease_token": "", "lease_until": nil, "updated_at": now.UTC()})
	if res.Error != nil {
		return failure("artifact_store_unavailable", 503, true)
	}
	return nil
}
