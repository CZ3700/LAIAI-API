package videoasset

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const managementVersion = "2024-01-01"
const managementHost = "ark.cn-beijing.volcengineapi.com"

var identifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func validID(v string) bool { return len(v) <= 64 && identifier.MatchString(v) }
func validScope(s Scope) bool {
	return validID(s.ID) && s.Version > 0 && validID(s.AccountID) && s.Region == "cn-beijing" && validID(s.Project)
}
func hash(v []byte) string { x := sha256.Sum256(v); return hex.EncodeToString(x[:]) }
func mac(key []byte, text string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(text))
	return h.Sum(nil)
}

// Result is an internal transport result. Body/Result may contain short-lived
// portrait secrets: neither belongs in logs or a generic JSON HTTP response.
type Result struct {
	Outcome                  string
	HTTPStatus               int
	RequestID, SafeErrorCode string
	Body                     []byte          `json:"-"`
	Data                     json.RawMessage `json:"-"`
}

func (r Result) LogValue() slog.Value {
	return slog.GroupValue(slog.String("outcome", r.Outcome), slog.Int("http_status", r.HTTPStatus), slog.String("request_id", r.RequestID), slog.String("safe_error_code", r.SafeErrorCode))
}

type ManagementClient struct{ http *http.Client }

func NewManagementClient(client *http.Client) *ManagementClient {
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	if copy.Timeout <= 0 || copy.Timeout > 30*time.Second {
		copy.Timeout = 30 * time.Second
	}
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ManagementClient{http: &copy}
}
func allowedAction(a string) bool {
	switch a {
	case "ListAssetGroups", "ListAssets", "UpdateAssetGroup", "UpdateAsset", "DeleteAsset", "CreateAssetGroup", "GetAssetGroup", "DeleteAssetGroup", "CreateAsset", "GetAsset", "CreateVisualValidateSession", "GetVisualValidateResult":
		return true
	}
	return false
}

// Signing follows the official volcengine/volc-sdk-golang base/sign.go V4
// algorithm (not AWS4: no AWS4 key prefix and terminator is "request").
func signedRequest(ctx context.Context, scope Scope, credentials Credentials, action string, body []byte, now time.Time) (*http.Request, error) {
	if !validScope(scope) || !allowedAction(action) || !identifier.MatchString(credentials.AccessKey) || len(credentials.SecretKey) < 1 ||
		strings.ContainsAny(credentials.SecretKey+credentials.SessionToken, "\r\n") || len(body) > 2<<20 || !json.Valid(body) {
		return nil, ErrInvalid
	}
	q := url.Values{"Action": {action}, "Version": {managementVersion}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+managementHost+"/?"+q, bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	// Disallow automatic replay even if a future caller changes the transport.
	req.GetBody = nil
	date := now.UTC().Format("20060102T150405Z")
	day := date[:8]
	payloadHash := hash(body)
	headers := []string{"content-type", "host", "x-content-sha256", "x-date"}
	canonicalHeaders := "content-type:application/json\nhost:" + managementHost + "\nx-content-sha256:" + payloadHash + "\nx-date:" + date + "\n"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Date", date)
	req.Header.Set("X-Content-Sha256", payloadHash)
	if credentials.SessionToken != "" {
		headers = append(headers, "x-security-token")
		canonicalHeaders += "x-security-token:" + strings.TrimSpace(credentials.SessionToken) + "\n"
		req.Header.Set("X-Security-Token", credentials.SessionToken)
	}
	signed := strings.Join(headers, ";")
	canonical := strings.Join([]string{http.MethodPost, "/", q, canonicalHeaders, signed, payloadHash}, "\n")
	credentialScope := day + "/" + scope.Region + "/ark/request"
	key := mac([]byte(credentials.SecretKey), day)
	key = mac(key, scope.Region)
	key = mac(key, "ark")
	key = mac(key, "request")
	defer clear(key)
	signature := hex.EncodeToString(mac(key, "HMAC-SHA256\n"+date+"\n"+credentialScope+"\n"+hash([]byte(canonical))))
	req.Header.Set("Authorization", "HMAC-SHA256 Credential="+credentials.AccessKey+"/"+credentialScope+", SignedHeaders="+signed+", Signature="+signature)
	return req, nil
}

func (c *ManagementClient) Call(ctx context.Context, scope Scope, credentials Credentials, action string, body []byte, now time.Time) Result {
	result := Result{Outcome: "not_sent", SafeErrorCode: "invalid_provider_request"}
	req, err := signedRequest(ctx, scope, credentials, action, body, now)
	if err != nil {
		return result
	}
	if ctx.Err() != nil {
		result.SafeErrorCode = "request_cancelled"
		return result
	}
	result.Outcome = "unknown"
	result.SafeErrorCode = "provider_unavailable"
	response, err := c.http.Do(req)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		result.SafeErrorCode = "provider_response_invalid"
		return result
	}
	result.Body = raw
	var envelope struct {
		ResponseMetadata struct {
			RequestID                        string `json:"RequestId"`
			Action, Version, Service, Region string
			Error                            *struct{ Code string }
		}
		Result json.RawMessage
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return result
	}
	if identifier.MatchString(envelope.ResponseMetadata.RequestID) {
		result.RequestID = envelope.ResponseMetadata.RequestID
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 && envelope.ResponseMetadata.Error != nil {
		result.Outcome = "rejected"
		result.SafeErrorCode = "provider_rejected"
		return result
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.ResponseMetadata.Error != nil ||
		envelope.ResponseMetadata.Action != action || envelope.ResponseMetadata.Version != managementVersion || envelope.ResponseMetadata.Service != "ark" || envelope.ResponseMetadata.Region != scope.Region ||
		result.RequestID == "" || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return result
	}
	result.Outcome = "accepted"
	result.SafeErrorCode = ""
	result.Data = envelope.Result
	return result
}
