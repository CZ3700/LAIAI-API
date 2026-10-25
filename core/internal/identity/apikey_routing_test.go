package identity

import (
	"errors"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"github.com/shopspring/decimal"
)

func TestKeyRoutingInputIsClosedBeforeDatabase(t *testing.T) {
	one, zero := uint64(1), uint64(0)
	for _, tt := range []struct {
		name, mode, limits string
		omitted            bool
		group              *uint64
		want               error
	}{
		{"omitted", "", "", true, nil, nil}, {"legacy", "legacy", "", false, nil, nil},
		{"empty", "", "", false, nil, ErrModelAccessMode}, {"upper", "LEGACY", "", false, nil, ErrModelAccessMode}, {"space", "legacy ", "", false, nil, ErrModelAccessMode},
		{"legacy-group", "legacy", "", false, &one, ErrRoutingGroup}, {"legacy-zero", "legacy", "", false, &zero, ErrRoutingGroup},
		{"inherit-no-group", "inherit_group", "", false, nil, ErrRoutingGroup}, {"inherit-extra", "inherit_group", "model", false, &one, ErrModelAccessLimits},
		{"subset-empty", "subset", "", false, &one, ErrModelAccessLimits}, {"inherit-disabled", "inherit_group", "", false, &one, ErrGroupRoutingDisabled}, {"subset-disabled", "subset", "model", false, &one, ErrGroupRoutingDisabled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mode *string
			if !tt.omitted {
				mode = &tt.mode
			}
			got, err := ValidateKeyRouting(mode, tt.group, tt.limits)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v want %v", err, tt.want)
			}
			if err == nil && got != ModelAccessLegacy {
				t.Fatal("legacy default missing")
			}
			if err != nil { // nil DB proves no quota/random/key/entitlement path ran.
				plain, key, issueErr := IssueKey(nil, IssueKeyInput{OrgID: 1, ModelAccessMode: mode, RoutingGroupID: tt.group, ModelLimits: tt.limits})
				if !errors.Is(issueErr, tt.want) || plain != "" || key != nil {
					t.Fatal("direct issuer bypass")
				}
			}
		})
	}
}

func TestKeyRoutingStoredValuesFailClosed(t *testing.T) {
	db := setupDB(t)
	if err := db.Exec("INSERT INTO organizations(id,slug,name,status) VALUES(1,'routing','routing',1)").Error; err != nil {
		t.Fatal(err)
	}
	plain, key, err := IssueKey(db, IssueKeyInput{OrgID: 1, ModelLimits: "m1,m2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		mode    string
		version uint64
		group   any
	}{
		{"", 1, nil}, {"LEGACY", 1, nil}, {"legacy ", 1, nil}, {"legacy", 0, nil}, {"legacy", 1, 1}, {"inherit_group", 1, 1}, {"subset", 1, 1}, {"future", 1, nil},
	} {
		if err := db.Model(&APIKey{}).Where("id=?", key.ID).Updates(map[string]any{"model_access_mode": tt.mode, "policy_version": tt.version, "routing_group_id": tt.group}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyKey(db, plain, time.Now()); !errors.Is(err, ErrKeyInvalid) {
			t.Fatalf("accepted stored mode %q: %v", tt.mode, err)
		}
		var stored APIKey
		if err := db.First(&stored, key.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.AllowsModel("m1") || stored.AllowsModel("anything") {
			t.Fatal("deep model guard bypass")
		}
	}
	if (&APIKey{}).AllowsModel("anything") {
		t.Fatal("zero record defaulted to unlimited")
	}
	if err := db.Model(&APIKey{}).Where("id=?", key.ID).Updates(map[string]any{"model_access_mode": "legacy", "policy_version": 1, "routing_group_id": nil}).Error; err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyKey(db, plain, time.Now())
	if err != nil || !verified.AllowsModel("m1") || verified.AllowsModel("m3") {
		t.Fatal("legacy whitelist changed")
	}
}

func TestKeyPolicyEventAndVersionAtomicity(t *testing.T) {
	db := setupDB(t)
	if err := db.Exec("INSERT INTO organizations(id,slug,name,status) VALUES(1,'routing','routing',1)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER refuse_key_event BEFORE INSERT ON routing_policy_events BEGIN SELECT RAISE(ABORT,'event unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	plain, key, err := IssueKey(db, IssueKeyInput{OrgID: 1})
	if err == nil || plain != "" || key != nil {
		t.Fatal("event failure returned key")
	}
	var count int64
	if err := db.Model(&APIKey{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("key survived failed event")
	}
	if err := db.Exec("DROP TRIGGER refuse_key_event").Error; err != nil {
		t.Fatal(err)
	}
	plain, key, err = IssueKey(db, IssueKeyInput{OrgID: 1, PolicyAudit: routinggroup.Audit{ActorUserID: 7, RequestID: "test_request"}})
	if err != nil || plain == "" {
		t.Fatal(err)
	}
	if key.ModelAccessMode != "legacy" || key.RoutingGroupID != nil || key.PolicyVersion != 1 {
		t.Fatal("implicit defaults")
	}
	if err := AddKeyUsageSigned(db, key.ID, decimal.RequireFromString("0.25")); err != nil {
		t.Fatal(err)
	}
	if err := db.First(key, key.ID).Error; err != nil || key.PolicyVersion != 1 {
		t.Fatal("usage changed policy version")
	}
	if err := RevokeKeyWithAudit(db, key.ID, 2, routinggroup.Audit{ActorUserID: 7}); err == nil {
		t.Fatal("cross org revoke")
	}
	if err := db.Exec(`CREATE TRIGGER refuse_revoke_event BEFORE INSERT ON routing_policy_events WHEN NEW.event_kind='key_revoked' BEGIN SELECT RAISE(ABORT,'event unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := RevokeKey(db, key.ID); err == nil {
		t.Fatal("revoke did not fail")
	}
	if _, err := VerifyKey(db, plain, time.Now()); err != nil {
		t.Fatal("event failure partially revoked", err)
	}
	if err := db.Exec("DROP TRIGGER refuse_revoke_event").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RevokeKeyWithAudit(db, key.ID, 1, routinggroup.Audit{ActorUserID: 7}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(key, key.ID).Error; err != nil || key.PolicyVersion != 2 || key.Status != StatusDisabled {
		t.Fatal("revoke not exactly once")
	}
	var events []routinggroup.Event
	if err := db.Order("id").Find(&events).Error; err != nil || len(events) != 2 {
		t.Fatal("event count", err)
	}
	if events[0].ActorUserID != 7 || events[1].AggregateVersion != 2 {
		t.Fatal("event provenance")
	}
}
