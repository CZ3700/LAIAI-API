package video

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const assetProjectSchema = `
CREATE TABLE video_provider_scopes(id INTEGER PRIMARY KEY AUTOINCREMENT,scope_key TEXT NOT NULL UNIQUE,account_ref TEXT NOT NULL,region TEXT NOT NULL,project_name TEXT NOT NULL,enabled INTEGER NOT NULL,fx_usd_per_cny TEXT NOT NULL,fx_version TEXT NOT NULL,fx_observed_at DATETIME NOT NULL);
CREATE UNIQUE INDEX uq_video_canonical_provider_scope ON video_provider_scopes(account_ref,region,project_name);
CREATE TABLE video_asset_events(event_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,operation_id TEXT NOT NULL,kind TEXT NOT NULL,evidence_ref TEXT NOT NULL,actor_id TEXT NOT NULL,created_at DATETIME NOT NULL);
`

func assetProjectFixture(t *testing.T) (*Service, ProviderScope) {
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
	if err := db.Exec(assetProjectSchema).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s := &Service{DB: db, Now: func() time.Time { return now }}
	source := ProviderScope{ScopeKey: "original", AccountRef: "2132103052", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.140000000000", FXVersion: "previous-fx", FXObservedAt: now.Add(-72 * time.Hour)}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	return s, source
}

func TestAssetProjectRegistersOnlyIdentityAndAudit(t *testing.T) {
	s, source := assetProjectFixture(t)
	// Existing financial and management tables are immutable in this operation.
	for _, table := range []string{"video_asset_scope_configs", "video_asset_org_routes", "video_model_configs", "video_pricing_rules", "video_quota_policies", "video_quota_buckets", "tasks", "video_task_states", "video_org_policies"} {
		if err := s.DB.Exec("CREATE TABLE " + table + "(id INTEGER PRIMARY KEY,sentinel TEXT); INSERT INTO " + table + " VALUES(1,'unchanged')").Error; err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
			if err := s.DB.Exec("CREATE TRIGGER deny_" + action + "_" + table + " BEFORE " + action + " ON " + table + " BEGIN SELECT RAISE(ABORT,'project registration must not change authorization'); END").Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	input := AssetProjectRegistration{SourceProviderScopeID: source.ID, ProjectName: "LAIAI-Gateway"}
	out, err := s.RegisterAssetProject(context.Background(), assetTestActor(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out.ProviderScope.ID == source.ID || out.ProviderScope.ID == 0 || !out.ProviderScope.Enabled || out.ProviderScope.ProjectName != input.ProjectName || out.ProviderScope.AccountRef != source.AccountRef {
		t.Fatal("wrong local identity projection")
	}
	var fresh, old ProviderScope
	if err := s.DB.First(&fresh, out.ProviderScope.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.First(&old, source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, source) {
		t.Fatal("source project changed")
	}
	if fresh.FXUSDPerCNY != source.FXUSDPerCNY || fresh.FXVersion != source.FXVersion || !fresh.FXObservedAt.Equal(source.FXObservedAt) {
		t.Fatal("FX observation was refreshed or changed")
	}
	replayed, err := s.RegisterAssetProject(context.Background(), assetTestActor(), input)
	if err != nil || replayed != out {
		t.Fatal("replay did not reuse canonical identity", err)
	}
	var count int64
	if err := s.DB.Model(&ProviderScope{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("project duplicated", count, err)
	}
	var events []videoasset.Event
	if err := s.DB.Find(&events).Error; err != nil || len(events) != 1 || events[0].Kind != "asset_project_registered" || events[0].ActorID != "77" {
		t.Fatal("registration audit mismatch", err)
	}
	raw, _ := json.Marshal(out)
	for _, forbidden := range []string{"scope_key", "fx_", "credential", "access_key", "secret_key", "session_token"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("unsafe response field", forbidden)
		}
	}
}

func TestAssetProjectValidatesSourceAndProject(t *testing.T) {
	s, source := assetProjectFixture(t)
	for _, name := range []string{"", " leading", "trailing ", "项目", "name/project", "-prefix", "a\n", strings.Repeat("a", 65)} {
		if _, err := s.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{source.ID, name}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid name %q accepted: %v", name, err)
		}
	}
	for _, actor := range []*identity.User{nil, {ID: 0, Role: identity.PlatRoleSuperadmin, Status: 1}, {ID: 1, Role: identity.PlatRoleAdmin, Status: 1}, {ID: 1, Role: identity.PlatRoleSuperadmin, Status: 0}} {
		if _, err := s.RegisterAssetProject(context.Background(), actor, AssetProjectRegistration{source.ID, "valid"}); !errors.Is(err, ErrForbidden) {
			t.Fatal("unauthorized registration", err)
		}
	}
	if _, err := s.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{0, "valid"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{999, "valid"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, change := range []map[string]any{{"enabled": false}, {"region": "ap-southeast-1"}, {"account_ref": "bad/account"}, {"fx_usd_per_cny": "0"}, {"fx_version": ""}, {"fx_observed_at": s.Now().Add(time.Hour)}} {
		t.Run("invalid_source", func(t *testing.T) {
			f, original := assetProjectFixture(t)
			if err := f.DB.Model(&ProviderScope{}).Where("id=?", original.ID).Updates(change).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := f.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{original.ID, "valid"}); !errors.Is(err, ErrCapability) {
				t.Fatal("invalid source allowed", err)
			}
		})
	}
	var count int64
	if err := s.DB.Model(&ProviderScope{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("invalid requests mutated projects", err)
	}
}

func TestAssetProjectReusesLegacyIdentityWithoutEnablingOrOverwriting(t *testing.T) {
	s, source := assetProjectFixture(t)
	legacy := source
	legacy.ID = 0
	legacy.ScopeKey = "old-unrelated-label"
	legacy.ProjectName = "LAIAI-Gateway"
	legacy.Enabled = false
	legacy.FXUSDPerCNY = "0.15"
	legacy.FXVersion = "legacy-rate"
	if err := s.DB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	out, err := s.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{source.ID, legacy.ProjectName})
	if err != nil || out.ProviderScope.ID != legacy.ID || out.ProviderScope.Enabled {
		t.Fatal("legacy identity changed", err)
	}
	var after ProviderScope
	if err := s.DB.First(&after, legacy.ID).Error; err != nil || !reflect.DeepEqual(after, legacy) {
		t.Fatal("existing FX or identity changed", err)
	}
	var count int64
	if err := s.DB.Model(&videoasset.Event{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("replay created duplicate registration event", err)
	}
	// Defensive against an incomplete legacy migration: never choose an
	// arbitrary duplicate account/region/project and split its financial scope.
	if err := s.DB.Exec("DROP INDEX uq_video_canonical_provider_scope").Error; err != nil {
		t.Fatal(err)
	}
	duplicate := legacy
	duplicate.ID = 0
	duplicate.ScopeKey = "another-legacy-label"
	if err := s.DB.Create(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{source.ID, legacy.ProjectName}); !errors.Is(err, ErrConflict) {
		t.Fatal("ambiguous legacy project accepted", err)
	}
}

func TestAssetProjectAuditFailureRollsBackIdentity(t *testing.T) {
	s, source := assetProjectFixture(t)
	if err := s.DB.Exec("CREATE TRIGGER deny_event BEFORE INSERT ON video_asset_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{source.ID, "LAIAI-Gateway"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("audit failure accepted", err)
	}
	var count int64
	if err := s.DB.Model(&ProviderScope{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("identity escaped transaction rollback", err)
	}
}

func TestAssetProjectConcurrentReplayAcrossSources(t *testing.T) {
	s, source := assetProjectFixture(t)
	second, err := s.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{source.ID, "second"})
	if err != nil {
		t.Fatal(err)
	}
	const n = 12
	results := make(chan AssetProjectRegistrationResult, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := source.ID
			if i%2 != 0 {
				id = second.ProviderScope.ID
			}
			out, err := s.RegisterAssetProject(context.Background(), assetTestActor(), AssetProjectRegistration{id, "LAIAI-Gateway"})
			results <- out
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id uint64
	for result := range results {
		if id == 0 {
			id = result.ProviderScope.ID
		}
		if id == 0 || result.ProviderScope.ID != id {
			t.Fatal("multiple canonical project identities")
		}
	}
	var count int64
	if err := s.DB.Model(&ProviderScope{}).Where("project_name=?", "LAIAI-Gateway").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate project", err)
	}
	if err := s.DB.Model(&videoasset.Event{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("concurrent replay duplicated audit", err)
	}
}
