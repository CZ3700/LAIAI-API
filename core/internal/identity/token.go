package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Session token：HMAC-SHA256 签名的紧凑令牌（沿用旧平台 ExtToken 思路，格式简化）。
// 形态: lt.<base64url(claims json)>.<base64url(hmac)>
var (
	ErrTokenInvalid = errors.New("identity: session token invalid")
	ErrTokenExpired = errors.New("identity: session token expired")
)

type SessionClaims struct {
	UserID uint64 `json:"uid"`
	OrgID  uint64 `json:"org"`
	Exp    int64  `json:"exp"` // unix 秒
	JTI    string `json:"jti"` // 吊销锚点（M1 接 Redis 黑名单）
}

func IssueSessionToken(secret []byte, userID, orgID uint64, ttl time.Duration, now time.Time) (string, SessionClaims, error) {
	jti := make([]byte, 8)
	if _, err := rand.Read(jti); err != nil {
		return "", SessionClaims{}, err
	}
	claims := SessionClaims{
		UserID: userID,
		OrgID:  orgID,
		Exp:    now.Add(ttl).Unix(),
		JTI:    hex.EncodeToString(jti),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", SessionClaims{}, err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return "lt." + body + "." + sign(secret, body), claims, nil
}

func VerifySessionToken(secret []byte, token string, now time.Time) (*SessionClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "lt" {
		return nil, ErrTokenInvalid
	}
	if !hmac.Equal([]byte(sign(secret, parts[1])), []byte(parts[2])) {
		return nil, ErrTokenInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrTokenInvalid
	}
	claims := &SessionClaims{}
	if err := json.Unmarshal(raw, claims); err != nil {
		return nil, ErrTokenInvalid
	}
	if now.Unix() >= claims.Exp {
		return nil, ErrTokenExpired
	}
	return claims, nil
}

func sign(secret []byte, body string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
