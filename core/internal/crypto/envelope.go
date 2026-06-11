// Package crypto：信封加密密钥环（设计 §5.5）。
// 密文格式 enc:v1:<key_id>:<base64url(nonce||ciphertext)> —— key_id 编进密文头，
// 轮换 = 加新 key + 切 active，旧密文用旧 key 解、新写入用新 key，无顺序依赖。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const prefix = "enc:v1:"

var (
	ErrNoActiveKey  = errors.New("crypto: keyring has no active key")
	ErrUnknownKeyID = errors.New("crypto: ciphertext references unknown key_id")
	ErrMalformed    = errors.New("crypto: malformed ciphertext")
)

type Keyring struct {
	active string
	keys   map[string][]byte // key_id -> 32 字节 AES-256 key
}

// NewKeyring 从 spec 构建。spec 形如 "k1:<base64 32B>,k2:<base64 32B>"，active 必须在其中。
func NewKeyring(spec, active string) (*Keyring, error) {
	keys := map[string][]byte{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, b64, ok := strings.Cut(part, ":")
		if !ok || id == "" {
			return nil, fmt.Errorf("crypto: malformed key spec entry %q", part)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("crypto: key %q must be base64 of exactly 32 bytes", id)
		}
		keys[id] = raw
	}
	if len(keys) == 0 {
		return nil, errors.New("crypto: empty keyring spec")
	}
	if _, ok := keys[active]; !ok {
		return nil, fmt.Errorf("crypto: active key %q not present in keyring", active)
	}
	return &Keyring{active: active, keys: keys}, nil
}

func (k *Keyring) Encrypt(plaintext string) (string, error) {
	key, ok := k.keys[k.active]
	if !ok {
		return "", ErrNoActiveKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + k.active + ":" + base64.RawURLEncoding.EncodeToString(ct), nil
}

func (k *Keyring) Decrypt(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, prefix) {
		return "", ErrMalformed
	}
	rest := strings.TrimPrefix(ciphertext, prefix)
	keyID, b64, ok := strings.Cut(rest, ":")
	if !ok {
		return "", ErrMalformed
	}
	key, exists := k.keys[keyID]
	if !exists {
		return "", fmt.Errorf("%w: %q", ErrUnknownKeyID, keyID)
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return "", ErrMalformed
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", ErrMalformed
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decrypt failed (wrong key or corrupted): %w", err)
	}
	return string(plain), nil
}
