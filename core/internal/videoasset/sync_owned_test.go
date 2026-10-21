package videoasset

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These fixtures use official ListAssetGroups/ListAssets envelopes and exact
// project/group filters. MediaStore is intentionally absent: listing official
// metadata must not require downloading, scanning or approving the media.
func exerciseOwnedSync(t *testing.T, s *Service, db *gorm.DB, now *time.Time, scope Scope, requireNullVerification bool) {
	t.Helper()
	ctx := context.Background()
	groupID, err := newID("ag_")
	if err != nil {
		t.Fatal(err)
	}
	providerID := "group-fixture-owned"
	g := Group{ID: groupID, OrgID: 1, Kind: KindVirtual, Name: "gg", ScopeID: scope.ID, ScopeVersion: scope.Version,
		AccountID: scope.AccountID, Region: scope.Region, Project: scope.Project, ProviderID: &providerID,
		Status: StatusActive, ReviewState: ReviewClear, RowVersion: 1, CreatedAt: *now, UpdatedAt: *now}
	if err := db.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	unknownID, err := newID("ag_")
	if err != nil {
		t.Fatal(err)
	}
	unknown := g
	unknown.ID, unknown.Name, unknown.ProviderID, unknown.Status, unknown.ReviewState = unknownID, "kk", nil, StatusPending, ReviewRequired
	if err := db.Create(&unknown).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureConsole(ConsoleOptions{}); err != nil {
		t.Fatal(err)
	}
	var calls []string
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		action := req.URL.Query().Get("Action")
		var body struct {
			Filter struct {
				GroupType string
				GroupIds  []string
			}
			ProjectName, SortBy, SortOrder, NextToken string
			MaxResults                                int
			PageNumber, PageSize                      *int
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Filter.GroupType != "AIGC" || body.ProjectName != scope.Project || body.SortBy != "CreateTime" || body.SortOrder != "Asc" || body.MaxResults != 100 || body.PageNumber != nil || body.PageSize != nil {
			t.Fatal("sync request does not match scoped official cursor contract")
		}
		calls = append(calls, action+":"+body.NextToken)
		switch action {
		case "ListAssetGroups":
			if len(body.Filter.GroupIds) != 0 {
				t.Fatal("unexpected group filter")
			}
			if body.NextToken == "" {
				return syncReply(req, map[string]any{"Items": []any{map[string]string{"Id": providerID, "Name": "gg", "GroupType": "AIGC", "ProjectName": scope.Project}}, "NextToken": "groups-next"}), nil
			}
			if body.NextToken != "groups-next" {
				t.Fatal("wrong group checkpoint")
			}
			// Same name as the unknown local creation is never identity evidence.
			return syncReply(req, map[string]any{"Items": []any{map[string]string{"Id": "group-fixture-external", "Name": "kk", "GroupType": "AIGC", "ProjectName": scope.Project}}}), nil
		case "ListAssets":
			if len(body.Filter.GroupIds) != 1 || body.Filter.GroupIds[0] != providerID {
				t.Fatal("asset request escaped the owned group")
			}
			asset := map[string]any{"Id": "asset-fixture-one", "Name": "first", "GroupId": providerID, "ProjectName": scope.Project, "AssetType": "Image", "Status": "Active", "URL": "https://private.example/source?signature=must-stay-private"}
			if body.NextToken == "" {
				return syncReply(req, map[string]any{"Items": []any{asset}, "NextToken": "assets-next"}), nil
			}
			if body.NextToken != "assets-next" {
				t.Fatal("group checkpoint leaked into asset pagination")
			}
			asset["Id"], asset["Name"] = "asset-fixture-two", "second"
			return syncReply(req, map[string]any{"Items": []any{asset}}), nil
		default:
			t.Fatal("metadata sync issued a mutation or media retrieval", action)
			return nil, ErrInvalid
		}
	})})
	caller := createContext("owned-sync-" + scope.ID)
	job, err := s.CreateSyncJob(ctx, caller, "virtual", "owned")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 8; attempt++ {
		if err := s.processSyncJobs(ctx); err != nil {
			t.Fatalf("owned sync page failed before metadata import: %v", err)
		}
		job, err = s.GetSyncJob(ctx, 1, job.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "succeeded" {
			break
		}
		if job.Status == "failed" {
			t.Fatal("owned sync failed", job.Phase, job.ErrorCode)
		}
		*now = now.Add(time.Second)
	}
	if job.Status != "succeeded" || job.Pages != 4 || job.Items != 3 {
		t.Fatal("metadata sync did not complete expected pages", job.Status, job.Pages, job.Items)
	}
	wantCalls := "ListAssetGroups:,ListAssetGroups:groups-next,ListAssets:,ListAssets:assets-next"
	if strings.Join(calls, ",") != wantCalls {
		t.Fatal("cursor/phase sequence changed", calls)
	}
	page, err := s.ConsoleAssets(ctx, 1, ListFilter{GroupID: g.ID, Limit: 20})
	if err != nil || page.Total != 2 || len(page.Data) != 2 {
		t.Fatal("official assets missing from console", page.Total, err)
	}
	for _, asset := range page.Data {
		if asset.Stage != "needs_review" || asset.Reference != "" || asset.PreviewURL != "" || asset.Source != "imported" || asset.DeletionMode != "official" {
			t.Fatal("sync either fabricated media verification or blocked deletion", asset.Stage, asset.Reference, asset.DeletionMode)
		}
		if asset.ProviderStatus != "active" || asset.MediaVerificationStatus != "unverified" || asset.GenerationStatus != "blocked" || asset.GenerationBlockReason != "media_unverified" {
			t.Fatal("official import did not preserve independent availability and verification", asset.ProviderStatus, asset.MediaVerificationStatus, asset.GenerationStatus)
		}
		var row Asset
		if err := db.Where("org_id = ? AND asset_id = ?", 1, asset.ID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if !row.VerifiedAt.IsZero() || row.MetadataVersion != 0 || row.ImmutableObjectRef != "" || hasMediaFacts(row) {
			t.Fatal("metadata import invented media verification")
		}
		if requireNullVerification {
			var nullCount int64
			if err := db.Model(&Asset{}).Where("asset_id = ? AND verified_at IS NULL", asset.ID).Count(&nullCount).Error; err != nil || nullCount != 1 {
				t.Fatal("unverified imported asset must store SQL NULL rather than a zero date", err)
			}
		}
		if _, err := s.ResolveBindingsTx(db, 1, scope, "task-unverified-"+asset.ID, []ReferenceInput{{AssetID: asset.ID, Role: "reference_image", ContentIndex: 0}}); err != ErrConflict {
			t.Fatal("unverified official import became eligible for generation", err)
		}
	}
	var unbound Group
	if err := db.Where("group_id = ?", unknown.ID).Take(&unbound).Error; err != nil || unbound.ProviderID != nil || unbound.ReviewState != ReviewRequired {
		t.Fatal("same-name unknown creation was claimed", err)
	}
	other, err := s.ConsoleAssets(ctx, 2, ListFilter{GroupID: g.ID, Limit: 20})
	if err != nil || other.Total != 0 {
		t.Fatal("import crossed tenant scope", err)
	}
	var discoveries int64
	if err := db.Model(&Discovery{}).Where("scope_id = ?", scope.ID).Count(&discoveries).Error; err != nil || discoveries != 0 {
		t.Fatal("owned sync imported an unassigned group", err)
	}
	// Imported media can still be removed while local upload/verification is
	// intentionally disabled. Only the exact scoped official asset is deleted.
	var toDelete Asset
	if err := db.Where("asset_id = ? AND org_id = ?", page.Data[0].ID, 1).Take(&toDelete).Error; err != nil {
		t.Fatal(err)
	}
	deletes := 0
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var body struct{ ID, ProjectName string }
		if json.NewDecoder(req.Body).Decode(&body) != nil || req.URL.Query().Get("Action") != "DeleteAsset" || body.ID != *toDelete.ProviderID || body.ProjectName != scope.Project {
			t.Fatal("wrong asset deletion identity")
		}
		deletes++
		return syncReply(req, map[string]any{}), nil
	})})
	operation, done, err := s.DeleteAsset(ctx, createContext("delete-imported-"+scope.ID), toDelete.ID)
	if err != nil || done {
		t.Fatal("imported asset deletion blocked by absent media service", err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err = s.GetOperation(ctx, 1, operation.ID)
	if err != nil || operation.Status != "confirmed" || deletes != 1 {
		t.Fatal("imported asset deletion did not complete", operation.Status, deletes, err)
	}
	if _, err := s.GetAsset(ctx, 1, toDelete.ID); err != ErrNotFound {
		t.Fatal("deleted asset remained visible", err)
	}
}

func TestOwnedSyncImportsTwoOfficialAssetsWithoutMediaStorage(t *testing.T) {
	s, db, now, _ := setup(t)
	exerciseOwnedSync(t, s, db, now, testScope(), false)
}

func TestOwnedSyncRejectsZeroDateRegression(t *testing.T) {
	s, db, now, _ := setup(t)
	// Mirror MySQL's minimum valid DATE range without relaxing the production
	// NO_ZERO_DATE guard. SQL NULL denotes that media verification never ran.
	if err := db.Exec(`CREATE TRIGGER reject_invalid_asset_verification_date BEFORE INSERT ON video_assets
		WHEN NEW.verified_at IS NOT NULL AND substr(CAST(NEW.verified_at AS TEXT), 1, 4) < '1000'
		BEGIN SELECT RAISE(ABORT, 'invalid verification date'); END`).Error; err != nil {
		t.Fatal(err)
	}
	exerciseOwnedSync(t, s, db, now, testScope(), true)
}

func TestOwnedSyncPersistenceFailureStopsWithoutLeaseLoop(t *testing.T) {
	s, db, now, _ := setup(t)
	group := readyGroup(t, s, "storage-failure-group")
	var row Group
	if err := db.Where("group_id = ?", group.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_sync_asset_insert BEFORE INSERT ON video_assets BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	_ = s.ConfigureConsole(ConsoleOptions{})
	calls := 0
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("Action") == "ListAssetGroups" {
			return syncReply(req, map[string]any{"Items": []any{}}), nil
		}
		return syncReply(req, map[string]any{"Items": []any{map[string]string{"Id": "asset-storage-failure", "Name": "fixture", "GroupId": *row.ProviderID, "ProjectName": row.Project, "AssetType": "Image", "Status": "Active"}}}), nil
	})})
	job, err := s.CreateSyncJob(context.Background(), createContext("storage-failure"), "virtual", "owned")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.processSyncJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if err := s.processSyncJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err = s.GetSyncJob(context.Background(), 1, job.ID, false)
	if err != nil || job.Status != "failed" || job.ErrorCode != "sync_storage_failed" || job.LeaseUntil != nil || job.Pages != 1 {
		t.Fatal("failed import retained an active lease", job.Status, job.ErrorCode, err)
	}
	var count int64
	if err := db.Model(&Asset{}).Where("org_id = ? AND group_id = ?", 1, group.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("partial import escaped transaction", err)
	}
	*now = now.Add(3 * time.Minute)
	if err := s.processSyncJobs(context.Background()); err != nil || calls != 2 {
		t.Fatal("permanent DB error repeatedly queried provider", calls, err)
	}
}

func TestMySQLOwnedAssetSyncPreservesUnknownVerification(t *testing.T) {
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN is required for real MySQL integration")
	}
	if !strings.Contains(dsn, "/ark_test?") {
		t.Fatal("integration requires the dedicated ark_test database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot open dedicated MySQL test database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var sqlMode string
	if err := db.Raw("SELECT @@SESSION.sql_mode").Scan(&sqlMode).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlMode, "NO_ZERO_DATE") || !strings.Contains(sqlMode, "STRICT_") {
		t.Fatal("test requires MySQL strict mode with NO_ZERO_DATE")
	}
	s, _, now, _ := setup(t)
	s.db = db
	s.evidence, err = invocation.NewVideoStore(db, s.keys, invocation.VideoStoreOptions{Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	scope := testScope()
	scope.ID, err = newID("sync_scope_")
	if err != nil {
		t.Fatal(err)
	}
	scope.AccountID = scope.ID
	original := s.options.ResolveScope
	s.options.ResolveScope = func(ctx context.Context, org uint64, requested Scope) (RuntimeScope, error) {
		if requested.ID != "" && !sameScope(requested, scope) {
			return RuntimeScope{}, ErrUnsupported
		}
		r, err := original(ctx, org, testScope())
		r.Scope = scope
		return r, err
	}
	exerciseOwnedSync(t, s, db, now, scope, true)
}
