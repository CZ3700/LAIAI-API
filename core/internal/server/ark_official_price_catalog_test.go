package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
)

func TestOfficialPriceCatalogRequiresIndividualActiveSuperadminAndDoesNotWrite(t *testing.T) {
	f, db := setupServer(t)
	for _, row := range []struct {
		id     uint64
		role   string
		status int
	}{
		{1, identity.PlatRoleSuperadmin, 1}, {2, identity.PlatRoleAdmin, 1}, {3, identity.PlatRoleSuperadmin, 0},
	} {
		email := fmt.Sprintf("catalog-%d@test.invalid", row.id)
		if e := db.Exec("INSERT INTO users(id,email,email_canonical,role,status) VALUES(?,?,?,?,?)", row.id, email, email, row.role, row.status).Error; e != nil {
			t.Fatal(e)
		}
	}
	// These callbacks reject any hidden mutation, including a price publication.
	writes := 0
	denyWrite := func(tx *gorm.DB) { writes++; tx.AddError(assertCatalogWrite{}) }
	if e := db.Callback().Create().Before("gorm:create").Register("catalog:deny_create", denyWrite); e != nil {
		t.Fatal(e)
	}
	if e := db.Callback().Update().Before("gorm:update").Register("catalog:deny_update", denyWrite); e != nil {
		t.Fatal(e)
	}
	if e := db.Callback().Delete().Before("gorm:delete").Register("catalog:deny_delete", denyWrite); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		actor uint64
		want  int
	}{{0, 403}, {1, 200}, {2, 403}, {3, 403}, {999, 403}} {
		r := gin.New()
		su := r.Group("/api/admin", func(c *gin.Context) {
			if tc.actor != 0 {
				c.Set("actor_id", tc.actor)
			}
		})
		f.srv.registerArkOfficialPriceCatalog(su)
		req := httptest.NewRequest("GET", "/api/admin/video/official-price-catalog", nil)
		req.Header.Set("X-Admin-Token", "shared-token-cannot-be-an-individual-reviewer")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("actor %d: got %d want %d", tc.actor, w.Code, tc.want)
		}
		if tc.want == 200 {
			var c arkpricecatalog.Catalog
			if e := json.Unmarshal(w.Body.Bytes(), &c); e != nil {
				t.Fatal(e)
			}
			if len(c.Tables) != 6 || c.Usage != "reference_only_no_price_or_capability_changes" || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("unsafe or incomplete catalog response")
			}
		}
	}
	if writes != 0 {
		t.Fatal("catalog attempted database writes")
	}
}

type assertCatalogWrite struct{}

func (assertCatalogWrite) Error() string { return "reference catalog attempted a database write" }
