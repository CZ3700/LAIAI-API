-- 0028_ledger_shards.sql
-- 账户分片（方案B，规模化）：把单 org 单账户行写热点拆成 N 个物理分片行，单 org 写吞吐随分片数扩展。
-- 默认 shard=0 单分片 → 与现状逐字一致，零回归。扩容某巨客户 = 给其设 shard_count>1 + 加 DB 容量，零代码。
--
-- 红线：分片对 ledger 三原语透明（仍按 account_id 操作）；恒等式 A/B 与 CheckDrift 按 account_id 分组天然成立；
-- 幂等键 UNIQUE(idempotency_key) 全表不含 shard，重放无论落哪片都命中首次结果。
--
-- migrate:ack-destructive 改唯一键 (org_id,type)→(org_id,type,shard) 必须先 DROP 旧唯一索引（破坏性但必要）。
-- runbook：上线前备份 ledger_accounts；DROP/ADD 唯一索引在 InnoDB 是在线 DDL（不锁表长时间），但仍须在
-- 低峰执行并验证无重复 (org_id,type,shard)；现存行 shard 默认 0，新唯一键与旧 (org_id,type) 在单分片下等价。

-- ledger_accounts 加 shard 列；唯一键改为三元 (org_id,type,shard)。现存账户 shard 默认 0（=主分片）。
ALTER TABLE ledger_accounts ADD COLUMN shard INT NOT NULL DEFAULT 0 COMMENT '账户分片号（0=主分片；规模化时 >0 分散写热点）';
ALTER TABLE ledger_accounts DROP INDEX uk_account_org_type;
ALTER TABLE ledger_accounts ADD UNIQUE KEY uk_account_org_type_shard (org_id, type, shard);

-- 分片数配置（缺省视为 1）。仅给真正触顶单账户行写吞吐的巨客户设 >1。
CREATE TABLE ledger_account_shards (
    org_id      BIGINT UNSIGNED NOT NULL,
    type        VARCHAR(32)  NOT NULL COMMENT 'user_credit|...',
    shard_count INT          NOT NULL DEFAULT 1 COMMENT '该 (org,type) 的物理分片行数',
    updated_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (org_id, type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
