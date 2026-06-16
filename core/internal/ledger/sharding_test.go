package ledger

import (
	"testing"

	"gorm.io/gorm"
)

func setShardCount(t *testing.T, db *gorm.DB, orgID uint64, typ string, n int) {
	t.Helper()
	if err := db.Create(&AccountShardConfig{OrgID: orgID, Type: typ, ShardCount: n}).Error; err != nil {
		t.Fatalf("set shard count: %v", err)
	}
}

func creditShard(t *testing.T, db *gorm.DB, orgID uint64, typ string, shard int, amount, key string) {
	t.Helper()
	mustTx(t, db, func(tx *gorm.DB) error {
		acc, err := ensureAccountAt(tx, orgID, typ, shard)
		if err != nil {
			return err
		}
		_, _, err = Post(tx, PostInput{IdempotencyKey: key, AccountID: acc.ID, Kind: KindCredit, Delta: d(amount), RefType: "order", RefID: "seed"})
		return err
	})
}

// 缺省 shard_count=1 → CreateHoldSharded 与单账户行为逐字一致（零回归）。
func TestDefaultShardZeroRegression(t *testing.T) {
	db := setupDB(t)
	creditShard(t, db, 1, AccountUserCredit, 0, "10", "seed")
	mustTx(t, db, func(tx *gorm.DB) error {
		_, _, e := CreateHoldSharded(tx, 1, AccountUserCredit, "anykey", HoldInput{IdempotencyKey: "h", Amount: d("5"), RefType: "usage"})
		return e
	})
	if avail, _ := OrgAvailable(db, 1, AccountUserCredit); !avail.Equal(d("5")) {
		t.Fatalf("available = %s, want 5", avail)
	}
	var cnt int64
	db.Model(&Account{}).Where("org_id = 1 AND type = ?", AccountUserCredit).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("account rows = %d, want 1 (single shard)", cnt)
	}
}

// 跨分片回退 + SAVEPOINT：primary 片空、非 primary 片有钱 → hold 必须回退落到有钱片，
// 且 primary 片无残留 hold 行（SAVEPOINT 撤销失败片的脏插入——命门）。
func TestShardedHoldFallbackSavepoint(t *testing.T) {
	db := setupDB(t)
	setShardCount(t, db, 1, AccountUserCredit, 2)
	key := "fallback-key"
	primary := shardForKey(key, 2)
	other := 1 - primary
	creditShard(t, db, 1, AccountUserCredit, other, "10", "seedother") // 只给非 primary 片充钱

	var hold *Hold
	mustTx(t, db, func(tx *gorm.DB) error {
		h, _, e := CreateHoldSharded(tx, 1, AccountUserCredit, key, HoldInput{IdempotencyKey: "h", Amount: d("8"), RefType: "usage"})
		hold = h
		return e
	})
	// hold 落在有钱的 other 片
	var holdAcc Account
	db.First(&holdAcc, hold.AccountID)
	if holdAcc.Shard != other {
		t.Fatalf("hold landed on shard %d, want fallback to %d", holdAcc.Shard, other)
	}
	// primary 片 held=0（SAVEPOINT 回滚了它的脏冻结）
	var primAcc Account
	if db.Where("org_id = 1 AND type = ? AND shard = ?", AccountUserCredit, primary).First(&primAcc).Error == nil {
		if !primAcc.HeldAmount.Equal(d("0")) {
			t.Fatalf("primary shard held = %s, want 0 (savepoint rollback)", primAcc.HeldAmount)
		}
	}
	// 全表只一条 hold 行（失败片无残留）
	var hc int64
	db.Model(&Hold{}).Count(&hc)
	if hc != 1 {
		t.Fatalf("hold rows = %d, want 1 (failed shard left no residual)", hc)
	}
	if avail, _ := OrgAvailable(db, 1, AccountUserCredit); !avail.Equal(d("2")) {
		t.Fatalf("org available = %s, want 2", avail)
	}
	if drifts, _ := CheckDrift(db); len(drifts) != 0 {
		t.Fatalf("drift after sharded hold: %+v", drifts)
	}
}

// 各分片皆不足 = 全 org 真不足 → 拒，且无任何残留 hold 行。
func TestShardedHoldTrueInsufficient(t *testing.T) {
	db := setupDB(t)
	setShardCount(t, db, 1, AccountUserCredit, 2)
	creditShard(t, db, 1, AccountUserCredit, 0, "3", "s0")
	creditShard(t, db, 1, AccountUserCredit, 1, "3", "s1") // org 共 6，但单片各 3
	err := db.Transaction(func(tx *gorm.DB) error {
		_, _, e := CreateHoldSharded(tx, 1, AccountUserCredit, "anykey", HoldInput{IdempotencyKey: "h7", Amount: d("7"), RefType: "usage"})
		return e
	})
	if err == nil {
		t.Fatal("hold 7 with each shard only 3 must be insufficient")
	}
	var hc int64
	db.Model(&Hold{}).Count(&hc)
	if hc != 0 {
		t.Fatalf("residual hold rows = %d, want 0 (all-shard SAVEPOINT rollback)", hc)
	}
	if _, held, _ := OrgBalances(db, 1, AccountUserCredit); !held.Equal(d("0")) {
		t.Fatalf("held = %s, want 0", held)
	}
}

// 幂等重放：同 idempotency_key 二次调用返回首次 hold，不重复冻结、不进回退。
func TestShardedHoldIdempotentReplay(t *testing.T) {
	db := setupDB(t)
	setShardCount(t, db, 1, AccountUserCredit, 4)
	creditShard(t, db, 1, AccountUserCredit, shardForKey("rk", 4), "100", "seed")
	var h1, h2 *Hold
	var already bool
	mustTx(t, db, func(tx *gorm.DB) error {
		h, _, e := CreateHoldSharded(tx, 1, AccountUserCredit, "rk", HoldInput{IdempotencyKey: "dup", Amount: d("5"), RefType: "usage"})
		h1 = h
		return e
	})
	mustTx(t, db, func(tx *gorm.DB) error {
		h, a, e := CreateHoldSharded(tx, 1, AccountUserCredit, "rk", HoldInput{IdempotencyKey: "dup", Amount: d("5"), RefType: "usage"})
		h2, already = h, a
		return e
	})
	if !already || h1.ID != h2.ID {
		t.Fatalf("replay: already=%v h1=%d h2=%d, want true + same id", already, h1.ID, h2.ID)
	}
	var hc int64
	db.Model(&Hold{}).Count(&hc)
	if hc != 1 {
		t.Fatalf("hold rows = %d, want 1 (no double freeze)", hc)
	}
}

// rebalancer 成对过账 net=0：搬钱不改 org 总额、CheckDrift 干净。
func TestRebalancePairedJournalNetZero(t *testing.T) {
	db := setupDB(t)
	setShardCount(t, db, 1, AccountUserCredit, 2)
	creditShard(t, db, 1, AccountUserCredit, 0, "100", "seed0")
	mustTx(t, db, func(tx *gorm.DB) error {
		return RebalanceShards(tx, 1, AccountUserCredit, 0, 1, d("40"), "rb1")
	})
	var s0, s1 Account
	db.Where("org_id = 1 AND type = ? AND shard = 0", AccountUserCredit).First(&s0)
	db.Where("org_id = 1 AND type = ? AND shard = 1", AccountUserCredit).First(&s1)
	if !s0.PostedBalance.Equal(d("60")) || !s1.PostedBalance.Equal(d("40")) {
		t.Fatalf("after rebalance s0=%s s1=%s, want 60/40", s0.PostedBalance, s1.PostedBalance)
	}
	if posted, _, _ := OrgBalances(db, 1, AccountUserCredit); !posted.Equal(d("100")) {
		t.Fatalf("org posted = %s, want 100 (net zero)", posted)
	}
	if drifts, _ := CheckDrift(db); len(drifts) != 0 {
		t.Fatalf("drift after rebalance: %+v", drifts)
	}
}
