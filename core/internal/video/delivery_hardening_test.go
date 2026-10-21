package video

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

func TestRequestResourceAcceptsOnlyPlatformIdentifiers(t *testing.T) {
	for _, tc := range []struct{ parameter, value, kind string }{{"id", "task_123", "task"}, {"operation_id", "op_123", "operation"}, {"asset_id", "asset_123", "asset"}, {"group_id", "ag_123", "asset_group"}, {"session_id", "pas_123", "portrait_authorization"}, {"id", "task_?secret", ""}, {"asset_id", "raw-upstream-secret", ""}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Params = gin.Params{{Key: tc.parameter, Value: tc.value}}
		kind, id := requestResource(c)
		if kind != tc.kind || (kind != "" && id != tc.value) || (kind == "" && id != "") {
			t.Fatalf("unexpected metadata %q/%q", kind, id)
		}
	}
}

func TestAssetAdmissionErrorsUsePublicHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{videoasset.ErrAuthorization, 403, "forbidden"}, {videoasset.ErrNotFound, 404, "not_found"}, {videoasset.ErrUnsupported, 422, "unsupported_capability"}, {videoasset.ErrInvalid, 422, "invalid_request"}, {videoasset.ErrConflict, 409, "invalid_state"}, {videoasset.ErrInUse, 409, "invalid_state"}, {videoasset.ErrNotEmpty, 409, "invalid_state"}, {videoasset.ErrStorage, 503, "provider_unavailable"}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("video_request_id", "request_test")
		respondError(c, tc.err)
		var response struct {
			RequestID string `json:"request_id"`
			Error     struct {
				Code      string `json:"code"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		must(t, json.Unmarshal(w.Body.Bytes(), &response))
		if w.Code != tc.status || response.Error.Code != tc.code || response.Error.Retryable != (tc.status == 503) || response.RequestID != "request_test" {
			t.Fatalf("%v -> %d %s", tc.err, w.Code, w.Body.String())
		}
	}
}

func TestMySQLRevokedAssetScopeIsRejectedBeforeProviderPOST(t *testing.T) {
	f := mysqlFixture(t)
	scope, _ := assetTestSetup(t, f)
	_, asset := assetTestSeed(t, f, scope, videoasset.KindVirtual)
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "image_url", Role: "reference_image", ImageURL: &ark.MediaReference{URL: "asset://" + asset.ID}}}}
	v, _, err := f.s.Create(context.Background(), f.key, f.requestID("asset_request"), "asset-revoke-before-post", in)
	must(t, err)
	must(t, f.s.SetAssetScopeEnabled(context.Background(), assetTestActor(), scope.ID, 1, false))
	f.dispatch(t, v.ID)
	if f.posts.Load() != 0 || f.hold(t, v.ID).Status != ledger.HoldReleased {
		t.Fatal("revoked asset dispatched or failed to release unsent hold")
	}
	var ref videoasset.TaskReference
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&ref).Error)
	if ref.ReleasedAt == nil {
		t.Fatal("unsent task retained asset reference")
	}
}

type deliveryContextKey struct{}

func TestMySQLTerminalSettlementAndAssetAdmissionUseSameLockOrder(t *testing.T) {
	f := mysqlFixture(t)
	scope, _ := assetTestSetup(t, f)
	_, asset := assetTestSeed(t, f, scope, videoasset.KindVirtual)
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "image_url", Role: "reference_image", ImageURL: &ark.MediaReference{URL: "asset://" + asset.ID}}}}
	requestID, _ := randomID("")
	v, _, err := f.s.Create(context.Background(), f.key, requestID, "asset-lock-order-first", in)
	must(t, err)
	f.dispatch(t, v.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	createHasTenant, pollWantsTenant := make(chan struct{}), make(chan struct{})
	var createOnce, pollOnce sync.Once
	must(t, f.s.DB.Callback().Query().Before("gorm:query").Register("test:admission-before-key", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(deliveryContextKey{}) == "create" && tx.Statement.Table == "api_keys" {
			createOnce.Do(func() {
				close(createHasTenant)
				select {
				case <-pollWantsTenant:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			})
		}
	}))
	defer f.s.DB.Callback().Query().Remove("test:admission-before-key")
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register("test:terminal-before-tenant", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(deliveryContextKey{}) == "poll" && tx.Statement.Table == "video_asset_tenant_locks" {
			pollOnce.Do(func() { close(pollWantsTenant) })
		}
	}))
	defer f.s.DB.Callback().Create().Remove("test:terminal-before-tenant")
	created, polled := make(chan error, 1), make(chan error, 1)
	go func() {
		rid, _ := randomID("")
		_, _, e := f.s.Create(context.WithValue(ctx, deliveryContextKey{}, "create"), f.key, rid, "asset-lock-order-second", in)
		created <- e
	}()
	select {
	case <-createHasTenant:
	case <-ctx.Done():
		t.Fatal("admission did not reach budget lock")
	}
	go func() {
		polled <- f.s.pollTask(context.WithValue(ctx, deliveryContextKey{}, "poll"), f.key.OrgID, v.ID)
	}()
	must(t, <-created)
	must(t, <-polled)
	if f.hold(t, v.ID).Status != ledger.HoldCaptured {
		t.Fatal("terminal settlement did not finish")
	}
}
