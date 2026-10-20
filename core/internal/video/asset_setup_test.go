package video

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Only the existing columns read by the diagnostic are mirrored. No credential
// resolver, encryption key, management client or mutation repository is needed.
const assetSetupSchema = `
CREATE TABLE organizations(id INTEGER PRIMARY KEY, status INTEGER NOT NULL);
CREATE TABLE video_provider_scopes(id INTEGER PRIMARY KEY,account_ref TEXT,region TEXT,project_name TEXT,enabled INTEGER);
CREATE TABLE video_asset_scope_configs(provider_scope_id INTEGER,version INTEGER,account_ref TEXT,region TEXT,project_name TEXT,capabilities_json TEXT,credential_cipher TEXT,enabled INTEGER,PRIMARY KEY(provider_scope_id,version));
CREATE TABLE video_asset_org_routes(org_id INTEGER,provider_scope_id INTEGER,scope_version INTEGER,enabled INTEGER,default_route INTEGER,PRIMARY KEY(org_id,provider_scope_id));
CREATE TABLE video_org_policies(org_id INTEGER PRIMARY KEY,async_execution_allowed INTEGER,capture_history INTEGER,retention_days INTEGER);
CREATE TABLE video_asset_callback_registry(id TEXT PRIMARY KEY,org_id INTEGER,url_hmac TEXT,value_cipher TEXT,expires_at DATETIME,revoked_at DATETIME);
INSERT INTO organizations VALUES(1,1),(2,1),(3,0);
`

func assetSetupFixture(t *testing.T) (*Service, *identity.User) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.Exec(assetSetupSchema).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s := &Service{DB: db, Secret: []byte(strings.Repeat("s", 32)), Now: func() time.Time { return now }}
	assets, err := videoasset.NewService(db, &crypto.Keyring{}, &invocation.VideoStore{}, videoasset.Options{
		Now: s.Now, ActiveHMACKey: "setup", HMACKeys: map[string][]byte{"setup": []byte(strings.Repeat("h", 32))},
		PlatformCallbackURL: "https://gateway.example" + videoasset.PortraitCallbackPath,
		ResolveCallback: func(context.Context, uint64, string) (videoasset.CallbackTarget, error) {
			t.Fatal("setup must not decrypt callback registry values")
			return videoasset.CallbackTarget{}, videoasset.ErrSecret
		},
		ResolveCallbackByID: func(context.Context, uint64, string) (videoasset.CallbackTarget, error) {
			t.Fatal("setup must not decrypt callback registry values")
			return videoasset.CallbackTarget{}, videoasset.ErrSecret
		},
		ResolveScope: func(context.Context, uint64, videoasset.Scope) (videoasset.RuntimeScope, error) {
			t.Fatal("read-only setup must not resolve or decrypt runtime credentials")
			return videoasset.RuntimeScope{}, videoasset.ErrSecret
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = assets.ConfigureConsole(videoasset.ConsoleOptions{}); err != nil {
		t.Fatal(err)
	}
	s.Assets = assets
	return s, &identity.User{ID: 11, Role: identity.PlatRoleSuperadmin, Status: identity.StatusActive}
}

func seedAssetSetup(t *testing.T, s *Service) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO video_provider_scopes VALUES(10,'1234567890','cn-beijing','default',1),(20,'2234567890','cn-beijing','second',1)`,
		`INSERT INTO video_asset_scope_configs VALUES(10,7,'1234567890','cn-beijing','default','{"VirtualGroups":true,"Images":true,"Portrait":true}','secret-cipher-never-return',1)`,
		`INSERT INTO video_asset_scope_configs VALUES(20,3,'2234567890','cn-beijing','second','{"VirtualGroups":true}','other-secret-never-return',1)`,
		`INSERT INTO video_asset_org_routes VALUES(1,10,7,1,1),(2,20,3,1,1)`,
		`INSERT INTO video_org_policies VALUES(1,1,0,30),(2,0,1,7)`,
	} {
		if err := s.DB.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func setupHasIssue(out AssetSetupView, want string) bool {
	for _, issue := range out.Issues {
		if issue == want {
			return true
		}
	}
	return false
}

func TestAssetSetupSafeProjectionIsReadOnlyAndOrgScoped(t *testing.T) {
	s, actor := assetSetupFixture(t)
	seedAssetSetup(t, s)
	// SQLite rejects every write, including raw SQL, even inside the read snapshot.
	if err := s.DB.Exec("PRAGMA query_only = ON").Error; err != nil {
		t.Fatal(err)
	}
	out, err := s.AssetSetup(context.Background(), actor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !out.ConfigurationReady || out.OfficialAccess != "not_checked" || len(out.ProviderScopes) != 2 || len(out.ScopeConfigs) != 2 {
		t.Fatalf("safe local configuration projection = %+v", out)
	}
	if len(out.OrgRoutes) != 1 || out.OrgRoutes[0].OrgID != 1 || out.OrgRoutes[0].ScopeVersion != 7 {
		t.Fatal("another org route leaked")
	}
	if !out.Policy.Present || !out.Policy.AsyncExecutionAllowed || out.Policy.CaptureHistory || out.Policy.RetentionDays != 30 {
		t.Fatal("wrong organization policy")
	}
	if !out.ScopeConfigs[0].CredentialPresent || !out.ScopeConfigs[0].IdentityMatchesProvider || !out.ScopeConfigs[0].Capabilities.Images {
		t.Fatal("missing safe presence projection")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-cipher", "other-secret", "credential_cipher", "access_key", "secret_key", "session_token", "url_hmac", "value_cipher", "https://"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("unsafe serialized field: %s", forbidden)
		}
	}
	other, err := s.AssetSetup(context.Background(), actor, 2)
	if err != nil || other.ConfigurationReady || !setupHasIssue(other, "async_execution_disabled") || len(other.OrgRoutes) != 1 || other.OrgRoutes[0].ProviderScopeID != 20 {
		t.Fatal("org-specific readiness missing", err)
	}
}

func TestAssetSetupMissingDependenciesAndStaleBindings(t *testing.T) {
	for _, tc := range []struct{ name, update, issue string }{
		{"config_without_route", "DELETE FROM video_asset_org_routes WHERE org_id=1", "org_default_route_missing"},
		{"scope_version_missing", "UPDATE video_asset_org_routes SET scope_version=8 WHERE org_id=1", "routed_scope_config_missing"},
		{"provider_missing", "DELETE FROM video_provider_scopes WHERE id=10", "routed_provider_missing"},
		{"provider_disabled", "UPDATE video_provider_scopes SET enabled=0 WHERE id=10", "routed_provider_disabled"},
		{"provider_region", "UPDATE video_provider_scopes SET region='ap-southeast-1' WHERE id=10", "routed_provider_invalid"},
		{"config_disabled", "UPDATE video_asset_scope_configs SET enabled=0 WHERE provider_scope_id=10", "routed_scope_config_disabled"},
		{"identity_changed", "UPDATE video_asset_scope_configs SET account_ref='changed' WHERE provider_scope_id=10", "scope_identity_mismatch"},
		{"credential_missing", "UPDATE video_asset_scope_configs SET credential_cipher='' WHERE provider_scope_id=10", "management_credentials_missing"},
		{"credential_null", "UPDATE video_asset_scope_configs SET credential_cipher=NULL WHERE provider_scope_id=10", "management_credentials_missing"},
		{"capabilities_invalid", "UPDATE video_asset_scope_configs SET capabilities_json='null' WHERE provider_scope_id=10", "scope_capabilities_invalid"},
		{"policy_missing", "DELETE FROM video_org_policies WHERE org_id=1", "execution_policy_missing"},
		{"policy_disabled", "UPDATE video_org_policies SET async_execution_allowed=0 WHERE org_id=1", "async_execution_disabled"},
		{"org_disabled", "UPDATE organizations SET status=0 WHERE id=1", "organization_inactive"},
		{"ambiguous_default", "INSERT INTO video_asset_org_routes VALUES(1,20,3,1,1)", "org_default_route_ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, actor := assetSetupFixture(t)
			seedAssetSetup(t, s)
			if err := s.DB.Exec(tc.update).Error; err != nil {
				t.Fatal(err)
			}
			out, err := s.AssetSetup(context.Background(), actor, 1)
			if err != nil || out.ConfigurationReady || !setupHasIssue(out, tc.issue) {
				t.Fatalf("got %+v err=%v", out, err)
			}
		})
	}
}

func TestAssetSetupEmptyGatewayAndAccessBoundary(t *testing.T) {
	s, actor := assetSetupFixture(t)
	s.Assets = nil
	out, err := s.AssetSetup(context.Background(), actor, 1)
	if err != nil || out.ConfigurationReady || out.ProviderScopes == nil || out.ScopeConfigs == nil || out.OrgRoutes == nil || !setupHasIssue(out, "gateway_unavailable") || !setupHasIssue(out, "provider_scopes_missing") || !setupHasIssue(out, "management_scope_configs_missing") {
		t.Fatal("empty setup must remain diagnosable", err)
	}
	for _, denied := range []*identity.User{nil, {ID: 0, Role: identity.PlatRoleSuperadmin, Status: 1}, {ID: 12, Role: identity.PlatRoleAdmin, Status: 1}, {ID: 11, Role: identity.PlatRoleSuperadmin, Status: 0}} {
		if _, err := s.AssetSetup(context.Background(), denied, 1); !errors.Is(err, ErrForbidden) {
			t.Fatal("unauthorized setup inspection", err)
		}
	}
	if _, err := s.AssetSetup(context.Background(), actor, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.AssetSetup(context.Background(), actor, 999); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.DB.Exec("DROP TABLE video_asset_scope_configs").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssetSetup(context.Background(), actor, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("schema errors must fail closed", err)
	}
}

func TestAssetSetupPortraitRegistryRequiresSameOrgURLAndLiveRecord(t *testing.T) {
	for _, variation := range []string{"valid", "wrong_org", "wrong_url", "expired", "revoked", "empty_record", "disabled_policy", "scope_identity"} {
		t.Run(variation, func(t *testing.T) {
			s, actor := assetSetupFixture(t)
			seedAssetSetup(t, s)
			const target = "https://console.example/app/video-assets"
			if err := s.Assets.ConfigureConsole(videoasset.ConsoleOptions{CallbackURL: target}); err != nil {
				t.Fatal(err)
			}
			org, lookup, expires := uint64(1), target, s.Now().Add(time.Hour)
			var revoked *time.Time
			cipher := "synthetic-presence-only-no-decryption"
			switch variation {
			case "wrong_org":
				org = 2
			case "wrong_url":
				lookup = "https://other.example/return"
			case "expired":
				expires = s.Now()
			case "revoked":
				now := s.Now()
				revoked = &now
			case "empty_record":
				cipher = ""
			case "disabled_policy":
				if err := s.DB.Exec("UPDATE video_org_policies SET async_execution_allowed=0 WHERE org_id=1").Error; err != nil {
					t.Fatal(err)
				}
			case "scope_identity":
				if err := s.DB.Exec("UPDATE video_asset_scope_configs SET project_name='wrong' WHERE provider_scope_id=10").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := s.DB.Exec("INSERT INTO video_asset_callback_registry VALUES(?,?,?,?,?,?)", "callback1", org, s.assetURLHash(org, "callback", lookup), cipher, expires, revoked).Error; err != nil {
				t.Fatal(err)
			}
			if err := s.DB.Exec("PRAGMA query_only=ON").Error; err != nil {
				t.Fatal(err)
			}
			out, err := s.AssetSetup(context.Background(), actor, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !out.Deployment.PlatformCallbackConfigured || !out.Deployment.PortraitReturnConfigured || out.Deployment.PortraitTargetReady != (variation == "valid") {
				t.Fatalf("registry eligibility=%+v", out.Deployment)
			}
			if variation != "valid" && variation != "disabled_policy" && variation != "scope_identity" && !setupHasIssue(out, "portrait_return_not_registered") {
				t.Fatal("missing registry issue")
			}
			if out.OfficialAccess != "not_checked" {
				t.Fatal("local registration must not claim official access")
			}
		})
	}
}
