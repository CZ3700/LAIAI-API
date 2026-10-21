-- Ark S2: durable video_assets
CREATE TABLE video_assets (
 asset_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, group_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 media_type VARCHAR(16) NOT NULL, name VARCHAR(64) NOT NULL, scope_id VARCHAR(64) NOT NULL,
 scope_version BIGINT UNSIGNED NOT NULL, provider_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
 status VARCHAR(16) NOT NULL, review_state VARCHAR(16) NOT NULL, source_payload_id VARCHAR(64) NOT NULL,
 immutable_object_ref VARCHAR(64) NOT NULL, object_sha256 CHAR(64) NOT NULL, mime VARCHAR(64) NOT NULL,
 byte_size BIGINT NOT NULL, duration_ms BIGINT NOT NULL, width INT NOT NULL, height INT NOT NULL,
 fps_num INT NOT NULL, fps_den INT NOT NULL, metadata_version BIGINT UNSIGNED NOT NULL, verified_at DATETIME(3) NOT NULL,
 create_operation_id VARCHAR(64) NOT NULL, safe_error_code VARCHAR(64) NOT NULL,
 row_version BIGINT UNSIGNED NOT NULL, deleted_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_asset_org(org_id,asset_id), UNIQUE KEY uk_video_asset_provider(scope_id,provider_id),
 KEY idx_video_asset_list(org_id,group_id,deleted_at,asset_id), KEY idx_video_asset_poll(status,updated_at),
 CONSTRAINT fk_video_asset_group FOREIGN KEY(org_id,group_id) REFERENCES video_asset_groups(org_id,group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
