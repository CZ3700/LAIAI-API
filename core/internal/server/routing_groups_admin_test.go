package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const routingAdminTestSchema = `
CREATE TABLE routing_groups(id INTEGER PRIMARY KEY AUTOINCREMENT,slug TEXT NOT NULL,name TEXT NOT NULL,description TEXT NOT NULL,owner_org_id INTEGER NOT NULL,status TEXT NOT NULL,policy_version INTEGER NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(owner_org_id,slug));
CREATE TABLE channel_group_bindings(group_id INTEGER NOT NULL,channel_id INTEGER NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,PRIMARY KEY(group_id,channel_id));
CREATE TABLE org_group_grants(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,group_id INTEGER NOT NULL,status TEXT NOT NULL,grant_version INTEGER NOT NULL,default_slot INTEGER,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,group_id),UNIQUE(org_id,default_slot));`

type routingAdminHTTPFixture struct {
	db     *gorm.DB
	engine *gin.Engine
	s      *Server
	tokens map[string]string
}

var routingAdminFixtureID atomic.Uint64

func newRoutingAdminHTTPFixture(t *testing.T) *routingAdminHTTPFixture {
	t.Helper()
	return newRoutingAdminHTTPFixtureAt(t, fmt.Sprintf("file:routing-admin-%d?mode=memory&cache=shared", routingAdminFixtureID.Add(1)))
}

func newRoutingAdminHTTPFixtureAt(t *testing.T, dsn string) *routingAdminHTTPFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(12)
	pool.SetMaxIdleConns(12)
	t.Cleanup(func() { _ = pool.Close() })
	for _, schema := range []string{testSchema, relayTestSchema, routingAdminTestSchema} {
		if err := db.Exec(schema).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, org := range []int{1, 2, 3} {
		if err := db.Exec("INSERT INTO organizations(id,slug,name) VALUES(?,?,?)", org, fmt.Sprintf("org-%d", org), "Fixture Org").Error; err != nil {
			t.Fatal(err)
		}
	}
	roles := []struct {
		name, role string
		status     int
	}{{"su", "superadmin", 1}, {"su2", "superadmin", 1}, {"su3", "superadmin", 1}, {"admin", "admin", 1}, {"user", "user", 1}, {"banned", "superadmin", 0}}
	tokens := make(map[string]string)
	for i, row := range roles {
		uid := uint64(i + 1)
		if err := db.Exec("INSERT INTO users(id,email,email_canonical,role,status) VALUES(?,?,?,?,?)", uid, row.name+"@example.test", row.name+"@example.test", row.role, row.status).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO org_members(org_id,user_id,role) VALUES(1,?,'owner')", uid).Error; err != nil {
			t.Fatal(err)
		}
		token, _, err := identity.IssueSessionToken(testSecret, uid, 1, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		tokens[row.name] = token
	}
	for _, ch := range []struct{ id, owner int }{{1, 0}, {2, 1}, {3, 2}} {
		if err := db.Exec("INSERT INTO channels(id,name,provider,base_url,credential_enc,models,owner_org_id) VALUES(?,?,'test','https://never-send.invalid/private-addr','secret-credential-not-output','private-mapping-model',?)", ch.id, fmt.Sprintf("Channel %d", ch.id), ch.owner).Error; err != nil {
			t.Fatal(err)
		}
	}
	engine, s := NewWithServer(db, Options{AuthSecret: testSecret, AdminToken: []byte(testAdminToken), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return &routingAdminHTTPFixture{db: db, engine: engine, s: s, tokens: tokens}
}

// Real router and middleware; helpers do not inject actor context or replace any
// routing-domain service. Also reusable by the isolated MySQL acceptance suite.
func routingAdminTestRequest(handler http.Handler, ctx context.Context, method, path, token, root, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "client-spoofed-request")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if root != "" {
		req.Header.Set("X-Admin-Token", root)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func (f *routingAdminHTTPFixture) request(method, path, token, body string) *httptest.ResponseRecorder {
	return routingAdminTestRequest(f.engine, context.Background(), method, path, token, "", body)
}

func routingAdminTestJSON(t *testing.T, w *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d want %d: %s", w.Code, status, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if status >= 200 && status < 300 && out["grouped_key_issuance_enabled"] != false {
		t.Fatalf("missing closed issuance gate: %v", out)
	}
	return out
}

func (f *routingAdminHTTPFixture) create(t *testing.T, owner uint64, slug string) uint64 {
	t.Helper()
	out := routingAdminTestJSON(t, f.request("POST", "/api/admin/routing-groups", f.tokens["su"], fmt.Sprintf(`{"owner_org_id":%d,"slug":%q,"name":"Group","description":"kept"}`, owner, slug)), 201)
	group := out["group"].(map[string]any)
	if group["status"] != "draft" || group["policy_version"] != float64(1) {
		t.Fatalf("invalid initial group %v", group)
	}
	return uint64(group["id"].(float64))
}

func routingAdminTableCount(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var count int64
	if err := db.Table(table).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func TestRoutingGroupsAdminRequiresNamedActiveSuperadmin(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	requests := []struct{ method, path, body string }{{"GET", "", ""}, {"POST", "", `{"owner_org_id":0,"slug":"denied","name":"Denied"}`}, {"GET", "/1", ""}, {"PATCH", "/1", `{"expected_version":1,"status":"enabled"}`}, {"GET", "/1/channels", ""}, {"PUT", "/1/channels/1", `{"expected_group_version":1}`}, {"DELETE", "/1/channels/1", `{"expected_group_version":1}`}, {"POST", "/1/impact-preview", `{"expected_version":1,"change":{"kind":"update","status":"enabled"}}`}}
	for _, auth := range []struct {
		name, token, root string
		status            int
	}{{"missing", "", "", 401}, {"api-key", "sk-fake-key", "", 401}, {"member", f.tokens["user"], "", 403}, {"admin", f.tokens["admin"], "", 403}, {"disabled", f.tokens["banned"], "", 401}, {"root", "", testAdminToken, 403}, {"root-and-session", f.tokens["su"], testAdminToken, 403}} {
		t.Run(auth.name, func(t *testing.T) {
			for _, r := range requests {
				w := routingAdminTestRequest(f.engine, context.Background(), r.method, "/api/admin/routing-groups"+r.path, auth.token, auth.root, r.body)
				routingAdminTestJSON(t, w, auth.status)
			}
		})
	}
	if routingAdminTableCount(t, f.db, "routing_groups") != 0 || routingAdminTableCount(t, f.db, "routing_policy_events") != 0 {
		t.Fatal("denied request changed configuration")
	}
	routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups", f.tokens["su"], ""), 200)
	if err := f.db.Exec("UPDATE users SET role='admin' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups", f.tokens["su"], ""), 403)
	if err := f.db.Exec("UPDATE users SET role='superadmin' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Callback().Query().Before("gorm:query").Register("routing-auth-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("private SQL authentication failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/api/admin/routing-groups", f.tokens["su"], "")
	if w.Code < 400 || strings.Contains(w.Body.String(), "private SQL") {
		t.Fatalf("identity DB fault failed open/leaked %s", w.Body.String())
	}
	_ = f.db.Callback().Query().Remove("routing-auth-failure")
}

func TestRoutingGroupsAdminStrictJSONAndURLNeverMutate(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	invalid := []string{`null`, `[]`, `"x"`, `{}`, `{"slug":"x","name":"x"}`, `{"owner_org_id":0,"slug":"x","name":"x","status":"enabled"}`, `{"owner_org_id":0,"slug":"x","name":"a","name":"b"}`, `{"owner_org_id":0,"slug":"x","name":"a","\u006eame":"b"}`, `{"owner_org_id":0,"slug":"x","Name":"x"}`, `{"owner_org_id":null,"slug":"x","name":"x"}`, `{"owner_org_id":0,"slug":"x","name":null}`, `{"owner_org_id":0,"slug":"x","name":"x","description":null}`, `{"owner_org_id":-1,"slug":"x","name":"x"}`, `{"owner_org_id":1.0,"slug":"x","name":"x"}`, `{"owner_org_id":1e0,"slug":"x","name":"x"}`, `{"owner_org_id":"0","slug":"x","name":"x"}`, `{"owner_org_id":18446744073709551616,"slug":"x","name":"x"}`, `{"owner_org_id":0,"slug":"x","name":"x"} {}`, `{"owner_org_id":0,"slug":"x","name":"x","description":"` + strings.Repeat("x", routingAdminBodyLimit) + `"}`}
	for i, body := range invalid {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			routingAdminTestJSON(t, f.request("POST", "/api/admin/routing-groups", f.tokens["su"], body), 400)
		})
	}
	for _, query := range []string{"owner_org_id=-1", "owner_org_id=1&owner_org_id=2", "owner_org_id=", "status=unknown", "status=", "cursor=-1", "cursor=18446744073709551616", "limit=0", "limit=101", "limit=1.0", "limit=1&limit=2", "unknown=1", "status=%ZZ", "owner_org_id=1;status=draft"} {
		routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups?"+query, f.tokens["su"], ""), 400)
	}
	if routingAdminTableCount(t, f.db, "routing_groups") != 0 || routingAdminTableCount(t, f.db, "routing_policy_events") != 0 {
		t.Fatal("invalid create changed configuration")
	}
	id := f.create(t, 0, "valid")
	path := fmt.Sprintf("/api/admin/routing-groups/%d", id)
	for _, body := range []string{`{"status":"enabled"}`, `{"expected_version":0,"status":"enabled"}`, `{"expected_version":1}`, `{"expected_version":1,"name":null}`, `{"expected_version":1,"owner_org_id":1}`, `{"expected_version":1,"slug":"new"}`, `{"expected_version":1,"actor":2}`, `{"expected_version":1,"status":"unknown"}`} {
		routingAdminTestJSON(t, f.request("PATCH", path, f.tokens["su"], body), 400)
	}
	for _, body := range []string{`{}`, `{"expected_group_version":null}`, `{"expected_group_version":0}`, `{"expected_group_version":1,"force":true}`} {
		routingAdminTestJSON(t, f.request("DELETE", path+"/channels/1", f.tokens["su"], body), 400)
	}
	for _, body := range []string{`{"expected_version":1,"change":null}`, `{"expected_version":1,"change":{"kind":"update","status":"enabled","status":"disabled"}}`, `{"expected_version":1,"change":{"kind":"update","status":null}}`, `{"expected_version":1,"change":{"kind":"update","unknown":1}}`, `{"expected_version":1,"change":{"kind":"bind","channel_id":1,"name":"x"}}`, `{"expected_version":1,"change":{"kind":"update","channel_id":1,"name":"x"}}`} {
		routingAdminTestJSON(t, f.request("POST", path+"/impact-preview", f.tokens["su"], body), 400)
	}
	for _, bad := range []string{"0", "-1", "1.0", "18446744073709551616"} {
		routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups/"+bad, f.tokens["su"], ""), 400)
	}
	if routingAdminTableCount(t, f.db, "routing_policy_events") != 1 || routingAdminTableCount(t, f.db, "channel_group_bindings") != 0 {
		t.Fatal("invalid mutation wrote event/binding")
	}
}

func TestRoutingGroupsAdminLifecycleCASOwnershipAndSafeDTO(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	id := f.create(t, 0, "platform")
	path := fmt.Sprintf("/api/admin/routing-groups/%d", id)
	before := routingAdminTableCount(t, f.db, "routing_policy_events")
	preview := routingAdminTestJSON(t, f.request("POST", path+"/impact-preview", f.tokens["su"], `{"expected_version":1,"change":{"kind":"update","status":"enabled"}}`), 200)
	if preview["preview_kind"] != "structure_only" || preview["effective_model_impact_available"] != false || preview["observed_group_version"] != float64(1) || preview["proposed_group"].(map[string]any)["policy_version"] != float64(1) {
		t.Fatalf("false preview authority %v", preview)
	}
	if routingAdminTableCount(t, f.db, "routing_policy_events") != before {
		t.Fatal("preview wrote event")
	}
	out := routingAdminTestJSON(t, f.request("PATCH", path, f.tokens["su"], `{"expected_version":1,"status":"enabled"}`), 200)
	g := out["group"].(map[string]any)
	if g["name"] != "Group" || g["description"] != "kept" || g["policy_version"] != float64(2) || out["changed"] != true {
		t.Fatalf("PATCH lost unspecified fields: %v", out)
	}
	out = routingAdminTestJSON(t, f.request("PATCH", path, f.tokens["su"], `{"expected_version":1,"status":"enabled"}`), 409)
	if out["code"] != "policy_version_conflict" {
		t.Fatal(out)
	}
	out = routingAdminTestJSON(t, f.request("PATCH", path, f.tokens["su"], `{"expected_version":2,"status":"enabled"}`), 200)
	if out["changed"] != false || out["group"].(map[string]any)["policy_version"] != float64(2) {
		t.Fatal("same-value patch incremented version")
	}
	routingAdminTestJSON(t, f.request("PUT", path+"/channels/2", f.tokens["su"], `{"expected_group_version":2}`), 403)
	routingAdminTestJSON(t, f.request("POST", path+"/impact-preview", f.tokens["su"], `{"expected_version":2,"change":{"kind":"bind","channel_id":2}}`), 403)
	w := f.request("PUT", path+"/channels/1", f.tokens["su"], `{"expected_group_version":2}`)
	out = routingAdminTestJSON(t, w, 200)
	if out["group"].(map[string]any)["policy_version"] != float64(3) || out["changed"] != true {
		t.Fatal(out)
	}
	var event routinggroup.Event
	if err := f.db.Order("id DESC").Take(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.ActorUserID != 1 || event.RequestID == "client-spoofed-request" || event.RequestID == "" || event.RequestID != w.Header().Get("X-Request-Id") {
		t.Fatalf("untrusted audit identity %#v", event)
	}
	w = f.request("GET", path+"/channels", f.tokens["su"], "")
	out = routingAdminTestJSON(t, w, 200)
	items := out["items"].([]any)
	if len(items) != 1 {
		t.Fatal(out)
	}
	channel := items[0].(map[string]any)["channel"].(map[string]any)
	if len(channel) != 5 || channel["name"] != "Channel 1" {
		t.Fatalf("unsafe channel DTO %v", channel)
	}
	for _, secret := range []string{"credential", "base_url", "private-addr", "private-mapping", "models", "param_override"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("binding DTO leaked %q", secret)
		}
	}
	routingAdminTestJSON(t, f.request("PATCH", path, f.tokens["su"], `{"expected_version":3,"status":"disabled"}`), 200)
	routingAdminTestJSON(t, f.request("DELETE", path+"/channels/1", f.tokens["su"], `{"expected_group_version":4}`), 200)
	if routingAdminTableCount(t, f.db, "channels") != 3 || routingAdminTableCount(t, f.db, "channel_group_bindings") != 0 {
		t.Fatal("unbind deleted channel or left relation")
	}
	private := f.create(t, 1, "private")
	privatePath := fmt.Sprintf("/api/admin/routing-groups/%d", private)
	routingAdminTestJSON(t, f.request("PUT", privatePath+"/channels/2", f.tokens["su"], `{"expected_group_version":1}`), 200)
	routingAdminTestJSON(t, f.request("PUT", privatePath+"/channels/3", f.tokens["su"], `{"expected_group_version":2}`), 403)
	routingAdminTestJSON(t, f.request("PUT", privatePath+"/channels/1", f.tokens["su"], `{"expected_group_version":2}`), 403)
	routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups/999999", f.tokens["su"], ""), 404)
	for _, table := range []string{"ledger_journal", "ledger_holds", "tasks", "api_keys", "model_prices", "org_group_grants"} {
		if routingAdminTableCount(t, f.db, table) != 0 {
			t.Fatalf("unexpected %s mutation", table)
		}
	}
}

func TestRoutingGroupsAdminSeekPagination(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	for i := 0; i < 23; i++ {
		f.create(t, uint64(i%2), fmt.Sprintf("group-%02d", i))
	}
	out := routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups?limit=20", f.tokens["su"], ""), 200)
	if len(out["items"].([]any)) != 20 || out["has_more"] != true {
		t.Fatal(out)
	}
	cursor := uint64(out["next_cursor"].(float64))
	out = routingAdminTestJSON(t, f.request("GET", fmt.Sprintf("/api/admin/routing-groups?limit=20&cursor=%d", cursor), f.tokens["su"], ""), 200)
	if len(out["items"].([]any)) != 3 || out["has_more"] != false {
		t.Fatal(out)
	}
	out = routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups?owner_org_id=0&status=draft&limit=100", f.tokens["su"], ""), 200)
	if len(out["items"].([]any)) != 12 {
		t.Fatal(out)
	}
	if _, ok := out["total"]; ok {
		t.Fatal("seek endpoint advertised unbounded count")
	}
}

func TestRoutingGroupsAdminDuplicateGroupConflictIsSpecific(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	path := "/api/admin/routing-groups"
	body := `{"owner_org_id":0,"slug":"same","name":"Original"}`
	routingAdminTestJSON(t, f.request("POST", path, f.tokens["su"], body), 201)
	w := f.request("POST", path, f.tokens["su"], body)
	out := routingAdminTestJSON(t, w, 409)
	if out["code"] != "routing_group_exists" || strings.Contains(w.Body.String(), "UNIQUE") || strings.Contains(w.Body.String(), "Original") {
		t.Fatal("duplicate group has wrong or unsafe error", out)
	}
	if routingAdminTableCount(t, f.db, "routing_groups") != 1 || routingAdminTableCount(t, f.db, "routing_policy_events") != 1 {
		t.Fatal("duplicate group changed persisted configuration")
	}
	routingAdminTestJSON(t, f.request("POST", path, f.tokens["su"], `{"owner_org_id":1,"slug":"same","name":"Private"}`), 201)
	// Force a real event UNIQUE error after the next successful group INSERT.
	if err := f.db.Create(&routinggroup.Event{AggregateType: "group", AggregateID: 3, AggregateVersion: 1, EventKind: "fixture", ActorUserID: 1, RequestID: "fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	w = f.request("POST", path, f.tokens["su"], `{"owner_org_id":0,"slug":"event-conflict","name":"Never committed"}`)
	out = routingAdminTestJSON(t, w, 503)
	if out["code"] != "routing_group_unavailable" || strings.Contains(w.Body.String(), "UNIQUE") || strings.Contains(w.Body.String(), "Never committed") {
		t.Fatal("event failure was mislabeled or leaked", out)
	}
	if routingAdminTableCount(t, f.db, "routing_groups") != 2 || routingAdminTableCount(t, f.db, "routing_policy_events") != 3 {
		t.Fatal("event conflict left partial group writes")
	}
	if err := f.db.Callback().Create().Before("gorm:create").Register("routing-create-database-fault", func(tx *gorm.DB) {
		if tx.Statement.Table == "routing_groups" {
			tx.AddError(errors.New("UNIQUE constraint failed: private SQL values"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = f.request("POST", path, f.tokens["su"], body)
	routingAdminTestJSON(t, w, 503)
	if strings.Contains(w.Body.String(), "private SQL") {
		t.Fatal("untyped database fault leaked")
	}
	if err := f.db.Callback().Create().Remove("routing-create-database-fault"); err != nil {
		t.Fatal(err)
	}
}

// The real read transaction commits before the injected intervening change.
// This deterministic SQLite fault models the pre-read/write boundary; actual
// lock/isolation concurrency is separately accepted against MySQL.
type routingAdminCommitPool struct {
	*sql.DB
	after func()
}

func (p routingAdminCommitPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &routingAdminCommitTx{Tx: tx, after: p.after}, nil
}

type routingAdminCommitTx struct {
	*sql.Tx
	after func()
}

func (tx *routingAdminCommitTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	tx.after()
	return nil
}

func TestRoutingGroupsAdminPatchRejectsFutureVersionAfterReadSnapshot(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	id := f.create(t, 0, "patch-version-race")
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	var injected atomic.Bool
	var injectionErr error
	f.s.db = f.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	f.s.db.Statement.ConnPool = routingAdminCommitPool{DB: pool, after: func() {
		if injected.CompareAndSwap(false, true) {
			injectionErr = f.db.Exec("UPDATE routing_groups SET description='concurrent-description',policy_version=2 WHERE id=?", id).Error
		}
	}}
	out := routingAdminTestJSON(t, f.request("PATCH", fmt.Sprintf("/api/admin/routing-groups/%d", id), f.tokens["su"], `{"expected_version":2,"name":"requested-name"}`), 409)
	if !injected.Load() || injectionErr != nil || out["code"] != "policy_version_conflict" {
		t.Fatalf("missing intervening committed change: injected=%v err=%v response=%v", injected.Load(), injectionErr, out)
	}
	var group routinggroup.Group
	if err := f.db.First(&group, id).Error; err != nil {
		t.Fatal(err)
	}
	if group.Name != "Group" || group.Description != "concurrent-description" || group.PolicyVersion != 2 || routingAdminTableCount(t, f.db, "routing_policy_events") != 1 {
		t.Fatalf("PATCH overwrote a field absent from the request: %#v", group)
	}
}

func TestRoutingGroupsAdminReadSlotsAndErrorsRelease(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	// Saturate with real authorized requests blocked in their first domain read.
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	var workers sync.WaitGroup
	var block atomic.Bool
	block.Store(true)
	if err := f.db.Callback().Query().Before("gorm:query").Register("routing-read-barrier", func(tx *gorm.DB) {
		if tx.Statement.Table == "routing_groups" && block.Load() {
			entered <- struct{}{}
			select {
			case <-release:
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		workers.Wait()
		_ = f.db.Callback().Query().Remove("routing-read-barrier")
	})
	results := make(chan *httptest.ResponseRecorder, 4)
	for i, actor := range []string{"su", "su", "su2", "su2"} {
		workers.Add(1)
		go func(token string) {
			defer workers.Done()
			results <- f.request("GET", "/api/admin/routing-groups", token, "")
		}(f.tokens[actor])
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request never entered read")
		}
		if i == 1 {
			w := f.request("GET", "/api/admin/routing-groups", f.tokens["su"], "")
			routingAdminTestJSON(t, w, 429)
			if w.Header().Get("Retry-After") != "1" {
				t.Fatal("missing retry hint")
			}
		}
	}
	routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups", f.tokens["su3"], ""), 429)
	block.Store(false)
	once.Do(func() { close(release) })
	for i := 0; i < 4; i++ {
		routingAdminTestJSON(t, <-results, 200)
	}
	_ = f.db.Callback().Query().Remove("routing-read-barrier")
	if f.s.routingGroupReads.active != 0 || len(f.s.routingGroupReads.actors) != 0 {
		t.Fatal("completed requests leaked read slots")
	}
	if err := f.db.Callback().Query().Before("gorm:query").Register("routing-domain-fault", func(tx *gorm.DB) {
		if tx.Statement.Table == "routing_groups" {
			tx.AddError(errors.New("private model credential SQL must not escape"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/api/admin/routing-groups", f.tokens["su"], "")
	routingAdminTestJSON(t, w, 503)
	if strings.Contains(w.Body.String(), "private model") {
		t.Fatal("raw database error escaped")
	}
	_ = f.db.Callback().Query().Remove("routing-domain-fault")
	routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups", f.tokens["su"], ""), 200)
	if f.s.routingGroupReads.active != 0 || len(f.s.routingGroupReads.actors) != 0 {
		t.Fatal("error leaked read slots")
	}
}

func TestRoutingGroupsAdminCancelledAndTimedOutReadsRelease(t *testing.T) {
	for _, callerCancel := range []bool{true, false} {
		t.Run(fmt.Sprint(callerCancel), func(t *testing.T) {
			// database/sql may discard the last SQLite connection on cancellation.
			// A real file preserves authentication rows across that reconnection.
			f := newRoutingAdminHTTPFixtureAt(t, filepath.Join(t.TempDir(), "routing-admin.db"))
			entered := make(chan struct{})
			var once sync.Once
			if err := f.db.Callback().Query().Before("gorm:query").Register("routing-read-cancel", func(tx *gorm.DB) {
				if tx.Statement.Table == "routing_groups" {
					once.Do(func() { close(entered) })
					<-tx.Statement.Context.Done()
					tx.AddError(tx.Statement.Context.Err())
				}
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan *httptest.ResponseRecorder, 1)
			done := make(chan struct{})
			t.Cleanup(func() { cancel(); <-done })
			start := time.Now()
			go func() {
				defer close(done)
				result <- routingAdminTestRequest(f.engine, ctx, "GET", "/api/admin/routing-groups", f.tokens["su"], "", "")
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("did not enter read")
			}
			if callerCancel {
				cancel()
			}
			select {
			case w := <-result:
				routingAdminTestJSON(t, w, 503)
			case <-time.After(1500 * time.Millisecond):
				t.Fatal("query ignored cancellation/deadline")
			}
			if time.Since(start) > 1500*time.Millisecond {
				t.Fatal("read exceeded one-second request budget")
			}
			_ = f.db.Callback().Query().Remove("routing-read-cancel")
			if f.s.routingGroupReads.active != 0 || len(f.s.routingGroupReads.actors) != 0 {
				t.Fatal("cancelled request leaked slot")
			}
			routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups", f.tokens["su"], ""), 200)
		})
	}
}
