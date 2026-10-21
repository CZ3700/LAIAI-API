package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
)

func TestArkAssetProjectRequiresActiveSuperadminAndStrictLocalContract(t *testing.T) {
	f, db := setupServer(t)
	for _, row := range []struct {
		id     uint64
		role   string
		status int
	}{{1, identity.PlatRoleSuperadmin, 1}, {2, identity.PlatRoleAdmin, 1}, {3, identity.PlatRoleSuperadmin, 0}} {
		email := fmt.Sprintf("asset-project-%d@test.invalid", row.id)
		if err := db.Exec("INSERT INTO users(id,email,email_canonical,role,status) VALUES(?,?,?,?,?)", row.id, email, email, row.role, row.status).Error; err != nil {
			t.Fatal(err)
		}
	}
	const schema = `
CREATE TABLE video_provider_scopes(id INTEGER PRIMARY KEY AUTOINCREMENT,scope_key TEXT NOT NULL UNIQUE,account_ref TEXT NOT NULL,region TEXT NOT NULL,project_name TEXT NOT NULL,enabled INTEGER NOT NULL,fx_usd_per_cny TEXT NOT NULL,fx_version TEXT NOT NULL,fx_observed_at DATETIME NOT NULL,UNIQUE(account_ref,region,project_name));
CREATE TABLE video_asset_events(event_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,operation_id TEXT NOT NULL,kind TEXT NOT NULL,evidence_ref TEXT NOT NULL,actor_id TEXT NOT NULL,created_at DATETIME NOT NULL);
`
	if err := db.Exec(schema).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	for _, source := range []video.ProviderScope{
		{ID: 1, ScopeKey: "original", AccountRef: "2132103052", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "old-fx", FXObservedAt: now},
		{ID: 2, ScopeKey: "disabled", AccountRef: "2132103052", Region: "cn-beijing", ProjectName: "disabled", Enabled: false, FXUSDPerCNY: "0.14", FXVersion: "old-fx", FXObservedAt: now},
	} {
		if err := db.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
	}
	const valid = `{"source_provider_scope_id":1,"project_name":"LAIAI-Gateway"}`
	var registeredID uint64
	for _, tc := range []struct {
		name   string
		actor  uint64
		body   string
		status int
	}{
		{"shared_root", 0, valid, 403}, {"ordinary_admin", 2, valid, 403}, {"disabled_superadmin", 3, valid, 403}, {"missing_actor", 999, valid, 403},
		{"foreign_account_field", 1, `{"source_provider_scope_id":1,"project_name":"LAIAI-Gateway","account_ref":"another-account"}`, 422},
		{"credential_field", 1, `{"source_provider_scope_id":1,"project_name":"LAIAI-Gateway","secret_key":"forbidden"}`, 422},
		{"duplicate_property", 1, `{"source_provider_scope_id":1,"project_name":"one","project_name":"two"}`, 422},
		{"invalid_name", 1, `{"source_provider_scope_id":1,"project_name":"bad/project"}`, 422},
		{"missing_source", 1, `{"source_provider_scope_id":999,"project_name":"LAIAI-Gateway"}`, 404},
		{"disabled_source", 1, `{"source_provider_scope_id":2,"project_name":"LAIAI-Gateway"}`, 422},
		{"new_local_project", 1, valid, 200}, {"replayed_project", 1, valid, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			su := r.Group("/api/admin", func(c *gin.Context) {
				if tc.actor != 0 {
					c.Set("actor_id", tc.actor)
				}
			})
			f.srv.registerArkAssetAdmin(su)
			req := httptest.NewRequest("POST", "/api/admin/video/asset-projects", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Admin-Token", "shared-root-is-not-an-individual-actor")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == 200 {
				var out video.AssetProjectRegistrationResult
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.ProviderScope.ID <= 2 || out.ProviderScope.ProjectName != "LAIAI-Gateway" || out.ProviderScope.AccountRef != "2132103052" || !out.ProviderScope.Enabled {
					t.Fatal("unsafe source-derived identity")
				}
				if registeredID != 0 && registeredID != out.ProviderScope.ID {
					t.Fatal("replay changed identity")
				}
				registeredID = out.ProviderScope.ID
				if w.Header().Get("Cache-Control") != "private, no-store" {
					t.Fatal("sensitive setup response cacheable")
				}
				for _, forbidden := range []string{"scope_key", "fx_", "credential", "access_key", "secret_key", "session_token"} {
					if strings.Contains(w.Body.String(), forbidden) {
						t.Fatal("internal field leaked", forbidden)
					}
				}
			}
		})
	}
	var count int64
	if err := db.Table("video_provider_scopes").Count(&count).Error; err != nil || count != 3 {
		t.Fatal("rejected/replayed requests changed projects", err)
	}
	if err := db.Table("video_asset_events").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("registration audit mismatch", err)
	}
}
