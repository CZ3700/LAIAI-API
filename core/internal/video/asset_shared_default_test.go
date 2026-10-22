package video

import (
	"context"
	"errors"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/videoasset"
)

func sharedAssetFixture(t *testing.T) (*Service, *identity.User) {
	t.Helper()
	s, actor := assetSetupFixture(t)
	seedAssetSetup(t, s)
	if err := s.DB.Exec(`CREATE TABLE video_asset_events(event_id TEXT PRIMARY KEY,org_id INTEGER,resource_type TEXT,resource_id TEXT,operation_id TEXT,kind TEXT,evidence_ref TEXT,actor_id TEXT,created_at DATETIME);`).Error; err != nil {
		t.Fatal(err)
	}
	return s, actor
}

func TestAssetSharedDefaultPublicationIsExplicitAuditedAndCAS(t *testing.T) {
	s, admin := sharedAssetFixture(t)
	ctx := context.Background()
	in := AssetSharedDefaultInput{ProviderScopeID: 10, ScopeVersion: 7, Enabled: true, AllowAsync: true}
	if err := s.SetAssetSharedDefault(ctx, &identity.User{ID: 12, Role: identity.PlatRoleAdmin, Status: identity.StatusActive}, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-superadmin published: %v", err)
	}
	if err := s.SetAssetSharedDefault(ctx, admin, in); err != nil {
		t.Fatal(err)
	}
	var row AssetSharedDefault
	if err := s.DB.First(&row, 1).Error; err != nil || row.Revision != 1 || !row.Enabled || row.UpdatedBy != admin.ID {
		t.Fatalf("publication not persisted: %+v %v", row, err)
	}
	if err := s.SetAssetSharedDefault(ctx, admin, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale overwrite accepted: %v", err)
	}
	var count int64
	s.DB.Model(&videoasset.Event{}).Where("kind = ?", "shared_default_published").Count(&count)
	if count != 1 {
		t.Fatalf("audit count %d", count)
	}
	in.ExpectedRevision, in.Enabled = 1, false
	if err := s.SetAssetSharedDefault(ctx, admin, in); err != nil {
		t.Fatal(err)
	}
	s.DB.First(&row, 1)
	if row.Enabled || row.AllowAsync || row.Revision != 2 {
		t.Fatalf("disable did not revoke default: %+v", row)
	}
}

func TestAssetSharedDefaultInheritanceNeverOverridesExplicitTenantDecision(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		ready        bool
		source       string
	}{
		{"inherits", "DELETE FROM video_asset_org_routes WHERE org_id=1; DELETE FROM video_org_policies WHERE org_id=1", true, "shared_default"},
		{"organization_override", "", true, "organization"},
		{"disabled_route", "UPDATE video_asset_org_routes SET enabled=0 WHERE org_id=1", false, "organization"},
		{"disabled_policy", "DELETE FROM video_asset_org_routes WHERE org_id=1; UPDATE video_org_policies SET async_execution_allowed=0 WHERE org_id=1", false, "shared_default"},
		{"inactive_organization", "DELETE FROM video_asset_org_routes WHERE org_id=1; UPDATE organizations SET status=0 WHERE id=1", false, "shared_default"},
		{"disabled_default", "DELETE FROM video_asset_org_routes WHERE org_id=1; UPDATE video_asset_shared_defaults SET enabled=0", false, "organization"},
		{"disabled_config", "DELETE FROM video_asset_org_routes WHERE org_id=1; UPDATE video_asset_scope_configs SET enabled=0 WHERE provider_scope_id=10", false, "shared_default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, admin := sharedAssetFixture(t)
			if err := s.SetAssetSharedDefault(context.Background(), admin, AssetSharedDefaultInput{ProviderScopeID: 10, ScopeVersion: 7, Enabled: true, AllowAsync: true}); err != nil {
				t.Fatal(err)
			}
			if tc.change != "" {
				if err := s.DB.Exec(tc.change).Error; err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.assetScopeTx(s.DB, 1, 0, videoasset.Scope{}, false)
			ready := err == nil && got.ExecutionRetentionConsent
			if ready != tc.ready {
				t.Fatalf("runtime ready=%v expected=%v error=%v", ready, tc.ready, err)
			}
			view, err := s.AssetSetup(context.Background(), admin, 1)
			if err != nil || view.ConfigurationReady != tc.ready || view.RouteSource != tc.source {
				t.Fatalf("setup disagrees: %+v %v", view, err)
			}
			if tc.name == "inherits" {
				if got.CaptureHistory || got.RetentionDays != 1 || got.RouteSource != "shared_default" || view.Policy.Present {
					t.Fatalf("inherited extra consent: %+v", got)
				}
				var policies int64
				s.DB.Table("video_org_policies").Where("org_id=1").Count(&policies)
				if policies != 0 {
					t.Fatal("asset default implicitly changed video consent")
				}
			}
		})
	}
}

func TestAssetSharedDefaultDoesNotRedirectPinnedResources(t *testing.T) {
	s, admin := sharedAssetFixture(t)
	if err := s.SetAssetSharedDefault(context.Background(), admin, AssetSharedDefaultInput{ProviderScopeID: 10, ScopeVersion: 7, Enabled: true, AllowAsync: true}); err != nil {
		t.Fatal(err)
	}
	s.DB.Exec("DELETE FROM video_asset_org_routes WHERE org_id=1")
	first, err := s.assetScopeTx(s.DB, 1, 0, videoasset.Scope{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAssetSharedDefault(context.Background(), admin, AssetSharedDefaultInput{ExpectedRevision: 1, ProviderScopeID: 20, ScopeVersion: 3, Enabled: true, AllowAsync: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.assetScopeTx(s.DB, 1, 0, first.Scope, false); !errors.Is(err, ErrCapability) {
		t.Fatalf("pinned resource redirected: %v", err)
	}
	current, err := s.assetScopeTx(s.DB, 1, 0, videoasset.Scope{}, false)
	if err != nil || current.Scope.Project != "second" {
		t.Fatalf("new creation default wrong: %+v %v", current, err)
	}
}
