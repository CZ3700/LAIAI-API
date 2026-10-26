package relay

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type isolationRoundTripper func(*http.Request) (*http.Response, error)

func (f isolationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func isolationResponse(r *http.Request, status int, location, body string) *http.Response {
	return &http.Response{Request: r, StatusCode: status, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestRequestRedirectOrigin(t *testing.T) {
	for _, pair := range [][2]string{
		{"https://Example.test", "https://example.test:443"},
		{"http://example.test", "http://EXAMPLE.test:00080"},
		{"https://[2001:0DB8::1]", "https://[2001:db8:0:0:0:0:0:1]:443"},
		{"https://[fe80::1%25EthA]", "https://[fe80:0::1%25EthA]:443"},
	} {
		a, _ := url.Parse(pair[0])
		b, _ := url.Parse(pair[1])
		x, ok := redirectOrigin(a)
		y, ok2 := redirectOrigin(b)
		if !ok || !ok2 || x != y {
			t.Fatalf("valid equivalent origin rejected: %v", pair)
		}
	}
	for _, pair := range [][2]string{
		{"https://example.test", "http://example.test:443"},
		{"https://example.test", "https://example.test:444"},
		{"https://example.test", "https://sub.example.test"},
		{"https://example.test", "https://example.test."},
		{"https://[fe80::1%25EthA]", "https://[fe80::1%25etha]"},
	} {
		a, _ := url.Parse(pair[0])
		b, _ := url.Parse(pair[1])
		x, ok := redirectOrigin(a)
		y, ok2 := redirectOrigin(b)
		if !ok || !ok2 || x == y {
			t.Fatalf("distinct origin collapsed: %v", pair)
		}
	}
	for _, host := range []string{"", "a:", "a:0", "a:65536", "a:+443", "a:-1", "a:4a", "a:443:443", "[::1]:", "[::1]:65536", "[::1]extra", "[127.0.0.1]", "a%20b", "a b", "[fe80::1%bad zone]", "[fe80::1%z/one]"} {
		if _, ok := redirectOrigin(&url.URL{Scheme: "https", Host: host}); ok {
			t.Fatalf("invalid authority accepted: %q", host)
		}
	}
	for _, raw := range []string{"ftp://a.test/x", "https://user:secret@a.test/x", "https:opaque"} {
		u, _ := url.Parse(raw)
		if _, ok := redirectOrigin(u); ok {
			t.Fatalf("invalid redirect accepted: %s", raw)
		}
	}
}

func TestRequestRedirectAPIFactoryWiring(t *testing.T) {
	db := pickerDB(t)
	withSettings(t, db)
	r := New(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	factories := map[string]func() *http.Client{
		"relay_default":   func() *http.Client { return r.clientFor(nil) },
		"relay_proxy":     func() *http.Client { return r.clientFor(&Channel{Proxy: "http://proxy.invalid:8080"}) },
		"relay_advanced":  func() *http.Client { return r.clientFor(&Channel{Provider: ProviderAdvancedCustom}) },
		"relay_byok":      func() *http.Client { return r.clientFor(&Channel{OwnerOrgID: 23}) },
		"guarded_webhook": func() *http.Client { return NewGuardedClient(time.Second, true) },
		"gcp_oauth":       func() *http.Client { return gcpTokenHTTPClient },
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			original := factory()
			if original.Jar != nil {
				t.Fatal("factory enabled cookies")
			}
			client := *original // replace only the network; retain the real factory policy
			for _, target := range []string{"https://a.test/ok", "https://b.test/ok", "https://a.test:444/ok", "https://sub.a.test/ok", "http://a.test/ok", "https://user:pass@a.test/ok"} {
				calls := 0
				client.Transport = isolationRoundTripper(func(q *http.Request) (*http.Response, error) {
					calls++
					if q.Header.Get("X-Arbitrary-Credential") != "synthetic-private" {
						t.Error("same-origin header unexpectedly changed")
					}
					if calls == 1 {
						return isolationResponse(q, 307, target, "redirect"), nil
					}
					return isolationResponse(q, 200, "", "ok"), nil
				})
				q, _ := http.NewRequest(http.MethodPost, "https://a.test/start", strings.NewReader("private-body"))
				q.Header.Set("X-Arbitrary-Credential", "synthetic-private")
				resp, err := client.Do(q)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				want := 1
				if target == "https://a.test/ok" {
					want = 2
				}
				if calls != want || (want == 1 && resp.StatusCode != 307) {
					t.Fatalf("target %s calls=%d status=%d", target, calls, resp.StatusCode)
				}
			}
		})
	}
	api := r.guardedClient(false)
	media := r.guardedMediaClient(false)
	if api == media || api.Transport != media.Transport || api.Jar != nil || media.Jar != nil {
		t.Fatal("media must share only the transport pool")
	}
}

func TestRequestRedirectMediaStickyHeaders(t *testing.T) {
	for _, returnToA := range []bool{false, true} {
		t.Run(map[bool]string{false: "A_B_B", true: "A_B_A"}[returnToA], func(t *testing.T) {
			var mu sync.Mutex
			var seen []http.Header
			var hosts []string
			var a, b *httptest.Server
			handler := func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Header.Clone())
				hosts = append(hosts, r.Host)
				mu.Unlock()
				switch r.URL.Path {
				case "/start":
					http.Redirect(w, r, b.URL+"/middle?signature=synthetic", 302)
				case "/middle":
					if r.URL.Query().Get("signature") != "synthetic" {
						t.Error("signed location query lost")
					}
					target := b.URL
					if returnToA {
						target = a.URL
					}
					http.Redirect(w, r, target+"/final", 307)
				default:
					_, _ = w.Write([]byte("media-bytes"))
				}
			}
			a = httptest.NewServer(http.HandlerFunc(handler))
			defer a.Close()
			b = httptest.NewServer(http.HandlerFunc(handler))
			defer b.Close()
			client := NewGuardedMediaClient(3*time.Second, true)
			defer client.CloseIdleConnections()
			q, _ := http.NewRequest(http.MethodGet, a.URL+"/start?original-secret=synthetic", nil)
			q.Host = "original-custom-host.test"
			for _, k := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Goog-Api-Key", "X-Api-Key", "Api-Key", "X-Access-Token", "X-Auth-Token", "X-Random-Private", "Accept", "User-Agent"} {
				q.Header.Set(k, "synthetic-private")
			}
			before := q.Header.Clone()
			resp, err := client.Do(q)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != "media-bytes" {
				t.Fatal("download failed")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 3 || !reflect.DeepEqual(q.Header, before) {
				t.Fatal("missing hop or mutated initial headers")
			}
			for k := range before {
				if seen[0].Get(k) != "synthetic-private" {
					t.Errorf("first-hop header lost: %s", k)
				}
			}
			for i := 1; i < len(seen); i++ {
				for k, values := range seen[i] {
					for _, value := range values {
						if strings.Contains(value, "synthetic-private") || k == "Referer" {
							t.Errorf("credential/referer reached hop %d: %s", i, k)
						}
					}
				}
				if hosts[i] == q.Host {
					t.Error("custom Host survived crossing")
				}
			}
		})
	}
}

func TestRequestRedirectSameHostDowngradeAndTLS(t *testing.T) {
	var plainCalls atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { plainCalls.Add(1); w.WriteHeader(200) }))
	defer plain.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://same.test:443/end", 302) }))
	defer tlsServer.Close()
	for _, factory := range []func() *http.Client{func() *http.Client { return NewUpstreamClient(3 * time.Second) }, func() *http.Client { return NewGuardedMediaClient(3*time.Second, true) }} {
		client := factory()
		calls := 0
		trusted := tlsServer.Client().Transport
		// The real TLS exchange still verifies the fixture CA/IP SAN. Only the
		// physical endpoint is mapped; Client.Do sees the unchanged same authority.
		client.Transport = isolationRoundTripper(func(q *http.Request) (*http.Response, error) {
			calls++
			physical := q.Clone(q.Context())
			u := *q.URL
			physical.URL = &u
			if q.URL.Scheme == "https" {
				p, _ := url.Parse(tlsServer.URL)
				physical.URL.Host = p.Host
				return trusted.RoundTrip(physical)
			}
			p, _ := url.Parse(plain.URL)
			physical.URL.Host = p.Host
			return http.DefaultTransport.RoundTrip(physical)
		})
		q, _ := http.NewRequest(http.MethodGet, "https://same.test:443/start", nil)
		q.Header.Set("X-Private", "synthetic-private")
		resp, err := client.Do(q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if calls != 1 || resp.StatusCode != 302 || plainCalls.Load() != 0 {
			t.Fatal("same-host HTTPS downgrade sent a request")
		}
	}
	// A client that has not trusted the fixture certificate must reject it.
	untrusted := NewGuardedMediaClient(time.Second, true)
	defer untrusted.CloseIdleConnections()
	if resp, err := untrusted.Get(tlsServer.URL); err == nil {
		resp.Body.Close()
		t.Fatal("TLS verification was disabled")
	}
}

func TestRequestRedirectMediaMethodLimitAndCancellation(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		client := NewGuardedMediaClient(time.Second, true)
		calls := 0
		client.Transport = isolationRoundTripper(func(q *http.Request) (*http.Response, error) {
			calls++
			return isolationResponse(q, 303, "http://b.test/end", ""), nil
		})
		q, _ := http.NewRequest(method, "http://a.test/start", strings.NewReader("private-body"))
		resp, err := client.Do(q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if calls != 1 || resp.StatusCode != 303 {
			t.Fatal("body-bearing media request followed redirect")
		}
	}
	for _, client := range []*http.Client{NewUpstreamClient(time.Second), NewGuardedMediaClient(time.Second, true)} {
		calls := 0
		client.Transport = isolationRoundTripper(func(q *http.Request) (*http.Response, error) {
			calls++
			return isolationResponse(q, 302, "http://a.test/again", ""), nil
		})
		resp, err := client.Get("http://a.test/start")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if calls != 5 {
			t.Fatalf("redirect limit changed: %d", calls)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	q, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	client := NewGuardedMediaClient(time.Second, true)
	defer client.CloseIdleConnections()
	if _, err := client.Do(q); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("caller cancellation not preserved")
	}
}

func TestRequestRedirectReplicateMediaSplit(t *testing.T) {
	var leaked atomic.Bool
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		_, _ = w.Write([]byte("image"))
	}))
	defer cdn.Close()
	var origin *httptest.Server
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/image" {
			http.Redirect(w, r, cdn.URL+"/final", 302)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-private" {
			t.Error("prediction poll credential lost")
		}
		_, _ = io.WriteString(w, `{"id":"prediction","status":"succeeded","output":["`+origin.URL+`/image"]}`)
	}))
	defer origin.Close()
	r := &Relay{Client: NewUpstreamClient(time.Second)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := replicateFinalizeImages(ctx, r.clientFor(nil), r.guardedMediaClient(true), origin.URL, "synthetic-private", []byte(`{"id":"prediction","status":"processing"}`), true, 1, time.Now)
	if err != nil || leaked.Load() || !strings.Contains(string(result), base64.StdEncoding.EncodeToString([]byte("image"))) {
		t.Fatalf("media split failed: %v", err)
	}
}

func TestRequestRedirectGCPAssertionAndProxy(t *testing.T) {
	var second atomic.Int32
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		second.Add(1)
		_, _ = io.WriteString(w, `{"access_token":"wrong-origin","expires_in":3600}`)
	}))
	defer b.Close()
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("assertion") == "" {
			t.Error("real signed assertion did not reach first origin")
		}
		http.Redirect(w, r, b.URL+"/token", 307)
	}))
	defer a.Close()
	raw, _ := testSAJSON(t, a.URL)
	sa, err := parseGCPServiceAccount(raw)
	if err != nil {
		t.Fatal(err)
	}
	sa.ClientEmail = "redirect-isolation@synthetic.test"
	sa.ProjectID = "redirect-isolation"
	if _, err := gcpAccessToken(t.Context(), sa, time.Now()); err == nil || second.Load() != 0 {
		t.Fatal("OAuth assertion followed cross-origin redirect")
	}
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		if r.URL.Host != "initial.invalid" {
			t.Error("proxy sent to redirect target")
		}
		w.Header().Set("Location", "http://second.invalid/request")
		w.WriteHeader(307)
	}))
	defer proxy.Close()
	r := &Relay{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	client := r.clientFor(&Channel{Proxy: proxy.URL})
	defer client.CloseIdleConnections()
	q, _ := http.NewRequest(http.MethodPost, "http://initial.invalid/request", strings.NewReader("private-body"))
	q.Header.Set("X-Custom-Secret", "synthetic")
	resp, err := client.Do(q)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if proxyCalls.Load() != 1 || resp.StatusCode != 307 {
		t.Fatal("real proxy client followed cross-origin redirect")
	}
}

func TestRequestRedirectConcurrentMediaChains(t *testing.T) {
	client := NewGuardedMediaClient(time.Second, true)
	var calls atomic.Int32
	client.Transport = isolationRoundTripper(func(q *http.Request) (*http.Response, error) {
		calls.Add(1)
		id := q.URL.Query().Get("id")
		if q.URL.Path == "/start" {
			if q.Header.Get("X-Private") != id {
				t.Error("first-hop request identity crossed")
			}
			return isolationResponse(q, 302, "http://b.test/again?id="+id, ""), nil
		}
		if q.Header.Get("X-Private") != "" || q.Header.Get("Referer") != "" {
			t.Error("concurrent chain leaked credential")
		}
		if q.URL.Path == "/again" {
			return isolationResponse(q, 302, "http://a.test/end?id="+id, ""), nil
		}
		return isolationResponse(q, 200, "", id), nil
	})
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two", "three", "four"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, _ := http.NewRequest(http.MethodGet, "http://a.test/start?id="+id, nil)
			q.Header.Set("X-Private", id)
			resp, err := client.Do(q)
			if err != nil {
				t.Error(err)
				return
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != id || q.Header.Get("X-Private") != id {
				t.Error("concurrent media request mutated")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 12 {
		t.Fatalf("unexpected concurrent hop count: %d", calls.Load())
	}
}
