-- Ark console private asset metadata.
CREATE TABLE video_asset_discoveries (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 provider_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 kind VARCHAR(16) NOT NULL, name VARCHAR(256) NOT NULL DEFAULT '', description VARCHAR(1200) NOT NULL DEFAULT '',
 status VARCHAR(16) NOT NULL, assigned_org BIGINT UNSIGNED NOT NULL DEFAULT 0, group_id VARCHAR(64) NOT NULL DEFAULT '',
 version BIGINT UNSIGNED NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_asset_discovery(scope_id,provider_id), KEY idx_video_asset_discovery_scope(scope_id,status,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
