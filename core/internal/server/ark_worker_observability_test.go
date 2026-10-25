package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
)

type observabilityDenyTransport struct{ calls atomic.Int32 }

func (tr *observabilityDenyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tr.calls.Add(1)
	return nil, fmt.Errorf("unexpected provider call: secret-sentinel")
}

func TestArkWorkerObservabilityRequiresNamedActiveSuperadmin(t *testing.T) {
	f, db := setupRelayServer(t)
	transport := &observabilityDenyTransport{}
	f.srv.video = &video.Service{DB: db, Now: time.Now, Client: &http.Client{Transport: transport}}
	const path = "/api/admin/video/worker-observability"
	call := func(token, shared, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path+query, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if shared != "" {
			r.Header.Set("X-Admin-Token", shared)
		}
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	if w := call("", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated status=%d", w.Code)
	}
	if w := call("", testAdminToken, ""); w.Code != 403 {
		t.Fatalf("shared token status=%d", w.Code)
	}
	org := identity.Organization{Slug: "worker-observation", Name: "worker-observation", Status: identity.StatusActive}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	apiKey, _, err := identity.IssueKey(db, identity.IssueKeyInput{OrgID: org.ID, Name: "worker-observation", Scopes: "video:*"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.VerifyKey(db, apiKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	if w := call(apiKey, "", ""); w.Code != 401 {
		t.Fatalf("valid data-plane API key status=%d", w.Code)
	}
	var superToken string
	for i, role := range []string{"user", "admin", "superadmin"} {
		user := identity.User{Email: fmt.Sprintf("worker-observation-%d@test.invalid", i), EmailCanonical: fmt.Sprintf("worker-observation-%d@test.invalid", i), Role: role, Status: identity.StatusActive}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		token, _, err := identity.IssueSessionToken(testSecret, user.ID, 1, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		w := call(token, "", "")
		if role != "superadmin" {
			if w.Code != 403 {
				t.Fatalf("role=%s status=%d", role, w.Code)
			}
			continue
		}
		superToken = token
		if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("superadmin status=%d cache=%q body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"schema_version", "observed_at", "runtime", "database", "db_pool"} {
			if _, ok := out[field]; !ok {
				t.Fatalf("missing %s", field)
			}
		}
		for _, secret := range []string{"secret-sentinel", "worker-observation-", "credential", "task_id", "org_id", "provider_scope_id", "request_id"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("observation leaks %q", secret)
			}
		}
		for _, query := range []string{"?org_id=1", "?since=2026-10-25", "?refresh=true", "?unknown="} {
			if w := call(token, "", query); w.Code != 422 {
				t.Fatalf("arbitrary query accepted: %s %d", query, w.Code)
			}
		}
		// The cached process/database snapshot must not bypass live role checks.
		if err := db.Model(&identity.User{}).Where("id = ?", user.ID).Update("role", "admin").Error; err != nil {
			t.Fatal(err)
		}
		if w := call(token, "", ""); w.Code != 403 {
			t.Fatalf("revoked superadmin read cached observations: %d", w.Code)
		}
		if err := db.Model(&identity.User{}).Where("id = ?", user.ID).Updates(map[string]any{"role": "superadmin", "status": 2}).Error; err != nil {
			t.Fatal(err)
		}
		if w := call(token, "", ""); w.Code == 200 {
			t.Fatal("inactive superadmin read observations")
		}
		if err := db.Model(&identity.User{}).Where("id = ?", user.ID).Update("status", identity.StatusActive).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.srv.video = nil
	if w := call(superToken, "", ""); w.Code != 503 {
		t.Fatalf("disabled video runtime status=%d", w.Code)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("observation endpoint called upstream")
	}
}
