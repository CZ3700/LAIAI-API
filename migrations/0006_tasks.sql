-- 0006_tasks.sql
-- 异步 Task API（设计 §4.4；蓝本 = NewAPI task 系统，知识库 §6.2，十陷阱内置规避）：
--   - 终态转换全部 CAS（UPDATE ... WHERE status IN (...)），防双重结算（陷阱 1）
--   - 计费快照列（price_out_usd_per_1m）在提交时落定，轮询结算不读实时价（陷阱 2）
--   - 资金走 hold→capture/release（hold_id 关联），不是 NewAPI 的 pre-consume+手工 refund
--   - 超时清扫由 PollOnce 内置（陷阱 8 的 cutoff 用可配置时长，不硬编码日期）

CREATE TABLE tasks (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    task_id             VARCHAR(40)  NOT NULL COMMENT '对外公开 id（task_<hex>）',
    org_id              BIGINT UNSIGNED NOT NULL,
    user_id             BIGINT UNSIGNED NULL,
    api_key_id          BIGINT UNSIGNED NOT NULL,
    model               VARCHAR(128) NOT NULL,
    channel_id          BIGINT UNSIGNED NOT NULL,
    provider            VARCHAR(32)  NOT NULL,
    status              VARCHAR(16)  NOT NULL COMMENT 'queued|running|succeeded|failed',
    fail_reason         VARCHAR(255) NOT NULL DEFAULT '',
    upstream_task_id    VARCHAR(128) NOT NULL DEFAULT '' COMMENT '厂商侧任务 id',
    result_url          VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '产物 URL（R2 重托管为二期）',
    hold_id             BIGINT UNSIGNED NOT NULL COMMENT '提交时冻结的 ledger hold',
    est_tokens          BIGINT       NOT NULL DEFAULT 0 COMMENT '预估输出 token（hold 依据）',
    actual_tokens       BIGINT       NOT NULL DEFAULT 0 COMMENT '上游返回的真实 completion_tokens',
    cost_usd            DECIMAL(20,8) NOT NULL DEFAULT 0 COMMENT '实收（journal 口径）',
    price_out_usd_per_1m DECIMAL(12,6) NOT NULL COMMENT '计费快照：提交时刻的输出单价',
    submitted_at        DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    started_at          DATETIME(3)  NULL,
    finished_at         DATETIME(3)  NULL,
    created_at          DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_tasks_task_id (task_id),
    KEY idx_tasks_unfinished (status, id),
    KEY idx_tasks_org (org_id, id),
    KEY idx_tasks_channel_active (channel_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 渠道任务并发闸（Seedance 公测 3 并发；超限提交直接 429 诚实背压，不本地排队）
ALTER TABLE channels ADD COLUMN max_concurrent_tasks INT NOT NULL DEFAULT 3;
