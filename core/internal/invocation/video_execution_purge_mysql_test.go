package invocation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func purgeLegacyInput(s *VideoStore, request string, expires time.Time) VideoPayloadInput {
	in := videoInput(s)
	in.RequestID, in.ExpiresAt = request, expires
	return in
}

func purgeMetadata(t *testing.T, db *gorm.DB, p *PreparedVideoPayload) VideoPayload {
	t.Helper()
	var meta VideoPayload
	if err := db.Where("org_id = ? AND payload_id = ?", p.metadata.OrgID, p.PayloadID()).Take(&meta).Error; err != nil {
		t.Fatal(err)
	}
	return meta
}

func assertPurgedInput(t *testing.T, db *gorm.DB, p *PreparedVideoPayload) {
	t.Helper()
	meta := purgeMetadata(t, db, p)
	if meta.WrappedKey != "" || len(meta.Nonce) != 0 || meta.CaptureStatus != VideoCaptureExpired ||
		meta.CorrelationKey != p.metadata.CorrelationKey || !meta.CreatedAt.Equal(p.metadata.CreatedAt) || !meta.ExpiresAt.Equal(p.metadata.ExpiresAt) {
		t.Fatal("input was not erased while preserving immutable metadata")
	}
	var n int64
	if err := db.Model(&VideoPayloadStaging{}).Where("payload_id = ?", p.PayloadID()).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("staging not erased", err)
	}
}

func TestMySQLVideoRetentionAndTTLPurge(t *testing.T) {
	s, db, now := videoExecutionMySQLStore(t)
	ctx := context.Background()
	expires := now.Add(300 * time.Millisecond)
	in := purgeLegacyInput(s, "disabled-history", expires)
	in.Purpose, in.Direction, in.CaptureEnabled = VideoPurposeHistory, VideoClientRequest, false
	disabled := videoStage(t, s, db, in)
	p := videoStage(t, s, db, purgeLegacyInput(s, "live-execution", expires))
	*now = now.Add(time.Hour)
	videoExecutionWaitUntil(t, db, expires)
	if plain, err := s.ReadForExecution(ctx, 1, p.PayloadID(), videoAccess(VideoRoleWorker)); !errors.Is(err, ErrVideoExpired) || plain != nil {
		t.Fatal("expired input readable")
	}
	if n, err := s.PurgeExpired(ctx, 100); err != nil || n != 2 {
		t.Fatalf("mixed purpose purge: %d %v", n, err)
	}
	assertPurgedInput(t, db, p)
	assertPurgedInput(t, db, disabled)
	if n, err := s.PurgeExpired(ctx, 100); err != nil || n != 0 {
		t.Fatalf("repeat purge: %d %v", n, err)
	}
}

func TestMySQLVideoBlobRestartSafeUploadAndPurge(t *testing.T) {
	s, db, now := videoExecutionMySQLStore(t)
	ctx := context.Background()
	blobs := &videoTestBlobs{objects: map[string][]byte{}, failPut: true}
	s.blobs = blobs
	p := videoStage(t, s, db, purgeLegacyInput(s, "blob-restart", now.Add(5*time.Second)))
	if _, err := s.UploadPending(ctx, "worker", 10); !errors.Is(err, ErrVideoStorage) {
		t.Fatal("ambiguous Put did not fail", err)
	}
	if _, err := s.ReadForExecution(ctx, 1, p.PayloadID(), videoAccess(VideoRoleWorker)); err != nil {
		t.Fatal("ambiguous Put lost SQL staging", err)
	}
	// Advance only the mutable retry schedule, not the encrypted identity or
	// the authority clock. This avoids waiting the existing one-minute retry.
	q := db.Model(&VideoPayloadStaging{}).Where("payload_id = ? AND fencing_token = ? AND lease_owner = ? AND attempts = ?", p.PayloadID(), 1, "", 1).
		Update("next_attempt_at", now.Add(-time.Second))
	if q.Error != nil || q.RowsAffected != 1 {
		t.Fatal("exact upload retry schedule", q.Error)
	}
	restarted, err := NewVideoStore(db, s.keyring, VideoStoreOptions{Now: func() time.Time { return *now }, BlobStore: blobs})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := restarted.UploadPending(ctx, "restarted-worker", 10); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	if blobs.puts != 2 || len(blobs.objects) != 1 {
		t.Fatal("idempotent object key not used")
	}
	if _, err := restarted.ReadForExecution(ctx, 1, p.PayloadID(), videoAccess(VideoRoleWorker)); err != nil {
		t.Fatal("stored ciphertext unreadable", err)
	}
	var count int64
	if err := db.Model(&VideoPayloadStaging{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("staging not ACKed", err)
	}
	videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
	before := videoExecutionDatabaseNow(t, db)
	if n, err := restarted.PurgeExpired(ctx, 10); err != nil || n != 1 {
		t.Fatal("execution purge", n, err)
	}
	after := videoExecutionDatabaseNow(t, db)
	assertPurgedInput(t, db, p)
	meta := purgeMetadata(t, db, p)
	grace := videoUploadLease + videoBlobTimeout
	if meta.CleanupAfter == nil || meta.CleanupAfter.Before(before.Add(grace)) || meta.CleanupAfter.After(after.Add(grace)) || len(blobs.objects) != 1 {
		t.Fatal("primary-clock upload grace not retained")
	}
	*now = now.Add(24 * time.Hour)
	if _, err := restarted.PurgeExpired(ctx, 10); err != nil || len(blobs.objects) != 1 {
		t.Fatal("host clock bypassed execution grace", err)
	}
	// This is the required natural 150-second grace observation, not a
	// cleanup_after rewrite or host-clock fixture.
	videoExecutionWaitUntil(t, db, *meta.CleanupAfter)
	blobs.failDelete = true
	if _, err := restarted.PurgeExpired(ctx, 10); !errors.Is(err, ErrVideoStorage) {
		t.Fatal("Delete failure not retryable", err)
	}
	if purgeMetadata(t, db, p).ObjectDeletedAt != nil || len(blobs.objects) != 1 {
		t.Fatal("failed Delete recorded as complete")
	}
	before = videoExecutionDatabaseNow(t, db)
	if n, err := restarted.PurgeExpired(ctx, 10); err != nil || n != 0 {
		t.Fatal("cleanup retry", n, err)
	}
	after = videoExecutionDatabaseNow(t, db)
	meta = purgeMetadata(t, db, p)
	if len(blobs.objects) != 0 || meta.ObjectDeletedAt == nil || meta.ObjectDeletedAt.Before(before) || meta.ObjectDeletedAt.After(after) {
		t.Fatal("object cleanup did not use the primary clock")
	}
}

func TestMySQLVideoUploadFenceAndExpiry(t *testing.T) {
	for _, scenario := range []string{"stolen_fence", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, now := videoExecutionMySQLStore(t)
			blobs := &videoTestBlobs{objects: map[string][]byte{}}
			s.blobs = blobs
			p := videoStage(t, s, db, purgeLegacyInput(s, scenario, now.Add(time.Second)))
			ctx := context.Background()
			blobs.afterPut = func() {
				if scenario == "expired" {
					videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
					if n, err := s.PurgeExpired(ctx, 10); err != nil || n != 1 {
						t.Fatal("Purge during Put", n, err)
					}
				} else {
					q := db.Model(&VideoPayloadStaging{}).Where("payload_id = ? AND fencing_token = ? AND lease_owner = ?", p.PayloadID(), 1, "old-worker").Update("fencing_token", 99)
					if q.Error != nil || q.RowsAffected != 1 {
						t.Fatal("steal exact upload fence", q.Error)
					}
				}
			}
			if n, err := s.UploadPending(ctx, "old-worker", 10); err != nil || n != 0 {
				t.Fatal("stale worker committed", n, err)
			}
			meta := purgeMetadata(t, db, p)
			if meta.CaptureStatus == VideoCaptureStored {
				t.Fatal("stale worker ACK")
			}
			if scenario == "expired" {
				assertPurgedInput(t, db, p)
				if meta.CleanupAfter == nil || len(blobs.objects) != 1 {
					t.Fatal("cleanup grace missing")
				}
				// Only this cleanup retry fixture advances a mutable tombstone
				// schedule. The preceding test proves the natural grace duration.
				q := db.Model(&VideoPayload{}).Where("org_id = ? AND payload_id = ? AND capture_status = ? AND cleanup_after = ? AND object_deleted_at IS NULL", meta.OrgID, meta.PayloadID, VideoCaptureExpired, *meta.CleanupAfter).
					Update("cleanup_after", videoExecutionDatabaseNow(t, db).Add(-time.Second))
				if q.Error != nil || q.RowsAffected != 1 {
					t.Fatal("exact cleanup retry schedule", q.Error)
				}
				if _, err := s.PurgeExpired(ctx, 10); err != nil || len(blobs.objects) != 0 {
					t.Fatal("post-expiry upload orphan", err)
				}
			}
		})
	}
}

func TestMySQLVideoExecutionPurgeClockFailurePreservesRow(t *testing.T) {
	s, db, now := videoExecutionMySQLStore(t)
	p := videoStage(t, s, db, purgeLegacyInput(s, "clock-failure", now.Add(200*time.Millisecond)))
	videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
	var clocks int
	const callback = "test:purge-primary-clock-failure"
	if err := db.Callback().Row().Before("gorm:row").Register(callback, func(q *gorm.DB) {
		if strings.Contains(q.Statement.SQL.String(), "SELECT UTC_TIMESTAMP(3) AS database_time") {
			clocks++
			if clocks == 2 {
				q.Statement.SQL.Reset()
				q.Statement.SQL.WriteString("SELECT private_missing_clock_column AS database_time")
				q.Statement.Vars = nil
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Row().Remove(callback) })
	if n, err := s.PurgeExpired(context.Background(), 1); n != 0 || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) || strings.Contains(err.Error(), "private") {
		t.Fatal("clock failure not safe", n, err)
	}
	if clocks != 2 {
		t.Fatal("lock-after-clock fault was not reached", clocks)
	}
	meta := purgeMetadata(t, db, p)
	if meta.WrappedKey != p.metadata.WrappedKey || string(meta.Nonce) != string(p.metadata.Nonce) || meta.CaptureStatus != VideoCaptureStaged || meta.CleanupAfter != nil {
		t.Fatal("clock failure erased metadata")
	}
	var n int64
	if err := db.Model(&VideoPayloadStaging{}).Where("payload_id = ?", p.PayloadID()).Count(&n).Error; err != nil || n != 1 {
		t.Fatal("clock failure erased staging", err)
	}
}

type purgeCommitCancelPool struct {
	*sql.DB
	commits int
	cancel  context.CancelFunc
}

func (p *purgeCommitCancelPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &purgeCommitCancelTx{Tx: tx, pool: p}, nil
}

type purgeCommitCancelTx struct {
	*sql.Tx
	pool *purgeCommitCancelPool
}

func (tx *purgeCommitCancelTx) Commit() error {
	err := tx.Tx.Commit()
	if err == nil {
		tx.pool.commits++
		if tx.pool.commits == 2 {
			tx.pool.cancel()
		}
	}
	return err
}

func TestMySQLVideoExecutionPurgeCountsCommittedCancellation(t *testing.T) {
	s, db, now := videoExecutionMySQLStore(t)
	p := videoStage(t, s, db, purgeLegacyInput(s, "cancel-after-commit", now.Add(200*time.Millisecond)))
	videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &purgeCommitCancelPool{DB: pool, cancel: cancel}
	wrappedDB := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	wrappedDB.ConnPool, wrappedDB.Statement.ConnPool = wrapper, wrapper
	s.db = wrappedDB
	// Candidate SELECT commits first; actual key/staging erasure commits
	// second. Cancellation then prevents the next partition, not that count.
	n, err := s.PurgeExpired(ctx, 1)
	if n != 1 || !errors.Is(err, context.Canceled) || wrapper.commits != 2 {
		t.Fatal("committed erasure count lost after cancellation", n, err, wrapper.commits)
	}
	assertPurgedInput(t, db, p)
}

func TestMySQLVideoExecutionPurgePartitions(t *testing.T) {
	t.Run("host_skew_and_exact_purpose", func(t *testing.T) {
		s, db, now := videoExecutionMySQLStore(t)
		live := videoStage(t, s, db, purgeLegacyInput(s, "live-db-window", now.Add(time.Hour)))
		var other []*PreparedVideoPayload
		for i, purpose := range []string{VideoPurposeHistory, "Execution", "execution "} {
			in := purgeLegacyInput(s, fmt.Sprintf("purpose-%d", i), now.Add(time.Hour))
			in.Purpose, in.Direction = VideoPurposeHistory, VideoClientRequest
			p := videoStage(t, s, db, in)
			if purpose != VideoPurposeHistory {
				// Deliberately malformed historical purpose metadata, not a valid
				// execution identity. The privacy job must use exact byte routing.
				q := db.Model(&VideoPayload{}).Where("org_id = ? AND payload_id = ?", p.metadata.OrgID, p.PayloadID()).Update("purpose", purpose)
				if q.Error != nil || q.RowsAffected != 1 {
					t.Fatal("create unknown-purpose fixture", q.Error)
				}
			}
			other = append(other, p)
		}
		*now = now.Add(-24 * time.Hour)
		if n, err := s.PurgeExpired(context.Background(), 10); err != nil || n != 0 {
			t.Fatal("slow host erased live data", n, err)
		}
		*now = now.Add(48 * time.Hour)
		if n, err := s.PurgeExpired(context.Background(), 10); err != nil || n != 3 {
			t.Fatal("exact purpose partition", n, err)
		}
		meta := purgeMetadata(t, db, live)
		if meta.CaptureStatus != VideoCaptureStaged || meta.WrappedKey != live.metadata.WrappedKey || meta.CleanupAfter != nil {
			t.Fatal("fast host erased a live primary-clock input")
		}
		for _, p := range other {
			assertPurgedInput(t, db, p)
		}
	})
	t.Run("two_bounded_batches_and_unknown_schema", func(t *testing.T) {
		s, db, now := videoExecutionMySQLStore(t)
		expires := now.Add(500 * time.Millisecond)
		var inputs []*PreparedVideoPayload
		for _, purpose := range []string{VideoPurposeHistory, VideoPurposeExecution} {
			for i := range 3 {
				in := purgeLegacyInput(s, fmt.Sprintf("%s-%d", purpose, i), expires)
				in.Purpose = purpose
				if purpose == VideoPurposeHistory {
					in.Direction = VideoClientRequest
				} else if i == 2 {
					in.SchemaVersion = 99 // encrypted as unknown before staging
				}
				inputs = append(inputs, videoStage(t, s, db, in))
			}
		}
		*now = now.Add(time.Hour)
		videoExecutionWaitUntil(t, db, expires)
		if n, err := s.PurgeExpired(context.Background(), 2); err != nil || n != 4 {
			t.Fatal("partition bound", n, err)
		}
		for _, purpose := range []string{VideoPurposeExecution, VideoPurposeHistory} {
			var count int64
			if err := db.Model(&VideoPayload{}).Where("BINARY purpose = BINARY ? AND capture_status = ?", purpose, VideoCaptureExpired).Count(&count).Error; err != nil || count != 2 {
				t.Fatal("one purpose starved the other", count, err)
			}
		}
		if n, err := s.PurgeExpired(context.Background(), 2); err != nil || n != 2 {
			t.Fatal("remaining bounded batch", n, err)
		}
		for _, p := range inputs {
			assertPurgedInput(t, db, p)
		}
		if n, err := s.PurgeExpired(context.Background(), 2); err != nil || n != 0 {
			t.Fatal("idempotent bounded batch", n, err)
		}
	})
}

func TestMySQLVideoExecutionPurgeUsesClockAfterLock(t *testing.T) {
	s, db, now := videoExecutionMySQLStore(t)
	p := videoStage(t, s, db, purgeLegacyInput(s, "lock-clock", now.Add(200*time.Millisecond)))
	videoExecutionWaitUntil(t, db, p.metadata.ExpiresAt)
	blocker := db.Begin()
	if blocker.Error != nil {
		t.Fatal(blocker.Error)
	}
	defer blocker.Rollback()
	var locked VideoPayload
	if err := blocker.Raw("SELECT * FROM invocation_payloads WHERE org_id = ? AND payload_id = ? FOR UPDATE", p.metadata.OrgID, p.PayloadID()).Scan(&locked).Error; err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	const callback = "test:purge-row-lock"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "invocation_payloads" {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				select {
				case entered <- struct{}{}:
				default:
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(callback)
	type result struct {
		count int64
		err   error
	}
	done := make(chan result, 1)
	go func() { n, err := s.PurgeExpired(context.Background(), 1); done <- result{n, err} }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Purge lock query not reached")
	}
	select {
	case <-done:
		t.Fatal("Purge bypassed the held metadata lock")
	case <-time.After(150 * time.Millisecond):
	}
	releasedAt := videoExecutionDatabaseNow(t, db)
	if err := blocker.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.count != 1 {
			t.Fatal("Purge after lock", outcome.count, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Purge did not finish after lock release")
	}
	meta := purgeMetadata(t, db, p)
	if meta.CleanupAfter == nil || meta.CleanupAfter.Before(releasedAt.Add(videoUploadLease+videoBlobTimeout)) {
		t.Fatal("Purge reused time from before its lock wait")
	}
	assertPurgedInput(t, db, p)
}
