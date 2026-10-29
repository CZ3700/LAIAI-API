package ark

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func testClient(rt roundTripFunc) *Client {
	return &Client{BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Credential: "private-test-credential", HTTPClient: &http.Client{Transport: rt}}
}

func TestHistoricalOfficialFixtures(t *testing.T) {
	// Reused from task/volcengine_fixture_test.go: captured 2026-07-05;
	// artifact signed URL is redacted. These are not new 2.5 validation results.
	create := `{"id":"cgt-20260705030718-2lkj4"}`
	running := `{"id":"cgt-20260705030718-2lkj4","model":"doubao-seedance-2-0-260128","status":"running","created_at":1783192038,"updated_at":1783192038,"service_tier":"default","execution_expires_after":172800,"generate_audio":false,"draft":false,"priority":0}`
	succeeded := `{"id":"cgt-20260705030718-2lkj4","model":"doubao-seedance-2-0-260128","status":"succeeded","content":{"video_url":"https://ark-acg-cn-beijing.tos-cn-beijing.volces.com/doubao-seedance-2-0/02178319203885700000000000000000000ffffac181416f7b23e.mp4?X-Tos-Algorithm=TOS4-HMAC-SHA256&X-Tos-Credential=REDACTED&X-Tos-Signature=REDACTED"},"usage":{"completion_tokens":87300,"total_tokens":87300},"created_at":1783192038,"updated_at":1783192284,"seed":91074,"resolution":"720p","ratio":"16:9","duration":4,"framespersecond":24,"service_tier":"default","execution_expires_after":172800,"generate_audio":false,"draft":false,"priority":0}`
	if r := ParseResponse(ActionSubmit, response(200, create), 0); r.Outcome != OutcomeAccepted || r.TaskID != "cgt-20260705030718-2lkj4" {
		t.Fatalf("submit: %+v", r)
	}
	if r := ParseResponse(ActionQuery, response(200, running), 0); r.Outcome != OutcomeAccepted || r.Observation.Usage.CompletionTokens != nil {
		t.Fatalf("running usage must be absent: %+v", r)
	}
	if r := ParseResponse(ActionQuery, response(200, succeeded), 0); r.Outcome != OutcomeAccepted || r.Observation.Usage.CompletionTokens == nil || *r.Observation.Usage.CompletionTokens != 87300 || r.Observation.Status != StatusSucceeded {
		t.Fatalf("success: %+v", r)
	} else {
		o := r.Observation
		if o.Seed == nil || *o.Seed != 91074 || o.Resolution != "720p" || o.Ratio != "16:9" || o.Duration == nil || *o.Duration != 4 || o.FramesPerSecond == nil || *o.FramesPerSecond != 24 || o.ServiceTier != "default" || o.ExecutionExpiresAfter == nil || *o.ExecutionExpiresAfter != 172800 || o.GenerateAudio == nil || *o.GenerateAudio || o.Draft == nil || *o.Draft || o.Priority == nil || *o.Priority != 0 {
			t.Fatalf("official query parameters were lost: %+v", o)
		}
	}
}

func TestQueryParameterValidationAndOptionalValues(t *testing.T) {
	body := `{"id":"cgt-test","status":"succeeded","generate_audio":false,"draft":false,"priority":0}`
	parsed := ParseResponse(ActionQuery, response(200, body), 0)
	if parsed.Outcome != OutcomeAccepted || parsed.Observation.GenerateAudio == nil || *parsed.Observation.GenerateAudio || parsed.Observation.Draft == nil || *parsed.Observation.Draft || parsed.Observation.Priority == nil || *parsed.Observation.Priority != 0 {
		t.Fatalf("explicit false/zero fields were lost: %+v", parsed)
	}
	for _, body := range []string{
		`{"id":"cgt-test","status":"succeeded","duration":"5"}`,
		`{"id":"cgt-test","status":"succeeded","framespersecond":24.5}`,
		`{"id":"cgt-test","status":"succeeded","execution_expires_after":"172800"}`,
		`{"id":"cgt-test","status":"succeeded","priority":-1}`,
		`{"id":"cgt-test","status":"succeeded","priority":10}`,
		`{"id":"cgt-test","status":"succeeded","duration":5,"frames":120}`,
	} {
		parsed := ParseResponse(ActionQuery, response(200, body), 0)
		if parsed.Outcome != OutcomeUnknown || parsed.Observation != nil {
			t.Errorf("invalid query parameters were accepted: %s", body)
		}
	}
}

func TestSimulatedProtocolUncertaintyAndUsage(t *testing.T) {
	// All cases in this test are simulated malformed/edge responses.
	for _, tc := range []struct {
		status int
		body   string
		want   Outcome
	}{{200, `{}`, OutcomeUnknown}, {200, `{"id":"a","id":"b"}`, OutcomeUnknown}, {500, `{"error":{"message":"private payload"}}`, OutcomeUnknown}, {408, `{}`, OutcomeUnknown}, {429, `{}`, OutcomeRejected}, {400, `{}`, OutcomeRejected}, {302, ``, OutcomeUnknown}} {
		r := ParseResponse(ActionSubmit, response(tc.status, tc.body), 0)
		if r.Outcome != tc.want {
			t.Errorf("%d %q: %s", tc.status, tc.body, r.Outcome)
		}
		if r.Error != nil && strings.Contains(r.Error.Error(), "private payload") {
			t.Fatal("upstream payload escaped in error")
		}
	}
	for _, usage := range []string{`{}`, `{"completion_tokens":null}`, `{"completion_tokens":0}`, `{"completion_tokens":-1}`, `{"completion_tokens":"3"}`, `{"completion_tokens":1.5}`, `{"completion_tokens":9223372036854775808}`} {
		r := ParseResponse(ActionQuery, response(200, `{"id":"cgt-test","status":"succeeded","usage":`+usage+`}`), 0)
		if usage == `{}` || usage == `{"completion_tokens":null}` {
			if r.Outcome != OutcomeAccepted || r.Observation.Usage.CompletionTokens != nil {
				t.Fatal("absence became zero")
			}
		} else if usage == `{"completion_tokens":0}` {
			if r.Outcome != OutcomeAccepted || r.Observation.Usage.CompletionTokens == nil || *r.Observation.Usage.CompletionTokens != 0 {
				t.Fatal("explicit zero lost")
			}
		} else if r.Outcome != OutcomeUnknown {
			t.Errorf("bad usage accepted: %s", usage)
		}
	}
	r := ParseResponse(ActionQuery, response(200, `{"id":"cgt-test","status":"new-state"}`), 0)
	if r.Observation == nil || r.Observation.Status != "" || r.Observation.RawStatus != "new-state" {
		t.Fatal("unknown state was guessed")
	}
}

type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	copy(p, `{"id":"cgt-test"`)
	return len(`{"id":"cgt-test"`), io.ErrUnexpectedEOF
}
func (failingReader) Close() error { return nil }

func TestBoundedEvidenceAndPartialBody(t *testing.T) {
	r := ParseResponse(ActionSubmit, response(200, strings.Repeat("x", 33)), 32)
	if !r.Truncated || len(r.Body) != 32 || r.Outcome != OutcomeUnknown {
		t.Fatalf("limit failed: %+v", r)
	}
	r = ParseResponse(ActionSubmit, &http.Response{StatusCode: 200, Body: failingReader{}}, 0)
	if r.Outcome != OutcomeUnknown || r.Error == nil || len(r.Body) == 0 {
		t.Fatal("partial response accepted or evidence discarded")
	}
}

func TestSubmitNeverRetriesOrFollowsRedirect(t *testing.T) {
	in, err := Normalize(textRequest(Model20), testCapability(Model20), Bindings{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "Bearer private-test-credential" || req.Header.Get("Idempotency-Key") != "" || req.GetBody != nil {
			t.Fatal("unexpected auth or replay configuration")
		}
		return nil, io.ErrUnexpectedEOF
	})
	if r := c.Submit(context.Background(), in); r.Outcome != OutcomeUnknown || calls != 1 {
		t.Fatalf("unsafe retry: %+v calls %d", r, calls)
	}
	calls = 0
	c.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		r := response(307, "")
		r.Header.Set("Location", "https://attacker.example/")
		return r, nil
	})
	if r := c.Submit(context.Background(), in); r.Outcome != OutcomeUnknown || calls != 1 {
		t.Fatal("followed redirect")
	}
}

func TestTransportNotSentOnlyWithDefiniteEvidence(t *testing.T) {
	in, _ := Normalize(textRequest(Model20), testCapability(Model20), Bindings{})
	for _, tc := range []struct {
		err  error
		want Outcome
	}{{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, OutcomeNotSent}, {context.DeadlineExceeded, OutcomeUnknown}, {context.Canceled, OutcomeUnknown}, {io.EOF, OutcomeUnknown}} {
		c := testClient(func(*http.Request) (*http.Response, error) { return nil, tc.err })
		if r := c.Submit(context.Background(), in); r.Outcome != tc.want {
			t.Fatalf("%v -> %s", tc.err, r.Outcome)
		}
	}
	calls := 0
	c := testClient(func(*http.Request) (*http.Response, error) { calls++; return response(200, `{"id":"x"}`), nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := c.Submit(ctx, in); r.Outcome != OutcomeNotSent || calls != 0 {
		t.Fatal("dispatched cancelled context")
	}
}

func TestRequestConstructionAndIdentityProtection(t *testing.T) {
	c := testClient(func(*http.Request) (*http.Response, error) {
		r := response(200, `{"id":"other","status":"running"}`)
		r.Header.Set("X-Request-Id", "req-safe")
		return r, nil
	})
	if _, err := c.BuildQuery(context.Background(), "../other?token=secret"); err == nil {
		t.Fatal("path injection accepted")
	}
	r := c.Query(context.Background(), "cgt-expected")
	if r.Outcome != OutcomeUnknown || r.Observation != nil || r.RequestID != "req-safe" {
		t.Fatal("query identity mismatch accepted")
	}
	for _, base := range []string{"http://ark.cn-beijing.volces.com/api/v3", "https://evil.example/api/v3", "https://ark.cn-beijing.volces.com/api/v3?key=secret", "https://secret@ark.cn-beijing.volces.com/api/v3"} {
		c.BaseURL = base
		if _, err := c.BuildDelete(context.Background(), "cgt-test"); err == nil {
			t.Fatal("unsafe base URL accepted")
		}
	}
}
