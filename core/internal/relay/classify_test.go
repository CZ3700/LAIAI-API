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
		// B-1：渠道特有 4xx 换渠道（不禁用）——403/404/409/422 换服务该模型的同层兄弟
		{"non-gemini 403 retries not disable", "openai", 403, `{"error":{"message":"forbidden"}}`, false, true},
		{"404 model not on channel retries", "openai", 404, `{"error":{"message":"model not found"}}`, false, true},
		{"409 conflict retries", "openai", 409, `{"error":{"message":"conflict"}}`, false, true},
		{"422 unprocessable retries", "openai", 422, `{"error":{"message":"unprocessable"}}`, false, true},
		// B-2：硬故障消息关键词 → 建议禁用（余额/token/组织）——不论 HTTP 码
		{"anthropic credit balance 400 suggests disable", "anthropic", 400,
			`{"error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`, true, true},
		{"aws security token 403 suggests disable", "aws", 403,
			`{"error":{"message":"The security token included in the request is invalid"}}`, true, true},
		{"org disabled suggests disable", "openai", 403,
			`{"error":{"message":"This organization has been disabled."}}`, true, true},
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

// healthPenalty（对抗 review 修）：只对渠道整体可靠性失败（5xx/429/硬故障）扣健康分；
// 渠道特有 4xx（403/404/409/422）换渠道但不扣（否则跨模型误降权）。
func TestHealthPenalty(t *testing.T) {
	yes := []struct {
		disp   upstreamDisposition
		status int
	}{
		{upstreamDisposition{DisableChannel: true, Retryable: true}, 400}, // 硬故障
		{upstreamDisposition{Retryable: true}, 429},                       // 限流
		{upstreamDisposition{Retryable: true}, 500},                       // 5xx
		{upstreamDisposition{Retryable: true}, 503},
	}
	for _, c := range yes {
		if !healthPenalty(c.disp, c.status) {
			t.Errorf("status %d should penalize health", c.status)
		}
	}
	// 渠道特有 4xx：Retryable=true 但**不**扣健康分
	no := []int{403, 404, 409, 422, 400}
	for _, s := range no {
		if healthPenalty(upstreamDisposition{Retryable: true}, s) {
			t.Errorf("status %d (channel-specific 4xx) must NOT penalize health", s)
		}
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
