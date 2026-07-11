package relay

// AWS Signature Version 4 签名（丝滑度批 C，AWS Bedrock 用）。纯 stdlib 手写——**刻意不引
// aws-sdk-go-v2**（几十个传递依赖），与 kling JWT/vidu 同哲学：签名是确定性标准算法，
// 手写 ~100 行 + AWS 官方测试向量单测即可保证正确，比拖入巨型 SDK 划算。
//
// 算法（AWS 文档 sigv4）：规范请求 → 待签字符串 → 派生签名密钥链 → HMAC 签名 → Authorization 头。
// 就地在 *http.Request 上设置 Authorization / X-Amz-Date /（X-Amz-Security-Token）。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// sigV4Sign：对已构造好 URL/Method/Content-Type、payload 已定的请求做 SigV4 签名。
// service 如 "bedrock"；region 如 "us-east-1"；sessionToken 可空（临时凭证才有）。
func sigV4Sign(req *http.Request, service, region, accessKey, secretKey, sessionToken string, payload []byte, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	if sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", sessionToken)
	}

	payloadHash := hexSHA256(payload)

	// 收集要签的头：host + x-amz-date（+ content-type + security-token）。
	headerVals := map[string]string{
		"host":       req.URL.Host,
		"x-amz-date": amzDate,
	}
	names := []string{"host", "x-amz-date"}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		headerVals["content-type"] = ct
		names = append(names, "content-type")
	}
	if sessionToken != "" {
		headerVals["x-amz-security-token"] = sessionToken
		names = append(names, "x-amz-security-token")
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n)
		ch.WriteByte(':')
		ch.WriteString(strings.TrimSpace(headerVals[n]))
		ch.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	// 规范请求：method\nURI\nquery\nheaders\nsignedHeaders\npayloadHash
	// canonical URI 必须用 **AWS UriEncode 规则**（保留 /，unreserved 不编码，其余 %XX），
	// 不能用 Go 的 EscapedPath()——后者保留冒号裸奔，而 AWS 验签时把冒号编码为 %3A
	// （Bedrock modelId 如 anthropic.claude...:0 含冒号）。不一致 = 403 SignatureDoesNotMatch。
	canonicalURI := awsURIEncodePath(req.URL.Path)
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalRequest := strings.Join([]string{
		req.Method, canonicalURI, canonicalQueryString(req.URL.RawQuery),
		ch.String(), signedHeaders, payloadHash,
	}, "\n")

	// 待签字符串
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	// 派生签名密钥链 kDate→kRegion→kService→kSigning，再签
	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
}

// canonicalQueryString：查询参数按 key 排序、各自 URI-encode（bedrock invoke/converse 无 query，
// 但保持通用正确）。空串直接返回空。
func canonicalQueryString(raw string) string {
	if raw == "" {
		return ""
	}
	pairs := strings.Split(raw, "&")
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// awsURIEncodePath：AWS SigV4 canonical URI 的 path 编码——保留 '/'，unreserved 字符
// （A-Za-z0-9-_.~）不编码，其余按 %XX（大写十六进制）。冒号 ':' 非 unreserved → %3A
// （Bedrock modelId 的 :0 后缀必须这样编码，否则验签失败）。
func awsURIEncodePath(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c == '/' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			const hex = "0123456789ABCDEF"
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
