package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

func TestMySQLRequestLogsCaptureFailuresReplayAndHistoryBoundary(t *testing.T) {
	f := mysqlFixture(t)
	r := gin.New()
	r.POST("/v1/video/generations", f.s.Middleware(), f.s.HandleCreate)
	call := func(raw, key string) *httptest.ResponseRecorder {
		q := httptest.NewRequest("POST", "/v1/video/generations", strings.NewReader(raw))
		q.Header.Set("Authorization", "Bearer "+f.plain)
		q.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		return w
	}
	failed := call(`{"model":"seedance-2.0","content":[{"type":"text","text":"request rejected"}],"unexpected":{"password":"fixture-secret","nested":true}}`, "log-invalid-1")
	if failed.Code != 422 && failed.Code != 400 {
		t.Fatalf("failed request %d %s", failed.Code, failed.Body.String())
	}
	failedID := failed.Header().Get("X-Request-Id")
	access := invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: "user_1", ReasonCode: "request_debugging", OperationID: "testread"}
	read, e := f.s.ReadRequestHistory(context.Background(), f.key.OrgID, failedID, invocation.VideoClientRequest, "", access)
	must(t, e)
	if !strings.Contains(string(read.Body), `"nested":true`) || strings.Contains(string(read.Body), "fixture-secret") {
		t.Fatal("received rejected JSON not retained safely")
	}
	if read.Representation != "received_json_redacted" {
		t.Fatal(read.Representation)
	}
	body := `{"model":"seedance-2.0","content":[{"type":"text","text":"original creation"}]}`
	first := call(body, "log-replay-1")
	if first.Code != 202 {
		t.Fatalf("create %d %s", first.Code, first.Body.String())
	}
	second := call(body, "log-replay-1")
	if second.Code != 202 {
		t.Fatalf("replay %d %s", second.Code, second.Body.String())
	}
	firstID, secondID := first.Header().Get("X-Request-Id"), second.Header().Get("X-Request-Id")
	a, e := f.s.RequestLogDetail(context.Background(), f.key.OrgID, firstID, false)
	must(t, e)
	b, e := f.s.RequestLogDetail(context.Background(), f.key.OrgID, secondID, false)
	must(t, e)
	if a.Request.TaskID == "" || a.Request.TaskID != b.Request.TaskID || a.Request.ReplayState != "new" || b.Request.ReplayState != "replayed" || b.Request.OriginRequestID != firstID {
		t.Fatalf("bad relation %+v %+v", a.Request, b.Request)
	}
	if a.Billing.NetChargedAmount != nil || a.Request.HTTPStatus != 202 {
		t.Fatal("pending represented as charged")
	}
	if _, e = f.s.ReadRequestHistory(context.Background(), f.key.OrgID, firstID, invocation.VideoEffectiveRequest, "", access); !errors.Is(e, ErrForbidden) {
		t.Fatal("org read effective body", e)
	}
	access.ActorRole = invocation.VideoRoleRoot
	effective, e := f.s.ReadRequestHistory(context.Background(), f.key.OrgID, firstID, invocation.VideoEffectiveRequest, "", access)
	must(t, e)
	if !json.Valid(effective.Body) || effective.Representation != "effective_json_redacted" {
		t.Fatal("effective history unavailable")
	}
	if _, e = f.s.RequestLogDetail(context.Background(), f.key.OrgID+100000, firstID, true); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-org log leak", e)
	}
	page, e := f.s.ListRequestLogs(context.Background(), f.key.OrgID, RequestLogFilter{Operation: "create_task"}, "", 1)
	must(t, e)
	if page.NextCursor == nil {
		t.Fatal("cursor missing")
	}
	if _, e = f.s.ListRequestLogs(context.Background(), f.key.OrgID, RequestLogFilter{Operation: "get_task"}, *page.NextCursor, 1); !errors.Is(e, ErrInvalid) {
		t.Fatal("cursor accepted another filter", e)
	}
	if _, e = f.s.ListRequestLogs(context.Background(), f.key.OrgID+1, RequestLogFilter{Operation: "create_task"}, *page.NextCursor, 1); !errors.Is(e, ErrInvalid) {
		t.Fatal("cursor accepted another tenant", e)
	}
	// Tombstones remain auditable and do not expose a content URL.
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, a.Request.TaskID).Updates(map[string]any{"deleted_at": f.now, "artifact_status": "ready"}).Error)
	deleted, e := f.s.RequestLogDetail(context.Background(), f.key.OrgID, firstID, false)
	must(t, e)
	if deleted.Task.DeletedAt == nil || deleted.Task.ContentURL != "" {
		t.Fatal("deleted audit/content boundary")
	}
	// Expired history cannot borrow the still-existing execution capsule.
	f.now = f.now.Add(31 * 24 * time.Hour)
	if _, e = f.s.ReadRequestHistory(context.Background(), f.key.OrgID, firstID, invocation.VideoEffectiveRequest, "", access); !errors.Is(e, invocation.ErrVideoExpired) {
		t.Fatal("expired body read", e)
	}
}

func TestMySQLRequestHistoryDisabledDoesNotReadExecution(t *testing.T) {
	f := mysqlFixture(t)
	must(t, f.s.DB.Model(&Policy{}).Where("org_id = ?", f.key.OrgID).Update("capture_history", false).Error)
	rid, e := randomID("request_disabled_")
	must(t, e)
	v, _, e := f.s.Create(context.Background(), f.key, rid, "log-disabled-1", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "history disabled"}}})
	must(t, e)
	org := f.key.OrgID
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		return f.s.Evidence.RequestStartTx(tx, invocation.VideoRequestStart{RequestID: rid, OrgID: &org, AuthOutcome: "authenticated", Method: "POST", Route: "/v1/video/generations", Operation: "create_task", StartedAt: f.now})
	}))
	d, e := f.s.RequestLogDetail(context.Background(), org, rid, true)
	must(t, e)
	if d.Request.TaskID != v.ID || d.Bodies[invocation.VideoEffectiveRequest].Availability != "disabled" {
		t.Fatal("disabled history descriptor", d)
	}
	_, e = f.s.ReadRequestHistory(context.Background(), org, rid, invocation.VideoEffectiveRequest, "", invocation.VideoAccess{ActorRole: invocation.VideoRoleRoot, ActorID: "user_1", ReasonCode: "request_debugging", OperationID: "disabled_read"})
	if !errors.Is(e, invocation.ErrVideoRetentionDenied) {
		t.Fatal("execution exposed as history", e)
	}
}

func TestMySQLRequestDisabledResponseKeepsOnlyPolicyMetadata(t *testing.T) {
	f := mysqlFixture(t)
	must(t, f.s.DB.Model(&Policy{}).Where("org_id = ?", f.key.OrgID).Update("capture_history", false).Error)
	r := gin.New()
	r.POST("/v1/video/generations", f.s.Middleware(), f.s.HandleCreate)
	q := httptest.NewRequest("POST", "/v1/video/generations", strings.NewReader(`{"model":"seedance-2.0","content":[{"type":"text","text":"never retained"}]}`))
	q.Header.Set("Authorization", "Bearer "+f.plain)
	q.Header.Set("Idempotency-Key", "no-history-http")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	if w.Code != 202 {
		t.Fatalf("create status %d", w.Code)
	}
	rid := w.Header().Get("X-Request-Id")
	d, err := f.s.RequestLogDetail(context.Background(), f.key.OrgID, rid, true)
	must(t, err)
	for _, section := range []string{invocation.VideoClientRequest, invocation.VideoClientResponse, invocation.VideoEffectiveRequest} {
		if d.Bodies[section].Availability != "disabled" {
			t.Fatalf("%s should be disabled: %+v", section, d.Bodies[section])
		}
	}
	var rows []invocation.VideoPayload
	must(t, f.s.DB.Where("org_id = ? AND request_id = ? AND purpose = ?", f.key.OrgID, rid, invocation.VideoPurposeHistory).Find(&rows).Error)
	if len(rows) != 3 {
		t.Fatalf("history descriptors %d", len(rows))
	}
	for _, row := range rows {
		if row.PlaintextBytes != 0 || row.CiphertextSHA256 != "" || row.WrappedKey != "" || row.ObjectRef != "" || len(row.Nonce) != 0 {
			t.Fatal("disabled descriptor retained body material")
		}
	}
}
