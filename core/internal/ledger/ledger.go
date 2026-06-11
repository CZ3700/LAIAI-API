// Package ledger 是 LAIAI-NEXT 的资金事实源（设计文档 §4.5）。
//
// 不变量（平台红线，见 docs/整体架构设计.md §5.1）：
//  1. 余额永不直接 UPDATE —— posted_balance/held_amount 只能由本包在过账时派生更新。
//  2. 所有资金动作必须在调用方事务 tx 内完成，journal 同写；本包任何 error 都必须导致上层回滚。
//  3. 幂等由 UNIQUE(idempotency_key) 兜底：重复请求返回首次结果，不重复记账。
//  4. journal append-only，无 UPDATE/DELETE 路径。
//
// 三原语：Hold（冻结）→ Capture（按实际用量结算）→ Release（解冻）。
// 直接过账（充值/grant 等）走 Post。available = posted_balance - held_amount。
package ledger

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 账户类型
const (
	AccountUserCredit      = "user_credit"
	AccountUserCommission  = "user_commission"
	AccountOrgMargin       = "org_margin"
	AccountPlatformRevenue = "platform_revenue"
)

// journal kind
const (
	KindCredit  = "credit"
	KindDebit   = "debit"
	KindCapture = "capture"
	KindGrant   = "grant"  // 订阅周期授予（设计 §4.6，原语 M0 预留）
	KindExpire  = "expire" // 订阅到期回收
)

// hold status
const (
	HoldActive   int8 = 0
	HoldCaptured int8 = 1
	HoldReleased int8 = 2
)

var (
	ErrInsufficientBalance = errors.New("ledger: insufficient available balance")
	ErrHoldNotActive       = errors.New("ledger: hold is not active")
	ErrCaptureExceedsHold  = errors.New("ledger: capture amount exceeds hold amount")
	ErrInvalidInput        = errors.New("ledger: invalid input")
)

type Account struct {
	ID            uint64          `gorm:"primaryKey"`
	OrgID         uint64          `gorm:"column:org_id"`
	Type          string          `gorm:"column:type"`
	PostedBalance decimal.Decimal `gorm:"column:posted_balance"`
	HeldAmount    decimal.Decimal `gorm:"column:held_amount"`
}

func (Account) TableName() string { return "ledger_accounts" }

// Available 返回可用余额（posted - held）。
func (a *Account) Available() decimal.Decimal { return a.PostedBalance.Sub(a.HeldAmount) }

type JournalEntry struct {
	ID             uint64          `gorm:"primaryKey"`
	IdempotencyKey string          `gorm:"column:idempotency_key"`
	AccountID      uint64          `gorm:"column:account_id"`
	Kind           string          `gorm:"column:kind"`
	Delta          decimal.Decimal `gorm:"column:delta"`
	BalanceAfter   decimal.Decimal `gorm:"column:balance_after"`
	HoldID         *uint64         `gorm:"column:hold_id"`
	RefType        string          `gorm:"column:ref_type"`
	RefID          string          `gorm:"column:ref_id"`
	Reason         string          `gorm:"column:reason"`
}

func (JournalEntry) TableName() string { return "ledger_journal" }

type Hold struct {
	ID             uint64          `gorm:"primaryKey"`
	IdempotencyKey string          `gorm:"column:idempotency_key"`
	AccountID      uint64          `gorm:"column:account_id"`
	Amount         decimal.Decimal `gorm:"column:amount"`
	CapturedAmount decimal.Decimal `gorm:"column:captured_amount"`
	Status         int8            `gorm:"column:status"`
	RefType        string          `gorm:"column:ref_type"`
	RefID          string          `gorm:"column:ref_id"`
}

func (Hold) TableName() string { return "ledger_holds" }

// EnsureAccount 取出（或创建）org 的某类型账户。幂等：依赖 UNIQUE(org_id, type)。
func EnsureAccount(tx *gorm.DB, orgID uint64, accType string) (*Account, error) {
	if orgID == 0 || accType == "" {
		return nil, fmt.Errorf("%w: orgID/accType required", ErrInvalidInput)
	}
	acc := &Account{OrgID: orgID, Type: accType}
	err := tx.Where("org_id = ? AND type = ?", orgID, accType).First(acc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if cerr := tx.Create(acc).Error; cerr != nil {
			if isDuplicate(cerr) { // 并发创建竞态：另一事务先建成，重读
				err = tx.Where("org_id = ? AND type = ?", orgID, accType).First(acc).Error
			} else {
				return nil, cerr
			}
		} else {
			return acc, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return acc, nil
}

// PostInput 直接过账（充值 credit、扣减 debit、订阅 grant/expire）。
type PostInput struct {
	IdempotencyKey string
	AccountID      uint64
	Kind           string
	Delta          decimal.Decimal // 有符号；credit/grant 为正，debit/expire 为负
	RefType        string
	RefID          string
	Reason         string
	AllowNegative  bool // 仅平台内部账户（如 platform_revenue 冲销）可置 true
}

// Post 过账一笔。返回 (entry, alreadyApplied, error)。
// alreadyApplied=true 表示该 idempotency_key 已记账，返回首次的 entry，余额未重复变动。
func Post(tx *gorm.DB, in PostInput) (*JournalEntry, bool, error) {
	if in.IdempotencyKey == "" || in.AccountID == 0 || in.Kind == "" || in.Delta.IsZero() {
		return nil, false, fmt.Errorf("%w: idempotency_key/account_id/kind/delta required", ErrInvalidInput)
	}
	acc, err := lockAccount(tx, in.AccountID)
	if err != nil {
		return nil, false, err
	}
	newBalance := acc.PostedBalance.Add(in.Delta)
	if in.Delta.IsNegative() && !in.AllowNegative {
		// 扣减不得击穿可用余额（held 部分已被预留，不可被直接 debit 挪用）
		if newBalance.Sub(acc.HeldAmount).IsNegative() {
			return nil, false, ErrInsufficientBalance
		}
	}
	entry := &JournalEntry{
		IdempotencyKey: in.IdempotencyKey,
		AccountID:      in.AccountID,
		Kind:           in.Kind,
		Delta:          in.Delta,
		BalanceAfter:   newBalance,
		RefType:        in.RefType,
		RefID:          in.RefID,
		Reason:         in.Reason,
	}
	if err := tx.Create(entry).Error; err != nil {
		if isDuplicate(err) {
			prior, ferr := findEntryByIdem(tx, in.IdempotencyKey)
			return prior, true, ferr
		}
		return nil, false, err
	}
	if err := setBalances(tx, acc.ID, newBalance, acc.HeldAmount); err != nil {
		return nil, false, err
	}
	return entry, false, nil
}

// HoldInput 冻结。
type HoldInput struct {
	IdempotencyKey string
	AccountID      uint64
	Amount         decimal.Decimal // 正数
	RefType        string
	RefID          string
}

// CreateHold 三原语之一：冻结可用余额。返回 (hold, alreadyApplied, error)。
func CreateHold(tx *gorm.DB, in HoldInput) (*Hold, bool, error) {
	if in.IdempotencyKey == "" || in.AccountID == 0 || !in.Amount.IsPositive() {
		return nil, false, fmt.Errorf("%w: idempotency_key/account_id/positive amount required", ErrInvalidInput)
	}
	acc, err := lockAccount(tx, in.AccountID)
	if err != nil {
		return nil, false, err
	}
	if acc.Available().LessThan(in.Amount) {
		return nil, false, ErrInsufficientBalance
	}
	h := &Hold{
		IdempotencyKey: in.IdempotencyKey,
		AccountID:      in.AccountID,
		Amount:         in.Amount,
		Status:         HoldActive,
		RefType:        in.RefType,
		RefID:          in.RefID,
	}
	if err := tx.Create(h).Error; err != nil {
		if isDuplicate(err) {
			prior := &Hold{}
			ferr := tx.Where("idempotency_key = ?", in.IdempotencyKey).First(prior).Error
			return prior, true, ferr
		}
		return nil, false, err
	}
	if err := setBalances(tx, acc.ID, acc.PostedBalance, acc.HeldAmount.Add(in.Amount)); err != nil {
		return nil, false, err
	}
	return h, false, nil
}

// Capture 三原语之二：按实际用量结算 hold。actual ≤ hold.Amount；差额自动解冻。
// 状态机 active→captured 本身就是幂等闸（重复 capture 返回 ErrHoldNotActive）。
func Capture(tx *gorm.DB, holdID uint64, actual decimal.Decimal, reason string) (*JournalEntry, error) {
	if actual.IsNegative() {
		return nil, fmt.Errorf("%w: actual must be >= 0", ErrInvalidInput)
	}
	h, err := lockHold(tx, holdID)
	if err != nil {
		return nil, err
	}
	if h.Status != HoldActive {
		return nil, ErrHoldNotActive
	}
	if actual.GreaterThan(h.Amount) {
		return nil, ErrCaptureExceedsHold
	}
	acc, err := lockAccount(tx, h.AccountID)
	if err != nil {
		return nil, err
	}
	newPosted := acc.PostedBalance.Sub(actual)
	newHeld := acc.HeldAmount.Sub(h.Amount) // 整个 hold 解除，未用差额自动回到可用
	var entry *JournalEntry
	if actual.IsPositive() {
		entry = &JournalEntry{
			IdempotencyKey: fmt.Sprintf("capture:%d", h.ID), // hold 一次性，天然唯一
			AccountID:      h.AccountID,
			Kind:           KindCapture,
			Delta:          actual.Neg(),
			BalanceAfter:   newPosted,
			HoldID:         &h.ID,
			RefType:        h.RefType,
			RefID:          h.RefID,
			Reason:         reason,
		}
		if err := tx.Create(entry).Error; err != nil {
			return nil, err
		}
	}
	if err := tx.Model(&Hold{}).Where("id = ? AND status = ?", h.ID, HoldActive).
		Updates(map[string]any{"status": HoldCaptured, "captured_amount": actual}).Error; err != nil {
		return nil, err
	}
	if err := setBalances(tx, acc.ID, newPosted, newHeld); err != nil {
		return nil, err
	}
	return entry, nil
}

// SettleHoldExact 按真实成本结算 hold 的统一语义（relay 与 task 共用）：
//   - actual ≤ hold：capture(actual)，差额自动解冻
//   - actual > hold（overage）：capture 全额 + AllowNegative debit 补差——
//     journal 必须记录真实成本，绝不"封顶少收"（系统性漏收）
//
// 返回 overage 金额（零值表示未发生超额），调用方据此告警。
func SettleHoldExact(tx *gorm.DB, holdID uint64, actual decimal.Decimal, refType, refID, reason string) (decimal.Decimal, error) {
	h, err := lockHold(tx, holdID)
	if err != nil {
		return decimal.Zero, err
	}
	if h.Status != HoldActive {
		return decimal.Zero, ErrHoldNotActive
	}
	if actual.LessThanOrEqual(h.Amount) {
		_, err := Capture(tx, holdID, actual, reason)
		return decimal.Zero, err
	}
	if _, err := Capture(tx, holdID, h.Amount, reason); err != nil {
		return decimal.Zero, err
	}
	overage := actual.Sub(h.Amount)
	_, _, err = Post(tx, PostInput{
		IdempotencyKey: fmt.Sprintf("overage:%d", holdID),
		AccountID:      h.AccountID,
		Kind:           KindDebit,
		Delta:          overage.Neg(),
		RefType:        refType + "_overage",
		RefID:          refID,
		Reason:         reason,
		AllowNegative:  true,
	})
	return overage, err
}

// Release 三原语之三：全额解冻（任务失败/超时回收）。
func Release(tx *gorm.DB, holdID uint64) error {
	h, err := lockHold(tx, holdID)
	if err != nil {
		return err
	}
	if h.Status != HoldActive {
		return ErrHoldNotActive
	}
	acc, err := lockAccount(tx, h.AccountID)
	if err != nil {
		return err
	}
	if err := tx.Model(&Hold{}).Where("id = ? AND status = ?", h.ID, HoldActive).
		Update("status", HoldReleased).Error; err != nil {
		return err
	}
	return setBalances(tx, acc.ID, acc.PostedBalance, acc.HeldAmount.Sub(h.Amount))
}

// ---- internal ----

// lockAccount 行锁取账户（MySQL: SELECT ... FOR UPDATE；SQLite 测试方言无行锁，事务串行化兜底）。
func lockAccount(tx *gorm.DB, id uint64) (*Account, error) {
	acc := &Account{}
	q := tx
	if tx.Dialector.Name() == "mysql" {
		q = tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(acc, id).Error; err != nil {
		return nil, fmt.Errorf("ledger: account %d: %w", id, err)
	}
	return acc, nil
}

func lockHold(tx *gorm.DB, id uint64) (*Hold, error) {
	h := &Hold{}
	q := tx
	if tx.Dialector.Name() == "mysql" {
		q = tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(h, id).Error; err != nil {
		return nil, fmt.Errorf("ledger: hold %d: %w", id, err)
	}
	return h, nil
}

// setBalances 是全代码库唯一允许写余额列的位置（红线 1）。
func setBalances(tx *gorm.DB, accID uint64, posted, held decimal.Decimal) error {
	if held.IsNegative() {
		return fmt.Errorf("ledger: invariant violation: held_amount would go negative on account %d", accID)
	}
	return tx.Model(&Account{}).Where("id = ?", accID).
		Updates(map[string]any{"posted_balance": posted, "held_amount": held}).Error
}

func findEntryByIdem(tx *gorm.DB, key string) (*JournalEntry, error) {
	e := &JournalEntry{}
	if err := tx.Where("idempotency_key = ?", key).First(e).Error; err != nil {
		return nil, err
	}
	return e, nil
}

func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "Duplicate entry") || // MySQL 1062
		strings.Contains(s, "UNIQUE constraint failed") // SQLite
}
