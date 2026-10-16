-- Ark S2: durable video_asset_events
CREATE TABLE video_asset_events (
 event_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, resource_type VARCHAR(32) NOT NULL, resource_id VARCHAR(64) NOT NULL,
 operation_id VARCHAR(64) NOT NULL, kind VARCHAR(64) NOT NULL, evidence_ref VARCHAR(64) NOT NULL, actor_id VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL, KEY idx_video_asset_events(org_id,resource_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
