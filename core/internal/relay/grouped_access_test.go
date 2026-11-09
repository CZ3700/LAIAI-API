package relay

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

func groupedRelayFixture(t *testing.T) (*Relay, *gorm.DB, *identity.APIKey, []Channel, uint64, uint64) {
	t.Helper()
	db := pickerDB(t)
	if err := db.Exec(`CREATE TABLE organizations(id INTEGER PRIMARY KEY,status INTEGER NOT NULL);
CREATE TABLE api_keys(id INTEGER PRIMARY KEY,org_id INTEGER NOT NULL,user_id INTEGER,status INTEGER NOT NULL,expires_at DATETIME,model_access_mode TEXT NOT NULL,routing_group_id INTEGER,policy_version INTEGER NOT NULL,model_limits TEXT NOT NULL,scopes TEXT NOT NULL);
CREATE TABLE org_entitlements(org_id INTEGER PRIMARY KEY,feature_images INTEGER NOT NULL,feature_video INTEGER NOT NULL);
CREATE TABLE routing_groups(id INTEGER PRIMARY KEY,owner_org_id INTEGER NOT NULL,status TEXT NOT NULL,policy_version INTEGER NOT NULL);
CREATE TABLE org_group_grants(id INTEGER PRIMARY KEY,org_id INTEGER NOT NULL,group_id INTEGER NOT NULL,status TEXT NOT NULL,grant_version INTEGER NOT NULL);
CREATE TABLE channel_group_bindings(group_id INTEGER NOT NULL,channel_id INTEGER NOT NULL,PRIMARY KEY(group_id,channel_id));
INSERT INTO organizations VALUES(1,1);
INSERT INTO org_entitlements VALUES(1,1,1);
INSERT INTO routing_groups VALUES(7,0,'enabled',1);
INSERT INTO org_group_grants VALUES(9,1,7,'enabled',1);
INSERT INTO api_keys VALUES(11,1,NULL,1,NULL,'inherit_group',7,1,'','video:read video:write');`).Error; err != nil {
		t.Fatal(err)
	}
	bound := mkChannel(t, db, "bound", 0, 1, 1, "m1,m2")
	unbound := mkChannel(t, db, "unbound", 100, 1, 1, "m1")
	if err := db.Exec("INSERT INTO channel_group_bindings VALUES(7,?)", bound).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot []Channel
	if err := db.Where("status=1").Order("priority DESC").Order("id ASC").Find(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	group := uint64(7)
	key := &identity.APIKey{ID: 11, OrgID: 1, ModelAccessMode: identity.ModelAccessInheritGroup, RoutingGroupID: &group, PolicyVersion: 1, Scopes: "video:read video:write"}
	return &Relay{DB: db, Now: time.Now}, db, key, snapshot, bound, unbound
}

func TestGroupedRelayPickerUsesCurrentBindingAndPublication(t *testing.T) {
	r, db, key, staleSnapshot, bound, unbound := groupedRelayFixture(t)
	ctx := context.Background()
	if ids, err := r.groupedModelChannels(ctx, key, "m1"); err != nil || !reflect.DeepEqual(ids, []uint64{bound}) {
		t.Fatal("grouped model candidates", ids, err)
	}
	if ch, err := r.pickRelayChannel(ctx, key, staleSnapshot, "m1", 0, nil, "", nil); err != nil || ch.ID != bound {
		t.Fatal("unbound high priority channel was chosen", ch, err)
	}
	if code, msg, ok := r.gateRelayKey(ctx, key, "127.0.0.1", "m1"); !ok || code != 0 || msg != "" {
		t.Fatal("valid grouped model rejected", code, msg)
	}
	if code, _, ok := r.GateKey(key, "127.0.0.1", "m1"); ok || code != http.StatusForbidden {
		t.Fatal("legacy task gate gained grouped access", code)
	}
	if err := db.Exec("UPDATE channels SET models='m2' WHERE id=?", bound).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.pickRelayChannel(ctx, key, staleSnapshot, "m1", 0, nil, "", nil); !errors.Is(err, routinggroup.ErrCandidatesDenied) {
		t.Fatal("stale cache bypassed current model publication", err)
	}
	if err := db.Exec("UPDATE channels SET models='m1,m2' WHERE id=?", bound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM channel_group_bindings WHERE group_id=7 AND channel_id=?", bound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO channel_group_bindings VALUES(7,?)", unbound).Error; err != nil {
		t.Fatal(err)
	}
	if ch, err := r.pickRelayChannel(ctx, key, staleSnapshot, "m1", 0, nil, "", nil); err != nil || ch.ID != unbound {
		t.Fatal("binding change did not affect next pick", ch, err)
	}
	if err := db.Exec("UPDATE org_group_grants SET status='revoked',grant_version=2 WHERE id=9").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.pickRelayChannel(ctx, key, staleSnapshot, "m1", 0, nil, "", nil); !errors.Is(err, routinggroup.ErrCandidatesDenied) {
		t.Fatal("revoked group still picked a channel", err)
	}
	if code, _, ok := r.gateRelayKey(ctx, key, "127.0.0.1", "m1"); ok || code != http.StatusForbidden {
		t.Fatal("revoked group entered relay", code)
	}
}

func TestGroupedRelayPickerRejectsKeyDriftAndPreservesLegacy(t *testing.T) {
	r, db, key, snapshot, _, unbound := groupedRelayFixture(t)
	ctx := context.Background()
	key.PolicyVersion = 2
	if _, err := r.groupedModelChannels(ctx, key, "m1"); !errors.Is(err, routinggroup.ErrCandidatesDenied) {
		t.Fatal("stale key version authorized", err)
	}
	key.PolicyVersion = 1
	if err := db.Exec("UPDATE channels SET status=0 WHERE id=?", unbound).Error; err != nil {
		t.Fatal(err)
	}
	legacy := &identity.APIKey{ID: 12, OrgID: 1, ModelAccessMode: identity.ModelAccessLegacy, PolicyVersion: 1}
	if ch, err := r.pickRelayChannel(ctx, legacy, snapshot, "m1", 0, nil, "", nil); err != nil || ch.ID != unbound {
		t.Fatal("legacy cached picker behavior changed", ch, err)
	}
}
