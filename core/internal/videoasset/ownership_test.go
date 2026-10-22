package videoasset

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
)

func ownerCaller(user, key uint64, suffix string) CreateContext {
	return CreateContext{OrgID: 1, UserID: user, KeyID: key, CallerKind: "api_key", RequestID: "req-" + suffix, IdempotencyKey: "idem-" + suffix}
}
func ownerAdmin() CreateContext {
	return CreateContext{OrgID: 1, UserID: 99, CallerKind: "session_user", Superadmin: true, RequestID: "req-admin", IdempotencyKey: "idem-admin"}
}
func createOwnedGroup(t *testing.T, s *Service, c CreateContext) GroupDTO {
	t.Helper()
	g, _, err := s.CreateGroup(context.Background(), c, GroupCreate{Kind: KindVirtual, Name: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	g, err = s.GetGroup(WithCaller(context.Background(), c), c.OrgID, g.ID)
	if err != nil || g.Status != "ready" {
		t.Fatalf("group not confirmed: %+v %v", g, err)
	}
	return g
}

func TestOwnershipNamedKeysAnonymousKeysLegacyAndCursorIsolation(t *testing.T) {
	s, db, _, _ := setup(t)
	u1, u1other, u2, k1, k2 := ownerCaller(10, 101, "one"), ownerCaller(10, 102, "one-other"), ownerCaller(20, 201, "two"), ownerCaller(0, 301, "key-one"), ownerCaller(0, 302, "key-two")
	g1 := createOwnedGroup(t, s, u1)
	g2 := createOwnedGroup(t, s, u2)
	gk := createOwnedGroup(t, s, k1)
	u1.IdempotencyKey, u1.RequestID = "idem-another", "req-another"
	createOwnedGroup(t, s, u1)
	var legacy Group
	if err := db.Where("group_id = ?", g1.ID).Take(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	legacy.ID, legacy.ProviderID, legacy.CreateOperationID = "ag_legacy", nil, ""
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		c       CreateContext
		id      string
		allowed bool
	}{{u1other, g1.ID, true}, {u2, g1.ID, false}, {k1, g1.ID, false}, {k1, gk.ID, true}, {k2, gk.ID, false}, {ownerAdmin(), g2.ID, true}, {u1, legacy.ID, false}, {ownerAdmin(), legacy.ID, true}} {
		_, err := s.GetGroup(WithCaller(context.Background(), tc.c), 1, tc.id)
		if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, ErrNotFound) {
			t.Fatalf("owner access %+v %s: %v", tc.c, tc.id, err)
		}
	}
	ctx := WithCaller(context.Background(), u1)
	page, err := s.ConsoleGroups(ctx, 1, ListFilter{Limit: 1})
	if err != nil || page.Total != 2 || len(page.Data) != 1 || page.NextCursor == "" {
		t.Fatalf("owner page %+v %v", page, err)
	}
	_, err = s.ConsoleGroups(WithCaller(context.Background(), u2), 1, ListFilter{Limit: 1, Cursor: page.NextCursor})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign cursor accepted: %v", err)
	}
	_, err = s.ConsoleGroups(WithCaller(context.Background(), u1other), 1, ListFilter{Limit: 1, Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("same user's other key lost cursor access: %v", err)
	}
	invalid := WithCaller(context.Background(), CreateContext{OrgID: 1})
	rows, _, err := s.ListGroups(invalid, 1, 100, "")
	if err != nil || len(rows) != 0 {
		t.Fatalf("invalid actor leaked: %d %v", len(rows), err)
	}
	_, err = s.GetGroup(WithCaller(context.Background(), u1), 2, g1.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org access: %v", err)
	}
}

func TestOwnershipBlocksMutationsOperationsAndTaskReferences(t *testing.T) {
	s, db, now, provider := setup(t)
	u1, u2 := ownerCaller(10, 101, "owner"), ownerCaller(20, 201, "other")
	g := createOwnedGroup(t, s, u1)
	if _, err := s.GetOperation(WithCaller(context.Background(), u2), 1, g.CreateOperation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("operation leak: %v", err)
	}
	if _, _, err := s.DeleteGroup(context.Background(), u2, g.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if _, err := s.RenameResource(context.Background(), u2, g.ID, true, Rename{Name: "stolen", ExpectedVersion: g.Version}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign rename: %v", err)
	}
	in := AssetCreate{GroupID: g.ID, MediaType: "image", URL: "https://media.example/image.png", Name: "reference"}
	if _, _, err := s.CreateAsset(context.Background(), u2, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign asset registration: %v", err)
	}
	u1.RequestID, u1.IdempotencyKey = "req-asset", "idem-asset"
	a, _, err := s.CreateAsset(context.Background(), u1, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(16 * time.Second)
	if err = s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetAsset(WithCaller(context.Background(), u2), 1, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("asset detail leak: %v", err)
	}
	if _, _, err = s.DeleteAsset(context.Background(), u2, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("asset delete leak: %v", err)
	}
	if _, _, err = s.ListAssets(WithCaller(context.Background(), u2), 1, g.ID, 10, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign group assets filter: %v", err)
	}
	refs := []ReferenceInput{{AssetID: a.ID, Role: "reference_image", ContentIndex: 0}}
	if _, err = s.ValidateBindingsTx(db.WithContext(WithCaller(context.Background(), u2)), 1, testScope(), refs); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign asset:// reference: %v", err)
	}
	u1.KeyID = 102
	if _, err = s.ValidateBindingsTx(db.WithContext(WithCaller(context.Background(), u1)), 1, testScope(), refs); err != nil {
		t.Fatalf("same user new key reference: %v", err)
	}
	if provider.calls["CreateAsset"] != 1 || provider.calls["DeleteAsset"] != 0 || provider.calls["DeleteAssetGroup"] != 0 {
		t.Fatalf("foreign write reached provider: %+v", provider.calls)
	}
}

func TestOwnershipPublicHTTPRejectsMissingActorAndFiltersAuthenticatedKey(t *testing.T) {
	s, _, _, _ := setup(t)
	u1, u2 := ownerCaller(10, 101, "http-owner"), ownerCaller(20, 201, "http-other")
	g := createOwnedGroup(t, s, u1)
	r := gin.New()
	group := r.Group("/v1/video", func(c *gin.Context) {
		if c.GetHeader("Authorization") != "" {
			uid := u2.UserID
			c.Set("video_key", &identity.APIKey{ID: u2.KeyID, OrgID: 1, UserID: &uid, Scopes: "video:read video:write"})
			c.Set("video_request_id", "req-http")
		}
		c.Next()
	})
	s.RegisterRoutes(group)
	for _, tc := range []struct {
		auth, path string
		code       int
	}{{"", "/v1/video/asset-groups/" + g.ID, 401}, {"fixture", "/v1/video/asset-groups/" + g.ID, 404}, {"fixture", "/v1/video/asset-groups", 200}} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.code || strings.Contains(w.Body.String(), g.ID) {
			t.Fatalf("HTTP owner guard %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}

func TestOwnershipSyncQueriesOnlyCreatorsGroupsAndPreservesImportedOwner(t *testing.T) {
	s, db, now, _ := setup(t)
	u1, u2 := ownerCaller(10, 101, "sync-owner"), ownerCaller(20, 201, "sync-other")
	g := createOwnedGroup(t, s, u1)
	createOwnedGroup(t, s, u2)
	var row Group
	if err := db.Where("group_id = ?", g.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureConsole(ConsoleOptions{}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			Filter      struct{ GroupIds []string }
			ProjectName string
		}
		if json.NewDecoder(req.Body).Decode(&body) != nil || req.URL.Query().Get("Action") != "ListAssets" || len(body.Filter.GroupIds) != 1 || body.Filter.GroupIds[0] != *row.ProviderID || body.ProjectName != row.Project {
			t.Fatal("ordinary sync queried global groups or another creator's group")
		}
		return syncReply(req, map[string]any{"Items": []any{map[string]string{"Id": "official-owner-asset", "GroupId": *row.ProviderID, "ProjectName": row.Project, "AssetType": "Image", "Status": "Active", "Name": "private import"}}}), nil
	})})
	u1.IdempotencyKey, u1.RequestID = "idem-sync-job", "req-sync-job"
	job, err := s.CreateSyncJob(context.Background(), u1, KindVirtual, "owned")
	if err != nil || job.Phase != "assets" {
		t.Fatalf("ordinary sync phase: %+v %v", job, err)
	}
	for i := 0; i < 4; i++ {
		if err = s.processSyncJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		job, err = s.GetSyncJob(WithCaller(context.Background(), u1), 1, job.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "succeeded" {
			break
		}
		*now = now.Add(time.Second)
	}
	if job.Status != "succeeded" || calls != 1 {
		t.Fatalf("ordinary sync: %+v calls=%d", job, calls)
	}
	var responseEvidence invocation.VideoPayload
	if err := db.Where("org_id = ? AND payload_id = ?", 1, job.LastResponsePayloadID).Take(&responseEvidence).Error; err != nil || responseEvidence.CaptureStatus != invocation.VideoCaptureDisabled {
		t.Fatalf("ordinary sync retained mixed creator page history: %+v %v", responseEvidence, err)
	}
	var staged int64
	if err := db.Table("invocation_payload_staging").Where("payload_id = ?", job.LastResponsePayloadID).Count(&staged).Error; err != nil || staged != 0 {
		t.Fatalf("disabled sync response staged ciphertext: %d %v", staged, err)
	}
	if _, err = s.GetSyncJob(WithCaller(context.Background(), u2), 1, job.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign sync detail: %v", err)
	}
	if latest, err := s.LatestSyncJob(WithCaller(context.Background(), u2), 1, KindVirtual); err != nil || latest != nil {
		t.Fatalf("foreign latest sync: %+v %v", latest, err)
	}
	u1.KeyID = 102
	if _, err = s.GetSyncJob(WithCaller(context.Background(), u1), 1, job.ID, false); err != nil {
		t.Fatalf("same user sync: %v", err)
	}
	page, err := s.ConsoleAssets(WithCaller(context.Background(), u1), 1, ListFilter{Limit: 20})
	if err != nil || page.Total != 1 || len(page.Data) != 1 {
		t.Fatalf("import ownership missing: %+v %v", page, err)
	}
	if _, err = s.GetAsset(WithCaller(context.Background(), u2), 1, page.Data[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("import leaked: %v", err)
	}
	if _, err = s.CreateSyncJob(context.Background(), u2, KindVirtual, "discover"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ordinary discovery: %v", err)
	}
}
