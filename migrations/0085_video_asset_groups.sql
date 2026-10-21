-- Ark S2: durable video_asset_groups
CREATE TABLE video_asset_groups (
 group_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, kind VARCHAR(16) NOT NULL, name VARCHAR(128) NOT NULL,
 scope_id VARCHAR(64) NOT NULL, scope_version BIGINT UNSIGNED NOT NULL,
 account_id VARCHAR(64) NOT NULL, region VARCHAR(32) NOT NULL, project VARCHAR(64) NOT NULL,
 provider_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
 status VARCHAR(16) NOT NULL, review_state VARCHAR(16) NOT NULL,
 portrait_id VARCHAR(64) NOT NULL, create_operation_id VARCHAR(64) NOT NULL,
 delete_operation_id VARCHAR(64) NOT NULL, row_version BIGINT UNSIGNED NOT NULL,
 deleted_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_group_org(org_id,group_id),
 UNIQUE KEY uk_video_group_provider(scope_id,provider_id),
 KEY idx_video_group_list(org_id,deleted_at,group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
