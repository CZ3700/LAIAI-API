// Package totp：RFC 6238 TOTP（HMAC-SHA1，30s 步长，6 位），不引外部库。
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	stepSeconds = 30
	digits      = 6
)

// GenerateSecret：20 字节随机 → base32（无填充），作为 TOTP 共享密钥。
func GenerateSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
}

// ProvisioningURI：otpauth:// URI，前端转二维码给认证器扫。
func ProvisioningURI(secret, account, issuer string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(digits))
	q.Set("period", fmt.Sprint(stepSeconds))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Validate：校验 code 是否匹配（含 ±1 个时间窗的容差，抗时钟漂移）。
func Validate(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != digits {
		return false
	}
	counter := now.Unix() / stepSeconds
	for _, off := range []int64{0, -1, 1} {
		if generate(secret, counter+off) == code {
			return true
		}
	}
	return false
}

func generate(secret string, counter int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	val := (binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%06d", val)
}

// CurrentCode：当前时间窗的有效码（测试/展示用）。
func CurrentCode(secret string) string {
	return generate(secret, timeNow().Unix()/stepSeconds)
}

var timeNow = time.Now
