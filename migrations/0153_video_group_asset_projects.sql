-- A routing group may select one Ark account/region/project for new asset work.
-- Existing assets and task snapshots keep their original provider scope.
CREATE TABLE video_group_asset_projects (
    group_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    provider_scope_id BIGINT UNSIGNED NOT NULL,
    scope_version BIGINT UNSIGNED NOT NULL,
    revision BIGINT UNSIGNED NOT NULL,
    updated_by BIGINT UNSIGNED NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    KEY idx_video_group_asset_project_scope (provider_scope_id, scope_version),
    CONSTRAINT ck_video_group_asset_project_revision CHECK (revision > 0),
    CONSTRAINT ck_video_group_asset_project_scope CHECK (provider_scope_id > 0 AND scope_version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
