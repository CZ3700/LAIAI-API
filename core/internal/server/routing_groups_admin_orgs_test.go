package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

func routingOrgEnabledGroup(t *testing.T, f *routingAdminHTTPFixture, owner uint64, slug string) uint64 {
	t.Helper()
	id := f.create(t, owner, slug)
	routingAdminTestJSON(t, f.request("PATCH", fmt.Sprintf("/api/admin/routing-groups/%d", id), f.tokens["su"], `{"expected_version":1,"status":"enabled"}`), 200)
	return id
}

func routingOrgGrantPath(org, group uint64) string {
	return fmt.Sprintf("/api/admin/orgs/%d/routing-groups/%d", org, group)
}

func routingOrgGrant(t *testing.T, f *routingAdminHTTPFixture, org, group, version uint64, status string) map[string]any {
	t.Helper()
	out := routingAdminTestJSON(t, f.request("PUT", routingOrgGrantPath(org, group), f.tokens["su"], fmt.Sprintf(`{"expected_grant_version":%d,"status":%q}`, version, status)), 200)
	return out["grant"].(map[string]any)
}

func TestRoutingOrgAdminSixRoutesRequireNamedActiveSuperadmin(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	routes := []struct{ method, path, body string }{
		{"GET", "/api/admin/orgs/2/routing-groups", ""},
		{"PUT", "/api/admin/orgs/2/routing-groups/1", `{"expected_grant_version":0,"status":"enabled"}`},
		{"GET", "/api/admin/orgs/2/routing-default", ""},
		{"PUT", "/api/admin/orgs/2/routing-default", `{"target_group_id":null,"expected_current_default":null}`},
		{"POST", "/api/admin/orgs/2/routing-groups/1/impact-preview", `{"expected_grant_version":0,"status":"enabled"}`},
		{"POST", "/api/admin/orgs/2/routing-default/impact-preview", `{"target_group_id":null,"expected_current_default":null}`},
	}
	for _, actor := range []struct {
		name, token, root string
		status            int
	}{{"missing", "", "", 401}, {"key", "sk-test-not-session", "", 401}, {"user", f.tokens["user"], "", 403}, {"admin", f.tokens["admin"], "", 403}, {"inactive", f.tokens["banned"], "", 401}, {"root", "", testAdminToken, 403}, {"mixed", f.tokens["su"], testAdminToken, 403}} {
		t.Run(actor.name, func(t *testing.T) {
			for _, route := range routes {
				routingAdminTestJSON(t, routingAdminTestRequest(f.engine, context.Background(), route.method, route.path, actor.token, actor.root, route.body), actor.status)
			}
		})
	}
	if routingAdminTableCount(t, f.db, "org_group_grants") != 0 || routingAdminTableCount(t, f.db, "routing_policy_events") != 0 {
		t.Fatal("denied organizational routes changed routing facts")
	}
	w := f.request("GET", routes[0].path, f.tokens["su"], "")
	out := routingAdminTestJSON(t, w, 200)
	if out["organization"].(map[string]any)["id"] != float64(2) || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Request-Id") == "" || w.Header().Get("X-Request-Id") == "client-spoofed-request" {
		t.Fatal("target org or private named-admin response identity is incorrect")
	}
}

func TestRoutingOrgAdminGrantAndDefaultLifecycle(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	a := routingOrgEnabledGroup(t, f, 0, "platform")
	b := routingOrgEnabledGroup(t, f, 2, "private-two")
	foreign := routingOrgEnabledGroup(t, f, 3, "private-three")
	ga := routingOrgGrant(t, f, 2, a, 0, "enabled")
	gb := routingOrgGrant(t, f, 2, b, 0, "enabled")
	if ga["org_id"] != float64(2) || ga["grant_version"] != float64(1) || ga["is_default"] != false || gb["grant_version"] != float64(1) {
		t.Fatal("new grants did not return transaction results for the target org")
	}
	before := routingAdminTableCount(t, f.db, "routing_policy_events")
	out := routingAdminTestJSON(t, f.request("PUT", routingOrgGrantPath(2, a), f.tokens["su"], `{"expected_grant_version":1,"status":"enabled"}`), 200)
	if out["changed"] != false || routingAdminTableCount(t, f.db, "routing_policy_events") != before {
		t.Fatal("accurate no-op emitted a grant event")
	}
	for _, version := range []int{0, 2} {
		routingAdminTestJSON(t, f.request("PUT", routingOrgGrantPath(2, a), f.tokens["su"], fmt.Sprintf(`{"expected_grant_version":%d,"status":"revoked"}`, version)), 409)
	}
	routingAdminTestJSON(t, f.request("PUT", routingOrgGrantPath(2, foreign), f.tokens["su"], `{"expected_grant_version":0,"status":"enabled"}`), 404)
	defaultPath := "/api/admin/orgs/2/routing-default"
	setA := fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":null,"expected_target_grant_version":1}`, a)
	out = routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], setA), 200)
	if out["changed"] != true || out["applied_target_group_id"] != float64(a) || len(out) != 4 {
		t.Fatal("default write returned more than its command receipt")
	}
	routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], setA), 409)
	get := routingAdminTestJSON(t, f.request("GET", defaultPath, f.tokens["su"], ""), 200)
	current := get["current_default"].(map[string]any)
	if current["group_id"] != float64(a) || current["grant_version"] != float64(2) || current["default_valid"] != true || len(current["invalid_reasons"].([]any)) != 0 {
		t.Fatal("first default snapshot invalid")
	}
	for _, versions := range [][2]int{{1, 2}, {2, 1}} {
		body := fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":{"group_id":%d,"grant_version":%d},"expected_target_grant_version":%d}`, a, a, versions[0], versions[1])
		routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], body), 409)
	}
	before = routingAdminTableCount(t, f.db, "routing_policy_events")
	same := fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":{"group_id":%d,"grant_version":2},"expected_target_grant_version":2}`, a, a)
	out = routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], same), 200)
	if out["changed"] != false || routingAdminTableCount(t, f.db, "routing_policy_events") != before {
		t.Fatal("same-default command was not an exact no-op")
	}
	switchB := fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":{"group_id":%d,"grant_version":2},"expected_target_grant_version":1}`, b, a)
	routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], switchB), 200)
	if routingAdminTableCount(t, f.db, "routing_policy_events") != before+2 {
		t.Fatal("default switch did not atomically emit its two side events")
	}
	before = routingAdminTableCount(t, f.db, "routing_policy_events")
	grant := routingOrgGrant(t, f, 2, b, 2, "revoked")
	if grant["is_default"] != false || grant["grant_version"] != float64(3) || routingAdminTableCount(t, f.db, "routing_policy_events") != before+1 {
		t.Fatal("revocation failed to clear default in its single grant event")
	}
	get = routingAdminTestJSON(t, f.request("GET", defaultPath, f.tokens["su"], ""), 200)
	if get["current_default"] != nil {
		t.Fatal("revoked default remained active")
	}
	grant = routingOrgGrant(t, f, 2, b, 3, "enabled")
	if grant["is_default"] != false || grant["grant_version"] != float64(4) {
		t.Fatal("reenabling incorrectly restored a previous default")
	}
	for _, body := range []string{`{"target_group_id":null,"expected_current_default":null}`, `{"target_group_id": null, "expected_current_default": null}`} {
		out = routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], body), 200)
		if out["changed"] != false || out["applied_target_group_id"] != nil {
			t.Fatal("explicitly empty default was not a no-op receipt")
		}
	}
	routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":null,"expected_target_grant_version":4}`, b)), 200)
	out = routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], fmt.Sprintf(`{"target_group_id":null,"expected_current_default":{"group_id":%d,"grant_version":5}}`, b)), 200)
	if out["changed"] != true || out["applied_target_group_id"] != nil {
		t.Fatal("explicit clear did not return its committed null target")
	}
	var events []routinggroup.Event
	if err := f.db.Where("aggregate_type = ?", "grant").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.ActorUserID != 1 || event.RequestID == "" || event.RequestID == "client-spoofed-request" || event.OrgID == nil || *event.OrgID != 2 {
			t.Fatal("grant audit accepted client actor/request/org identity")
		}
	}
}

func TestRoutingOrgAdminDefaultStrictPresenceNullAndExactIntegers(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	invalid := []string{
		`null`, `[]`, `{}`, `{"target_group_id":null}`, `{"expected_current_default":null}`,
		`{"target_group_id":null,"expected_current_default":null,"expected_target_grant_version":0}`,
		`{"target_group_id":null,"expected_current_default":null,"expected_target_grant_version":null}`,
		`{"target_group_id":null,"expected_current_default":null,"expected_target_grant_version":1}`,
		`{"target_group_id":1,"expected_current_default":null}`,
		`{"target_group_id":1,"expected_current_default":null,"expected_target_grant_version":0}`,
		`{"target_group_id":1,"expected_current_default":null,"expected_target_grant_version":null}`,
		`{"target_group_id":1,"expected_current_default":{},"expected_target_grant_version":1}`,
		`{"target_group_id":null,"expected_current_default":{"group_id":1}}`,
		`{"target_group_id":null,"expected_current_default":{"group_id":1,"grant_version":null}}`,
		`{"target_group_id":null,"expected_current_default":{"group_id":1,"grant_version":0}}`,
		`{"target_group_id":null,"expected_current_default":{"group_id":1,"grant_version":1,"grant_version":2}}`,
		`{"target_group_id":null,"expected_current_default":{"group_id":1,"grant_version":1,"\u0067rant_version":2}}`,
		`{"target_group_id":null,"expected_current_default":{"group_id":1,"grant_version":1,"status":"enabled"}}`,
		`{"target_group_id":null,"expected_current_default":{"Group_id":1,"grant_version":1}}`,
		`{"target_group_id":null,"expected_current_default":{"target_group_id":null}}`,
		`{"target_group_id":[null],"expected_current_default":null}`,
		`{"target_group_id":null,"target_group_id":1,"expected_current_default":null}`,
		`{"Target_group_id":null,"expected_current_default":null}`,
		`{"target_group_id":null,"expected_current_default":null,"org_id":1}`,
		`{"target_group_id":null,"expected_current_default":null,"actor_user_id":1}`,
		`{"target_group_id":null,"expected_current_default":null} {}`,
		`{"target_group_id":null,"expected_current_default":null,"extra":"` + strings.Repeat("x", routingAdminBodyLimit) + `"}`,
		"{\"target_group_id\":null,\"expected_current_default\":null,\"bad\":\"\xff\"}",
	}
	for _, number := range []string{"0", "-1", "1.0", "1e0", `"1"`, "18446744073709551616"} {
		invalid = append(invalid, fmt.Sprintf(`{"target_group_id":%s,"expected_current_default":null,"expected_target_grant_version":1}`, number))
		invalid = append(invalid, fmt.Sprintf(`{"target_group_id":1,"expected_current_default":null,"expected_target_grant_version":%s}`, number))
		invalid = append(invalid, fmt.Sprintf(`{"target_group_id":null,"expected_current_default":{"group_id":1,"grant_version":%s}}`, number))
	}
	for i, body := range invalid {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			for _, route := range []struct{ method, suffix string }{{"PUT", ""}, {"POST", "/impact-preview"}} {
				routingAdminTestJSON(t, f.request(route.method, "/api/admin/orgs/2/routing-default"+route.suffix, f.tokens["su"], body), 400)
			}
		})
	}
	for _, body := range []string{`{}`, `{"expected_grant_version":null,"status":"enabled"}`, `{"expected_grant_version":0,"status":null}`, `{"expected_grant_version":0,"status":"draft"}`, `{"expected_grant_version":0,"status":"enabled","is_default":true}`, `{"expected_grant_version":0,"status":"enabled","expected_grant_version":1}`, `{"expected_grant_version":1.0,"status":"enabled"}`, `{"expected_grant_version":1e0,"status":"enabled"}`} {
		for _, route := range []struct{ method, suffix string }{{"PUT", ""}, {"POST", "/impact-preview"}} {
			routingAdminTestJSON(t, f.request(route.method, "/api/admin/orgs/2/routing-groups/1"+route.suffix, f.tokens["su"], body), 400)
		}
	}
	for _, query := range []string{"status=", "status=disabled", "status=enabled&status=revoked", "cursor=", "cursor=-1", "limit=0", "limit=101", "limit=1&limit=2", "org_id=1", "owner_org_id=1", "status=%ZZ"} {
		routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/2/routing-groups?"+query, f.tokens["su"], ""), 400)
	}
	for _, id := range []string{"0", "-1", "1.0", "18446744073709551616"} {
		routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/"+id+"/routing-default", f.tokens["su"], ""), 400)
	}
	routingAdminTestJSON(t, f.request("PUT", "/api/admin/orgs/2/routing-default?force=true", f.tokens["su"], `{"target_group_id":null,"expected_current_default":null}`), 400)
	if routingAdminTableCount(t, f.db, "org_group_grants") != 0 || routingAdminTableCount(t, f.db, "routing_policy_events") != 0 {
		t.Fatal("malformed command changed routing facts")
	}
	validEmpty := `{"target_group_id":null,"expected_current_default":null}`
	boundary := validEmpty + strings.Repeat(" ", routingAdminBodyLimit-len(validEmpty))
	routingAdminTestJSON(t, f.request("PUT", "/api/admin/orgs/2/routing-default", f.tokens["su"], boundary), 200)
	routingAdminTestJSON(t, f.request("PUT", "/api/admin/orgs/2/routing-default", f.tokens["su"], boundary+" "), 400)
	// Parse actual JSON uint64 without a float64 roundtrip, independently of
	// SQLite's signed integer storage limit.
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Params = gin.Params{{Key: "id", Value: "2"}}
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"target_group_id":9007199254740993,"expected_current_default":{"group_id":18446744073709551615,"grant_version":9007199254740995},"expected_target_grant_version":9007199254740997}`))
	input, err := routingAdminOrgDefaultInput(c)
	if err != nil || *input.GroupID != 9007199254740993 || *input.ExpectedGroupID != 18446744073709551615 || input.ExpectedCurrentVersion != 9007199254740995 || input.ExpectedTargetVersion != 9007199254740997 {
		t.Fatal("default parser rounded integer identity")
	}
}

func TestRoutingOrgAdminNullExtensionKeepsDepthAndB1Rejection(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		nullable   bool
		valid      bool
	}{
		{"b1-null-still-rejected", `{"target_group_id":null}`, false, false},
		{"default-top-null", `{"target_group_id":null}`, true, true},
		{"same-key-nested-null", `{"x":{"target_group_id":null}}`, true, false},
		{"array-null", `{"target_group_id":[null]}`, true, false},
		{"root-null", `null`, true, false},
		{"depth-eight", `{"x":` + strings.Repeat("[", 7) + `1` + strings.Repeat("]", 7) + `}`, true, true},
		{"depth-nine", `{"x":` + strings.Repeat("[", 8) + `1` + strings.Repeat("]", 8) + `}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(tc.body))
			var out map[string]json.RawMessage
			var err error
			if tc.nullable {
				err = routingAdminJSONNullable(c, &out, map[string]bool{"/target_group_id": true, "/expected_current_default": true}, "target_group_id", "expected_current_default", "x")
			} else {
				err = routingAdminJSON(c, &out, "target_group_id", "expected_current_default", "x")
			}
			if (err == nil) != tc.valid {
				t.Fatal("nullable extension changed its exact path or depth boundary")
			}
		})
	}
	// Keep the original recursive entry point's all-null rejection intact.
	d := json.NewDecoder(bytes.NewReader([]byte(`{"target_group_id":null}`)))
	if routingAdminJSONValue(d, 0, true) == nil {
		t.Fatal("B1 recursive parser silently accepted null")
	}
}

func TestRoutingOrgAdminDefaultCommitDoesNotDependOnLaterRead(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	group := routingOrgEnabledGroup(t, f, 0, "commit-receipt")
	routingOrgGrant(t, f, 2, group, 0, "enabled")
	pool, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	var committed, fault atomic.Bool
	f.s.db = f.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	f.s.db.Statement.ConnPool = routingAdminCommitPool{DB: pool, after: func() { committed.Store(true) }}
	name := "routing-org-default-post-commit-failure"
	if err := f.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if committed.Load() && (tx.Statement.Table == "org_group_grants" || tx.Statement.Table == "routing_groups") {
			fault.Store(true)
			tx.AddError(errors.New("private post-commit read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	w := f.request("PUT", "/api/admin/orgs/2/routing-default", f.tokens["su"], fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":null,"expected_target_grant_version":1}`, group))
	out := routingAdminTestJSON(t, w, 200)
	if !committed.Load() || fault.Load() || out["changed"] != true || out["applied_target_group_id"] != float64(group) {
		t.Fatal("committed write performed an unnecessary post-commit lookup")
	}
	routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/2/routing-default", f.tokens["su"], ""), 503)
	if !fault.Load() {
		t.Fatal("post-commit read failure positive control did not execute")
	}
}

func routingOrgFacts(t *testing.T, f *routingAdminHTTPFixture) string {
	t.Helper()
	var grants []routinggroup.Grant
	var events []routinggroup.Event
	if err := f.db.Order("id").Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Order("id").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]any{grants, events})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRoutingOrgAdminPreviewsAreBoundedCommandsWithoutWrites(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	group := routingOrgEnabledGroup(t, f, 0, "preview-grant")
	grantPath := routingOrgGrantPath(2, group)
	defaultPath := "/api/admin/orgs/2/routing-default"
	before := routingOrgFacts(t, f)
	out := routingAdminTestJSON(t, f.request("POST", grantPath+"/impact-preview", f.tokens["su"], `{"expected_grant_version":0,"status":"enabled"}`), 200)
	if out["preview_kind"] != "structure_only" || out["effective_model_impact_available"] != false || out["observed_current_default"] != nil || out["changed"] != true {
		t.Fatal("grant preview implied runtime access or incorrect current default")
	}
	observed := out["observed_grants"].([]any)
	effects := out["affected_grants"].([]any)
	if len(observed) != 1 || observed[0].(map[string]any)["exists"] != false || observed[0].(map[string]any)["grant"] != nil || len(effects) != 1 {
		t.Fatal("new grant preview invented an existing grant")
	}
	change := effects[0].(map[string]any)
	if _, hasID := change["grant_id"]; hasID || change["from_version"] != float64(0) || change["to_version"] != float64(1) || fmt.Sprint(change["actions"]) != "[create]" || before != routingOrgFacts(t, f) {
		t.Fatal("new grant preview assigned a future ID or wrote policy facts")
	}
	routingOrgGrant(t, f, 2, group, 0, "enabled")
	before = routingOrgFacts(t, f)
	out = routingAdminTestJSON(t, f.request("POST", grantPath+"/impact-preview", f.tokens["su"], `{"expected_grant_version":1,"status":"enabled"}`), 200)
	if out["changed"] != false || len(out["affected_grants"].([]any)) != 0 {
		t.Fatal("unchanged grant preview invented effects")
	}
	body := fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":null,"expected_target_grant_version":1}`, group)
	out = routingAdminTestJSON(t, f.request("POST", defaultPath+"/impact-preview", f.tokens["su"], body), 200)
	change = out["affected_grants"].([]any)[0].(map[string]any)
	if change["from_version"] != float64(1) || change["to_version"] != float64(2) || change["is_default"] != true || fmt.Sprint(change["actions"]) != "[default_set]" || before != routingOrgFacts(t, f) {
		t.Fatal("default preview mutated state or failed to show its single transition")
	}
	routingAdminTestJSON(t, f.request("PUT", defaultPath, f.tokens["su"], body), 200)
	before = routingOrgFacts(t, f)
	out = routingAdminTestJSON(t, f.request("POST", grantPath+"/impact-preview", f.tokens["su"], `{"expected_grant_version":2,"status":"revoked"}`), 200)
	change = out["affected_grants"].([]any)[0].(map[string]any)
	if len(out["affected_grants"].([]any)) != 1 || change["from_version"] != float64(2) || change["to_version"] != float64(3) || change["is_default"] != false || fmt.Sprint(change["actions"]) != "[revoke default_clear]" {
		t.Fatal("default revocation preview did not describe one atomic grant change")
	}
	clear := fmt.Sprintf(`{"target_group_id":null,"expected_current_default":{"group_id":%d,"grant_version":2}}`, group)
	out = routingAdminTestJSON(t, f.request("POST", defaultPath+"/impact-preview", f.tokens["su"], clear), 200)
	command := out["change"].(map[string]any)
	if command["target_group_id"] != nil || len(command) != 2 {
		t.Fatal("clear preview fabricated an unused target version")
	}
	routingAdminTestJSON(t, f.request("POST", defaultPath+"/impact-preview", f.tokens["su"], body), 409)
	if before != routingOrgFacts(t, f) {
		t.Fatal("preview wrote grant or event state")
	}
}

func TestRoutingOrgAdminGroupSeekAndIndependentDefaultSnapshot(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	a := routingOrgEnabledGroup(t, f, 0, "seek-a")
	b := routingOrgEnabledGroup(t, f, 0, "seek-b")
	c := routingOrgEnabledGroup(t, f, 2, "seek-c")
	// Create grants in a deliberately different order from their group IDs.
	routingOrgGrant(t, f, 2, c, 0, "enabled")
	routingOrgGrant(t, f, 2, a, 0, "enabled")
	routingOrgGrant(t, f, 2, b, 0, "enabled")
	routingOrgGrant(t, f, 2, b, 1, "revoked")
	routingAdminTestJSON(t, f.request("PUT", "/api/admin/orgs/2/routing-default", f.tokens["su"], fmt.Sprintf(`{"target_group_id":%d,"expected_current_default":null,"expected_target_grant_version":1}`, c)), 200)
	before := routingOrgFacts(t, f)
	for i, id := range []uint64{a, b, c} {
		cursor := uint64(0)
		if i > 0 {
			cursor = []uint64{a, b, c}[i-1]
		}
		out := routingAdminTestJSON(t, f.request("GET", fmt.Sprintf("/api/admin/orgs/2/routing-groups?limit=1&cursor=%d", cursor), f.tokens["su"], ""), 200)
		items := out["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["grant"].(map[string]any)["group_id"] != float64(id) || out["current_default"].(map[string]any)["group_id"] != float64(c) {
			t.Fatal("seek cursor used grant ID or default was incorrectly scoped to current page")
		}
		if out["has_more"] != (i < 2) || i < 2 && out["next_cursor"] != float64(id) || i == 2 && out["next_cursor"] != float64(0) {
			t.Fatal("group seek cursor contract changed")
		}
	}
	out := routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/2/routing-groups?status=revoked", f.tokens["su"], ""), 200)
	if len(out["items"].([]any)) != 1 || out["current_default"].(map[string]any)["group_id"] != float64(c) || before != routingOrgFacts(t, f) {
		t.Fatal("default snapshot was filtered by revoked-grant list or GET wrote facts")
	}
	if err := f.db.Exec("UPDATE organizations SET status=0 WHERE id=2").Error; err != nil {
		t.Fatal(err)
	}
	out = routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/2/routing-default", f.tokens["su"], ""), 200)
	current := out["current_default"].(map[string]any)
	if out["organization"].(map[string]any)["active"] != false || current["default_valid"] != false || fmt.Sprint(current["invalid_reasons"]) != "[org_inactive]" {
		t.Fatal("inactive organization could not inspect retained default configuration")
	}
	routingAdminTestJSON(t, f.request("PUT", routingOrgGrantPath(2, b), f.tokens["su"], `{"expected_grant_version":2,"status":"enabled"}`), 403)
	routingOrgGrant(t, f, 2, c, 2, "revoked")
	routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/99/routing-default", f.tokens["su"], ""), 404)
	routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/99/routing-groups", f.tokens["su"], ""), 404)
}

func TestRoutingOrgAdminSharesB1ReadCapacityAndReleasesErrors(t *testing.T) {
	f := newRoutingAdminHTTPFixture(t)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	var workers sync.WaitGroup
	var block atomic.Bool
	block.Store(true)
	name := "routing-org-shared-read-barrier"
	if err := f.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if block.Load() && (tx.Statement.Table == "routing_groups" || tx.Statement.Table == "organizations") {
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
	t.Cleanup(func() { once.Do(func() { close(release) }); workers.Wait(); _ = f.db.Callback().Query().Remove(name) })
	responses := make(chan *httptest.ResponseRecorder, 4)
	for i, request := range []struct{ actor, path string }{{"su", "/api/admin/routing-groups"}, {"su", "/api/admin/orgs/2/routing-groups"}, {"su2", "/api/admin/orgs/2/routing-default"}, {"su2", "/api/admin/routing-groups"}} {
		workers.Add(1)
		go func(actor, path string) {
			defer workers.Done()
			responses <- f.request("GET", path, f.tokens[actor], "")
		}(request.actor, request.path)
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("actual business read did not occupy a shared slot")
		}
		if i == 1 {
			w := f.request("GET", "/api/admin/orgs/3/routing-default", f.tokens["su"], "")
			routingAdminTestJSON(t, w, 429)
			if w.Header().Get("Retry-After") != "1" {
				t.Fatal("cross-org same actor did not receive shared gate retry hint")
			}
		}
	}
	routingAdminTestJSON(t, f.request("POST", "/api/admin/orgs/3/routing-default/impact-preview", f.tokens["su3"], `{"target_group_id":null,"expected_current_default":null}`), 429)
	block.Store(false)
	once.Do(func() { close(release) })
	for i := 0; i < 4; i++ {
		routingAdminTestJSON(t, <-responses, 200)
	}
	if err := f.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	faultName := "routing-org-partial-read-fault"
	if err := f.db.Callback().Query().Before("gorm:query").Register(faultName, func(tx *gorm.DB) {
		if tx.Statement.Table == "org_group_grants" {
			tx.AddError(errors.New("private SQL token-should-never-escape"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/api/admin/orgs/2/routing-groups", f.tokens["su"], "")
	out := routingAdminTestJSON(t, w, 503)
	if out["items"] != nil || out["current_default"] != nil || strings.Contains(w.Body.String(), "token-should") {
		t.Fatal("failed domain query returned partial success or driver diagnostics")
	}
	_ = f.db.Callback().Query().Remove(faultName)
	routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/2/routing-groups", f.tokens["su"], ""), 200)
	f.s.routingGroupReads.mu.Lock()
	defer f.s.routingGroupReads.mu.Unlock()
	if f.s.routingGroupReads.active != 0 || len(f.s.routingGroupReads.actors) != 0 {
		t.Fatal("completed or failed shared reads retained capacity")
	}
}

func TestRoutingOrgAdminCancellationReleasesCapacity(t *testing.T) {
	f := newRoutingAdminHTTPFixtureAt(t, filepath.Join(t.TempDir(), "routing-org-cancel.db"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reached atomic.Bool
	name := "routing-org-cancel-after-org"
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "organizations" && reached.CompareAndSwap(false, true) {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	w := routingAdminTestRequest(f.engine, ctx, "GET", "/api/admin/orgs/2/routing-default", f.tokens["su"], "", "")
	routingAdminTestJSON(t, w, 503)
	if !reached.Load() {
		t.Fatal("cancel did not occur inside the actual business read")
	}
	_ = f.db.Callback().Query().Remove(name)
	routingAdminTestJSON(t, f.request("GET", "/api/admin/orgs/2/routing-default", f.tokens["su"], ""), 200)
	f.s.routingGroupReads.mu.Lock()
	defer f.s.routingGroupReads.mu.Unlock()
	if f.s.routingGroupReads.active != 0 || len(f.s.routingGroupReads.actors) != 0 {
		t.Fatal("canceled organization query leaked capacity")
	}
}
