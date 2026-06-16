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
//
// 严格遵循 Google Authenticator Key URI Format：
//   - label = issuer ":" account，issuer 与 account **各自**百分号编码、中间一个**字面量**冒号；
//   - issuer 在 label 前缀与 issuer query 参数必须相等且同款编码。
//
// 修复（用户报 QR 错）：旧实现 `url.PathEscape(issuer+":"+account)` 先拼后转——issuer 内部冒号被当
// 分隔符切错、且 PathEscape 不转 ':'；而 url.Values.Encode() 把空格编成 '+'、label 里却是 '%20'，
// 二者不一致 → 多词品牌名(如 "LAIAI Next")或含冒号时验证器解析出错/扫不上。
// 这里用 otpEscape（QueryEscape 后把 '+' 还原为 '%20'）对每个组件单独编码，空格恒 %20、冒号恒 %3A。
func ProvisioningURI(secret, account, issuer string) string {
	label := otpEscape(issuer) + ":" + otpEscape(account)
	q := "secret=" + otpEscape(secret) +
		"&issuer=" + otpEscape(issuer) +
		"&algorithm=SHA1" +
		"&digits=" + fmt.Sprint(digits) +
		"&period=" + fmt.Sprint(stepSeconds)
	return "otpauth://totp/" + label + "?" + q
}

// otpEscape：otpauth 组件级百分号编码。QueryEscape 把空格编成 '+'（部分验证器不还原），统一还原为 '%20'；
// 冒号/at 等都被正确转义（issuer 内部冒号→%3A，不与 label 分隔符混淆；account 的 @ → %40）。
func otpEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
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
