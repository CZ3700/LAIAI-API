package ledger

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 测试 schema 镜像 migrations/0002_ledger.sql（SQLite 方言）。
// 红线测试纪律（移植旧平台）：断言 DB 状态而非内部变量；fixture 用真实结构。
const testSchema = `
CREATE TABLE ledger_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL,
    type TEXT NOT NULL,
    posted_balance NUMERIC NOT NULL DEFAULT 0,
    held_amount NUMERIC NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, type)
);
CREATE TABLE ledger_journal (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key TEXT NOT NULL UNIQUE,
    account_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    delta NUMERIC NOT NULL,
    balance_after NUMERIC NOT NULL,
    hold_id INTEGER,
    ref_type TEXT NOT NULL DEFAULT '',
    ref_id TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE ledger_holds (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key TEXT NOT NULL UNIQUE,
    account_id INTEGER NOT NULL,
    amount NUMERIC NOT NULL,
    captured_amount NUMERIC NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 0,
    ref_type TEXT NOT NULL DEFAULT '',
    ref_id TEXT NOT NULL DEFAULT '',
    expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(testSchema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// 建一个有初始余额的账户
func seedAccount(t *testing.T, db *gorm.DB, orgID uint64, balance string) *Account {
	t.Helper()
	var acc *Account
	err := db.Transaction(func(tx *gorm.DB) error {
		a, err := EnsureAccount(tx, orgID, AccountUserCredit)
		if err != nil {
			return err
		}
		if d(balance).IsPositive() {
			_, _, err = Post(tx, PostInput{
				IdempotencyKey: fmt.Sprintf("seed:%d", orgID),
				AccountID:      a.ID, Kind: KindCredit, Delta: d(balance),
				RefType: "order", RefID: "seed", Reason: "test seed",
			})
		}
		acc = a
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return acc
}

func reload(t *testing.T, db *gorm.DB, id uint64) *Account {
	t.Helper()
	acc := &Account{}
	if err := db.First(acc, id).Error; err != nil {
		t.Fatalf("reload account: %v", err)
	}
	return acc
}

func TestEnsureAccountIdempotent(t *testing.T) {
	db := setupDB(t)
	var id1, id2 uint64
	_ = db.Transaction(func(tx *gorm.DB) error {
		a, err := EnsureAccount(tx, 7, AccountUserCredit)
		if err != nil {
			t.Fatalf("ensure 1: %v", err)
		}
		id1 = a.ID
		return nil
	})
	_ = db.Transaction(func(tx *gorm.DB) error {
		a, err := EnsureAccount(tx, 7, AccountUserCredit)
		if err != nil {
			t.Fatalf("ensure 2: %v", err)
		}
		id2 = a.ID
		return nil
	})
	if id1 != id2 {
		t.Fatalf("EnsureAccount not idempotent: %d vs %d", id1, id2)
	}
}

func TestPostIdempotency(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 1, "0")
	for i := 0; i < 3; i++ { // 同一 key 重放 3 次
		err := db.Transaction(func(tx *gorm.DB) error {
			entry, applied, err := Post(tx, PostInput{
				IdempotencyKey: "recharge:ord-1",
				AccountID:      acc.ID, Kind: KindCredit, Delta: d("10.5"),
				RefType: "order", RefID: "ord-1",
			})
			if err != nil {
				return err
			}
			if i == 0 && applied {
				t.Fatal("first post reported alreadyApplied")
			}
			if i > 0 && !applied {
				t.Fatal("replay not detected as alreadyApplied")
			}
			if !entry.Delta.Equal(d("10.5")) {
				t.Fatalf("entry delta = %s", entry.Delta)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("post round %d: %v", i, err)
		}
	}
	got := reload(t, db, acc.ID)
	if !got.PostedBalance.Equal(d("10.5")) {
		t.Fatalf("balance = %s, want 10.5 (replays must not double-credit)", got.PostedBalance)
	}
	var n int64
	db.Model(&JournalEntry{}).Count(&n)
	if n != 1 {
		t.Fatalf("journal rows = %d, want 1", n)
	}
}

func TestDebitCannotBreachAvailable(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 2, "5")
	err := db.Transaction(func(tx *gorm.DB) error {
		_, _, err := Post(tx, PostInput{
			IdempotencyKey: "debit:1", AccountID: acc.ID,
			Kind: KindDebit, Delta: d("-6"),
		})
		return err
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
	got := reload(t, db, acc.ID)
	if !got.PostedBalance.Equal(d("5")) {
		t.Fatalf("balance changed on failed debit: %s", got.PostedBalance)
	}
}

func TestHoldCaptureFlow(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 3, "100")

	var holdID uint64
	// 冻结 30
	err := db.Transaction(func(tx *gorm.DB) error {
		h, applied, err := CreateHold(tx, HoldInput{
			IdempotencyKey: "req:abc", AccountID: acc.ID, Amount: d("30"),
			RefType: "usage", RefID: "req-abc",
		})
		if err != nil || applied {
			return fmt.Errorf("hold: %v applied=%v", err, applied)
		}
		holdID = h.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := reload(t, db, acc.ID)
	if !got.Available().Equal(d("70")) || !got.PostedBalance.Equal(d("100")) {
		t.Fatalf("after hold: posted=%s held=%s", got.PostedBalance, got.HeldAmount)
	}

	// 冻结期间可用余额不足以再冻 80
	err = db.Transaction(func(tx *gorm.DB) error {
		_, _, err := CreateHold(tx, HoldInput{IdempotencyKey: "req:def", AccountID: acc.ID, Amount: d("80")})
		return err
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance during hold, got %v", err)
	}

	// 按实际用量 12.34567891 结算（差额 17.65 自动解冻）
	err = db.Transaction(func(tx *gorm.DB) error {
		entry, err := Capture(tx, holdID, d("12.34567891"), "usage settle")
		if err != nil {
			return err
		}
		if !entry.Delta.Equal(d("-12.34567891")) {
			t.Fatalf("capture delta = %s", entry.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got = reload(t, db, acc.ID)
	if !got.PostedBalance.Equal(d("87.65432109")) {
		t.Fatalf("posted after capture = %s, want 87.65432109", got.PostedBalance)
	}
	if !got.HeldAmount.IsZero() {
		t.Fatalf("held after capture = %s, want 0", got.HeldAmount)
	}

	// 重复 capture 必须被状态机拒绝
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := Capture(tx, holdID, d("1"), "double")
		return err
	})
	if !errors.Is(err, ErrHoldNotActive) {
		t.Fatalf("double capture: want ErrHoldNotActive, got %v", err)
	}
}

func TestCaptureExceedsHold(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 4, "50")
	var holdID uint64
	_ = db.Transaction(func(tx *gorm.DB) error {
		h, _, err := CreateHold(tx, HoldInput{IdempotencyKey: "h:1", AccountID: acc.ID, Amount: d("10")})
		if err != nil {
			t.Fatal(err)
		}
		holdID = h.ID
		return nil
	})
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := Capture(tx, holdID, d("10.00000001"), "")
		return err
	})
	if !errors.Is(err, ErrCaptureExceedsHold) {
		t.Fatalf("want ErrCaptureExceedsHold, got %v", err)
	}
}

func TestReleaseRestoresAvailable(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 5, "20")
	var holdID uint64
	_ = db.Transaction(func(tx *gorm.DB) error {
		h, _, err := CreateHold(tx, HoldInput{IdempotencyKey: "h:r", AccountID: acc.ID, Amount: d("20")})
		if err != nil {
			t.Fatal(err)
		}
		holdID = h.ID
		return nil
	})
	if err := db.Transaction(func(tx *gorm.DB) error { return Release(tx, holdID) }); err != nil {
		t.Fatal(err)
	}
	got := reload(t, db, acc.ID)
	if !got.Available().Equal(d("20")) {
		t.Fatalf("available after release = %s, want 20", got.Available())
	}
	// 释放后 journal 不应有任何过账（release 不动 posted）
	var n int64
	db.Model(&JournalEntry{}).Where("kind = ?", KindCapture).Count(&n)
	if n != 0 {
		t.Fatalf("release must not create capture entries, got %d", n)
	}
	// 重复 release 被拒
	err := db.Transaction(func(tx *gorm.DB) error { return Release(tx, holdID) })
	if !errors.Is(err, ErrHoldNotActive) {
		t.Fatalf("double release: want ErrHoldNotActive, got %v", err)
	}
}

func TestCaptureZeroActual(t *testing.T) {
	// actual=0（上游免费/失败但占用过额度）：hold 全额解冻，无 journal 过账
	db := setupDB(t)
	acc := seedAccount(t, db, 6, "10")
	var holdID uint64
	_ = db.Transaction(func(tx *gorm.DB) error {
		h, _, err := CreateHold(tx, HoldInput{IdempotencyKey: "h:z", AccountID: acc.ID, Amount: d("10")})
		if err != nil {
			t.Fatal(err)
		}
		holdID = h.ID
		return nil
	})
	err := db.Transaction(func(tx *gorm.DB) error {
		entry, err := Capture(tx, holdID, decimal.Zero, "no usage")
		if err != nil {
			return err
		}
		if entry != nil {
			t.Fatal("zero capture must not create a journal entry")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := reload(t, db, acc.ID)
	if !got.PostedBalance.Equal(d("10")) || !got.HeldAmount.IsZero() {
		t.Fatalf("after zero capture: posted=%s held=%s", got.PostedBalance, got.HeldAmount)
	}
}

func TestHoldIdempotency(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 8, "40")
	for i := 0; i < 2; i++ {
		err := db.Transaction(func(tx *gorm.DB) error {
			_, applied, err := CreateHold(tx, HoldInput{IdempotencyKey: "h:same", AccountID: acc.ID, Amount: d("15")})
			if err != nil {
				return err
			}
			if i > 0 && !applied {
				t.Fatal("hold replay not detected")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got := reload(t, db, acc.ID)
	if !got.HeldAmount.Equal(d("15")) {
		t.Fatalf("held = %s after replay, want 15 (must not double-hold)", got.HeldAmount)
	}
}

// P1-2：CreateHold 重放绝不能因期间可用余额下降而误报 insufficient——幂等查询前置于余额检查。
// 首次冻结 $25（avail 100→75）→ 再冻结 $70（avail 75→5）→ 用同首次的 key 重放 $25：
// 必须原样返回首次 hold 且 applied=true（avail=5 < 25 在修复前会错误地返 ErrInsufficientBalance）。
func TestHoldIdempotentReplaySurvivesBalanceDrop(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 11, "100")

	var firstID uint64
	mustTx(t, db, func(tx *gorm.DB) error {
		h, applied, e := CreateHold(tx, HoldInput{IdempotencyKey: "rk:1", AccountID: acc.ID, Amount: d("25")})
		if e == nil {
			firstID = h.ID
			if applied {
				t.Fatal("first hold reported applied")
			}
		}
		return e
	})
	// 另一笔冻结把可用余额压到 5（< 25）
	mustTx(t, db, func(tx *gorm.DB) error {
		_, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "rk:2", AccountID: acc.ID, Amount: d("70")})
		return e
	})
	if a := reload(t, db, acc.ID); !a.Available().Equal(d("5")) {
		t.Fatalf("precondition available = %s, want 5", a.Available())
	}

	// 重放首次的 key：必须幂等返回首次 hold，不得 ErrInsufficientBalance、不得二次冻结
	mustTx(t, db, func(tx *gorm.DB) error {
		h, applied, e := CreateHold(tx, HoldInput{IdempotencyKey: "rk:1", AccountID: acc.ID, Amount: d("25")})
		if e != nil {
			t.Fatalf("replay returned error %v, want idempotent success", e)
		}
		if !applied {
			t.Fatal("replay not detected as alreadyApplied")
		}
		if h.ID != firstID {
			t.Fatalf("replay hold id = %d, want first %d", h.ID, firstID)
		}
		return nil
	})
	// held 仍是 25+70=95，未因重放翻倍
	if a := reload(t, db, acc.ID); !a.HeldAmount.Equal(d("95")) {
		t.Fatalf("held after replay = %s, want 95 (no double-hold)", a.HeldAmount)
	}
}

// P1-2（同类，Post）：debit 重放绝不能因期间余额下降而误报 insufficient（如 admin subtract 重试）。
// $100 → debit 60（key d1，posted 40）→ debit 40（posted 0）→ 用 d1 重放 debit 60：
// 必须幂等返回首次 entry、posted 不再变（修复前会因 0-60<0 错误返 ErrInsufficientBalance）。
func TestPostDebitIdempotentReplaySurvivesBalanceDrop(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 12, "100")
	mustTx(t, db, func(tx *gorm.DB) error {
		_, _, e := Post(tx, PostInput{IdempotencyKey: "d1", AccountID: acc.ID, Kind: KindDebit, Delta: d("-60")})
		return e
	})
	mustTx(t, db, func(tx *gorm.DB) error {
		_, _, e := Post(tx, PostInput{IdempotencyKey: "d2", AccountID: acc.ID, Kind: KindDebit, Delta: d("-40")})
		return e
	})
	if a := reload(t, db, acc.ID); !a.PostedBalance.IsZero() {
		t.Fatalf("precondition posted = %s, want 0", a.PostedBalance)
	}
	mustTx(t, db, func(tx *gorm.DB) error {
		_, applied, e := Post(tx, PostInput{IdempotencyKey: "d1", AccountID: acc.ID, Kind: KindDebit, Delta: d("-60")})
		if e != nil {
			t.Fatalf("debit replay returned %v, want idempotent success", e)
		}
		if !applied {
			t.Fatal("debit replay not detected as alreadyApplied")
		}
		return nil
	})
	if a := reload(t, db, acc.ID); !a.PostedBalance.IsZero() {
		t.Fatalf("posted after replay = %s, want still 0 (no double-debit)", a.PostedBalance)
	}
}

// journal 重放对账：sum(delta) 必须等于 posted_balance（漂移自检 job 的核心断言，设计 §4.5）
func TestJournalReplayMatchesBalance(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 9, "100")
	var holdID uint64
	_ = db.Transaction(func(tx *gorm.DB) error {
		h, _, _ := CreateHold(tx, HoldInput{IdempotencyKey: "h:rp", AccountID: acc.ID, Amount: d("25")})
		holdID = h.ID
		return nil
	})
	_ = db.Transaction(func(tx *gorm.DB) error {
		_, err := Capture(tx, holdID, d("20"), "")
		return err
	})
	_ = db.Transaction(func(tx *gorm.DB) error {
		_, _, err := Post(tx, PostInput{IdempotencyKey: "r2", AccountID: acc.ID, Kind: KindCredit, Delta: d("3.3")})
		return err
	})

	var sum decimal.Decimal
	rows, err := db.Model(&JournalEntry{}).Where("account_id = ?", acc.ID).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		e := &JournalEntry{}
		if err := db.ScanRows(rows, e); err != nil {
			t.Fatal(err)
		}
		sum = sum.Add(e.Delta)
	}
	got := reload(t, db, acc.ID)
	if !sum.Equal(got.PostedBalance) {
		t.Fatalf("journal replay %s != posted_balance %s (drift!)", sum, got.PostedBalance)
	}
	if !got.PostedBalance.Equal(d("83.3")) {
		t.Fatalf("posted = %s, want 83.3", got.PostedBalance)
	}
}

// SettleHoldExact overage（审查指出的唯一无直接覆盖的资金不变量）：
// 实际成本 > hold 时，capture 全额 + AllowNegative debit 补差，journal 记录真实成本，余额可负。
func TestSettleHoldExactOverage(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 10, "1") // 仅 $1 余额
	var holdID uint64
	_ = db.Transaction(func(tx *gorm.DB) error {
		h, _, err := CreateHold(tx, HoldInput{IdempotencyKey: "h:ov", AccountID: acc.ID, Amount: d("1")})
		if err != nil {
			t.Fatal(err)
		}
		holdID = h.ID
		return nil
	})
	// 实际 $3 > hold $1：必须记录全部 $3，绝不封顶少收
	var overage decimal.Decimal
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		overage, e = SettleHoldExact(tx, holdID, d("3"), "usage", "m", "overage test")
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if !overage.Equal(d("2")) {
		t.Fatalf("overage = %s, want 2", overage)
	}
	got := reload(t, db, acc.ID)
	// posted = 1 - 3 = -2（允许负，下次充值补回）；held 清零
	if !got.PostedBalance.Equal(d("-2")) {
		t.Fatalf("posted = %s, want -2 (true cost recorded, balance negative)", got.PostedBalance)
	}
	if !got.HeldAmount.IsZero() {
		t.Fatalf("held = %s, want 0", got.HeldAmount)
	}
	// journal 重放 = posted（capture -1 + overage debit -2 + seed +1 = -2）
	var sum decimal.Decimal
	var entries []JournalEntry
	db.Where("account_id = ?", acc.ID).Find(&entries)
	for _, e := range entries {
		sum = sum.Add(e.Delta)
	}
	if !sum.Equal(got.PostedBalance) {
		t.Fatalf("journal replay %s != posted %s", sum, got.PostedBalance)
	}
}

// SettleHoldExact 正好等于 hold：纯 capture，无 overage debit。
func TestSettleHoldExactExact(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 11, "10")
	var holdID uint64
	_ = db.Transaction(func(tx *gorm.DB) error {
		h, _, _ := CreateHold(tx, HoldInput{IdempotencyKey: "h:ex", AccountID: acc.ID, Amount: d("4")})
		holdID = h.ID
		return nil
	})
	var overage decimal.Decimal
	_ = db.Transaction(func(tx *gorm.DB) error {
		var e error
		overage, e = SettleHoldExact(tx, holdID, d("4"), "usage", "m", "")
		return e
	})
	if !overage.IsZero() {
		t.Fatalf("overage = %s, want 0", overage)
	}
	got := reload(t, db, acc.ID)
	if !got.PostedBalance.Equal(d("6")) || !got.HeldAmount.IsZero() {
		t.Fatalf("posted=%s held=%s, want 6/0", got.PostedBalance, got.HeldAmount)
	}
}

// SweepStuckHolds：超时仍 Active 的 usage hold 被释放，task hold 不受影响（ref_type 过滤）。
func TestSweepStuckHolds(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 12, "100")
	mk := func(idem, refType string) {
		_ = db.Transaction(func(tx *gorm.DB) error {
			_, _, err := CreateHold(tx, HoldInput{
				IdempotencyKey: idem, AccountID: acc.ID, Amount: d("10"), RefType: refType,
			})
			return err
		})
	}
	mk("stuck1", "usage")
	mk("stuck2", "usage")
	mk("tasktype", "task") // 不应被 usage 清扫触及
	// 把全部 hold 的 created_at 推到过去（绕过 autoCreateTime）
	db.Exec("UPDATE ledger_holds SET created_at = ?", time.Now().UTC().Add(-time.Hour))

	n, err := SweepStuckHolds(db, "usage", time.Now().UTC().Add(-15*time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("released = %d, want 2 (only usage holds)", n)
	}
	got := reload(t, db, acc.ID)
	// 2 个 usage hold 释放 ($20)，task hold ($10) 仍冻结 → held = 10
	if !got.HeldAmount.Equal(d("10")) {
		t.Fatalf("held = %s, want 10 (task hold untouched)", got.HeldAmount)
	}
}
