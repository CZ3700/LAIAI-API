package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/shopspring/decimal"
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
	// 精细管控（0012）
	QuotaLimit  decimal.Decimal `gorm:"column:quota_limit"`  // 累计消费上限 USD，0=无限
	UsedAmount  decimal.Decimal `gorm:"column:used_amount"`  // 已消费 USD
	ModelLimits string          `gorm:"column:model_limits"` // 模型白名单，逗号分隔，空=不限
	AllowIPs    string          `gorm:"column:allow_ips"`    // IP 白名单，逗号/换行分隔，支持 CIDR，空=不限
	RPMLimit    int             `gorm:"column:rpm_limit"`    // key 级每分钟请求上限，0=不限（0041）
}

func (APIKey) TableName() string { return "api_keys" }

// AllowsModel 返回该 Key 是否可调用某模型（空白名单=不限）。
func (k *APIKey) AllowsModel(model string) bool {
	if strings.TrimSpace(k.ModelLimits) == "" {
		return true
	}
	for _, m := range strings.Split(k.ModelLimits, ",") {
		if strings.TrimSpace(m) == model {
			return true
		}
	}
	return false
}

// AllowsIP 返回调用方 IP 是否在白名单内（空=不限）。支持精确 IP 与 CIDR。
func (k *APIKey) AllowsIP(ip string) bool {
	if strings.TrimSpace(k.AllowIPs) == "" {
		return true
	}
	parsed := net.ParseIP(ip)
	for _, entry := range strings.FieldsFunc(k.AllowIPs, func(r rune) bool {
		return r == '\n' || r == ',' || r == ' ' || r == '\r' || r == '\t'
	}) {
		if entry == ip {
			return true
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil && parsed != nil && cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// QuotaExhausted 返回该 Key 是否已用满累计额度（无限额度恒为 false）。
func (k *APIKey) QuotaExhausted() bool {
	return k.QuotaLimit.IsPositive() && k.UsedAmount.GreaterThanOrEqual(k.QuotaLimit)
}

type IssueKeyInput struct {
	OrgID       uint64
	UserID      *uint64 // per-member key；org 级 key 传 nil
	Name        string
	Scopes      string
	ExpiresAt   *time.Time
	QuotaLimit  decimal.Decimal // 累计消费上限 USD，0=无限
	ModelLimits string          // 模型白名单
	AllowIPs    string          // IP 白名单
	RPMLimit    int             // key 级每分钟请求上限，0=不限（0041）
	MaxKeys     int             // org 活跃 Key 上限（entitlements），0=不限
}

// IssueKey 签发新 Key。返回 (明文, 记录)；明文此后不可再取。
func IssueKey(tx *gorm.DB, in IssueKeyInput) (string, *APIKey, error) {
	if in.OrgID == 0 {
		return "", nil, ErrInvalidInput
	}
	// 资源上限（entitlements）：同事务内 COUNT 活跃 Key，触顶即拒（原子，无 TOCTOU）。
	if in.MaxKeys > 0 {
		var n int64
		if err := tx.Model(&APIKey{}).
			Where("org_id = ? AND status = ?", in.OrgID, StatusActive).Count(&n).Error; err != nil {
			return "", nil, err
		}
		if n >= int64(in.MaxKeys) {
			return "", nil, ErrKeyLimit
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext := keyPrefix + base64.RawURLEncoding.EncodeToString(raw) // 9 + 43 字符
	sum := sha256.Sum256([]byte(plaintext))

	k := &APIKey{
		OrgID:       in.OrgID,
		UserID:      in.UserID,
		Name:        in.Name,
		Prefix:      plaintext[:len(keyPrefix)+7], // 展示用 "sk-laiai-Ab3dEf9"
		KeyHash:     hex.EncodeToString(sum[:]),
		Scopes:      in.Scopes,
		Status:      StatusActive,
		ExpiresAt:   in.ExpiresAt,
		QuotaLimit:  in.QuotaLimit,
		ModelLimits: in.ModelLimits,
		AllowIPs:    in.AllowIPs,
		RPMLimit:    in.RPMLimit,
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
	// P0-1：key 所属 org 必须 active；per-member key（绑定具体成员）还需该成员 active 且 membership 仍在
	// ——组织禁用 / 成员被移除 / 用户被封禁后，其 key 立即失效（不等 TTL）。
	if k.UserID != nil {
		if err := VerifyActiveMembership(db, *k.UserID, k.OrgID); err != nil {
			return nil, err
		}
	} else if err := VerifyOrgActive(db, k.OrgID); err != nil {
		return nil, err
	}
	return k, nil
}

// RevokeKey 吊销（幂等：重复吊销无害）。
func RevokeKey(tx *gorm.DB, keyID uint64) error {
	return tx.Model(&APIKey{}).Where("id = ?", keyID).Update("status", StatusDisabled).Error
}

// AddKeyUsageSigned 原子调整 used_amount（允许负 delta），用于 reserve→actual 对账与失败退还。
// 单条 SQL 增量（非读改写），并发安全；忽略 0。
func AddKeyUsageSigned(db *gorm.DB, keyID uint64, delta decimal.Decimal) error {
	if delta.IsZero() {
		return nil
	}
	return db.Model(&APIKey{}).Where("id = ?", keyID).
		Update("used_amount", gorm.Expr("used_amount + ?", delta)).Error
}

// ReserveKeyQuota 在请求受理前原子预留额度（P0-3：消解 quota 并发超支）。
// 软上限语义（与既有"调用前 used<limit 即放行、本次消费可越界、下次拦截"一致）：
// 条件 UPDATE 仅当 quota 无限(0) 或 used_amount 尚未触顶(< quota_limit) 时才 +amount。
// 不用 used+amount<=quota 的硬前置（否则单次 hold 估值即可大于一个小 quota，会误拒首调）；
// 真正的资金护栏是 org 余额的 ledger hold，key quota 仅为软子预算，至多越界一笔在途调用。
// 返回 true=放行；false=已触顶，调用方应 402 拒绝。amount<=0 视为无需预留直接放行。
// 与 ledger hold 同构：先按 hold 估值预留，capture 用 AddKeyUsageSigned(actual-reserved) 对账，
// 失败用 AddKeyUsageSigned(-reserved) 退还。单条带条件 UPDATE 行锁串行化，并发下不会人人放行。
func ReserveKeyQuota(db *gorm.DB, keyID uint64, amount decimal.Decimal) bool {
	if !amount.IsPositive() {
		return true
	}
	res := db.Exec(
		`UPDATE api_keys SET used_amount = used_amount + ? WHERE id = ? AND (quota_limit = 0 OR used_amount < quota_limit)`,
		amount, keyID)
	return res.Error == nil && res.RowsAffected == 1
}
