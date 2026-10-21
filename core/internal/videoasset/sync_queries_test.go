package videoasset

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestLatestSyncJobScopeIsolationAndOrdering(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	base := SyncJob{ID: "asj_current", OrgID: 1, ScopeID: "scope1", ScopeVersion: 1, AccountID: "account1", Region: "cn-beijing", Project: "project1", Mode: "owned", Kind: "virtual", Phase: "assets", Status: "succeeded", IdempotencyHash: "current", CreatedAt: *now, UpdatedAt: *now}
	if err := db.Create(&base).Error; err != nil {
		t.Fatal(err)
	}
	for i, change := range []func(*SyncJob){
		func(j *SyncJob) { j.OrgID = 2 },
		func(j *SyncJob) { j.ScopeID = "other-scope" },
		func(j *SyncJob) { j.ScopeVersion = 2 },
		func(j *SyncJob) { j.AccountID = "other-account" },
		func(j *SyncJob) { j.Region = "cn-shanghai" },
		func(j *SyncJob) { j.Project = "other-project" },
		func(j *SyncJob) { j.Mode = "discover" },
		func(j *SyncJob) { j.Kind = "real_person" },
	} {
		row := base
		row.ID, row.IdempotencyHash = fmt.Sprintf("asj_foreign_%d", i), fmt.Sprintf("foreign-%d", i)
		row.CreatedAt = now.Add(time.Minute)
		change(&row)
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LatestSyncJob(ctx, 1, "virtual")
	if err != nil || got == nil || got.ID != base.ID {
		t.Fatalf("latest leaked another scope or kind: job=%+v error=%v", got, err)
	}
	// Timestamp takes precedence over lexical ID, then ID resolves ties.
	for _, input := range []struct {
		id string
		at time.Time
	}{
		{"asj_zzz_older", now.Add(-time.Minute)},
		{"asj_yyy_same_time", *now},
	} {
		row := base
		row.ID, row.IdempotencyHash, row.CreatedAt = input.id, input.id, input.at
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.LatestSyncJob(ctx, 1, "virtual")
	if err != nil || got == nil || got.ID != "asj_yyy_same_time" {
		t.Fatalf("wrong latest ordering: job=%+v error=%v", got, err)
	}
	got, err = s.LatestSyncJob(ctx, 3, "virtual")
	if err != nil || got != nil {
		t.Fatalf("empty org received a job: %+v %v", got, err)
	}
	if len(provider.calls) != 0 {
		t.Fatal("reading sync progress contacted the provider")
	}
}

func TestLatestSyncJobReturnsTerminalAndActiveStates(t *testing.T) {
	for _, status := range []string{"pending", "running", "succeeded", "failed", "needs_review"} {
		t.Run(status, func(t *testing.T) {
			s, db, now, _ := setup(t)
			row := SyncJob{ID: "asj_latest", OrgID: 1, ScopeID: "scope1", ScopeVersion: 1, AccountID: "account1", Region: "cn-beijing", Project: "project1", Mode: "owned", Kind: "real_person", Phase: "assets", Status: status, ErrorCode: "provider_unavailable", IdempotencyHash: "latest", CreatedAt: *now, UpdatedAt: *now}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			got, err := s.LatestSyncJob(context.Background(), 1, "real_person")
			if err != nil || got == nil || got.ID != row.ID || got.Status != status || got.ErrorCode != row.ErrorCode {
				t.Fatalf("lost state: job=%+v error=%v", got, err)
			}
		})
	}
}

func TestLatestSyncJobInvalidUnavailableAndStorageFailure(t *testing.T) {
	s, db, _, _ := setup(t)
	for _, input := range []struct {
		org  uint64
		kind string
	}{{0, "virtual"}, {1, ""}, {1, "real"}, {1, "discover"}, {1, "Virtual"}} {
		if got, err := s.LatestSyncJob(context.Background(), input.org, input.kind); !errors.Is(err, ErrInvalid) || got != nil {
			t.Fatalf("invalid input accepted: %+v %v", got, err)
		}
	}
	resolver := s.options.ResolveScope
	s.options.ResolveScope = func(context.Context, uint64, Scope) (RuntimeScope, error) { return RuntimeScope{}, ErrUnsupported }
	if got, err := s.LatestSyncJob(context.Background(), 1, "virtual"); !errors.Is(err, ErrUnsupported) || got != nil {
		t.Fatalf("missing route must fail closed: %+v %v", got, err)
	}
	s.options.ResolveScope = resolver
	if err := db.Exec("DROP TABLE video_asset_sync_jobs").Error; err != nil {
		t.Fatal(err)
	}
	if got, err := s.LatestSyncJob(context.Background(), 1, "virtual"); !errors.Is(err, ErrStorage) || got != nil {
		t.Fatalf("storage failure treated as empty: %+v %v", got, err)
	}
}

func TestLatestSyncJobRejectsCaseInsensitiveIdentityMatch(t *testing.T) {
	s, db, _, _ := setup(t)
	// Model a deployment whose text columns have a case-insensitive collation.
	if err := db.Exec(`DROP TABLE video_asset_sync_jobs;
CREATE TABLE video_asset_sync_jobs(id TEXT PRIMARY KEY,org_id INTEGER,scope_id TEXT,scope_version INTEGER,account_id TEXT,region TEXT,project TEXT COLLATE NOCASE,mode TEXT,kind TEXT,created_at DATETIME);
INSERT INTO video_asset_sync_jobs VALUES('asj_mismatch',1,'scope1',1,'account1','cn-beijing','PROJECT1','owned','virtual','2026-10-22 00:00:00');`).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := s.LatestSyncJob(context.Background(), 1, "virtual"); !errors.Is(err, ErrStorage) || got != nil {
		t.Fatalf("SQL collation weakened scope identity: %+v %v", got, err)
	}
}
