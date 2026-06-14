package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// ErrBackupGen：连续撞码无法凑齐唯一码（40bit 空间下天文级不可能，仅作死循环兜底）。
var ErrBackupGen = errors.New("identity: could not generate unique backup codes")

// 2FA 备用码（gap #3）：TOTP 设备丢失时的恢复手段。只存归一化明文的 SHA-256，
// used_at 实现用过即焚。生成数固定 10 个。

const backupCodeCount = 10

// TwoFABackupCode：一条备用码（仅存哈希）。
type TwoFABackupCode struct {
	ID        uint64 `gorm:"primaryKey"`
	UserID    uint64 `gorm:"column:user_id"`
	CodeHash  string `gorm:"column:code_hash"`
	UsedAt    *int64 `gorm:"column:used_at"`
	CreatedAt int64  `gorm:"column:created_at"`
}

func (TwoFABackupCode) TableName() string { return "twofa_backup_codes" }

// normalizeBackupCode：去分隔符/空白 + 转小写，使展示形态(ab3d5-9f2c1)与输入(带不带横杠/大小写)一致。
func normalizeBackupCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hashBackupCode(s string) string {
	sum := sha256.Sum256([]byte(normalizeBackupCode(s)))
	return hex.EncodeToString(sum[:])
}

// genBackupCode：10 hex 字符（5 字节 = 40 bit 熵），展示形态 xxxxx-xxxxx。
func genBackupCode() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	h := hex.EncodeToString(b) // 10 字符
	return h[:5] + "-" + h[5:], nil
}

// GenerateBackupCodes：先删旧、再生成 backupCodeCount 个新码（写哈希）。返回明文（仅此一次）。
// 须在事务内调用（与"启用/重置 2FA"同事务）。
func GenerateBackupCodes(tx *gorm.DB, userID uint64, now int64) ([]string, error) {
	if err := tx.Where("user_id = ?", userID).Delete(&TwoFABackupCode{}).Error; err != nil {
		return nil, err
	}
	codes := make([]string, 0, backupCodeCount)
	rows := make([]TwoFABackupCode, 0, backupCodeCount)
	seen := map[string]bool{}
	for attempts := 0; len(codes) < backupCodeCount; attempts++ {
		if attempts > backupCodeCount*20 { // 死循环兜底（撞码天文级不可能）
			return nil, ErrBackupGen
		}
		code, err := genBackupCode()
		if err != nil {
			return nil, err
		}
		h := hashBackupCode(code)
		if seen[h] { // 极低概率撞码，跳过重生（保证恰好 10 个唯一）
			continue
		}
		seen[h] = true
		codes = append(codes, code)
		rows = append(rows, TwoFABackupCode{UserID: userID, CodeHash: h, CreatedAt: now})
	}
	if err := tx.Create(&rows).Error; err != nil {
		return nil, err
	}
	return codes, nil
}

// ConsumeBackupCode：原子校验并消费一个未用码。返回 true=成功消费。
// 单条带条件 UPDATE（used_at IS NULL）即原子闸：并发同码登录只有一个 RowsAffected=1，杜绝重复使用。
func ConsumeBackupCode(db *gorm.DB, userID uint64, code string, now int64) (bool, error) {
	if normalizeBackupCode(code) == "" {
		return false, nil // 空/全分隔符等畸形输入：不查询
	}
	// hashBackupCode 内部归一化，与生成时口径一致；带条件 UPDATE 是原子闸
	res := db.Model(&TwoFABackupCode{}).
		Where("user_id = ? AND code_hash = ? AND used_at IS NULL", userID, hashBackupCode(code)).
		Update("used_at", now)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// CountUnusedBackupCodes：剩余可用备用码数（用于登录后低余量提醒）。
func CountUnusedBackupCodes(db *gorm.DB, userID uint64) (int64, error) {
	var n int64
	err := db.Model(&TwoFABackupCode{}).Where("user_id = ? AND used_at IS NULL", userID).Count(&n).Error
	return n, err
}

// DeleteBackupCodes：清空某用户全部备用码（关闭 2FA 时调用）。
func DeleteBackupCodes(db *gorm.DB, userID uint64) error {
	return db.Where("user_id = ?", userID).Delete(&TwoFABackupCode{}).Error
}
