package routinggroup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testSchema = `
CREATE TABLE routing_groups(id INTEGER PRIMARY KEY AUTOINCREMENT,slug TEXT NOT NULL,name TEXT NOT NULL,description TEXT NOT NULL,owner_org_id INTEGER NOT NULL,status TEXT NOT NULL,policy_version INTEGER NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(owner_org_id,slug));
CREATE TABLE channel_group_bindings(group_id INTEGER NOT NULL,channel_id INTEGER NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,PRIMARY KEY(group_id,channel_id));
CREATE TABLE org_group_grants(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,group_id INTEGER NOT NULL,status TEXT NOT NULL,grant_version INTEGER NOT NULL,default_slot INTEGER,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,group_id),UNIQUE(org_id,default_slot),CHECK(default_slot IS NULL OR(default_slot=1 AND status='enabled')));
CREATE TABLE routing_policy_events(id INTEGER PRIMARY KEY AUTOINCREMENT,aggregate_type TEXT NOT NULL,aggregate_id INTEGER NOT NULL,aggregate_version INTEGER NOT NULL,event_kind TEXT NOT NULL,org_id INTEGER,group_id INTEGER,channel_id INTEGER,key_id INTEGER,actor_user_id INTEGER NOT NULL,request_id TEXT NOT NULL,created_at DATETIME NOT NULL,UNIQUE(aggregate_type,aggregate_id,aggregate_version));
`

type fixtureDependencies struct {
	orgs     map[uint64]bool
	channels map[uint64]uint64
	fail     error
}

func (f *fixtureDependencies) LockOrganizations(_ context.Context, tx *gorm.DB, ids []uint64) ([]Organization, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	var out []Organization
	if tx.Statement.ConnPool == nil {
		return nil, ErrUnavailable
	}
	for _, id := range ids {
		if active, ok := f.orgs[id]; ok {
			out = append(out, Organization{ID: id, Active: active})
		}
	}
	return out, nil
}
func (f *fixtureDependencies) LockChannels(_ context.Context, _ *gorm.DB, ids []uint64) ([]Channel, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	var out []Channel
	for _, id := range ids {
		if org, ok := f.channels[id]; ok {
			out = append(out, Channel{ID: id, OwnerOrgID: org, Status: 1})
		}
	}
	return out, nil
}

func serviceFixture(t *testing.T) (*Service, *fixtureDependencies) {
	t.Helper()
	db, e := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if e != nil {
		t.Fatal(e)
	}
	sql, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	if e = db.Exec(testSchema).Error; e != nil {
		t.Fatal(e)
	}
	f := &fixtureDependencies{orgs: map[uint64]bool{1: true, 2: true, 3: false}, channels: map[uint64]uint64{10: 0, 11: 1, 12: 2}}
	return &Service{DB: db, Organizations: f, Channels: f}, f
}

var audit = Audit{ActorUserID: 7, RequestID: "fixture"}

func enabledGroup(t *testing.T, s *Service, owner uint64, slug string) Group {
	t.Helper()
	g, e := s.Create(context.Background(), CreateInput{OwnerOrgID: owner, Slug: slug, Name: slug, Audit: audit})
	if e != nil {
		t.Fatal(e)
	}
	g, changed, e := s.Update(context.Background(), UpdateInput{OwnerOrgID: owner, GroupID: g.ID, ExpectedVersion: g.PolicyVersion, Name: g.Name, Status: Enabled, Audit: audit})
	if e != nil || !changed {
		t.Fatal(e)
	}
	return g
}
func rowCount(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	if e := db.Model(model).Count(&n).Error; e != nil {
		t.Fatal(e)
	}
	return n
}

func TestGroupCreateDuplicateRequiresExactOwnerSlug(t *testing.T) {
	s, _ := serviceFixture(t)
	in := CreateInput{Slug: "same-slug", Name: "Original", Description: "unchanged", Audit: audit}
	original, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Name = "Must not overwrite"
	got, err := s.Create(context.Background(), in)
	if !errors.Is(err, ErrExists) || got != (Group{}) || err.Error() != ErrExists.Error() {
		t.Fatalf("duplicate group must return only its safe domain error: %#v %v", got, err)
	}
	var saved Group
	if err := s.DB.First(&saved, original.ID).Error; err != nil || saved.Name != original.Name || saved.PolicyVersion != 1 {
		t.Fatal("duplicate replaced the existing group", err)
	}
	if rowCount(t, s.DB, &Group{}) != 1 || rowCount(t, s.DB, &Event{}) != 1 {
		t.Fatal("duplicate created configuration or events")
	}
	in.OwnerOrgID = 1
	if _, err := s.Create(context.Background(), in); err != nil {
		t.Fatal("same slug in another owner is valid", err)
	}
	// A typed UNIQUE error on a different constraint is not an owner/slug
	// conflict. Confirming the requested key must find a real matching row.
	if err := s.DB.Exec("CREATE UNIQUE INDEX fixture_group_name ON routing_groups(name)").Error; err != nil {
		t.Fatal(err)
	}
	in.Slug = "different-slug"
	if _, err := s.Create(context.Background(), in); err == nil || errors.Is(err, ErrExists) {
		t.Fatal("unrelated unique constraint was called an existing group", err)
	}
	if rowCount(t, s.DB, &Group{}) != 2 || rowCount(t, s.DB, &Event{}) != 2 {
		t.Fatal("failed unrelated uniqueness left partial writes")
	}
}

func TestGroupCreateEventDuplicateDoesNotBecomeGroupExists(t *testing.T) {
	s, _ := serviceFixture(t)
	// The first generated group will have ID 1. Its version event already
	// exists, producing an actual SQLite UNIQUE error after the group INSERT.
	if err := s.DB.Create(&Event{AggregateType: "group", AggregateID: 1, AggregateVersion: 1, EventKind: "fixture", ActorUserID: audit.ActorUserID, RequestID: audit.RequestID}).Error; err != nil {
		t.Fatal(err)
	}
	_, err := s.Create(context.Background(), CreateInput{Slug: "event-conflict", Name: "Event conflict", Audit: audit})
	if err == nil || errors.Is(err, ErrExists) || rowCount(t, s.DB, &Group{}) != 0 || rowCount(t, s.DB, &Event{}) != 1 {
		t.Fatal("event conflict was swallowed or failed to roll back the group", err)
	}
}

func TestGroupCreateDuplicateTextAndConfirmationFailureStayUnavailable(t *testing.T) {
	s, _ := serviceFixture(t)
	in := CreateInput{Slug: "exists", Name: "Existing", Audit: audit}
	if _, err := s.Create(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Callback().Create().Before("gorm:create").Register("group-untyped-duplicate", func(tx *gorm.DB) {
		if tx.Statement.Table == "routing_groups" {
			tx.AddError(errors.New("UNIQUE constraint failed: routing_groups.owner_org_id, routing_groups.slug"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create(context.Background(), in)
	if err == nil || errors.Is(err, ErrExists) {
		t.Fatal("plain driver-looking text was treated as typed duplicate", err)
	}
	if err := s.DB.Callback().Create().Remove("group-untyped-duplicate"); err != nil {
		t.Fatal(err)
	}
	checked := false
	if err := s.DB.Callback().Query().Before("gorm:query").Register("group-confirmation-fault", func(tx *gorm.DB) {
		if tx.Statement.Table == "routing_groups" {
			checked = true
			tx.AddError(errors.New("synthetic confirmation unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(context.Background(), in)
	if !checked || err == nil || errors.Is(err, ErrExists) {
		t.Fatal("failed current read was treated as confirmed duplicate", err)
	}
	if err := s.DB.Callback().Query().Remove("group-confirmation-fault"); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, s.DB, &Group{}) != 1 || rowCount(t, s.DB, &Event{}) != 1 {
		t.Fatal("failed duplicate classification changed data")
	}
}

type groupCreateMySQLDialect struct{ gorm.Dialector }

func (groupCreateMySQLDialect) Name() string { return "mysql" }

func TestGroupInsertDuplicateClassifierUsesTypedDriverAndDialect(t *testing.T) {
	s, _ := serviceFixture(t)
	mysqlDB := s.DB.Session(&gorm.Session{})
	config := *mysqlDB.Config
	mysqlDB.Config = &config
	mysqlDB.Dialector = groupCreateMySQLDialect{s.DB.Dialector}
	for _, test := range []struct {
		err  error
		want bool
	}{
		{&mysql.MySQLError{Number: 1062, Message: "private-values"}, true},
		{fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1062}), true},
		{&mysql.MySQLError{Number: 1213, Message: "Duplicate entry"}, false},
		{errors.New("Duplicate entry for key uk_routing_group_slug"), false},
		{gorm.ErrDuplicatedKey, false},
	} {
		if got := groupInsertDuplicate(mysqlDB, test.err); got != test.want {
			t.Fatal("unexpected MySQL typed classification")
		}
		if groupInsertDuplicate(s.DB, test.err) {
			t.Fatal("MySQL errors accepted on SQLite")
		}
	}
	if strings.Contains(ErrExists.Error(), "private-values") {
		t.Fatal("domain conflict error contains driver text")
	}
}

func TestGroupOwnershipCASAndBindingAtomicity(t *testing.T) {
	s, f := serviceFixture(t)
	ctx := context.Background()
	platform := enabledGroup(t, s, 0, "platform")
	private := enabledGroup(t, s, 1, "private")
	for _, tt := range []BindingInput{
		{OwnerOrgID: 0, GroupID: platform.ID, ChannelID: 11, ExpectedVersion: platform.PolicyVersion, Bound: true, Audit: audit},
		{OwnerOrgID: 1, GroupID: private.ID, ChannelID: 10, ExpectedVersion: private.PolicyVersion, Bound: true, Audit: audit},
		{OwnerOrgID: 1, GroupID: private.ID, ChannelID: 12, ExpectedVersion: private.PolicyVersion, Bound: true, Audit: audit},
	} {
		if _, _, e := s.SetBinding(ctx, tt); !errors.Is(e, ErrOwnership) {
			t.Fatal("cross-domain binding", e)
		}
	}
	in := BindingInput{OwnerOrgID: 1, GroupID: private.ID, ChannelID: 11, ExpectedVersion: private.PolicyVersion, Bound: true, Audit: audit}
	g, changed, e := s.SetBinding(ctx, in)
	if e != nil || !changed || g.PolicyVersion != 3 {
		t.Fatal("bind", e)
	}
	if _, _, e = s.SetBinding(ctx, in); !errors.Is(e, ErrConflict) {
		t.Fatal("stale CAS", e)
	}
	in.ExpectedVersion = g.PolicyVersion
	events := rowCount(t, s.DB, &Event{})
	if _, changed, e = s.SetBinding(ctx, in); e != nil || changed || rowCount(t, s.DB, &Event{}) != events {
		t.Fatal("noop must retain version/event", e)
	}
	if e = s.DB.Exec(`CREATE TRIGGER refuse_event BEFORE INSERT ON routing_policy_events BEGIN SELECT RAISE(ABORT,'event unavailable'); END`).Error; e != nil {
		t.Fatal(e)
	}
	in.Bound = false
	if _, _, e = s.SetBinding(ctx, in); e == nil {
		t.Fatal("missing event failure")
	}
	if rowCount(t, s.DB, &Binding{}) != 1 {
		t.Fatal("partial unbind")
	}
	var reread Group
	if e = s.DB.First(&reread, g.ID).Error; e != nil || reread.PolicyVersion != 3 {
		t.Fatal("partial version")
	}
	if e = s.DB.Exec("DROP TRIGGER refuse_event").Error; e != nil {
		t.Fatal(e)
	}
	delete(f.channels, 11) // Existing channel deletion must not prevent cleanup.
	if _, changed, e = s.SetBinding(ctx, in); e != nil || !changed || rowCount(t, s.DB, &Binding{}) != 0 {
		t.Fatal("dangling cleanup", e)
	}
	if _, _, e = s.Update(ctx, UpdateInput{OwnerOrgID: 2, GroupID: private.ID, ExpectedVersion: 4, Name: "bad", Status: Enabled, Audit: audit}); !errors.Is(e, ErrNotFound) {
		t.Fatal("wrong tenant could mutate group", e)
	}
}

func TestDefaultGrantVersionsRollbackAndInactiveCleanup(t *testing.T) {
	s, f := serviceFixture(t)
	ctx := context.Background()
	a := enabledGroup(t, s, 0, "a")
	b := enabledGroup(t, s, 0, "b")
	private := enabledGroup(t, s, 2, "private")
	if _, _, e := s.SetGrant(ctx, GrantInput{OrgID: 1, GroupID: private.ID, Status: Enabled, Audit: audit}); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-org private grant", e)
	}
	ga, _, e := s.SetGrant(ctx, GrantInput{OrgID: 1, GroupID: a.ID, Status: Enabled, Audit: audit})
	if e != nil {
		t.Fatal(e)
	}
	gb, _, e := s.SetGrant(ctx, GrantInput{OrgID: 1, GroupID: b.ID, Status: Enabled, Audit: audit})
	if e != nil {
		t.Fatal(e)
	}
	if changed, e := s.SetDefault(ctx, DefaultInput{OrgID: 1, GroupID: &a.ID, ExpectedTargetVersion: ga.GrantVersion, Audit: audit}); e != nil || !changed {
		t.Fatal(e)
	}
	if _, e := s.SetDefault(ctx, DefaultInput{OrgID: 1, GroupID: &b.ID, ExpectedTargetVersion: gb.GrantVersion, Audit: audit}); !errors.Is(e, ErrConflict) {
		t.Fatal("missing expected old default", e)
	}
	if e = s.DB.Exec(`CREATE TRIGGER refuse_new_default BEFORE INSERT ON routing_policy_events WHEN NEW.event_kind='default_set' BEGIN SELECT RAISE(ABORT,'event unavailable'); END`).Error; e != nil {
		t.Fatal(e)
	}
	switching := DefaultInput{OrgID: 1, ExpectedGroupID: &a.ID, GroupID: &b.ID, ExpectedCurrentVersion: 2, ExpectedTargetVersion: 1, Audit: audit}
	if _, e := s.SetDefault(ctx, switching); e == nil {
		t.Fatal("failure injection ignored")
	}
	var current Grant
	if e = s.DB.Where("org_id=1 AND default_slot=1").Take(&current).Error; e != nil || current.GroupID != a.ID || current.GrantVersion != 2 {
		t.Fatal("partial default clear", e)
	}
	if e = s.DB.Exec("DROP TRIGGER refuse_new_default").Error; e != nil {
		t.Fatal(e)
	}
	if changed, e := s.SetDefault(ctx, switching); e != nil || !changed {
		t.Fatal("switch", e)
	}
	current = Grant{}
	if e = s.DB.Where("org_id=1 AND default_slot=1").Take(&current).Error; e != nil || current.GroupID != b.ID || current.GrantVersion != 2 {
		t.Fatal("switch lost", e)
	}
	if _, _, e := s.Update(ctx, UpdateInput{GroupID: b.ID, ExpectedVersion: b.PolicyVersion, Name: b.Name, Status: Disabled, Audit: audit}); e != nil {
		t.Fatal(e)
	}
	f.orgs[1] = false
	if _, _, e := s.SetGrant(ctx, GrantInput{OrgID: 1, GroupID: b.ID, ExpectedVersion: 2, Status: Enabled, Audit: audit}); !errors.Is(e, ErrInactive) {
		t.Fatal("inactive grant re-enabled", e)
	}
	if changed, e := s.SetDefault(ctx, DefaultInput{OrgID: 1, ExpectedGroupID: &b.ID, ExpectedCurrentVersion: 2, Audit: audit}); e != nil || !changed {
		t.Fatal("inactive clear blocked", e)
	}
	g, changed, e := s.SetGrant(ctx, GrantInput{OrgID: 1, GroupID: b.ID, ExpectedVersion: 3, Status: Revoked, Audit: audit})
	if e != nil || !changed || g.GrantVersion != 4 || g.DefaultSlot != nil {
		t.Fatal("inactive revoke", e)
	}
	if rowCount(t, s.DB, &Grant{}) != 2 {
		t.Fatal("revoked grant was deleted")
	}
}

func TestGroupDependencyAndValidationFailuresDoNotWrite(t *testing.T) {
	s, f := serviceFixture(t)
	ctx := context.Background()
	for _, in := range []CreateInput{{Slug: "Upper", Name: "x", Audit: audit}, {Slug: "ok", Name: " ", Audit: audit}, {Slug: "ok", Name: "x"}, {OwnerOrgID: 3, Slug: "inactive", Name: "x", Audit: audit}} {
		if _, e := s.Create(ctx, in); e == nil {
			t.Fatal("invalid create accepted")
		}
	}
	f.fail = errors.New("injected unavailable")
	if _, e := s.Create(ctx, CreateInput{OwnerOrgID: 1, Slug: "private", Name: "private", Audit: audit}); e == nil {
		t.Fatal("failed dependency accepted")
	}
	if rowCount(t, s.DB, &Group{}) != 0 || rowCount(t, s.DB, &Event{}) != 0 {
		t.Fatal("partial create")
	}
}

func TestKeyEventRequiresCallerTransaction(t *testing.T) {
	s, _ := serviceFixture(t)
	if err := RecordKeyEvent(s.DB, 1, 1, 1, "key_issued", audit); !errors.Is(err, gorm.ErrInvalidTransaction) {
		t.Fatal("standalone event write accepted", err)
	}
	if rowCount(t, s.DB, &Event{}) != 0 {
		t.Fatal("bare DB wrote policy event")
	}
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := RecordKeyEvent(tx, 1, 1, 1, "key_issued", audit); err != nil {
			return err
		}
		return ErrConflict
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if rowCount(t, s.DB, &Event{}) != 0 {
		t.Fatal("rolled-back key event survived")
	}
}
