-- Ark S2: durable video_asset_tenant_locks
CREATE TABLE video_asset_tenant_locks (
 org_id BIGINT UNSIGNED PRIMARY KEY, version BIGINT UNSIGNED NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
