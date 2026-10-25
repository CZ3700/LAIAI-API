package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

// These tests reuse B1's randomly named private schema, tracked migrations,
// real router/session middleware, and separate management callback registry.
// No test accepts an application DSN or changes the production service wiring.
func routingOrgMySQLGroup(t *testing.T, f *routingAdminHTTPFixture, owner uint64, slug string) uint64 {
	t.Helper()
	id := f.create(t, owner, slug)
	routingAdminTestJSON(t, f.request("PATCH", fmt.Sprintf("/api/admin/routing-groups/%d", id), f.tokens["su"], `{"expected_version":1,"status":"enabled"}`), 200)
	return id
}

func routingOrgMySQLPath(org, group uint64) string {
	return fmt.Sprintf("/api/admin/orgs/%d/routing-groups/%d", org, group)
}

func routingOrgMySQLDefaultBody(target, current *uint64, currentVersion, targetVersion uint64) string {
	cur := "null"
	if current != nil {
		cur = fmt.Sprintf(`{"group_id":%d,"grant_version":%d}`, *current, currentVersion)
	}
	if target == nil {
		return fmt.Sprintf(`{"target_group_id":null,"expected_current_default":%s}`, cur)
	}
	return fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":%s,"expected_target_grant_version":%d}`, *target, cur, targetVersion)
}

func routingOrgMySQLGrant(t *testing.T, f *routingAdminHTTPFixture, org, group, version uint64, status string) *httptest.ResponseRecorder {
	t.Helper()
	w := f.request("PUT", routingOrgMySQLPath(org, group), f.tokens["su"], fmt.Sprintf(`{"expected_grant_version":%d,"status":%q}`, version, status))
	routingAdminTestJSON(t, w, 200)
	return w
}

func routingOrgMySQLGrantState(t *testing.T, db *gorm.DB, org, group, version uint64, status string, isDefault bool) routinggroup.Grant {
	t.Helper()
	var grant routinggroup.Grant
	if err := db.Where("org_id=? AND group_id=?", org, group).Take(&grant).Error; err != nil {
		t.Fatal("cannot read independent grant fact")
	}
	if grant.GrantVersion != version || grant.Status != status || (grant.DefaultSlot != nil && *grant.DefaultSlot == 1) != isDefault {
		t.Fatalf("unexpected grant fact org=%d group=%d version=%d status=%s default=%v", org, group, grant.GrantVersion, grant.Status, grant.DefaultSlot != nil)
	}
	return grant
}

func routingOrgMySQLDefault(t *testing.T, f *routingAdminHTTPFixture, org uint64, target, current *uint64, currentVersion, targetVersion uint64, changed bool) *httptest.ResponseRecorder {
	t.Helper()
	w := f.request("PUT", fmt.Sprintf("/api/admin/orgs/%d/routing-default", org), f.tokens["su"], routingOrgMySQLDefaultBody(target, current, currentVersion, targetVersion))
	out := routingAdminTestJSON(t, w, 200)
	var want any
	if target != nil {
		want = float64(*target)
	}
	if out["changed"] != changed || out["applied_target_group_id"] != want {
		t.Fatal("default command receipt did not match the submitted intent")
	}
	return w
}

func TestMySQLRoutingOrgAdminLifecycleAndPreservation(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	a := routingOrgMySQLGroup(t, f, 0, "lifecycle-platform")
	b := routingOrgMySQLGroup(t, f, 1, "lifecycle-private")
	routingAdminSeedPreservedFacts(t, f.db)
	store, err := invocation.NewVideoStore(f.db, testKeyring(t), invocation.VideoStoreOptions{})
	if err != nil {
		t.Fatal("cannot create the real invocation preservation fixture")
	}
	prepared, err := store.Prepare(invocation.VideoPayloadInput{OrgID: 1, RequestID: "routing-preserved-request", AttemptID: "routing-preserved-attempt", Direction: invocation.VideoClientRequest, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: []byte(`{"prompt":"private preserved fixture","authorization":"never expose"}`), CaptureEnabled: true, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal("cannot encrypt independent preservation payload")
	}
	if err := f.db.Transaction(func(tx *gorm.DB) error { _, err := store.StageTx(tx, prepared); return err }); err != nil {
		t.Fatal("cannot stage independent preservation payload")
	}
	tables := []string{"ledger_accounts", "ledger_journal", "ledger_holds", "ledger_operations", "ledger_movements", "tasks", "api_keys", "model_prices", "channels", "channel_credentials", "video_billing_snapshots", "video_pricing_templates", "invocation_payloads", "invocation_payload_staging", "invocation_payload_accesses", "routing_groups"}
	before := routingAdminFingerprints(t, f.db, tables...)
	for _, table := range []string{"ledger_accounts", "ledger_journal", "ledger_holds", "tasks", "api_keys", "model_prices", "invocation_payloads", "invocation_payload_staging"} {
		if before[table].Rows == 0 {
			t.Fatal("empty preservation positive control", table)
		}
	}
	type expectedEvent struct {
		group, version uint64
		kind, request  string
	}
	var expected []expectedEvent
	remember := func(group, version uint64, kind string, w *httptest.ResponseRecorder) {
		expected = append(expected, expectedEvent{group, version, kind, w.Header().Get("X-Request-Id")})
	}
	preview := func(path, body string, changed bool) map[string]any {
		facts := routingAdminFingerprints(t, f.db, "routing_groups", "org_group_grants", "routing_policy_events")
		out := routingAdminTestJSON(t, f.request("POST", path+"/impact-preview", f.tokens["su"], body), 200)
		if out["preview_kind"] != "structure_only" || out["effective_model_impact_available"] != false || out["changed"] != changed || !reflect.DeepEqual(facts, routingAdminFingerprints(t, f.db, "routing_groups", "org_group_grants", "routing_policy_events")) {
			t.Fatal("preview changed persistent facts or advertised runtime authorization")
		}
		return out
	}
	creation := preview(routingOrgMySQLPath(1, a), `{"expected_grant_version":0,"status":"enabled"}`, true)
	observed := creation["observed_grants"].([]any)
	if len(observed) != 1 || observed[0].(map[string]any)["exists"] != false || observed[0].(map[string]any)["grant"] != nil {
		t.Fatal("new grant preview invented an existing authorization")
	}
	effects := creation["affected_grants"].([]any)
	if len(effects) != 1 || effects[0].(map[string]any)["grant_id"] != nil || effects[0].(map[string]any)["from_version"] != float64(0) || effects[0].(map[string]any)["to_version"] != float64(1) {
		t.Fatal("new grant preview invented a durable ID or version")
	}
	remember(a, 1, "grant_enabled", routingOrgMySQLGrant(t, f, 1, a, 0, "enabled"))
	remember(b, 1, "grant_enabled", routingOrgMySQLGrant(t, f, 1, b, 0, "enabled"))
	ga := routingOrgMySQLGrantState(t, f.db, 1, a, 1, "enabled", false)
	gb := routingOrgMySQLGrantState(t, f.db, 1, b, 1, "enabled", false)
	unchanged := routingAdminTestJSON(t, routingOrgMySQLGrant(t, f, 1, a, 1, "enabled"), 200)
	if unchanged["changed"] != false {
		t.Fatal("unchanged grant skipped its no-op semantics")
	}
	defaultPath := "/api/admin/orgs/1/routing-default"
	preview(defaultPath, routingOrgMySQLDefaultBody(&a, nil, 0, 1), true)
	remember(a, 2, "default_set", routingOrgMySQLDefault(t, f, 1, &a, nil, 0, 1, true))
	preview(defaultPath, routingOrgMySQLDefaultBody(&a, &a, 2, 2), false)
	routingOrgMySQLDefault(t, f, 1, &a, &a, 2, 2, false)
	switching := preview(defaultPath, routingOrgMySQLDefaultBody(&b, &a, 2, 1), true)
	effects = switching["affected_grants"].([]any)
	if len(effects) != 2 {
		t.Fatal("default switch preview omitted a grant")
	}
	for i, raw := range effects {
		effect := raw.(map[string]any)
		group, from, to, action, def := a, uint64(2), uint64(3), "default_clear", false
		if i == 1 {
			group, from, to, action, def = b, 1, 2, "default_set", true
		}
		if effect["group_id"] != float64(group) || effect["from_version"] != float64(from) || effect["to_version"] != float64(to) || effect["is_default"] != def || !reflect.DeepEqual(effect["actions"], []any{action}) {
			t.Fatal("default switch preview differs from its two-grant command")
		}
	}
	w := routingOrgMySQLDefault(t, f, 1, &b, &a, 2, 1, true)
	remember(a, 3, "default_cleared", w)
	remember(b, 2, "default_set", w)
	routingOrgMySQLGrantState(t, f.db, 1, a, 3, "enabled", false)
	routingOrgMySQLGrantState(t, f.db, 1, b, 2, "enabled", true)
	revoking := preview(routingOrgMySQLPath(1, b), `{"expected_grant_version":2,"status":"revoked"}`, true)
	effects = revoking["affected_grants"].([]any)
	if len(effects) != 1 {
		t.Fatal("revoke preview invented multiple grant versions")
	}
	effect := effects[0].(map[string]any)
	if effect["to_version"] != float64(3) || effect["is_default"] != false || !reflect.DeepEqual(effect["actions"], []any{"revoke", "default_clear"}) {
		t.Fatal("revoke preview did not clear the default in the same grant version")
	}
	remember(b, 3, "grant_revoked", routingOrgMySQLGrant(t, f, 1, b, 2, "revoked"))
	remember(b, 4, "grant_enabled", routingOrgMySQLGrant(t, f, 1, b, 3, "enabled"))
	routingOrgMySQLGrantState(t, f.db, 1, b, 4, "enabled", false)
	out := routingAdminTestJSON(t, f.request("GET", defaultPath, f.tokens["su"], ""), 200)
	if out["current_default"] != nil {
		t.Fatal("reenabling a grant resurrected its old default pointer")
	}
	remember(b, 5, "default_set", routingOrgMySQLDefault(t, f, 1, &b, nil, 0, 4, true))
	remember(b, 6, "default_cleared", routingOrgMySQLDefault(t, f, 1, nil, &b, 5, 0, true))
	routingOrgMySQLDefault(t, f, 1, nil, nil, 0, 0, false)
	routingOrgMySQLGrantState(t, f.db, 1, b, 6, "enabled", false)
	var events []routinggroup.Event
	if err := f.db.Where("aggregate_type='grant'").Order("id").Find(&events).Error; err != nil || len(events) != len(expected) {
		t.Fatal("grant events were omitted, duplicated or added by no-op/preview")
	}
	for i, event := range events {
		want := expected[i]
		id := ga.ID
		if want.group == b {
			id = gb.ID
		}
		if event.AggregateID != id || event.AggregateVersion != want.version || event.EventKind != want.kind || event.OrgID == nil || *event.OrgID != 1 || event.GroupID == nil || *event.GroupID != want.group || event.ActorUserID != 1 || event.RequestID == "" || event.RequestID != want.request || event.RequestID == "client-spoofed-request" {
			t.Fatalf("event %d differs from independent command history", i)
		}
	}
	for _, mode := range []string{"inherit_group", "subset"} {
		plain, key, err := identity.IssueKey(f.db, identity.IssueKeyInput{OrgID: 1, Name: "must-stay-closed", ModelAccessMode: &mode, RoutingGroupID: &a, ModelLimits: "fixture-model"})
		if err == nil || plain != "" || key != nil {
			t.Fatal("grant/default configuration opened grouped Key issuance")
		}
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, tables...)) {
		t.Fatal("grant/default HTTP changed preserved finance, Keys, Tasks, prices, channels, group versions or invocation ciphertext")
	}
}

func TestMySQLRoutingOrgAdminPermissionsNullAndInactiveCleanup(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	a := routingOrgMySQLGroup(t, f, 0, "permission-platform")
	foreign := routingOrgMySQLGroup(t, f, 2, "permission-private")
	path := routingOrgMySQLPath(1, a)
	def := "/api/admin/orgs/1/routing-default"
	requests := []struct{ method, path, body string }{
		{"GET", "/api/admin/orgs/1/routing-groups", ""}, {"PUT", path, `{"expected_grant_version":0,"status":"enabled"}`},
		{"GET", def, ""}, {"PUT", def, routingOrgMySQLDefaultBody(nil, nil, 0, 0)},
		{"POST", path + "/impact-preview", `{"expected_grant_version":0,"status":"enabled"}`}, {"POST", def + "/impact-preview", routingOrgMySQLDefaultBody(nil, nil, 0, 0)},
	}
	key, _, err := identity.IssueKey(f.db, identity.IssueKeyInput{OrgID: 1, Name: "real-api-token"})
	if err != nil {
		t.Fatal("cannot create real unprivileged Key")
	}
	before := routingAdminFingerprints(t, f.db, "org_group_grants", "routing_policy_events", "routing_groups")
	for _, request := range requests {
		for _, token := range []string{f.tokens["user"], f.tokens["admin"], key} {
			w := f.request(request.method, request.path, token, request.body)
			if w.Code != 401 && w.Code != 403 {
				t.Fatal("unprivileged identity reached routing policy API", w.Code)
			}
		}
		for _, token := range []string{"", f.tokens["su"]} {
			routingAdminTestJSON(t, routingAdminTestRequest(f.engine, context.Background(), request.method, request.path, token, testAdminToken, request.body), 403)
		}
	}
	routingAdminSQL(t, f.db, "UPDATE users SET status=0 WHERE id=2")
	w := f.request("GET", def, f.tokens["su2"], "")
	if w.Code != 401 && w.Code != 403 {
		t.Fatal("inactive superadmin reached routing policy API")
	}
	badGrant := []string{`{"status":"enabled"}`, `{"expected_grant_version":null,"status":"enabled"}`, `{"expected_grant_version":0,"status":null}`, `{"expected_grant_version":0,"status":"enabled","org_id":2}`, `{"expected_grant_version":0,"expected_grant_version":1,"status":"enabled"}`, `{"expected_grant_version":0.0,"status":"enabled"}`}
	for _, body := range badGrant {
		for _, suffix := range []string{"", "/impact-preview"} {
			method := "PUT"
			if suffix != "" {
				method = "POST"
			}
			routingAdminTestJSON(t, f.request(method, path+suffix, f.tokens["su"], body), 400)
		}
	}
	badDefault := []string{`{}`, `{"target_group_id":null}`, `{"expected_current_default":null}`, `{"target_group_id":null,"expected_current_default":null,"expected_target_grant_version":null}`, `{"target_group_id":null,"expected_current_default":null,"expected_target_grant_version":1}`, `{"target_group_id":null,"expected_current_default":{"group_id":1}}`, `{"target_group_id":null,"expected_current_default":{"group_id":1,"grant_version":null}}`, `{"target_group_id":null,"expected_current_default":{"group_id":1,"group_id":2,"grant_version":1}}`, `{"target_group_id":1,"expected_current_default":null}`, `{"target_group_id":null,"expected_current_default":null,"force":true}`}
	for _, body := range badDefault {
		for _, suffix := range []string{"", "/impact-preview"} {
			method := "PUT"
			if suffix != "" {
				method = "POST"
			}
			routingAdminTestJSON(t, f.request(method, def+suffix, f.tokens["su"], body), 400)
		}
	}
	routingAdminTestJSON(t, f.request("PUT", routingOrgMySQLPath(1, foreign), f.tokens["su"], `{"expected_grant_version":0,"status":"enabled"}`), 404)
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, "org_group_grants", "routing_policy_events", "routing_groups")) {
		t.Fatal("denied/malformed requests wrote policy facts")
	}
	routingOrgMySQLGrant(t, f, 1, a, 0, "enabled")
	routingOrgMySQLGrant(t, f, 2, a, 0, "enabled")
	routingOrgMySQLGrant(t, f, 2, foreign, 0, "enabled")
	routingOrgMySQLDefault(t, f, 1, &a, nil, 0, 1, true)
	routingAdminSQL(t, f.db, "UPDATE organizations SET status=0 WHERE id=1")
	routingAdminTestJSON(t, f.request("PATCH", fmt.Sprintf("/api/admin/routing-groups/%d", a), f.tokens["su"], `{"expected_version":2,"status":"disabled"}`), 200)
	out := routingAdminTestJSON(t, f.request("GET", def, f.tokens["su"], ""), 200)
	cur := out["current_default"].(map[string]any)
	if cur["group_id"] != float64(a) || cur["default_valid"] != false {
		t.Fatal("inactive configuration lost its default diagnostic pointer")
	}
	routingAdminTestJSON(t, f.request("PUT", path, f.tokens["su"], `{"expected_grant_version":2,"status":"enabled"}`), 403)
	routingAdminTestJSON(t, f.request("PUT", def, f.tokens["su"], routingOrgMySQLDefaultBody(&a, &a, 2, 2)), 403)
	routingOrgMySQLDefault(t, f, 1, nil, &a, 2, 0, true)
	routingOrgMySQLGrant(t, f, 1, a, 3, "revoked")
	routingOrgMySQLGrantState(t, f.db, 1, a, 4, "revoked", false)
}

// Both contenders begin actual transactions, then meet before their Org lock.
// Connection IDs prove this is a two-connection race, not sequential requests.
func routingOrgMySQLRace(t *testing.T, f *routingAdminHTTPFixture, requests [2]func() *httptest.ResponseRecorder) {
	t.Helper()
	arrived := make(chan uint64, 2)
	release := make(chan struct{})
	var once sync.Once
	var workers sync.WaitGroup
	var entered atomic.Int32
	name := "routing_org_real_cas_barrier"
	if err := f.db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table != "organizations" {
			return
		}
		if _, ok := q.Statement.Clauses["FOR"]; !ok || entered.Add(1) > 2 {
			return
		}
		var id uint64
		if err := q.Statement.ConnPool.QueryRowContext(q.Statement.Context, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			q.AddError(err)
			return
		}
		arrived <- id
		select {
		case <-release:
		case <-q.Statement.Context.Done():
			q.AddError(q.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	unblock := func() { once.Do(func() { close(release) }) }
	cleanup := func() { unblock(); workers.Wait(); _ = f.db.Callback().Query().Remove(name) }
	t.Cleanup(cleanup)
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, request := range requests {
		workers.Add(1)
		go func(run func() *httptest.ResponseRecorder) { defer workers.Done(); results <- run() }(request)
	}
	ids := map[uint64]bool{}
	for range 2 {
		select {
		case id := <-arrived:
			ids[id] = true
		case <-time.After(2 * time.Second):
			t.Fatal("two real write transactions did not reach Org-lock barrier")
		}
	}
	if len(ids) != 2 {
		t.Fatal("CAS requests reused one transaction connection")
	}
	unblock()
	statuses := map[int]int{}
	for range 2 {
		select {
		case w := <-results:
			statuses[w.Code]++
			if w.Code == 409 {
				out := routingAdminTestJSON(t, w, 409)
				if out["code"] != "policy_version_conflict" {
					t.Fatal("CAS loser had an unrelated failure")
				}
			}
		case <-time.After(6 * time.Second):
			t.Fatal("CAS transaction did not finish")
		}
	}
	cleanup()
	if statuses[200] != 1 || statuses[409] != 1 {
		t.Fatal("CAS did not have exactly one winner", statuses)
	}
}

func TestMySQLRoutingOrgAdminCASUniqueDefaultAndEventRollback(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	a := routingOrgMySQLGroup(t, f, 0, "cas-a")
	b := routingOrgMySQLGroup(t, f, 0, "cas-b")
	c := routingOrgMySQLGroup(t, f, 0, "cas-c")
	create := func() *httptest.ResponseRecorder {
		return f.request("PUT", routingOrgMySQLPath(1, a), f.tokens["su"], `{"expected_grant_version":0,"status":"enabled"}`)
	}
	routingOrgMySQLRace(t, f, [2]func() *httptest.ResponseRecorder{create, create})
	routingOrgMySQLGrantState(t, f.db, 1, a, 1, "enabled", false)
	routingOrgMySQLGrant(t, f, 1, b, 0, "enabled")
	routingOrgMySQLGrant(t, f, 1, c, 0, "enabled")
	def := "/api/admin/orgs/1/routing-default"
	routingOrgMySQLRace(t, f, [2]func() *httptest.ResponseRecorder{
		func() *httptest.ResponseRecorder {
			return f.request("PUT", def, f.tokens["su"], routingOrgMySQLDefaultBody(&a, nil, 0, 1))
		},
		func() *httptest.ResponseRecorder {
			return f.request("PUT", def, f.tokens["su2"], routingOrgMySQLDefaultBody(&b, nil, 0, 1))
		},
	})
	var current routinggroup.Grant
	if err := f.db.Where("org_id=1 AND default_slot=1").Take(&current).Error; err != nil || current.GrantVersion != 2 {
		t.Fatal("concurrent default creation lost the unique pointer")
	}
	other := a
	if current.GroupID == a {
		other = b
	}
	err := f.db.Exec("UPDATE org_group_grants SET default_slot=1 WHERE org_id=1 AND group_id=?", other).Error
	var duplicate *mysqlDriver.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		t.Fatal("real MySQL did not enforce one default slot per Org")
	}
	for _, body := range []string{routingOrgMySQLDefaultBody(&c, nil, 0, 1), routingOrgMySQLDefaultBody(&c, &current.GroupID, 1, 1), routingOrgMySQLDefaultBody(&c, &current.GroupID, 2, 2), routingOrgMySQLDefaultBody(&current.GroupID, &current.GroupID, 2, 1)} {
		routingAdminTestJSON(t, f.request("PUT", def, f.tokens["su"], body), 409)
	}
	// Fail on the TARGET event, after clearing the old default and inserting
	// its event. A first-step failure cannot satisfy this independent collision.
	target := routingOrgMySQLGrantState(t, f.db, 1, c, 1, "enabled", false)
	routingAdminSQL(t, f.db, "INSERT INTO routing_policy_events(aggregate_type,aggregate_id,aggregate_version,event_kind,org_id,group_id,actor_user_id,created_at) VALUES('grant',?,2,'fixture_collision',1,?,1,UTC_TIMESTAMP(3))", target.ID, c)
	tables := []string{"org_group_grants", "routing_policy_events", "routing_groups"}
	before := routingAdminFingerprints(t, f.db, tables...)
	body := routingOrgMySQLDefaultBody(&c, &current.GroupID, 2, 1)
	routingAdminTestJSON(t, f.request("PUT", def, f.tokens["su"], body), 503)
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, tables...)) {
		t.Fatal("second-side event failure left partial default/grant changes")
	}
	routingAdminSQL(t, f.db, "DELETE FROM routing_policy_events WHERE aggregate_type='grant' AND aggregate_id=? AND aggregate_version=2", target.ID)
	routingOrgMySQLDefault(t, f, 1, &c, &current.GroupID, 2, 1, true)
	routingOrgMySQLGrantState(t, f.db, 1, current.GroupID, 3, "enabled", false)
	routingOrgMySQLGrantState(t, f.db, 1, c, 2, "enabled", true)
	// A failed revoke must retain the current default and its grant version.
	routingAdminSQL(t, f.db, "INSERT INTO routing_policy_events(aggregate_type,aggregate_id,aggregate_version,event_kind,org_id,group_id,actor_user_id,created_at) VALUES('grant',?,3,'fixture_collision',1,?,1,UTC_TIMESTAMP(3))", target.ID, c)
	before = routingAdminFingerprints(t, f.db, tables...)
	routingAdminTestJSON(t, f.request("PUT", routingOrgMySQLPath(1, c), f.tokens["su"], `{"expected_grant_version":2,"status":"revoked"}`), 503)
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, tables...)) {
		t.Fatal("failed revoke dropped the default pointer")
	}
	routingAdminSQL(t, f.db, "DELETE FROM routing_policy_events WHERE aggregate_type='grant' AND aggregate_id=? AND aggregate_version=3", target.ID)
	// Two simultaneous switches from the same old pointer also have one winner.
	routingOrgMySQLRace(t, f, [2]func() *httptest.ResponseRecorder{
		func() *httptest.ResponseRecorder {
			return f.request("PUT", def, f.tokens["su"], routingOrgMySQLDefaultBody(&current.GroupID, &c, 2, 3))
		},
		func() *httptest.ResponseRecorder {
			return f.request("PUT", def, f.tokens["su2"], routingOrgMySQLDefaultBody(&other, &c, 2, 1))
		},
	})
	var count int64
	if err := f.db.Model(&routinggroup.Grant{}).Where("org_id=1 AND default_slot=1").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("default switch race violated unique current pointer")
	}
	// New grant creation must roll back on its own event failure as well.
	d := routingOrgMySQLGroup(t, f, 0, "cas-new-grant")
	var nextID uint64
	if err := f.db.Raw("SELECT auto_increment FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='org_group_grants'").Scan(&nextID).Error; err != nil || nextID == 0 {
		t.Fatal("cannot identify this private schema's next grant identity")
	}
	routingAdminSQL(t, f.db, "INSERT INTO routing_policy_events(aggregate_type,aggregate_id,aggregate_version,event_kind,org_id,group_id,actor_user_id,created_at) VALUES('grant',?,1,'fixture_collision',1,?,1,UTC_TIMESTAMP(3))", nextID, d)
	before = routingAdminFingerprints(t, f.db, tables...)
	routingAdminTestJSON(t, f.request("PUT", routingOrgMySQLPath(1, d), f.tokens["su"], `{"expected_grant_version":0,"status":"enabled"}`), 503)
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, tables...)) {
		t.Fatal("new grant survived its event transaction failure")
	}
	routingAdminSQL(t, f.db, "DELETE FROM routing_policy_events WHERE aggregate_type='grant' AND aggregate_id=? AND aggregate_version=1", nextID)
	routingOrgMySQLGrant(t, f, 1, d, 0, "enabled")
	revoke := func() *httptest.ResponseRecorder {
		return f.request("PUT", routingOrgMySQLPath(1, d), f.tokens["su"], `{"expected_grant_version":1,"status":"revoked"}`)
	}
	routingOrgMySQLRace(t, f, [2]func() *httptest.ResponseRecorder{revoke, revoke})
	routingOrgMySQLGrantState(t, f.db, 1, d, 2, "revoked", false)
}

func TestMySQLRoutingOrgAdminReadonlySnapshot(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	a := routingOrgMySQLGroup(t, f, 0, "snapshot-a")
	b := routingOrgMySQLGroup(t, f, 0, "snapshot-b")
	routingOrgMySQLGrant(t, f, 1, a, 0, "enabled")
	routingOrgMySQLGrant(t, f, 1, b, 0, "enabled")
	routingOrgMySQLDefault(t, f, 1, &a, nil, 0, 1, true)
	var once atomic.Bool
	var readOnly bool
	var writerErr error
	name := "routing_org_rr_independent_writer"
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table != "organizations" || q.Error != nil {
			return
		}
		if _, tx := q.Statement.ConnPool.(gorm.TxCommitter); !tx || !once.CompareAndSwap(false, true) {
			return
		}
		err := q.Session(&gorm.Session{NewDB: true, Context: q.Statement.Context}).Exec("UPDATE org_group_grants SET grant_version=grant_version WHERE org_id=1").Error
		var denied *mysqlDriver.MySQLError
		readOnly = errors.As(err, &denied) && denied.Number == 1792
		// This commits on another actual connection after the first consistent
		// read. Subsequent grants/default/group reads must retain the old image.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		writerErr = f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, statement := range []struct {
				sql  string
				args []any
			}{
				{"UPDATE organizations SET status=0 WHERE id=1", nil},
				{"UPDATE routing_groups SET status='disabled',policy_version=3 WHERE id IN (?,?)", []any{a, b}},
				{"UPDATE org_group_grants SET default_slot=NULL,grant_version=3 WHERE org_id=1 AND group_id=?", []any{a}},
				{"UPDATE org_group_grants SET default_slot=1,grant_version=2 WHERE org_id=1 AND group_id=?", []any{b}},
			} {
				if err := tx.Exec(statement.sql, statement.args...).Error; err != nil {
					return err
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	out := routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/1/routing-groups", f.tokens["su"], ""), 200)
	_ = f.db.Callback().Query().Remove(name)
	if !once.Load() || !readOnly || writerErr != nil {
		t.Fatal("real RR/read-only writer positive controls failed")
	}
	if out["organization"].(map[string]any)["active"] != true {
		t.Fatal("organization snapshot changed during the request")
	}
	current := out["current_default"].(map[string]any)
	if current["group_id"] != float64(a) || current["grant_version"] != float64(2) || current["group_policy_version"] != float64(2) || current["default_valid"] != true {
		t.Fatal("default came from a later connection snapshot")
	}
	items := out["items"].([]any)
	if len(items) != 2 {
		t.Fatal("snapshot lost an authorization")
	}
	for i, raw := range items {
		row := raw.(map[string]any)
		grant := row["grant"].(map[string]any)
		wantGroup, wantVersion := a, uint64(2)
		if i == 1 {
			wantGroup, wantVersion = b, 1
		}
		if grant["group_id"] != float64(wantGroup) || grant["grant_version"] != float64(wantVersion) || row["configuration_valid"] != true || row["group"].(map[string]any)["status"] != "enabled" {
			t.Fatal("list combined authorization and group data from different snapshots")
		}
	}
	next := routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/1/routing-default", f.tokens["su"], ""), 200)
	current = next["current_default"].(map[string]any)
	if next["organization"].(map[string]any)["active"] != false || current["group_id"] != float64(b) || current["grant_version"] != float64(2) || current["default_valid"] != false {
		t.Fatal("next request did not observe the independent writer commit")
	}
	reasons := current["invalid_reasons"].([]any)
	got := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		got = append(got, reason.(string))
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"group_inactive", "org_inactive"}) {
		t.Fatal("inactive default diagnostics are incomplete", got)
	}
}

func TestMySQLRoutingOrgAdminBoundedPagingAndExplain(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	groups, grants := make([]map[string]any, 0, 1001), make([]map[string]any, 0, 2002)
	for i := 0; i <= 1000; i++ {
		group := i + 100
		groups = append(groups, map[string]any{"id": group, "slug": fmt.Sprintf("page-%04d", i), "name": "Page group", "owner_org_id": 0, "status": "enabled", "policy_version": 7, "created_at": now, "updated_at": now})
		status := "enabled"
		if i%2 == 1 {
			status = "revoked"
		}
		var slot any
		if i == 1000 {
			slot = 1
		}
		// Grant IDs run in the opposite direction: the oracle catches an
		// accidental PK cursor even if all 1001 rows happen to be returned.
		grants = append(grants, map[string]any{"id": 11000 - i, "org_id": 1, "group_id": group, "status": status, "grant_version": 4, "default_slot": slot, "created_at": now, "updated_at": now})
		grants = append(grants, map[string]any{"id": 21000 - i, "org_id": 2, "group_id": group, "status": "enabled", "grant_version": 9, "created_at": now, "updated_at": now})
	}
	for _, batch := range []struct {
		table string
		rows  []map[string]any
	}{{"routing_groups", groups}, {"org_group_grants", grants}} {
		if err := f.db.Table(batch.table).CreateInBatches(batch.rows, 100).Error; err != nil {
			t.Fatal("cannot seed independent paging population")
		}
	}
	type captured struct {
		table, query string
		args         []any
	}
	var queries []captured
	var businessQueries int
	capture := true
	name := "routing_org_actual_query_plan"
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if _, tx := q.Statement.ConnPool.(gorm.TxCommitter); !tx {
			return
		}
		if q.Statement.Table != "organizations" && q.Statement.Table != "routing_groups" && q.Statement.Table != "org_group_grants" {
			return
		}
		businessQueries++
		if strings.Contains(strings.ToUpper(q.Statement.SQL.String()), "COUNT(") {
			t.Error("bounded grant list issued COUNT")
		}
		if !strings.Contains(q.Statement.SQL.String(), "MAX_EXECUTION_TIME(") {
			t.Error("bounded grant SELECT lacks its server time bound")
		}
		if capture {
			queries = append(queries, captured{q.Statement.Table, q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...)})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	assertDefault := func(out map[string]any) {
		cur := out["current_default"].(map[string]any)
		if cur["group_id"] != float64(1100) || cur["grant_version"] != float64(4) || cur["default_valid"] != true {
			t.Fatal("default outside page/filter disappeared")
		}
	}
	filtered := routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/1/routing-groups?status=revoked&limit=20", f.tokens["su"], ""), 200)
	assertDefault(filtered)
	for i, raw := range filtered["items"].([]any) {
		row := raw.(map[string]any)
		if row["grant"].(map[string]any)["group_id"] != float64(101+i*2) || row["configuration_valid"] != false {
			t.Fatal("status-filter independent oracle failed")
		}
	}
	if businessQueries > 5 {
		t.Fatal("grant list exceeded its fixed SELECT bound")
	}
	cursor, seen := uint64(0), 0
	for {
		businessQueries = 0
		out := routingAdminTestJSON(t, f.request("GET", fmt.Sprintf("/api/admin/orgs/1/routing-groups?limit=100&cursor=%d", cursor), f.tokens["su"], ""), 200)
		assertDefault(out)
		items := out["items"].([]any)
		if len(items) > 100 || businessQueries > 5 {
			t.Fatal("list read was not bounded")
		}
		for _, raw := range items {
			row := raw.(map[string]any)
			grant := row["grant"].(map[string]any)
			want := float64(100 + seen)
			if grant["org_id"] != float64(1) || grant["group_id"] != want || grant["grant_version"] != float64(4) {
				t.Fatal("seek repeated, omitted or crossed organization rows")
			}
			seen++
		}
		capture = false // Preserve one filtered and one full page's real plans.
		if out["has_more"] == false {
			if out["next_cursor"] != float64(0) {
				t.Fatal("finished seek retained a cursor")
			}
			break
		}
		next := uint64(out["next_cursor"].(float64))
		if next <= cursor || next != uint64(99+seen) {
			t.Fatal("cursor is not the final returned group ID")
		}
		cursor = next
		if seen > 1001 {
			t.Fatal("seek failed to finish")
		}
	}
	if seen != 1001 {
		t.Fatal("bounded seek did not return all 1001 independent fixture rows")
	}
	_ = f.db.Callback().Query().Remove(name)
	covered := map[string]bool{}
	for _, q := range queries {
		var plan []struct {
			Table, Type string
			Key         sql.NullString
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := f.db.WithContext(ctx).Raw("EXPLAIN "+q.query, q.args...).Scan(&plan).Error
		cancel()
		if err != nil || len(plan) == 0 {
			t.Fatal("cannot EXPLAIN actual bounded grant SELECT")
		}
		for _, row := range plan {
			// A broad status page can legitimately choose a scan. Do not force
			// an index; record the real optimizer decision alongside bounds.
			t.Logf("actual organization-policy SELECT table=%s type=%s key=%s", row.Table, row.Type, row.Key.String)
			if q.table == "organizations" && (!row.Key.Valid || row.Type == "ALL") {
				t.Fatal("Org point lookup lacks its existing primary index")
			}
		}
		covered[q.table] = true
	}
	if !covered["organizations"] || !covered["routing_groups"] || !covered["org_group_grants"] {
		t.Fatal("query plan evidence omitted a bounded context")
	}
}

type routingOrgReadBarrierPool struct {
	*sql.DB
	entered chan struct{}
	release <-chan struct{}
}

func (p routingOrgReadBarrierPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	if options == nil || !options.ReadOnly {
		return tx, nil
	}
	p.entered <- struct{}{}
	select {
	case <-p.release:
		return tx, nil
	case <-ctx.Done():
		_ = tx.Rollback()
		return nil, ctx.Err()
	}
}

func TestMySQLRoutingOrgAdminSharedGateCancellationAndFailure(t *testing.T) {
	f := newRoutingAdminMySQLFixture(t)
	a := routingOrgMySQLGroup(t, f, 0, "gate")
	routingOrgMySQLGrant(t, f, 1, a, 0, "enabled")
	routingAdminSQL(t, f.db, "INSERT INTO users(id,email,email_canonical,role) VALUES(5,'third@example.test','third@example.test','superadmin')")
	token, _, err := identity.IssueSessionToken(testSecret, 5, 1, time.Hour, time.Now())
	if err != nil {
		t.Fatal("cannot issue third synthetic admin session")
	}
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal("cannot access fixture pool")
	}
	entered, release := make(chan struct{}, 4), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	f.s.db = f.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	f.s.db.Statement.ConnPool = routingOrgReadBarrierPool{DB: pool, entered: entered, release: release}
	var workers sync.WaitGroup
	var once sync.Once
	done := make(chan *httptest.ResponseRecorder, 4)
	cleanup := func() { cancel(); once.Do(func() { close(release) }); workers.Wait(); f.s.db = f.db }
	t.Cleanup(cleanup)
	requests := []struct{ path, token string }{
		{"/api/admin/routing-groups", f.tokens["su"]}, {"/api/admin/orgs/1/routing-groups", f.tokens["su"]},
		{"/api/admin/orgs/1/routing-default", f.tokens["su2"]}, {"/api/admin/orgs/2/routing-groups", f.tokens["su2"]},
	}
	for phase := 0; phase < 2; phase++ {
		for _, request := range requests[phase*2 : phase*2+2] {
			workers.Add(1)
			go func(path, token string) {
				defer workers.Done()
				done <- routingAdminTestRequest(f.engine, ctx, "GET", path, token, "", "")
			}(request.path, request.token)
		}
		for range 2 {
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("real read transactions did not occupy the shared B1/B2 gate")
			}
		}
		if phase == 0 {
			// Two free global slots remain: this 429 proves the per-user limit
			// is shared across B1/B2 and across target organizations.
			routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/3/routing-groups", f.tokens["su"], ""), 429)
		}
	}
	routingAdminTestJSON(t, f.request("GET", "/api/admin/routing-groups", token, ""), 429)
	cancel()
	for range 4 {
		select {
		case w := <-done:
			routingAdminTestJSON(t, w, 503)
		case <-time.After(2 * time.Second):
			t.Fatal("cancellation did not release an active transaction")
		}
	}
	cleanup()
	f.s.routingGroupReads.mu.Lock()
	clean := f.s.routingGroupReads.active == 0 && len(f.s.routingGroupReads.actors) == 0
	f.s.routingGroupReads.mu.Unlock()
	if !clean {
		t.Fatal("shared read gate retained canceled users")
	}
	routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/1/routing-groups", f.tokens["su"], ""), 200)
	// The third business SELECT is the current default lookup. Break its
	// actual SQL after Org+grant reads: no partial list or fake empty default.
	var reads atomic.Int32
	name := "routing_org_failed_actual_select"
	if err := f.db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if _, tx := q.Statement.ConnPool.(gorm.TxCommitter); !tx {
			return
		}
		if q.Statement.Table != "organizations" && q.Statement.Table != "org_group_grants" && q.Statement.Table != "routing_groups" {
			return
		}
		if reads.Add(1) == 3 {
			q.Statement.SQL.Reset()
			q.Statement.SQL.WriteString("SELECT `private-query-diagnostic` FROM org_group_grants")
			q.Statement.Vars = nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/api/admin/orgs/1/routing-groups", f.tokens["su"], "")
	_ = f.db.Callback().Query().Remove(name)
	out := routingAdminTestJSON(t, w, 503)
	if reads.Load() < 3 || out["items"] != nil || out["current_default"] != nil || strings.Contains(w.Body.String(), "private-query-diagnostic") || strings.Contains(w.Body.String(), "SELECT") {
		t.Fatal("failed multi-query read published partial data or private SQL")
	}
	routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/1/routing-default", f.tokens["su"], ""), 200)
}
