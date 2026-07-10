package relay

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
)

// 上游错误分类（知识库 §2.1，提炼自 NewAPI service/channel.go:47-102 的生产规则）。
// 决定：① 是否禁用渠道（鉴权失效/配额耗尽 = 渠道坏了，禁用）；
//
//	② 是否换渠道重试（M2 接重试循环时用）。
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
	case statusCode == 504 || statusCode == 524:
		d.Retryable = false // 真网关超时：换渠道大概率也超时，重试无意义
	case (statusCode == 400 || statusCode == 408) && !d.DisableChannel:
		d.Retryable = false // 纯客户端 400/408：请求本身的问题，换渠道无意义
	case d.DisableChannel || statusCode == 429 || (statusCode >= 500 && statusCode < 600):
		d.Retryable = true // 渠道坏了（含 400+认证类禁用）/限流/5xx：换一个健康渠道重试
	}
	return d
}

// upstreamErrorCode：排障闭环（0045）的上游错误码提取——**只**取上游错误体的 code/type 枚举
// 字段（context_length_exceeded / rate_limit_error 等），字符白名单过滤 + 截断，绝不落自由文本
// message（红线 7/8：message 可能回显 prompt 碎片/org 标识）。两者皆空回退分类 reason（合成串，安全）。
func upstreamErrorCode(dispReason string, body []byte) string {
	var parsed struct {
		Error struct {
			Type string `json:"type"`
			Code any    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	code := sanitizeErrCode(toStr(parsed.Error.Code))
	if code == "" {
		code = sanitizeErrCode(parsed.Error.Type)
	}
	if code == "" {
		// dispReason 是分类器合成串，可能含 provider 名（如 "gemini 403 forbidden"）——该字段对
		// 请求所有者可见（invocations.upstream_error_code），不得泄露渠道 provider（设计上 json:"-"）。
		// 映射为**不含 provider 的通用状态类**（对抗 review），绝不回退原始 dispReason。
		code = genericErrClass(dispReason)
	}
	return code
}

// genericErrClass：把分类器 reason 折叠为不含 provider 名的通用状态类（HTTP 语义粒度）。
func genericErrClass(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "401") || strings.Contains(r, "unauthorized") || strings.Contains(r, "invalid_api_key") || strings.Contains(r, "authentication"):
		return "upstream_auth_error"
	case strings.Contains(r, "403") || strings.Contains(r, "forbidden") || strings.Contains(r, "permission"):
		return "upstream_forbidden"
	case strings.Contains(r, "quota") || strings.Contains(r, "429") || strings.Contains(r, "rate"):
		return "upstream_quota_or_rate"
	case strings.Contains(r, "timeout") || strings.Contains(r, "504") || strings.Contains(r, "524"):
		return "upstream_timeout"
	case reason == "":
		return ""
	default:
		return "upstream_error"
	}
}

// sanitizeErrCode：仅放行枚举样式字符（字母/数字/_/-/.），含其他字符（空格/引号等自由文本征兆）
// 即整体丢弃；截断 64。
func sanitizeErrCode(s string) string {
	if s == "" || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return ""
		}
	}
	return s
}

// ingressProtocolKey / ingressProtocol：入站方言标记（排障闭环 0045）。anthropic/responses
// 入站包装层在调 HandleChatCompletions 前 c.Set；未标记 = 原生 OpenAI 端点。
const ingressProtocolKey = "laiai_ingress_protocol"

func ingressProtocol(c *gin.Context) string {
	if p := c.GetString(ingressProtocolKey); p != "" {
		return p
	}
	return "openai"
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
