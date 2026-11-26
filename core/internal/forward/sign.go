package forward

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func hmacSHA256Bytes(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256HexString(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SplitAKSK 解析两段式或三段式密钥（兼容 ':' 与 '|' 分隔符，如 AK:SK、AK|SK、SecretId:SecretKey:SubAppId）。
// 错误返回不暴露原始凭证。
func SplitAKSK(credential string) (ak, sk, extra string, ok bool) {
	trimmed := strings.TrimSpace(credential)
	if trimmed == "" {
		return "", "", "", false
	}
	sep := ":"
	if !strings.Contains(trimmed, ":") && strings.Contains(trimmed, "|") {
		sep = "|"
	}
	first, rest, found := strings.Cut(trimmed, sep)
	if !found {
		return "", "", "", false
	}
	second, third, hasThird := strings.Cut(rest, sep)
	ak = strings.TrimSpace(first)
	sk = strings.TrimSpace(second)
	if hasThird {
		extra = strings.TrimSpace(third)
	}
	if ak == "" || sk == "" {
		return "", "", "", false
	}
	return ak, sk, extra, true
}

// ParseTencentVODCredential 解析腾讯云点播 SecretId:SecretKey[:SubAppId] 凭证。
func ParseTencentVODCredential(credential string) (secretID, secretKey string, subAppID uint64, err error) {
	ak, sk, extra, ok := SplitAKSK(credential)
	if !ok {
		return "", "", 0, errors.New("tencent credential must be SecretId:SecretKey or SecretId:SecretKey:SubAppId")
	}
	var appID uint64
	if extra != "" {
		if parsed, perr := strconv.ParseUint(extra, 10, 64); perr == nil {
			appID = parsed
		}
	}
	return ak, sk, appID, nil
}

// SignVolcengineV4 对齐火山引擎官方 Signature V4（HMAC-SHA256），供即梦 CV / 火山视觉与媒体 API 签名。
func SignVolcengineV4(
	req *http.Request,
	accessKey string,
	secretKey string,
	service string,
	region string,
	payload []byte,
	now time.Time,
) error {
	if req == nil || req.URL == nil {
		return errors.New("volcengine sign: nil request")
	}
	ak := strings.TrimSpace(accessKey)
	sk := strings.TrimSpace(secretKey)
	if ak == "" || sk == "" {
		return errors.New("volcengine sign: missing access_key or secret_key")
	}
	utc := now.UTC()
	xDate := utc.Format("20060102T150405Z")
	dateShort := utc.Format("20060102")
	svc := cmp.Or(strings.TrimSpace(service), "cv")
	reg := cmp.Or(strings.TrimSpace(region), "cn-north-1")
	host := cmp.Or(req.URL.Host, "visual.volcengineapi.com")
	reqPath := cmp.Or(req.URL.Path, "/")
	rawQuery := req.URL.RawQuery

	payloadHash := sha256HexString(payload)
	canonicalHeaders := fmt.Sprintf(
		"content-type:application/json\nhost:%s\nx-content-sha256:%s\nx-date:%s\n",
		host, payloadHash, xDate,
	)
	const signedHeaders = "content-type;host;x-content-sha256;x-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		reqPath,
		rawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	hashedCanonical := sha256HexString([]byte(canonicalRequest))
	credentialScope := fmt.Sprintf("%s/%s/%s/request", dateShort, reg, svc)
	stringToSign := strings.Join([]string{
		"HMAC-SHA256",
		xDate,
		credentialScope,
		hashedCanonical,
	}, "\n")

	kDate := hmacSHA256Bytes([]byte(sk), []byte(dateShort))
	kRegion := hmacSHA256Bytes(kDate, []byte(reg))
	kService := hmacSHA256Bytes(kRegion, []byte(svc))
	kSigning := hmacSHA256Bytes(kService, []byte("request"))
	signature := hex.EncodeToString(hmacSHA256Bytes(kSigning, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		ak, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Date", xDate)
	req.Header.Set("X-Content-Sha256", payloadHash)
	req.Header.Set("Authorization", auth)
	return nil
}

// SignTencentTC3 对齐腾讯云 TC3-HMAC-SHA256 签名（支持 VOD 2018-07-17 与 MPS 2019-06-12）。
func SignTencentTC3(
	req *http.Request,
	secretID string,
	secretKey string,
	service string,
	action string,
	version string,
	region string,
	payload []byte,
	now time.Time,
) error {
	if req == nil || req.URL == nil {
		return errors.New("tencent tc3 sign: nil request")
	}
	sid := strings.TrimSpace(secretID)
	skey := strings.TrimSpace(secretKey)
	if sid == "" || skey == "" {
		return errors.New("tencent tc3 sign: missing secret_id or secret_key")
	}
	svc := cmp.Or(strings.TrimSpace(service), "vod")
	host := cmp.Or(req.URL.Host, svc+".tencentcloudapi.com")
	utc := now.UTC()
	timestamp := utc.Unix()
	date := utc.Format("2006-01-02")

	hashedPayload := sha256HexString(payload)
	canonicalRequest := fmt.Sprintf(
		"POST\n/\n\ncontent-type:application/json\nhost:%s\n\ncontent-type;host\n%s",
		host, hashedPayload,
	)
	credentialScope := fmt.Sprintf("%s/%s/tc3_request", date, svc)
	hashedCanonical := sha256HexString([]byte(canonicalRequest))
	stringToSign := fmt.Sprintf(
		"TC3-HMAC-SHA256\n%d\n%s\n%s",
		timestamp, credentialScope, hashedCanonical,
	)

	secretDate := hmacSHA256Bytes([]byte("TC3"+skey), []byte(date))
	secretService := hmacSHA256Bytes(secretDate, []byte(svc))
	secretSigning := hmacSHA256Bytes(secretService, []byte("tc3_request"))
	signature := hex.EncodeToString(hmacSHA256Bytes(secretSigning, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=content-type;host, Signature=%s",
		sid, credentialScope, signature,
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	if strings.TrimSpace(region) != "" {
		req.Header.Set("X-TC-Region", strings.TrimSpace(region))
	}
	req.Header.Set("Authorization", auth)
	return nil
}

// MintKlingJWT 使用 HS256 签发 30 分钟有效期的可灵 JWT。
func MintKlingJWT(ak, sk string, now time.Time) (string, error) {
	ak = strings.TrimSpace(ak)
	sk = strings.TrimSpace(sk)
	if ak == "" || sk == "" {
		return "", errors.New("kling jwt: empty access_key or secret_key")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claimsBytes, err := json.Marshal(map[string]any{
		"iss": ak,
		"exp": now.Add(30 * time.Minute).Unix(),
		"nbf": now.Add(-5 * time.Second).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal kling jwt claims: %w", err)
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claimsBytes)
	sig := hmacSHA256Bytes([]byte(sk), []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ResolveKlingBearer 自动识别可灵官方 API Key（直传 Bearer）与旧版 AK:SK / AK|SK（自动签发 JWT）。
func ResolveKlingBearer(credential string, now time.Time) (string, error) {
	trimmed := strings.TrimSpace(credential)
	trimmed = strings.TrimPrefix(trimmed, "Bearer ")
	if trimmed == "" {
		return "", errors.New("kling: empty credential")
	}
	if ak, sk, _, ok := SplitAKSK(trimmed); ok {
		return MintKlingJWT(ak, sk, now)
	}
	return trimmed, nil
}
