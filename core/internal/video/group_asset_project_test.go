package video

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func groupAssetProjectFixture(t *testing.T) (*Service, *identity.User) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	for _, statement := range []string{
		`CREATE TABLE routing_groups(id INTEGER PRIMARY KEY,owner_org_id INTEGER,status TEXT,policy_version INTEGER)`,
		`CREATE TABLE channel_group_bindings(group_id INTEGER,channel_id INTEGER,PRIMARY KEY(group_id,channel_id))`,
		`CREATE TABLE channels(id INTEGER PRIMARY KEY,owner_org_id INTEGER,status INTEGER)`,
		`CREATE TABLE video_model_configs(public_model TEXT PRIMARY KEY,channel_id INTEGER,provider_scope_id INTEGER,enabled INTEGER,verified_at DATETIME)`,
		`CREATE TABLE video_provider_scopes(id INTEGER PRIMARY KEY,account_ref TEXT,region TEXT,project_name TEXT,enabled INTEGER)`,
		`CREATE TABLE video_asset_scope_configs(provider_scope_id INTEGER,version INTEGER,account_ref TEXT,region TEXT,project_name TEXT,capabilities_json TEXT,credential_cipher TEXT,enabled INTEGER,PRIMARY KEY(provider_scope_id,version))`,
		`CREATE TABLE video_group_asset_projects(group_id INTEGER PRIMARY KEY,provider_scope_id INTEGER,scope_version INTEGER,revision INTEGER,updated_by INTEGER,updated_at DATETIME)`,
		`CREATE TABLE video_asset_events(event_id TEXT PRIMARY KEY,org_id INTEGER,resource_type TEXT,resource_id TEXT,operation_id TEXT,kind TEXT,evidence_ref TEXT,actor_id TEXT,created_at DATETIME)`,
		`CREATE TABLE api_keys(id INTEGER PRIMARY KEY,org_id INTEGER,status INTEGER,model_access_mode TEXT,routing_group_id INTEGER)`,
		`CREATE TABLE org_group_grants(id INTEGER PRIMARY KEY,org_id INTEGER,group_id INTEGER,status TEXT,default_slot INTEGER)`,
		`INSERT INTO routing_groups VALUES(1,0,'enabled',1)`,
		`INSERT INTO channel_group_bindings VALUES(1,10)`,
		`INSERT INTO channels VALUES(10,0,1)`,
		`INSERT INTO video_model_configs VALUES('seedance-2.5',10,20,1,'2026-11-09')`,
		`INSERT INTO video_provider_scopes VALUES(20,'1234567890','cn-beijing','project1',1),(21,'1234567890','cn-beijing','project2',1)`,
		`INSERT INTO video_asset_scope_configs VALUES(20,3,'1234567890','cn-beijing','project1','{"VirtualGroups":true}','encrypted',1),(21,1,'1234567890','cn-beijing','project2','{"VirtualGroups":true}','encrypted',1)`,
		`INSERT INTO api_keys VALUES(11,5,1,'inherit_group',1),(12,5,1,'legacy',NULL)`,
		`INSERT INTO org_group_grants VALUES(1,5,1,'enabled',1)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(statement, err)
		}
	}
	return &Service{DB: db, Now: func() time.Time { return time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC) }}, &identity.User{ID: 7, Role: identity.PlatRoleSuperadmin, Status: identity.StatusActive}
}

func TestGroupAssetProjectMatchesPublishedChannelAndRevisions(t *testing.T) {
	s, actor := groupAssetProjectFixture(t)
	ctx := context.Background()
	if _, err := s.SetGroupAssetProject(ctx, actor, GroupAssetProjectInput{GroupID: 1, ProviderScopeID: 21, ScopeVersion: 1}); !errors.Is(err, ErrCapability) {
		t.Fatalf("mismatched project = %v", err)
	}
	created, err := s.SetGroupAssetProject(ctx, actor, GroupAssetProjectInput{GroupID: 1, ProviderScopeID: 20, ScopeVersion: 3})
	if err != nil || created.Revision != 1 {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if _, err := s.SetGroupAssetProject(ctx, actor, GroupAssetProjectInput{GroupID: 1, ProviderScopeID: 20, ScopeVersion: 3, ExpectedRevision: 0}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	read, err := s.ReadGroupAssetProject(ctx, actor, 1)
	if err != nil || read.ProviderScopeID != 20 || read.ScopeVersion != 3 {
		t.Fatalf("read = %+v, %v", read, err)
	}
	var events int64
	if err := s.DB.Table("video_asset_events").Where("resource_type = ?", "routing_group_asset_project").Count(&events).Error; err != nil || events != 1 {
		t.Fatalf("audit events = %d, %v", events, err)
	}
}

func TestGroupAssetProjectRoutesGroupedKeysAndDefaultSession(t *testing.T) {
	s, actor := groupAssetProjectFixture(t)
	grouped := videoasset.WithAPIKey(context.Background(), &identity.APIKey{ID: 11, OrgID: 5})
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		_, err := s.callerAssetGroupRouteTx(grouped, tx, 5)
		if !errors.Is(err, ErrCapability) {
			t.Fatalf("unmapped group = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetGroupAssetProject(context.Background(), actor, GroupAssetProjectInput{GroupID: 1, ProviderScopeID: 20, ScopeVersion: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		route, err := s.callerAssetGroupRouteTx(grouped, tx, 5)
		if err != nil || route == nil || route.ProviderScopeID != 20 || route.ScopeVersion != 3 {
			t.Fatalf("grouped route = %+v, %v", route, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	legacy := videoasset.WithAPIKey(context.Background(), &identity.APIKey{ID: 12, OrgID: 5})
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		route, err := s.callerAssetGroupRouteTx(legacy, tx, 5)
		if err != nil || route != nil {
			t.Fatalf("legacy route = %+v, %v", route, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	session := videoasset.WithCaller(context.Background(), videoasset.CreateContext{CallerKind: "session_user", OrgID: 5, UserID: 9})
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		route, err := s.callerAssetGroupRouteTx(session, tx, 5)
		if err != nil || route == nil || route.ProviderScopeID != 20 {
			t.Fatalf("session route = %+v, %v", route, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("UPDATE org_group_grants SET status='revoked' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		_, err := s.callerAssetGroupRouteTx(grouped, tx, 5)
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("revoked grant = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("UPDATE org_group_grants SET status='enabled' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("UPDATE video_model_configs SET provider_scope_id=21 WHERE public_model='seedance-2.5'").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		_, err := s.callerAssetGroupRouteTx(grouped, tx, 5)
		if !errors.Is(err, ErrCapability) {
			t.Fatalf("channel changed scope without project reassociation = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
