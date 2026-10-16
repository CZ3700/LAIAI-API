CREATE TABLE video_asset_org_routes (
 org_id BIGINT UNSIGNED NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 scope_version BIGINT UNSIGNED NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 default_route BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(org_id,provider_scope_id),
 KEY idx_video_asset_org_route_default(org_id,enabled,default_route)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
