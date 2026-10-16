-- Ark console private asset metadata.
CREATE TABLE video_asset_sync_jobs (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 mode VARCHAR(16) NOT NULL, kind VARCHAR(16) NOT NULL, phase VARCHAR(16) NOT NULL, status VARCHAR(16) NOT NULL,
 group_id VARCHAR(64) NOT NULL DEFAULT '', after_group_id VARCHAR(64) NOT NULL DEFAULT '', checkpoint_id VARCHAR(64) NOT NULL DEFAULT '',
 seen_hashes MEDIUMTEXT NOT NULL, idempotency_hash CHAR(64) NOT NULL,
 pages BIGINT NOT NULL DEFAULT 0, items BIGINT NOT NULL DEFAULT 0, fence BIGINT UNSIGNED NOT NULL DEFAULT 0,
 lease_until DATETIME(3) NULL, error_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_asset_sync_idem(org_id,idempotency_hash), KEY idx_video_asset_sync_work(status,lease_until), KEY idx_video_asset_sync_scope(scope_id,status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
