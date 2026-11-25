package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrBackupGen：连续撞码无法凑齐唯一码（40bit 空间下天文级不可能，仅作死循环兜底）。
var ErrBackupGen = errors.New("identity: could not generate unique backup codes")

const backupCodeCount = 10

type backupCodeEntry struct {
	Hash      string `json:"hash"`
	UsedAt    *int64 `json:"used_at,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

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

func genBackupCode() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	h := hex.EncodeToString(b)
	return h[:5] + "-" + h[5:], nil
}

func decodeBackupCodes(raw string) []backupCodeEntry {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var entries []backupCodeEntry
	_ = json.Unmarshal([]byte(raw), &entries)
	return entries
}

// GenerateBackupCodes replaces backup codes on users.totp_backup_codes_json and returns plaintext once.
func GenerateBackupCodes(tx *gorm.DB, userID uint64, now int64) ([]string, error) {
	codes := make([]string, 0, backupCodeCount)
	entries := make([]backupCodeEntry, 0, backupCodeCount)
	seen := map[string]bool{}
	for attempts := 0; len(codes) < backupCodeCount; attempts++ {
		if attempts > backupCodeCount*20 {
			return nil, ErrBackupGen
		}
		code, err := genBackupCode()
		if err != nil {
			return nil, err
		}
		h := hashBackupCode(code)
		if seen[h] {
			continue
		}
		seen[h] = true
		codes = append(codes, code)
		entries = append(entries, backupCodeEntry{Hash: h, CreatedAt: now})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	if err := tx.Model(&User{}).Where("id = ?", userID).Update("totp_backup_codes_json", string(raw)).Error; err != nil {
		return nil, err
	}
	return codes, nil
}

// ConsumeBackupCode atomically consumes a matching unused backup code inside a transaction.
func ConsumeBackupCode(db *gorm.DB, userID uint64, code string, now int64) (bool, error) {
	if normalizeBackupCode(code) == "" {
		return false, nil
	}
	targetHash := hashBackupCode(code)
	consumed := false
	err := db.Transaction(func(tx *gorm.DB) error {
		q := tx.Select("id, totp_backup_codes_json")
		if tx.Dialector.Name() != "sqlite" {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var u User
		if err := q.Where("id = ?", userID).First(&u).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		entries := decodeBackupCodes(u.TOTPBackupCodesJSON)
		idx := -1
		for i := range entries {
			if entries[i].UsedAt == nil && entries[i].Hash == targetHash {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		usedAt := now
		entries[idx].UsedAt = &usedAt
		raw, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where("id = ?", userID).Update("totp_backup_codes_json", string(raw)).Error; err != nil {
			return err
		}
		consumed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return consumed, nil
}

// CountUnusedBackupCodes returns remaining unused backup codes for a user.
func CountUnusedBackupCodes(db *gorm.DB, userID uint64) (int64, error) {
	var u User
	if err := db.Select("totp_backup_codes_json").Where("id = ?", userID).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	var count int64
	for _, entry := range decodeBackupCodes(u.TOTPBackupCodesJSON) {
		if entry.UsedAt == nil {
			count++
		}
	}
	return count, nil
}

// DeleteBackupCodes clears all backup codes for a user.
func DeleteBackupCodes(db *gorm.DB, userID uint64) error {
	return db.Model(&User{}).Where("id = ?", userID).Update("totp_backup_codes_json", "[]").Error
}
