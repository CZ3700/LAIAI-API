package relay

import "testing"

func TestClassifyUpstreamError(t *testing.T) {
	cases := []struct {
		name        string
		provider    string
		status      int
		body        string
		wantDisable bool
		wantRetry   bool
	}{
		{"openai insufficient_quota", "openai", 429,
			`{"error":{"type":"insufficient_quota","message":"You exceeded your quota"}}`, true, true},
		{"invalid api key code", "openai", 401,
			`{"error":{"code":"invalid_api_key","message":"bad key"}}`, true, true},
		{"claude auth error", "anthropic", 401,
			`{"error":{"type":"authentication_error","message":"x"}}`, true, true},
		{"gemini 403 disables", "gemini", 403, `{"error":{"message":"forbidden"}}`, true, true},
		{"non-gemini 403 no disable", "openai", 403, `{"error":{"message":"forbidden"}}`, false, false},
		{"rate limit 429 retryable no disable", "openai", 429,
			`{"error":{"type":"rate_limit_error","message":"slow down"}}`, false, true},
		{"gateway timeout 504 no retry", "openai", 504, `upstream timeout`, false, false},
		{"bad request 400 no retry no disable", "openai", 400,
			`{"error":{"type":"invalid_request_error","message":"bad"}}`, false, false},
		{"400 with disable type still retries to switch channel", "openai", 400,
			`{"error":{"type":"authentication_error","message":"bad key on this channel"}}`, true, true},
		{"408 timeout no disable no retry", "openai", 408, `request timeout`, false, false},
		{"server 500 retryable", "openai", 500, `{"error":{"message":"internal"}}`, false, true},
		{"quota exceeded message match", "openai", 402,
			`{"error":{"message":"Your quota has been exceeded for this month"}}`, true, true},
		{"garbage body 500 still retryable", "openai", 500, `not json at all`, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := classifyUpstreamError(c.provider, c.status, []byte(c.body))
			if d.DisableChannel != c.wantDisable {
				t.Errorf("DisableChannel = %v, want %v (reason=%q)", d.DisableChannel, c.wantDisable, d.Reason)
			}
			if d.Retryable != c.wantRetry {
				t.Errorf("Retryable = %v, want %v", d.Retryable, c.wantRetry)
			}
		})
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"end_turn": "stop", "stop_sequence": "stop",
		"max_tokens": "length", "tool_use": "tool_calls",
		"refusal": "content_filter", "unknown": "stop",
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}
