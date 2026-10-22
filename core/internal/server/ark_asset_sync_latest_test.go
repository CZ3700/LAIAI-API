package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoasset"
)

func TestArkAssetLatestSyncRouteTenantScopeAndStaticPath(t *testing.T) {
	f, db := setupServer(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	const schema = `
INSERT INTO organizations(id,slug,name) VALUES(1,'sync-one','Sync one'),(2,'sync-two','Sync two'),(3,'sync-empty','Sync empty');
INSERT INTO users(id,email,email_canonical) VALUES(1,'sync@test.invalid','sync@test.invalid');
INSERT INTO org_members(org_id,user_id,role) VALUES(1,1,'billing'),(2,1,'member'),(3,1,'owner');
CREATE TABLE video_asset_sync_jobs(id TEXT PRIMARY KEY,org_id INTEGER,scope_id TEXT,scope_version INTEGER,account_id TEXT,region TEXT,project TEXT,mode TEXT,kind TEXT,status TEXT,created_at DATETIME,updated_at DATETIME);
CREATE TABLE video_asset_owners(org_id INTEGER,resource_type TEXT,resource_id TEXT,owner_user_id INTEGER,owner_key_id INTEGER,manage_all BOOLEAN,PRIMARY KEY(org_id,resource_type,resource_id));
INSERT INTO video_asset_sync_jobs VALUES
('asj_owned',1,'scope1',1,'account1','cn-beijing','project1','owned','virtual','failed','2026-10-22 00:00:00','2026-10-22 00:00:00'),
('asj_foreign',2,'scope1',1,'account1','cn-beijing','project1','owned','virtual','running','2026-10-22 01:00:00','2026-10-22 01:00:00'),
('asj_discovery',1,'scope1',1,'account1','cn-beijing','project1','discover','virtual','succeeded','2026-10-22 02:00:00','2026-10-22 02:00:00'),
('asj_revision',1,'scope1',2,'account1','cn-beijing','project1','owned','virtual','running','2026-10-22 03:00:00','2026-10-22 03:00:00'),
('asj_real',1,'scope1',1,'account1','cn-beijing','project1','owned','real_person','succeeded','2026-10-22 04:00:00','2026-10-22 04:00:00');
INSERT INTO video_asset_owners SELECT org_id,'sync',id,1,0,0 FROM video_asset_sync_jobs;
`
	if err := db.Exec(schema).Error; err != nil {
		t.Fatal(err)
	}
	keys, err := crypto.NewKeyring("sync:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "sync")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := invocation.NewVideoStore(db, keys, invocation.VideoStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scope := videoasset.Scope{ID: "scope1", Version: 1, AccountID: "account1", Region: "cn-beijing", Project: "project1"}
	assets, err := videoasset.NewService(db, keys, evidence, videoasset.Options{
		HMACKeys: map[string][]byte{"h1": bytes.Repeat([]byte{8}, 32)}, ActiveHMACKey: "h1",
		ResolveScope: func(context.Context, uint64, videoasset.Scope) (videoasset.RuntimeScope, error) {
			return videoasset.RuntimeScope{Scope: scope}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// setupServer registers the real session and membership middleware without
	// optional video auditing. Inject only the read service for this route test.
	f.srv.video = &video.Service{Assets: assets}
	token := func(org uint64) string {
		t.Helper()
		value, _, err := identity.IssueSessionToken(testSecret, 1, org, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, tc := range []struct {
		name, path string
		org        uint64
		wantStatus int
		wantID     string
	}{
		{"latest static route", "/api/video/asset-sync-jobs/latest?kind=virtual", 1, 200, "asj_owned"},
		{"kind separation", "/api/video/asset-sync-jobs/latest?kind=real_person", 1, 200, "asj_real"},
		{"org query cannot override session", "/api/video/asset-sync-jobs/latest?kind=virtual&org_id=1", 2, 200, "asj_foreign"},
		{"empty collection", "/api/video/asset-sync-jobs/latest?kind=virtual", 3, 200, ""},
		{"kind required", "/api/video/asset-sync-jobs/latest", 1, 422, ""},
		{"invalid kind", "/api/video/asset-sync-jobs/latest?kind=real", 1, 422, ""},
		{"duplicate kind", "/api/video/asset-sync-jobs/latest?kind=virtual&kind=real_person", 1, 422, ""},
		{"session required", "/api/video/asset-sync-jobs/latest?kind=virtual", 0, 401, ""},
		{"existing ID route", "/api/video/asset-sync-jobs/asj_owned", 1, 200, "asj_owned"},
		{"foreign ID hidden", "/api/video/asset-sync-jobs/asj_foreign", 1, 404, ""},
		{"discovery ID hidden", "/api/video/asset-sync-jobs/asj_discovery", 1, 404, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := ""
			if tc.org != 0 {
				auth = token(tc.org)
			}
			w, _ := f.do(t, "GET", tc.path, auth, nil)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			if w.Code != 200 {
				return
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if raw, isLatest := body["job"]; isLatest {
				if w.Header().Get("Cache-Control") != "private, no-store" {
					t.Fatal("latest sync status must not be cached")
				}
				if tc.wantID == "" {
					if string(raw) != "null" {
						t.Fatalf("empty latest must be null: %s", raw)
					}
					return
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
			}
			if string(body["id"]) != `"`+tc.wantID+`"` {
				t.Fatalf("wrong job: %s", w.Body.String())
			}
			for _, hidden := range []string{"org_id", "account_id", "scope_version", "region", "idempotency_hash", "checkpoint_id", "lease_until"} {
				if _, exists := body[hidden]; exists {
					t.Fatalf("private field exposed: %s", hidden)
				}
			}
		})
	}
	// A route revision change must not restore an older version's successful job.
	scope.Version = 3
	w, out := f.do(t, "GET", "/api/video/asset-sync-jobs/latest?kind=virtual", token(1), nil)
	if w.Code != 200 || out["job"] != nil {
		t.Fatalf("old scope version was restored: %d %s", w.Code, w.Body.String())
	}
}
