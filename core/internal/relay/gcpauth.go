package relay

// GCP service account → OAuth2 access token（丝滑度批 C，Google Vertex AI 用）。纯 stdlib
// 手写——与 sigv4/kling JWT 同哲学：**刻意不引 google.golang.org/api 或 oauth2 巨型依赖**。
//
// 流程（Google 服务账号 JWT-bearer 授权，标准算法）：
//   SA JSON（client_email + RSA private_key）→ 签一个 RS256 JWT assertion
//   → POST token_uri（grant_type=jwt-bearer）→ 拿短期 access_token（默认 1h）
//   → Bearer 调 Vertex。access_token 按 SA 缓存，提前 60s 过期，避免每请求换 token。
//
// 安全：private_key 是敏感凭证——只在内存签名，绝不入日志/错误消息；缓存 key 用
// client_email+project（非 secret）；缓存的 access_token 短期、仅内存（与其它 provider 凭证同）。

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const gcpDefaultTokenURI = "https://oauth2.googleapis.com/token"
const gcpScope = "https://www.googleapis.com/auth/cloud-platform"

// gcpServiceAccount：SA JSON 的子集（下载的服务账号密钥文件）。
type gcpServiceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"` // PEM（PKCS#8 或 PKCS#1）
	TokenURI    string `json:"token_uri"`
}

// parseGCPServiceAccount 解析 + 校验 SA JSON。缺 token_uri 时补官方默认。
func parseGCPServiceAccount(saJSON string) (*gcpServiceAccount, error) {
	var sa gcpServiceAccount
	if err := json.Unmarshal([]byte(saJSON), &sa); err != nil {
		return nil, fmt.Errorf("vertex: service account 不是合法 JSON: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" || sa.ProjectID == "" {
		return nil, fmt.Errorf("vertex: service account 缺 client_email/private_key/project_id")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = gcpDefaultTokenURI
	}
	return &sa, nil
}

type gcpCachedToken struct {
	token  string
	expiry time.Time
}

var (
	gcpTokenMu    sync.Mutex
	gcpTokenCache = map[string]gcpCachedToken{}
	// token_uri 由 admin 凭证提供，信任级别与渠道 base_url 同类，走裸 client。
	// ⚠️ 若未来把 vertex 加进 BYOK 白名单（byok.go），此处必须换 NewGuardedClient
	// 并在 parseGCPServiceAccount 校验 token_uri 的 scheme/host——否则租户可借 token_uri SSRF。
	gcpTokenHTTPClient = NewUpstreamClient(15 * time.Second)
)

// gcpAccessToken 用 SA 换 access token（带缓存）。now 便于测试注入；生产传 time.Now()。
func gcpAccessToken(ctx context.Context, sa *gcpServiceAccount, now time.Time) (string, error) {
	cacheKey := sa.ClientEmail + "|" + sa.ProjectID
	gcpTokenMu.Lock()
	if c, ok := gcpTokenCache[cacheKey]; ok && now.Before(c.expiry) {
		tok := c.token
		gcpTokenMu.Unlock()
		return tok, nil
	}
	gcpTokenMu.Unlock()

	assertion, err := signGCPJWT(sa, now)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sa.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := gcpTokenHTTPClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("vertex: token 换取失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// Google 的错误体不含我方凭证，可安全带出（截断防日志膨胀）
		return "", fmt.Errorf("vertex: token 换取 HTTP %d: %s", resp.StatusCode, gcpTruncate(string(respBody), 200))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("vertex: token 响应无 access_token")
	}
	ttl := tr.ExpiresIn
	if ttl <= 60 { // ≤60 会因提前 60s 过期缓存出生即死；Google 恒回 3600，异常值按默认处理
		ttl = 3600
	}
	gcpTokenMu.Lock()
	gcpTokenCache[cacheKey] = gcpCachedToken{
		token:  tr.AccessToken,
		expiry: now.Add(time.Duration(ttl-60) * time.Second), // 提前 60s 过期，留时钟裕度
	}
	gcpTokenMu.Unlock()
	return tr.AccessToken, nil
}

// signGCPJWT 构造并 RS256 签名 JWT assertion（iss=SA 邮箱，scope=cloud-platform，aud=token_uri）。
func signGCPJWT(sa *gcpServiceAccount, now time.Time) (string, error) {
	headerSeg := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]any{
		"iss":   sa.ClientEmail,
		"scope": gcpScope,
		"aud":   sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := headerSeg + "." + base64.RawURLEncoding.EncodeToString(cb)

	priv, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, h[:])
	if err != nil {
		return "", fmt.Errorf("vertex: JWT 签名失败: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseRSAPrivateKey 解析 PEM RSA 私钥（SA 用 PKCS#8，兼容回退 PKCS#1）。
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("vertex: private_key 非 PEM 格式")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("vertex: private_key 非 RSA")
		}
		return rk, nil
	}
	if rk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return rk, nil
	}
	return nil, fmt.Errorf("vertex: private_key 无法解析（既非 PKCS#8 也非 PKCS#1）")
}

func gcpTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
