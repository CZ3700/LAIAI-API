package forward

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	AuthBearer      = "bearer"
	AuthToken       = "token"
	AuthXAPIKey     = "x-api-key"
	AuthXGoogAPIKey = "x-goog-api-key"
	AuthQueryKey    = "query_key"
	AuthKey         = "key"
	AuthJimeng      = "jimeng"
	AuthTencentVOD  = "tencent_vod"
	AuthTencentMPS  = "tencent_mps"
	AuthVolcTTS     = "volcengine_tts"
)

// ValidateAuthType 校验转发鉴权方式是否在受支持的白名单内（空串表示使用协议默认鉴权）。
func ValidateAuthType(authType string) error {
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "", AuthBearer, AuthToken, AuthXAPIKey, AuthXGoogAPIKey, AuthQueryKey, AuthKey,
		AuthJimeng, AuthTencentVOD, AuthTencentMPS, AuthVolcTTS:
		return nil
	default:
		return fmt.Errorf("unsupported auth_type %q", authType)
	}
}

// ValidatePathTemplate 校验规则中的路径模板，阻断绝对 URL、协议相对路径（//evil）与目录穿越（..）。
func ValidatePathTemplate(raw string) error {
	p := strings.TrimSpace(raw)
	if p == "" {
		return nil
	}
	if strings.Contains(p, "://") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) {
		return fmt.Errorf("path template %q must be a relative HTTP path, not a full URL", raw)
	}
	pathPart, _, _ := strings.Cut(p, "?")
	for seg := range strings.SplitSeq(pathPart, "/") {
		if seg == ".." {
			return fmt.Errorf("path template %q must not contain '..' traversal segments", raw)
		}
	}
	return nil
}

// JoinUpstreamURL 安全拼接渠道 baseURL 与规则相对路径 upstreamPath：
// 1. 拒绝携带 scheme 或 // 的外部主机覆盖（防配置侧 SSRF 逃逸）；
// 2. 自动消除重复版本前缀（如 baseURL 含 /api/v3 且 upstreamPath 也以 /api/v3/ 开头）；
// 3. 保留 upstreamPath 中合法的 query 参数。
func JoinUpstreamURL(baseURL, upstreamPath string) (*url.URL, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, errors.New("empty channel base_url")
	}
	parsedBase, err := url.Parse(base)
	if err != nil || parsedBase.Scheme == "" || parsedBase.Host == "" {
		return nil, fmt.Errorf("invalid channel base_url %q", baseURL)
	}

	up := strings.TrimSpace(upstreamPath)
	if up == "" {
		return parsedBase, nil
	}
	if err := ValidatePathTemplate(up); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(up, "/") {
		up = "/" + up
	}

	upPath, upQuery, hasQuery := strings.Cut(up, "?")
	cleanUpPath := path.Clean(upPath)
	if !strings.HasPrefix(cleanUpPath, "/") {
		cleanUpPath = "/" + cleanUpPath
	}

	basePath := strings.TrimRight(parsedBase.Path, "/")
	finalPath := cleanUpPath
	if basePath != "" && cleanUpPath != basePath && !strings.HasPrefix(cleanUpPath, basePath+"/") {
		finalPath = basePath + cleanUpPath
	}

	out := *parsedBase
	out.Path = finalPath
	out.RawPath = ""
	if hasQuery {
		if out.RawQuery != "" {
			out.RawQuery = out.RawQuery + "&" + upQuery
		} else {
			out.RawQuery = upQuery
		}
	}
	return &out, nil
}

// ApplyAuth 将解密后的渠道凭证按指定鉴权策略注入上游请求，避免业务包重复硬编码鉴权分支。
// 对于需要请求体参与 HMAC 签名的协议（jimeng / tencent_vod / tencent_mps），若调用方已完成签名则保留原头。
func ApplyAuth(req *http.Request, authType, credential string) {
	if req == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case AuthToken:
		tok := strings.TrimSpace(credential)
		tok = strings.TrimPrefix(tok, "Token ")
		tok = strings.TrimPrefix(tok, "Bearer ")
		req.Header.Set("Authorization", "Token "+tok)
	case AuthXAPIKey:
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", credential)
	case AuthVolcTTS:
		req.Header.Del("Authorization")
		req.Header.Set("X-Api-Key", credential)
	case AuthXGoogAPIKey:
		req.Header.Del("Authorization")
		req.Header.Set("x-goog-api-key", credential)
	case AuthQueryKey:
		req.Header.Del("Authorization")
		q := req.URL.Query()
		q.Set("key", credential)
		req.URL.RawQuery = q.Encode()
	case AuthKey:
		tok := strings.TrimPrefix(strings.TrimSpace(credential), "Key ")
		req.Header.Set("Authorization", "Key "+tok)
	case AuthJimeng, AuthTencentVOD, AuthTencentMPS:
		// 签名类鉴权已在适配器构造完整 body 后写入 Authorization/X-Date/X-TC-*，此处绝不覆盖为 Bearer。
		return
	default:
		existing := req.Header.Get("Authorization")
		if strings.HasPrefix(existing, "HMAC-SHA256 ") || strings.HasPrefix(existing, "TC3-HMAC-SHA256 ") {
			return
		}
		if strings.HasPrefix(existing, "Bearer ") && (strings.Contains(credential, ":") || strings.Contains(credential, "|")) {
			return
		}
		tok := strings.TrimPrefix(strings.TrimSpace(credential), "Bearer ")
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}
