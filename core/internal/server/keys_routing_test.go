package server

import (
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
)

func TestCreateKeyRoutingGatePrecedesVideoConsent(t *testing.T) {
	for _, tt := range []struct {
		name      string
		mode      any
		withMode  bool
		group     any
		withGroup bool
		limits    []string
		status    int
		code      string
	}{
		{"null", nil, true, nil, false, nil, 400, "invalid_model_access_mode"},
		{"empty", "", true, nil, false, nil, 400, "invalid_model_access_mode"},
		{"upper", "LEGACY", true, nil, false, nil, 400, "invalid_model_access_mode"},
		{"space", "legacy ", true, nil, false, nil, 400, "invalid_model_access_mode"},
		{"legacy-zero", "legacy", true, 0, true, nil, 400, "invalid_routing_group"},
		{"omitted-with-group", nil, false, 1, true, nil, 400, "invalid_routing_group"},
		{"inherit", "inherit_group", true, 1, true, nil, 409, "group_routing_not_enabled"},
		{"subset", "subset", true, 1, true, []string{"m1"}, 409, "group_routing_not_enabled"},
		{"inherit-extra", "inherit_group", true, 1, true, []string{"m1"}, 400, "invalid_model_limits"},
		{"subset-empty", "subset", true, 1, true, nil, 400, "invalid_model_limits"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, db, token := keyVideoFixture(t)
			body := map[string]any{"name": "routing", "enable_video_async": true, "scopes": "video:*", "model_limits": tt.limits}
			if tt.withMode {
				body["model_access_mode"] = tt.mode
			}
			if tt.withGroup {
				body["routing_group_id"] = tt.group
			}
			w, out := f.do(t, "POST", "/api/keys", token, body)
			if w.Code != tt.status || out["code"] != tt.code {
				t.Fatalf("got %d %v", w.Code, out)
			}
			for _, model := range []any{&identity.APIKey{}, &video.Policy{}, &routinggroup.Event{}} {
				var n int64
				if err := db.Model(model).Count(&n).Error; err != nil || n != 0 {
					t.Fatal("rejected routing request wrote state", err, n)
				}
			}
		})
	}
}

func TestCreateKeyInheritsAdministratorDefaultGroupAndVideoScopes(t *testing.T) {
	f, db, token := keyVideoFixture(t)
	if err := db.Exec(`INSERT INTO video_org_policies(org_id,async_execution_allowed,capture_history,retention_days,updated_at) VALUES(1,1,0,1,CURRENT_TIMESTAMP);
INSERT INTO routing_groups(id,slug,name,description,owner_org_id,status,policy_version,created_at,updated_at) VALUES(7,'seedance','Seedance','',0,'enabled',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
INSERT INTO org_group_grants(id,org_id,group_id,status,grant_version,default_slot,created_at,updated_at) VALUES(9,1,7,'enabled',1,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);`).Error; err != nil {
		t.Fatal(err)
	}
	if w, out := f.do(t, "GET", "/api/keys/video-access", token, nil); w.Code != 200 || out["grouped_issuance_enabled"] != true || out["default_group_configured"] != true || out["default_group_state"] != "active" || out["default_group"].(map[string]any)["id"] != float64(7) {
		t.Fatal("grouped readiness", w.Code, out)
	}
	w, out := f.do(t, "POST", "/api/keys", token, map[string]any{"name": "automatic"})
	if w.Code != 201 {
		t.Fatal("grouped key create", w.Code, out)
	}
	var key identity.APIKey
	if err := db.First(&key).Error; err != nil || key.ModelAccessMode != identity.ModelAccessInheritGroup || key.RoutingGroupID == nil || *key.RoutingGroupID != 7 || key.ModelLimits != "" || key.Scopes != "video:read video:write" {
		t.Fatal("grouped key persistence", key, err)
	}
	plain, _ := out["key"].(string)
	if _, err := identity.VerifyKey(db, plain, time.Now()); err != nil {
		t.Fatal("grouped key not verified", err)
	}
	if key.AllowsModel("unpublished") {
		t.Fatal("grouped key bypassed action membership")
	}
	if w, out := f.do(t, "POST", "/api/keys", token, map[string]any{"name": "bad", "model_limits": []string{"seedance-2.5"}}); w.Code != 400 || out["code"] != "invalid_model_limits" {
		t.Fatal("caller whitelist overrode admin group", w.Code, out)
	}
	if err := db.Exec("UPDATE routing_groups SET status='disabled',policy_version=2 WHERE id=7").Error; err != nil {
		t.Fatal(err)
	}
	if w, out := f.do(t, "GET", "/api/keys/video-access", token, nil); w.Code != 200 || out["grouped_issuance_enabled"] != false || out["default_group_configured"] != true || out["default_group_state"] != "inactive" || out["default_group"].(map[string]any)["id"] != float64(7) {
		t.Fatal("inactive default readiness", w.Code, out)
	}
	if w, out := f.do(t, "POST", "/api/keys", token, map[string]any{"name": "stale"}); w.Code != 403 || out["code"] != "routing_group_inactive" {
		t.Fatal("disabled default fell back to legacy", w.Code, out)
	}
}

func TestGroupedKeyRequiresAdministratorVideoPolicy(t *testing.T) {
	for _, scenario := range []string{"missing_policy", "disabled_policy", "feature_disabled"} {
		t.Run(scenario, func(t *testing.T) {
			f, db, token := keyVideoFixture(t)
			if err := db.Exec(`INSERT INTO routing_groups(id,slug,name,description,owner_org_id,status,policy_version,created_at,updated_at) VALUES(7,'seedance','Seedance','',0,'enabled',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
INSERT INTO org_group_grants(id,org_id,group_id,status,grant_version,default_slot,created_at,updated_at) VALUES(9,1,7,'enabled',1,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);`).Error; err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "disabled_policy":
				if err := db.Exec("INSERT INTO video_org_policies VALUES(1,0,0,1,CURRENT_TIMESTAMP)").Error; err != nil {
					t.Fatal(err)
				}
			case "feature_disabled":
				if err := db.Exec("INSERT INTO org_entitlements(org_id,feature_video) VALUES(1,0)").Error; err != nil {
					t.Fatal(err)
				}
			}
			w, out := f.do(t, "POST", "/api/keys", token, map[string]any{"name": "must-fail", "enable_video_async": true})
			if w.Code != 403 || out["code"] == nil {
				t.Fatal("grouped video policy bypass", w.Code, out)
			}
			var keys int64
			if err := db.Model(&identity.APIKey{}).Count(&keys).Error; err != nil || keys != 0 {
				t.Fatal("failed issue wrote key", err, keys)
			}
		})
	}
}

func TestDefaultGroupSwitchAffectsOnlyNewKeys(t *testing.T) {
	f, db, token := keyVideoFixture(t)
	if err := db.Exec(`INSERT INTO video_org_policies(org_id,async_execution_allowed,capture_history,retention_days,updated_at) VALUES(1,1,0,1,CURRENT_TIMESTAMP);
INSERT INTO routing_groups(id,slug,name,description,owner_org_id,status,policy_version,created_at,updated_at) VALUES(7,'first','First','',0,'enabled',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),(8,'second','Second','',0,'enabled',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
INSERT INTO org_group_grants(id,org_id,group_id,status,grant_version,default_slot,created_at,updated_at) VALUES(9,1,7,'enabled',1,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),(10,1,8,'enabled',1,NULL,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);`).Error; err != nil {
		t.Fatal(err)
	}
	first, firstResult := f.do(t, "POST", "/api/keys", token, map[string]any{"name": "first"})
	if first.Code != 201 {
		t.Fatal("first key", first.Code, firstResult)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE org_group_grants SET default_slot=NULL, grant_version=2 WHERE id=9").Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE org_group_grants SET default_slot=1, grant_version=2 WHERE id=10").Error
	}); err != nil {
		t.Fatal(err)
	}
	second, secondResult := f.do(t, "POST", "/api/keys", token, map[string]any{"name": "second"})
	if second.Code != 201 {
		t.Fatal("second key", second.Code, secondResult)
	}
	var keys []identity.APIKey
	if err := db.Order("id ASC").Find(&keys).Error; err != nil || len(keys) != 2 || keys[0].RoutingGroupID == nil || *keys[0].RoutingGroupID != 7 || keys[1].RoutingGroupID == nil || *keys[1].RoutingGroupID != 8 {
		t.Fatal("default switch changed existing key", err, keys)
	}
	if _, err := identity.VerifyKey(db, firstResult["key"].(string), time.Now()); err != nil {
		t.Fatal("old group grant should remain usable", err)
	}
}

func TestCreateKeyLegacyExplicitNullAndReadback(t *testing.T) {
	f, db, token := keyVideoFixture(t)
	for _, body := range []map[string]any{{"name": "omitted"}, {"name": "null-group", "routing_group_id": nil}, {"name": "legacy", "model_access_mode": "legacy", "routing_group_id": nil}} {
		w, out := f.do(t, "POST", "/api/keys", token, body)
		if w.Code != 201 {
			t.Fatal(w.Code, out)
		}
	}
	w, out := f.do(t, "GET", "/api/keys", token, nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	keys := out["keys"].([]any)
	if len(keys) != 3 {
		t.Fatal("missing keys")
	}
	for _, row := range keys {
		k := row.(map[string]any)
		if k["model_access_mode"] != "legacy" || k["routing_group_id"] != nil || k["policy_version"] != float64(1) || k["grouped_key_issuance_enabled"] != false {
			t.Fatal("routing readback", k)
		}
		if _, ok := k["key_hash"]; ok {
			t.Fatal("hash exposed")
		}
	}
	var n int64
	if err := db.Model(&routinggroup.Event{}).Where("actor_user_id=1 AND event_kind='key_issued'").Count(&n).Error; err != nil || n != 3 {
		t.Fatal("missing issuance audit")
	}
}
