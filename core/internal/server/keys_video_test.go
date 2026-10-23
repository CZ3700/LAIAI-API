package server

import (
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
)

func keyVideoFixture(t *testing.T) (*gin999, *gorm.DB, string) {
	t.Helper()
	f, db := setupServer(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(`CREATE TABLE video_org_policies(org_id INTEGER PRIMARY KEY,async_execution_allowed BOOLEAN NOT NULL,capture_history BOOLEAN NOT NULL,retention_days INTEGER NOT NULL,updated_at DATETIME NOT NULL);
INSERT INTO organizations(id,slug,name,status) VALUES(1,'key-video-one','One',1),(2,'key-video-two','Two',1);
INSERT INTO users(id,email,email_canonical,role,status) VALUES(1,'video-owner@test.invalid','video-owner@test.invalid','user',1);
INSERT INTO org_members(org_id,user_id,role) VALUES(1,1,'owner');`).Error; err != nil {
		t.Fatal(err)
	}
	f.srv.video = &video.Service{DB: db}
	token, _, err := identity.IssueSessionToken(testSecret, 1, 1, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return f, db, token
}

func TestKeyVideoFirstConsentIsAtomicAndReusable(t *testing.T) {
	f, db, token := keyVideoFixture(t)
	w, out := f.do(t, "GET", "/api/keys/video-access", token, nil)
	if w.Code != 200 || out["state"] != "not_configured" || out["can_enable"] != true {
		t.Fatalf("readiness: %d %v", w.Code, out)
	}
	body := map[string]any{"name": "video", "scopes": "video:read video:write", "model_limits": []string{"seedance-2.0-mini"}, "quota_limit": "1.25", "rpm_limit": 3}
	w, out = f.do(t, "POST", "/api/keys", token, body)
	if w.Code != 403 || out["code"] != "video_execution_consent_required" {
		t.Fatalf("missing consent: %d %v", w.Code, out)
	}
	body["enable_video_async"] = true
	w, _ = f.do(t, "POST", "/api/keys", token, body)
	if w.Code != 201 {
		t.Fatalf("owner consent: %d", w.Code)
	}
	var policy video.Policy
	if e := db.First(&policy, "org_id=1").Error; e != nil || !policy.AsyncExecutionAllowed || policy.CaptureHistory || policy.RetentionDays != 1 {
		t.Fatal("policy", policy, e)
	}
	var key identity.APIKey
	if e := db.First(&key).Error; e != nil || key.Scopes != "video:read video:write" || key.ModelLimits != "seedance-2.0-mini" || key.QuotaLimit.String() != "1.25" || key.RPMLimit != 3 {
		t.Fatal("key restrictions lost")
	}
	delete(body, "enable_video_async")
	w, _ = f.do(t, "POST", "/api/keys", token, body)
	if w.Code != 201 {
		t.Fatal("future key blocked", w.Code)
	}
	var audits, policies int64
	db.Model(&AuditLog{}).Where("action='video.policy.enable_on_key_create'").Count(&audits)
	db.Model(&video.Policy{}).Count(&policies)
	if audits != 1 || policies != 1 {
		t.Fatal("duplicate consent or cross-org write", audits, policies)
	}
	w, out = f.do(t, "GET", "/api/keys/video-access", token, nil)
	if w.Code != 200 || out["state"] != "enabled" {
		t.Fatal("readback", out)
	}
}

func TestKeyVideoConsentGuardsAndRollback(t *testing.T) {
	for _, scenario := range []string{"member", "disabled", "entitlement", "key_limit", "audit_failure", "revoked_member", "inactive_user", "inactive_org", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f, db, token := keyVideoFixture(t)
			statement := map[string]string{
				"member":         "UPDATE org_members SET role='member' WHERE org_id=1",
				"disabled":       "INSERT INTO video_org_policies VALUES(1,0,1,7,CURRENT_TIMESTAMP)",
				"entitlement":    "INSERT INTO org_entitlements(org_id,feature_video) VALUES(1,0)",
				"key_limit":      "INSERT INTO org_entitlements(org_id,max_api_keys) VALUES(1,1); INSERT INTO api_keys(org_id,user_id,name,prefix,key_hash,scopes,status) VALUES(1,1,'existing','fixture','fixture-hash','',1)",
				"audit_failure":  "CREATE TRIGGER reject_key_consent BEFORE INSERT ON audit_logs WHEN NEW.action='video.policy.enable_on_key_create' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END",
				"revoked_member": "DELETE FROM org_members WHERE org_id=1",
				"inactive_user":  "UPDATE users SET status=0 WHERE id=1",
				"inactive_org":   "UPDATE organizations SET status=0 WHERE id=1",
			}[scenario]
			if statement != "" {
				if e := db.Exec(statement).Error; e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "unavailable" {
				f.srv.video = nil
			}
			w, _ := f.do(t, "POST", "/api/keys", token, map[string]any{"scopes": "video:*", "enable_video_async": true})
			if w.Code < 400 {
				t.Fatal("guard bypass", w.Code)
			}
			var keys, policies, audits int64
			db.Model(&identity.APIKey{}).Count(&keys)
			db.Model(&video.Policy{}).Count(&policies)
			db.Model(&AuditLog{}).Where("action='video.policy.enable_on_key_create'").Count(&audits)
			wantKeys, wantPolicies := int64(0), int64(0)
			if scenario == "key_limit" {
				wantKeys = 1
			}
			if scenario == "disabled" {
				wantPolicies = 1
			}
			if keys != wantKeys || policies != wantPolicies || audits != 0 {
				t.Fatal("partial write", keys, policies, audits)
			}
		})
	}
}

func TestKeyVideoDoesNotChangeHistoryOrReadOnlyKeys(t *testing.T) {
	f, db, token := keyVideoFixture(t)
	for _, scope := range []string{"", "video:read"} {
		w, _ := f.do(t, "POST", "/api/keys", token, map[string]any{"scopes": scope})
		if w.Code != 201 {
			t.Fatal("legacy/read key blocked", w.Code)
		}
	}
	var count int64
	db.Model(&video.Policy{}).Count(&count)
	if count != 0 {
		t.Fatal("implicit consent")
	}
	if err := db.Exec("INSERT INTO video_org_policies VALUES(1,1,1,30,CURRENT_TIMESTAMP)").Error; err != nil {
		t.Fatal(err)
	}
	w, _ := f.do(t, "POST", "/api/keys", token, map[string]any{"scopes": "video:write", "enable_video_async": true})
	if w.Code != 201 {
		t.Fatal(w.Code)
	}
	var policy video.Policy
	db.First(&policy, "org_id=1")
	if !policy.CaptureHistory || policy.RetentionDays != 30 {
		t.Fatal("history overwritten")
	}
	w, _ = f.do(t, "POST", "/api/keys", token, map[string]any{"scopes": "video:read", "enable_video_async": true})
	if w.Code != 400 {
		t.Fatal("non-write consent accepted")
	}
}
