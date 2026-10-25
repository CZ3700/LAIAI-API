package server

import (
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"github.com/laiai/laiai-next/core/internal/video"
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
