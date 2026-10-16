-- Ark S2: durable video_task_asset_refs
CREATE TABLE video_task_asset_refs (
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 asset_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 group_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 content_index INT NOT NULL, role VARCHAR(64) NOT NULL, asset_metadata_version BIGINT UNSIGNED NOT NULL,
 authorization_version BIGINT UNSIGNED NOT NULL, released_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(org_id,task_id,content_index), KEY idx_video_asset_refs(org_id,asset_id,released_at),
 KEY idx_video_group_refs(org_id,group_id,released_at),
 CONSTRAINT fk_video_ref_asset FOREIGN KEY(org_id,asset_id) REFERENCES video_assets(org_id,asset_id),
 CONSTRAINT fk_video_ref_group FOREIGN KEY(org_id,group_id) REFERENCES video_asset_groups(org_id,group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
