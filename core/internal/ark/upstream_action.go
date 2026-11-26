package ark

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/forward"
)

// CallUpstreamActionRaw 通过上游渠道 Bearer Key 调用火山 Action 协议（如 /?Action=CreateAsset&Version=2024-01-01），
// 并在遇到 429 或流控错误时自动走自适应并发限流与抖动退避。
func CallUpstreamActionRaw(
	ctx context.Context,
	httpClient *http.Client,
	proxyBase string,
	bearerKey string,
	action string,
	version string,
	body any,
	rateRetryOverride time.Duration,
) ([]byte, int, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	ver := cmp.Or(strings.TrimSpace(version), DefaultArkAPIVersion)
	trimmedBase := strings.TrimRight(strings.TrimSpace(proxyBase), "/")
	if trimmedBase == "" {
		return nil, 0, fmt.Errorf("upstream asset base url is empty")
	}
	u, err := url.Parse(trimmedBase)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, 0, fmt.Errorf("invalid upstream asset base url %q", proxyBase)
	}
	q := u.Query()
	q.Set("Action", action)
	q.Set("Version", ver)
	u.RawQuery = q.Encode()

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	scope := "uar:" + trimmedBase
	cl := &Client{http: httpClient, rateRetryScale: rateRetryOverride}

	var status int
	var respBytes []byte
	for attempt := 0; attempt <= MaxRateLimitRetries; attempt++ {
		release, perr := AcquireRateLimitPermitIfActive(ctx, scope, action)
		if perr != nil {
			return nil, 0, perr
		}
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
		if rerr != nil {
			release()
			return nil, 0, rerr
		}
		req.Header.Set("Content-Type", "application/json")
		forward.ApplyAuth(req, forward.AuthBearer, bearerKey)

		resp, derr := httpClient.Do(req)
		if derr != nil {
			release()
			return nil, 0, derr
		}
		respBytes, rerr = io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
		_ = resp.Body.Close()
		status = resp.StatusCode
		release()
		if rerr != nil {
			return nil, status, rerr
		}
		if (status < 200 || status >= 300) && attempt < MaxRateLimitRetries && IsAPIRateLimited(status, string(respBytes)) {
			ActivateRateLimitThrottle(scope, action)
			if jerr := cl.sleepRateLimitJitter(ctx, attempt+1); jerr != nil {
				return respBytes, status, jerr
			}
			continue
		}
		break
	}
	return respBytes, status, nil
}

func callUpstreamAssetAction(
	ctx context.Context,
	httpClient *http.Client,
	proxyBase string,
	bearerKey string,
	action string,
	body any,
) (map[string]any, error) {
	raw, status, err := CallUpstreamActionRaw(ctx, httpClient, proxyBase, bearerKey, action, DefaultArkAPIVersion, body, 10*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("upstream %s HTTP %d: %s", action, status, strings.TrimSpace(string(raw)))
	}
	var parsed map[string]any
	if uerr := json.Unmarshal(raw, &parsed); uerr != nil {
		return nil, fmt.Errorf("decode %s JSON: %w", action, uerr)
	}
	if meta, ok := parsed["ResponseMetadata"].(map[string]any); ok {
		if errObj, ok := meta["Error"].(map[string]any); ok && len(errObj) > 0 {
			return nil, fmt.Errorf("%s failed (%v): %v", action, errObj["Code"], errObj["Message"])
		}
	}
	return parsed, nil
}

// ExtractResultField 从 `{ "Result": { ... } }` 或顶层 JSON 提取字符串字段。
func ExtractResultField(res map[string]any, field string) string {
	return extractResultField(res, field)
}

func extractResultField(res map[string]any, field string) string {
	if res == nil {
		return ""
	}
	if r, ok := res["Result"].(map[string]any); ok {
		if s, ok := r[field].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if s, ok := res[field].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return ""
}

func extractResultID(res map[string]any) string {
	if s := extractResultField(res, "Id"); s != "" {
		return s
	}
	return extractResultField(res, "id")
}
