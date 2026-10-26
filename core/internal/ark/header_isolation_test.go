package ark

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type isolationCookieJar struct{ calls atomic.Int32 }

func (j *isolationCookieJar) Cookies(*url.URL) []*http.Cookie {
	j.calls.Add(1)
	return []*http.Cookie{{Name: "private-session", Value: "synthetic-cookie"}}
}
func (j *isolationCookieJar) SetCookies(*url.URL, []*http.Cookie) { j.calls.Add(1) }

func isolationCall(c *Client, action Action, in NormalizedRequest) Result {
	switch action {
	case ActionSubmit:
		return c.Submit(context.Background(), in)
	case ActionQuery:
		return c.Query(context.Background(), "task-fixed")
	default:
		return c.Delete(context.Background(), "task-fixed")
	}
}

func TestOfficialHeaderIsolationRedirectAndJar(t *testing.T) {
	in, err := Normalize(textRequest(Model20), testCapability(Model20), Bindings{})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{ActionSubmit, ActionQuery, ActionDelete} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%s/%d", action, status), func(t *testing.T) {
				jar := &isolationCookieJar{}
				var calls, redirectCalls atomic.Int32
				transport := roundTripFunc(func(q *http.Request) (*http.Response, error) {
					calls.Add(1)
					if q.URL.Scheme != "https" || q.URL.Host != "ark.cn-beijing.volces.com" || q.Header.Get("Authorization") != "Bearer synthetic-fixed" || q.Header.Get("Cookie") != "" || q.Header.Get("Idempotency-Key") != "" || q.GetBody != nil {
						t.Error("official fixed request boundary changed")
					}
					if (action == ActionSubmit) != (q.Header.Get("Content-Type") == "application/json") {
						t.Error("unexpected content-type")
					}
					wantHeaders := 1
					if action == ActionSubmit {
						wantHeaders++
					}
					if len(q.Header) != wantHeaders {
						t.Error("unexpected inherited header")
					}
					r := response(status, "redirect evidence")
					r.Header.Set("Location", "https://ark.cn-beijing.volces.com/other")
					if status == 302 {
						r.Header.Set("Location", "https://other.invalid/steal")
					}
					r.Header.Set("Set-Cookie", "private-session=should-not-persist")
					return r, nil
				})
				shared := &http.Client{Timeout: 5 * time.Second, Jar: jar, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { redirectCalls.Add(1); return nil }}
				c := &Client{BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Credential: "synthetic-fixed", HTTPClient: shared}
				result := isolationCall(c, action, in)
				if result.Outcome != OutcomeUnknown || result.HTTPStatus != status || string(result.Body) != "redirect evidence" || calls.Load() != 1 || redirectCalls.Load() != 0 || jar.calls.Load() != 0 {
					t.Fatal("redirect followed, cookies used or uncertainty/evidence changed")
				}
				if shared.Jar != jar || shared.Timeout != 5*time.Second || shared.CheckRedirect == nil {
					t.Fatal("shared client mutated")
				}
			})
		}
	}
}

func TestOfficialHeaderIsolationConcurrentCredentials(t *testing.T) {
	in, err := Normalize(textRequest(Model20), testCapability(Model20), Bindings{})
	if err != nil {
		t.Fatal(err)
	}
	jar := &isolationCookieJar{}
	var mu sync.Mutex
	seen := map[string]int{}
	shared := &http.Client{Jar: jar, Transport: roundTripFunc(func(q *http.Request) (*http.Response, error) {
		credential := q.Header.Get("Authorization")
		if credential != "Bearer synthetic-one" && credential != "Bearer synthetic-two" {
			t.Error("credential crossed request")
		}
		if q.Header.Get("Cookie") != "" {
			t.Error("jar reached official request")
		}
		mu.Lock()
		seen[credential]++
		mu.Unlock()
		if q.Method == http.MethodPost {
			return response(200, `{"id":"task-fixed"}`), nil
		}
		if q.Method == http.MethodGet {
			return response(200, `{"id":"task-fixed","status":"running"}`), nil
		}
		return response(204, ""), nil
	})}
	var wg sync.WaitGroup
	for _, credential := range []string{"synthetic-one", "synthetic-two"} {
		for _, action := range []Action{ActionSubmit, ActionQuery, ActionDelete} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := &Client{BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Credential: credential, HTTPClient: shared}
				if isolationCall(c, action, in).Outcome != OutcomeAccepted {
					t.Error("valid response rejected")
				}
			}()
		}
	}
	wg.Wait()
	if seen["Bearer synthetic-one"] != 3 || seen["Bearer synthetic-two"] != 3 || jar.calls.Load() != 0 || shared.Jar != jar {
		t.Fatal("concurrent fixed credentials were not isolated")
	}
}
