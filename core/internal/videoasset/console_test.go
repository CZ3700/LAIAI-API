package videoasset

import (
	"bytes"
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const consoleTestSchema = `
CREATE TABLE video_asset_details(resource_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,description TEXT NOT NULL,source TEXT NOT NULL,last_synced_at DATETIME,updated_at DATETIME NOT NULL);
CREATE TABLE video_portrait_labels(session_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,name TEXT NOT NULL,description TEXT NOT NULL,name_synced BOOLEAN NOT NULL);
CREATE TABLE video_asset_sync_jobs(id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,scope_id TEXT NOT NULL,scope_version INTEGER NOT NULL,account_id TEXT NOT NULL,region TEXT NOT NULL,project TEXT NOT NULL,mode TEXT NOT NULL,kind TEXT NOT NULL,phase TEXT NOT NULL,status TEXT NOT NULL,group_id TEXT NOT NULL,after_group_id TEXT NOT NULL,checkpoint_id TEXT NOT NULL,seen_hashes TEXT NOT NULL,idempotency_hash TEXT NOT NULL,pages INTEGER NOT NULL,items INTEGER NOT NULL,fence INTEGER NOT NULL,lease_until DATETIME,error_code TEXT NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,idempotency_hash));
CREATE TABLE video_asset_discoveries(id TEXT PRIMARY KEY,scope_id TEXT NOT NULL,scope_version INTEGER NOT NULL,account_id TEXT NOT NULL,region TEXT NOT NULL,project TEXT NOT NULL,provider_id TEXT NOT NULL,kind TEXT NOT NULL,name TEXT NOT NULL,description TEXT NOT NULL,status TEXT NOT NULL,assigned_org INTEGER NOT NULL,group_id TEXT NOT NULL,version INTEGER NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(scope_id,provider_id));
CREATE TABLE video_media_uploads(id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,user_id INTEGER NOT NULL,key_id INTEGER NOT NULL,media_type TEXT NOT NULL,status TEXT NOT NULL,idempotency_hash TEXT NOT NULL,body_hash TEXT NOT NULL,object_ref TEXT NOT NULL,object_sha256 TEXT NOT NULL,mime TEXT NOT NULL,bytes INTEGER NOT NULL,duration_ms INTEGER NOT NULL,width INTEGER NOT NULL,height INTEGER NOT NULL,fps_num INTEGER NOT NULL,fps_den INTEGER NOT NULL,error_code TEXT NOT NULL,expires_at DATETIME NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,idempotency_hash));
`

func TestConsoleCallerRenameDelete(t *testing.T) {
	s, _, now, _ := setup(t)
	ctx := context.Background()
	c := CreateContext{CallerKind: "session_user", OrgID: 1, UserID: 22, RequestID: "req-session", IdempotencyKey: "create-session"}
	group, _, err := s.CreateGroup(ctx, c, GroupCreate{Kind: "virtual", Name: "name", Description: "description"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ConsoleGroups(ctx, 1, ListFilter{Kind: "virtual", Limit: 20})
	if err != nil || len(page.Data) != 1 || page.Data[0].Description != "description" {
		t.Fatal(page, err)
	}
	desc := "new description"
	c.RequestID = "req-rename"
	c.IdempotencyKey = "rename"
	op, err := s.RenameResource(ctx, c, group.ID, true, Rename{Name: "new name", Description: &desc, ExpectedVersion: page.Data[0].Version})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	op, err = s.GetOperation(ctx, 1, op.ID)
	if err != nil || op.Status != "confirmed" {
		t.Fatal(op, err)
	}
	if _, err = s.GetOperation(ctx, 2, op.ID); err != ErrNotFound {
		t.Fatal("cross-org operation", err)
	}
	asset := readyAsset(t, s, now, group.ID, "delete-single")
	c.RequestID = "req-delete"
	c.IdempotencyKey = "delete-single"
	op, _, err = s.DeleteAsset(ctx, c, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetAsset(ctx, 1, asset.ID); err != ErrNotFound {
		t.Fatal(err)
	}
}
func TestConsoleSessionCannotReadAnotherH5(t *testing.T) {
	s, _, _, _ := setup(t)
	c := CreateContext{CallerKind: "session_user", OrgID: 1, UserID: 22, RequestID: "req-h5", IdempotencyKey: "h5"}
	p, _, err := s.CreatePortrait(context.Background(), c, PortraitCreate{Name: "portrait", CallbackURL: "https://customer.example/return"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.UserID = 23
	if _, err = s.GetPortraitForCaller(context.Background(), c, p.ID, true); err != ErrSecret {
		t.Fatal(err)
	}
	c.UserID = 22
	got, err := s.GetPortraitForCaller(context.Background(), c, p.ID, true)
	if err != nil || got.AuthorizationURL == "" {
		t.Fatal(got, err)
	}
}
func TestPublicAddressFilter(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "100.64.0.1"} {
		if publicIP(net.ParseIP(raw)) {
			t.Fatal(raw)
		}
	}
	if !strings.HasPrefix(extractProviderID([]byte(`{"Id":"group1"}`)), "group") {
		t.Fatal("provider parser")
	}
}

func syncReply(req *http.Request, result any) *http.Response {
	raw := bodyJSON(map[string]any{"ResponseMetadata": map[string]string{"RequestId": "sync-fixture", "Action": req.URL.Query().Get("Action"), "Version": managementVersion, "Service": "ark", "Region": "cn-beijing"}, "Result": result})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: http.Header{}, Request: req}
}
func TestSyncDiscoveryDoesNotGrantTenantAccess(t *testing.T) {
	s, db, now, _ := setup(t)
	if err := s.ConfigureConsole(ConsoleOptions{}); err != nil {
		t.Fatal(err)
	}
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		return syncReply(req, map[string]any{"Items": []any{map[string]string{"Id": "official-real-import", "Name": "external", "Description": "official", "GroupType": "LivenessFace", "ProjectName": "project1"}}}), nil
	})})
	ctx := context.Background()
	c := createContext("discover")
	job, err := s.CreateSyncJob(ctx, c, "real_person", "discover")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.processSyncJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetSyncJob(ctx, 1, job.ID, false); err != ErrNotFound {
		t.Fatal("discovery leaked", err)
	}
	page, err := s.ConsoleGroups(ctx, 1, ListFilter{Limit: 20})
	if err != nil || page.Total != 0 {
		t.Fatal("discovered group assigned automatically", page, err)
	}
	ds, err := s.ListDiscoveries(ctx, "scope1")
	if err != nil || len(ds) != 1 {
		t.Fatal(ds, err)
	}
	c.OrgID = 2
	c.RequestID = "req-assign"
	c.IdempotencyKey = "assign"
	group, err := s.AssignDiscovery(ctx, c, ds[0].ID, ds[0].Version, "evidence1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetGroup(ctx, 1, group.ID); err != ErrNotFound {
		t.Fatal("cross-tenant import visible", err)
	}
	if group.AuthorizationStatus != "needs_review" {
		t.Fatal(group)
	}
	var row Group
	if db.Where("group_id = ?", group.ID).Take(&row).Error != nil {
		t.Fatal("group absent")
	}
	var p Portrait
	if db.Where("session_id = ?", row.PortraitID).Take(&p).Error != nil || p.SessionStatus != "imported_pending" {
		t.Fatal("H5 success was fabricated", p.SessionStatus)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		return s.VerifyLicenseTx(tx, 2, p.ID, LicenseEvidence{ActorID: "reviewer", EvidenceRef: "evidence2", Source: "verified_provider_license", Purpose: "video_generation", ValidFrom: *now, ValidUntil: now.Add(time.Hour), VerifiedAt: *now, ReverifyDueAt: now.Add(time.Hour)})
	})
	if err != nil {
		t.Fatal(err)
	}
	group, err = s.GetGroup(ctx, 2, group.ID)
	if err != nil || group.AuthorizationStatus != "authorized" {
		t.Fatal(group, err)
	}
}
func TestSyncPaginationCycleKeepsCheckpointAndFails(t *testing.T) {
	s, _, _, _ := setup(t)
	_ = s.ConfigureConsole(ConsoleOptions{})
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		if _, bad := body["PageNumber"]; bad {
			t.Fatal("mixed pagination")
		}
		return syncReply(req, map[string]any{"Items": []any{}, "NextToken": "same-token"}), nil
	})})
	ctx := context.Background()
	job, err := s.CreateSyncJob(ctx, createContext("cycle"), "virtual", "owned")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.processSyncJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.processSyncJobs(ctx); err != nil {
		t.Fatal(err)
	}
	job, err = s.GetSyncJob(ctx, 1, job.ID, false)
	if err != nil || job.Status != "failed" || job.ErrorCode != "pagination_cycle" || job.CheckpointID == "" {
		t.Fatal(job, err)
	}
}
func TestUnknownDeletePreservesBarrier(t *testing.T) {
	s, db, now, provider := setup(t)
	g := readyGroup(t, s, "barrier")
	a := readyAsset(t, s, now, g.ID, "barrier")
	provider.unknown = true
	op, _, err := s.DeleteAsset(context.Background(), createContext("delete-unknown"), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, err = s.GetOperation(context.Background(), 1, op.ID)
	if err != nil || op.Status != "unknown" {
		t.Fatal(op, err)
	}
	var row Asset
	if db.Where("asset_id = ?", a.ID).Take(&row).Error != nil || row.Status != StatusDeleting || row.DeletedAt != nil {
		t.Fatal("delete barrier lost")
	}
}
func TestPublicMediaSignatureAndRange(t *testing.T) {
	root := t.TempDir()
	id := "media_fixture"
	if err := os.WriteFile(filepath.Join(root, id), []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &MediaStore{root: root, base: "https://media.example/source", key: bytes.Repeat([]byte{1}, 32)}
	good := m.url(1, id, time.Now().Add(time.Minute))
	request := httptest.NewRequest("GET", good, nil)
	request.Header.Set("Range", "bytes=2-4")
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, request)
	if rec.Code != 206 || rec.Body.String() != "234" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	bad := strings.Replace(good, "org=1", "org=2", 1)
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", bad, nil))
	if rec.Code != 404 {
		t.Fatal("signature not tenant bound")
	}
	if _, err := m.path("../escape"); err == nil {
		t.Fatal("path traversal allowed")
	}
}
