package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
)

func catalogHostFixture(t *testing.T, scopes, limits string) (*Server, *gorm.DB, string, *identity.APIKey) {
	t.Helper()
	f, db, _ := pricingCatalogFixture(t)
	for _, statement := range []string{
		`CREATE TABLE video_org_policies(org_id INTEGER PRIMARY KEY,async_execution_allowed BOOLEAN NOT NULL,capture_history BOOLEAN NOT NULL,retention_days INTEGER NOT NULL,updated_at DATETIME NOT NULL)`,
		`INSERT INTO organizations(id,slug,name,status) VALUES(1,'catalog','catalog',1)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.srv.video = &video.Service{DB: db, Now: time.Now}
	plain, key, err := identity.IssueKey(db, identity.IssueKeyInput{OrgID: 1, Scopes: scopes, ModelLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	return f.srv, db, plain, key
}

func catalogHostRequest(t *testing.T, host *Server, plain string, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	r.GET("/v1/models", host.listPricedModels)
	request := httptest.NewRequest("GET", "/v1/models", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+plain)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("catalog must not be shared or cached")
	}
	return w
}

func catalogHostIDs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var response struct {
		Object string `json:"object"`
		Data   []struct {
			ID    string `json:"id"`
			Owner string `json:"owned_by"`
		} `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Object != "list" {
		t.Fatalf("discovery failed: %d %s", w.Code, w.Body.String())
	}
	ids := []string{}
	for _, row := range response.Data {
		if row.Owner == "" {
			t.Fatal("missing public provider identity")
		}
		ids = append(ids, row.ID)
	}
	return ids
}

func TestVideoCatalogHostScopesAndLegacyWhitelist(t *testing.T) {
	for _, scope := range []string{"", "chat:write", "video:read", "video:write", "video:*", "*"} {
		t.Run(scope, func(t *testing.T) {
			host, _, plain, _ := catalogHostFixture(t, scope, "priced,seedance-2.0,official-alias")
			want := []string{"priced"}
			if strings.HasPrefix(scope, "video:") || scope == "*" {
				want = append(want, ark.Model20)
			}
			if got := catalogHostIDs(t, catalogHostRequest(t, host, plain, context.Background())); !reflect.DeepEqual(got, want) {
				t.Fatalf("scope %q: %v want %v", scope, got, want)
			}
		})
	}
	// The authenticated directory observes a fresh subject after VerifyKey.
	host, db, plain, key := catalogHostFixture(t, "video:read", "")
	changed := false
	name := "catalog_revoke_between_auth_and_snapshot"
	if err := db.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if changed || q.Statement.Table != "api_keys" {
			return
		}
		if _, inTx := q.Statement.ConnPool.(gorm.TxCommitter); inTx {
			return
		}
		changed = true
		if err := db.Model(&identity.APIKey{}).Where("id = ?", key.ID).Update("model_limits", "priced").Error; err != nil {
			t.Error("cannot change synthetic whitelist")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	if got := catalogHostIDs(t, catalogHostRequest(t, host, plain, context.Background())); !changed || !reflect.DeepEqual(got, []string{"priced"}) {
		t.Fatal("discovery used pre-transaction whitelist", got)
	}
}

func TestVideoCatalogHostActionsRemainIndependent(t *testing.T) {
	host, db, plain, key := catalogHostFixture(t, "video:read", ark.Model20)
	if err := db.Exec(`INSERT INTO org_entitlements(org_id,feature_images,feature_video) VALUES(1,0,0)`).Error; err != nil {
		t.Fatal(err)
	}
	observation, err := host.video.Catalog(context.Background(), video.CatalogSubjectRef{OrgID: 1, KeyID: key.ID})
	if err != nil || len(observation.Models) != 1 {
		t.Fatal("read-only catalog failed", err)
	}
	row := observation.Models[0]
	if !row.Actions.ModelsRead.Eligible || row.Actions.Create.Eligible || row.Actions.Estimate.Eligible || row.Publication.Status != "partial" {
		t.Fatalf("scopes or execution flags were combined: %+v", row.Actions)
	}
	if got := catalogHostIDs(t, catalogHostRequest(t, host, plain, context.Background())); !reflect.DeepEqual(got, []string{ark.Model20}) {
		t.Fatal("execution opt-out hid readable model")
	}
	// Old canonical price shape remains a decimal string, without supplier data.
	if len(row.Prices) != 1 || row.Prices[0].Amount != "9.125" {
		t.Fatal("wrong public base price")
	}
	raw, _ := json.Marshal(observation)
	for _, forbidden := range []string{"credential_enc", "scope_id", "supplier_cny", "account_ref", "evidence_hash", "source_hash", "999"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("private fact leaked", forbidden)
		}
	}
}

func TestVideoCatalogHostFailureNeverReturnsPartialModels(t *testing.T) {
	for _, failure := range []string{"missing_service", "generic_query", "cancel_after_read", "subject_revoked"} {
		t.Run(failure, func(t *testing.T) {
			host, db, plain, key := catalogHostFixture(t, "video:read", "priced,seedance-2.0")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			name := "catalog_failure_" + failure
			if failure == "missing_service" {
				host.video = nil
			} else {
				err := db.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
					if q.Statement.Table == "model_prices" {
						if failure == "generic_query" {
							q.AddError(errors.New("synthetic confidential database details"))
						}
						if failure == "cancel_after_read" {
							cancel()
						}
					}
					if failure == "subject_revoked" && q.Statement.Table == "api_keys" {
						if _, inTx := q.Statement.ConnPool.(gorm.TxCommitter); !inTx {
							_ = db.Model(&identity.APIKey{}).Where("id = ?", key.ID).Update("status", 0).Error
						}
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
			}
			w := catalogHostRequest(t, host, plain, ctx)
			want := 503
			if failure == "subject_revoked" {
				want = 403
			}
			if w.Code != want || strings.Contains(w.Body.String(), `"data"`) || strings.Contains(w.Body.String(), "confidential") {
				t.Fatalf("partial or unsafe failure: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestVideoCatalogHostAndMarketingShareEndpointNormalization(t *testing.T) {
	host, db, plain, _ := catalogHostFixture(t, "video:read", ark.Model20)
	for _, base := range []string{"https://ark.cn-beijing.volces.com/api/v3/", "https://ark.cn-beijing.volces.com/api/v3///", "https://example.invalid/api/v3"} {
		if err := db.Exec("UPDATE channels SET base_url = ? WHERE id = 99", base).Error; err != nil {
			t.Fatal(err)
		}
		ids := catalogHostIDs(t, catalogHostRequest(t, host, plain, context.Background()))
		rows, err := host.publicPricing(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row["model"] == ark.Model20 {
				found = true
			}
		}
		want := !strings.Contains(base, "example.invalid")
		if (len(ids) == 1) != want || found != want {
			t.Fatal("marketing and authenticated catalog disagree", base)
		}
	}
}

func TestVideoCatalogHostMalformedPricingVersionRemainsDiagnostic(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			host, db, plain, key := catalogHostFixture(t, "video:read", ark.Model20)
			if err := db.Model(&video.ModelConfig{}).Where("public_model=?", ark.Model20).Updates(map[string]any{"pricing_version": strings.Repeat("x", 65), "enabled": enabled}).Error; err != nil {
				t.Fatal(err)
			}
			out, err := host.video.Catalog(context.Background(), video.CatalogSubjectRef{OrgID: 1, KeyID: key.ID})
			if err != nil || len(out.Models) != 1 || out.Models[0].Publication.Status != "blocked" {
				t.Fatal("bad version erased diagnostics", err)
			}
			found := false
			for _, reason := range out.Models[0].Publication.ReasonCodes {
				if reason == "capability_invalid" {
					found = true
				}
			}
			if !found || len(out.Models[0].Prices) != 0 {
				t.Fatal("invalid version became sellable")
			}
			if ids := catalogHostIDs(t, catalogHostRequest(t, host, plain, context.Background())); len(ids) != 0 {
				t.Fatal("SDK exposed invalid model", ids)
			}
		})
	}
}
