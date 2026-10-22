package videoasset

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

func TestOwnedSyncAllowsTheSameCursorInDifferentGroups(t *testing.T) {
	s, db, now, _ := setup(t)
	_ = s.ConfigureConsole(ConsoleOptions{})
	scope := testScope()
	for _, id := range []string{"ag_first", "ag_second"} {
		providerID := "official-" + id
		group := Group{ID: id, OrgID: 1, Kind: KindVirtual, Name: id, ScopeID: scope.ID, ScopeVersion: scope.Version,
			AccountID: scope.AccountID, Region: scope.Region, Project: scope.Project, ProviderID: &providerID,
			Status: StatusActive, ReviewState: ReviewClear, RowVersion: 1, CreatedAt: *now, UpdatedAt: *now}
		if err := db.Create(&group).Error; err != nil {
			t.Fatal(err)
		}
	}
	requests := map[string][]string{}
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("Action") == "ListAssetGroups" {
			return syncReply(req, map[string]any{"Items": []any{}}), nil
		}
		if req.URL.Query().Get("Action") != "ListAssets" {
			t.Fatal("sync dispatched an unexpected mutation")
		}
		var body struct {
			Filter                 struct{ GroupIds []string }
			NextToken, ProjectName string
		}
		if json.NewDecoder(req.Body).Decode(&body) != nil || body.ProjectName != scope.Project || len(body.Filter.GroupIds) != 1 {
			t.Fatal("invalid scoped asset request")
		}
		groupID := body.Filter.GroupIds[0]
		if groupID != "official-ag_first" && groupID != "official-ag_second" {
			t.Fatal("unowned group was queried")
		}
		requests[groupID] = append(requests[groupID], body.NextToken)
		suffix, next := "one", "page-two"
		if body.NextToken != "" {
			if body.NextToken != "page-two" {
				t.Fatal("unexpected pagination token")
			}
			suffix, next = "two", ""
		}
		asset := map[string]string{"Id": groupID + "-" + suffix, "Name": suffix, "GroupId": groupID,
			"ProjectName": scope.Project, "AssetType": "Image", "Status": "Active"}
		return syncReply(req, map[string]any{"Items": []any{asset}, "NextToken": next}), nil
	})})
	job, err := s.CreateSyncJob(context.Background(), adminContext("repeat-cursor"), KindVirtual, "owned")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := s.processSyncJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Second)
	}
	job, err = s.GetSyncJob(context.Background(), 1, job.ID, false)
	if err != nil || job.Status != "succeeded" || job.Pages != 5 || job.Items != 4 {
		t.Fatal("valid repeated cursor failed", job.Status, job.ErrorCode, job.Pages, job.Items, err)
	}
	for _, id := range []string{"official-ag_first", "official-ag_second"} {
		if got := requests[id]; len(got) != 2 || got[0] != "" || got[1] != "page-two" {
			t.Fatal("group cursor sequence was not independent", id, got)
		}
	}
	var assets int64
	if err := db.Model(&Asset{}).Where("org_id = ?", 1).Count(&assets).Error; err != nil || assets != 4 {
		t.Fatal("one group lost an asset page", assets, err)
	}
	var checkpoints []invocation.VideoPayload
	if err := db.Where("request_id = ? AND purpose = ?", job.ID, invocation.VideoPurposeExecution).Find(&checkpoints).Error; err != nil || len(checkpoints) != 2 {
		t.Fatal("per-claim cursor evidence missing", len(checkpoints), err)
	}
	if checkpoints[0].AttemptID == checkpoints[1].AttemptID {
		t.Fatal("cursor evidence correlations collide across groups")
	}
}

func TestOwnedSyncDoesNotApplyToGroupChangedDuringProviderCall(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		changes    func(time.Time) map[string]any
	}{
		{"deleting", "resource_mutation_in_progress", func(time.Time) map[string]any { return map[string]any{"status": StatusDeleting} }},
		{"deleted", "resource_mutation_in_progress", func(now time.Time) map[string]any { return map[string]any{"status": StatusDeleted, "deleted_at": now} }},
		{"project-changed", "resource_identity_mismatch", func(time.Time) map[string]any { return map[string]any{"project": "other-project"} }},
		{"provider-changed", "resource_identity_mismatch", func(time.Time) map[string]any { return map[string]any{"provider_id": "another-official-group"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, now, _ := setup(t)
			group := readyGroup(t, s, "late-"+tc.name)
			var original Group
			if err := db.Where("org_id = ? AND group_id = ?", 1, group.ID).Take(&original).Error; err != nil {
				t.Fatal(err)
			}
			_ = s.ConfigureConsole(ConsoleOptions{})
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("Action") == "ListAssetGroups" {
					return syncReply(req, map[string]any{"Items": []any{}}), nil
				}
				if req.URL.Query().Get("Action") != "ListAssets" {
					t.Fatal("unexpected mutation")
				}
				// Model a competing tenant operation committing after dispatch,
				// before the provider's stale asset page reaches the sync worker.
				if err := db.Transaction(func(tx *gorm.DB) error {
					if err := lockTenant(tx, 1); err != nil {
						return err
					}
					return tx.Model(&Group{}).Where("org_id = ? AND group_id = ?", 1, group.ID).Updates(tc.changes(*now)).Error
				}); err != nil {
					t.Fatal(err)
				}
				asset := map[string]string{"Id": "late-official-asset", "Name": "stale", "GroupId": *original.ProviderID,
					"ProjectName": original.Project, "AssetType": "Image", "Status": "Active"}
				return syncReply(req, map[string]any{"Items": []any{asset}, "NextToken": "late-next"}), nil
			})})
			job, err := s.CreateSyncJob(context.Background(), adminContext("late-sync-"+tc.name), KindVirtual, "owned")
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := s.processSyncJobs(context.Background()); err != nil {
					t.Fatal(err)
				}
				*now = now.Add(time.Second)
			}
			job, err = s.GetSyncJob(context.Background(), 1, job.ID, false)
			if err != nil || job.Status != "failed" || job.ErrorCode != tc.code || job.LeaseUntil != nil || job.Pages != 1 || job.Items != 0 || job.CheckpointID != "" {
				t.Fatal("stale group page was not rejected", job.Status, job.ErrorCode, job.Pages, err)
			}
			var assets, checkpoints int64
			if err := db.Model(&Asset{}).Where("org_id = ? AND group_id = ?", 1, group.ID).Count(&assets).Error; err != nil || assets != 0 {
				t.Fatal("stale response populated a changed group", assets, err)
			}
			if err := db.Model(&invocation.VideoPayload{}).Where("request_id = ? AND purpose = ?", job.ID, invocation.VideoPurposeExecution).Count(&checkpoints).Error; err != nil || checkpoints != 0 {
				t.Fatal("rejected page committed its cursor", checkpoints, err)
			}
		})
	}
}
