-- migrate:ack-destructive: drop unused pre-tkeapi routing_groups tables and align api_keys with tkeapi tokens
-- 清理老旧的 routing_groups 体系，并按 tkeapi 补齐令牌多维配额/限流/HA 列

DROP TABLE IF EXISTS routing_policy_events;
DROP TABLE IF EXISTS org_group_grants;
DROP TABLE IF EXISTS channel_group_bindings;
DROP TABLE IF EXISTS routing_groups;

ALTER TABLE api_keys
    DROP COLUMN IF EXISTS routing_group_id,
    DROP COLUMN IF EXISTS model_access_mode;

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS kid VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS rps_limit INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS high_availability SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS daily_quota_limit NUMERIC(20,8) NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS daily_quota_used NUMERIC(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS weekly_quota_limit NUMERIC(20,8) NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS weekly_quota_used NUMERIC(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS monthly_quota_limit NUMERIC(20,8) NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS monthly_quota_used NUMERIC(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_reset_day VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_reset_week VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_reset_month VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;
