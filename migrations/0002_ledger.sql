-- 0002_ledger.sql
-- Ledger 资金事实源（设计文档 §4.5）：余额是 journal 的派生视图，永不直接 UPDATE。
-- 红线：所有资金动作必须在事务内 + journal 同写；idempotency_key UNIQUE 兜底幂等。
-- DECIMAL(20,8)：覆盖 USDC 6 位小数；内部单位 USD 单币种。

CREATE TABLE ledger_accounts (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    org_id          BIGINT UNSIGNED NOT NULL,
    type            VARCHAR(32)  NOT NULL COMMENT 'user_credit|user_commission|org_margin|platform_revenue',
    posted_balance  DECIMAL(20,8) NOT NULL DEFAULT 0 COMMENT '已过账余额，仅由 journal 派生更新',
    held_amount     DECIMAL(20,8) NOT NULL DEFAULT 0 COMMENT '活跃冻结总额；available = posted - held',
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_account_org_type (org_id, type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- append-only。禁止 UPDATE/DELETE（审计红线，同旧平台 ext_transfer_logs 纪律）。
CREATE TABLE ledger_journal (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    idempotency_key VARCHAR(128) NOT NULL,
    account_id      BIGINT UNSIGNED NOT NULL,
    kind            VARCHAR(16)  NOT NULL COMMENT 'credit|debit|capture|grant|expire',
    delta           DECIMAL(20,8) NOT NULL COMMENT '有符号变动',
    balance_after   DECIMAL(20,8) NOT NULL COMMENT '过账后 posted_balance 尽力快照（展示/审计用，非对账依据）。方案A 乐观并发下跨事务并发对同一账户过账时，本列可能锚定旧快照值；对账以 delta 求和为准（CheckDrift 恒等式 A：posted==Σdelta，不依赖本列）。capture 路径回读精确，post 路径尽力。',
    hold_id         BIGINT UNSIGNED NULL COMMENT 'capture 来源 hold',
    ref_type        VARCHAR(32)  NOT NULL DEFAULT '' COMMENT 'order|usage|withdrawal|commission|...',
    ref_id          VARCHAR(64)  NOT NULL DEFAULT '',
    reason          VARCHAR(255) NOT NULL DEFAULT '',
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_journal_idem (idempotency_key),
    KEY idx_journal_account (account_id, id),
    KEY idx_journal_ref (ref_type, ref_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE ledger_holds (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    idempotency_key VARCHAR(128) NOT NULL,
    account_id      BIGINT UNSIGNED NOT NULL,
    amount          DECIMAL(20,8) NOT NULL COMMENT '冻结金额（正数）',
    captured_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    status          TINYINT      NOT NULL DEFAULT 0 COMMENT '0=active 1=captured 2=released',
    ref_type        VARCHAR(32)  NOT NULL DEFAULT '',
    ref_id          VARCHAR(64)  NOT NULL DEFAULT '',
    expires_at      DATETIME(3)  NULL COMMENT '超时自动 release 的回收 job 依据',
    created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_hold_idem (idempotency_key),
    KEY idx_holds_account_status (account_id, status),
    KEY idx_holds_expires (status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
