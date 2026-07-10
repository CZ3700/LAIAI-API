-- 0044_channel_owner_org.sql
-- channels 加 owner_org_id：BYOK 自带上游 Key（设计 §4.4；指令 §5 / roadmap A5）。
-- 0=平台渠道（默认，全量存量不变）；>0 = 该 org 自有渠道——选路谓词
-- `owner_org_id = 0 OR owner_org_id = 请求方 org` 在 tierForAttempt 结构性强制（隔离红线），
-- org 自有渠道构成虚拟最高优先层，失败回落平台渠道。计费 = 平台价 × byok_fee_percent。
ALTER TABLE channels
    ADD COLUMN owner_org_id BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'BYOK 归属 org，0=平台渠道（0044）',
    ADD KEY idx_channels_owner (owner_org_id);
