package videoartifact

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

type Worker struct {
	Repo            Repository
	Resolver        SourceResolver
	Downloader      *Downloader
	Blobs           BlobStore
	LeaseDuration   time.Duration
	TransferTimeout time.Duration
	MaxAttempts     int
	Now             func() time.Time
}

func (w Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

// Transfer is idempotent once ready. retry=false forbids silently repeating a
// failed or abandoned download. A scheduler must apply a bounded retry policy
// aware of upstream expiry/download limits before explicitly setting retry=true.
func (w Worker) Transfer(ctx context.Context, org uint64, task string, kind Kind, retry bool) (Record, error) {
	if w.Repo.DB == nil || w.Resolver == nil || w.Downloader == nil || w.Blobs == nil || w.TransferTimeout <= 0 || w.LeaseDuration <= w.TransferTimeout || w.MaxAttempts < 1 {
		return Record{}, failure("invalid_artifact_config", 503, false)
	}
	record, err := w.Repo.Lookup(ctx, org, task, kind, w.now())
	if err != nil {
		return Record{}, err
	}
	if record.Status == Ready {
		return record, nil
	}
	lease, err := w.Repo.Claim(ctx, org, task, kind, w.now(), w.LeaseDuration, retry, w.MaxAttempts)
	if err != nil {
		return Record{}, err
	}
	transferCtx, cancel := context.WithTimeout(ctx, w.TransferTimeout)
	defer cancel()
	fail := func(cause error) (Record, error) {
		// Request cancellation must not prevent recording the safe failure, but
		// this independent DB write also has a short bounded deadline.
		failCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if recordErr := w.Repo.Fail(failCtx, lease, cause, w.now()); recordErr != nil {
			return Record{}, recordErr
		}
		return Record{}, cause
	}
	source, err := w.Resolver.ResolveSource(transferCtx, org, task, kind)
	if err != nil {
		if errors.Is(err, ErrSourceDeferred) {
			deferCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			if e := w.Repo.Defer(deferCtx, lease, w.now()); e != nil {
				return Record{}, e
			}
			return Record{}, ErrSourceDeferred
		}
		return fail(failure("artifact_source_unavailable", 502, true))
	}
	download, err := w.Downloader.Fetch(transferCtx, source, kind)
	if err != nil {
		return fail(err)
	}
	defer download.Body.Close()
	if err = w.Blobs.Put(transferCtx, objectKey(lease), download.Body, download.Metadata); err != nil {
		return fail(failure("artifact_blob_unavailable", 503, true))
	}
	if err = w.Repo.Complete(transferCtx, lease, download.Metadata, w.now()); err != nil {
		// Do not delete on an ambiguous DB failure: the commit may have happened.
		// A lease-lost response is definite; its unique object cannot be current.
		var safe *Error
		if errors.As(err, &safe) && safe.Code == "artifact_lease_lost" {
			cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = w.Blobs.Delete(cleanupCtx, objectKey(lease))
			stop()
		}
		return Record{}, err
	}
	return w.Repo.Lookup(ctx, org, task, kind, w.now())
}

type Service struct {
	Repo  Repository
	Blobs BlobStore
}

// Open returns only platform bytes. HTTP handlers must authenticate org, close
// Body, set Content-Type/Length and Cache-Control, and never redirect upstream.
func (s Service) Open(ctx context.Context, org uint64, task string, kind Kind, now time.Time) (Content, error) {
	started := time.Now()
	now = now.UTC()
	if s.Blobs == nil {
		return Content{}, failure("invalid_artifact_config", 503, false)
	}
	record, err := s.Repo.Lookup(ctx, org, task, kind, now)
	if err != nil {
		return Content{}, err
	}
	if record.Status != Ready {
		return Content{}, failure("artifact_not_ready", 409, true)
	}
	body, err := s.Blobs.Open(ctx, record.ObjectKey)
	if err != nil {
		return Content{}, failure("artifact_blob_unavailable", 503, true)
	}
	// Recheck deletion/fence after opening storage, narrowing the race. A
	// deletion cannot revoke bytes already delivered by an in-flight reader.
	current, err := s.Repo.Lookup(ctx, org, task, kind, now.Add(time.Since(started)))
	if err != nil || current.Fence != record.Fence || current.Status != Ready {
		_ = body.Close()
		if err != nil {
			return Content{}, err
		}
		return Content{}, failure("artifact_not_ready", 409, true)
	}
	return Content{Body: body, Metadata: Metadata{MediaType: record.MediaType, Size: record.Size, SHA256: record.SHA256, Width: record.Width, Height: record.Height}, CacheControl: "private, no-store"}, nil
}

// Cleanup deletes only hidden/expired platform objects; repeated calls are
// safe. BlobStore.Delete must treat missing objects as success. Financial and
// task evidence are untouched. Orphan attempt keys need a storage lifecycle job.
func (s Service) Cleanup(ctx context.Context, org uint64, task string, kind Kind, now time.Time) error {
	now = now.UTC()
	if s.Repo.DB == nil || s.Blobs == nil || !validIdentity(org, task, kind) {
		return failure("not_found", 404, false)
	}
	var record Record
	query := scope(s.Repo.DB.WithContext(ctx), org, task, kind)
	if err := query.Take(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return failure("not_found", 404, false)
		}
		return failure("artifact_store_unavailable", 503, true)
	}
	if record.DeletedAt == nil && record.ExpiresAt.After(now) {
		return failure("invalid_state", 409, false)
	}
	if record.CleanedAt != nil {
		return nil
	}
	if record.ObjectKey != "" {
		if err := s.Blobs.Delete(ctx, record.ObjectKey); err != nil {
			return failure("artifact_blob_unavailable", 503, true)
		}
	}
	updates := map[string]any{"cleaned_at": now.UTC(), "lease_token": "", "lease_until": nil, "fence": record.Fence + 1, "updated_at": now.UTC()}
	if record.DeletedAt == nil {
		updates["status"] = Expired
	}
	res := scope(s.Repo.DB.WithContext(ctx), org, task, kind).Where("fence = ? AND cleaned_at IS NULL AND (deleted_at IS NOT NULL OR expires_at <= ?)", record.Fence, now).Updates(updates)
	if res.Error != nil {
		return failure("artifact_store_unavailable", 503, true)
	}
	if res.RowsAffected != 1 {
		return failure("artifact_lease_lost", 409, true)
	}
	return nil
}
