package ledger

import (
	"testing"

	"gorm.io/gorm"
)

// 健康账本（hold→capture + post + 一个活跃 hold + 一个已 release 的 hold）→ 无漂移。
func TestCheckDriftClean(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 1, "100")

	// hold 25 → capture 20（差额 5 自动解冻；journal -20）
	var h1 uint64
	mustTx(t, db, func(tx *gorm.DB) error {
		h, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "h1", AccountID: acc.ID, Amount: d("25")})
		h1 = h.ID
		return e
	})
	mustTx(t, db, func(tx *gorm.DB) error { _, e := Capture(tx, h1, d("20"), ""); return e })
	// 直接过账一笔 credit
	mustTx(t, db, func(tx *gorm.DB) error {
		_, _, e := Post(tx, PostInput{IdempotencyKey: "c1", AccountID: acc.ID, Kind: KindCredit, Delta: d("10")})
		return e
	})
	// 一个仍活跃的 hold（应计入 held），一个 release 的 hold（不计入）
	var h3 uint64
	mustTx(t, db, func(tx *gorm.DB) error {
		_, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "h2-active", AccountID: acc.ID, Amount: d("7")})
		return e
	})
	mustTx(t, db, func(tx *gorm.DB) error {
		h, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "h3-rel", AccountID: acc.ID, Amount: d("3")})
		h3 = h.ID
		return e
	})
	mustTx(t, db, func(tx *gorm.DB) error { return Release(tx, h3) })

	// posted = 100 - 20 + 10 = 90；held = 仅活跃的 7
	if a := reload(t, db, acc.ID); !a.PostedBalance.Equal(d("90")) || !a.HeldAmount.Equal(d("7")) {
		t.Fatalf("precondition: posted=%s held=%s, want 90/7", a.PostedBalance, a.HeldAmount)
	}
	drifts, err := CheckDrift(db)
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if len(drifts) != 0 {
		t.Fatalf("healthy ledger drift = %+v, want none", drifts)
	}
}

// 注入 posted 漂移（绕开本包直接 UPDATE 余额列，红线 1 违例）→ CheckDrift 必检出，且报出
// stored/derived 与漂移量。
func TestCheckDriftDetectsPostedTamper(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 2, "50") // posted=50, journal Σ=50

	// 绕开 ledger 包，直接篡改余额列（模拟 bug / 越权写）
	if e := db.Exec(`UPDATE ledger_accounts SET posted_balance = 999 WHERE id = ?`, acc.ID).Error; e != nil {
		t.Fatal(e)
	}
	drifts, err := CheckDrift(db)
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if len(drifts) != 1 {
		t.Fatalf("drift count = %d, want 1", len(drifts))
	}
	dft := drifts[0]
	if !dft.PostedStored.Equal(d("999")) || !dft.PostedDerived.Equal(d("50")) {
		t.Fatalf("posted stored/derived = %s/%s, want 999/50", dft.PostedStored, dft.PostedDerived)
	}
	if !dft.PostedDrift().Equal(d("949")) {
		t.Fatalf("posted drift = %s, want 949", dft.PostedDrift())
	}
}

// 注入 held 漂移（活跃 hold 与 held_amount 不一致）→ CheckDrift 必检出。
func TestCheckDriftDetectsHeldTamper(t *testing.T) {
	db := setupDB(t)
	acc := seedAccount(t, db, 3, "100")
	mustTx(t, db, func(tx *gorm.DB) error {
		_, _, e := CreateHold(tx, HoldInput{IdempotencyKey: "hx", AccountID: acc.ID, Amount: d("10")})
		return e
	})
	// 篡改 held（少冻结 4）
	if e := db.Exec(`UPDATE ledger_accounts SET held_amount = 6 WHERE id = ?`, acc.ID).Error; e != nil {
		t.Fatal(e)
	}
	drifts, err := CheckDrift(db)
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if len(drifts) != 1 || !drifts[0].HeldStored.Equal(d("6")) || !drifts[0].HeldDerived.Equal(d("10")) {
		t.Fatalf("held drift = %+v, want stored 6 derived 10", drifts)
	}
}

func mustTx(t *testing.T, db *gorm.DB, fn func(tx *gorm.DB) error) {
	t.Helper()
	if err := db.Transaction(fn); err != nil {
		t.Fatalf("tx: %v", err)
	}
}
