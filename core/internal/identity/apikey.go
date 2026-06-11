package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// API Key 设计（红线：明文任何层永不落库，只存 SHA-256 指纹；明文仅签发时返回一次）。
const keyPrefix = "sk-laiai-"

var (
	ErrKeyInvalid = errors.New("identity: api key invalid")
	ErrKeyExpired = errors.New("identity: api key expired")
	ErrKeyRevoked = errors.New("identity: api key revoked")
)

type APIKey struct {
	ID        uint64     `gorm:"primaryKey"`
	OrgID     uint64     `gorm:"column:org_id"`
	UserID    *uint64    `gorm:"column:user_id"`
	Name      string     `gorm:"column:name"`
	Prefix    string     `gorm:"column:prefix"`
	KeyHash   string     `gorm:"column:key_hash"`
	Scopes    string     `gorm:"column:scopes"`
	Status    int8       `gorm:"column:status"`
	ExpiresAt *time.Time `gorm:"column:expires_at"`
}

func (APIKey) TableName() string { return "api_keys" }

type IssueKeyInput struct {
	OrgID     uint64
	UserID    *uint64 // per-member key；org 级 key 传 nil
	Name      string
	Scopes    string
	ExpiresAt *time.Time
}

// IssueKey 签发新 Key。返回 (明文, 记录)；明文此后不可再取。
func IssueKey(tx *gorm.DB, in IssueKeyInput) (string, *APIKey, error) {
	if in.OrgID == 0 {
		return "", nil, ErrInvalidInput
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext := keyPrefix + base64.RawURLEncoding.EncodeToString(raw) // 9 + 43 字符
	sum := sha256.Sum256([]byte(plaintext))

	k := &APIKey{
		OrgID:     in.OrgID,
		UserID:    in.UserID,
		Name:      in.Name,
		Prefix:    plaintext[:len(keyPrefix)+7], // 展示用 "sk-laiai-Ab3dEf9"
		KeyHash:   hex.EncodeToString(sum[:]),
		Scopes:    in.Scopes,
		Status:    StatusActive,
		ExpiresAt: in.ExpiresAt,
	}
	if err := tx.Create(k).Error; err != nil {
		return "", nil, err
	}
	return plaintext, k, nil
}

// VerifyKey 校验明文 Key，返回有效记录。无效/吊销/过期分别返回对应错误。
func VerifyKey(db *gorm.DB, plaintext string, now time.Time) (*APIKey, error) {
	if !strings.HasPrefix(plaintext, keyPrefix) {
		return nil, ErrKeyInvalid
	}
	sum := sha256.Sum256([]byte(plaintext))
	k := &APIKey{}
	if err := db.Where("key_hash = ?", hex.EncodeToString(sum[:])).First(k).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKeyInvalid
		}
		return nil, err
	}
	if k.Status != StatusActive {
		return nil, ErrKeyRevoked
	}
	if k.ExpiresAt != nil && now.After(*k.ExpiresAt) {
		return nil, ErrKeyExpired
	}
	return k, nil
}

// RevokeKey 吊销（幂等：重复吊销无害）。
func RevokeKey(tx *gorm.DB, keyID uint64) error {
	return tx.Model(&APIKey{}).Where("id = ?", keyID).Update("status", StatusDisabled).Error
}
