package relay

import (
	"encoding/json"
	"strings"
)

// 上游错误分类（知识库 §2.1，提炼自 NewAPI service/channel.go:47-102 的生产规则）。
// 决定：① 是否禁用渠道（鉴权失效/配额耗尽 = 渠道坏了，禁用）；
//        ② 是否换渠道重试（M2 接重试循环时用）。
type upstreamDisposition struct {
	DisableChannel bool   // 该渠道应自动禁用（凭证/配额类硬故障）
	Retryable      bool   // 可换渠道重试（M2）
	Reason         string // 禁用/分类原因，入 audit
}

// classifyUpstreamError 按 HTTP 状态码 + 上游错误体判定处置。
func classifyUpstreamError(provider string, statusCode int, body []byte) upstreamDisposition {
	d := upstreamDisposition{}

	// 解析上游错误体的 type/code（OpenAI/Claude 形态）
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	errType := strings.ToLower(parsed.Error.Type)
	errCode := strings.ToLower(toStr(parsed.Error.Code))
	msg := strings.ToLower(parsed.Error.Message)

	// —— 鉴权/配额硬故障 → 禁用渠道（知识库 §2.1）——
	disableTypes := map[string]bool{
		"insufficient_quota": true, "insufficient_user_quota": true,
		"authentication_error": true, "permission_error": true, "forbidden": true,
	}
	disableCodes := map[string]bool{
		"invalid_api_key": true, "account_deactivated": true, "billing_not_active": true,
	}
	switch {
	case disableTypes[errType]:
		d.DisableChannel, d.Reason = true, "upstream error type: "+errType
	case disableCodes[errCode]:
		d.DisableChannel, d.Reason = true, "upstream error code: "+errCode
	case statusCode == 401:
		d.DisableChannel, d.Reason = true, "upstream 401 unauthorized"
	case statusCode == 403 && provider == "gemini":
		d.DisableChannel, d.Reason = true, "gemini 403 forbidden"
	case strings.Contains(msg, "quota") && strings.Contains(msg, "exceed"):
		d.DisableChannel, d.Reason = true, "quota exceeded (message match)"
	}

	// —— 可重试性（知识库 §2.1 ShouldRetry）——
	// 504/524 网关超时、400/408 永不重试；其余 5xx 与 429 可换渠道重试。
	switch {
	case statusCode == 504 || statusCode == 524 || statusCode == 400 || statusCode == 408:
		d.Retryable = false
	case statusCode == 429 || (statusCode >= 500 && statusCode < 600):
		d.Retryable = true
	case d.DisableChannel:
		d.Retryable = true // 渠道坏了，换一个
	}
	return d
}

func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strings.TrimSuffix(strings.TrimRight(jsonNumber(x), "0"), ".")
	default:
		return ""
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
