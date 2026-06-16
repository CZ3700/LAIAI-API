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
//
// journal 生命周期口径（哪些原语写 journal、哪些只动余额列）——这是漂移自检的前提：
//   - Post                写 1 条 journal（credit/debit/grant/expire），改 posted_balance。
//   - CreateHold          不写 journal；只 +held_amount（冻结是预留，非过账）。
//   - Capture(actual>0)   写 1 条 KindCapture(delta=-actual)，改 posted_balance；并 -整笔 held。
//   - Capture(actual==0)  不写 journal；只 -整笔 held（纯解冻，无消费）。
//   - Release             不写 journal；只 -整笔 held。
//
// 由此得两条对账恒等式（CheckDrift 巡检；设计 §4.5）：
//
//	(A) posted_balance == Σ journal.delta            （append-only，过账唯一来源）
//	(B) held_amount    == Σ holds.amount WHERE status=Active （仅活跃冻结占用）
//
// 任一不成立即资金漂移——只可能源于绕开本包写余额、或局部写入损坏，必须告警人工裁定。
package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/laiai/laiai-next/core/internal/dberr"
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
	Shard         int             `gorm:"column:shard"` // 账户分片号（0=主分片；规模化方案B）。默认 0=逻辑单账户。
	PostedBalance decimal.Decimal `gorm:"column:posted_balance"`
	HeldAmount    decimal.Decimal `gorm:"column:held_amount"`
}

func (Account) TableName() string { return "ledger_accounts" }

// AccountShardConfig：(org,type) 的物理分片数（缺省 1）。仅给触顶单账户行写吞吐的巨客户设 >1（规模化）。
type AccountShardConfig struct {
	OrgID      uint64 `gorm:"column:org_id"`
	Type       string `gorm:"column:type"`
	ShardCount int    `gorm:"column:shard_count"`
}

func (AccountShardConfig) TableName() string { return "ledger_account_shards" }

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

// EnsureAccount 取出（或创建）org 的某类型账户的**主分片(shard=0)**。幂等：UNIQUE(org_id,type,shard)。
//
// 🚨 分片表上 (org_id,type) 不再唯一（uk_account_org_type_shard），故必须显式钉 shard=0——否则
// `First WHERE org_id AND type` 在 N>1 时非确定命中任意分片（admin override 据此算 delta 会严重订偏，
// 对抗评审 high）。充值/grant/admin-override-base 等「逻辑账户级」写都入主分片；hold 走 CreateHoldSharded 选片。
func EnsureAccount(tx *gorm.DB, orgID uint64, accType string) (*Account, error) {
	if orgID == 0 || accType == "" {
		return nil, fmt.Errorf("%w: orgID/accType required", ErrInvalidInput)
	}
	return ensureAccountAt(tx, orgID, accType, 0)
}

// ensureAccountAt 取出/创建指定分片的账户行（三元唯一键幂等）。
func ensureAccountAt(tx *gorm.DB, orgID uint64, accType string, shard int) (*Account, error) {
	acc := &Account{OrgID: orgID, Type: accType, Shard: shard}
	err := tx.Where("org_id = ? AND type = ? AND shard = ?", orgID, accType, shard).First(acc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if cerr := tx.Create(acc).Error; cerr != nil {
			if isDuplicate(cerr) { // 并发创建竞态：另一事务先建成，重读
				err = tx.Where("org_id = ? AND type = ? AND shard = ?", orgID, accType, shard).First(acc).Error
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

// shardCountFor 返回 (org,type) 的分片数（无配置=1）。
func shardCountFor(tx *gorm.DB, orgID uint64, accType string) (int, error) {
	cfg := &AccountShardConfig{}
	err := tx.Where("org_id = ? AND type = ?", orgID, accType).First(cfg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 1, nil // 缺省单分片 → 与现状逐字一致
	}
	if err != nil {
		return 0, err
	}
	if cfg.ShardCount < 1 {
		return 1, nil
	}
	return cfg.ShardCount, nil
}

// shardForKey 确定性哈希选片（同 key 总落同片）。幂等仍由 UNIQUE(idempotency_key) 全表兜底，与此无关。
func shardForKey(key string, n int) int {
	if n <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(n))
}

// shardOrder：primary(hash 分散写热点) 优先 + 其余按升序回退。回退段全局同序限制跨分片回退死锁窗口。
func shardOrder(primary, n int) []int {
	order := make([]int, 0, n)
	order = append(order, primary)
	for s := 0; s < n; s++ {
		if s != primary {
			order = append(order, s)
		}
	}
	return order
}

// EnsureShardedAccount：按 shardKey 选片并 ensure（hold 选片用；非 hold 的逻辑账户级写用 EnsureAccount=主分片）。
func EnsureShardedAccount(tx *gorm.DB, orgID uint64, accType, shardKey string) (*Account, error) {
	n, err := shardCountFor(tx, orgID, accType)
	if err != nil {
		return nil, err
	}
	return ensureAccountAt(tx, orgID, accType, shardForKey(shardKey, n))
}

// CreateHoldSharded：分片感知 hold——按 shardKey 选主分片，余额不足时按 shardOrder 跨分片回退；
// 各片皆不足才返回 ErrInsufficientBalance（=全 org 真不足）。
//
// 🚨 回退命门（对抗评审 high）：CreateHold 先插 hold 行（UNIQUE(idempotency_key) tripwire）再条件冻结；
// 某片不足时**必须 SAVEPOINT/RollbackTo 撤销该片刚插的 hold 行**，否则下一片 CreateHold 撞自己刚插的
// 同键脏行→被误判幂等命中、返回 held=0 的错误片 hold。savepoint 名只用循环序号（绝不拼 shardKey，
// 防 GORM 裸拼接 SQL 的语法破坏/注入）。RollbackTo 也释放失败片本次取得的行锁，配合 shardOrder
// 的全局同序回退消除跨分片回退死锁。
//
// 幂等：重放（同 idempotency_key）无论原 hold 落哪片，第一片 CreateHold 的前置 idem 查询即命中首次 hold
// 直接返回（already=true），不再选片/回退（注意：重放的 shardKey 可能 hash 到不同 primary，但 idem 查询
// 是全表按 key，故仍命中——绝不重复冻结）。
//
// N<=1（缺省/未分片）→ 直接走 CreateHold，与现状逐字一致（零回归，不引入 SAVEPOINT 开销）。
func CreateHoldSharded(tx *gorm.DB, orgID uint64, accType, shardKey string, in HoldInput) (*Hold, bool, error) {
	n, err := shardCountFor(tx, orgID, accType)
	if err != nil {
		return nil, false, err
	}
	if n <= 1 {
		acc, e := ensureAccountAt(tx, orgID, accType, 0)
		if e != nil {
			return nil, false, e
		}
		in.AccountID = acc.ID
		return CreateHold(tx, in)
	}
	for attempt, shard := range shardOrder(shardForKey(shardKey, n), n) {
		acc, e := ensureAccountAt(tx, orgID, accType, shard)
		if e != nil {
			return nil, false, e
		}
		in.AccountID = acc.ID
		sp := "sp_" + strconv.Itoa(attempt) // 受控字符（绝不拼 shardKey）
		if e := tx.SavePoint(sp).Error; e != nil {
			return nil, false, e
		}
		h, already, herr := CreateHold(tx, in)
		if herr == nil {
			return h, already, nil // 成功 或 幂等命中：直接返回，不再换片
		}
		if !errors.Is(herr, ErrInsufficientBalance) {
			return nil, false, herr // 真错误（DB 等）→ 上抛回滚
		}
		if e := tx.RollbackTo(sp).Error; e != nil { // 撤销本片脏 hold 行 + 释放本片行锁
			return nil, false, e
		}
	}
	return nil, false, ErrInsufficientBalance // 各片皆不足 = 全 org 真不足
}

// OrgAvailable 返回 (org,type) 跨所有分片的可用余额之和（Σ posted - held）。读路径与回退判定复用。
func OrgAvailable(tx *gorm.DB, orgID uint64, accType string) (decimal.Decimal, error) {
	posted, held, err := OrgBalances(tx, orgID, accType)
	if err != nil {
		return decimal.Zero, err
	}
	return posted.Sub(held), nil
}

// OrgBalances 返回 (org,type) 跨所有分片的 (Σposted, Σheld)。供 console/admin 余额展示按分片归并。
func OrgBalances(tx *gorm.DB, orgID uint64, accType string) (posted, held decimal.Decimal, err error) {
	var row struct {
		Posted decimal.Decimal `gorm:"column:posted"`
		Held   decimal.Decimal `gorm:"column:held"`
	}
	e := tx.Model(&Account{}).
		Select("COALESCE(SUM(posted_balance),0) AS posted, COALESCE(SUM(held_amount),0) AS held").
		Where("org_id = ? AND type = ?", orgID, accType).Scan(&row).Error
	return row.Posted, row.Held, e
}

// ShardAccountIDs 返回 (org,type) 所有分片账户行的 id（getLedger 流水按 account_id IN (...) 过滤用）。
func ShardAccountIDs(tx *gorm.DB, orgID uint64, accType string) ([]uint64, error) {
	var ids []uint64
	err := tx.Model(&Account{}).Where("org_id = ? AND type = ?", orgID, accType).Pluck("id", &ids).Error
	return ids, err
}

// LockOrgPosted 锁定 (org,type) 所有分片行（FOR UPDATE）并返回 Σposted_balance。
// admin override 精确订正用：持锁到事务结束，期间各分片 posted 不被改动，delta=目标-Σposted 精确。
func LockOrgPosted(tx *gorm.DB, orgID uint64, accType string) (decimal.Decimal, error) {
	q := tx
	if tx.Dialector.Name() == "mysql" {
		q = tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var accs []Account
	if err := q.Where("org_id = ? AND type = ?", orgID, accType).Find(&accs).Error; err != nil {
		return decimal.Zero, err
	}
	total := decimal.Zero
	for i := range accs {
		total = total.Add(accs[i].PostedBalance)
	}
	return total, nil
}

// RebalanceShards 在同 (org,type) 两分片间搬钱：from 片 debit、to 片 credit，成对过账 net=0，
// 保住恒等式 A（Σjournal.delta 守恒）。供 rebalancer job 抹平分片倾斜（充值默认入主分片、消费打散，
// 不抹平则非主片饿死→回退反复打主片，分片退化为入金单点）。amount 正数。
func RebalanceShards(tx *gorm.DB, orgID uint64, accType string, fromShard, toShard int, amount decimal.Decimal, idemKey string) error {
	if !amount.IsPositive() || fromShard == toShard {
		return fmt.Errorf("%w: positive amount + distinct shards required", ErrInvalidInput)
	}
	from, err := ensureAccountAt(tx, orgID, accType, fromShard)
	if err != nil {
		return err
	}
	to, err := ensureAccountAt(tx, orgID, accType, toShard)
	if err != nil {
		return err
	}
	if _, _, err := Post(tx, PostInput{ // debit from（带可用守卫，不可搬出超过其可用额）
		IdempotencyKey: "rebal_out:" + idemKey, AccountID: from.ID,
		Kind: KindDebit, Delta: amount.Neg(), RefType: "rebalance", RefID: accType,
		Reason: fmt.Sprintf("shard rebalance %d->%d", fromShard, toShard),
	}); err != nil {
		return err
	}
	_, _, err = Post(tx, PostInput{ // credit to
		IdempotencyKey: "rebal_in:" + idemKey, AccountID: to.ID,
		Kind: KindCredit, Delta: amount, RefType: "rebalance", RefID: accType,
		Reason: fmt.Sprintf("shard rebalance %d->%d", fromShard, toShard),
	})
	return err
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
	// 幂等前置（P1-2）：已存在同键过账 → 直接返回首次结果，**不再做余额检查**。
	// 否则 debit 重放会在期间余额下降时误报 insufficient（如 admin subtract 重试）——
	// 首次早已成功扣减，重放本应原样返回，绝不能因后续余额变化翻盘。
	existing := &JournalEntry{}
	switch err := tx.Where("idempotency_key = ?", in.IdempotencyKey).First(existing).Error; {
	case err == nil:
		return existing, true, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, false, err
	}
	// 方案A：plain 读账户（不加 FOR UPDATE）算 balance_after 锚点。乐观模型下高并发跨事务
	// 交错时 balance_after 是尽力快照（恒等式A: posted==Σjournal.delta 不依赖它，CheckDrift 仍精确）；
	// 顺序场景（admin override 已持 LockAccount、settle 同事务内 overage）回读即精确。
	acc := &Account{}
	if err := tx.Select("posted_balance").First(acc, in.AccountID).Error; err != nil {
		return nil, false, fmt.Errorf("ledger: account %d: %w", in.AccountID, err)
	}
	newBalance := acc.PostedBalance.Add(in.Delta)
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
	// 先插 journal（UNIQUE(idempotency_key) 是并发幂等 tripwire），再条件过账：
	// 只有赢得 INSERT 的事务才继续改余额，杜绝并发同键双重过账。
	if err := tx.Create(entry).Error; err != nil {
		if isDuplicate(err) {
			prior, ferr := findEntryByIdem(tx, in.IdempotencyKey)
			return prior, true, ferr
		}
		return nil, false, err
	}
	// 条件过账：posted += delta。debit 且不允许负 → 守卫 posted+delta-held>=0（held 已预留，
	// 不可被 debit 挪用）；credit/grant/AllowNegative → 无守卫必成功。守卫把参数折进算术（见 applyBalanceDelta）。
	var n int64
	var derr error
	if in.Delta.IsNegative() && !in.AllowNegative {
		n, derr = applyBalanceDelta(tx, in.AccountID, in.Delta, decimal.Zero,
			"ROUND(posted_balance + ? - held_amount, 8) >= 0", in.Delta)
	} else {
		n, derr = applyBalanceDelta(tx, in.AccountID, in.Delta, decimal.Zero, "")
	}
	if derr != nil {
		return nil, false, derr
	}
	if n == 0 {
		if in.Delta.IsNegative() && !in.AllowNegative {
			return nil, false, ErrInsufficientBalance // debit 守卫未过 → 上层回滚撤销 journal
		}
		return nil, false, fmt.Errorf("ledger: post affected 0 rows on account %d", in.AccountID)
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
	// 幂等前置（P1-2）：已存在同键 hold → 直接返回首次结果，**不再做余额检查**。
	// 否则重放会在期间可用余额下降时误报 ErrInsufficientBalance——但首次早已成功冻结，
	// 重放本应原样返回首次结果（幂等契约），绝不能因后续余额变化翻盘。
	existing := &Hold{}
	switch err := tx.Where("idempotency_key = ?", in.IdempotencyKey).First(existing).Error; {
	case err == nil:
		return existing, true, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, false, err
	}
	// 方案A（去 FOR UPDATE 行锁，乐观条件原子写）：先插 hold 行（UNIQUE(idempotency_key)
	// 是并发幂等 tripwire，也是恒等式 B 的派生来源），再发条件冻结 UPDATE。
	// 顺序理由：hold 行先落 → 并发同键重放在此处即被 UNIQUE 拦下，绝不会双重进入条件冻结；
	// 若随后条件冻结失败（余额不足）返回 error → 同事务回滚把刚插的 hold 行一并撤销。
	h := &Hold{
		IdempotencyKey: in.IdempotencyKey,
		AccountID:      in.AccountID,
		Amount:         in.Amount,
		Status:         HoldActive,
		RefType:        in.RefType,
		RefID:          in.RefID,
	}
	if err := tx.Create(h).Error; err != nil {
		if isDuplicate(err) { // 并发竞态：前置查询后、Create 前被另一事务抢先插入 → 重读返回首次结果
			prior := &Hold{}
			ferr := tx.Where("idempotency_key = ?", in.IdempotencyKey).First(prior).Error
			return prior, true, ferr
		}
		return nil, false, err
	}
	// 条件冻结：held += amount，守卫 available(=posted-held) >= amount。
	// 守卫把 amount 折进算术（- ? >= 0）而非直接比较（>= ?）——见 applyBalanceDelta 注释（SQLite 方言）。
	n, err := applyBalanceDelta(tx, in.AccountID, decimal.Zero, in.Amount,
		"ROUND(posted_balance - held_amount - ?, 8) >= 0", in.Amount)
	if err != nil {
		return nil, false, err
	}
	if n == 0 {
		return nil, false, ErrInsufficientBalance // 余额不足 → 上层回滚，hold 行随之撤销
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
	// 方案A：状态机 CAS 先行（active→captured）作 capture 的并发幂等闸——只有一个事务能赢得
	// 该转移，故后续余额写与 journal 插入无需账户行锁（去掉 lockAccount FOR UPDATE 热点）。
	res := tx.Model(&Hold{}).Where("id = ? AND status = ?", h.ID, HoldActive).
		Updates(map[string]any{"status": HoldCaptured, "captured_amount": actual})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrHoldNotActive // 并发已抢走该 hold
	}
	// posted -= actual；held -= h.Amount（整笔 hold 解除，未用差额自动回到可用）。
	// held 守卫 held-h.Amount>=0：把旧 setBalances 的 held 负值防御从「兜底」提为「强制前置守卫」。
	n, err := applyBalanceDelta(tx, h.AccountID, actual.Neg(), h.Amount.Neg(),
		"ROUND(held_amount - ?, 8) >= 0", h.Amount)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("ledger: capture would drive held_amount negative on account %d (invariant violation)", h.AccountID)
	}
	var entry *JournalEntry
	if actual.IsPositive() {
		// 回读 posted 填 balance_after：本事务上一条 UPDATE 已持该账户行锁、并发写被挡到提交后，
		// 故回读即结算后真实余额（精确）。绝不 UPDATE journal（红线4 append-only）。
		posted, perr := readPosted(tx, h.AccountID)
		if perr != nil {
			return nil, perr
		}
		entry = &JournalEntry{
			IdempotencyKey: fmt.Sprintf("capture:%d", h.ID), // hold 一次性，天然唯一
			AccountID:      h.AccountID,
			Kind:           KindCapture,
			Delta:          actual.Neg(),
			BalanceAfter:   posted,
			HoldID:         &h.ID,
			RefType:        h.RefType,
			RefID:          h.RefID,
			Reason:         reason,
		}
		if err := tx.Create(entry).Error; err != nil {
			return nil, err
		}
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
	// 方案A：状态机 CAS 先行（active→released）作幂等闸，再条件解冻 held（去账户行锁）。
	res := tx.Model(&Hold{}).Where("id = ? AND status = ?", h.ID, HoldActive).
		Update("status", HoldReleased)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrHoldNotActive // 并发已结算
	}
	n, err := applyBalanceDelta(tx, h.AccountID, decimal.Zero, h.Amount.Neg(),
		"ROUND(held_amount - ?, 8) >= 0", h.Amount)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("ledger: release would drive held_amount negative on account %d (invariant violation)", h.AccountID)
	}
	return nil
}

// SweepStuckHolds 释放超时仍 Active 的 hold（审查 H-1）。
// 兜底 relay 同步结算失败（响应已发但 capture 事务失败）留下的孤儿 hold——
// 否则这类 hold 永久冻结用户余额、无人回收。按 ref_type 过滤，避免误伤 task hold
// （task 有自己的 sweepTimedOut）。返回释放数量。
//
// 注意：这是"宁可漏收不可冻死"——结算失败的那笔 usage 收不回了（journal 漂移自检会标记），
// 但用户余额必须解冻。relay 结算失败是罕见事件（DB 瞬断），漏收可接受。
func SweepStuckHolds(db *gorm.DB, refType string, olderThan time.Time, limit int) (int, error) {
	var holds []Hold
	if err := db.Where("status = ? AND ref_type = ? AND created_at < ?",
		HoldActive, refType, olderThan).Limit(limit).Find(&holds).Error; err != nil {
		return 0, err
	}
	released := 0
	for i := range holds {
		h := &holds[i]
		err := db.Transaction(func(tx *gorm.DB) error { return Release(tx, h.ID) })
		if err != nil {
			if errors.Is(err, ErrHoldNotActive) {
				continue // 已被并发结算，跳过
			}
			return released, err
		}
		released++
	}
	return released, nil
}

// AccountDrift 描述单个账户的余额漂移（CheckDrift 结果）。Stored=账户列现值，
// Derived=由 journal / 活跃 holds 重算的应然值；两者之差即漂移量。
type AccountDrift struct {
	AccountID     uint64
	OrgID         uint64
	Type          string
	PostedStored  decimal.Decimal
	PostedDerived decimal.Decimal // Σ journal.delta
	HeldStored    decimal.Decimal
	HeldDerived   decimal.Decimal // Σ holds.amount WHERE active
}

func (d AccountDrift) PostedDrift() decimal.Decimal { return d.PostedStored.Sub(d.PostedDerived) }
func (d AccountDrift) HeldDrift() decimal.Decimal   { return d.HeldStored.Sub(d.HeldDerived) }

// driftScale：对账比较精度（= 货币列 DECIMAL(20,8) 的最小单位）。差值四舍五入到 8 位后非零
// 才算漂移——MySQL DECIMAL 上是精确比较（真实漂移 ≥ 1e-8 必被检出），同时吸收 SQLite
// 测试方言 NUMERIC 以 float 存储、SQL SUM 带来的亚 1e-8 浮点噪声（不会误报）。
const driftScale = 8

// CheckDrift 只读重算两条对账恒等式（见包注 A/B），返回存在漂移的账户（空=全一致）。
// 不修复（修复需人工裁定资金归属），供 ledger-drift 后台 job 周期巡检并在发现时告警。
// 两条聚合查询 + 内存比对，按 account 分组；账户无 journal/hold 时派生值视为 0。
func CheckDrift(db *gorm.DB) ([]AccountDrift, error) {
	var drifts []AccountDrift
	// 三次读包进只读事务取同一快照（对抗评审 low）：消除持续资金写流量下「账户行 vs journal SUM
	// vs holds SUM」跨语句撕裂读导致的瞬时假阳/假阴一拍。MySQL REPEATABLE READ 下事务首读即固定快照。
	err := db.Transaction(func(tx *gorm.DB) error {
		var accounts []Account
		if err := tx.Find(&accounts).Error; err != nil {
			return err
		}
		type sumRow struct {
			AccountID uint64          `gorm:"column:account_id"`
			S         decimal.Decimal `gorm:"column:s"` // decimal.Scan 兼容 MySQL DECIMAL([]byte) 与 SQLite REAL(float64)
		}
		postedSum := map[uint64]decimal.Decimal{}
		heldSum := map[uint64]decimal.Decimal{}

		var jr []sumRow
		if err := tx.Model(&JournalEntry{}).
			Select("account_id AS account_id, COALESCE(SUM(delta),0) AS s").
			Group("account_id").Scan(&jr).Error; err != nil {
			return err
		}
		for _, r := range jr {
			postedSum[r.AccountID] = r.S
		}
		var hr []sumRow
		if err := tx.Model(&Hold{}).Where("status = ?", HoldActive).
			Select("account_id AS account_id, COALESCE(SUM(amount),0) AS s").
			Group("account_id").Scan(&hr).Error; err != nil {
			return err
		}
		for _, r := range hr {
			heldSum[r.AccountID] = r.S
		}

		for i := range accounts {
			a := &accounts[i]
			pd, hd := postedSum[a.ID], heldSum[a.ID] // 缺省零值 decimal 即 0
			if a.PostedBalance.Sub(pd).Round(driftScale).IsZero() &&
				a.HeldAmount.Sub(hd).Round(driftScale).IsZero() {
				continue
			}
			drifts = append(drifts, AccountDrift{
				AccountID: a.ID, OrgID: a.OrgID, Type: a.Type,
				PostedStored: a.PostedBalance, PostedDerived: pd,
				HeldStored: a.HeldAmount, HeldDerived: hd,
			})
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return drifts, err
}

// ---- internal ----

// LockAccount 行锁读取账户余额（导出供需要"先锁再据余额决策"的调用方，如 admin override
// 精确订正）。必须在调用方事务内调用；锁持有到事务结束，期间余额不被他人改动。
func LockAccount(tx *gorm.DB, id uint64) (*Account, error) { return lockAccount(tx, id) }

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

// applyBalanceDelta 是全代码库唯一允许写余额列的位置（红线 1 的等价路径）。
// 方案A 核心：单条条件增量 UPDATE——posted/held 用 gorm.Expr 自增自减（绝不全量覆盖，杜绝
// 读改写 TOCTOU）；guard 为附加 WHERE 守卫（守住余额不变量），空串=无附加守卫。
// 返回 rowsAffected：0 表示守卫未通过（余额不足/不变量将被破坏），由调用方据此判定。
//
// 🚨 SQLite 方言铁律（已实测）：guard 里的 decimal 参数必须折进算术表达式（如 "... - ? >= 0"）
// 而非直接比较（"... >= ?"）。因 shopspring decimal 绑定为字符串(TEXT)，SQLite 下
// numeric >= text 按存储类排序恒为假（numeric < text）→ RowsAffected 永远 0；把 ? 折进算术
// 会强制 text→numeric 数值化，MySQL/SQLite 均得正确比较。直接比较形式在 MySQL 工作、SQLite 失效，
// 是最隐蔽的护栏盲区，绝不可用。
func applyBalanceDelta(tx *gorm.DB, accID uint64, postedDelta, heldDelta decimal.Decimal, guard string, guardArgs ...any) (int64, error) {
	// ROUND(..,8) 把结果钳到货币精度（DECIMAL(20,8) 列契约）：MySQL 上对精确 DECIMAL 是恒等，
	// SQLite 上把 NUMERIC 列的 REAL(float64) 运算噪声（如 9.995→9.995000000000001）钳回 8 位，
	// 两方言存储口径一致（实为修掉旧 setBalances 时代 SQLite 存全精度、MySQL 存 8 位的隐性分叉）。
	updates := map[string]any{}
	if !postedDelta.IsZero() {
		updates["posted_balance"] = gorm.Expr("ROUND(posted_balance + ?, 8)", postedDelta)
	}
	if !heldDelta.IsZero() {
		updates["held_amount"] = gorm.Expr("ROUND(held_amount + ?, 8)", heldDelta)
	}
	if len(updates) == 0 {
		return 1, nil // 无余额变化（如 actual==0 的 capture 不动 posted）：视为成功 noop
	}
	q := tx.Model(&Account{}).Where("id = ?", accID)
	if guard != "" {
		q = q.Where(guard, guardArgs...)
	}
	res := q.Updates(updates)
	return res.RowsAffected, res.Error
}

// readPosted 回读账户 posted_balance（填 capture 的 balance_after 锚点）。
// 调用方应在已对该账户行发过写（持行锁）的事务内调用，回读即结算后真实余额。
func readPosted(tx *gorm.DB, accID uint64) (decimal.Decimal, error) {
	acc := &Account{}
	if err := tx.Select("posted_balance").First(acc, accID).Error; err != nil {
		return decimal.Zero, fmt.Errorf("ledger: account %d: %w", accID, err)
	}
	return acc.PostedBalance, nil
}

func findEntryByIdem(tx *gorm.DB, key string) (*JournalEntry, error) {
	e := &JournalEntry{}
	if err := tx.Where("idempotency_key = ?", key).First(e).Error; err != nil {
		return nil, err
	}
	return e, nil
}

func isDuplicate(err error) bool { return dberr.IsDuplicate(err) }
