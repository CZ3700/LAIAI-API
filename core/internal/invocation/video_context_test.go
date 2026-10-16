package invocation

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestVideoRequestBodyLinkPreservesStartAndTenant(t *testing.T) {
	s, db, _ := videoSetup(t)
	org := uint64(1)
	start := VideoRequestStart{RequestID: "request_link", OrgID: &org, AuthOutcome: "authenticated", Method: "POST", Route: "/v1/video/generations", Operation: "create_task", StartedAt: s.now()}
	if e := db.Transaction(func(tx *gorm.DB) error { return s.RequestStartTx(tx, start) }); e != nil {
		t.Fatal(e)
	}
	p := videoStage(t, s, db, VideoPayloadInput{OrgID: org, RequestID: start.RequestID, Direction: VideoClientRequest, Purpose: VideoPurposeHistory, SchemaVersion: 1, Body: []byte(`{"model":"seedance-2.0","unrecognized":{"value":true},"password":"secret"}`), CaptureEnabled: true, ExpiresAt: s.now().Add(time.Hour), CaptureRepresentation: "received_json_redacted"})
	if e := db.Transaction(func(tx *gorm.DB) error { return s.LinkRequestPayloadTx(tx, org, start.RequestID, p.PayloadID()) }); e != nil {
		t.Fatal(e)
	}
	if e := db.Transaction(func(tx *gorm.DB) error { return s.RequestStartTx(tx, start) }); e != nil {
		t.Fatal("start replay changed", e)
	}
	if e := db.Transaction(func(tx *gorm.DB) error { return s.LinkRequestPayloadTx(tx, 2, start.RequestID, p.PayloadID()) }); !errors.Is(e, ErrVideoNotFound) {
		t.Fatal("cross-tenant link", e)
	}
	raw, e := s.ReadForHistory(context.Background(), org, p.PayloadID(), VideoAccess{ActorRole: VideoRoleOrgReader, ActorID: "user_1", ReasonCode: "request_debugging", OperationID: "read1"})
	if e != nil {
		t.Fatal(e)
	}
	if string(raw) != `{"model":"seedance-2.0","unrecognized":{"value":true}}` {
		t.Fatalf("received fields/redaction lost: %s", raw)
	}
	if e := db.Model(&VideoPayload{}).Where("org_id = ? AND payload_id = ?", org, p.PayloadID()).Update("capture_representation", "legacy_canonical_json").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReadForHistory(context.Background(), org, p.PayloadID(), VideoAccess{ActorRole: VideoRoleOrgReader, ActorID: "user_1", ReasonCode: "request_debugging", OperationID: "read2"}); !errors.Is(e, ErrVideoCorrupt) {
		t.Fatal("representation claim not authenticated", e)
	}
}

func TestVideoRequestContextEnrichmentCannotRebind(t *testing.T) {
	_, db, now := videoSetup(t)
	if e := db.Exec(`CREATE TABLE video_request_contexts(request_id TEXT PRIMARY KEY,org_id INTEGER,operation TEXT,public_model TEXT,task_id TEXT,operation_id TEXT,idempotency_record_id INTEGER,replay_state TEXT,origin_request_id TEXT,created_at DATETIME,updated_at DATETIME)`).Error; e != nil {
		t.Fatal(e)
	}
	in := VideoRequestContext{RequestID: "r1", OrgID: 1, Operation: "create_task", PublicModel: "seedance-2.0", CreatedAt: *now}
	put := func(v VideoRequestContext) error {
		return db.Transaction(func(tx *gorm.DB) error { return PutVideoRequestContextTx(tx, v) })
	}
	if e := put(in); e != nil {
		t.Fatal(e)
	}
	in.TaskID = "task_a"
	in.OriginRequestID = "r1"
	in.ReplayState = "new"
	if e := put(in); e != nil {
		t.Fatal(e)
	}
	if e := put(in); e != nil {
		t.Fatal(e)
	}
	in.TaskID = "task_b"
	if e := put(in); !errors.Is(e, ErrVideoConflict) {
		t.Fatal("task rebound", e)
	}
	in.OrgID = 2
	if e := put(in); !errors.Is(e, ErrVideoConflict) {
		t.Fatal("tenant rebound", e)
	}
	var n int64
	db.Model(&VideoRequestContext{}).Count(&n)
	if n != 1 {
		t.Fatal("duplicate contexts", n)
	}
}
