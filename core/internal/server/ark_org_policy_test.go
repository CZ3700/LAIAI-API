package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
)

func orgPolicyFixture(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	f, db := setupServer(t)
	if err := db.Exec(`
CREATE TABLE video_org_policies(org_id INTEGER PRIMARY KEY,async_execution_allowed BOOLEAN NOT NULL,capture_history BOOLEAN NOT NULL,retention_days INTEGER NOT NULL,updated_at DATETIME NOT NULL);
INSERT INTO organizations(id,slug,name) VALUES(1,'policy-one','Policy one'),(2,'policy-two','Policy two');
INSERT INTO users(id,email,email_canonical,role,status) VALUES(1,'policy@test.invalid','policy@test.invalid','superadmin',1);
`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	f.srv.video = &video.Service{DB: db}
	r := gin.New()
	r.PUT("/api/admin/video/org-policies/:org_id", func(c *gin.Context) {
		if c.GetHeader("X-Fixture-Actor") != "none" {
			c.Set("actor_id", uint64(1))
		}
	}, f.srv.publishVideoOrgPolicy)
	return r, db
}

func putOrgPolicy(t *testing.T, r *gin.Engine, org uint64, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", fmt.Sprintf("/api/admin/video/org-policies/%d", org), strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
	}
	return w
}

func TestVideoOrgPolicyConditionalFirstCreateAndLegacyCompatibility(t *testing.T) {
	r, db := orgPolicyFixture(t)
	body := gin.H{"async_execution_allowed": true, "capture_history": false, "retention_days": 1, "expected_policy": videoPolicyExpectation{}}
	putOrgPolicy(t, r, 1, body, 204)
	// Both administrators could have read absence before either submitted. The
	// second must not overwrite the first administrator's explicit decisions.
	body["capture_history"] = true
	body["retention_days"] = 30
	w := putOrgPolicy(t, r, 1, body, 409)
	if !strings.Contains(w.Body.String(), `"code":"execution_policy_conflict"`) {
		t.Fatal("missing safe conflict classification")
	}
	var stored video.Policy
	if err := db.First(&stored, "org_id=1").Error; err != nil || !stored.AsyncExecutionAllowed || stored.CaptureHistory || stored.RetentionDays != 1 {
		t.Fatal("stale first-time setup overwrote policy", stored, err)
	}
	var audits int64
	if err := db.Model(&AuditLog{}).Where("action='video.policy.publish'").Count(&audits).Error; err != nil || audits != 1 {
		t.Fatal("conflict wrote an audit or successful save lost its audit", audits, err)
	}
	body["expected_policy"] = videoPolicyExpectation{Present: true, Async: true, Days: 1}
	putOrgPolicy(t, r, 1, body, 204)
	if err := db.First(&stored, "org_id=1").Error; err != nil || !stored.CaptureHistory || stored.RetentionDays != 30 {
		t.Fatal("matching precondition not published", stored, err)
	}
	// Existing integrations remain valid when they omit the new optional field.
	delete(body, "expected_policy")
	body["async_execution_allowed"] = false
	body["capture_history"] = false
	body["retention_days"] = 7
	putOrgPolicy(t, r, 1, body, 204)
	if err := db.First(&stored, "org_id=1").Error; err != nil || stored.AsyncExecutionAllowed || stored.CaptureHistory || stored.RetentionDays != 7 {
		t.Fatal("legacy publisher compatibility changed", stored, err)
	}
}

func TestVideoOrgPolicyRejectsEveryStaleDecisionAndCrossOrgPrecondition(t *testing.T) {
	for _, field := range []string{"async_execution_allowed", "capture_history", "retention_days", "presence"} {
		t.Run(field, func(t *testing.T) {
			r, db := orgPolicyFixture(t)
			initial := gin.H{"async_execution_allowed": true, "capture_history": false, "retention_days": 7}
			putOrgPolicy(t, r, 1, initial, 204)
			expected := videoPolicyExpectation{Present: true, Async: true, Days: 7}
			if field == "presence" {
				if err := db.Exec("DELETE FROM video_org_policies WHERE org_id=1").Error; err != nil {
					t.Fatal(err)
				}
			} else {
				concurrent := gin.H{"async_execution_allowed": true, "capture_history": false, "retention_days": 7}
				switch field {
				case "async_execution_allowed":
					concurrent[field] = false
				case "capture_history":
					concurrent[field] = true
				case "retention_days":
					concurrent[field] = 1
				}
				putOrgPolicy(t, r, 1, concurrent, 204)
			}
			var before []video.Policy
			var beforeAudits, afterAudits int64
			if err := db.Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			db.Model(&AuditLog{}).Count(&beforeAudits)
			stale := gin.H{"async_execution_allowed": true, "capture_history": true, "retention_days": 30, "expected_policy": expected}
			putOrgPolicy(t, r, 1, stale, 409)
			putOrgPolicy(t, r, 2, stale, 409)
			var after []video.Policy
			if err := db.Find(&after).Error; err != nil {
				t.Fatal(err)
			}
			db.Model(&AuditLog{}).Count(&afterAudits)
			a, _ := json.Marshal(before)
			b, _ := json.Marshal(after)
			if string(a) != string(b) || beforeAudits != afterAudits {
				t.Fatal("stale precondition changed a policy or audit")
			}
		})
	}
}

func TestVideoOrgPolicyConditionalSaveRemainsAuthorizedValidatedAndAtomic(t *testing.T) {
	r, db := orgPolicyFixture(t)
	body := gin.H{"async_execution_allowed": true, "capture_history": false, "retention_days": 1, "expected_policy": videoPolicyExpectation{}}
	req := httptest.NewRequest("PUT", "/api/admin/video/org-policies/1", strings.NewReader(`{"async_execution_allowed":true,"capture_history":false,"retention_days":1}`))
	req.Header.Set("X-Fixture-Actor", "none")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("individual superadmin requirement bypassed", w.Code)
	}
	for _, days := range []int{0, 31} {
		body["retention_days"] = days
		putOrgPolicy(t, r, 1, body, 422)
	}
	body["retention_days"] = 1
	body["expected_policy"] = videoPolicyExpectation{Days: 1}
	putOrgPolicy(t, r, 1, body, 422)
	body["expected_policy"] = videoPolicyExpectation{}
	if err := db.Exec("UPDATE organizations SET status=? WHERE id=1", identity.StatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	putOrgPolicy(t, r, 1, body, 422)
	if err := db.Exec("UPDATE organizations SET status=? WHERE id=1", identity.StatusActive).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_policy_audit BEFORE INSERT ON audit_logs WHEN NEW.action='video.policy.publish' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	putOrgPolicy(t, r, 1, body, 422)
	var policies, audits int64
	db.Model(&video.Policy{}).Count(&policies)
	db.Model(&AuditLog{}).Count(&audits)
	if policies != 0 || audits != 0 {
		t.Fatal("failed admission or audit left partial policy", policies, audits)
	}
}
