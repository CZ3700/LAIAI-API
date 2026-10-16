-- Ark console private asset metadata.
CREATE TABLE video_asset_details (
 resource_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, description VARCHAR(1200) NOT NULL DEFAULT '', source VARCHAR(32) NOT NULL DEFAULT '',
 last_synced_at DATETIME(3) NULL, updated_at DATETIME(3) NOT NULL,
 KEY idx_video_asset_details_org(org_id,resource_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
