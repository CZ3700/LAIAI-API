package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
)

func TestMySQLRequestLogConsoleAuthorizationAndAudit(t *testing.T) {
	f := arkBillMySQLFixture(t, "running")
	svc, e := video.NewService(f.db, f.s.keyring, []byte(strings.Repeat("h", 32)), nil)
	arkBillMust(t, e)
	svc.Now = func() time.Time { return f.now }
	f.s.video = svc
	arkBillMust(t, f.db.Create(&identity.OrgMember{OrgID: f.org, UserID: f.users[0], Role: identity.RoleOwner}).Error)
	org := f.r.Group("/api", f.s.authRequired(), f.s.requireActiveUser(), f.s.requireActiveMembership())
	su := f.r.Group("/api/admin", f.s.adminRequired(identity.PlatRoleAdmin), f.s.requireSuperadmin())
	f.s.registerArkRequestLogs(org, su)
	rid := "request-" + f.uid
	arkBillMust(t, f.db.Transaction(func(tx *gorm.DB) error {
		return svc.Evidence.RequestStartTx(tx, invocation.VideoRequestStart{RequestID: rid, OrgID: &f.org, AuthOutcome: "authenticated", Method: "POST", Route: "/v1/video/generations", Operation: "create_task", StartedAt: f.now})
	}))
	for _, direction := range []string{invocation.VideoClientRequest, invocation.VideoEffectiveRequest} {
		p, e := svc.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: f.org, RequestID: rid, Direction: direction, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: []byte(`{"prompt":"private fixture","password":"must disappear"}`), CaptureEnabled: true, ExpiresAt: f.now.Add(time.Hour), CaptureRepresentation: "received_json_redacted"})
		arkBillMust(t, e)
		arkBillMust(t, f.db.Transaction(func(tx *gorm.DB) error { _, e := svc.Evidence.StageTx(tx, p); return e }))
	}
	call := func(method, path, body, token string, shared bool, want int) *httptest.ResponseRecorder {
		q := httptest.NewRequest(method, path, strings.NewReader(body))
		q.Header.Set("Content-Type", "application/json")
		if token != "" {
			q.Header.Set("Authorization", "Bearer "+token)
		}
		if shared {
			q.Header.Set("X-Admin-Token", "shared-root-test")
		}
		w := httptest.NewRecorder()
		f.r.ServeHTTP(w, q)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	path := "/api/video/request-logs/" + rid
	meta := call("GET", path, "", f.tokens[0], false, 200)
	if strings.Contains(meta.Body.String(), "private fixture") || strings.Contains(meta.Body.String(), "wrapped_key") {
		t.Fatal("metadata decrypted content")
	}
	read := call("POST", path+"/payload-reads", `{"section":"client_request","reason_code":"request_debugging"}`, f.tokens[0], false, 200)
	if !strings.Contains(read.Body.String(), "private fixture") || strings.Contains(read.Body.String(), "must disappear") {
		t.Fatal("client body redaction failed")
	}
	call("POST", path+"/payload-reads", `{"section":"effective_request","reason_code":"request_debugging"}`, f.tokens[0], false, 403)
	call("POST", path+"/payload-reads", `{"section":"client_request","reason_code":"request_debugging","actor_role":"root"}`, f.tokens[0], false, 422)
	adminPath := fmt.Sprintf("/api/admin/video/request-logs/%s/payload-reads?org_id=%d", rid, f.org)
	call("POST", adminPath, `{"section":"effective_request","reason_code":"request_debugging"}`, "", true, 403)
	call("POST", adminPath, `{"section":"effective_request","reason_code":"request_debugging"}`, f.tokens[0], false, 200)
	call("GET", fmt.Sprintf("/api/admin/video/request-logs/%s?org_id=%d", rid, f.org+100000), "", f.tokens[0], false, 404)
	// Private asset history is creator scoped, including historical list/source
	// responses which were captured before creator filtering existed.
	for _, source := range []struct {
		suffix string
		owner  uint64
	}{{"own", f.users[0]}, {"other", f.users[1]}} {
		assetRID := "asset-" + source.suffix + "-" + f.uid
		arkBillMust(t, f.db.Transaction(func(tx *gorm.DB) error {
			return svc.Evidence.RequestStartTx(tx, invocation.VideoRequestStart{RequestID: assetRID, OrgID: &f.org, UserID: &source.owner, AuthOutcome: "authenticated", Method: "GET", Route: "/api/video/assets", Operation: "video_api", StartedAt: f.now})
		}))
		for _, direction := range []string{invocation.VideoClientRequest, invocation.VideoClientResponse} {
			p, e := svc.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: f.org, RequestID: assetRID, Direction: direction, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: []byte(`{"private_asset_fixture":"historical mixed creator response"}`), CaptureEnabled: true, ExpiresAt: f.now.Add(time.Hour), CaptureRepresentation: "response_json_redacted"})
			arkBillMust(t, e)
			arkBillMust(t, f.db.Transaction(func(tx *gorm.DB) error { _, e := svc.Evidence.StageTx(tx, p); return e }))
		}
		assetPath := "/api/video/request-logs/" + assetRID
		if source.suffix == "other" {
			call("GET", assetPath, "", f.tokens[0], false, 404)
			call("POST", assetPath+"/payload-reads", `{"section":"client_request","reason_code":"request_debugging"}`, f.tokens[0], false, 404)
		} else {
			call("POST", assetPath+"/payload-reads", `{"section":"client_request","reason_code":"request_debugging"}`, f.tokens[0], false, 200)
			call("POST", assetPath+"/payload-reads", `{"section":"client_response","reason_code":"request_debugging"}`, f.tokens[0], false, 403)
			meta := call("GET", assetPath, "", f.tokens[0], false, 200)
			if !strings.Contains(meta.Body.String(), `"client_response":{"availability":"forbidden"`) {
				t.Fatal("asset response visibility metadata disagrees with read guard", meta.Body.String())
			}
		}
		call("POST", fmt.Sprintf("/api/admin/video/request-logs/%s/payload-reads?org_id=%d", assetRID, f.org), `{"section":"client_response","reason_code":"request_debugging"}`, f.tokens[0], false, 200)
	}
	assetList := call("GET", "/api/video/request-logs?operation=all", "", f.tokens[0], false, 200)
	if strings.Contains(assetList.Body.String(), "asset-other-"+f.uid) || !strings.Contains(assetList.Body.String(), "asset-own-"+f.uid) {
		t.Fatal("private asset request list leaked a different creator")
	}
	// A failure in the existing audit channel blocks delivery even when payload storage works.
	arkBillMust(t, f.db.Callback().Create().Before("gorm:create").Register("test_log_audit_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(errors.New("fixture audit unavailable"))
		}
	}))
	denied := call("POST", path+"/payload-reads", `{"section":"client_request","reason_code":"request_debugging"}`, f.tokens[0], false, 503)
	if strings.Contains(denied.Body.String(), "private fixture") {
		t.Fatal("audit failure returned body")
	}
	arkBillMust(t, f.db.Callback().Create().Remove("test_log_audit_failure"))
	arkBillMust(t, f.db.Where("org_id = ? AND user_id = ?", f.org, f.users[0]).Delete(&identity.OrgMember{}).Error)
	call("GET", path, "", f.tokens[0], false, 403)
}

func TestMySQLSessionAuditUsesRealUserAndSkipsMediaBody(t *testing.T) {
	f := arkBillMySQLFixture(t, "running")
	svc, e := video.NewService(f.db, f.s.keyring, []byte(strings.Repeat("h", 32)), nil)
	arkBillMust(t, e)
	svc.Now = func() time.Time { return f.now }
	arkBillMust(t, f.db.Create(&video.Policy{OrgID: f.org, AsyncExecutionAllowed: true, CaptureHistory: true, RetentionDays: 1, UpdatedAt: f.now}).Error)
	r := gin.New()
	g := r.Group("/api", svc.SessionAudit(func(*gin.Context) (uint64, uint64, error) { return f.org, f.users[0], nil }))
	g.POST("/video/media-uploads/:id/complete", func(c *gin.Context) {
		raw, e := io.ReadAll(c.Request.Body)
		if e != nil || string(raw) != "raw-media-fixture" {
			t.Fatal("audit consumed media stream")
		}
		c.JSON(200, gin.H{"media_ref": "media_fixture", "authorization_url": "https://example.test/secret-h5"})
	})
	q := httptest.NewRequest("POST", "/api/video/media-uploads/upload_fixture/complete", strings.NewReader("raw-media-fixture"))
	q.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var row invocation.VideoRequestLog
	arkBillMust(t, f.db.Where("org_id = ? AND request_id = ?", f.org, w.Header().Get("X-Request-Id")).Take(&row).Error)
	if row.APIKeyID != nil || row.UserID == nil || *row.UserID != f.users[0] || row.RequestPayloadID != "" || row.Outcome != "completed" {
		t.Fatal("session attribution/media capture", row)
	}
	raw, e := svc.Evidence.ReadForHistory(q.Context(), f.org, row.ResponsePayloadID, invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: "user_fixture", ReasonCode: "request_debugging", OperationID: "session_read"})
	arkBillMust(t, e)
	var data map[string]any
	arkBillMust(t, json.Unmarshal(raw, &data))
	if _, ok := data["authorization_url"]; ok {
		t.Fatal("H5 link entered response history")
	}
}
