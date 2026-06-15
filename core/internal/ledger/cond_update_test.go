package ledger

import (
	"fmt"
	"testing"

	"gorm.io/gorm"
)

// 方案A（条件原子 UPDATE 去 FOR UPDATE）专项回归：钉死边界与小数精度，防 SQLite NUMERIC 列
// REAL(float64) 运算噪声/存储类比较坑被回改。

// 可用额恰等于冻结额时必须放行（守卫 ROUND(posted-held-?,8)>=0 在边界为 0），再多 0.01 必须拒。
func TestHoldExactBoundary(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 1, "5")

	// 恰好冻结全部可用
	mustTx(t, db, func(tx *gorm.DB) error {
		_, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "h:full", AccountID: acc.ID, Amount: d("5"), RefType: "usage"})
		return e
	})
	if got := reload(t, db, acc.ID); !got.Available().Equal(d("0")) {
		t.Fatalf("available after full hold = %s, want 0", got.Available())
	}
	// 再冻结 0.01 必须因不足被拒（且不留 hold 行——回滚）
	err := db.Transaction(func(tx *gorm.DB) error {
		_, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "h:over", AccountID: acc.ID, Amount: d("0.01"), RefType: "usage"})
		return e
	})
	if err == nil {
		t.Fatal("over-boundary hold must fail with insufficient balance")
	}
	var cnt int64
	db.Model(&Hold{}).Where("idempotency_key = ?", "h:over").Count(&cnt)
	if cnt != 0 {
		t.Fatalf("rejected hold must roll back its row, found %d", cnt)
	}
}

// 多笔小数冻结/释放不得累积 float 漂移（ROUND 到 8 位钳噪声）。
func TestSmallDecimalNoDrift(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 1, "1")

	// 10 笔 0.1 冻结，累计应恰好 held=1.0、available=0
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("h:%d", i)
		mustTx(t, db, func(tx *gorm.DB) error {
			_, _, e := CreateHold(tx, HoldInput{IdempotencyKey: key, AccountID: acc.ID, Amount: d("0.1"), RefType: "usage"})
			return e
		})
	}
	got := reload(t, db, acc.ID)
	if !got.HeldAmount.Equal(d("1")) {
		t.Fatalf("held after 10×0.1 = %s, want exactly 1", got.HeldAmount)
	}
	if !got.Available().Equal(d("0")) {
		t.Fatalf("available = %s, want exactly 0", got.Available())
	}
	// 漂移自检必须干净（恒等式 B：held==Σ active holds）
	drifts, err := CheckDrift(db)
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(drifts) != 0 {
		t.Fatalf("unexpected drift after small-decimal holds: %+v", drifts)
	}
}

// capture 的 balance_after 必须是结算后精确 posted（回读，非全量覆盖）。
func TestCaptureBalanceAfterExact(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 1, "10")

	var holdID uint64
	mustTx(t, db, func(tx *gorm.DB) error {
		h, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "h:1", AccountID: acc.ID, Amount: d("5"), RefType: "usage"})
		if e == nil {
			holdID = h.ID
		}
		return e
	})
	var entry *JournalEntry
	mustTx(t, db, func(tx *gorm.DB) error {
		e, err := Capture(tx, holdID, d("3"), "settle")
		entry = e
		return err
	})
	// posted 10-3=7，held 全解除=0，未用 2 自动回可用
	got := reload(t, db, acc.ID)
	if !got.PostedBalance.Equal(d("7")) || !got.HeldAmount.Equal(d("0")) {
		t.Fatalf("after capture posted=%s held=%s, want 7/0", got.PostedBalance, got.HeldAmount)
	}
	if entry == nil || !entry.BalanceAfter.Equal(d("7")) {
		t.Fatalf("capture journal balance_after = %v, want 7", entry)
	}
	// 漂移干净
	drifts, _ := CheckDrift(db)
	if len(drifts) != 0 {
		t.Fatalf("drift after capture: %+v", drifts)
	}
}
