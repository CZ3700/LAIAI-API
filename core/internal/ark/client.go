package ark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (c *Client) BuildSubmit(ctx context.Context, in NormalizedRequest) (*http.Request, error) {
	if len(in.Body) == 0 || int64(len(in.Body)) > DefaultMaxRequestBytes || !json.Valid(in.Body) || !upstreamID.MatchString(in.UpstreamModel) {
		return nil, failure("invalid_request", "normalized request is required", "body", 422)
	}
	fields, ok := closedObject(in.Body, createFields[:len(createFields)-2])
	var model string
	if !ok || !uniqueJSON(in.Body) || json.Unmarshal(fields["model"], &model) != nil || model != in.UpstreamModel || len(fields["content"]) == 0 {
		return nil, failure("invalid_request", "normalized body and bound upstream model must agree", "body", 422)
	}
	return c.build(ctx, http.MethodPost, "", in.Body)
}
func (c *Client) BuildQuery(ctx context.Context, taskID string) (*http.Request, error) {
	return c.build(ctx, http.MethodGet, taskID, nil)
}
func (c *Client) BuildDelete(ctx context.Context, taskID string) (*http.Request, error) {
	return c.build(ctx, http.MethodDelete, taskID, nil)
}
func (c *Client) build(ctx context.Context, method, taskID string, body []byte) (*http.Request, error) {
	if ctx == nil {
		return nil, failure("invalid_request", "context is required", "", 500)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || !strings.HasPrefix(u.Hostname(), "ark.") || !strings.HasSuffix(u.Hostname(), ".volces.com") || (u.Port() != "" && u.Port() != "443") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != "/api/v3" {
		return nil, failure("invalid_configuration", "official Ark HTTPS base URL is required", "base_url", 500)
	}
	if c.Credential == "" || strings.ContainsAny(c.Credential, "\r\n\t ") {
		return nil, failure("invalid_configuration", "valid upstream credential is required", "credential", 500)
	}
	path := "/api/v3/contents/generations/tasks"
	if method != http.MethodPost {
		if !upstreamID.MatchString(taskID) {
			return nil, failure("invalid_request", "invalid upstream task reference", "task_id", 422)
		}
		path += "/" + taskID
	}
	u.Path = path
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, failure("invalid_request", "could not build upstream request", "", 500)
	}
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	// No Idempotency-Key is forwarded: official support has not been verified.
	// Disable request replay/307 redirection even though bytes.Reader has GetBody.
	req.GetBody = nil
	return req, nil
}

func (c *Client) Submit(ctx context.Context, in NormalizedRequest) Result {
	req, err := c.BuildSubmit(ctx, in)
	return c.execute(ActionSubmit, req, err)
}
func (c *Client) Query(ctx context.Context, taskID string) Result {
	req, err := c.BuildQuery(ctx, taskID)
	result := c.execute(ActionQuery, req, err)
	if result.Observation != nil && result.Observation.TaskID != taskID {
		result.Outcome = OutcomeUnknown
		result.Observation = nil
		result.Error = failure("upstream_identity_mismatch", "upstream task identity does not match the query", "", 502)
	}
	return result
}
func (c *Client) Delete(ctx context.Context, taskID string) Result {
	req, err := c.BuildDelete(ctx, taskID)
	return c.execute(ActionDelete, req, err)
}

func (c *Client) execute(action Action, req *http.Request, buildErr error) Result {
	if buildErr != nil {
		var safe *Error
		if !errors.As(buildErr, &safe) {
			safe = failure("invalid_request", "request could not be built", "", 500)
		}
		return Result{Action: action, Outcome: OutcomeNotSent, Error: safe}
	}
	if req.Context().Err() != nil {
		return Result{Action: action, Outcome: OutcomeNotSent, Error: failure("request_cancelled", "request was cancelled before dispatch", "", 503)}
	}
	client := http.Client{Timeout: 2 * time.Minute}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
		if client.Timeout <= 0 {
			client.Timeout = 2 * time.Minute
		}
	}
	// Fixed official headers never inherit cookies from an injected client's jar.
	// Modify only this value copy, not the shared client used by other tasks.
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req) // Exactly one application-level attempt. Never retry POST here.
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		outcome := OutcomeUnknown
		// Only a definite dial failure proves no HTTP request reached Ark. A
		// timeout, EOF, context cancellation or generic RoundTripper error does not.
		var network *net.OpError
		if errors.As(err, &network) && network.Op == "dial" {
			outcome = OutcomeNotSent
		}
		return Result{Action: action, Outcome: outcome, Error: &Error{Code: "upstream_transport_error", Message: "upstream transport did not return a confirmed result", Retryable: action == ActionQuery, HTTPStatus: 502}}
	}
	return ParseResponse(action, resp, c.MaxResponseBytes)
}

// ParseResponse owns and closes the response body. Unknown fields can be added
// by Ark, but duplicate fields, missing IDs/status, malformed usage or truncated
// bodies are never silently treated as a billable successful response.
func ParseResponse(action Action, resp *http.Response, maxBytes int64) Result {
	out := Result{Action: action, Outcome: OutcomeUnknown}
	if resp == nil || resp.Body == nil {
		out.Error = failure("invalid_upstream_response", "upstream response is missing", "", 502)
		return out
	}
	defer resp.Body.Close()
	out.HTTPStatus = resp.StatusCode
	out.RequestID = safeRequestID(resp.Header.Get("X-Request-Id"))
	if out.RequestID == "" {
		out.RequestID = safeRequestID(resp.Header.Get("X-Tt-Logid"))
	}
	if maxBytes <= 0 || maxBytes > DefaultMaxResponseBytes {
		maxBytes = DefaultMaxResponseBytes
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if int64(len(raw)) > maxBytes {
		out.Body = raw[:maxBytes]
		out.Truncated = true
		out.Error = failure("upstream_response_too_large", "upstream response exceeded the evidence limit", "", 502)
		return out
	}
	out.Body = raw
	if err != nil {
		out.Error = failure("incomplete_upstream_response", "upstream response was incomplete", "", 502)
		return out
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 408 and 5xx may follow a committed create. Redirects are never followed.
		if containsStatus([]int{400, 401, 403, 404, 409, 413, 422, 429}, resp.StatusCode) {
			out.Outcome = OutcomeRejected
		}
		out.Error = &Error{Code: "upstream_rejected", Message: "upstream did not accept this request", Retryable: action == ActionQuery && (resp.StatusCode == 429 || resp.StatusCode >= 500), HTTPStatus: 502}
		if resp.StatusCode == 404 {
			out.Error.Code = "upstream_not_found"
		}
		if out.Outcome == OutcomeUnknown {
			out.Error.Code = "upstream_outcome_unknown"
			out.Error.Message = "upstream result requires reconciliation"
		}
		return out
	}
	if action == ActionDelete {
		out.Outcome = OutcomeAccepted
		return out
	}
	if action != ActionSubmit && action != ActionQuery {
		out.Error = failure("invalid_action", "unsupported protocol action", "", 500)
		return out
	}
	if !uniqueJSON(raw) {
		out.Error = failure("invalid_upstream_response", "upstream returned invalid or duplicate JSON", "", 502)
		return out
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		out.Error = failure("invalid_upstream_response", "upstream response must be an object", "", 502)
		return out
	}
	if json.Unmarshal(fields["id"], &out.TaskID) != nil || !upstreamID.MatchString(out.TaskID) {
		out.Error = failure("invalid_upstream_response", "upstream task ID is missing or invalid", "", 502)
		return out
	}
	if action == ActionSubmit {
		if raw, ok := fields["error"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && !bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
			out.Error = failure("invalid_upstream_response", "upstream returned conflicting creation evidence", "", 502)
			return out
		}
		out.Outcome = OutcomeAccepted
		return out
	}
	obs, parseErr := parseObservation(fields)
	if parseErr != nil {
		out.Error = parseErr
		return out
	}
	out.Observation = obs
	out.Outcome = OutcomeAccepted
	return out
}

func parseObservation(fields map[string]json.RawMessage) (*Observation, *Error) {
	var p struct {
		ID            string          `json:"id"`
		Model         string          `json:"model"`
		Status        string          `json:"status"`
		Content       json.RawMessage `json:"content"`
		Usage         json.RawMessage `json:"usage"`
		Error         json.RawMessage `json:"error"`
		Resolution    string          `json:"resolution"`
		Ratio         string          `json:"ratio"`
		Duration      *int64          `json:"duration"`
		Frames        *int64          `json:"frames"`
		FPS           *int64          `json:"framespersecond"`
		Seed          *int64          `json:"seed"`
		CreatedAt     *int64          `json:"created_at"`
		UpdatedAt     *int64          `json:"updated_at"`
		GenerateAudio *bool           `json:"generate_audio"`
	}
	filtered := map[string]json.RawMessage{}
	for _, k := range []string{"id", "model", "status", "content", "usage", "error", "resolution", "ratio", "duration", "frames", "framespersecond", "seed", "created_at", "updated_at", "generate_audio"} {
		if v, ok := fields[k]; ok {
			filtered[k] = v
		}
	}
	b, _ := json.Marshal(filtered)
	if json.Unmarshal(b, &p) != nil || p.Status == "" {
		return nil, failure("invalid_upstream_response", "upstream task observation is malformed", "", 502)
	}
	o := &Observation{TaskID: p.ID, Model: p.Model, RawStatus: p.Status, Resolution: p.Resolution, Ratio: p.Ratio, Duration: p.Duration, Frames: p.Frames, FramesPerSecond: p.FPS, Seed: p.Seed, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, GenerateAudio: p.GenerateAudio}
	if contains([]string{"queued", "running", "succeeded", "failed", "cancelled", "expired"}, p.Status) {
		o.Status = Status(p.Status)
	}
	if len(p.Usage) > 0 && string(p.Usage) != "null" {
		var usageFields map[string]json.RawMessage
		if json.Unmarshal(p.Usage, &usageFields) != nil || usageFields == nil {
			return nil, failure("invalid_upstream_usage", "upstream usage is malformed", "", 502)
		}
		for _, entry := range []struct {
			name   string
			target **int64
		}{{"completion_tokens", &o.Usage.CompletionTokens}, {"total_tokens", &o.Usage.TotalTokens}} {
			if raw, ok := usageFields[entry.name]; ok {
				if json.Unmarshal(raw, entry.target) != nil || (*entry.target != nil && **entry.target < 0) {
					return nil, failure("invalid_upstream_usage", "upstream token count is invalid", "", 502)
				}
			}
		}
	}
	if len(p.Content) > 0 && string(p.Content) != "null" {
		var content map[string]json.RawMessage
		if json.Unmarshal(p.Content, &content) != nil || content == nil {
			return nil, failure("invalid_upstream_response", "upstream content is malformed", "", 502)
		}
		for _, entry := range []struct {
			name   string
			target *string
		}{{"video_url", &o.VideoURL}, {"last_frame_url", &o.LastFrameURL}} {
			if raw, ok := content[entry.name]; ok && json.Unmarshal(raw, entry.target) != nil {
				return nil, failure("invalid_upstream_response", "upstream artifact reference is malformed", "", 502)
			}
			if *entry.target != "" && !validMediaURL(*entry.target) {
				return nil, failure("invalid_upstream_response", "upstream artifact URL is not HTTPS", "", 502)
			}
		}
	}
	for _, n := range []*int64{o.Duration, o.Frames, o.FramesPerSecond, o.CreatedAt, o.UpdatedAt} {
		if n != nil && *n < 0 {
			return nil, failure("invalid_upstream_response", "upstream numeric metadata is invalid", "", 502)
		}
	}
	o.HasError = len(p.Error) > 0 && string(p.Error) != "null" && string(p.Error) != "{}"
	return o, nil
}
func containsStatus(values []int, n int) bool {
	for _, v := range values {
		if n == v {
			return true
		}
	}
	return false
}
func safeRequestID(s string) string {
	if len(s) > 256 || !upstreamID.MatchString(s) {
		return ""
	}
	return s
}
