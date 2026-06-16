package ledger

import (
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 账户分片的「规模化调配」层（设计 §scale）：给单账户行写吞吐触顶的巨客户把 (org,type) 拆成 N 个
// 物理分片行，hold 按 RequestID 散列打散写热点（见 CreateHoldSharded）。本文件提供：
//   ① SetShardConfig —— 运营按 org 配置分片数（"只升级配置即扩容"）；
//   ② RebalanceOrgShards / RebalanceAllShardedOrgs —— 周期把各片余额摊平，防「充值入主片、消费打散」
//      导致非主片饿死（饿死后 hold 反复回退主片→分片退化为入金单点）。
// 未配置(shard_count=1，今日全部 org)时这些都是 no-op，与现状逐字一致——零回归。

// SetShardConfig 设置 (org,type) 物理分片数（count<1 视为 1）。upsert ledger_account_shards。
// 扩容(增加分片)即时生效；缩容前应先 Rebalance/确认高 shard 资金可被搬回——OrgBalances 按 (org,type)
// SUM 不漏钱，但 hold 选片范围缩小后高 shard 余额不再被新 hold 选中，需搬回低片才能继续动用。
func SetShardConfig(db *gorm.DB, orgID uint64, accType string, count int) error {
	if orgID == 0 || accType == "" {
		return fmt.Errorf("%w: orgID/accType required", ErrInvalidInput)
	}
	if count < 1 {
		count = 1
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "org_id"}, {Name: "type"}},
		DoUpdates: clause.AssignmentColumns([]string{"shard_count"}),
	}).Create(&AccountShardConfig{OrgID: orgID, Type: accType, ShardCount: count}).Error
}

// ShardedOrg：一条 shard_count>1 的配置。
type ShardedOrg struct {
	OrgID      uint64 `gorm:"column:org_id"`
	Type       string `gorm:"column:type"`
	ShardCount int    `gorm:"column:shard_count"`
}

// ListShardedOrgs 返回所有 shard_count>1 的 (org,type)（rebalancer 驱动遍历用）。
func ListShardedOrgs(db *gorm.DB) ([]ShardedOrg, error) {
	var rows []ShardedOrg
	err := db.Model(&AccountShardConfig{}).
		Select("org_id, type, shard_count").
		Where("shard_count > 1").Scan(&rows).Error
	return rows, err
}

// RebalanceOrgShards 把 (org,type) 各分片 posted 余额摊平到均值。单事务内：锁所有分片行 → 算均值 target →
// 富片(posted>target)按**可用额(posted-held)**上限搬给穷片(posted<target)。成对过账 net=0
// （RebalanceShards），恒等式 A（Σjournal.delta 守恒）严格保住——只挪不增减总额。
//
// idemKey 含 runID+orgID+accType+from->to，全局唯一（ledger_journal.idempotency_key 是全表唯一键）：
// 同一次运行同一 (org,type,from,to) 至多搬一次；下一次运行 runID 变 → 重新按当时倾斜搬动。
// 返回实际搬动笔数。N≤1（未分片）直接返回 0。
func RebalanceOrgShards(db *gorm.DB, orgID uint64, accType, runID string) (int, error) {
	moves := 0
	err := db.Transaction(func(tx *gorm.DB) error {
		n, e := shardCountFor(tx, orgID, accType)
		if e != nil || n <= 1 {
			return e
		}
		// 锁所有分片行（MySQL FOR UPDATE），读 posted/held；缺的分片补建（posted=0）以纳入摊平目标。
		q := tx
		if tx.Dialector.Name() == "mysql" {
			q = tx.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var accs []Account
		if e := q.Where("org_id = ? AND type = ?", orgID, accType).Order("shard").Find(&accs).Error; e != nil {
			return e
		}
		have := map[int]bool{}
		for i := range accs {
			have[accs[i].Shard] = true
		}
		for s := 0; s < n; s++ {
			if !have[s] {
				a, ce := ensureAccountAt(tx, orgID, accType, s)
				if ce != nil {
					return ce
				}
				accs = append(accs, *a)
			}
		}
		total := decimal.Zero
		for i := range accs {
			total = total.Add(accs[i].PostedBalance)
		}
		target := total.DivRound(decimal.NewFromInt(int64(n)), 8) // 均值（8 位精度；只作摊平目标，总额由成对过账守恒）
		type slot struct {
			shard int
			amt   decimal.Decimal // 富片=可搬出额；穷片=待补额（均为正）
		}
		var donors, receivers []slot
		for i := range accs {
			a := accs[i]
			diff := a.PostedBalance.Sub(target)
			if diff.IsPositive() {
				movable := decimal.Min(diff, a.Available()) // 不搬走 held 占用的（held 是已冻结，红线不可挪）
				if movable.IsPositive() {
					donors = append(donors, slot{a.Shard, movable})
				}
			} else if diff.IsNegative() {
				receivers = append(receivers, slot{a.Shard, diff.Neg()})
			}
		}
		di := 0
		for ri := range receivers {
			need := receivers[ri].amt
			for need.IsPositive() && di < len(donors) {
				give := decimal.Min(need, donors[di].amt)
				if give.IsPositive() {
					idem := fmt.Sprintf("%s:%d:%s:%d-%d", runID, orgID, accType, donors[di].shard, receivers[ri].shard)
					if re := RebalanceShards(tx, orgID, accType, donors[di].shard, receivers[ri].shard, give, idem); re != nil {
						return re
					}
					moves++
					need = need.Sub(give)
					donors[di].amt = donors[di].amt.Sub(give)
				}
				if !donors[di].amt.IsPositive() {
					di++
				}
			}
		}
		return nil
	})
	return moves, err
}

// RebalanceAllShardedOrgs 遍历所有 shard_count>1 的配置逐个摊平。单 org 失败不阻断其余（记下末次错误返回，
// 由调用方告警，下轮重试）。runID 由调用方传当次运行标识（如时间戳）。返回 (摊平过的 org 数, 总搬动笔数)。
func RebalanceAllShardedOrgs(db *gorm.DB, runID string) (orgs, moves int, err error) {
	cfgs, e := ListShardedOrgs(db)
	if e != nil {
		return 0, 0, e
	}
	for _, cfg := range cfgs {
		m, re := RebalanceOrgShards(db, cfg.OrgID, cfg.Type, runID)
		if re != nil {
			err = re
			continue
		}
		if m > 0 {
			orgs++
			moves += m
		}
	}
	return orgs, moves, err
}
