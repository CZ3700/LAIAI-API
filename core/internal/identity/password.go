package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id 参数（OWASP 2025 推荐档：m=64MiB, t=3, p=2）。
// 参数编码进 hash 字符串，未来调参不破坏旧 hash 校验。
const (
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

var ErrPasswordMismatch = errors.New("identity: password mismatch")

// HashPassword 生成 PHC 格式编码：$argon2id$v=19$m=...,t=...,p=...$salt$hash
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", errors.New("identity: password must be at least 8 characters")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// dummyHash：固定的合法 argon2id PHC 串（离线用生产同参 m=64MiB,t=3,p=2 生成）。仅供「无有效用户」
// 路径跑等量 argon2，绝不是任何真实凭证。用编译期常量而非运行时计算——避免 rand.Read 等运行时失败
// 导致 dummyHash 为空而静默跳过 argon2，反而重开时延型用户枚举缺口（TestDummyHashRunsArgon2 守住其合法性）。
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$bGFpYWktZml4ZWQtc2FsdA$r30mqdU0mol6lev8kQ4aasgbXc32xafGwTI3TyuJKWY"

// VerifyDummyPassword 对固定 dummy hash 跑一次 argon2 校验（恒不匹配），用于 login 没有真实用户可
// 校验时吃掉等量 CPU，消除「邮箱存在且 active / 不存在 / 被封」之间的响应时延差——防时延型用户枚举。
func VerifyDummyPassword(password string) {
	_ = VerifyPassword(password, dummyHash)
}

// VerifyPassword 按 hash 内编码的参数校验（常数时间比较）。
func VerifyPassword(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("identity: malformed password hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return errors.New("identity: malformed hash version")
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return errors.New("identity: malformed hash params")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return errors.New("identity: malformed hash salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return errors.New("identity: malformed hash digest")
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}
