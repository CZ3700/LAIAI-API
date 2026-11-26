package identity

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// API Key 设计（红线：明文任何层永不落库，只存 SHA-256 指纹；明文仅签发时返回一次）。
const keyPrefix = "sk-laiai-"

var (
	ErrKeyInvalid = errors.New("identity: api key invalid")
	ErrKeyExpired = errors.New("identity: api key expired")
	ErrKeyRevoked = errors.New("identity: api key revoked")

	defaultCSTZone = time.FixedZone("CST", 8*3600)
)

type APIKey struct {
	ID        uint64     `gorm:"primaryKey"`
	OrgID     uint64     `gorm:"column:org_id"`
	UserID    *uint64    `gorm:"column:user_id"`
	Name      string     `gorm:"column:name"`
	Prefix    string     `gorm:"column:prefix"`
	KID       string     `gorm:"column:kid"`
	KeyHash   string     `gorm:"column:key_hash"`
	Scopes    string     `gorm:"column:scopes"`
	Status    int8       `gorm:"column:status"`
	ExpiresAt *time.Time `gorm:"column:expires_at"`
	// 精细管控与周期限额（对标 tkeapi ApiToken）
	QuotaLimit        decimal.Decimal `gorm:"column:quota_limit"` // 累计消费上限 USD，<=0=无限
	UsedAmount        decimal.Decimal `gorm:"column:used_amount"` // 已消费 USD
	DailyQuotaLimit   float64         `gorm:"column:daily_quota_limit"`
	DailyQuotaUsed    float64         `gorm:"column:daily_quota_used"`
	WeeklyQuotaLimit  float64         `gorm:"column:weekly_quota_limit"`
	WeeklyQuotaUsed   float64         `gorm:"column:weekly_quota_used"`
	MonthlyQuotaLimit float64         `gorm:"column:monthly_quota_limit"`
	MonthlyQuotaUsed  float64         `gorm:"column:monthly_quota_used"`
	LastResetDay      string          `gorm:"column:last_reset_day"`
	LastResetWeek     string          `gorm:"column:last_reset_week"`
	LastResetMonth    string          `gorm:"column:last_reset_month"`
	ModelLimits       string          `gorm:"column:model_limits"` // 模型白名单，逗号分隔，空=不限
	AllowIPs          string          `gorm:"column:allow_ips"`    // IP 白名单，逗号/换行分隔，支持 CIDR，空=不限
	RPSLimit          int             `gorm:"column:rps_limit"`    // key 级每秒请求上限，0=不限
	RPMLimit          int             `gorm:"column:rpm_limit"`    // key 级每分钟请求上限，0=不限
	RoutePolicy       string          `gorm:"column:route_policy"` // key 级路由策略，空=order 默认
	HighAvailability  int8            `gorm:"column:high_availability"`
	LastUsedAt        *time.Time      `gorm:"column:last_used_at"`
	CreatedAt         time.Time       `gorm:"column:created_at"`
	UpdatedAt         time.Time       `gorm:"column:updated_at"`
}

func (APIKey) TableName() string { return "api_keys" }

// MakeTokenKID 生成 6 位短编号（UID 后 3 位 + 3 位随机数字，对齐 tkeapi make_token_kid）。
func MakeTokenKID(uid uint64) string {
	suffix := fmt.Sprintf("%03d", uid%1000)
	n, err := rand.Int(rand.Reader, big.NewInt(1000))
	rnd := 0
	if err == nil {
		rnd = int(n.Int64())
	}
	return fmt.Sprintf("%s%03d", suffix, rnd)
}

// QuotaPeriodKeys 计算指定时刻在目标时区下的日/周/月周期键（周键对齐 `%Y-%U` 与前端 getLocalPeriodKeys）。
func QuotaPeriodKeys(now time.Time, loc *time.Location) (day, week, month string) {
	if loc == nil {
		loc = defaultCSTZone
	}
	local := now.In(loc)
	day = local.Format("2006-01-02")
	month = local.Format("2006-01")
	year := local.Year()
	janFirst := time.Date(year, time.January, 1, 0, 0, 0, 0, loc)
	localMidnight := time.Date(year, local.Month(), local.Day(), 0, 0, 0, 0, loc)
	days := int(localMidnight.Sub(janFirst).Hours() / 24)
	weekNum := (days + int(janFirst.Weekday())) / 7
	week = fmt.Sprintf("%04d-%02d", year, weekNum)
	return day, week, month
}

// EffectivePeriodUsage 返回在指定时刻/时区下当前有效的日/周/月已用额度。
func (k *APIKey) EffectivePeriodUsage(now time.Time, loc *time.Location) (daily, weekly, monthly float64) {
	if k == nil {
		return 0, 0, 0
	}
	day, week, month := QuotaPeriodKeys(now, loc)
	if k.LastResetDay == day {
		daily = k.DailyQuotaUsed
	}
	if k.LastResetWeek == week {
		weekly = k.WeeklyQuotaUsed
	}
	if k.LastResetMonth == month {
		monthly = k.MonthlyQuotaUsed
	}
	return daily, weekly, monthly
}

// AllowsModel 返回该 Key 是否允许使用指定模型（空白名单 = 允许全部）。
func (k *APIKey) AllowsModel(model string) bool {
	if k == nil {
		return false
	}
	return LegacyModelAllowed(k.ModelLimits, model)
}

// LegacyModelAllowed preserves CSV model allowlist matching.
func LegacyModelAllowed(modelLimits, model string) bool {
	if strings.TrimSpace(modelLimits) == "" {
		return true
	}
	for entry := range strings.SplitSeq(modelLimits, ",") {
		if strings.TrimSpace(entry) == model {
			return true
		}
	}
	return false
}

// AllowsIP 返回调用方 IP 是否在白名单内（空=不限）。支持精确 IP 与 CIDR。
func (k *APIKey) AllowsIP(ip string) bool {
	if k == nil {
		return false
	}
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

// QuotaExhausted 返回该 Key 是否已用满累计额度（无限额度 <=0 恒为 false）。
func (k *APIKey) QuotaExhausted() bool {
	if k == nil {
		return false
	}
	return k.QuotaLimit.IsPositive() && k.UsedAmount.GreaterThanOrEqual(k.QuotaLimit)
}

// ExhaustedQuotaCode 按 总 -> 日 -> 周 -> 月 顺序返回首个已耗尽维度的错误码（未耗尽返回空串）。
func (k *APIKey) ExhaustedQuotaCode(now time.Time, loc *time.Location) string {
	if k == nil {
		return ""
	}
	if k.QuotaExhausted() {
		return "quota_total"
	}
	daily, weekly, monthly := k.EffectivePeriodUsage(now, loc)
	if k.DailyQuotaLimit >= 0 && daily >= k.DailyQuotaLimit {
		return "quota_daily"
	}
	if k.WeeklyQuotaLimit >= 0 && weekly >= k.WeeklyQuotaLimit {
		return "quota_weekly"
	}
	if k.MonthlyQuotaLimit >= 0 && monthly >= k.MonthlyQuotaLimit {
		return "quota_monthly"
	}
	return ""
}

// QuotaExhaustedAt 返回该 Key 在指定时刻是否已触发累计或任一周期额度上限。
func (k *APIKey) QuotaExhaustedAt(now time.Time, loc *time.Location) bool {
	return k.ExhaustedQuotaCode(now, loc) != ""
}

type IssueKeyInput struct {
	OrgID             uint64
	UserID            *uint64 // per-member key；org 级 key 传 nil
	Name              string
	Scopes            string
	ExpiresAt         *time.Time
	QuotaLimit        decimal.Decimal // 累计消费上限 USD，<=0=无限
	DailyQuotaLimit   *float64        // nil 或 <0 = 无限
	WeeklyQuotaLimit  *float64        // nil 或 <0 = 无限
	MonthlyQuotaLimit *float64        // nil 或 <0 = 无限
	ModelLimits       string          // 模型白名单
	AllowIPs          string          // IP 白名单
	RPSLimit          int             // key 级每秒请求上限，0=不限
	RPMLimit          int             // key 级每分钟请求上限，0=不限
	RoutePolicy       string          // key 级路由策略，调用方先过白名单校验
	HighAvailability  *int8           // nil 默认为 1
	MaxKeys           int             // org 活跃 Key 上限（entitlements），0=不限
}

func resolvePeriodicLimit(v *float64) float64 {
	if v == nil || *v < 0 {
		return -1
	}
	return *v
}

// IssueKey 签发新 Key。返回 (明文, 记录)；明文此后不可再取。
func IssueKey(tx *gorm.DB, in IssueKeyInput) (string, *APIKey, error) {
	if tx == nil || in.OrgID == 0 {
		return "", nil, ErrInvalidInput
	}
	var plain string
	var key *APIKey
	err := tx.Transaction(func(inner *gorm.DB) error {
		var e error
		plain, key, e = issueKeyTx(inner, in)
		return e
	})
	if err != nil {
		return "", nil, err
	}
	return plain, key, nil
}

func issueKeyTx(tx *gorm.DB, in IssueKeyInput) (string, *APIKey, error) {
	if in.OrgID == 0 {
		return "", nil, ErrInvalidInput
	}
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

	kidOwner := in.OrgID
	if in.UserID != nil && *in.UserID > 0 {
		kidOwner = *in.UserID
	}
	ha := int8(1)
	if in.HighAvailability != nil {
		if *in.HighAvailability == 0 {
			ha = 0
		}
	}

	k := &APIKey{
		OrgID:             in.OrgID,
		UserID:            in.UserID,
		Name:              cmp.Or(strings.TrimSpace(in.Name), "default"),
		Prefix:            plaintext[:len(keyPrefix)+7], // 展示用 "sk-laiai-Ab3dEf9"
		KID:               MakeTokenKID(kidOwner),
		KeyHash:           hex.EncodeToString(sum[:]),
		Scopes:            in.Scopes,
		Status:            StatusActive,
		ExpiresAt:         in.ExpiresAt,
		QuotaLimit:        in.QuotaLimit,
		DailyQuotaLimit:   resolvePeriodicLimit(in.DailyQuotaLimit),
		WeeklyQuotaLimit:  resolvePeriodicLimit(in.WeeklyQuotaLimit),
		MonthlyQuotaLimit: resolvePeriodicLimit(in.MonthlyQuotaLimit),
		ModelLimits:       in.ModelLimits,
		AllowIPs:          in.AllowIPs,
		RPSLimit:          max(0, in.RPSLimit),
		RPMLimit:          max(0, in.RPMLimit),
		RoutePolicy:       in.RoutePolicy,
		HighAvailability:  ha,
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
	return RevokeKeyScoped(tx, keyID, 0)
}

// RevokeKeyScoped 仅在 orgID 匹配（或 orgID=0 信任内部调用）时原子将 Key 置为禁用。
func RevokeKeyScoped(tx *gorm.DB, keyID, orgID uint64) error {
	return tx.Transaction(func(inner *gorm.DB) error {
		var key APIKey
		q := inner.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", keyID)
		if orgID != 0 {
			q = q.Where("org_id = ?", orgID)
		}
		if err := q.Take(&key).Error; err != nil {
			return err
		}
		if key.Status == StatusDisabled {
			return nil
		}
		return inner.Model(&APIKey{}).Where("id = ? AND org_id = ?", key.ID, key.OrgID).
			Update("status", StatusDisabled).Error
	})
}

// AddKeyUsageSigned 原子调整 used_amount 与当前活跃周期用量（允许负 delta），用于 reserve->actual 对账与失败退还。
func AddKeyUsageSigned(db *gorm.DB, keyID uint64, delta decimal.Decimal) error {
	if delta.IsZero() {
		return nil
	}
	deltaF, _ := delta.Float64()
	day, week, month := QuotaPeriodKeys(time.Now().UTC(), defaultCSTZone)
	return db.Exec(
		`UPDATE api_keys
		 SET used_amount = used_amount + ?,
		     daily_quota_used = CASE WHEN last_reset_day = ? THEN CASE WHEN daily_quota_used + ? < 0 THEN 0 ELSE daily_quota_used + ? END ELSE daily_quota_used END,
		     weekly_quota_used = CASE WHEN last_reset_week = ? THEN CASE WHEN weekly_quota_used + ? < 0 THEN 0 ELSE weekly_quota_used + ? END ELSE weekly_quota_used END,
		     monthly_quota_used = CASE WHEN last_reset_month = ? THEN CASE WHEN monthly_quota_used + ? < 0 THEN 0 ELSE monthly_quota_used + ? END ELSE monthly_quota_used END,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		delta,
		day, deltaF, deltaF,
		week, deltaF, deltaF,
		month, deltaF, deltaF,
		keyID,
	).Error
}

// ReserveKeyQuota 在请求受理前原子预留总额度与日/周/月周期额度（P0-3：消解 quota 并发超支）。
// 软上限语义：仅当累计及各已启用周期额度均未触顶时才原子增量放行；无限额度（总 <=0、周期 <0）不拦。
func ReserveKeyQuota(db *gorm.DB, keyID uint64, amount decimal.Decimal) bool {
	if !amount.IsPositive() {
		return true
	}
	amountF, _ := amount.Float64()
	day, week, month := QuotaPeriodKeys(time.Now().UTC(), defaultCSTZone)
	res := db.Exec(
		`UPDATE api_keys
		 SET used_amount = used_amount + ?,
		     daily_quota_used = CASE WHEN last_reset_day = ? THEN daily_quota_used + ? ELSE ? END,
		     weekly_quota_used = CASE WHEN last_reset_week = ? THEN weekly_quota_used + ? ELSE ? END,
		     monthly_quota_used = CASE WHEN last_reset_month = ? THEN monthly_quota_used + ? ELSE ? END,
		     last_reset_day = ?,
		     last_reset_week = ?,
		     last_reset_month = ?,
		     last_used_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?
		   AND (quota_limit <= 0 OR used_amount < quota_limit)
		   AND (daily_quota_limit < 0 OR (CASE WHEN last_reset_day = ? THEN daily_quota_used ELSE 0 END) < daily_quota_limit)
		   AND (weekly_quota_limit < 0 OR (CASE WHEN last_reset_week = ? THEN weekly_quota_used ELSE 0 END) < weekly_quota_limit)
		   AND (monthly_quota_limit < 0 OR (CASE WHEN last_reset_month = ? THEN monthly_quota_used ELSE 0 END) < monthly_quota_limit)`,
		amount,
		day, amountF, amountF,
		week, amountF, amountF,
		month, amountF, amountF,
		day, week, month,
		keyID,
		day, week, month,
	)
	return res.Error == nil && res.RowsAffected == 1
}
