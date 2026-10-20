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
)

func TestArkAssetSetupRequiresIndividualSuperadminAndWorksWithoutGateway(t *testing.T) {
	f, db := setupServer(t)
	for _, row := range []struct {
		id     uint64
		role   string
		status int
	}{
		{1, identity.PlatRoleSuperadmin, 1}, {2, identity.PlatRoleAdmin, 1}, {3, identity.PlatRoleSuperadmin, 0},
	} {
		email := fmt.Sprintf("asset-setup-%d@test.invalid", row.id)
		if err := db.Exec("INSERT INTO users(id,email,email_canonical,role,status) VALUES(?,?,?,?,?)", row.id, email, email, row.role, row.status).Error; err != nil {
			t.Fatal(err)
		}
	}
	const schema = `
CREATE TABLE video_provider_scopes(id INTEGER PRIMARY KEY,account_ref TEXT,region TEXT,project_name TEXT,enabled INTEGER);
CREATE TABLE video_asset_scope_configs(provider_scope_id INTEGER,version INTEGER,account_ref TEXT,region TEXT,project_name TEXT,capabilities_json TEXT,credential_cipher TEXT,enabled INTEGER);
CREATE TABLE video_asset_org_routes(org_id INTEGER,provider_scope_id INTEGER,scope_version INTEGER,enabled INTEGER,default_route INTEGER);
CREATE TABLE video_org_policies(org_id INTEGER,async_execution_allowed INTEGER,capture_history INTEGER,retention_days INTEGER);
INSERT INTO organizations(id,slug,name) VALUES(1,'setup-one','Setup one'),(2,'setup-two','Setup two');
INSERT INTO video_provider_scopes VALUES(10,'1234567890','cn-beijing','default',1);
INSERT INTO video_asset_scope_configs VALUES(10,4,'1234567890','cn-beijing','default','{"VirtualGroups":true}','never-return-this-cipher',1);
INSERT INTO video_asset_org_routes VALUES(2,10,4,1,1);
INSERT INTO video_org_policies VALUES(1,1,0,30),(2,1,0,30);
`
	if err := db.Exec(schema).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA query_only=ON").Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		actor uint64
		query string
		want  int
	}{
		{0, "?org_id=1", 403}, {2, "?org_id=1", 403}, {3, "?org_id=1", 403}, {999, "?org_id=1", 403},
		{1, "", 422}, {1, "?org_id=0", 422}, {1, "?org_id=-1", 422}, {1, "?org_id=1&org_id=2", 422}, {1, "?org_id=999", 404}, {1, "?org_id=1", 200},
	} {
		t.Run(fmt.Sprintf("actor_%d_query_%s", tc.actor, tc.query), func(t *testing.T) {
			r := gin.New()
			su := r.Group("/api/admin", func(c *gin.Context) {
				if tc.actor != 0 {
					c.Set("actor_id", tc.actor)
				}
			})
			f.srv.registerArkAssetAdmin(su)
			req := httptest.NewRequest("GET", "/api/admin/video/asset-setup"+tc.query, nil)
			req.Header.Set("X-Admin-Token", "shared-root-cannot-inspect-management-credentials")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("response=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			for _, forbidden := range []string{"never-return-this-cipher", "credential_cipher", "access_key", "secret_key", "session_token"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatalf("unsafe field %s", forbidden)
				}
			}
			if tc.want == 200 {
				var out video.AssetSetupView
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.ConfigurationReady || out.OfficialAccess != "not_checked" || len(out.OrgRoutes) != 0 || len(out.ScopeConfigs) != 1 || !out.ScopeConfigs[0].CredentialPresent {
					t.Fatal("wrong or leaking projection")
				}
				if w.Header().Get("Cache-Control") != "private, no-store" {
					t.Fatal("setup response may be cached")
				}
				foundGateway, foundRoute := false, false
				for _, issue := range out.Issues {
					if issue == "gateway_unavailable" {
						foundGateway = true
					}
					if issue == "org_default_route_missing" {
						foundRoute = true
					}
				}
				if !foundGateway || !foundRoute {
					t.Fatal("gateway/config-without-route gaps not shown")
				}
			}
		})
	}
}
