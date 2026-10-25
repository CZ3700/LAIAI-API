package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const routingMigration = "0129_routing_groups.sql"

func routingMigrationFiles(t *testing.T) (fstest.MapFS, fstest.MapFS) {
	t.Helper()
	base, update := fstest.MapFS{}, fstest.MapFS{}
	source := os.DirFS("../../../migrations")
	names, e := fs.Glob(source, "*.sql")
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range names {
		if name > routingMigration {
			continue
		}
		raw, e := fs.ReadFile(source, name)
		if e != nil {
			t.Fatal(e)
		}
		if name == routingMigration {
			update[name] = &fstest.MapFile{Data: raw}
		} else {
			base[name] = &fstest.MapFile{Data: raw}
		}
	}
	// The merged 0128 inbox migration is part of the real upgrade baseline.
	if len(base) != 128 || base["0128_video_response_inbox.sql"] == nil || len(update) != 1 {
		t.Fatal("formal migrations missing")
	}
	return base, update
}

func TestMySQLRoutingGroupMigrationPreservesLegacyAndReplays(t *testing.T) {
	for partial := 0; partial <= 5; partial++ {
		t.Run(fmt.Sprintf("resume_after_%d", partial), func(t *testing.T) {
			db := runtimeMigrationDatabase(t)
			base, update := routingMigrationFiles(t)
			if _, e := Up(db, base); e != nil {
				t.Fatal(e)
			}
			seedRuntimeLegacyRows(t, db, "routing-preserve")
			for _, q := range []string{
				"INSERT INTO organizations(id,slug,name,group_name,status) VALUES(1,'routing','routing','existing-billing-group',1)",
				"INSERT INTO api_keys(org_id,name,prefix,key_hash,scopes,status,model_limits,quota_limit,used_amount) VALUES(1,'old','old','old-key-hash','video:*',1,'m1, m2',12.12345678,1.23456789)",
			} {
				if e := db.Exec(q).Error; e != nil {
					t.Fatal(e)
				}
			}
			before := map[string]runtimeTableSnapshot{}
			for _, table := range []string{"organizations", "api_keys", "tasks", "video_task_states", "video_operations", "video_query_receipts", "video_billing_snapshots", "ledger_accounts", "ledger_holds", "ledger_journal"} {
				before[table] = snapshotRuntimeTable(t, db, table, nil)
				if before[table].Count == 0 {
					t.Fatal("empty preservation control", table)
				}
			}
			statements := splitStatements(string(update[routingMigration].Data))
			if len(statements) != 5 {
				t.Fatal("expected four CREATE plus one atomic ALTER")
			}
			for _, statement := range statements[:partial] {
				if e := db.Exec(statement).Error; e != nil {
					t.Fatal(e)
				}
			}
			ran, e := Up(db, update)
			if e != nil || !reflect.DeepEqual(ran, []string{"0129_routing_groups"}) {
				t.Fatalf("migration not applied: %v %v", ran, e)
			}
			for table, old := range before {
				now := snapshotRuntimeTable(t, db, table, old.Columns)
				if old.Hash != now.Hash || old.Count != now.Count {
					t.Fatal("legacy mutation", table)
				}
			}
			var key identity.APIKey
			if e := db.First(&key).Error; e != nil {
				t.Fatal(e)
			}
			if key.ModelAccessMode != "legacy" || key.RoutingGroupID != nil || key.PolicyVersion != 1 || !key.AllowsModel("m1") || key.AllowsModel("m3") {
				t.Fatal("legacy classification/whitelist changed")
			}
			if e := db.Exec("INSERT INTO api_keys(org_id,name,prefix,key_hash,model_limits) VALUES(1,'old-after','old-after','old-after-key-hash','')").Error; e != nil {
				t.Fatal("old writer failed", e)
			}
			key = identity.APIKey{}
			if e := db.Where("key_hash='old-after-key-hash'").Take(&key).Error; e != nil || key.PolicyVersion != 1 || key.ModelAccessMode != "legacy" || key.RoutingGroupID != nil || !key.AllowsModel("any") {
				t.Fatal("old writer default invalid", e)
			}
			if e := db.Exec("UPDATE api_keys SET name='old-writer-update',model_limits='old-model',used_amount=0.12345678 WHERE id=?", key.ID).Error; e != nil {
				t.Fatal(e)
			}
			if e := db.First(&key, key.ID).Error; e != nil || key.ModelAccessMode != "legacy" || key.RoutingGroupID != nil || key.PolicyVersion != 1 || !key.AllowsModel("old-model") || key.AllowsModel("any") {
				t.Fatal("old UPDATE changed routing contract", e)
			}
			assertRoutingSchema(t, db)
			assertRoutingChecks(t, db, key.ID)
			for _, table := range []string{"routing_groups", "channel_group_bindings", "org_group_grants", "routing_policy_events"} {
				var n int64
				if e := db.Table(table).Count(&n).Error; e != nil || n != 0 {
					t.Fatal("migration created authorization state", table, e)
				}
			}
			before = map[string]runtimeTableSnapshot{}
			for _, table := range []string{"api_keys", "organizations", "ledger_accounts", "ledger_holds", "ledger_journal"} {
				before[table] = snapshotRuntimeTable(t, db, table, nil)
			}
			if ran, e := Up(db, update); e != nil || len(ran) != 0 {
				t.Fatal("replay", e)
			}
			for table, old := range before {
				now := snapshotRuntimeTable(t, db, table, old.Columns)
				if old.Hash != now.Hash {
					t.Fatal("replay changed", table)
				}
			}
			if drift, e := ledger.CheckDrift(db); e != nil || len(drift) != 0 {
				t.Fatal("ledger drift", e)
			}
		})
	}
}

func assertRoutingSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, column := range []struct{ table, name string }{{"api_keys", "model_access_mode"}, {"routing_groups", "status"}, {"routing_groups", "slug"}, {"org_group_grants", "status"}, {"routing_policy_events", "aggregate_type"}} {
		var collation string
		if e := db.Raw("SELECT collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?", column.table, column.name).Scan(&collation).Error; e != nil || collation != "utf8mb4_0900_bin" {
			t.Fatal("not exact NO PAD", column, collation, e)
		}
	}
	for _, index := range []struct{ table, name, columns string }{
		{"routing_groups", "uk_routing_group_slug", "owner_org_id,slug"}, {"routing_groups", "idx_routing_group_owner", "owner_org_id,status,id"},
		{"channel_group_bindings", "PRIMARY", "group_id,channel_id"}, {"channel_group_bindings", "idx_routing_channel_group", "channel_id,group_id"},
		{"org_group_grants", "uk_routing_org_group", "org_id,group_id"}, {"org_group_grants", "uk_routing_org_default", "org_id,default_slot"},
		{"org_group_grants", "idx_routing_grant_org", "org_id,status,group_id"}, {"org_group_grants", "idx_routing_grant_group", "group_id,org_id"},
		{"routing_policy_events", "uk_routing_policy_event", "aggregate_type,aggregate_id,aggregate_version"}, {"api_keys", "idx_api_key_routing_group", "routing_group_id,id"},
	} {
		var columns string
		if e := db.Raw("SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=?", index.table, index.name).Scan(&columns).Error; e != nil || columns != index.columns {
			t.Fatal("index mismatch", index.name, columns, e)
		}
	}
}

func assertRoutingChecks(t *testing.T, db *gorm.DB, id uint64) {
	t.Helper()
	for _, set := range []string{"model_access_mode=''", "model_access_mode='LEGACY'", "model_access_mode='legacy '", "model_access_mode='unknown'", "model_access_mode=NULL", "policy_version=0", "routing_group_id=0", "routing_group_id=1", "model_access_mode='inherit_group'", "model_access_mode='subset',routing_group_id=0"} {
		if e := db.Exec("UPDATE api_keys SET "+set+" WHERE id=?", id).Error; e == nil {
			t.Fatal("invalid key value accepted", set)
		}
	}
	// Database can represent future grouped keys, but current Go MUST reject
	// them. Roll back these probes so no grouped authorization survives a test.
	for _, mode := range []string{"inherit_group", "subset"} {
		e := db.Transaction(func(tx *gorm.DB) error {
			if e := tx.Exec("UPDATE api_keys SET model_access_mode=?,routing_group_id=1 WHERE id=?", mode, id).Error; e != nil {
				return e
			}
			if e := tx.Exec("UPDATE api_keys SET name='old-binary-edit' WHERE id=?", id).Error; e != nil {
				return e
			}
			var key identity.APIKey
			if e := tx.First(&key, id).Error; e != nil {
				return e
			}
			if key.AllowsModel("any") {
				t.Fatal("grouped key fallback")
			}
			if key.ModelAccessMode != mode || key.RoutingGroupID == nil || *key.RoutingGroupID != 1 || key.PolicyVersion != 1 {
				t.Fatal("old UPDATE overwrote grouped routing fields")
			}
			return errRoutingProbeRollback
		})
		if !errors.Is(e, errRoutingProbeRollback) {
			t.Fatal("valid future representation rejected", e)
		}
	}
}

var errRoutingProbeRollback = errors.New("test-only rollback")

// Test host adapter implements the documented current-read boundary. Business
// routinggroup code never reads organizations/channels or their credentials.
type mysqlRoutingDependencies struct{}

func (mysqlRoutingDependencies) LockOrganizations(ctx context.Context, tx *gorm.DB, ids []uint64) ([]routinggroup.Organization, error) {
	var rows []identity.Organization
	e := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", ids).Order("id").Find(&rows).Error
	out := make([]routinggroup.Organization, 0, len(rows))
	for _, r := range rows {
		out = append(out, routinggroup.Organization{ID: r.ID, Active: r.Status == identity.StatusActive})
	}
	return out, e
}

func TestMySQLRoutingDefaultsSerializeAndKeepEventsAtomic(t *testing.T) {
	db := runtimeMigrationDatabase(t)
	base, update := routingMigrationFiles(t)
	if _, e := Up(db, base); e != nil {
		t.Fatal(e)
	}
	if _, e := Up(db, update); e != nil {
		t.Fatal(e)
	}
	if e := db.Exec("INSERT INTO organizations(id,slug,name,status) VALUES(1,'routing-concurrency','routing',1)").Error; e != nil {
		t.Fatal(e)
	}
	s := &routinggroup.Service{DB: db, Organizations: mysqlRoutingDependencies{}}
	ctx := context.Background()
	audit := routinggroup.Audit{ActorUserID: 1, RequestID: "mysql-routing"}
	var groups []routinggroup.Group
	for _, slug := range []string{"one", "two"} {
		g, e := s.Create(ctx, routinggroup.CreateInput{Slug: slug, Name: slug, Audit: audit})
		if e != nil {
			t.Fatal(e)
		}
		g, _, e = s.Update(ctx, routinggroup.UpdateInput{GroupID: g.ID, ExpectedVersion: 1, Name: slug, Status: routinggroup.Enabled, Audit: audit})
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = s.SetGrant(ctx, routinggroup.GrantInput{OrgID: 1, GroupID: g.ID, Status: routinggroup.Enabled, Audit: audit}); e != nil {
			t.Fatal(e)
		}
		groups = append(groups, g)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, g := range groups {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			<-start
			_, e := s.SetDefault(ctx, routinggroup.DefaultInput{OrgID: 1, GroupID: &id, ExpectedTargetVersion: 1, Audit: audit})
			results <- e
		}(g.ID)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, routinggroup.ErrConflict) {
			conflict++
		} else {
			t.Fatal("unexpected concurrency error", e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("two stale default writers succeeded", success, conflict)
	}
	var current routinggroup.Grant
	if e := db.Where("org_id=1 AND default_slot=1").Take(&current).Error; e != nil {
		t.Fatal(e)
	}
	if current.GrantVersion != 2 {
		t.Fatal("unexpected winning version")
	}
	other := groups[0].ID
	if other == current.GroupID {
		other = groups[1].ID
	}
	// Database is a second line of defense even if a future caller omits locks.
	if e := db.Exec("UPDATE org_group_grants SET default_slot=1 WHERE org_id=1 AND group_id=?", other).Error; e == nil {
		t.Fatal("multiple defaults allowed")
	}
	if e := db.Exec("UPDATE org_group_grants SET default_slot=0 WHERE org_id=1 AND group_id=?", other).Error; e == nil {
		t.Fatal("zero default marker allowed")
	}
	if e := db.Exec("UPDATE org_group_grants SET status='revoked' WHERE id=?", current.ID).Error; e == nil {
		t.Fatal("revoked default allowed")
	}
	var events int64
	if e := db.Model(&routinggroup.Event{}).Where("aggregate_type='grant'").Count(&events).Error; e != nil || events != 3 {
		t.Fatal("duplicate grant event", events, e)
	}
	// Duplicate version event causes the whole default-switch transaction to
	// fail; both pointer/version updates must roll back.
	if e := db.Create(&routinggroup.Event{AggregateType: "grant", AggregateID: current.ID, AggregateVersion: 3, EventKind: "test_conflict", ActorUserID: 1, RequestID: "fixture"}).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetDefault(ctx, routinggroup.DefaultInput{OrgID: 1, ExpectedGroupID: &current.GroupID, GroupID: &other, ExpectedCurrentVersion: 2, ExpectedTargetVersion: 1, Audit: audit}); e == nil {
		t.Fatal("event conflict ignored")
	}
	var after routinggroup.Grant
	if e := db.Where("org_id=1 AND default_slot=1").Take(&after).Error; e != nil || after.ID != current.ID || after.GrantVersion != 2 {
		t.Fatal("event failure partially moved default", e)
	}
	// A second organization fails on the TARGET event after the old pointer
	// and its event have already been written. That entire sequence rolls back.
	if e := db.Exec("INSERT INTO organizations(id,slug,name,status) VALUES(2,'routing-target-conflict','routing',1)").Error; e != nil {
		t.Fatal(e)
	}
	var targetGrant routinggroup.Grant
	for _, g := range groups {
		grant, _, e := s.SetGrant(ctx, routinggroup.GrantInput{OrgID: 2, GroupID: g.ID, Status: routinggroup.Enabled, Audit: audit})
		if e != nil {
			t.Fatal(e)
		}
		if g.ID == groups[1].ID {
			targetGrant = grant
		}
	}
	if _, e := s.SetDefault(ctx, routinggroup.DefaultInput{OrgID: 2, GroupID: &groups[0].ID, ExpectedTargetVersion: 1, Audit: audit}); e != nil {
		t.Fatal(e)
	}
	if e := db.Create(&routinggroup.Event{AggregateType: "grant", AggregateID: targetGrant.ID, AggregateVersion: 2, EventKind: "test_target_conflict", ActorUserID: 1, RequestID: "fixture"}).Error; e != nil {
		t.Fatal(e)
	}
	grantsBefore := snapshotRuntimeTable(t, db, "org_group_grants", nil)
	eventsBefore := snapshotRuntimeTable(t, db, "routing_policy_events", nil)
	if _, e := s.SetDefault(ctx, routinggroup.DefaultInput{OrgID: 2, ExpectedGroupID: &groups[0].ID, GroupID: &groups[1].ID, ExpectedCurrentVersion: 2, ExpectedTargetVersion: 1, Audit: audit}); e == nil {
		t.Fatal("target event conflict ignored")
	}
	if got := snapshotRuntimeTable(t, db, "org_group_grants", nil); got.Hash != grantsBefore.Hash {
		t.Fatal("target event failure partially changed grants")
	}
	if got := snapshotRuntimeTable(t, db, "routing_policy_events", nil); got.Hash != eventsBefore.Hash {
		t.Fatal("old-clear event survived target event failure")
	}
	var plan []struct {
		Key  *string `gorm:"column:key"`
		Type string  `gorm:"column:type"`
	}
	if e := db.Raw("EXPLAIN SELECT id FROM org_group_grants WHERE org_id=1 AND default_slot=1").Scan(&plan).Error; e != nil || len(plan) != 1 || plan[0].Key == nil || *plan[0].Key != "uk_routing_org_default" || plan[0].Type == "ALL" {
		t.Fatal("default lookup missing bounded unique index", e)
	}
}
