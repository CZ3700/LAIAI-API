package video

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type allowGate struct{}

func (allowGate) GateKey(*identity.APIKey, string, string) (int, string, bool) { return 200, "", true }
func (allowGate) AllowModelRate(uint64, string) (bool, int)                    { return true, 0 }
func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

type fixture struct {
	s        *Service
	key      *identity.APIKey
	plain    string
	now      time.Time
	posts    atomic.Int32
	gets     atomic.Int32
	response func(*http.Request) (*http.Response, error)
}

func mysqlFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN is required for real MySQL integration")
	}
	// Refuse accidental use against a non-test database. Migrations are applied
	// explicitly by CI/setup, never by ORM or destructive test resets.
	if !strings.Contains(dsn, "/ark_test?") {
		t.Fatal("integration requires the dedicated ark_test database")
	}
	db, e := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if e != nil {
		t.Fatal(e)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(20)
	t.Cleanup(func() { _ = sqlDB.Close() })
	kr, e := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	s, e := NewService(db, kr, []byte(strings.Repeat("h", 32)), nil)
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{s: s, now: now}
	s.Now = func() time.Time { return f.now }
	s.Gate = allowGate{}
	uid, _ := randomID("")
	org := identity.Organization{Slug: uid, Name: "Ark integration fixture", Group: "default", Status: 1}
	must(t, db.Create(&org).Error)
	must(t, db.Transaction(func(tx *gorm.DB) error {
		var e error
		f.plain, f.key, e = identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, Name: "integration", Scopes: "video:*"})
		return e
	}))
	must(t, db.Transaction(func(tx *gorm.DB) error {
		a, e := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		_, _, e = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "test:" + uid, AccountID: a.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100), RefType: "integration", RefID: uid})
		return e
	}))
	encrypted, e := kr.Encrypt("official-test-credential")
	must(t, e)
	ch := relay.Channel{Name: "Ark integration", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", CredentialEnc: encrypted, Models: ark.Model20, Weight: 1, Status: 1, MaxConcurrentTasks: 100}
	must(t, db.Create(&ch).Error)
	scope := ProviderScope{ScopeKey: uid, AccountRef: "test-" + uid, Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "test-fx", FXObservedAt: now}
	must(t, db.Create(&scope).Error)
	cap := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "test-cap-v1", Enabled: true, Verified: true, Resolutions: []string{"720p"}, Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: 15, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4}
	capJSON, _ := json.Marshal(cap)
	cfg := ModelConfig{PublicModel: ark.Model20, Version: "test-config", ProviderScopeID: scope.ID, ChannelID: ch.ID, Enabled: true, CapabilityJSON: string(capJSON), PricingVersion: uid, NoChargeStatuses: "failed,cancelled", BoundsJSON: `{"720p/16:9":{"width":1280,"height":720}}`, VerifiedAt: &now, UpdatedAt: now}
	must(t, db.Save(&cfg).Error)
	pricing := videobilling.PricingRule{RuleID: uid, Version: uid, PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: ark.Model20, Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "test-formula", SourceHash: strings.Repeat("a", 64), EffectiveFrom: now.Add(-time.Hour), Enabled: true}
	must(t, db.Create(&pricing).Error)
	must(t, db.Create(&Policy{OrgID: org.ID, AsyncExecutionAllowed: true, CaptureHistory: true, RetentionDays: 30, UpdatedAt: now}).Error)
	s.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			f.posts.Add(1)
		}
		if r.Method == "GET" {
			f.gets.Add(1)
		}
		if f.response != nil {
			return f.response(r)
		}
		if r.Method == "POST" {
			return reply(200, `{"id":"up_`+uid+`"}`), nil
		}
		return reply(200, `{"id":"up_`+uid+`","status":"succeeded","usage":{"completion_tokens":100,"total_tokens":999}}`), nil
	})}
	return f
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) create(t *testing.T, key string) TaskView {
	t.Helper()
	rid, _ := randomID("")
	v, _, e := f.s.Create(context.Background(), f.key, rid, key, ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "test prompt private"}}})
	must(t, e)
	return v
}
func (f *fixture) dispatch(t *testing.T, id string) {
	t.Helper()
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, id, "create_task").First(&op).Error)
	must(t, f.s.processOperation(context.Background(), op))
}
func (f *fixture) hold(t *testing.T, id string) ledger.Hold {
	t.Helper()
	var snap videobilling.Snapshot
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).First(&snap).Error)
	var h ledger.Hold
	must(t, f.s.DB.First(&h, snap.HoldID).Error)
	return h
}

func TestMySQLVideoCreateSettleReplay(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "same-request-1")
	replay := f.create(t, "same-request-1")
	if v.ID != replay.ID {
		t.Fatal("replay created a second task")
	}
	if h := f.hold(t, v.ID); h.Status != ledger.HoldActive {
		t.Fatal("not frozen")
	}
	f.dispatch(t, v.ID)
	f.dispatch(t, v.ID)
	if f.posts.Load() != 1 {
		t.Fatalf("POST count %d", f.posts.Load())
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	state, e := f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if state.Status != "succeeded" || state.BillingState != "settled" || state.Usage == nil || *state.Usage.CompletionTokens != 100 || *state.Usage.TotalTokens != 999 {
		t.Fatalf("bad final projection %+v", state)
	}
	if h := f.hold(t, v.ID); h.Status != ledger.HoldCaptured || h.CapturedAmount.String() != "0.0008" {
		t.Fatalf("bad charge %+v", h)
	}
	var count int64
	must(t, f.s.DB.Model(&invocation.Invocation{}).Where("org_id = ?", f.key.OrgID).Count(&count).Error)
	if count != 1 {
		t.Fatal("missing synchronous invocation")
	}
	// Retry after price disable/Key rotation is still the original acceptance.
	must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", ark.Model20).Update("enabled", false).Error)
	if again := f.create(t, "same-request-1"); again.ID != v.ID {
		t.Fatal("config change broke idempotency")
	}
	if _, e = f.s.Get(f.key.OrgID+100000, v.ID); !isNotFound(e) {
		t.Fatal("tenant leak")
	}
}

func TestMySQLVideoUnknownAndMissingUsageNeverRefund(t *testing.T) {
	t.Run("lost POST response", func(t *testing.T) {
		f := mysqlFixture(t)
		f.response = func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }
		v := f.create(t, "unknown-post-1")
		f.dispatch(t, v.ID)
		f.now = f.now.Add(72 * time.Hour)
		must(t, f.s.recoverOperations(context.Background()))
		f.dispatch(t, v.ID)
		state, e := f.s.Get(f.key.OrgID, v.ID)
		must(t, e)
		if state.Status != "submission_unknown" || f.posts.Load() != 1 || f.hold(t, v.ID).Status != ledger.HoldActive {
			t.Fatal("unknown task retried or refunded")
		}
	})
	t.Run("missing differs from zero", func(t *testing.T) {
		f := mysqlFixture(t)
		v := f.create(t, "missing-usage-1")
		f.dispatch(t, v.ID)
		var row task.Task
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
		body := `{"id":"` + row.UpstreamTaskID + `","status":"succeeded"}`
		f.response = func(*http.Request) (*http.Response, error) { return reply(200, body), nil }
		must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
		if f.hold(t, v.ID).Status != ledger.HoldActive {
			t.Fatal("missing usage refunded")
		}
		body = `{"id":"` + row.UpstreamTaskID + `","status":"succeeded","usage":{"completion_tokens":0}}`
		f.now = f.now.Add(11 * time.Second)
		must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
		state, e := f.s.Get(f.key.OrgID, v.ID)
		must(t, e)
		if state.BillingState != "settled" || state.Usage == nil || state.Usage.CompletionTokens == nil || *state.Usage.CompletionTokens != 0 {
			t.Fatal("real zero was not settled")
		}
	})
}

func TestMySQLVideoLocalCancelAndDelete(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "cancel-local-1")
	op, replay, e := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "cancel-request", "cancel-unique-1")
	must(t, e)
	if replay || op.Status != "confirmed" {
		t.Fatal("local cancellation not confirmed")
	}
	f.dispatch(t, v.ID)
	if f.posts.Load() != 0 || f.hold(t, v.ID).Status != ledger.HoldReleased {
		t.Fatal("cancelled task dispatched or hold retained")
	}
	same, replay, e := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "repeat-request", "cancel-unique-1")
	must(t, e)
	if !replay || same.ID != op.ID {
		t.Fatal("cancel replay mismatch")
	}
	deletion, done, e := f.s.Delete(context.Background(), f.key.OrgID, v.ID, "delete-request")
	must(t, e)
	if done || deletion.Status != "confirmed" {
		t.Fatal("local deletion")
	}
	_, done, e = f.s.Delete(context.Background(), f.key.OrgID, v.ID, "delete-again")
	must(t, e)
	if !done {
		t.Fatal("delete replay")
	}
	if _, e = f.s.Get(f.key.OrgID, v.ID); !isNotFound(e) {
		t.Fatal("deleted task readable")
	}
	must(t, f.s.owned(f.key.OrgID, v.ID))
}

func TestMySQLVideoDeleteAck404IsUnknown(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "cancel-race-1")
	f.dispatch(t, v.ID)
	op, _, e := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "cancel-request", "cancel-race-key")
	must(t, e)
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == "DELETE" {
			return reply(200, `{}`), nil
		}
		return reply(404, `{"error":{"code":"NotFound"}}`), nil
	}
	must(t, f.s.processMutation(context.Background(), op))
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	current, e := f.s.GetOperation(f.key.OrgID, op.ID)
	must(t, e)
	if current.Status != "unknown" || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("DELETE/404 falsely confirmed")
	}
}

func TestMySQLVideoConcurrentIdempotencyAndRollback(t *testing.T) {
	f := mysqlFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rid, _ := randomID("")
			v, _, e := f.s.Create(context.Background(), f.key, rid, "concurrent-create-1", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "same"}}})
			if e != nil {
				errs <- e
			} else {
				ids <- v.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Errorf("concurrent create: %v", e)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("duplicate tasks")
		}
	}
	var count int64
	must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ?", f.key.OrgID).Count(&count).Error)
	if count != 1 {
		t.Fatalf("task count %d", count)
	}
	_, _, e := f.s.Create(context.Background(), f.key, "changed-request", "concurrent-create-1", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "different"}}})
	if !errors.Is(e, ErrConflict) {
		t.Fatal("changed body not rejected")
	}
	// An event/outbox failure must roll back the hold, quota, payload and task.
	name := "test:reject-video-event-" + fmt.Sprint(f.key.OrgID)
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_events" {
			tx.AddError(errors.New("injected event failure"))
		}
	}))
	defer f.s.DB.Callback().Create().Remove(name)
	_, _, e = f.s.Create(context.Background(), f.key, "rollback-request", "rollback-create-1", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "rollback"}}})
	if e == nil {
		t.Fatal("injected error ignored")
	}
	must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ?", f.key.OrgID).Count(&count).Error)
	if count != 1 {
		t.Fatal("partial task committed")
	}
	must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ?", f.key.OrgID).Count(&count).Error)
	if count != 1 {
		t.Fatal("orphan hold snapshot")
	}
}

func TestMySQLVideoHTTPAuditAndRetention(t *testing.T) {
	f := mysqlFixture(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("", f.s.Middleware())
	g.POST("/v1/video/generations", f.s.HandleCreate)
	g.GET("/v1/video/generations/:id", f.s.HandleGet)
	req := httptest.NewRequest("POST", "/v1/video/generations", strings.NewReader(`{"model":"seedance-2.0","content":[{"type":"text","text":"test prompt private"}]}`))
	req.Header.Set("Authorization", "Bearer "+f.plain)
	req.Header.Set("Idempotency-Key", "http-audit-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	var log invocation.VideoRequestLog
	must(t, f.s.DB.Where("request_id = ?", w.Header().Get("X-Request-Id")).First(&log).Error)
	if log.Outcome != "completed" || log.HTTPStatus != 202 {
		t.Fatal("missing request finish")
	}
	if log.ResponsePayloadID == "" {
		t.Fatal("client response evidence missing")
	}
	responseBody, e := f.s.Evidence.ReadForHistory(context.Background(), f.key.OrgID, log.ResponsePayloadID, invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: fmt.Sprint(f.key.ID), ReasonCode: "response_test"})
	must(t, e)
	if !bytes.Contains(responseBody, []byte(`"billing_state":"reserved"`)) {
		t.Fatal("client response evidence did not preserve safe DTO")
	}
	g.GET("/v1/video/portrait-authorizations/:session_id", func(c *gin.Context) {
		c.JSON(200, gin.H{"session_id": c.Param("session_id"), "authorization_url": "https://identity.example.test?token=secret-h5-one-use"})
	})
	portrait := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v1/video/portrait-authorizations/pas_test", nil)
		req.Header.Set("Authorization", "Bearer "+f.plain)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	portraitResponse := portrait()
	if portraitResponse.Code != 200 {
		t.Fatal("portrait response")
	}
	log = invocation.VideoRequestLog{}
	must(t, f.s.DB.Where("request_id=?", portraitResponse.Header().Get("X-Request-Id")).First(&log).Error)
	responseBody, e = f.s.Evidence.ReadForHistory(context.Background(), f.key.OrgID, log.ResponsePayloadID, invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: fmt.Sprint(f.key.ID), ReasonCode: "response_test"})
	must(t, e)
	if bytes.Contains(responseBody, []byte("authorization_url")) || bytes.Contains(responseBody, []byte("secret-h5")) {
		t.Fatal("short-lived authorization secret entered response history")
	}
	denied := httptest.NewRecorder()
	r.ServeHTTP(denied, httptest.NewRequest("GET", "/v1/video/generations/task_missing", nil))
	if denied.Code != 401 {
		t.Fatal("unauthenticated response")
	}
	must(t, f.s.DB.Where("request_id = ?", denied.Header().Get("X-Request-Id")).First(&log).Error)
	must(t, f.s.DB.Model(&Policy{}).Where("org_id = ?", f.key.OrgID).Update("capture_history", false).Error)
	portraitResponse = portrait()
	log = invocation.VideoRequestLog{}
	must(t, f.s.DB.Where("request_id=?", portraitResponse.Header().Get("X-Request-Id")).First(&log).Error)
	if log.ResponsePayloadID != "" {
		t.Fatal("disabled response history captured body")
	}
	v := f.create(t, "zero-history-1")
	f.dispatch(t, v.ID)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	state, e := f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if state.BillingState != "settled" {
		t.Fatal("metadata-only history broke lifecycle")
	}
}
