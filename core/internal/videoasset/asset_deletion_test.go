package videoasset

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
)

func rejectedAsset(t *testing.T, s *Service) AssetDTO {
	t.Helper()
	g := readyGroup(t, s, "asset-cleanup-group")
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		return groupErrorReply(req, 403, "AccessDenied", nil), nil
	})})
	a, _, err := s.CreateAsset(context.Background(), createContext("rejected-asset"), AssetCreate{GroupID: g.ID, MediaType: "image", URL: "https://media.example/immutable.png"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, err = s.GetAsset(context.Background(), 1, a.ID)
	if err != nil || a.Status != "failed" || a.DeletionMode != "local_failed" || a.CreateOperation == nil || a.CreateOperation.Status != "rejected" {
		t.Fatal(a, err)
	}
	return a
}

func TestFailedAssetLocalCleanupIsAtomicIsolatedAndIdempotent(t *testing.T) {
	s, db, _, _ := setup(t)
	a := rejectedAsset(t, s)
	ctx := context.Background()
	s.options.ResolveScope = func(context.Context, uint64, Scope) (RuntimeScope, error) {
		t.Fatal("cleanup resolved credentials")
		return RuntimeScope{}, ErrUnsupported
	}
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("cleanup contacted provider"); return nil, nil })})
	other := createContext("other")
	other.OrgID = 2
	if _, _, err := s.DeleteAsset(ctx, other, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_asset_cleanup_audit BEFORE INSERT ON video_asset_events WHEN NEW.kind = 'local_failed_asset_deleted' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, done, err := s.DeleteAsset(ctx, createContext("cleanup"), a.ID); !errors.Is(err, ErrStorage) || done {
		t.Fatal(done, err)
	}
	var stored Asset
	if err := db.Where("asset_id = ?", a.ID).Take(&stored).Error; err != nil || stored.DeletedAt != nil || stored.Status != StatusFailed {
		t.Fatal(stored, err)
	}
	if err := db.Exec("DROP TRIGGER reject_asset_cleanup_audit").Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cleanup", "cleanup", "another-cleanup"} {
		if _, done, err := s.DeleteAsset(ctx, createContext(key), a.ID); err != nil || !done {
			t.Fatal(done, err)
		}
	}
	var events, operations, payloads int64
	db.Model(&Event{}).Where("org_id = ? AND resource_id = ? AND kind = ?", 1, a.ID, "local_failed_asset_deleted").Count(&events)
	db.Model(&Operation{}).Where("org_id = ? AND resource_id = ?", 1, a.ID).Count(&operations)
	db.Table("invocation_payloads").Where("org_id = ? AND attempt_id = ?", 1, a.CreateOperation.ID).Count(&payloads)
	if events != 1 || operations != 1 || payloads == 0 {
		t.Fatal("cleanup changed history or duplicated writes", events, operations, payloads)
	}
	if _, err := s.GetAsset(ctx, 1, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	var activeAssets int64
	db.Model(&Asset{}).Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", 1, a.GroupID).Count(&activeAssets)
	if activeAssets != 0 {
		t.Fatal("failed asset still prevents empty-group deletion")
	}
}

func TestFailedAssetCleanupRejectsUncertainOrMismatchedState(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*gorm.DB, AssetDTO)
	}{
		{"unknown", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Operation{}).Where("operation_id = ?", a.CreateOperation.ID).Update("status", "unknown")
		}},
		{"pending", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Operation{}).Where("operation_id = ?", a.CreateOperation.ID).Update("status", "pending")
		}},
		{"in-flight", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Operation{}).Where("operation_id = ?", a.CreateOperation.ID).Update("status", "in_flight")
		}},
		{"review", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Asset{}).Where("asset_id = ?", a.ID).Update("review_state", ReviewRequired)
		}},
		{"operation-scope", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Operation{}).Where("operation_id = ?", a.CreateOperation.ID).Update("project", "another")
		}},
		{"asset-scope", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Asset{}).Where("asset_id = ?", a.ID).Update("scope_version", 2)
		}},
		{"resource", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Operation{}).Where("operation_id = ?", a.CreateOperation.ID).Update("resource_id", "other")
		}},
		{"kind", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Operation{}).Where("operation_id = ?", a.CreateOperation.ID).Update("resource_type", "asset_group")
		}},
		{"org", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Operation{}).Where("operation_id = ?", a.CreateOperation.ID).Update("org_id", 2)
		}},
		{"action", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Operation{}).Where("operation_id = ?", a.CreateOperation.ID).Update("action", "UpdateAsset")
		}},
		{"empty-provider-id", func(db *gorm.DB, a AssetDTO) {
			db.Model(&Asset{}).Where("asset_id = ?", a.ID).Update("provider_id", "")
		}},
		{"active-reference", func(db *gorm.DB, a AssetDTO) {
			db.Create(&TaskReference{OrgID: 1, TaskID: "task-live", AssetID: a.ID, GroupID: a.GroupID})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _, _ := setup(t)
			a := rejectedAsset(t, s)
			tc.edit(db, a)
			if _, done, err := s.DeleteAsset(context.Background(), createContext("cleanup"), a.ID); err == nil || done {
				t.Fatal("unsafe cleanup", done, err)
			}
			var stored Asset
			db.Where("asset_id = ?", a.ID).Take(&stored)
			if stored.DeletedAt != nil {
				t.Fatal("asset was deleted")
			}
		})
	}
}

func TestAssetDeleteReceiptsSurvivePendingUnknownAndConfigurationRemoval(t *testing.T) {
	s, db, now, provider := setup(t)
	g := readyGroup(t, s, "delete-receipt-group")
	a := readyAsset(t, s, now, g.ID, "delete-receipt-asset")
	c := createContext("delete-receipt")
	op, _, err := s.DeleteAsset(context.Background(), c, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "in_flight", "unknown"} {
		t.Run(state, func(t *testing.T) {
			db.Model(&Operation{}).Where("operation_id = ?", op.ID).Updates(map[string]any{"status": state, "safe_error_code": "provider_unavailable"})
			if state == "unknown" {
				db.Model(&Asset{}).Where("asset_id = ?", a.ID).Update("review_state", ReviewRequired)
			}
			s.options.ResolveScope = func(context.Context, uint64, Scope) (RuntimeScope, error) {
				t.Fatal("receipt read resolved current credentials")
				return RuntimeScope{}, ErrUnsupported
			}
			for _, request := range []CreateContext{c, createContext("new-delete-key")} {
				got, done, err := s.DeleteAsset(context.Background(), request, a.ID)
				if err != nil || done || got.ID != op.ID || got.Status != state || got.RequestID != c.RequestID {
					t.Fatal(got, done, err)
				}
			}
			view, err := s.GetAsset(context.Background(), 1, a.ID)
			if err != nil || view.DeleteOperation == nil || view.DeleteOperation.ID != op.ID || view.DeletionMode != "blocked" {
				t.Fatal(view, err)
			}
			want := "operation_pending"
			if state == "unknown" {
				want = "submission_uncertain"
			}
			if view.DeletionBlockReason != want {
				t.Fatal(view)
			}
		})
	}
	var ops int64
	db.Model(&Operation{}).Where("resource_id = ? AND action = ?", a.ID, "DeleteAsset").Count(&ops)
	if ops != 1 || provider.calls["DeleteAsset"] != 0 {
		t.Fatal("receipt read submitted another deletion", ops)
	}
}

func TestUnknownCreationProjectionAndFiltersHaveScopedSafeReceipts(t *testing.T) {
	s, db, _, provider := setup(t)
	ctx := context.Background()
	ready := readyGroup(t, s, "ready-group")
	provider.unknown = true
	g, _, err := s.CreateGroup(ctx, createContext("unknown-group"), GroupCreate{Kind: KindVirtual, Name: "uncertain"})
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.CreateAsset(ctx, createContext("unknown-asset"), AssetCreate{GroupID: ready.ID, MediaType: "image", URL: "https://media.example/immutable.png"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	g, err = s.GetGroup(ctx, 1, g.ID)
	if err != nil || g.Status != "needs_review" || g.CreateOperation == nil || g.CreateOperation.Status != "unknown" || g.CreateOperation.RequestID != "req-unknown-group" || g.DeletionBlockReason != "submission_uncertain" {
		t.Fatal(g, err)
	}
	a, err = s.GetAsset(ctx, 1, a.ID)
	if err != nil || a.Status != "needs_review" || a.CreateOperation == nil || a.CreateOperation.Status != "unknown" || a.DeletionBlockReason != "submission_uncertain" {
		t.Fatal(a, err)
	}
	groups, err := s.ConsoleGroups(ctx, 1, ListFilter{Status: "needs_review", Limit: 20})
	if err != nil || groups.Total != 1 || groups.Data[0].ID != g.ID {
		t.Fatal(groups, err)
	}
	groups, err = s.ConsoleGroups(ctx, 1, ListFilter{Status: "creating", Limit: 20})
	if err != nil || groups.Total != 0 {
		t.Fatal(groups, err)
	}
	assets, err := s.ConsoleAssets(ctx, 1, ListFilter{Status: "needs_review", Limit: 20})
	if err != nil || assets.Total != 1 || assets.Data[0].ID != a.ID {
		t.Fatal(assets, err)
	}
	view := assets.Data[0]
	if view.ProviderStatus != "unknown" || view.MediaVerificationStatus != "verified" || view.GenerationStatus != "blocked" || view.GenerationBlockReason != "submission_uncertain" {
		t.Fatal("uncertain create was confused with unverified media or official processing", view)
	}
	unknownAssets, err := s.ConsoleAssets(ctx, 1, ListFilter{ProviderStatus: "unknown", Limit: 20})
	if err != nil || unknownAssets.Total != 1 || unknownAssets.Data[0].ID != a.ID {
		t.Fatal("uncertain create absent from official unknown filter", unknownAssets, err)
	}
	for _, view := range []any{g, a} {
		raw, _ := json.Marshal(view)
		for _, secret := range []string{"AKTEST", "test-secret", "account1", "project1", "immutable.png", "payload_id", "receipt"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("projection leaked private fields", secret)
			}
		}
	}
	// A foreign scope's operation is neither exposed nor accepted for cleanup.
	db.Model(&Operation{}).Where("operation_id IN ?", []string{g.CreateOperation.ID, a.CreateOperation.ID}).Update("project", "foreign-project")
	g, err = s.GetGroup(ctx, 1, g.ID)
	if err != nil || g.CreateOperation != nil {
		t.Fatal(g, err)
	}
	a, err = s.GetAsset(ctx, 1, a.ID)
	if err != nil || a.CreateOperation != nil {
		t.Fatal(a, err)
	}
}

func TestAssetDeletionProjectionUsesOriginalScope(t *testing.T) {
	s, _, now, _ := setup(t)
	g := readyGroup(t, s, "fixed-scope-group")
	a := readyAsset(t, s, now, g.ID, "fixed-scope-asset")
	for _, tc := range []struct {
		name    string
		err     error
		enabled bool
		reason  string
	}{
		{"capability-removed", nil, false, "delete_capability_disabled"},
		{"configuration-removed", ErrUnsupported, false, "scope_unavailable"},
		{"enabled", nil, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.options.ResolveScope = func(_ context.Context, org uint64, scope Scope) (RuntimeScope, error) {
				if org != 1 || !sameScope(scope, testScope()) {
					t.Fatal("projection used current/default scope instead of resource scope", org, scope)
				}
				return RuntimeScope{Scope: testScope(), ExecutionRetentionConsent: true, Capabilities: Capabilities{DeleteAssets: tc.enabled}}, tc.err
			}
			view, err := s.GetAsset(context.Background(), 1, a.ID)
			if err != nil || view.DeletionBlockReason != tc.reason {
				t.Fatal(view, err)
			}
			if tc.enabled && view.DeletionMode != "official" || !tc.enabled && view.DeletionMode != "blocked" {
				t.Fatal(view)
			}
		})
	}
}

func TestAssetCleanupHTTPRemainsIdempotentAndTenantIsolated(t *testing.T) {
	s, db, _, _ := setup(t)
	a := rejectedAsset(t, s)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	org := uint64(1)
	group := router.Group("/v1/video")
	group.Use(func(c *gin.Context) {
		uid := uint64(2)
		c.Set("video_key", &identity.APIKey{ID: 3, OrgID: org, UserID: &uid, Scopes: "video:read video:write"})
		c.Set("video_request_id", "req-delete-http")
		c.Next()
	})
	s.RegisterRoutes(group)
	var beforeEvents, beforeOperations int64
	db.Model(&Event{}).Count(&beforeEvents)
	db.Model(&Operation{}).Count(&beforeOperations)
	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, "/v1/video/assets/"+a.ID, nil))
	if missing.Code != 400 {
		t.Fatal("missing idempotency key accepted", missing.Code, missing.Body.String())
	}
	var afterEvents, afterOperations int64
	db.Model(&Event{}).Count(&afterEvents)
	db.Model(&Operation{}).Count(&afterOperations)
	var stored Asset
	if err := db.Where("asset_id = ?", a.ID).Take(&stored).Error; err != nil || stored.DeletedAt != nil || beforeEvents != afterEvents || beforeOperations != afterOperations {
		t.Fatal("missing idempotency key mutated state", stored, err)
	}
	for _, tc := range []struct {
		org  uint64
		want int
	}{{2, 404}, {1, 204}, {1, 204}, {2, 404}} {
		org = tc.org
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/v1/video/assets/"+a.ID, nil)
		req.Header.Set("Idempotency-Key", "asset-delete-http")
		router.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
}
