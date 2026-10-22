package videoasset

import (
	"context"
	"encoding/json"
	"testing"

	"gorm.io/gorm"
)

func TestConsoleOfficialStatusAndMediaEligibilityRemainIndependent(t *testing.T) {
	s, db, now, _ := setup(t)
	ctx := context.Background()
	group := readyGroup(t, s, "state-dimensions")
	ready := readyAsset(t, s, now, group.ID, "verified-state")
	var storedGroup Group
	if err := db.Where("org_id = ? AND group_id = ?", 1, group.ID).Take(&storedGroup).Error; err != nil {
		t.Fatal(err)
	}
	job := SyncJob{OrgID: 1, ScopeID: storedGroup.ScopeID, ScopeVersion: storedGroup.ScopeVersion}
	for _, official := range []string{"Active", "Processing", "Failed"} {
		item := upstreamAsset{ID: "official-" + official, GroupID: *storedGroup.ProviderID, ProjectName: storedGroup.Project, Name: official, AssetType: "Image", Status: official}
		if err := db.Transaction(func(tx *gorm.DB) error { return s.syncAssetTx(tx, job, storedGroup, item) }); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ConsoleAssets(ctx, 1, ListFilter{GroupID: group.ID, Limit: 20})
	if err != nil || page.Total != 4 {
		t.Fatal(page.Total, err)
	}
	var importedID string
	for _, asset := range page.Data {
		if asset.ID == ready.ID {
			if asset.ProviderStatus != "active" || asset.MediaVerificationStatus != "verified" || asset.GenerationStatus != "ready" || asset.GenerationBlockReason != "" || asset.Reference == "" {
				t.Fatal("verified reference lost its readiness", asset)
			}
			continue
		}
		if asset.MediaVerificationStatus != "unverified" {
			t.Fatal("official import invented local media verification", asset)
		}
		switch asset.Name {
		case "Active":
			importedID = asset.ID
			if asset.ProviderStatus != "active" || asset.Status != "ready" || asset.Stage != "ready" || asset.GenerationStatus != "ready" || asset.GenerationBlockReason != "" || asset.Reference == "" {
				t.Fatal("official Active image did not become referenceable", asset)
			}
		case "Processing":
			if asset.ProviderStatus != "processing" || asset.GenerationStatus != "blocked" || asset.Reference != "" || asset.GenerationBlockReason != "provider_not_ready" {
				t.Fatal("official processing state lost", asset)
			}
		case "Failed":
			if asset.ProviderStatus != "failed" || asset.GenerationStatus != "blocked" || asset.Reference != "" || asset.GenerationBlockReason != "provider_not_ready" {
				t.Fatal("official failed state lost", asset)
			}
		default:
			t.Fatal("unexpected asset")
		}
	}
	if importedID == "" {
		t.Fatal("missing official Active import")
	}
	if _, err := s.ResolveBindingsTx(db, 1, testScope(), "provider-image-task", []ReferenceInput{{AssetID: importedID, Role: "reference_image", ContentIndex: 0}}); err != nil {
		t.Fatal("official Active image was blocked by optional local verification", err)
	}
	for _, tc := range []struct {
		provider, legacy string
		want             int64
	}{
		{"active", "", 2}, {"processing", "", 1}, {"failed", "", 1}, {"unknown", "", 0},
		{"", "ready", 2}, {"active", "ready", 2}, {"active", "verifying", 0}, {"failed", "ready", 0},
	} {
		got, err := s.ConsoleAssets(ctx, 1, ListFilter{GroupID: group.ID, ProviderStatus: tc.provider, Status: tc.legacy, Limit: 20})
		if err != nil || got.Total != tc.want {
			t.Fatalf("filters provider=%q legacy=%q total=%d want=%d err=%v", tc.provider, tc.legacy, got.Total, tc.want, err)
		}
	}
	first, err := s.ConsoleAssets(ctx, 1, ListFilter{GroupID: group.ID, ProviderStatus: "active", Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatal("missing scoped official-status cursor", err)
	}
	if _, err := s.ConsoleAssets(ctx, 1, ListFilter{GroupID: group.ID, ProviderStatus: "failed", Limit: 1, Cursor: first.NextCursor}); err != ErrInvalid {
		t.Fatal("cursor crossed official-state filter", err)
	}
	other, err := s.ConsoleAssets(ctx, 2, ListFilter{GroupID: group.ID, ProviderStatus: "active", Limit: 20})
	if err != nil || other.Total != 0 {
		t.Fatal("official-state filter crossed tenant boundary", err)
	}
	if _, err := s.ConsoleAssets(ctx, 1, ListFilter{ProviderStatus: "ready", Limit: 20}); err != ErrInvalid {
		t.Fatal("legacy ready must not mean official Active", err)
	}
}

func TestConsoleUnknownDeletionOverridesPreviouslyActiveOfficialSnapshot(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	g := readyGroup(t, s, "unknown-state-group")
	a := readyAsset(t, s, now, g.ID, "unknown-state-asset")
	provider.unknown = true
	if _, _, err := s.DeleteAsset(ctx, createContext("uncertain-state-delete"), a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Preserve a legacy Active snapshot while the authoritative scoped deletion
	// receipt remains unknown. The console must not turn this into confirmation.
	if err := db.Model(&Asset{}).Where("org_id = ? AND asset_id = ?", 1, a.ID).Update("status", StatusActive).Error; err != nil {
		t.Fatal(err)
	}
	page, err := s.ConsoleAssets(ctx, 1, ListFilter{GroupID: g.ID, ProviderStatus: "unknown", Limit: 20})
	if err != nil || page.Total != 1 {
		t.Fatal("unknown deletion was omitted from unknown filter", page.Total, err)
	}
	asset := page.Data[0]
	if asset.ProviderStatus != "unknown" || asset.MediaVerificationStatus != "verified" || asset.GenerationStatus != "blocked" || asset.GenerationBlockReason != "submission_uncertain" || asset.DeleteOperation == nil || asset.DeleteOperation.Status != "unknown" {
		t.Fatal("unknown deletion was presented as confirmed availability", asset)
	}
	page, err = s.ConsoleAssets(ctx, 1, ListFilter{GroupID: g.ID, ProviderStatus: "active", Limit: 20})
	if err != nil || page.Total != 0 {
		t.Fatal("unknown deletion leaked into official Active filter", page.Total, err)
	}
	page, err = s.ConsoleAssets(ctx, 2, ListFilter{GroupID: g.ID, ProviderStatus: "unknown", Limit: 20})
	if err != nil || page.Total != 0 {
		t.Fatal("unknown-state OR predicates escaped the tenant boundary", page.Total, err)
	}
	if _, err := s.ResolveBindingsTx(db, 1, testScope(), "uncertain-delete-task", []ReferenceInput{{AssetID: a.ID, Role: "reference_image", ContentIndex: 0}}); err != ErrConflict {
		t.Fatal("unknown deletion bypassed generation barrier", err)
	}
}

func TestConsoleProviderStatusDoesNotChangeExistingCursorIdentity(t *testing.T) {
	legacy := struct {
		Kind, GroupID, Name, MediaType, Status, Cursor string
		Limit                                          int
	}{Kind: KindVirtual, GroupID: "ag_legacy", Status: "ready"}
	raw, _ := json.Marshal(legacy)
	current := filterIdentity(ListFilter{Kind: legacy.Kind, GroupID: legacy.GroupID, Status: legacy.Status, Limit: 20})
	if current != "console:"+hash(raw) {
		t.Fatal("new optional filter invalidated an in-flight legacy cursor")
	}
}
