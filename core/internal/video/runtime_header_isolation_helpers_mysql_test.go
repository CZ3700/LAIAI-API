package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/videobilling"
)

// Only this test's active channel is changed before real acceptance. The
// accepted credential snapshot is never rewritten to manufacture isolation.
func runtimeHeaderFixture(t *testing.T, credential string) *runtimeHTTPFixture {
	t.Helper()
	f := mysqlFixture(t)
	var cfg ModelConfig
	must(t, f.s.DB.Where("public_model = ?", ark.Model20).Take(&cfg).Error)
	cipher, err := f.s.Keyring.Encrypt(credential)
	must(t, err)
	must(t, f.s.DB.Model(&relay.Channel{}).Where("id = ?", cfg.ChannelID).Update("credential_enc", cipher).Error)
	v := f.create(t, "header-isolation")
	h := &runtimeHTTPFixture{f: f, id: v.ID, upstreamID: "up_" + v.ID, history: true}
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&h.state).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&h.task).Error)
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, h.state.BillingSnapshotID).Take(&h.snapshot).Error)
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, h.state.CredentialSnapshotID).Take(&h.credential).Error)
	h.hold = f.hold(t, v.ID)
	must(t, f.s.DB.First(&h.account, h.hold.AccountID).Error)
	h.input, err = f.s.Evidence.ReadForExecutionSchema(context.Background(), f.key.OrgID, h.state.EffectivePayloadRef, 1, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "header-isolation", ReasonCode: "dispatch"})
	must(t, err)
	_, buckets, key := h.quotaFacts(t)
	h.buckets = make(map[uint64]videobilling.QuotaBucket, len(buckets))
	for _, bucket := range buckets {
		h.buckets[bucket.ID] = bucket
	}
	h.keyUsed = key.UsedAmount
	h.pinned(t)
	return h
}

type runtimeHeaderWire struct {
	mu                  sync.Mutex
	h                   *runtimeHTTPFixture
	credential          string
	method              string
	status              int
	body                string
	calls, secondHops   int
	badHeaders          bool
	client              *http.Client
	jar                 http.CookieJar
	originalRedirectRan bool
}

func runtimeHeaderInstall(t *testing.T, h *runtimeHTTPFixture, credential string) *runtimeHeaderWire {
	t.Helper()
	jar, err := cookiejar.New(nil)
	must(t, err)
	u, err := url.Parse(h.state.ProviderBaseURL)
	must(t, err)
	jar.SetCookies(u, []*http.Cookie{{Name: "synthetic_jar_secret", Value: "private-cookie", Path: "/"}})
	w := &runtimeHeaderWire{h: h, credential: credential, jar: jar}
	w.client = &http.Client{Transport: roundTripFunc(w.roundTrip), Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.originalRedirectRan = true
		return nil
	}}
	h.f.s.Client = w.client
	return w
}

func (w *runtimeHeaderWire) set(method string, status int, body string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.method, w.status, w.body = method, status, body
}

func (w *runtimeHeaderWire) roundTrip(req *http.Request) (*http.Response, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if req.URL.Host != "ark.cn-beijing.volces.com" || req.URL.Scheme != "https" {
		w.secondHops++
		return reply(200, `{}`), nil
	}
	path := "/api/v3/contents/generations/tasks"
	if req.Method != http.MethodPost {
		path += "/" + w.h.upstreamID
	}
	valid := req.Method == w.method && req.URL.Path == path && req.URL.RawQuery == "" && req.GetBody == nil && req.Header.Get("Authorization") == "Bearer "+w.credential
	for name := range req.Header {
		if name != "Authorization" && !(req.Method == http.MethodPost && name == "Content-Type") {
			valid = false
		}
	}
	if req.Method == http.MethodPost {
		body, err := io.ReadAll(req.Body)
		valid = valid && err == nil && bytes.Equal(body, w.h.input) && req.Header.Get("Content-Type") == "application/json"
	}
	if !valid {
		w.badHeaders = true
		return nil, errors.New("synthetic fixed request contract mismatch")
	}
	response := reply(w.status, w.body)
	if w.status >= 300 && w.status < 400 {
		response.Header.Set("Location", "https://redirect.invalid/never-follow?synthetic=1")
	}
	return response, nil
}

func (w *runtimeHeaderWire) assert(t *testing.T, calls int) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	u, err := url.Parse(w.h.state.ProviderBaseURL)
	must(t, err)
	if w.calls != calls || w.secondHops != 0 || w.badHeaders || w.originalRedirectRan || w.h.f.s.Client != w.client || w.client.Jar != w.jar || len(w.jar.Cookies(u)) != 1 || w.client.CheckRedirect == nil {
		t.Fatal("fixed request isolation, no second hop, or shared client preservation failed")
	}
}

func runtimeHeaderActiveOverride(t *testing.T, h *runtimeHTTPFixture) {
	t.Helper()
	h.reprice(t)
	body, err := json.Marshal(map[string]string{"Authorization": "Bearer active-override-secret", "X-Private-Override": "synthetic-private-header", "Cookie": "override=private"})
	must(t, err)
	must(t, h.f.s.DB.Model(&relay.Channel{}).Where("id = ?", h.task.ChannelID).Update("header_override", string(body)).Error)
	var cfg ModelConfig
	must(t, h.f.s.DB.Where("public_model = ?", h.task.Model).Take(&cfg).Error)
	must(t, h.f.s.DB.Model(&relay.Channel{}).Where("id = ?", cfg.ChannelID).Update("header_override", string(body)).Error)
	h.pinned(t)
}
