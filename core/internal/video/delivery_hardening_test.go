package video

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

func TestMySQLArtifactScopeSelectionAndRateDeferral(t *testing.T) {
	f := mysqlFixture(t)
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			id, _ := randomID("up_")
			return reply(200, `{"id":"`+id+`"}`), nil
		}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		return reply(200, `{"id":"`+id+`","status":"succeeded","usage":{"completion_tokens":100},"content":{"video_url":"https://cdn.example.com/private.mp4?signature=secret"}}`), nil
	}
	var last string
	for i := 0; i < 6; i++ {
		f.now = f.now.Add(2 * time.Second)
		key, _ := randomID("create_")
		v := f.create(t, key)
		last = v.ID
		f.dispatch(t, v.ID)
		must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	}
	f.s.ArtifactWorker = &videoartifact.Worker{MaxAttempts: 3}
	query := f.s.artifactCandidates(context.Background(), f.now).Where("v.org_id = ?", f.key.OrgID)
	var rows []videoartifact.Record
	must(t, f.s.DB.Table("(?) AS eligible", query).Where("scope_rank = 1").Find(&rows).Error)
	if len(rows) != 1 {
		t.Fatalf("same scope admitted %d simultaneous transfers", len(rows))
	}
	before := f.gets.Load()
	_, err := f.s.ResolveSource(context.Background(), f.key.OrgID, last, videoartifact.Video)
	if !errors.Is(err, videoartifact.ErrSourceDeferred) || f.gets.Load() != before {
		t.Fatalf("rate denial sent HTTP or spent a transfer: %v", err)
	}
	f.now = f.now.Add(2 * time.Second)
	source, err := f.s.ResolveSource(context.Background(), f.key.OrgID, last, videoartifact.Video)
	must(t, err)
	if !strings.Contains(source, "signature=secret") || f.gets.Load() != before+1 {
		t.Fatal("later source resolution not admitted")
	}
}

func readyArtifact(t *testing.T, f *fixture, id string) videoartifact.Repository {
	t.Helper()
	repo := videoartifact.Repository{DB: f.s.DB}
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		_, e := repo.RegisterTx(tx, videoartifact.Spec{OrgID: f.key.OrgID, TaskID: id, Kind: videoartifact.Video, ExpiresAt: f.now.Add(time.Hour)}, f.now)
		return e
	}))
	lease, e := repo.Claim(context.Background(), f.key.OrgID, id, videoartifact.Video, f.now, time.Minute, false, 3)
	must(t, e)
	must(t, repo.Complete(context.Background(), lease, videoartifact.Metadata{MediaType: "video/mp4", Size: 20, SHA256: strings.Repeat("a", 64)}, f.now))
	return repo
}

func TestMySQLArtifactProjectionUsesCurrentFencedRecord(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "artifact-projection")
	repo := readyArtifact(t, f, v.ID)
	// A loser sees a terminal artifact error. Its projection must use the
	// winner's current record, not translate the old attempt error to failed.
	_, err := repo.Claim(context.Background(), f.key.OrgID, v.ID, videoartifact.Video, f.now, time.Minute, true, 3)
	if err == nil {
		t.Fatal("ready artifact unexpectedly claimed")
	}
	must(t, f.s.syncArtifactProjection(context.Background(), f.key.OrgID, v.ID, f.now))
	state, e := f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if state.ArtifactStatus != "ready" {
		t.Fatal("late losing attempt overwrote ready")
	}
	f.now = f.now.Add(2 * time.Hour)
	must(t, f.s.syncArtifactProjection(context.Background(), f.key.OrgID, v.ID, f.now))
	state, e = f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if state.ArtifactStatus != "expired" {
		t.Fatal("expiry was not projected")
	}
}

type failingContentBlobs struct{ shortOnly bool }

func (b failingContentBlobs) Put(context.Context, string, io.Reader, videoartifact.Metadata) error {
	return errors.New("unused")
}
func (b failingContentBlobs) Delete(context.Context, string) error { return nil }
func (b failingContentBlobs) Open(context.Context, string) (io.ReadCloser, error) {
	if b.shortOnly {
		return io.NopCloser(strings.NewReader("partial")), nil
	}
	return &interruptedVideoBody{}, nil
}

type interruptedVideoBody struct{ sent bool }

func (b *interruptedVideoBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(p, "partial"), nil
}
func (*interruptedVideoBody) Close() error { return nil }

func TestMySQLContentFailureAuditsResourceAndIncompleteDelivery(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "read_error", true: "short_eof"}[short], func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "content-incomplete")
			repo := readyArtifact(t, f, v.ID)
			f.s.ArtifactService = &videoartifact.Service{Repo: repo, Blobs: failingContentBlobs{shortOnly: short}}
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.GET("/v1/video/generations/:id/content", f.s.Middleware(), f.s.HandleContent)
			req := httptest.NewRequest("GET", "/v1/video/generations/"+v.ID+"/content", nil)
			req.Header.Set("Authorization", "Bearer "+f.plain)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != 200 || w.Body.String() != "partial" || w.Header().Get("Content-Length") != "20" {
				t.Fatalf("unexpected stream %d %q", w.Code, w.Body.String())
			}
			var log invocation.VideoRequestLog
			must(t, f.s.DB.Where("request_id = ?", w.Header().Get("X-Request-Id")).First(&log).Error)
			if log.ResourceType != "task" || log.ResourceID != v.ID || log.Outcome != "incomplete" || log.HTTPStatus != 0 || log.ResponseBytes != 7 || log.SafeErrorCode != "content_stream_interrupted" {
				t.Fatalf("incorrect durable stream audit %+v", log)
			}
		})
	}
}

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
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "image_url", ImageURL: &ark.MediaReference{URL: "asset://" + asset.ID}}}}
	v, _, err := f.s.Create(context.Background(), f.key, "asset_request", "asset-revoke-before-post", in)
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
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "image_url", ImageURL: &ark.MediaReference{URL: "asset://" + asset.ID}}}}
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
