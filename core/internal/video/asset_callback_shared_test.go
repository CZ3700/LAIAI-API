package video

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoasset"
)

// The console return page is the only URL eligible for shared-default
// fallback resolution; tenant-specific pages stay per-organization.
const sharedTestConsoleReturn = "https://console.example/assets/return"

func sharedCallbackFixture(t *testing.T) (*Service, *identity.User) {
	t.Helper()
	s, admin := sharedAssetFixture(t)
	for _, statement := range []string{
		`CREATE TABLE video_asset_tenant_locks(org_id INTEGER PRIMARY KEY, version INTEGER)`,
		`ALTER TABLE video_asset_callback_registry ADD COLUMN evidence_ref TEXT`,
		`ALTER TABLE video_asset_callback_registry ADD COLUMN created_by INTEGER`,
		`ALTER TABLE video_asset_callback_registry ADD COLUMN created_at DATETIME`,
	} {
		if err := s.DB.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Real fail-closed resolvers: the fixture stubs t.Fatal on callback
	// resolution, so replace the console service via ConfigureAssets.
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.Keyring = kr
	s.Evidence = &invocation.VideoStore{}
	// ConsoleCapabilities resolves the runtime with credentials, so the seeded
	// scope config needs a credential cipher this keyring can actually open.
	cipher, err := s.assetSeal("management", 0, "10", 7, map[string]string{
		"Account": "1234567890", "Region": "cn-beijing", "Project": "default",
		"AccessKey": "fixture-access-key", "SecretKey": "fixture-secret-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`UPDATE video_asset_scope_configs SET credential_cipher=? WHERE provider_scope_id=10 AND version=7`, cipher).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureAssets(AssetIntegrationOptions{
		PlatformCallbackURL: "https://gateway.example" + videoasset.PortraitCallbackPath,
		ConsoleReturnURL:    sharedTestConsoleReturn,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Assets.ConfigureConsole(videoasset.ConsoleOptions{CallbackURL: sharedTestConsoleReturn}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAssetCallback(context.Background(), admin, AssetCallbackRegistration{
		ID: "shared-console-return", OrgID: 1, URL: sharedTestConsoleReturn,
		ExpiresAt: s.Now().UTC().Add(180 * 24 * time.Hour), EvidenceRef: "evidence-shared-return",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAssetSharedDefault(context.Background(), admin, AssetSharedDefaultInput{ProviderScopeID: 10, ScopeVersion: 7, Enabled: true, AllowAsync: true}); err != nil {
		t.Fatal(err)
	}
	return s, admin
}

// The regression behind the fix: a plain tenant (no explicit route, policy or
// callback registration) could not create real-person asset groups because
// consolePortraitReady demanded a per-org callback row. With the shared
// default enabled, the superadmin-registered console return page resolves for
// every tenant.
func TestConsolePortraitReadyForTenantViaSharedCallback(t *testing.T) {
	s, _ := sharedCallbackFixture(t)
	if err := s.DB.Exec(`DELETE FROM video_asset_org_routes WHERE org_id=2; DELETE FROM video_org_policies WHERE org_id=2;`).Error; err != nil {
		t.Fatal(err)
	}
	caps := s.Assets.ConsoleCapabilities(context.Background(), 2)
	if !caps.Ready || caps.RouteSource != "shared_default" {
		t.Fatalf("tenant runtime not inherited: %+v", caps)
	}
	if !caps.Portrait {
		t.Fatalf("portrait capability blocked despite shared callback: %+v", caps)
	}
}

// The fallback rides on the shared default: flipping the singleton toggles
// tenant portrait readiness even though route and policy stay explicit, so
// the callback gate is observable in isolation.
func TestSharedCallbackFallbackRequiresSharedDefault(t *testing.T) {
	s, _ := sharedCallbackFixture(t)
	if err := s.DB.Exec(`UPDATE video_asset_org_routes SET provider_scope_id=10, scope_version=7 WHERE org_id=2; UPDATE video_org_policies SET async_execution_allowed=1 WHERE org_id=2;`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`UPDATE video_asset_shared_defaults SET enabled=0, allow_async=0 WHERE id=1`).Error; err != nil {
		t.Fatal(err)
	}
	caps := s.Assets.ConsoleCapabilities(context.Background(), 2)
	if !caps.Ready || caps.RouteSource != "organization" {
		t.Fatalf("explicit route should stay ready: %+v", caps)
	}
	if caps.Portrait {
		t.Fatalf("shared callback resolved while shared default disabled: %+v", caps)
	}
	if err := s.DB.Exec(`UPDATE video_asset_shared_defaults SET enabled=1, allow_async=1 WHERE id=1`).Error; err != nil {
		t.Fatal(err)
	}
	if caps = s.Assets.ConsoleCapabilities(context.Background(), 2); !caps.Portrait {
		t.Fatalf("portrait still blocked after enabling shared default: %+v", caps)
	}
}

// Another tenant's registered return page must never resolve for a caller
// that did not ask for the platform console page: the fallback skips foreign
// URLs instead of failing, so the shared row still serves tenants while the
// foreign URL itself stays exclusive to the owning tenant.
func TestSharedCallbackFallbackNeverExposesTenantURLs(t *testing.T) {
	s, admin := sharedCallbackFixture(t)
	if err := s.DB.Exec(`DELETE FROM video_asset_org_routes WHERE org_id=2; DELETE FROM video_org_policies WHERE org_id=2;`).Error; err != nil {
		t.Fatal(err)
	}
	foreign := "https://org1.example/return"
	if err := s.RegisterAssetCallback(context.Background(), admin, AssetCallbackRegistration{
		ID: "org1-custom-return", OrgID: 1, URL: foreign,
		ExpiresAt: s.Now().UTC().Add(180 * 24 * time.Hour), EvidenceRef: "evidence-org1-return",
	}); err != nil {
		t.Fatal(err)
	}
	if caps := s.Assets.ConsoleCapabilities(context.Background(), 2); !caps.Portrait {
		t.Fatalf("foreign row shadowed the shared console page: %+v", caps)
	}
	if _, err := s.resolveAssetCallbackURL(context.Background(), 2, foreign); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign URL resolved cross-tenant: %v", err)
	}
	if got, err := s.resolveAssetCallbackURL(context.Background(), 1, foreign); err != nil || got.URL != foreign {
		t.Fatalf("own registration broken: %+v %v", got, err)
	}
}

func TestSharedCallbackByIDFallback(t *testing.T) {
	s, _ := sharedCallbackFixture(t)
	if err := s.DB.Exec(`DELETE FROM video_asset_org_routes WHERE org_id=2; DELETE FROM video_org_policies WHERE org_id=2;`).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.resolveAssetCallbackID(context.Background(), 2, "shared-console-return")
	if err != nil || got.URL != sharedTestConsoleReturn {
		t.Fatalf("by-id fallback failed: %+v %v", got, err)
	}
	if _, err := s.resolveAssetCallbackID(context.Background(), 2, "missing-callback"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unknown id resolved: %v", err)
	}
}
