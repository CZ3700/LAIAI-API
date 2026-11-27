-- 0006_maintenance_and_relay_pg.sql: invocations_archive cold archive table, error cleanup partial indexes, api_keys.tpm_limit, channels.thinking_to_content

CREATE TABLE IF NOT EXISTS invocations_archive (
    id BIGINT PRIMARY KEY,
    org_id BIGINT NOT NULL,
    user_id BIGINT,
    api_key_id BIGINT NOT NULL DEFAULT 0,
    request_id VARCHAR(128) NOT NULL UNIQUE,
    model VARCHAR(255) NOT NULL,
    modality VARCHAR(64) NOT NULL DEFAULT '',
    channel_id BIGINT NOT NULL DEFAULT 0,
    provider VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(64) NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    is_stream BOOLEAN NOT NULL DEFAULT FALSE,
    attempts INTEGER NOT NULL DEFAULT 0,
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
    usage_source VARCHAR(32) NOT NULL DEFAULT '',
    frt_ms INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    upstream_request_id VARCHAR(255) NOT NULL DEFAULT '',
    error_brief TEXT NOT NULL DEFAULT '',
    payload_object_key TEXT NOT NULL DEFAULT '',
    finish_reason VARCHAR(64) NOT NULL DEFAULT '',
    protocol VARCHAR(64) NOT NULL DEFAULT '',
    cached_tokens BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens BIGINT NOT NULL DEFAULT 0,
    upstream_error_code VARCHAR(128) NOT NULL DEFAULT '',
    client_ip VARCHAR(64) NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    referer TEXT NOT NULL DEFAULT '',
    forward_rule_eid VARCHAR(128) NOT NULL DEFAULT '',
    billing_rule_pid VARCHAR(128) NOT NULL DEFAULT '',
    upstream_cost_ratio NUMERIC(12,6) NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_inv_archive_org_created
    ON invocations_archive (org_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_inv_archive_created
    ON invocations_archive (created_at);
CREATE INDEX IF NOT EXISTS idx_inv_archive_err_cleanup
    ON invocations_archive (created_at, status_code) WHERE status_code NOT IN (200, 0);

CREATE INDEX IF NOT EXISTS idx_invocations_err_cleanup
    ON invocations (created_at, status_code) WHERE status_code NOT IN (200, 0);

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS tpm_limit INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channels ADD COLUMN IF NOT EXISTS thinking_to_content SMALLINT NOT NULL DEFAULT 0;
